// Package app implements the PoundControl application controller and live deck execution engine.
package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/mrpoundsign/PoundControl/pkg/deck"
	"github.com/mrpoundsign/PoundControl/pkg/plugin"
	"github.com/mrpoundsign/PoundControl/pkg/secrets"
)

// SlotVisual represents the live rendered appearance of a keypad slot or encoder dial.
type SlotVisual struct {
	Title           string         `json:"title"`
	Subtitle        string         `json:"subtitle,omitempty"`
	Color           string         `json:"color,omitempty"`
	BackgroundColor string         `json:"backgroundColor,omitempty"`
	BackgroundImage string         `json:"backgroundImage,omitempty"`
	ImageURI        string         `json:"imageUri,omitempty"`
	State           int            `json:"state,omitempty"`
	Feedback        map[string]any `json:"feedback,omitempty"`
	Flash           string         `json:"flash,omitempty"`
}

// RenderedSlot pairs a persisted SlotConfig with its live SlotVisual and resolved ActionDescriptor.
type RenderedSlot struct {
	Config      *deck.SlotConfig         `json:"config,omitempty"`
	Visual      SlotVisual               `json:"visual"`
	Action      *plugin.ActionDescriptor `json:"action,omitempty"`
	AutoBack    bool                     `json:"autoBack,omitempty"`
	Disabled    bool                     `json:"disabled,omitempty"`
	Unavailable bool                     `json:"unavailable,omitempty"`
	PluginUUID  string                   `json:"pluginUuid,omitempty"`
	PluginName  string                   `json:"pluginName,omitempty"`
}

// ProfileSummary describes one configured deck profile for headers and selectors.
type ProfileSummary struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Grid      deck.GridSize `json:"grid"`
	Dials     int           `json:"dials"`
	PageCount int           `json:"pageCount"`
}

// FolderSummary describes a configured sub-deck folder in the active profile.
type FolderSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Snapshot is an immutable point-in-time view of the active deck state for UI and remote surfaces.
type Snapshot struct {
	Profiles          []ProfileSummary        `json:"profiles"`
	ActiveProfileID   string                  `json:"activeProfileId"`
	ActiveProfileName string                  `json:"activeProfileName"`
	Grid              deck.GridSize           `json:"grid"`
	Dials             int                     `json:"dials"`
	PageIndex         int                     `json:"pageIndex"`
	PageCount         int                     `json:"pageCount"`
	PageName          string                  `json:"pageName"`
	PageNames         []string                `json:"pageNames,omitempty"`
	FolderPath        []string                `json:"folderPath,omitempty"`
	ActiveFolderID    string                  `json:"activeFolderId,omitempty"`
	Folders           []FolderSummary         `json:"folders,omitempty"`
	InFolder          bool                    `json:"inFolder"`
	Slots             map[string]RenderedSlot `json:"slots"`
	DialSlots         map[int]RenderedSlot    `json:"dialSlots"`
	ColorPalette    []deck.ColorSwatch `json:"colorPalette,omitempty"`
	DisabledPlugins []string           `json:"disabledPlugins,omitempty"`
	PluginCount     int                `json:"pluginCount"`
	ActionCount       int                     `json:"actionCount"`
	CanUndo           bool                    `json:"canUndo"`
	CanRedo           bool                    `json:"canRedo"`
	UndoLabel         string                  `json:"undoLabel,omitempty"`
	RedoLabel         string                  `json:"redoLabel,omitempty"`
	HistoryRevision   uint64                  `json:"historyRevision"`
}

const (
	maxHistoryEntries    = 50
	actionFolderOpenUUID = "com.github.mrpoundsign.poundcontrol.core.folder.open"
)

type historyEntry struct {
	label       string
	cfg         *deck.Config
	pageIdx     int
	folderStack []string
}

type liveSlotVisual struct {
	vis      SlotVisual
	hasTitle bool
}

// Engine coordinates deck configuration, persistence, navigation, plugin lifecycle, and live state updates.
type Engine struct {
	opMu        sync.Mutex
	mu          sync.RWMutex
	store       deck.Store
	secretStore secrets.Store
	registry    *plugin.Registry
	cfg         *deck.Config

	pageIdx     int
	folderStack []string
	visuals     map[string]liveSlotVisual
	logs        []string

	undoStack  []historyEntry
	redoStack  []historyEntry
	historyRev uint64

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
		store:       store,
		secretStore: secrets.NewMemoryStore(),
		registry:    reg,
		cfg:         cfg,
		visuals:     make(map[string]liveSlotVisual),
		subs:        make(map[uint64]func(Snapshot)),
	}
	reg.SetHost(&engineHost{eng: eng})
	reg.SetDisabledPlugins(cfg.DisabledPlugins)
	for _, pid := range reg.DisabledPlugins() {
		_ = reg.NotifyPluginEnabled(context.Background(), pid, false)
	}

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

// SetSecretStore configures the secrets.Store used for keyring-backed plugin global password settings.
// Passing nil resets the store to an isolated in-memory store.
func (e *Engine) SetSecretStore(store secrets.Store) {
	if store == nil {
		store = secrets.NewMemoryStore()
	}
	e.mu.Lock()
	e.secretStore = store
	e.mu.Unlock()
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
	var activeFolderID string
	if inFolder && len(e.folderStack) > 0 {
		idx := min(len(folderNames), len(e.folderStack)) - 1
		activeFolderID = e.folderStack[idx]
	}

	pageIdx := min(max(e.pageIdx, 0), max(0, len(prof.Pages)-1))
	pageNames := make([]string, len(prof.Pages))
	for i, pg := range prof.Pages {
		name := strings.TrimSpace(pg.Name)
		if name == "" {
			name = "Page " + strconv.Itoa(i+1)
		}
		pageNames[i] = name
	}
	pageName := ""
	if len(pageNames) > 0 {
		pageName = pageNames[pageIdx]
	}

	folders := make([]FolderSummary, 0, len(prof.Folders))
	for fid, f := range prof.Folders {
		if f == nil {
			continue
		}
		name := strings.TrimSpace(f.Name)
		if name == "" {
			name = "Folder"
		}
		folders = append(folders, FolderSummary{ID: fid, Name: name})
	}
	slices.SortFunc(folders, func(a, b FolderSummary) int {
		if c := cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})

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

	var undoLabel, redoLabel string
	if n := len(e.undoStack); n > 0 {
		undoLabel = e.undoStack[n-1].label
	}
	if n := len(e.redoStack); n > 0 {
		redoLabel = e.redoStack[n-1].label
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
		PageNames:         pageNames,
		FolderPath:        folderNames,
		ActiveFolderID:    activeFolderID,
		Folders:           folders,
		InFolder:          inFolder,
		Slots:             renderedSlots,
		DialSlots:       renderedDials,
		ColorPalette:    slices.Clone(e.cfg.ColorPalette),
		DisabledPlugins: slices.Clone(e.cfg.DisabledPlugins),
		PluginCount:     len(e.registry.Providers()),
		ActionCount:       len(e.registry.Actions()),
		CanUndo:           len(e.undoStack) > 0,
		CanRedo:           len(e.redoStack) > 0,
		UndoLabel:         undoLabel,
		RedoLabel:         redoLabel,
		HistoryRevision:   e.historyRev,
	}
}

