// Package app implements the PoundDeck application controller and live deck execution engine.
package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/mrpoundsign/PoundDeck/pkg/deck"
	"github.com/mrpoundsign/PoundDeck/pkg/plugin"
)

// SlotVisual represents the live rendered appearance of a keypad slot or encoder dial.
type SlotVisual struct {
	Title    string         `json:"title"`
	Subtitle string         `json:"subtitle,omitempty"`
	Color    string         `json:"color,omitempty"`
	ImageURI string         `json:"imageUri,omitempty"`
	State    int            `json:"state,omitempty"`
	Feedback map[string]any `json:"feedback,omitempty"`
	Flash    string         `json:"flash,omitempty"`
}

// RenderedSlot pairs a persisted SlotConfig with its live SlotVisual and resolved ActionDescriptor.
type RenderedSlot struct {
	Config   *deck.SlotConfig         `json:"config,omitempty"`
	Visual   SlotVisual               `json:"visual"`
	Action   *plugin.ActionDescriptor `json:"action,omitempty"`
	AutoBack bool                     `json:"autoBack,omitempty"`
}

// ProfileSummary describes one configured deck profile for headers and selectors.
type ProfileSummary struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Grid      deck.GridSize `json:"grid"`
	Dials     int           `json:"dials"`
	PageCount int           `json:"pageCount"`
}

// Snapshot is an immutable point-in-time view of the active deck state for UI and remote surfaces.
type Snapshot struct {
	Profiles          []ProfileSummary     `json:"profiles"`
	ActiveProfileID   string               `json:"activeProfileId"`
	ActiveProfileName string               `json:"activeProfileName"`
	Grid              deck.GridSize        `json:"grid"`
	Dials             int                  `json:"dials"`
	PageIndex         int                  `json:"pageIndex"`
	PageCount         int                  `json:"pageCount"`
	PageName          string               `json:"pageName"`
	FolderPath        []string             `json:"folderPath,omitempty"`
	InFolder          bool                 `json:"inFolder"`
	Slots             map[string]RenderedSlot `json:"slots"`
	DialSlots         map[int]RenderedSlot    `json:"dialSlots"`
	PluginCount       int                  `json:"pluginCount"`
	ActionCount       int                  `json:"actionCount"`
}

// Engine coordinates deck configuration, persistence, navigation, plugin lifecycle, and live state updates.
type Engine struct {
	opMu     sync.Mutex
	mu       sync.RWMutex
	store    deck.Store
	registry *plugin.Registry
	cfg      *deck.Config

	pageIdx     int
	folderStack []string
	visuals     map[string]SlotVisual
	logs        []string

	subs   map[uint64]func(Snapshot)
	subSeq uint64
}

// NewEngine loads configuration from store, binds host callbacks onto reg, and emits initial WillAppear events.
func NewEngine(store deck.Store, reg *plugin.Registry) (*Engine, error) {
	if store == nil {
		return nil, errors.New("deck store is required")
	}
	if reg == nil {
		reg = plugin.NewRegistry()
	}

	cfg, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("load deck config: %w", err)
	}
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid deck config: %w", err)
	}

	eng := &Engine{
		store:    store,
		registry: reg,
		cfg:      cfg,
		visuals:  make(map[string]SlotVisual),
		subs:     make(map[uint64]func(Snapshot)),
	}
	reg.SetHost(&engineHost{eng: eng})

	eng.mu.RLock()
	appear := eng.visibleEventsLocked(plugin.EventWillAppear)
	eng.mu.RUnlock()

	eng.dispatchAll(context.Background(), appear)
	return eng, nil
}

// Registry returns the plugin registry attached to the engine.
func (e *Engine) Registry() *plugin.Registry {
	return e.registry
}

// Close shuts down the plugin registry and clears active subscribers.
func (e *Engine) Close() error {
	e.mu.Lock()
	clear(e.subs)
	e.mu.Unlock()
	if e.registry != nil {
		return e.registry.Close()
	}
	return nil
}

// Subscribe registers fn to receive Snapshot updates whenever deck state or visuals change.
func (e *Engine) Subscribe(fn func(Snapshot)) func() {
	if fn == nil {
		return func() {}
	}
	e.mu.Lock()
	e.subSeq++
	id := e.subSeq
	e.subs[id] = fn
	e.mu.Unlock()

	return func() {
		e.mu.Lock()
		delete(e.subs, id)
		e.mu.Unlock()
	}
}

// Snapshot returns a deep, thread-safe snapshot of the current active deck state.
func (e *Engine) Snapshot() Snapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.snapshotLocked()
}

