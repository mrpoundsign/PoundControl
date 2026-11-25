package deck

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
)

// CurrentSchemaVersion is the persisted configuration schema version.
const CurrentSchemaVersion = 1

// NewUUID generates a random RFC 4122 v4 UUID string without external dependencies.
func NewUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is non-recoverable on supported OSes, fallback deterministically only in extremis.
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ColorSwatch defines one named hex color entry in the user's reusable global palette.
type ColorSwatch struct {
	Name string `json:"name"`
	Hex  string `json:"hex"`
}

// DefaultColorPalette returns the built-in Obsidian Slate accent & background swatches.
func DefaultColorPalette() []ColorSwatch {
	return []ColorSwatch{
		{Name: "Sky Blue", Hex: "#38BDF8"},
		{Name: "Emerald", Hex: "#22C55E"},
		{Name: "Amber", Hex: "#FBBF24"},
		{Name: "Rose", Hex: "#EF4444"},
		{Name: "Violet", Hex: "#A855F7"},
		{Name: "Slate", Hex: "#94A3B8"},
	}
}

// NormalizeHexColor validates a 3- or 6-digit hex color (with or without '#') and returns uppercase #RRGGBB.
func NormalizeHexColor(raw string) (string, bool) {
	s := strings.TrimPrefix(strings.TrimSpace(raw), "#")
	switch len(s) {
	case 3:
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	case 6:
	default:
		return "", false
	}
	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return "", false
		}
	}
	return "#" + strings.ToUpper(s), true
}

// NormalizeColorPalette canonicalizes hex colors, trims names, and deduplicates swatches by hex.
// A nil slice seeds DefaultColorPalette; an intentionally empty non-nil slice is preserved.
func NormalizeColorPalette(in []ColorSwatch) []ColorSwatch {
	if in == nil {
		return DefaultColorPalette()
	}
	out := make([]ColorSwatch, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, sw := range in {
		hex, ok := NormalizeHexColor(sw.Hex)
		if !ok {
			continue
		}
		if _, dup := seen[hex]; dup {
			continue
		}
		seen[hex] = struct{}{}
		name := strings.TrimSpace(sw.Name)
		if name == "" {
			name = hex
		}
		out = append(out, ColorSwatch{
			Name: name,
			Hex:  hex,
		})
	}
	return out
}

// SlotConfig holds the persisted action binding and configuration for one keypad slot or dial.
type SlotConfig struct {
	Context         string         `json:"context"`
	ActionUUID      string         `json:"actionUuid"`
	Title           string         `json:"title,omitempty"`
	Color           string         `json:"color,omitempty"`
	BackgroundColor string         `json:"backgroundColor,omitempty"`
	BackgroundImage string         `json:"backgroundImage,omitempty"`
	HideTitle       bool           `json:"hideTitle,omitempty"`
	Hotkey          string         `json:"hotkey,omitempty"`
	State           int            `json:"state,omitempty"`
	Settings        map[string]any `json:"settings,omitempty"`
}

// Clone returns a deep copy of the slot configuration.
func (s *SlotConfig) Clone() *SlotConfig {
	if s == nil {
		return nil
	}
	cp := *s
	cp.Settings = cloneAnyMap(s.Settings)
	return &cp
}

// Page represents a single page of keypad slots and encoder dials within a profile.
type Page struct {
	ID    string                 `json:"id"`
	Name  string                 `json:"name"`
	Slots map[string]*SlotConfig `json:"slots,omitempty"`
	Dials map[int]*SlotConfig    `json:"dials,omitempty"`
}

// Clone returns a deep copy of the page and all its slots.
func (p *Page) Clone() *Page {
	if p == nil {
		return nil
	}
	return &Page{
		ID:    p.ID,
		Name:  p.Name,
		Slots: cloneSlotMap(p.Slots),
		Dials: cloneDialMap(p.Dials),
	}
}

// Folder represents a nested sub-deck container addressable by ID inside a profile.
type Folder struct {
	ID    string                 `json:"id"`
	Name  string                 `json:"name"`
	Slots map[string]*SlotConfig `json:"slots,omitempty"`
	Dials map[int]*SlotConfig    `json:"dials,omitempty"`
}

// Clone returns a deep copy of the folder and all its slots.
func (f *Folder) Clone() *Folder {
	if f == nil {
		return nil
	}
	return &Folder{
		ID:    f.ID,
		Name:  f.Name,
		Slots: cloneSlotMap(f.Slots),
		Dials: cloneDialMap(f.Dials),
	}
}

// Profile defines a deck grid geometry, encoder dial count, pages, and folders.
type Profile struct {
	ID      string             `json:"id"`
	Name    string             `json:"name"`
	Grid    GridSize           `json:"grid"`
	Dials   int                `json:"dials"`
	Pages   []*Page            `json:"pages"`
	Folders map[string]*Folder `json:"folders,omitempty"`
}

// Clone returns a deep copy of the profile.
func (p *Profile) Clone() *Profile {
	if p == nil {
		return nil
	}
	pages := make([]*Page, len(p.Pages))
	for i, pg := range p.Pages {
		pages[i] = pg.Clone()
	}
	folders := make(map[string]*Folder, len(p.Folders))
	for k, f := range p.Folders {
		folders[k] = f.Clone()
	}
	return &Profile{
		ID:      p.ID,
		Name:    p.Name,
		Grid:    p.Grid,
		Dials:   p.Dials,
		Pages:   pages,
		Folders: folders,
	}
}

// Config is the top-level persisted PoundControl configuration document.
type Config struct {
	SchemaVersion   int                       `json:"schemaVersion"`
	ActiveProfileID string                    `json:"activeProfileId"`
	Profiles        []*Profile                `json:"profiles"`
	ColorPalette    []ColorSwatch             `json:"colorPalette"`
	PluginSettings  map[string]map[string]any `json:"pluginSettings,omitempty"`
	DisabledPlugins []string                  `json:"disabledPlugins,omitempty"`
}

// NormalizeDisabledPlugins trims, deduplicates, and sorts disabled plugin UUIDs cleanly.
func NormalizeDisabledPlugins(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil
	}
	slices.Sort(out)
	return out
}