func (e *Engine) renderSlotLocked(slot *deck.SlotConfig) RenderedSlot {
	cp := slot.Clone()
	var descPtr *plugin.ActionDescriptor
	if desc, ok := e.registry.Action(cp.ActionUUID); ok {
		descCopy := desc
		descPtr = &descCopy
	}

	var (
		disabledPlugin    bool
		unavailablePlugin bool
		pluginUUID        string
		pluginName        string
	)
	if descPtr != nil {
		pluginUUID = descPtr.PluginUUID
	} else if m, known := e.registry.PluginForAction(cp.ActionUUID); known && !e.registry.IsPluginEnabled(m.UUID) {
		disabledPlugin = true
		pluginUUID = m.UUID
		pluginName = m.Name
	} else {
		unavailablePlugin = true
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
		Title:           title,
		Color:           col,
		BackgroundColor: cp.BackgroundColor,
		BackgroundImage: cp.BackgroundImage,
		State:           cp.State,
	}
	switch {
	case disabledPlugin:
		vis.Subtitle = "DISABLED"
		vis.Color = "#64748B"
		vis.BackgroundColor = "#14161D"
		vis.BackgroundImage = ""
		vis.State = 0
		vis.Feedback = map[string]any{"value": "DISABLED"}
	case unavailablePlugin:
		vis.Subtitle = "MISSING PLUGIN"
		vis.Color = "#F59E0B"
		vis.State = 0
		vis.Feedback = map[string]any{"value": "MISSING"}
	default:
		live, hasLive := e.visuals[cp.Context]
		if hasLive {
			if live.hasTitle {
				vis.Title = live.vis.Title
			}
			if live.vis.Subtitle != "" {
				vis.Subtitle = live.vis.Subtitle
			}
			if live.vis.Color != "" {
				vis.Color = live.vis.Color
			}
			if live.vis.BackgroundColor != "" {
				vis.BackgroundColor = live.vis.BackgroundColor
				vis.BackgroundImage = ""
			}
			if live.vis.ImageURI != "" {
				vis.ImageURI = live.vis.ImageURI
			}
			if len(live.vis.Feedback) > 0 {
				vis.Feedback = maps.Clone(live.vis.Feedback)
			}
			if live.vis.Flash != "" {
				vis.Flash = live.vis.Flash
			}
			vis.State = live.vis.State
		}
		if vis.ImageURI == "" && descPtr != nil && descPtr.DefaultImage != "" {
			vis.ImageURI = descPtr.DefaultImage
		}
		if cp.HideTitle {
			vis.Title = ""
		} else if descPtr != nil && descPtr.HideTitle && (!hasLive || !live.hasTitle) && vis.ImageURI != "" &&
			(cp.Title == descPtr.DefaultTitle || cp.Title == descPtr.Name) {
			vis.Title = ""
		}
	}

	if cp.HideTitle && (disabledPlugin || unavailablePlugin) {
		vis.Title = ""
	}

	return RenderedSlot{
		Config:      cp,
		Visual:      vis,
		Action:      descPtr,
		Disabled:    disabledPlugin,
		Unavailable: unavailablePlugin,
		PluginUUID:  pluginUUID,
		PluginName:  pluginName,
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
	return e.collectVisibleEventsLocked(evType, "")
}

func (e *Engine) visibleEventsForPluginLocked(evType plugin.EventType, pluginUUID string) []plugin.ActionEvent {
	return e.collectVisibleEventsLocked(evType, strings.TrimSpace(pluginUUID))
}

func (e *Engine) collectVisibleEventsLocked(evType plugin.EventType, filterPluginUUID string) []plugin.ActionEvent {
	prof := e.activeProfileLocked()
	slotsMap, dialsMap, _ := e.activeContainerLocked(prof)
	events := make([]plugin.ActionEvent, 0, prof.Grid.TotalSlots()+prof.Dials)

	matchSlot := func(actionUUID string) bool {
		if e.registry == nil {
			return filterPluginUUID == ""
		}
		m, known := e.registry.PluginForAction(actionUUID)
		if filterPluginUUID != "" {
			return known && m.UUID == filterPluginUUID
		}
		return !known || e.registry.IsPluginEnabled(m.UUID)
	}

	for row := range prof.Grid.Rows {
		for col := range prof.Grid.Cols {
			c := deck.Coordinates{Row: row, Col: col}
			slot := slotsMap[c.SlotKey()]
			if slot == nil || !matchSlot(slot.ActionUUID) {
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
		if slot == nil || !matchSlot(slot.ActionUUID) {
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

func (e *Engine) clearPluginVisualsLocked(pluginUUID string) {
	if e.cfg == nil || e.registry == nil {
		return
	}
	clearSlotMap := func(m map[string]*deck.SlotConfig) {
		for _, slot := range m {
			if slot == nil {
				continue
			}
			if mf, ok := e.registry.PluginForAction(slot.ActionUUID); ok && mf.UUID == pluginUUID {
				delete(e.visuals, slot.Context)
			}
		}
	}
	clearDialMap := func(m map[int]*deck.SlotConfig) {
		for _, slot := range m {
			if slot == nil {
				continue
			}
			if mf, ok := e.registry.PluginForAction(slot.ActionUUID); ok && mf.UUID == pluginUUID {
				delete(e.visuals, slot.Context)
			}
		}
	}
	for _, prof := range e.cfg.Profiles {
		if prof == nil {
			continue
		}
		for _, pg := range prof.Pages {
			if pg == nil {
				continue
			}
			clearSlotMap(pg.Slots)
			clearDialMap(pg.Dials)
		}
		for _, f := range prof.Folders {
			if f == nil {
				continue
			}
			clearSlotMap(f.Slots)
			clearDialMap(f.Dials)
		}
	}
}

func (e *Engine) dispatchAll(ctx context.Context, events []plugin.ActionEvent) {
	for _, ev := range events {
		_ = e.registry.Dispatch(ctx, ev)
	}
}

func (e *Engine) captureHistoryLocked(label string) historyEntry {
	return historyEntry{
		label:       label,
		cfg:         e.cfg.Clone(),
		pageIdx:     e.pageIdx,
		folderStack: slices.Clone(e.folderStack),
	}
}

func pushBoundedHistory(stack []historyEntry, entry historyEntry) []historyEntry {
	stack = append(stack, entry)
	if len(stack) > maxHistoryEntries {
		stack = slices.Delete(stack, 0, len(stack)-maxHistoryEntries)
	}
	return stack
}

func (e *Engine) pushUndoLocked(entry historyEntry) {
	e.undoStack = pushBoundedHistory(e.undoStack, entry)
	e.redoStack = nil
}

func (e *Engine) restoreStateLocked(entry historyEntry) {
	restored := entry.cfg.Clone()
	restored.Normalize()
	e.restoreNormalizedStateLocked(restored, entry)
}

func (e *Engine) restoreNormalizedStateLocked(restored *deck.Config, entry historyEntry) {
	e.cfg = restored
	prof := e.activeProfileLocked()
	if len(prof.Pages) == 0 {
		e.pageIdx = 0
	} else {
		e.pageIdx = min(max(entry.pageIdx, 0), len(prof.Pages)-1)
	}
	e.folderStack = nil
	for _, fid := range entry.folderStack {
		if prof.Folders != nil && prof.Folders[fid] != nil {
			e.folderStack = append(e.folderStack, fid)
		} else {
			break
		}
	}
	clear(e.visuals)
}

func (e *Engine) finishMutation(before historyEntry, cfgCopy *deck.Config, label string) error {
	if err := e.store.Save(cfgCopy); err != nil {
		e.mu.Lock()
		e.restoreStateLocked(before)
		e.mu.Unlock()
		return err
	}
	e.mu.Lock()
	before.label = label
	e.pushUndoLocked(before)
	e.historyRev++
	e.mu.Unlock()
	return nil
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
	before := e.captureHistoryLocked("")
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

	err := e.finishMutation(before, cfgCopy, "Create profile")
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
	before := e.captureHistoryLocked("")
	disappear := e.visibleEventsLocked(plugin.EventWillDisappear)
	target.Grid = grid
	target.Dials = dials
	appear := e.visibleEventsLocked(plugin.EventWillAppear)
	cfgCopy := e.cfg.Clone()
	e.mu.Unlock()

	err := e.finishMutation(before, cfgCopy, "Resize grid")
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

	before := e.captureHistoryLocked("")
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

	err := e.finishMutation(before, cfgCopy, "Delete profile")
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
	before := e.captureHistoryLocked("")
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

	err := e.finishMutation(before, cfgCopy, "Add page")
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

	before := e.captureHistoryLocked("")
	disappear := e.visibleEventsLocked(plugin.EventWillDisappear)
	prof.Pages = slices.Delete(prof.Pages, pageIndex, pageIndex+1)
	if e.pageIdx >= len(prof.Pages) {
		e.pageIdx = len(prof.Pages) - 1
	}
	e.folderStack = nil
	appear := e.visibleEventsLocked(plugin.EventWillAppear)
	cfgCopy := e.cfg.Clone()
	e.mu.Unlock()

	err := e.finishMutation(before, cfgCopy, "Delete page")
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
	before := e.captureHistoryLocked("")
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

	err := e.finishMutation(before, cfgCopy, "Assign "+desc.Name)
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
	before := e.captureHistoryLocked("")
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

	err := e.finishMutation(before, cfgCopy, "Assign "+desc.Name)
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

// SlotAppearance describes user-editable visual and trigger metadata for a button or dial slot.
type SlotAppearance struct {
	Title           string
	Color           string
	BackgroundColor string
	BackgroundImage string
	HideTitle       bool
	SetHotkey       bool
	Hotkey          string
}

func applySlotAppearance(slot *deck.SlotConfig, app SlotAppearance, allowHotkey bool) error {
	slot.Title = app.Title
	if raw := strings.TrimSpace(app.Color); raw == "" {
		slot.Color = ""
	} else if hex, ok := deck.NormalizeHexColor(raw); ok {
		slot.Color = hex
	} else {
		return fmt.Errorf("invalid accent color %q (expected #RRGGBB)", app.Color)
	}
	if raw := strings.TrimSpace(app.BackgroundColor); raw == "" {
		slot.BackgroundColor = ""
	} else if hex, ok := deck.NormalizeHexColor(raw); ok {
		slot.BackgroundColor = hex
	} else {
		return fmt.Errorf("invalid background color %q (expected #RRGGBB)", app.BackgroundColor)
	}
	slot.BackgroundImage = strings.TrimSpace(app.BackgroundImage)
	slot.HideTitle = app.HideTitle
	if allowHotkey && app.SetHotkey {
		slot.Hotkey = strings.TrimSpace(app.Hotkey)
	}
	return nil
}

// UpdateKeypadSlot persists title, color, and settings changes for the action at c.
func (e *Engine) UpdateKeypadSlot(c deck.Coordinates, title, color string, settings map[string]any) error {
	return e.updateKeypadSlotInternal(c, title, color, false, "", settings)
}

// UpdateKeypadSlotWithHotkey atomically persists title, color, global hotkey, and settings for keypad slot c.
func (e *Engine) UpdateKeypadSlotWithHotkey(c deck.Coordinates, title, color, hotkeyStr string, settings map[string]any) error {
	return e.updateKeypadSlotInternal(c, title, color, true, strings.TrimSpace(hotkeyStr), settings)
}

// UpdateKeypadSlotAppearance atomically persists title, accent/background styling, hotkey, and settings for keypad slot c.
func (e *Engine) UpdateKeypadSlotAppearance(c deck.Coordinates, app SlotAppearance, settings map[string]any) error {
	return e.updateKeypadSlotWith(c, "Edit slot", func(slot *deck.SlotConfig) error {
		return applySlotAppearance(slot, app, true)
	}, settings)
}

func (e *Engine) updateKeypadSlotInternal(
	c deck.Coordinates,
	title string,
	color string,
	setHotkey bool,
	hotkeyStr string,
	settings map[string]any,
) error {
	return e.updateKeypadSlotWith(c, "Edit slot", func(slot *deck.SlotConfig) error {
		return applySlotAppearance(slot, SlotAppearance{
			Title:           title,
			Color:           color,
			BackgroundColor: slot.BackgroundColor,
			BackgroundImage: slot.BackgroundImage,
			HideTitle:       slot.HideTitle,
			SetHotkey:       setHotkey,
			Hotkey:          hotkeyStr,
		}, true)
	}, settings)
}

func (e *Engine) updateKeypadSlotWith(
	c deck.Coordinates,
	label string,
	mutate func(slot *deck.SlotConfig) error,
	settings map[string]any,
) error {
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
	before := e.captureHistoryLocked("")
	if err := mutate(slot); err != nil {
		e.restoreStateLocked(before)
		e.mu.Unlock()
		e.opMu.Unlock()
		return err
	}
	if settings != nil {
		slot.Settings = cloneSettings(settings)
	}
	if live, ok := e.visuals[slot.Context]; ok {
		live.hasTitle = false
		e.visuals[slot.Context] = live
	}
	slotCopy := slot.Clone()
	cfgCopy := e.cfg.Clone()
	e.mu.Unlock()

	err := e.finishMutation(before, cfgCopy, label)
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
	return e.updateDialSlotWith(dialIndex, "Edit dial", func(slot *deck.SlotConfig) error {
		return applySlotAppearance(slot, SlotAppearance{
			Title:           title,
			Color:           color,
			BackgroundColor: slot.BackgroundColor,
			BackgroundImage: slot.BackgroundImage,
			HideTitle:       slot.HideTitle,
		}, false)
	}, settings)
}

// UpdateDialSlotAppearance atomically persists title, accent/background styling, and settings for encoder dialIndex.
func (e *Engine) UpdateDialSlotAppearance(dialIndex int, app SlotAppearance, settings map[string]any) error {
	return e.updateDialSlotWith(dialIndex, "Edit dial", func(slot *deck.SlotConfig) error {
		return applySlotAppearance(slot, app, false)
	}, settings)
}

func (e *Engine) updateDialSlotWith(
	dialIndex int,
	label string,
	mutate func(slot *deck.SlotConfig) error,
	settings map[string]any,
) error {
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
	before := e.captureHistoryLocked("")
	if err := mutate(slot); err != nil {
		e.restoreStateLocked(before)
		e.mu.Unlock()
		e.opMu.Unlock()
		return err
	}
	if settings != nil {
		slot.Settings = cloneSettings(settings)
	}
	if live, ok := e.visuals[slot.Context]; ok {
		live.hasTitle = false
		e.visuals[slot.Context] = live
	}
	slotCopy := slot.Clone()
	cfgCopy := e.cfg.Clone()
	e.mu.Unlock()

	err := e.finishMutation(before, cfgCopy, label)
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

// SetKeypadHotkey persists a global hotkey trigger expression on keypad slot c.
func (e *Engine) SetKeypadHotkey(c deck.Coordinates, hotkeyStr string) error {
	trimmed := strings.TrimSpace(hotkeyStr)

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
	if slot.Hotkey == trimmed {
		e.mu.Unlock()
		e.opMu.Unlock()
		return nil
	}
	before := e.captureHistoryLocked("")
	slot.Hotkey = trimmed
	cfgCopy := e.cfg.Clone()
	e.mu.Unlock()

	err := e.finishMutation(before, cfgCopy, "Set hotkey")
	e.opMu.Unlock()
	if err != nil {
		return err
	}
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
	before := e.captureHistoryLocked("")
	oldSlot := existing.Clone()
	delete(slotsMap, c.SlotKey())
	delete(e.visuals, existing.Context)
	cfgCopy := e.cfg.Clone()
	e.mu.Unlock()

	err := e.finishMutation(before, cfgCopy, "Clear slot")
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
	before := e.captureHistoryLocked("")
	oldSlot := existing.Clone()
	delete(dialsMap, dialIndex)
	delete(e.visuals, existing.Context)
	cfgCopy := e.cfg.Clone()
	e.mu.Unlock()

	err := e.finishMutation(before, cfgCopy, "Clear dial")
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

// SlotLocation uniquely addresses a keypad button or encoder dial inside any profile page or folder.
type SlotLocation struct {
	ProfileID  string
	PageIndex  int
	FolderID   string
	Controller deck.ControllerType
	Coords     deck.Coordinates
	Dial       int
}

// CurrentSlotLocation returns the SlotLocation corresponding to the currently active page or folder.
func (e *Engine) CurrentSlotLocation(ctrl deck.ControllerType, coords deck.Coordinates, dial int) SlotLocation {
	e.mu.RLock()
	defer e.mu.RUnlock()
	prof := e.activeProfileLocked()
	pageIdx := min(max(e.pageIdx, 0), max(0, len(prof.Pages)-1))
	var folderID string
	if len(e.folderStack) > 0 {
		for _, fid := range e.folderStack {
			if f := prof.Folders[fid]; f != nil {
				folderID = fid
			} else {
				break
			}
		}
	}
	return SlotLocation{
		ProfileID:  prof.ID,
		PageIndex:  pageIdx,
		FolderID:   folderID,
		Controller: ctrl,
		Coords:     coords,
		Dial:       dial,
	}
}

func (e *Engine) findProfileByIDLocked(profileID string) *deck.Profile {
	pid := strings.TrimSpace(profileID)
	if pid == "" {
		return e.activeProfileLocked()
	}
	for _, p := range e.cfg.Profiles {
		if p.ID == pid {
			return p
		}
	}
	return nil
}

func (e *Engine) resolveSlotLocationLocked(loc SlotLocation) (
	prof *deck.Profile,
	slots map[string]*deck.SlotConfig,
	dials map[int]*deck.SlotConfig,
	norm SlotLocation,
	err error,
) {
	prof = e.findProfileByIDLocked(loc.ProfileID)
	if prof == nil {
		return nil, nil, nil, SlotLocation{}, fmt.Errorf("profile %q not found", loc.ProfileID)
	}
	norm = loc
	norm.ProfileID = prof.ID
	norm.FolderID = strings.TrimSpace(norm.FolderID)
	if norm.Controller == "" {
		norm.Controller = deck.ControllerKeypad
	}

	if norm.FolderID != "" {
		if prof.Folders == nil {
			return nil, nil, nil, SlotLocation{}, fmt.Errorf("folder %q not found", norm.FolderID)
		}
		f := prof.Folders[norm.FolderID]
		if f == nil {
			return nil, nil, nil, SlotLocation{}, fmt.Errorf("folder %q not found", norm.FolderID)
		}
		if f.Slots == nil {
			f.Slots = make(map[string]*deck.SlotConfig)
		}
		if f.Dials == nil {
			f.Dials = make(map[int]*deck.SlotConfig)
		}
		norm.PageIndex = 0
		slots = f.Slots
		dials = f.Dials
	} else {
		if norm.PageIndex < 0 || norm.PageIndex >= len(prof.Pages) {
			return nil, nil, nil, SlotLocation{}, fmt.Errorf("page index %d out of range [0, %d)", norm.PageIndex, len(prof.Pages))
		}
		pg := prof.Pages[norm.PageIndex]
		if pg.Slots == nil {
			pg.Slots = make(map[string]*deck.SlotConfig)
		}
		if pg.Dials == nil {
			pg.Dials = make(map[int]*deck.SlotConfig)
		}
		slots = pg.Slots
		dials = pg.Dials
	}

	switch norm.Controller {
	case deck.ControllerKeypad:
		if !prof.Grid.Contains(norm.Coords) {
			return nil, nil, nil, SlotLocation{}, fmt.Errorf("slot %s out of bounds for %dx%d grid", norm.Coords.SlotKey(), prof.Grid.Cols, prof.Grid.Rows)
		}
		if norm.FolderID != "" && norm.Coords == (deck.Coordinates{Row: 0, Col: 0}) {
			return nil, nil, nil, SlotLocation{}, errors.New("slot (0, 0) is reserved for folder Back button")
		}
	case deck.ControllerEncoder:
		if norm.Dial < 0 || norm.Dial >= prof.Dials {
			return nil, nil, nil, SlotLocation{}, fmt.Errorf("dial index %d out of range [0, %d)", norm.Dial, prof.Dials)
		}
	default:
		return nil, nil, nil, SlotLocation{}, fmt.Errorf("unsupported controller %q", norm.Controller)
	}
	return prof, slots, dials, norm, nil
}

func slotLocationsEqual(a, b SlotLocation) bool {
	if a.ProfileID != b.ProfileID || a.FolderID != b.FolderID || a.Controller != b.Controller {
		return false
	}
	if a.FolderID == "" && a.PageIndex != b.PageIndex {
		return false
	}
	if a.Controller == deck.ControllerEncoder {
		return a.Dial == b.Dial
	}
	return a.Coords == b.Coords
}

func (e *Engine) isSlotLocationVisibleLocked(loc SlotLocation) bool {
	active := e.activeProfileLocked()
	if active == nil || loc.ProfileID != active.ID {
		return false
	}
	if len(e.folderStack) > 0 {
		var topFolderID string
		for _, fid := range e.folderStack {
			if active.Folders[fid] != nil {
				topFolderID = fid
			} else {
				break
			}
		}
		if topFolderID != "" {
			return loc.FolderID == topFolderID
		}
	}
	return loc.FolderID == "" && loc.PageIndex == min(max(e.pageIdx, 0), max(0, len(active.Pages)-1))
}

func makeSlotLifecycleEvent(evType plugin.EventType, slot *deck.SlotConfig, loc SlotLocation) plugin.ActionEvent {
	ev := plugin.ActionEvent{
		Type:       evType,
		Context:    slot.Context,
		ActionUUID: slot.ActionUUID,
		Controller: loc.Controller,
		Settings:   cloneSettings(slot.Settings),
		State:      slot.State,
	}
	if loc.Controller == deck.ControllerEncoder {
		ev.DialIndex = loc.Dial
	} else {
		ev.Coordinates = loc.Coords
	}
	return ev
}

func folderIDForOpenSlot(slot *deck.SlotConfig) string {
	if slot == nil || slot.ActionUUID != actionFolderOpenUUID {
		return ""
	}
	if raw, ok := slot.Settings["folderId"].(string); ok && strings.TrimSpace(raw) != "" {
		return strings.TrimSpace(raw)
	}
	return slot.Context
}

func transferFolderSubtreeLocked(srcProf, dstProf *deck.Profile, folderSlot *deck.SlotConfig, visited map[string]bool) {
	if srcProf == nil || dstProf == nil || srcProf.ID == dstProf.ID || srcProf.Folders == nil {
		return
	}
	fid := folderIDForOpenSlot(folderSlot)
	if fid == "" || visited[fid] {
		return
	}
	srcFolder := srcProf.Folders[fid]
	if srcFolder == nil {
		return
	}
	visited[fid] = true
	if dstProf.Folders == nil {
		dstProf.Folders = make(map[string]*deck.Folder)
	}
	if _, exists := dstProf.Folders[fid]; !exists {
		dstProf.Folders[fid] = srcFolder.Clone()
	}
	for _, child := range srcFolder.Slots {
		transferFolderSubtreeLocked(srcProf, dstProf, child, visited)
	}
}

func cloneFolderTreeFreshLocked(srcProf *deck.Profile, srcFolderID string, dstProf *deck.Profile, cloned map[string]string) string {
	if newID, ok := cloned[srcFolderID]; ok {
		return newID
	}
	srcFolder := srcProf.Folders[srcFolderID]
	if srcFolder == nil {
		return ""
	}
	newFolderID := deck.NewUUID()
	cloned[srcFolderID] = newFolderID
	if dstProf.Folders == nil {
		dstProf.Folders = make(map[string]*deck.Folder)
	}
	dstFolder := &deck.Folder{
		ID:    newFolderID,
		Name:  srcFolder.Name,
		Slots: make(map[string]*deck.SlotConfig, len(srcFolder.Slots)),
		Dials: make(map[int]*deck.SlotConfig, len(srcFolder.Dials)),
	}
	for k, child := range srcFolder.Slots {
		if child == nil {
			continue
		}
		cp := child.Clone()
		cp.Context = deck.NewUUID()
		if cp.ActionUUID == actionFolderOpenUUID {
			if subSrcID := folderIDForOpenSlot(child); subSrcID != "" && srcProf.Folders[subSrcID] != nil {
				if subNewID := cloneFolderTreeFreshLocked(srcProf, subSrcID, dstProf, cloned); subNewID != "" {
					if cp.Settings == nil {
						cp.Settings = make(map[string]any)
					}
					cp.Settings["folderId"] = subNewID
				}
			}
		}
		dstFolder.Slots[k] = cp
	}
	for idx, child := range srcFolder.Dials {
		if child == nil {
			continue
		}
		cp := child.Clone()
		cp.Context = deck.NewUUID()
		dstFolder.Dials[idx] = cp
	}
	dstProf.Folders[newFolderID] = dstFolder
	return newFolderID
}

// MoveSlot moves or swaps an assigned keypad button or dial between src and dst (within or across pages, folders, or profiles).
// If dst is occupied, the two slots are atomically swapped. Only moved slots whose screen visibility or coordinates changed
// receive targeted WillDisappear / WillAppear events, under a single Undo step.
func (e *Engine) MoveSlot(src, dst SlotLocation) (bool, error) {
	if src.Controller != "" && dst.Controller != "" && src.Controller != dst.Controller {
		return false, fmt.Errorf("cannot move %s into %s slot", src.Controller, dst.Controller)
	}

	e.opMu.Lock()
	e.mu.Lock()
	srcProf, srcSlots, srcDials, normSrc, err := e.resolveSlotLocationLocked(src)
	if err != nil {
		e.mu.Unlock()
		e.opMu.Unlock()
		return false, err
	}
	dstProf, dstSlots, dstDials, normDst, err := e.resolveSlotLocationLocked(dst)
	if err != nil {
		e.mu.Unlock()
		e.opMu.Unlock()
		return false, err
	}
	if slotLocationsEqual(normSrc, normDst) {
		e.mu.Unlock()
		e.opMu.Unlock()
		return false, nil
	}

	var srcSlot, dstSlot *deck.SlotConfig
	if normSrc.Controller == deck.ControllerEncoder {
		srcSlot = srcDials[normSrc.Dial]
		dstSlot = dstDials[normDst.Dial]
		if srcSlot == nil {
			e.mu.Unlock()
			e.opMu.Unlock()
			return false, fmt.Errorf("dial %d is empty", normSrc.Dial)
		}
	} else {
		srcSlot = srcSlots[normSrc.Coords.SlotKey()]
		dstSlot = dstSlots[normDst.Coords.SlotKey()]
		if srcSlot == nil {
			e.mu.Unlock()
			e.opMu.Unlock()
			return false, fmt.Errorf("slot %s is empty", normSrc.Coords.SlotKey())
		}
	}

	if normDst.FolderID != "" && srcProf.ID == dstProf.ID && folderIDForOpenSlot(srcSlot) == normDst.FolderID {
		e.mu.Unlock()
		e.opMu.Unlock()
		return false, errors.New("cannot move a folder button inside itself")
	}
	if dstSlot != nil && normSrc.FolderID != "" && srcProf.ID == dstProf.ID && folderIDForOpenSlot(dstSlot) == normSrc.FolderID {
		e.mu.Unlock()
		e.opMu.Unlock()
		return false, errors.New("cannot move a folder button inside itself")
	}

	before := e.captureHistoryLocked("")
	srcVis := e.isSlotLocationVisibleLocked(normSrc)
	dstVis := e.isSlotLocationVisibleLocked(normDst)

	var disappear, appear []plugin.ActionEvent
	if srcVis {
		disappear = append(disappear, makeSlotLifecycleEvent(plugin.EventWillDisappear, srcSlot, normSrc))
	}
	if dstVis && dstSlot != nil {
		disappear = append(disappear, makeSlotLifecycleEvent(plugin.EventWillDisappear, dstSlot, normDst))
	}

	if srcProf.ID != dstProf.ID {
		transferFolderSubtreeLocked(srcProf, dstProf, srcSlot, make(map[string]bool))
		if dstSlot != nil {
			transferFolderSubtreeLocked(dstProf, srcProf, dstSlot, make(map[string]bool))
		}
	}

	swapped := dstSlot != nil
	if normSrc.Controller == deck.ControllerEncoder {
		dstDials[normDst.Dial] = srcSlot
		if swapped {
			srcDials[normSrc.Dial] = dstSlot
		} else {
			delete(srcDials, normSrc.Dial)
		}
	} else {
		dstSlots[normDst.Coords.SlotKey()] = srcSlot
		if swapped {
			srcSlots[normSrc.Coords.SlotKey()] = dstSlot
		} else {
			delete(srcSlots, normSrc.Coords.SlotKey())
		}
	}

	if !dstVis {
		delete(e.visuals, srcSlot.Context)
	}
	if dstSlot != nil && !srcVis {
		delete(e.visuals, dstSlot.Context)
	}

	if dstVis {
		appear = append(appear, makeSlotLifecycleEvent(plugin.EventWillAppear, srcSlot, normDst))
	}
	if srcVis && dstSlot != nil {
		appear = append(appear, makeSlotLifecycleEvent(plugin.EventWillAppear, dstSlot, normSrc))
	}

	label := "Move button"
	if normSrc.Controller == deck.ControllerEncoder {
		if swapped {
			label = "Swap dials"
		} else {
			label = "Move dial"
		}
	} else if swapped {
		label = "Swap buttons"
	}

	cfgCopy := e.cfg.Clone()
	e.mu.Unlock()

	err = e.finishMutation(before, cfgCopy, label)
	e.opMu.Unlock()
	if err != nil {
		return false, err
	}

	e.dispatchAll(context.Background(), disappear)
	e.dispatchAll(context.Background(), appear)
	e.notifySubscribers()
	return swapped, nil
}

// MoveSlotToPage moves the keypad action at src into the first empty slot on targetPageIdx of src's profile.
func (e *Engine) MoveSlotToPage(src SlotLocation, targetPageIdx int) (deck.Coordinates, error) {
	if src.Controller != "" && src.Controller != deck.ControllerKeypad {
		return deck.Coordinates{}, errors.New("only keypad buttons can move to another page")
	}

	e.mu.RLock()
	srcProf := e.findProfileByIDLocked(src.ProfileID)
	if srcProf == nil {
		e.mu.RUnlock()
		return deck.Coordinates{}, fmt.Errorf("profile %q not found", src.ProfileID)
	}
	if targetPageIdx < 0 || targetPageIdx >= len(srcProf.Pages) {
		e.mu.RUnlock()
		return deck.Coordinates{}, fmt.Errorf("page index %d out of range", targetPageIdx)
	}
	if src.FolderID == "" && src.PageIndex == targetPageIdx && (src.ProfileID == "" || src.ProfileID == srcProf.ID) {
		e.mu.RUnlock()
		return src.Coords, nil
	}
	targetPg := srcProf.Pages[targetPageIdx]
	var (
		found    bool
		dstCoord deck.Coordinates
	)
	for row := range srcProf.Grid.Rows {
		for col := range srcProf.Grid.Cols {
			c := deck.Coordinates{Row: row, Col: col}
			if targetPg.Slots[c.SlotKey()] == nil {
				dstCoord = c
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	srcProfileID := srcProf.ID
	e.mu.RUnlock()

	if !found {
		return deck.Coordinates{}, fmt.Errorf("page %d is full", targetPageIdx+1)
	}

	dst := SlotLocation{
		ProfileID:  srcProfileID,
		PageIndex:  targetPageIdx,
		Controller: deck.ControllerKeypad,
		Coords:     dstCoord,
	}
	if _, err := e.MoveSlot(src, dst); err != nil {
		return deck.Coordinates{}, err
	}
	return dstCoord, nil
}

// MoveSlotToNewPage appends a new page to src's profile and moves src to slot (0, 0) on that page atomically.
func (e *Engine) MoveSlotToNewPage(src SlotLocation) (int, deck.Coordinates, error) {
	if src.Controller != "" && src.Controller != deck.ControllerKeypad {
		return 0, deck.Coordinates{}, errors.New("only keypad buttons can move to a new page")
	}

	e.opMu.Lock()
	e.mu.Lock()
	srcProf, srcSlots, _, normSrc, err := e.resolveSlotLocationLocked(src)
	if err != nil {
		e.mu.Unlock()
		e.opMu.Unlock()
		return 0, deck.Coordinates{}, err
	}
	srcSlot := srcSlots[normSrc.Coords.SlotKey()]
	if srcSlot == nil {
		e.mu.Unlock()
		e.opMu.Unlock()
		return 0, deck.Coordinates{}, fmt.Errorf("slot %s is empty", normSrc.Coords.SlotKey())
	}

	before := e.captureHistoryLocked("")
	var disappear []plugin.ActionEvent
	if e.isSlotLocationVisibleLocked(normSrc) {
		disappear = append(disappear, makeSlotLifecycleEvent(plugin.EventWillDisappear, srcSlot, normSrc))
	}

	newPageIdx := len(srcProf.Pages)
	newPage := &deck.Page{
		ID:    deck.NewUUID(),
		Name:  fmt.Sprintf("Page %d", newPageIdx+1),
		Slots: map[string]*deck.SlotConfig{(deck.Coordinates{Row: 0, Col: 0}).SlotKey(): srcSlot},
		Dials: make(map[int]*deck.SlotConfig),
	}
	srcProf.Pages = append(srcProf.Pages, newPage)
	delete(srcSlots, normSrc.Coords.SlotKey())
	delete(e.visuals, srcSlot.Context)

	cfgCopy := e.cfg.Clone()
	e.mu.Unlock()

	err = e.finishMutation(before, cfgCopy, "Move button to new page")
	e.opMu.Unlock()
	if err != nil {
		return 0, deck.Coordinates{}, err
	}

	e.dispatchAll(context.Background(), disappear)
	e.notifySubscribers()
	return newPageIdx, deck.Coordinates{Row: 0, Col: 0}, nil
}

// MoveSlotToFolder moves the keypad button at src into the first open slot (skipping reserved AutoBack (0,0)) in targetFolderID.
func (e *Engine) MoveSlotToFolder(src SlotLocation, targetFolderID string) (deck.Coordinates, error) {
	if src.Controller != "" && src.Controller != deck.ControllerKeypad {
		return deck.Coordinates{}, errors.New("only keypad buttons can move into folders")
	}
	fid := strings.TrimSpace(targetFolderID)
	if fid == "" {
		return deck.Coordinates{}, errors.New("target folder ID cannot be empty")
	}

	e.mu.RLock()
	srcProf := e.findProfileByIDLocked(src.ProfileID)
	if srcProf == nil {
		e.mu.RUnlock()
		return deck.Coordinates{}, fmt.Errorf("profile %q not found", src.ProfileID)
	}
	folder := srcProf.Folders[fid]
	if folder == nil {
		e.mu.RUnlock()
		return deck.Coordinates{}, fmt.Errorf("folder %q not found", fid)
	}
	// Check source slot is not the OpenFolder button pointing to targetFolderID itself.
	if src.FolderID == "" && src.PageIndex >= 0 && src.PageIndex < len(srcProf.Pages) {
		if s := srcProf.Pages[src.PageIndex].Slots[src.Coords.SlotKey()]; s != nil && folderIDForOpenSlot(s) == fid {
			e.mu.RUnlock()
			return deck.Coordinates{}, errors.New("cannot move a folder button inside itself")
		}
	} else if srcF := srcProf.Folders[src.FolderID]; srcF != nil {
		if s := srcF.Slots[src.Coords.SlotKey()]; s != nil && folderIDForOpenSlot(s) == fid {
			e.mu.RUnlock()
			return deck.Coordinates{}, errors.New("cannot move a folder button inside itself")
		}
	}

	var (
		found    bool
		dstCoord deck.Coordinates
	)
	for row := range srcProf.Grid.Rows {
		for col := range srcProf.Grid.Cols {
			if row == 0 && col == 0 {
				continue
			}
			c := deck.Coordinates{Row: row, Col: col}
			if folder.Slots[c.SlotKey()] == nil {
				dstCoord = c
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	srcProfileID := srcProf.ID
	e.mu.RUnlock()

	if !found {
		return deck.Coordinates{}, errors.New("folder is full")
	}

	dst := SlotLocation{
		ProfileID:  srcProfileID,
		FolderID:   fid,
		Controller: deck.ControllerKeypad,
		Coords:     dstCoord,
	}
	if _, err := e.MoveSlot(src, dst); err != nil {
		return deck.Coordinates{}, err
	}
	return dstCoord, nil
}

// PasteSlotConfig clones cfg (allocating a fresh Context UUID and independent folder subtree if ActionFolderOpen) into dst.
func (e *Engine) PasteSlotConfig(cfg *deck.SlotConfig, srcProfileID string, dst SlotLocation) (*deck.SlotConfig, error) {
	if cfg == nil || strings.TrimSpace(cfg.ActionUUID) == "" {
		return nil, errors.New("copied slot configuration is empty")
	}
	checkCtrl := dst.Controller
	if checkCtrl == "" {
		checkCtrl = deck.ControllerKeypad
	}
	if desc, ok := e.registry.Action(cfg.ActionUUID); ok && !desc.SupportsController(checkCtrl) {
		return nil, fmt.Errorf("action %q does not support %s slots", desc.Name, checkCtrl)
	}

	e.opMu.Lock()
	e.mu.Lock()
	dstProf, dstSlots, dstDials, normDst, err := e.resolveSlotLocationLocked(dst)
	if err != nil {
		e.mu.Unlock()
		e.opMu.Unlock()
		return nil, err
	}

	before := e.captureHistoryLocked("")
	dstVis := e.isSlotLocationVisibleLocked(normDst)
	var oldDst *deck.SlotConfig
	if normDst.Controller == deck.ControllerEncoder {
		oldDst = dstDials[normDst.Dial]
	} else {
		oldDst = dstSlots[normDst.Coords.SlotKey()]
	}

	var disappear, appear []plugin.ActionEvent
	if oldDst != nil {
		if dstVis {
			disappear = append(disappear, makeSlotLifecycleEvent(plugin.EventWillDisappear, oldDst, normDst))
		}
		delete(e.visuals, oldDst.Context)
	}

	newSlot := cfg.Clone()
	newSlot.Context = deck.NewUUID()
	if newSlot.ActionUUID == actionFolderOpenUUID {
		srcProf := e.findProfileByIDLocked(srcProfileID)
		if srcProf == nil {
			srcProf = dstProf
		}
		if srcFid := folderIDForOpenSlot(cfg); srcFid != "" && srcProf.Folders[srcFid] != nil {
			if clonedID := cloneFolderTreeFreshLocked(srcProf, srcFid, dstProf, make(map[string]string)); clonedID != "" {
				if newSlot.Settings == nil {
					newSlot.Settings = make(map[string]any)
				}
				newSlot.Settings["folderId"] = clonedID
			}
		}
	}

	if normDst.Controller == deck.ControllerEncoder {
		dstDials[normDst.Dial] = newSlot
	} else {
		dstSlots[normDst.Coords.SlotKey()] = newSlot
	}
	slotCopy := newSlot.Clone()
	if dstVis {
		appear = append(appear, makeSlotLifecycleEvent(plugin.EventWillAppear, slotCopy, normDst))
	}

	label := "Paste button"
	if normDst.Controller == deck.ControllerEncoder {
		label = "Paste dial"
	}
	cfgCopy := e.cfg.Clone()
	e.mu.Unlock()

	err = e.finishMutation(before, cfgCopy, label)
	e.opMu.Unlock()
	if err != nil {
		return nil, err
	}

	e.dispatchAll(context.Background(), disappear)
	e.dispatchAll(context.Background(), appear)
	e.notifySubscribers()
	return slotCopy, nil
}

// Undo reverts the most recent user configuration change and persists the restored deck state.
func (e *Engine) Undo() (string, error) {
	return e.applyHistoryStep(false)
}

// Redo reapplies the most recently undone configuration change and persists the deck state.
func (e *Engine) Redo() (string, error) {
	return e.applyHistoryStep(true)
}

func (e *Engine) applyHistoryStep(isRedo bool) (string, error) {
	e.opMu.Lock()
	e.mu.Lock()
	var stack []historyEntry
	if isRedo {
		stack = e.redoStack
	} else {
		stack = e.undoStack
	}
	if len(stack) == 0 {
		e.mu.Unlock()
		e.opMu.Unlock()
		return "", nil
	}

	topIdx := len(stack) - 1
	target := stack[topIdx]
	nextCfg := target.cfg.Clone()
	if e.cfg != nil {
		if e.cfg.ColorPalette != nil {
			nextCfg.ColorPalette = slices.Clone(e.cfg.ColorPalette)
		}
		nextCfg.DisabledPlugins = slices.Clone(e.cfg.DisabledPlugins)
	}
	nextCfg.Normalize()
	e.mu.Unlock()

	if err := e.store.Save(nextCfg); err != nil {
		e.opMu.Unlock()
		return "", err
	}

	e.mu.Lock()
	currentSnapshot := e.captureHistoryLocked(target.label)
	if isRedo {
		e.redoStack = slices.Delete(e.redoStack, topIdx, topIdx+1)
		e.undoStack = pushBoundedHistory(e.undoStack, currentSnapshot)
	} else {
		e.undoStack = slices.Delete(e.undoStack, topIdx, topIdx+1)
		e.redoStack = pushBoundedHistory(e.redoStack, currentSnapshot)
	}

	disappear := e.visibleEventsLocked(plugin.EventWillDisappear)
	e.restoreNormalizedStateLocked(nextCfg, target)
	e.historyRev++
	appear := e.visibleEventsLocked(plugin.EventWillAppear)
	e.mu.Unlock()
	e.opMu.Unlock()

	e.dispatchAll(context.Background(), disappear)
	e.dispatchAll(context.Background(), appear)
	e.notifySubscribers()
	return target.label, nil
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

// ReloadVisibleActions re-dispatches EventWillAppear for all visible slots and dials and notifies UI surfaces.
func (e *Engine) ReloadVisibleActions() {
	e.mu.RLock()
	appear := e.visibleEventsLocked(plugin.EventWillAppear)
	e.mu.RUnlock()

	e.dispatchAll(context.Background(), appear)
	e.notifySubscribers()
}

// DisabledPlugins returns a cloned sorted list of disabled plugin UUIDs.
func (e *Engine) DisabledPlugins() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.cfg == nil {
		return nil
	}
	return slices.Clone(e.cfg.DisabledPlugins)
}

// IsPluginEnabled reports whether pluginUUID is currently enabled.
func (e *Engine) IsPluginEnabled(pluginUUID string) bool {
	pid := strings.TrimSpace(pluginUUID)
	if pid == "" {
		return true
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.cfg == nil {
		return true
	}
	return !slices.Contains(e.cfg.DisabledPlugins, pid)
}

// SetPluginEnabled enables or disables pluginUUID persistently and updates live plugin runtime & slot visuals.
func (e *Engine) SetPluginEnabled(pluginUUID string, enabled bool) error {
	pid := strings.TrimSpace(pluginUUID)
	if pid == "" {
		return errors.New("plugin UUID cannot be empty")
	}
	manifest, ok := e.registry.Manifest(pid)
	if !ok {
		return fmt.Errorf("unknown plugin %q", pid)
	}
	if !enabled && manifest.Required {
		return fmt.Errorf("plugin %q is required and cannot be disabled", manifest.Name)
	}

	var disappear []plugin.ActionEvent
	if !enabled {
		e.mu.RLock()
		curEnabled := e.cfg == nil || !slices.Contains(e.cfg.DisabledPlugins, pid)
		if curEnabled {
			disappear = e.visibleEventsForPluginLocked(plugin.EventWillDisappear, pid)
		}
		e.mu.RUnlock()
		if len(disappear) > 0 {
			e.dispatchAll(context.Background(), disappear)
		}
	}

	e.opMu.Lock()
	e.mu.Lock()
	curEnabled := e.cfg == nil || !slices.Contains(e.cfg.DisabledPlugins, pid)
	if curEnabled == enabled {
		e.mu.Unlock()
		_ = e.registry.SetPluginEnabled(pid, enabled)
		e.opMu.Unlock()
		return nil
	}

	prevDisabled := slices.Clone(e.cfg.DisabledPlugins)
	var nextDisabled []string
	if enabled {
		for _, id := range prevDisabled {
			if id != pid {
				nextDisabled = append(nextDisabled, id)
			}
		}
	} else {
		nextDisabled = append(slices.Clone(prevDisabled), pid)
	}
	e.cfg.DisabledPlugins = deck.NormalizeDisabledPlugins(nextDisabled)
	if !enabled {
		e.clearPluginVisualsLocked(pid)
	}
	cfgCopy := e.cfg.Clone()
	e.mu.Unlock()

	if err := e.store.Save(cfgCopy); err != nil {
		e.mu.Lock()
		e.cfg.DisabledPlugins = prevDisabled
		e.mu.Unlock()
		e.opMu.Unlock()
		return err
	}

	_ = e.registry.SetPluginEnabled(pid, enabled)
	var appear []plugin.ActionEvent
	if enabled {
		e.mu.RLock()
		appear = e.visibleEventsForPluginLocked(plugin.EventWillAppear, pid)
		e.mu.RUnlock()
	}
	e.opMu.Unlock()

	_ = e.registry.NotifyPluginEnabled(context.Background(), pid, enabled)
	if len(appear) > 0 {
		e.dispatchAll(context.Background(), appear)
	}
	e.notifySubscribers()
	return nil
}

// PropertyInspectorSession returns an HTML/WebView Property Inspector session for the selected slot or dial, if supported.
func (e *Engine) PropertyInspectorSession(ctrl deck.ControllerType, coords deck.Coordinates, dial int) (plugin.PropertyInspectorSession, bool) {
	if e.registry == nil {
		return plugin.PropertyInspectorSession{}, false
	}
	e.mu.RLock()
	prof := e.activeProfileLocked()
	slotsMap, dialsMap, _ := e.activeContainerLocked(prof)
	var slot *deck.SlotConfig
	if ctrl == deck.ControllerEncoder {
		slot = dialsMap[dial]
	} else {
		slot = slotsMap[coords.SlotKey()]
	}
	var (
		ctxID      string
		actionUUID string
		settings   map[string]any
	)
	if slot != nil {
		ctxID = slot.Context
		actionUUID = slot.ActionUUID
		settings = cloneSettings(slot.Settings)
	}
	e.mu.RUnlock()

	if actionUUID == "" {
		return plugin.PropertyInspectorSession{}, false
	}
	return e.registry.PropertyInspectorSession(ctxID, actionUUID, ctrl, coords, dial, settings)
}

func globalSecretKey(pluginUUID, fieldKey string) string {
	return "plugin.global." + strings.TrimSpace(pluginUUID) + "." + strings.TrimSpace(fieldKey)
}

func (e *Engine) passwordFieldKeys(pluginUUID string) map[string]struct{} {
	if e.registry == nil {
		return nil
	}
	manifest, ok := e.registry.Manifest(pluginUUID)
	if !ok || len(manifest.GlobalSettingsSchema) == 0 {
		return nil
	}
	var keys map[string]struct{}
	for _, f := range manifest.GlobalSettingsSchema {
		if f.Type == plugin.FieldPassword && strings.TrimSpace(f.Key) != "" {
			if keys == nil {
				keys = make(map[string]struct{})
			}
			keys[f.Key] = struct{}{}
		}
	}
	return keys
}

// HasGlobalSecret reports whether a non-empty secret is stored in the secret store for pluginUUID's fieldKey.
func (e *Engine) HasGlobalSecret(pluginUUID, fieldKey string) bool {
	pid := strings.TrimSpace(pluginUUID)
	fk := strings.TrimSpace(fieldKey)
	if pid == "" || fk == "" {
		return false
	}
	e.mu.RLock()
	store := e.secretStore
	e.mu.RUnlock()
	if store == nil {
		return false
	}
	val, err := store.Get(globalSecretKey(pid, fk))
	return err == nil && val != ""
}

// GlobalSettings returns a cloned copy of persisted plugin-wide settings for pluginUUID,
// transparently merging any FieldPassword values stored in the engine's secret store.
func (e *Engine) GlobalSettings(pluginUUID string) map[string]any {
	e.mu.RLock()
	var out map[string]any
	if e.cfg != nil && e.cfg.PluginSettings != nil {
		out = cloneSettings(e.cfg.PluginSettings[pluginUUID])
	} else {
		out = make(map[string]any)
	}
	store := e.secretStore
	e.mu.RUnlock()

	if store != nil {
		for k := range e.passwordFieldKeys(pluginUUID) {
			if val, err := store.Get(globalSecretKey(pluginUUID, k)); err == nil && val != "" {
				out[k] = val
			}
		}
	}
	return out
}

// SaveGlobalSettings persists plugin-wide settings for pluginUUID atomically, routing any
// FieldPassword schema fields to the secret store instead of config.json, and notifies the owning provider.
// Password fields omitted from settings preserve their existing secret store value; an empty string deletes it.
func (e *Engine) SaveGlobalSettings(pluginUUID string, settings map[string]any) error {
	return e.saveGlobalSettings(pluginUUID, settings, true)
}

func (e *Engine) saveGlobalSettings(pluginUUID string, settings map[string]any, notify bool) error {
	pid := strings.TrimSpace(pluginUUID)
	if pid == "" {
		return errors.New("plugin UUID cannot be empty")
	}

	pwKeys := e.passwordFieldKeys(pid)
	plain := make(map[string]any, len(settings))
	var secretUpdates map[string]string
	for k, v := range settings {
		if _, isPW := pwKeys[k]; isPW {
			if secretUpdates == nil {
				secretUpdates = make(map[string]string)
			}
			if v == nil {
				secretUpdates[k] = ""
			} else {
				secretUpdates[k] = fmt.Sprint(v)
			}
			continue
		}
		plain[k] = v
	}

	e.opMu.Lock()
	e.mu.RLock()
	store := e.secretStore
	e.mu.RUnlock()

	if store != nil && len(secretUpdates) > 0 {
		for k, val := range secretUpdates {
			secKey := globalSecretKey(pid, k)
			if val == "" {
				if err := store.Delete(secKey); err != nil && !errors.Is(err, secrets.ErrNotFound) {
					e.opMu.Unlock()
					return fmt.Errorf("delete plugin secret %q: %w", k, err)
				}
			} else {
				if err := store.Set(secKey, val); err != nil {
					e.opMu.Unlock()
					return fmt.Errorf("save plugin secret %q: %w", k, err)
				}
			}
		}
	}

	e.mu.Lock()
	if e.cfg.PluginSettings == nil {
		e.cfg.PluginSettings = make(map[string]map[string]any)
	}
	e.cfg.PluginSettings[pid] = plain
	cfgCopy := e.cfg.Clone()
	e.mu.Unlock()

	err := e.store.Save(cfgCopy)
	e.opMu.Unlock()
	if err != nil {
		return err
	}
	e.notifySubscribers()
	if notify && e.registry != nil {
		_ = e.registry.NotifyGlobalSettingsChanged(context.Background(), pid, e.GlobalSettings(pid))
	}
	return nil
}

// ColorPalette returns a cloned slice of the user's global reusable color swatches.
func (e *Engine) ColorPalette() []deck.ColorSwatch {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.cfg == nil {
		return deck.DefaultColorPalette()
	}
	return slices.Clone(e.cfg.ColorPalette)
}

func (e *Engine) mutateColorPalette(fn func(cur []deck.ColorSwatch) ([]deck.ColorSwatch, error)) error {
	e.opMu.Lock()
	e.mu.Lock()
	cur := slices.Clone(e.cfg.ColorPalette)
	next, err := fn(cur)
	if err != nil {
		e.mu.Unlock()
		e.opMu.Unlock()
		return err
	}
	if next == nil {
		next = []deck.ColorSwatch{}
	} else {
		next = deck.NormalizeColorPalette(next)
	}
	prev := e.cfg.ColorPalette
	e.cfg.ColorPalette = next
	cfgCopy := e.cfg.Clone()
	e.mu.Unlock()

	if err := e.store.Save(cfgCopy); err != nil {
		e.mu.Lock()
		e.cfg.ColorPalette = prev
		e.mu.Unlock()
		e.opMu.Unlock()
		return err
	}
	e.opMu.Unlock()
	e.notifySubscribers()
	return nil
}

// SaveColorPalette replaces the persisted global color palette with normalized swatches.
func (e *Engine) SaveColorPalette(swatches []deck.ColorSwatch) error {
	return e.mutateColorPalette(func(_ []deck.ColorSwatch) ([]deck.ColorSwatch, error) {
		if swatches == nil {
			return []deck.ColorSwatch{}, nil
		}
		return swatches, nil
	})
}

// AddColorSwatch validates hex, updates an existing swatch name or appends a new swatch, and persists the palette.
func (e *Engine) AddColorSwatch(name, hex string) error {
	normHex, ok := deck.NormalizeHexColor(hex)
	if !ok {
		return fmt.Errorf("invalid hex color %q (expected #RRGGBB)", hex)
	}
	cleanName := strings.TrimSpace(name)
	if cleanName == "" {
		cleanName = normHex
	}
	return e.mutateColorPalette(func(cur []deck.ColorSwatch) ([]deck.ColorSwatch, error) {
		for i := range cur {
			if strings.EqualFold(cur[i].Hex, normHex) {
				cur[i].Hex = normHex
				cur[i].Name = cleanName
				return cur, nil
			}
		}
		return append(cur, deck.ColorSwatch{Name: cleanName, Hex: normHex}), nil
	})
}

// ResetColorPalette restores the built-in Obsidian Slate default color palette.
func (e *Engine) ResetColorPalette() error {
	return e.SaveColorPalette(deck.DefaultColorPalette())
}

func (e *Engine) updateVisual(ctxID string, patch plugin.VisualPatch) {
	e.mu.Lock()
	cur := e.visuals[ctxID]
	if patch.Title != nil {
		cur.vis.Title = *patch.Title
		cur.hasTitle = true
	}
	if patch.Subtitle != nil {
		cur.vis.Subtitle = *patch.Subtitle
	}
	if patch.Color != nil {
		cur.vis.Color = *patch.Color
	}
	if patch.BackgroundColor != nil {
		cur.vis.BackgroundColor = *patch.BackgroundColor
	}
	if patch.ImageURI != nil {
		cur.vis.ImageURI = *patch.ImageURI
	}
	if patch.State != nil {
		cur.vis.State = *patch.State
	}
	if patch.Feedback != nil {
		cur.vis.Feedback = maps.Clone(patch.Feedback)
	}
	cur.vis.Flash = patch.Flash
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
		e.historyRev++
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
	return h.eng.saveGlobalSettings(pluginUUID, settings, false)
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