func (e *Engine) snapshotLocked() Snapshot {
	prof := e.activeProfileLocked()
	summaries := make([]ProfileSummary, len(e.cfg.Profiles))
	for i, p := range e.cfg.Profiles {
		summaries[i] = ProfileSummary{
			ID:        p.ID,
			Name:      p.Name,
			Grid:      p.Grid,
			Dials:     p.Dials,
			PageCount: len(p.Pages),
		}
	}

	slotsMap, dialsMap, folderNames := e.activeContainerLocked(prof)
	inFolder := len(folderNames) > 0
	pageIdx := min(max(e.pageIdx, 0), max(0, len(prof.Pages)-1))
	pageName := ""
	if len(prof.Pages) > 0 {
		pageName = prof.Pages[pageIdx].Name
	}

	renderedSlots := make(map[string]RenderedSlot, prof.Grid.TotalSlots())
	for row := range prof.Grid.Rows {
		for col := range prof.Grid.Cols {
			c := deck.Coordinates{Row: row, Col: col}
			key := c.SlotKey()
			slot := slotsMap[key]
			if slot != nil {
				renderedSlots[key] = e.renderSlotLocked(slot)
				continue
			}
			if inFolder && row == 0 && col == 0 {
				renderedSlots[key] = RenderedSlot{
					AutoBack: true,
					Visual: SlotVisual{
						Title:    "← Back",
						Subtitle: "Up",
						Color:    "#94A3B8",
					},
				}
			}
		}
	}

	renderedDials := make(map[int]RenderedSlot, prof.Dials)
	for i := range prof.Dials {
		if slot := dialsMap[i]; slot != nil {
			renderedDials[i] = e.renderSlotLocked(slot)
		}
	}

	return Snapshot{
		Profiles:          summaries,
		ActiveProfileID:   prof.ID,
		ActiveProfileName: prof.Name,
		Grid:              prof.Grid,
		Dials:             prof.Dials,
		PageIndex:         pageIdx,
		PageCount:         len(prof.Pages),
		PageName:          pageName,
		FolderPath:        folderNames,
		InFolder:          inFolder,
		Slots:             renderedSlots,
		DialSlots:         renderedDials,
		PluginCount:       len(e.registry.Providers()),
		ActionCount:       len(e.registry.Actions()),
	}
}

func (e *Engine) renderSlotLocked(slot *deck.SlotConfig) RenderedSlot {
	cp := slot.Clone()
	var descPtr *plugin.ActionDescriptor
	if desc, ok := e.registry.Action(cp.ActionUUID); ok {
		descCopy := desc
		descPtr = &descCopy
	}

	title := cp.Title
	if title == "" && descPtr != nil {
		if descPtr.DefaultTitle != "" {
			title = descPtr.DefaultTitle
		} else {
			title = descPtr.Name
		}
	}
	if title == "" {
		title = cp.ActionUUID
	}

	col := cp.Color
	if col == "" && descPtr != nil {
		col = descPtr.DefaultColor
	}

	vis := SlotVisual{
		Title: title,
		Color: col,
		State: cp.State,
	}
	if live, ok := e.visuals[cp.Context]; ok {
		if live.Title != "" {
			vis.Title = live.Title
		}
		if live.Subtitle != "" {
			vis.Subtitle = live.Subtitle
		}
		if live.Color != "" {
			vis.Color = live.Color
		}
		if live.ImageURI != "" {
			vis.ImageURI = live.ImageURI
		}
		if len(live.Feedback) > 0 {
			vis.Feedback = maps.Clone(live.Feedback)
		}
		if live.Flash != "" {
			vis.Flash = live.Flash
		}
		vis.State = live.State
	}

	return RenderedSlot{
		Config: cp,
		Visual: vis,
		Action: descPtr,
	}
}

func (e *Engine) notifySubscribers() {
	e.mu.RLock()
	snap := e.snapshotLocked()
	cbs := make([]func(Snapshot), 0, len(e.subs))
	for _, fn := range e.subs {
		cbs = append(cbs, fn)
	}
	e.mu.RUnlock()

	for _, fn := range cbs {
		fn(snap)
	}
}

func (e *Engine) activeProfileLocked() *deck.Profile {
	for _, p := range e.cfg.Profiles {
		if p.ID == e.cfg.ActiveProfileID {
			return p
		}
	}
	return e.cfg.Profiles[0]
}

func (e *Engine) activeContainerLocked(prof *deck.Profile) (map[string]*deck.SlotConfig, map[int]*deck.SlotConfig, []string) {
	if len(e.folderStack) > 0 {
		names := make([]string, 0, len(e.folderStack))
		var top *deck.Folder
		for _, fid := range e.folderStack {
			f, ok := prof.Folders[fid]
			if !ok || f == nil {
				break
			}
			names = append(names, f.Name)
			top = f
		}
		if top != nil {
			return top.Slots, top.Dials, names
		}
	}
	idx := min(max(e.pageIdx, 0), max(0, len(prof.Pages)-1))
	pg := prof.Pages[idx]
	return pg.Slots, pg.Dials, nil
}

func (e *Engine) visibleEventsLocked(evType plugin.EventType) []plugin.ActionEvent {
	prof := e.activeProfileLocked()
	slotsMap, dialsMap, _ := e.activeContainerLocked(prof)
	events := make([]plugin.ActionEvent, 0, prof.Grid.TotalSlots()+prof.Dials)

	for row := range prof.Grid.Rows {
		for col := range prof.Grid.Cols {
			c := deck.Coordinates{Row: row, Col: col}
			slot := slotsMap[c.SlotKey()]
			if slot == nil {
				continue
			}
			events = append(events, plugin.ActionEvent{
				Type:        evType,
				Context:     slot.Context,
				ActionUUID:  slot.ActionUUID,
				Controller:  deck.ControllerKeypad,
				Coordinates: c,
				Settings:    cloneSettings(slot.Settings),
				State:       slot.State,
			})
		}
	}

	for i := range prof.Dials {
		slot := dialsMap[i]
		if slot == nil {
			continue
		}
		events = append(events, plugin.ActionEvent{
			Type:       evType,
			Context:    slot.Context,
			ActionUUID: slot.ActionUUID,
			Controller: deck.ControllerEncoder,
			DialIndex:  i,
			Settings:   cloneSettings(slot.Settings),
			State:      slot.State,
		})
	}
	return events
}

func (e *Engine) dispatchAll(ctx context.Context, events []plugin.ActionEvent) {
	for _, ev := range events {
		_ = e.registry.Dispatch(ctx, ev)
	}
}