// DefaultConfig returns a valid baseline configuration with a 5x3 "Default" profile.
func DefaultConfig() *Config {
	profID := NewUUID()
	return &Config{
		SchemaVersion:   CurrentSchemaVersion,
		ActiveProfileID: profID,
		Profiles: []*Profile{
			{
				ID:    profID,
				Name:  "Default",
				Grid:  DefaultGrid(),
				Dials: 4,
				Pages: []*Page{
					{
						ID:    NewUUID(),
						Name:  "Page 1",
						Slots: make(map[string]*SlotConfig),
						Dials: make(map[int]*SlotConfig),
					},
				},
				Folders: make(map[string]*Folder),
			},
		},
		ColorPalette:   DefaultColorPalette(),
		PluginSettings: make(map[string]map[string]any),
	}
}

// Clone returns a deep copy of the entire Config tree.
func (c *Config) Clone() *Config {
	if c == nil {
		return nil
	}
	profiles := make([]*Profile, len(c.Profiles))
	for i, p := range c.Profiles {
		profiles[i] = p.Clone()
	}
	pluginSettings := make(map[string]map[string]any, len(c.PluginSettings))
	for pid, settings := range c.PluginSettings {
		pluginSettings[pid] = cloneAnyMap(settings)
	}
	return &Config{
		SchemaVersion:   c.SchemaVersion,
		ActiveProfileID: c.ActiveProfileID,
		Profiles:        profiles,
		ColorPalette:    slices.Clone(c.ColorPalette),
		PluginSettings:  pluginSettings,
		DisabledPlugins: slices.Clone(c.DisabledPlugins),
	}
}