// CreateProfile creates and activates a new profile with a single initial page.
func (e *Engine) CreateProfile(name string, grid deck.GridSize, dials int) (*deck.Profile, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return nil, errors.New("profile name cannot be empty")
	}
	if err := grid.Validate(); err != nil {
		return nil, err
	}
	if dials < 0 || dials > 16 {
		return nil, fmt.Errorf("dials count %d out of range [0, 16]", dials)
	}

	e.opMu.Lock()
	e.mu.Lock()
	disappear := e.visibleEventsLocked(plugin.EventWillDisappear)

	prof := &deck.Profile{
		ID:    deck.NewUUID(),
		Name:  trimmed,
		Grid:  grid,
		Dials: dials,
		Pages: []*deck.Page{
			{
				ID:    deck.NewUUID(),
				Name:  "Page 1",
				Slots: make(map[string]*deck.SlotConfig),
				Dials: make(map[int]*deck.SlotConfig),
			},
		},
		Folders: make(map[string]*deck.Folder),
	}
	e.cfg.Profiles = append(e.cfg.Profiles, prof)
	e.cfg.ActiveProfileID = prof.ID
	e.pageIdx = 0
	e.folderStack = nil
	appear := e.visibleEventsLocked(plugin.EventWillAppear)
	cfgCopy := e.cfg.Clone()
	profCopy := prof.Clone()
	e.mu.Unlock()

	err := e.store.Save(cfgCopy)
	e.opMu.Unlock()
	if err != nil {
		return nil, err
	}

	e.dispatchAll(context.Background(), disappear)
	e.dispatchAll(context.Background(), appear)
	e.notifySubscribers()
	return profCopy, nil
}

// UpdateProfileGrid updates the grid dimensions and dial count for profileID non-destructively.
func (e *Engine) UpdateProfileGrid(profileID string, grid deck.GridSize, dials int) error {
	if err := grid.Validate(); err != nil {
		return err
	}
	if dials < 0 || dials > 16 {
		return fmt.Errorf("dials count %d out of range [0, 16]", dials)
	}

	e.opMu.Lock()
	e.mu.Lock()
	var target *deck.Profile
	for _, p := range e.cfg.Profiles {
		if p.ID == profileID {
			target = p
			break
		}
	}
	if target == nil {
		e.mu.Unlock()
		e.opMu.Unlock()
		return fmt.Errorf("profile %q not found", profileID)
	}
	disappear := e.visibleEventsLocked(plugin.EventWillDisappear)
	target.Grid = grid
	target.Dials = dials
	appear := e.visibleEventsLocked(plugin.EventWillAppear)
	cfgCopy := e.cfg.Clone()
	e.mu.Unlock()

	err := e.store.Save(cfgCopy)
	e.opMu.Unlock()
	if err != nil {
		return err
	}

	e.dispatchAll(context.Background(), disappear)
	e.dispatchAll(context.Background(), appear)
	e.notifySubscribers()
	return nil
}

// DeleteProfile deletes profileID unless it is the last remaining profile.
func (e *Engine) DeleteProfile(profileID string) error {
	e.opMu.Lock()
	e.mu.Lock()
	if len(e.cfg.Profiles) <= 1 {
		e.mu.Unlock()
		e.opMu.Unlock()
		return errors.New("cannot delete the last remaining profile")
	}

	idx := -1
	for i, p := range e.cfg.Profiles {
		if p.ID == profileID {
			idx = i
			break
		}
	}
	if idx < 0 {
		e.mu.Unlock()
		e.opMu.Unlock()
		return fmt.Errorf("profile %q not found", profileID)
	}

	disappear := e.visibleEventsLocked(plugin.EventWillDisappear)
	e.cfg.Profiles = slices.Delete(e.cfg.Profiles, idx, idx+1)
	if e.cfg.ActiveProfileID == profileID {
		e.cfg.ActiveProfileID = e.cfg.Profiles[0].ID
		e.pageIdx = 0
		e.folderStack = nil
	}
	appear := e.visibleEventsLocked(plugin.EventWillAppear)
	cfgCopy := e.cfg.Clone()
	e.mu.Unlock()

	err := e.store.Save(cfgCopy)
	e.opMu.Unlock()
	if err != nil {
		return err
	}

	e.dispatchAll(context.Background(), disappear)
	e.dispatchAll(context.Background(), appear)
	e.notifySubscribers()
	return nil
}

// SwitchProfile switches the active profile by ID or case-insensitive Name.
func (e *Engine) SwitchProfile(profileRef string) error {
	ref := strings.TrimSpace(profileRef)
	if ref == "" {
		return errors.New("profile reference cannot be empty")
	}

	e.opMu.Lock()
	e.mu.Lock()
	var target *deck.Profile
	for _, p := range e.cfg.Profiles {
		if p.ID == ref || strings.EqualFold(p.Name, ref) {
			target = p
			break
		}
	}
	if target == nil {
		e.mu.Unlock()
		e.opMu.Unlock()
		return fmt.Errorf("profile %q not found", ref)
	}
	if e.cfg.ActiveProfileID == target.ID && len(e.folderStack) == 0 && e.pageIdx == 0 {
		e.mu.Unlock()
		e.opMu.Unlock()
		return nil
	}

	disappear := e.visibleEventsLocked(plugin.EventWillDisappear)
	e.cfg.ActiveProfileID = target.ID
	e.pageIdx = 0
	e.folderStack = nil
	appear := e.visibleEventsLocked(plugin.EventWillAppear)
	cfgCopy := e.cfg.Clone()
	e.mu.Unlock()

	err := e.store.Save(cfgCopy)
	e.opMu.Unlock()
	if err != nil {
		return err
	}

	e.dispatchAll(context.Background(), disappear)
	e.dispatchAll(context.Background(), appear)
	e.notifySubscribers()
	return nil
}