// Normalize fills in missing defaults, non-nil maps, and slot Context UUIDs in place.
func (c *Config) Normalize() {
	if c == nil {
		return
	}
	if c.SchemaVersion <= 0 {
		c.SchemaVersion = CurrentSchemaVersion
	}
	c.ColorPalette = NormalizeColorPalette(c.ColorPalette)
	c.DisabledPlugins = NormalizeDisabledPlugins(c.DisabledPlugins)
	if c.PluginSettings == nil {
		c.PluginSettings = make(map[string]map[string]any)
	} else {
		for pid, settings := range c.PluginSettings {
			if settings == nil {
				c.PluginSettings[pid] = make(map[string]any)
			}
		}
	}
	if len(c.Profiles) == 0 {
		def := DefaultConfig()
		c.Profiles = def.Profiles
		c.ActiveProfileID = def.ActiveProfileID
		return
	}

	activeFound := false
	for i, prof := range c.Profiles {
		if prof == nil {
			prof = &Profile{}
			c.Profiles[i] = prof
		}
		if strings.TrimSpace(prof.ID) == "" {
			prof.ID = NewUUID()
		}
		if strings.TrimSpace(prof.Name) == "" {
			prof.Name = fmt.Sprintf("Profile %d", i+1)
		}
		if err := prof.Grid.Validate(); err != nil {
			prof.Grid = DefaultGrid()
		}
		if prof.Dials < 0 {
			prof.Dials = 0
		}
		if prof.Dials > 16 {
			prof.Dials = 16
		}
		if prof.Folders == nil {
			prof.Folders = make(map[string]*Folder)
		}
		if len(prof.Pages) == 0 {
			prof.Pages = []*Page{
				{
					ID:    NewUUID(),
					Name:  "Page 1",
					Slots: make(map[string]*SlotConfig),
					Dials: make(map[int]*SlotConfig),
				},
			}
		}
		for j, pg := range prof.Pages {
			if pg == nil {
				pg = &Page{}
				prof.Pages[j] = pg
			}
			if strings.TrimSpace(pg.ID) == "" {
				pg.ID = NewUUID()
			}
			if strings.TrimSpace(pg.Name) == "" {
				pg.Name = fmt.Sprintf("Page %d", j+1)
			}
			pg.Slots = normalizeSlotMap(pg.Slots)
			pg.Dials = normalizeDialMap(pg.Dials)
		}
		for id, f := range prof.Folders {
			if f == nil {
				delete(prof.Folders, id)
				continue
			}
			if strings.TrimSpace(f.ID) == "" {
				f.ID = id
			}
			if strings.TrimSpace(f.Name) == "" {
				f.Name = "Folder"
			}
			f.Slots = normalizeSlotMap(f.Slots)
			f.Dials = normalizeDialMap(f.Dials)
		}
		if prof.ID == c.ActiveProfileID {
			activeFound = true
		}
	}

	if !activeFound {
		c.ActiveProfileID = c.Profiles[0].ID
	}
	c.MigrateLegacySettings()
}

const (
	obsAudioVolumeActionUUID = "com.github.mrpoundsign.poundcontrol.obs.audio.volume"
	coreMultiActionUUID      = "com.github.mrpoundsign.poundcontrol.core.multiaction"
)

// MigrateLegacySettings upgrades legacy slot settings (such as OBS Audio Volume dB fields)
// in place across all profiles, pages, folders, and Multi-Action step sequences.
// Returns true if any slot or step was modified.
func (c *Config) MigrateLegacySettings() bool {
	if c == nil {
		return false
	}
	changed := false
	for _, prof := range c.Profiles {
		if prof == nil {
			continue
		}
		for _, pg := range prof.Pages {
			if pg == nil {
				continue
			}
			for _, slot := range pg.Slots {
				if migrateSlotLegacySettings(slot) {
					changed = true
				}
			}
			for _, slot := range pg.Dials {
				if migrateSlotLegacySettings(slot) {
					changed = true
				}
			}
		}
		for _, f := range prof.Folders {
			if f == nil {
				continue
			}
			for _, slot := range f.Slots {
				if migrateSlotLegacySettings(slot) {
					changed = true
				}
			}
			for _, slot := range f.Dials {
				if migrateSlotLegacySettings(slot) {
					changed = true
				}
			}
		}
	}
	return changed
}

func migrateSlotLegacySettings(slot *SlotConfig) bool {
	if slot == nil || len(slot.Settings) == 0 {
		return false
	}
	switch slot.ActionUUID {
	case obsAudioVolumeActionUUID:
		return migrateOBSVolumeSettings(slot.Settings)
	case coreMultiActionUUID:
		rawSteps, ok := slot.Settings["steps"].(string)
		if !ok || strings.TrimSpace(rawSteps) == "" {
			return false
		}
		var steps []map[string]any
		if err := json.Unmarshal([]byte(rawSteps), &steps); err != nil {
			return false
		}
		stepChanged := false
		for _, step := range steps {
			if step == nil {
				continue
			}
			uuidStr, _ := step["actionUuid"].(string)
			if uuidStr != obsAudioVolumeActionUUID {
				continue
			}
			settings, ok := step["settings"].(map[string]any)
			if !ok || len(settings) == 0 {
				continue
			}
			if migrateOBSVolumeSettings(settings) {
				step["settings"] = settings
				stepChanged = true
			}
		}
		if !stepChanged {
			return false
		}
		encoded, err := json.Marshal(steps)
		if err != nil {
			return false
		}
		slot.Settings["steps"] = string(encoded)
		return true
	default:
		return false
	}
}