// AddPage appends a new page to the active profile, switches to it, and returns its 0-based index.
func (e *Engine) AddPage(name string) (int, error) {
	e.opMu.Lock()
	e.mu.Lock()
	prof := e.activeProfileLocked()
	disappear := e.visibleEventsLocked(plugin.EventWillDisappear)
	pageName := strings.TrimSpace(name)
	if pageName == "" {
		pageName = fmt.Sprintf("Page %d", len(prof.Pages)+1)
	}
	prof.Pages = append(prof.Pages, &deck.Page{
		ID:    deck.NewUUID(),
		Name:  pageName,
		Slots: make(map[string]*deck.SlotConfig),
		Dials: make(map[int]*deck.SlotConfig),
	})
	e.pageIdx = len(prof.Pages) - 1
	e.folderStack = nil
	appear := e.visibleEventsLocked(plugin.EventWillAppear)
	newIdx := e.pageIdx
	cfgCopy := e.cfg.Clone()
	e.mu.Unlock()

	err := e.store.Save(cfgCopy)
	e.opMu.Unlock()
	if err != nil {
		return 0, err
	}

	e.dispatchAll(context.Background(), disappear)
	e.dispatchAll(context.Background(), appear)
	e.notifySubscribers()
	return newIdx, nil
}

// DeletePage removes pageIndex from the active profile unless it is the last remaining page.
func (e *Engine) DeletePage(pageIndex int) error {
	e.opMu.Lock()
	e.mu.Lock()
	prof := e.activeProfileLocked()
	if len(prof.Pages) <= 1 {
		e.mu.Unlock()
		e.opMu.Unlock()
		return errors.New("cannot delete the last remaining page")
	}
	if pageIndex < 0 || pageIndex >= len(prof.Pages) {
		e.mu.Unlock()
		e.opMu.Unlock()
		return fmt.Errorf("page index %d out of range", pageIndex)
	}

	disappear := e.visibleEventsLocked(plugin.EventWillDisappear)
	prof.Pages = slices.Delete(prof.Pages, pageIndex, pageIndex+1)
	if e.pageIdx >= len(prof.Pages) {
		e.pageIdx = len(prof.Pages) - 1
	}
	e.folderStack = nil
	appear := e.visibleEventsLocked(plugin.EventWillAppear)
	cfgCopy := e.cfg.Clone()
	e.mu.Unlock()

	err := e.store.Save(cfgCopy)
	e.opMu.Unlock()
	if err != nil {
		return err
	}

	e.dispatchAll(context.Background(), disappear)
	e.dispatchAll(context.Background(), appear)
	e.notifySubscribers()
	return nil
}

// NextPage advances to the next page in the active profile (exiting any open folder).
func (e *Engine) NextPage(wrap bool) error {
	return e.stepPage(1, wrap)
}

// PrevPage moves to the previous page in the active profile (exiting any open folder).
func (e *Engine) PrevPage(wrap bool) error {
	return e.stepPage(-1, wrap)
}

func (e *Engine) stepPage(delta int, wrap bool) error {
	e.opMu.Lock()
	e.mu.Lock()
	prof := e.activeProfileLocked()
	count := len(prof.Pages)
	if count == 0 {
		e.mu.Unlock()
		e.opMu.Unlock()
		return nil
	}
	next := e.pageIdx + delta
	if wrap {
		next = ((next % count) + count) % count
	} else {
		next = min(max(next, 0), count-1)
	}
	if next == e.pageIdx && len(e.folderStack) == 0 {
		e.mu.Unlock()
		e.opMu.Unlock()
		return nil
	}
	disappear := e.visibleEventsLocked(plugin.EventWillDisappear)
	e.pageIdx = next
	e.folderStack = nil
	appear := e.visibleEventsLocked(plugin.EventWillAppear)
	e.mu.Unlock()
	e.opMu.Unlock()

	e.dispatchAll(context.Background(), disappear)
	e.dispatchAll(context.Background(), appear)
	e.notifySubscribers()
	return nil
}

// GoToPage navigates to a 1-based page number in the active profile.
func (e *Engine) GoToPage(oneBasedPage int) error {
	e.opMu.Lock()
	e.mu.Lock()
	prof := e.activeProfileLocked()
	if oneBasedPage < 1 || oneBasedPage > len(prof.Pages) {
		e.mu.Unlock()
		e.opMu.Unlock()
		return fmt.Errorf("page %d out of range [1, %d]", oneBasedPage, len(prof.Pages))
	}
	target := oneBasedPage - 1
	if target == e.pageIdx && len(e.folderStack) == 0 {
		e.mu.Unlock()
		e.opMu.Unlock()
		return nil
	}
	disappear := e.visibleEventsLocked(plugin.EventWillDisappear)
	e.pageIdx = target
	e.folderStack = nil
	appear := e.visibleEventsLocked(plugin.EventWillAppear)
	e.mu.Unlock()
	e.opMu.Unlock()

	e.dispatchAll(context.Background(), disappear)
	e.dispatchAll(context.Background(), appear)
	e.notifySubscribers()
	return nil
}

// OpenFolder pushes folderID onto the active folder stack, creating the folder container if needed.
func (e *Engine) OpenFolder(folderID, folderName string) error {
	fid := strings.TrimSpace(folderID)
	if fid == "" {
		return errors.New("folder ID cannot be empty")
	}
	name := strings.TrimSpace(folderName)
	if name == "" {
		name = "Folder"
	}

	e.opMu.Lock()
	e.mu.Lock()
	prof := e.activeProfileLocked()
	disappear := e.visibleEventsLocked(plugin.EventWillDisappear)
	createdOrUpdated := false
	f, exists := prof.Folders[fid]
	if !exists || f == nil {
		f = &deck.Folder{
			ID:    fid,
			Name:  name,
			Slots: make(map[string]*deck.SlotConfig),
			Dials: make(map[int]*deck.SlotConfig),
		}
		prof.Folders[fid] = f
		createdOrUpdated = true
	} else if f.Name != name {
		f.Name = name
		createdOrUpdated = true
	}
	e.folderStack = append(e.folderStack, fid)
	appear := e.visibleEventsLocked(plugin.EventWillAppear)
	var cfgCopy *deck.Config
	if createdOrUpdated {
		cfgCopy = e.cfg.Clone()
	}
	e.mu.Unlock()

	var saveErr error
	if cfgCopy != nil {
		saveErr = e.store.Save(cfgCopy)
	}
	e.opMu.Unlock()
	if saveErr != nil {
		return saveErr
	}

	e.dispatchAll(context.Background(), disappear)
	e.dispatchAll(context.Background(), appear)
	e.notifySubscribers()
	return nil
}

// CloseFolder pops the top folder from the active folder stack, returning to the parent folder or page.
func (e *Engine) CloseFolder() error {
	e.opMu.Lock()
	e.mu.Lock()
	if len(e.folderStack) == 0 {
		e.mu.Unlock()
		e.opMu.Unlock()
		return nil
	}
	disappear := e.visibleEventsLocked(plugin.EventWillDisappear)
	e.folderStack = e.folderStack[:len(e.folderStack)-1]
	appear := e.visibleEventsLocked(plugin.EventWillAppear)
	e.mu.Unlock()
	e.opMu.Unlock()

	e.dispatchAll(context.Background(), disappear)
	e.dispatchAll(context.Background(), appear)
	e.notifySubscribers()
	return nil
}

// AssignKeypadAction binds actionUUID to keypad slot c in the active page or folder.
func (e *Engine) AssignKeypadAction(c deck.Coordinates, actionUUID string) (*deck.SlotConfig, error) {
	desc, ok := e.registry.Action(actionUUID)
	if !ok {
		return nil, fmt.Errorf("unknown action %q", actionUUID)
	}
	if !desc.SupportsController(deck.ControllerKeypad) {
		return nil, fmt.Errorf("action %q does not support Keypad slots", desc.Name)
	}

	e.opMu.Lock()
	e.mu.Lock()
	prof := e.activeProfileLocked()
	if !prof.Grid.Contains(c) {
		e.mu.Unlock()
		e.opMu.Unlock()
		return nil, fmt.Errorf("slot %s out of bounds for %dx%d grid", c.SlotKey(), prof.Grid.Cols, prof.Grid.Rows)
	}
	slotsMap, _, _ := e.activeContainerLocked(prof)
	var oldSlot *deck.SlotConfig
	if existing := slotsMap[c.SlotKey()]; existing != nil {
		oldSlot = existing.Clone()
		delete(e.visuals, existing.Context)
	}

	slot := buildDefaultSlotConfig(desc)
	slotsMap[c.SlotKey()] = slot
	slotCopy := slot.Clone()
	cfgCopy := e.cfg.Clone()
	e.mu.Unlock()

	err := e.store.Save(cfgCopy)
	e.opMu.Unlock()
	if err != nil {
		return nil, err
	}

	if oldSlot != nil {
		_ = e.registry.Dispatch(context.Background(), plugin.ActionEvent{
			Type:        plugin.EventWillDisappear,
			Context:     oldSlot.Context,
			ActionUUID:  oldSlot.ActionUUID,
			Controller:  deck.ControllerKeypad,
			Coordinates: c,
			Settings:    cloneSettings(oldSlot.Settings),
			State:       oldSlot.State,
		})
	}
	_ = e.registry.Dispatch(context.Background(), plugin.ActionEvent{
		Type:        plugin.EventWillAppear,
		Context:     slotCopy.Context,
		ActionUUID:  slotCopy.ActionUUID,
		Controller:  deck.ControllerKeypad,
		Coordinates: c,
		Settings:    cloneSettings(slotCopy.Settings),
		State:       slotCopy.State,
	})
	e.notifySubscribers()
	return slotCopy, nil
}