func migrateOBSVolumeSettings(settings map[string]any) bool {
	if len(settings) == 0 {
		return false
	}
	changed := false
	if rawTarget, hasLegacyTarget := settings["targetDb"]; hasLegacyTarget {
		if _, hasTargetPct := settings["targetPct"]; !hasTargetPct {
			if db, ok := parseLegacyFloat(rawTarget); ok {
				settings["targetPct"] = obsDbToFaderPct(db)
			}
		}
		delete(settings, "targetDb")
		changed = true
	}
	if rawStep, hasLegacyStep := settings["stepDb"]; hasLegacyStep {
		if _, hasStepPct := settings["stepPct"]; !hasStepPct {
			if stepDb, ok := parseLegacyFloat(rawStep); ok {
				settings["stepPct"] = obsStepDbToStepPct(stepDb)
			}
		}
		delete(settings, "stepDb")
		changed = true
	}
	if push, ok := settings["pushAction"].(string); ok && push == "reset_0db" {
		settings["pushAction"] = "reset_100"
		changed = true
	}
	return changed
}

func parseLegacyFloat(raw any) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		return v, !math.IsNaN(v) && !math.IsInf(v, 0)
	case float32:
		f := float64(v)
		return f, !math.IsNaN(f) && !math.IsInf(f, 0)
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	default:
		return 0, false
	}
}