// AssignDialAction binds actionUUID to encoder dialIndex in the active page or folder.
func (e *Engine) AssignDialAction(dialIndex int, actionUUID string) (*deck.SlotConfig, error) {
	desc, ok := e.registry.Action(actionUUID)
	if !ok {
		return nil, fmt.Errorf("unknown action %q", actionUUID)
	}
	if !desc.SupportsController(deck.ControllerEncoder) {
		return nil, fmt.Errorf("action %q does not support Encoder dials", desc.Name)
	}

	e.opMu.Lock()
	e.mu.Lock()
	prof := e.activeProfileLocked()
	if dialIndex < 0 || dialIndex >= prof.Dials {
		e.mu.Unlock()
		e.opMu.Unlock()
		return nil, fmt.Errorf("dial index %d out of range [0, %d)", dialIndex, prof.Dials)
	}
	_, dialsMap, _ := e.activeContainerLocked(prof)
	var oldSlot *deck.SlotConfig
	if existing := dialsMap[dialIndex]; existing != nil {
		oldSlot = existing.Clone()
		delete(e.visuals, existing.Context)
	}

	slot := buildDefaultSlotConfig(desc)
	dialsMap[dialIndex] = slot
	slotCopy := slot.Clone()
	cfgCopy := e.cfg.Clone()
	e.mu.Unlock()

	err := e.store.Save(cfgCopy)
	e.opMu.Unlock()
	if err != nil {
		return nil, err
	}

	if oldSlot != nil {
		_ = e.registry.Dispatch(context.Background(), plugin.ActionEvent{
			Type:       plugin.EventWillDisappear,
			Context:    oldSlot.Context,
			ActionUUID: oldSlot.ActionUUID,
			Controller: deck.ControllerEncoder,
			DialIndex:  dialIndex,
			Settings:   cloneSettings(oldSlot.Settings),
			State:      oldSlot.State,
		})
	}
	_ = e.registry.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventWillAppear,
		Context:    slotCopy.Context,
		ActionUUID: slotCopy.ActionUUID,
		Controller: deck.ControllerEncoder,
		DialIndex:  dialIndex,
		Settings:   cloneSettings(slotCopy.Settings),
		State:      slotCopy.State,
	})
	e.notifySubscribers()
	return slotCopy, nil
}

// UpdateKeypadSlot persists title, color, and settings changes for the action at c.
func (e *Engine) UpdateKeypadSlot(c deck.Coordinates, title, color string, settings map[string]any) error {
	e.opMu.Lock()
	e.mu.Lock()
	prof := e.activeProfileLocked()
	slotsMap, _, _ := e.activeContainerLocked(prof)
	slot := slotsMap[c.SlotKey()]
	if slot == nil {
		e.mu.Unlock()
		e.opMu.Unlock()
		return fmt.Errorf("no action assigned at slot %s", c.SlotKey())
	}
	slot.Title = title
	slot.Color = color
	if settings != nil {
		slot.Settings = cloneSettings(settings)
	}
	slotCopy := slot.Clone()
	cfgCopy := e.cfg.Clone()
	e.mu.Unlock()

	err := e.store.Save(cfgCopy)
	e.opMu.Unlock()
	if err != nil {
		return err
	}

	_ = e.registry.Dispatch(context.Background(), plugin.ActionEvent{
		Type:        plugin.EventSettingsChanged,
		Context:     slotCopy.Context,
		ActionUUID:  slotCopy.ActionUUID,
		Controller:  deck.ControllerKeypad,
		Coordinates: c,
		Settings:    cloneSettings(slotCopy.Settings),
		State:       slotCopy.State,
	})
	e.notifySubscribers()
	return nil
}

// UpdateDialSlot persists title, color, and settings changes for the action on dialIndex.
func (e *Engine) UpdateDialSlot(dialIndex int, title, color string, settings map[string]any) error {
	e.opMu.Lock()
	e.mu.Lock()
	prof := e.activeProfileLocked()
	_, dialsMap, _ := e.activeContainerLocked(prof)
	slot := dialsMap[dialIndex]
	if slot == nil {
		e.mu.Unlock()
		e.opMu.Unlock()
		return fmt.Errorf("no action assigned at dial %d", dialIndex)
	}
	slot.Title = title
	slot.Color = color
	if settings != nil {
		slot.Settings = cloneSettings(settings)
	}
	slotCopy := slot.Clone()
	cfgCopy := e.cfg.Clone()
	e.mu.Unlock()

	err := e.store.Save(cfgCopy)
	e.opMu.Unlock()
	if err != nil {
		return err
	}

	_ = e.registry.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventSettingsChanged,
		Context:    slotCopy.Context,
		ActionUUID: slotCopy.ActionUUID,
		Controller: deck.ControllerEncoder,
		DialIndex:  dialIndex,
		Settings:   cloneSettings(slotCopy.Settings),
		State:      slotCopy.State,
	})
	e.notifySubscribers()
	return nil
}

// ClearKeypadSlot removes any action bound to keypad slot c.
func (e *Engine) ClearKeypadSlot(c deck.Coordinates) error {
	e.opMu.Lock()
	e.mu.Lock()
	prof := e.activeProfileLocked()
	slotsMap, _, _ := e.activeContainerLocked(prof)
	existing := slotsMap[c.SlotKey()]
	if existing == nil {
		e.mu.Unlock()
		e.opMu.Unlock()
		return nil
	}
	oldSlot := existing.Clone()
	delete(slotsMap, c.SlotKey())
	delete(e.visuals, existing.Context)
	cfgCopy := e.cfg.Clone()
	e.mu.Unlock()

	err := e.store.Save(cfgCopy)
	e.opMu.Unlock()
	if err != nil {
		return err
	}

	_ = e.registry.Dispatch(context.Background(), plugin.ActionEvent{
		Type:        plugin.EventWillDisappear,
		Context:     oldSlot.Context,
		ActionUUID:  oldSlot.ActionUUID,
		Controller:  deck.ControllerKeypad,
		Coordinates: c,
		Settings:    cloneSettings(oldSlot.Settings),
		State:       oldSlot.State,
	})
	e.notifySubscribers()
	return nil
}

// ClearDialSlot removes any action bound to encoder dialIndex.
func (e *Engine) ClearDialSlot(dialIndex int) error {
	e.opMu.Lock()
	e.mu.Lock()
	prof := e.activeProfileLocked()
	_, dialsMap, _ := e.activeContainerLocked(prof)
	existing := dialsMap[dialIndex]
	if existing == nil {
		e.mu.Unlock()
		e.opMu.Unlock()
		return nil
	}
	oldSlot := existing.Clone()
	delete(dialsMap, dialIndex)
	delete(e.visuals, existing.Context)
	cfgCopy := e.cfg.Clone()
	e.mu.Unlock()

	err := e.store.Save(cfgCopy)
	e.opMu.Unlock()
	if err != nil {
		return err
	}

	_ = e.registry.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventWillDisappear,
		Context:    oldSlot.Context,
		ActionUUID: oldSlot.ActionUUID,
		Controller: deck.ControllerEncoder,
		DialIndex:  dialIndex,
		Settings:   cloneSettings(oldSlot.Settings),
		State:      oldSlot.State,
	})
	e.notifySubscribers()
	return nil
}

// PressKey triggers KeyDown for the slot at c, or navigates back when pressing AutoBack inside a folder.
func (e *Engine) PressKey(ctx context.Context, c deck.Coordinates) error {
	e.mu.RLock()
	prof := e.activeProfileLocked()
	if !prof.Grid.Contains(c) {
		e.mu.RUnlock()
		return fmt.Errorf("slot %s out of bounds", c.SlotKey())
	}
	slotsMap, _, folderNames := e.activeContainerLocked(prof)
	slot := slotsMap[c.SlotKey()]
	inFolder := len(folderNames) > 0
	var ev plugin.ActionEvent
	if slot != nil {
		ev = plugin.ActionEvent{
			Type:        plugin.EventKeyDown,
			Context:     slot.Context,
			ActionUUID:  slot.ActionUUID,
			Controller:  deck.ControllerKeypad,
			Coordinates: c,
			Settings:    cloneSettings(slot.Settings),
			State:       slot.State,
		}
	}
	e.mu.RUnlock()

	if slot == nil {
		if inFolder && c.Row == 0 && c.Col == 0 {
			return e.CloseFolder()
		}
		return nil
	}

	return e.registry.Dispatch(ctx, ev)
}

// ReleaseKey dispatches EventKeyUp for the action at c, if any.
func (e *Engine) ReleaseKey(ctx context.Context, c deck.Coordinates) error {
	e.mu.RLock()
	prof := e.activeProfileLocked()
	if !prof.Grid.Contains(c) {
		e.mu.RUnlock()
		return fmt.Errorf("slot %s out of bounds", c.SlotKey())
	}
	slotsMap, _, _ := e.activeContainerLocked(prof)
	slot := slotsMap[c.SlotKey()]
	if slot == nil {
		e.mu.RUnlock()
		return nil
	}
	ev := plugin.ActionEvent{
		Type:        plugin.EventKeyUp,
		Context:     slot.Context,
		ActionUUID:  slot.ActionUUID,
		Controller:  deck.ControllerKeypad,
		Coordinates: c,
		Settings:    cloneSettings(slot.Settings),
		State:       slot.State,
	}
	e.mu.RUnlock()

	return e.registry.Dispatch(ctx, ev)
}

// RotateDial dispatches EventDialRotate with ticks delta to the action bound to dialIndex.
func (e *Engine) RotateDial(ctx context.Context, dialIndex, ticks int) error {
	if ticks == 0 {
		return nil
	}
	e.mu.RLock()
	prof := e.activeProfileLocked()
	if dialIndex < 0 || dialIndex >= prof.Dials {
		e.mu.RUnlock()
		return fmt.Errorf("dial index %d out of range", dialIndex)
	}
	_, dialsMap, _ := e.activeContainerLocked(prof)
	slot := dialsMap[dialIndex]
	if slot == nil {
		e.mu.RUnlock()
		return nil
	}
	ev := plugin.ActionEvent{
		Type:       plugin.EventDialRotate,
		Context:    slot.Context,
		ActionUUID: slot.ActionUUID,
		Controller: deck.ControllerEncoder,
		DialIndex:  dialIndex,
		Settings:   cloneSettings(slot.Settings),
		State:      slot.State,
		Ticks:      ticks,
	}
	e.mu.RUnlock()

	return e.registry.Dispatch(ctx, ev)
}

// PressDial dispatches EventDialPress to the action bound to dialIndex.
func (e *Engine) PressDial(ctx context.Context, dialIndex int) error {
	e.mu.RLock()
	prof := e.activeProfileLocked()
	if dialIndex < 0 || dialIndex >= prof.Dials {
		e.mu.RUnlock()
		return fmt.Errorf("dial index %d out of range", dialIndex)
	}
	_, dialsMap, _ := e.activeContainerLocked(prof)
	slot := dialsMap[dialIndex]
	if slot == nil {
		e.mu.RUnlock()
		return nil
	}
	ev := plugin.ActionEvent{
		Type:       plugin.EventDialPress,
		Context:    slot.Context,
		ActionUUID: slot.ActionUUID,
		Controller: deck.ControllerEncoder,
		DialIndex:  dialIndex,
		Settings:   cloneSettings(slot.Settings),
		State:      slot.State,
	}
	e.mu.RUnlock()

	return e.registry.Dispatch(ctx, ev)
}

// TriggerAction executes an arbitrary action with settings (used by Multi-Action steps and Inspector tests).
func (e *Engine) TriggerAction(ctx context.Context, actionUUID string, settings map[string]any) error {
	return e.registry.Dispatch(ctx, plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    deck.NewUUID(),
		ActionUUID: actionUUID,
		Controller: deck.ControllerKeypad,
		Settings:   cloneSettings(settings),
	})
}