func obsDbToFaderPct(db float64) float64 {
	if math.IsNaN(db) || math.IsInf(db, -1) || db <= -100 {
		return 0
	}
	if db >= 0 {
		return 100
	}
	mul := math.Pow(10, db/20.0)
	pct := math.Round(math.Cbrt(mul)*100.0*2.0) / 2.0
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

func obsStepDbToStepPct(stepDb float64) float64 {
	if math.IsNaN(stepDb) || stepDb == 0 {
		return 5
	}
	mag := math.Round(math.Abs(stepDb)*2.5*2.0) / 2.0
	if mag < 0.5 {
		mag = 0.5
	}
	if mag > 100 {
		mag = 100
	}
	return math.Copysign(mag, stepDb)
}

// Validate checks structural invariants across profiles, pages, folders, and slots.
func (c *Config) Validate() error {
	if c == nil {
		return errors.New("config is nil")
	}
	if c.SchemaVersion <= 0 {
		return fmt.Errorf("invalid schemaVersion %d", c.SchemaVersion)
	}
	if len(c.Profiles) == 0 {
		return errors.New("config must contain at least one profile")
	}

	seenProfiles := make(map[string]struct{}, len(c.Profiles))
	activeFound := false
	for i, prof := range c.Profiles {
		if prof == nil {
			return fmt.Errorf("profile[%d] is nil", i)
		}
		if strings.TrimSpace(prof.ID) == "" {
			return fmt.Errorf("profile[%d] ID is required", i)
		}
		if _, dup := seenProfiles[prof.ID]; dup {
			return fmt.Errorf("duplicate profile ID %q", prof.ID)
		}
		seenProfiles[prof.ID] = struct{}{}
		if strings.TrimSpace(prof.Name) == "" {
			return fmt.Errorf("profile %q Name is required", prof.ID)
		}
		if err := prof.Grid.Validate(); err != nil {
			return fmt.Errorf("profile %q grid: %w", prof.ID, err)
		}
		if prof.Dials < 0 || prof.Dials > 16 {
			return fmt.Errorf("profile %q dials %d out of range [0, 16]", prof.ID, prof.Dials)
		}
		if len(prof.Pages) == 0 {
			return fmt.Errorf("profile %q must contain at least one page", prof.ID)
		}
		if err := validatePages(prof); err != nil {
			return err
		}
		if err := validateFolders(prof); err != nil {
			return err
		}
		if prof.ID == c.ActiveProfileID {
			activeFound = true
		}
	}

	if !activeFound {
		return fmt.Errorf("activeProfileId %q not found in profiles", c.ActiveProfileID)
	}
	return nil
}

func validatePages(prof *Profile) error {
	seenPages := make(map[string]struct{}, len(prof.Pages))
	for j, pg := range prof.Pages {
		if pg == nil {
			return fmt.Errorf("profile %q page[%d] is nil", prof.ID, j)
		}
		if strings.TrimSpace(pg.ID) == "" {
			return fmt.Errorf("profile %q page[%d] ID is required", prof.ID, j)
		}
		if _, dup := seenPages[pg.ID]; dup {
			return fmt.Errorf("profile %q duplicate page ID %q", prof.ID, pg.ID)
		}
		seenPages[pg.ID] = struct{}{}
		if strings.TrimSpace(pg.Name) == "" {
			return fmt.Errorf("profile %q page %q Name is required", prof.ID, pg.ID)
		}
		if err := validateSlotContainer("page "+pg.ID, pg.Slots, pg.Dials); err != nil {
			return fmt.Errorf("profile %q %w", prof.ID, err)
		}
	}
	return nil
}

func validateFolders(prof *Profile) error {
	for fid, f := range prof.Folders {
		if f == nil {
			return fmt.Errorf("profile %q folder %q is nil", prof.ID, fid)
		}
		if strings.TrimSpace(f.ID) == "" {
			return fmt.Errorf("profile %q folder %q ID is required", prof.ID, fid)
		}
		if strings.TrimSpace(f.Name) == "" {
			return fmt.Errorf("profile %q folder %q Name is required", prof.ID, fid)
		}
		if err := validateSlotContainer("folder "+f.ID, f.Slots, f.Dials); err != nil {
			return fmt.Errorf("profile %q %w", prof.ID, err)
		}
	}
	return nil
}

func validateSlotContainer(label string, slots map[string]*SlotConfig, dials map[int]*SlotConfig) error {
	for k, slot := range slots {
		if slot == nil {
			return fmt.Errorf("%s slot %q is nil", label, k)
		}
		if strings.TrimSpace(slot.Context) == "" {
			return fmt.Errorf("%s slot %q context is required", label, k)
		}
		if strings.TrimSpace(slot.ActionUUID) == "" {
			return fmt.Errorf("%s slot %q actionUuid is required", label, k)
		}
	}
	for idx, slot := range dials {
		if idx < 0 {
			return fmt.Errorf("%s negative dial index %d", label, idx)
		}
		if slot == nil {
			return fmt.Errorf("%s dial %d is nil", label, idx)
		}
		if strings.TrimSpace(slot.Context) == "" {
			return fmt.Errorf("%s dial %d context is required", label, idx)
		}
		if strings.TrimSpace(slot.ActionUUID) == "" {
			return fmt.Errorf("%s dial %d actionUuid is required", label, idx)
		}
	}
	return nil
}

func normalizeSlotConfig(slot *SlotConfig) {
	if strings.TrimSpace(slot.Context) == "" {
		slot.Context = NewUUID()
	}
	if slot.Settings == nil {
		slot.Settings = make(map[string]any)
	}
	if hex, ok := NormalizeHexColor(slot.Color); ok {
		slot.Color = hex
	} else {
		slot.Color = strings.TrimSpace(slot.Color)
	}
	if hex, ok := NormalizeHexColor(slot.BackgroundColor); ok {
		slot.BackgroundColor = hex
	} else {
		slot.BackgroundColor = strings.TrimSpace(slot.BackgroundColor)
	}
	slot.BackgroundImage = strings.TrimSpace(slot.BackgroundImage)
}

func normalizeSlotMap(src map[string]*SlotConfig) map[string]*SlotConfig {
	if src == nil {
		return make(map[string]*SlotConfig)
	}
	for k, slot := range src {
		if slot == nil {
			delete(src, k)
			continue
		}
		normalizeSlotConfig(slot)
	}
	return src
}

func normalizeDialMap(src map[int]*SlotConfig) map[int]*SlotConfig {
	if src == nil {
		return make(map[int]*SlotConfig)
	}
	for k, slot := range src {
		if slot == nil || k < 0 {
			delete(src, k)
			continue
		}
		normalizeSlotConfig(slot)
	}
	return src
}

func cloneSlotMap(src map[string]*SlotConfig) map[string]*SlotConfig {
	if src == nil {
		return make(map[string]*SlotConfig)
	}
	out := make(map[string]*SlotConfig, len(src))
	for k, v := range src {
		out[k] = v.Clone()
	}
	return out
}

func cloneDialMap(src map[int]*SlotConfig) map[int]*SlotConfig {
	if src == nil {
		return make(map[int]*SlotConfig)
	}
	out := make(map[int]*SlotConfig, len(src))
	for k, v := range src {
		out[k] = v.Clone()
	}
	return out
}

func cloneAnyMap(src map[string]any) map[string]any {
	if src == nil {
		return make(map[string]any)
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = cloneAnyValue(v)
	}
	return out
}

func cloneAnyValue(v any) any {
	switch tv := v.(type) {
	case map[string]any:
		return cloneAnyMap(tv)
	case []any:
		cp := make([]any, len(tv))
		for i, elem := range tv {
			cp[i] = cloneAnyValue(elem)
		}
		return cp
	case map[string]string:
		return maps.Clone(tv)
	case []string:
		return slices.Clone(tv)
	default:
		return v
	}
}