// DynamicOptions queries registered providers for live inspector choices matching optionsKey.
func (e *Engine) DynamicOptions(ctx context.Context, optionsKey string, currentSettings map[string]any) []plugin.SelectOption {
	if e.registry == nil {
		return nil
	}
	return e.registry.DynamicOptions(ctx, optionsKey, currentSettings)
}

// GlobalSettings returns a cloned copy of persisted plugin-wide settings for pluginUUID.
func (e *Engine) GlobalSettings(pluginUUID string) map[string]any {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.cfg == nil || e.cfg.PluginSettings == nil {
		return make(map[string]any)
	}
	return cloneSettings(e.cfg.PluginSettings[pluginUUID])
}

// SaveGlobalSettings persists plugin-wide settings for pluginUUID atomically.
func (e *Engine) SaveGlobalSettings(pluginUUID string, settings map[string]any) error {
	pid := strings.TrimSpace(pluginUUID)
	if pid == "" {
		return errors.New("plugin UUID cannot be empty")
	}

	e.opMu.Lock()
	e.mu.Lock()
	if e.cfg.PluginSettings == nil {
		e.cfg.PluginSettings = make(map[string]map[string]any)
	}
	e.cfg.PluginSettings[pid] = cloneSettings(settings)
	cfgCopy := e.cfg.Clone()
	e.mu.Unlock()

	err := e.store.Save(cfgCopy)
	e.opMu.Unlock()
	if err != nil {
		return err
	}
	e.notifySubscribers()
	return nil
}

func (e *Engine) updateVisual(ctxID string, patch plugin.VisualPatch) {
	e.mu.Lock()
	cur := e.visuals[ctxID]
	if patch.Title != nil {
		cur.Title = *patch.Title
	}
	if patch.Subtitle != nil {
		cur.Subtitle = *patch.Subtitle
	}
	if patch.Color != nil {
		cur.Color = *patch.Color
	}
	if patch.ImageURI != nil {
		cur.ImageURI = *patch.ImageURI
	}
	if patch.State != nil {
		cur.State = *patch.State
	}
	if patch.Feedback != nil {
		cur.Feedback = maps.Clone(patch.Feedback)
	}
	cur.Flash = patch.Flash
	e.visuals[ctxID] = cur
	e.mu.Unlock()

	e.notifySubscribers()
}

func (e *Engine) saveContextSettings(ctxID string, settings map[string]any) error {
	e.opMu.Lock()
	e.mu.Lock()
	updated := false
	for _, prof := range e.cfg.Profiles {
		for _, pg := range prof.Pages {
			if updateContainerSlotSettings(pg.Slots, pg.Dials, ctxID, settings) {
				updated = true
			}
		}
		for _, f := range prof.Folders {
			if updateContainerSlotSettings(f.Slots, f.Dials, ctxID, settings) {
				updated = true
			}
		}
	}
	var cfgCopy *deck.Config
	if updated {
		cfgCopy = e.cfg.Clone()
	}
	e.mu.Unlock()

	if cfgCopy == nil {
		e.opMu.Unlock()
		return fmt.Errorf("slot context %q not found", ctxID)
	}
	err := e.store.Save(cfgCopy)
	e.opMu.Unlock()
	if err != nil {
		return err
	}
	e.notifySubscribers()
	return nil
}

func updateContainerSlotSettings(slots map[string]*deck.SlotConfig, dials map[int]*deck.SlotConfig, ctxID string, settings map[string]any) bool {
	found := false
	for _, s := range slots {
		if s != nil && s.Context == ctxID {
			s.Settings = cloneSettings(settings)
			found = true
		}
	}
	for _, s := range dials {
		if s != nil && s.Context == ctxID {
			s.Settings = cloneSettings(settings)
			found = true
		}
	}
	return found
}

func buildDefaultSlotConfig(desc plugin.ActionDescriptor) *deck.SlotConfig {
	title := desc.DefaultTitle
	if title == "" {
		title = desc.Name
	}
	settings := make(map[string]any, len(desc.SettingsSchema))
	for _, field := range desc.SettingsSchema {
		if field.Default != nil {
			settings[field.Key] = field.Default
		}
	}
	return &deck.SlotConfig{
		Context:    deck.NewUUID(),
		ActionUUID: desc.UUID,
		Title:      title,
		Color:      desc.DefaultColor,
		Settings:   settings,
	}
}

func cloneSettings(src map[string]any) map[string]any {
	if len(src) == 0 {
		return make(map[string]any)
	}
	return maps.Clone(src)
}

type engineHost struct {
	eng *Engine
}

func (h *engineHost) UpdateVisual(ctxID string, patch plugin.VisualPatch) {
	h.eng.updateVisual(ctxID, patch)
}

func (h *engineHost) SaveSettings(ctxID string, settings map[string]any) error {
	return h.eng.saveContextSettings(ctxID, settings)
}

func (h *engineHost) GetGlobalSettings(pluginUUID string) map[string]any {
	return h.eng.GlobalSettings(pluginUUID)
}

func (h *engineHost) SaveGlobalSettings(pluginUUID string, settings map[string]any) error {
	return h.eng.SaveGlobalSettings(pluginUUID, settings)
}

func (h *engineHost) Log(pluginUUID, message string) {
	h.eng.mu.Lock()
	h.eng.logs = append(h.eng.logs, fmt.Sprintf("[%s] %s", pluginUUID, message))
	if len(h.eng.logs) > 200 {
		h.eng.logs = h.eng.logs[len(h.eng.logs)-200:]
	}
	h.eng.mu.Unlock()
}

func (h *engineHost) Deck() plugin.DeckControl {
	return h.eng
}
