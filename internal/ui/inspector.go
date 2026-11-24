package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/mrpoundsign/PoundControl/internal/app"
	"github.com/mrpoundsign/PoundControl/internal/hotkey"
	"github.com/mrpoundsign/PoundControl/pkg/builtin/core"
	"github.com/mrpoundsign/PoundControl/pkg/deck"
	"github.com/mrpoundsign/PoundControl/pkg/plugin"
)

type stepInspectorCallbacks struct {
	onSave         func(idx int, settings map[string]any) error
	onTest         func(idx int, step core.MultiActionStep)
	onDelete       func(idx int)
	onBackToButton func()
}

// ActionInspector renders a schema-driven configuration form for the selected slot or dial.
type ActionInspector struct {
	win                     fyne.Window
	engine                  *app.Engine
	onStatus                func(string)
	onSuspendHotkeys        func(bool)
	piViewer                PIViewer
	sdCtrl                  SDPluginController
	onOpenPluginsSettings   func()
	onOpenPaletteSettings   func()
	onSetPluginEnabled      func(string, bool) error
	onMoveSelected          func()
	onCopySelected          func()
	onEditMultiAction       func()
	assetsDir               string
	palette                 []deck.ColorSwatch
	root                    fyne.CanvasObject

	slotLbl    *widget.Label
	actionLbl  *widget.Label
	metaLbl    *widget.Label
	formScroll *container.Scroll
	formBox    *fyne.Container
	accordion  *widget.Accordion
	moveCopyRow *fyne.Container
	actionsRow *fyne.Container

	sectionOpen map[string]bool // keyed by strings.ToLower(sectionTitle)

	titleEntry         *widget.Entry
	accentColorCtrl    *colorFieldControl
	bgColorCtrl        *colorFieldControl
	bgImageCtrl        *imageFieldControl
	hideTitleCheck     *widget.Check
	hotkeyEntry        *widget.Entry
	hotkeyCaptureBtn   *widget.Button
	openPIBtn          *widget.Button
	schemaForm         *schemaFormState
	hotkeyFieldButtons map[string]*widget.Button
	fields             map[string]schemaFieldBinding
	saveBtn            *widget.Button
	triggerBtn         *widget.Button
	clearBtn           *widget.Button
	moveBtn            *widget.Button
	copyBtn            *widget.Button
	lastCapture        *hotkeyCaptureSession

	selCtrl            deck.ControllerType
	selCoords          deck.Coordinates
	selDial            int
	boundContext       string
	boundActionID      string
	boundAutoBack      bool
	boundDisabled      bool
	boundUnavailable   bool
	boundHasSchema     bool
	boundPluginUUID    string
	boundHistoryRev    uint64
	curSettings        map[string]any
	syncing            bool
	editingMultiAction bool
	slotInitialSnap    string

	stepMode           bool
	stepIndex          int
	stepTotal          int
	stepData           core.MultiActionStep
	stepDesc           *plugin.ActionDescriptor
	stepNoSchema       bool
	stepRawJSONEntry   *widget.Entry
	stepInitialSnap    string
	onStepSave         func(idx int, settings map[string]any) error
	onStepTest         func(idx int, step core.MultiActionStep)
	onStepDelete       func(idx int)
	onStepBackToButton func()
}

// NewActionInspector constructs an inspector panel bound to engine.
func NewActionInspector(win fyne.Window, engine *app.Engine, onStatus func(string)) *ActionInspector {
	ins := &ActionInspector{
		win:         win,
		engine:      engine,
		onStatus:    onStatus,
		sectionOpen: make(map[string]bool),
	}
	ins.schemaForm = newSchemaFormState(schemaFormOptions{
		win:       win,
		engine:    engine,
		assetsDir: func() string { return ins.assetsDir },
		palette:   func() []deck.ColorSwatch { return ins.palette },
		onStatus:  ins.reportStatus,
		onCaptureHotkey: func(title string, target *widget.Entry) {
			ins.openHotkeyCapture(title, target, captureModeChord)
		},
	})
	ins.fields = ins.schemaForm.fields
	ins.hotkeyFieldButtons = ins.schemaForm.hotkeyFieldButtons

	heading := widget.NewLabelWithStyle("Action Inspector", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	ins.slotLbl = widget.NewLabel("Slot: Row 0, Col 0")
	ins.actionLbl = widget.NewLabel("Unassigned")
	ins.actionLbl.TextStyle = fyne.TextStyle{Bold: true}
	ins.metaLbl = widget.NewLabel("")
	ins.metaLbl.Wrapping = fyne.TextWrapWord

	ins.formBox = container.NewVBox()
	ins.formScroll = container.NewVScroll(ins.formBox)

	ins.saveBtn = widget.NewButtonWithIcon("Save", theme.DocumentSaveIcon(), ins.handleSave)
	ins.saveBtn.Importance = widget.HighImportance
	ins.triggerBtn = widget.NewButtonWithIcon("Trigger", theme.MediaPlayIcon(), ins.handleTrigger)
	ins.clearBtn = widget.NewButtonWithIcon("Clear", theme.DeleteIcon(), ins.handleClear)
	ins.moveBtn = widget.NewButton("Move...", func() {
		if ins.onMoveSelected != nil {
			ins.onMoveSelected()
		}
	})
	ins.moveBtn.Disable()
	ins.copyBtn = widget.NewButtonWithIcon("Copy", theme.ContentCopyIcon(), func() {
		if ins.onCopySelected != nil {
			ins.onCopySelected()
		}
	})
	ins.copyBtn.Disable()
	ins.moveCopyRow = container.NewGridWithColumns(2, ins.moveBtn, ins.copyBtn)
	ins.actionsRow = container.NewVBox(
		ins.moveCopyRow,
		container.NewGridWithColumns(3, ins.saveBtn, ins.triggerBtn, ins.clearBtn),
	)

	top := container.NewVBox(
		heading,
		widget.NewSeparator(),
		ins.slotLbl,
		ins.actionLbl,
		ins.metaLbl,
		widget.NewSeparator(),
	)

	panelBg := canvas.NewRectangle(ColorDarkCharcoal)
	ins.root = container.NewStack(
		panelBg,
		container.NewPadded(container.NewBorder(top, ins.actionsRow, nil, nil, ins.formScroll)),
	)

	return ins
}

// Object returns the root Fyne canvas object for layout embedding.
func (ins *ActionInspector) Object() fyne.CanvasObject {
	return ins.root
}

// SetHotkeySuspender sets a callback used to pause OS global hotkeys while capturing a new shortcut.
func (ins *ActionInspector) SetHotkeySuspender(fn func(bool)) {
	ins.onSuspendHotkeys = fn
}

// SetPIViewer attaches a Property Inspector viewer used for interactive plugin HTML UIs.
func (ins *ActionInspector) SetPIViewer(v PIViewer) {
	ins.piViewer = v
}

// SetSDPluginController attaches an external .sdPlugin host controller for runtime status prompts.
func (ins *ActionInspector) SetSDPluginController(ctrl SDPluginController) {
	ins.sdCtrl = ctrl
}

// SetOnOpenPluginsSettings registers a callback invoked when the user clicks "Install Node.js in Settings".
func (ins *ActionInspector) SetOnOpenPluginsSettings(fn func()) {
	ins.onOpenPluginsSettings = fn
}

// SetOnOpenPaletteSettings registers a callback invoked when the user opens the palette editor.
func (ins *ActionInspector) SetOnOpenPaletteSettings(fn func()) {
	ins.onOpenPaletteSettings = fn
}

// SetOnSetPluginEnabled registers a callback invoked when the user clicks Enable on a disabled plugin's slot.
func (ins *ActionInspector) SetOnSetPluginEnabled(fn func(string, bool) error) {
	ins.onSetPluginEnabled = fn
}

// SetAssetsDir configures the directory where imported custom button images are stored.
func (ins *ActionInspector) SetAssetsDir(dir string) {
	ins.assetsDir = strings.TrimSpace(dir)
}

// SetOnMoveSelected registers a callback invoked when the user clicks Move in the inspector.
func (ins *ActionInspector) SetOnMoveSelected(fn func()) {
	ins.onMoveSelected = fn
}

// SetOnCopySelected registers a callback invoked when the user clicks Copy in the inspector.
func (ins *ActionInspector) SetOnCopySelected(fn func()) {
	ins.onCopySelected = fn
}

// SetOnEditMultiAction registers a callback invoked when the user clicks Edit Sequence in the inspector.
func (ins *ActionInspector) SetOnEditMultiAction(fn func()) {
	ins.onEditMultiAction = fn
}

// SetEditingMultiAction toggles whether the center-stage sequence editor is currently active for the selected slot.
func (ins *ActionInspector) SetEditingMultiAction(editing bool) {
	if ins.editingMultiAction == editing {
		return
	}
	ins.editingMultiAction = editing
	ins.InvalidateBinding()
}

// InvalidateBinding clears the cached inspector binding so the next SetSelection rebuilds controls cleanly.
func (ins *ActionInspector) InvalidateBinding() {
	ins.boundContext = ""
	ins.boundActionID = ""
	ins.boundAutoBack = false
}

func (ins *ActionInspector) refreshPaletteSwatches(swatches []deck.ColorSwatch) {
	if slices.Equal(ins.palette, swatches) {
		return
	}
	ins.palette = slices.Clone(swatches)
	if ins.accentColorCtrl != nil {
		ins.accentColorCtrl.RefreshPalette(ins.palette)
	}
	if ins.bgColorCtrl != nil {
		ins.bgColorCtrl.RefreshPalette(ins.palette)
	}
	ins.schemaForm.refreshPalette(ins.palette)
}

// SetSelection refreshes the inspector to reflect the currently selected slot or dial.
// Form controls are rebuilt only when the bound action context or action UUID changes so active input focus is preserved.
func (ins *ActionInspector) SetSelection(snap app.Snapshot, ctrl deck.ControllerType, coords deck.Coordinates, dial int) {
	if ins.stepMode {
		return
	}
	ins.refreshPaletteSwatches(snap.ColorPalette)
	ins.selCtrl = ctrl
	ins.selCoords = coords
	ins.selDial = dial

	var rendered *app.RenderedSlot
	if ctrl == deck.ControllerEncoder {
		ins.slotLbl.SetText(fmt.Sprintf("Controller: Encoder Dial %d", dial+1))
		if r, ok := snap.DialSlots[dial]; ok {
			rcopy := r
			rendered = &rcopy
		}
	} else {
		ins.slotLbl.SetText(fmt.Sprintf("Slot: Row %d, Col %d (Keypad)", coords.Row, coords.Col))
		if r, ok := snap.Slots[coords.SlotKey()]; ok {
			rcopy := r
			rendered = &rcopy
		}
	}

	if rendered == nil {
		if ins.boundContext == "" && ins.boundActionID == "" && !ins.boundAutoBack && len(ins.formBox.Objects) > 0 {
			ins.boundHistoryRev = snap.HistoryRevision
			return
		}
		ins.boundContext = ""
		ins.boundActionID = ""
		ins.boundAutoBack = false
		ins.boundHistoryRev = snap.HistoryRevision
		ins.curSettings = nil
		ins.clearForm("Unassigned", "Select an action in the Action Catalog on the left to bind it to this slot.", false)
		return
	}

	if rendered.AutoBack {
		if ins.boundAutoBack && ins.boundContext == "" {
			ins.boundHistoryRev = snap.HistoryRevision
			return
		}
		ins.boundContext = ""
		ins.boundActionID = ""
		ins.boundAutoBack = true
		ins.boundHistoryRev = snap.HistoryRevision
		ins.curSettings = nil
		ins.clearForm("Auto Folder Back", "Automatic navigation button created inside folders when (0, 0) is empty.", false)
		return
	}

	cfg := rendered.Config
	if cfg == nil {
		return
	}

	sameBinding := ins.boundContext == cfg.Context &&
		ins.boundActionID == cfg.ActionUUID &&
		!ins.boundAutoBack &&
		ins.boundDisabled == rendered.Disabled &&
		ins.boundUnavailable == rendered.Unavailable &&
		ins.boundHasSchema == (rendered.Action != nil) &&
		ins.boundHistoryRev == snap.HistoryRevision
	ins.boundContext = cfg.Context
	ins.boundActionID = cfg.ActionUUID
	ins.boundAutoBack = false
	ins.boundDisabled = rendered.Disabled
	ins.boundUnavailable = rendered.Unavailable
	ins.boundHasSchema = rendered.Action != nil
	ins.boundPluginUUID = rendered.PluginUUID
	ins.boundHistoryRev = snap.HistoryRevision
	ins.curSettings = cfg.Settings

	if sameBinding {
		return
	}
	ins.rebuildForm(rendered)
}

func (ins *ActionInspector) clearForm(title, message string, canTrigger bool) {
	ins.saveSectionOpenState()
	ins.accordion = nil
	ins.actionLbl.SetText(title)
	ins.metaLbl.SetText(message)
	ins.boundDisabled = false
	ins.boundUnavailable = false
	ins.boundHasSchema = false
	ins.boundPluginUUID = ""
	ins.slotInitialSnap = ""
	ins.schemaForm.reset()
	ins.titleEntry = nil
	ins.accentColorCtrl = nil
	ins.bgColorCtrl = nil
	ins.bgImageCtrl = nil
	ins.hideTitleCheck = nil
	ins.hotkeyEntry = nil
	ins.hotkeyCaptureBtn = nil
	ins.openPIBtn = nil
	hint := widget.NewLabel("No configurable properties for this slot.")
	hint.Wrapping = fyne.TextWrapWord
	ins.formBox.Objects = []fyne.CanvasObject{hint, layout.NewSpacer()}
	ins.formBox.Refresh()

	ins.saveBtn.Disable()
	ins.clearBtn.Disable()
	if ins.moveBtn != nil {
		ins.moveBtn.Disable()
	}
	if ins.copyBtn != nil {
		ins.copyBtn.Disable()
	}
	if canTrigger {
		ins.triggerBtn.Enable()
	} else {
		ins.triggerBtn.Disable()
	}
}

func (ins *ActionInspector) rebuildForm(rendered *app.RenderedSlot) {
	ins.syncing = true
	defer func() { ins.syncing = false }()

	cfg := rendered.Config
	desc := rendered.Action

	ins.saveSectionOpenState()
	ins.accordion = nil
	ins.schemaForm.reset()
	ins.hotkeyCaptureBtn = nil
	ins.openPIBtn = nil
	name := cfg.ActionUUID
	meta := string(plugin.ProviderBuiltin)
	var schema []plugin.SettingField
	switch {
	case desc != nil:
		name = desc.Name
		meta = fmt.Sprintf("%s • %s", desc.Category, desc.Provider)
		if desc.Tooltip != "" {
			meta += "\n" + desc.Tooltip
		}
		schema = desc.SettingsSchema
	case rendered.Disabled:
		label := strings.TrimSpace(rendered.PluginName)
		if label == "" {
			label = rendered.PluginUUID
		}
		meta = fmt.Sprintf("Disabled Feature (%s) • %s", label, cfg.ActionUUID)
	case rendered.Unavailable:
		meta = "Missing Plugin • " + cfg.ActionUUID
	}

	ins.actionLbl.SetText(name)
	ins.metaLbl.SetText(meta)

	ins.titleEntry = widget.NewEntry()
	ins.titleEntry.SetPlaceHolder("Tile label")
	ins.titleEntry.SetText(cfg.Title)

	paletteBtn := widget.NewButton("Palette...", func() {
		if ins.onOpenPaletteSettings != nil {
			ins.onOpenPaletteSettings()
		}
	})
	accentHeader := container.NewBorder(nil, nil, nil, paletteBtn, widget.NewLabel("Accent Color"))
	ins.accentColorCtrl = newColorFieldControl(
		ins.win,
		"Accent Color",
		cfg.Color,
		ins.palette,
		ins.engine.AddColorSwatch,
		ins.reportStatus,
	)

	ins.bgColorCtrl = newColorFieldControl(
		ins.win,
		"Background Color",
		cfg.BackgroundColor,
		ins.palette,
		ins.engine.AddColorSwatch,
		ins.reportStatus,
	)

	var hotkeyRow fyne.CanvasObject
	if ins.selCtrl == deck.ControllerKeypad {
		ins.bgImageCtrl = newImageFieldControl(
			ins.win,
			cfg.BackgroundImage,
			func() string { return ins.assetsDir },
			ins.reportStatus,
		)
		ins.hideTitleCheck = widget.NewCheck("Hide Title on Button", nil)
		ins.hideTitleCheck.SetChecked(cfg.HideTitle)

		ins.hotkeyEntry = widget.NewEntry()
		ins.hotkeyEntry.SetPlaceHolder("e.g. Ctrl+Shift+F1 or F13 (global)")
		ins.hotkeyEntry.SetText(cfg.Hotkey)
		ins.hotkeyCaptureBtn = widget.NewButton("Capture", func() {
			ins.openHotkeyCapture("Capture Global Hotkey", ins.hotkeyEntry, captureModeGlobal)
		})
		hotkeyRow = container.NewBorder(nil, nil, nil, ins.hotkeyCaptureBtn, ins.hotkeyEntry)
	} else {
		ins.bgImageCtrl = nil
		ins.hideTitleCheck = nil
		ins.hotkeyEntry = nil
		ins.hotkeyCaptureBtn = nil
	}

	var items []fyne.CanvasObject
	switch {
	case rendered.Disabled:
		label := strings.TrimSpace(rendered.PluginName)
		if label == "" {
			label = rendered.PluginUUID
		}
		warnLbl := widget.NewLabel(fmt.Sprintf(
			"Feature %q is currently disabled. Your saved button settings are preserved.",
			label,
		))
		warnLbl.Wrapping = fyne.TextWrapWord
		warnLbl.Importance = widget.WarningImportance
		items = append(items, warnLbl)
		if ins.onSetPluginEnabled != nil && rendered.PluginUUID != "" {
			pid := rendered.PluginUUID
			enableBtn := widget.NewButton("Enable "+label, func() {
				if err := ins.onSetPluginEnabled(pid, true); err != nil {
					ins.reportStatus("Enable failed: " + err.Error())
				}
			})
			enableBtn.Importance = widget.HighImportance
			items = append(items, enableBtn)
		}
		items = append(items, widget.NewSeparator())
	case rendered.Unavailable:
		warnLbl := widget.NewLabel("The plugin for this action is not currently installed. Your saved button settings are preserved.")
		warnLbl.Wrapping = fyne.TextWrapWord
		warnLbl.Importance = widget.WarningImportance
		items = append(items, warnLbl, widget.NewSeparator())
	}

	if ins.sdCtrl != nil && desc != nil {
		if reqMajor, needsNode := ins.sdCtrl.NeedsNodeForAction(cfg.ActionUUID); needsNode {
			warnLbl := widget.NewLabel(fmt.Sprintf(
				"Node.js %d+ runtime is required for this external plugin to run. You can install or configure Node.js in Settings -> Plugins.",
				reqMajor,
			))
			warnLbl.Wrapping = fyne.TextWrapWord
			warnLbl.Importance = widget.WarningImportance
			installBtn := widget.NewButtonWithIcon("Install Node.js in Settings", theme.DownloadIcon(), func() {
				if ins.onOpenPluginsSettings != nil {
					ins.onOpenPluginsSettings()
				}
			})
			installBtn.Importance = widget.HighImportance
			items = append(items, warnLbl, installBtn, widget.NewSeparator())
		}
	}

	grouped := groupSchemaFields(schema)
	var accItems []*widget.AccordionItem

	genObjs := make([]fyne.CanvasObject, 0, 4+len(grouped.general)*2)
	genObjs = append(genObjs, widget.NewLabel("Title"), ins.titleEntry)
	if cfg.ActionUUID == core.ActionMultiAction {
		genObjs = append(genObjs, ins.buildMultiActionSummaryCard(cfg.Settings)...)
	}
	genObjs = append(genObjs, ins.buildSchemaObjects(grouped.general, cfg.Settings)...)
	accItems = append(accItems, ins.makeAccordionSection(plugin.SectionGeneral, genObjs))

	for _, cg := range grouped.custom {
		accItems = append(accItems, ins.makeAccordionSection(cg.title, ins.buildSchemaObjects(cg.fields, cfg.Settings)))
	}

	appObjs := []fyne.CanvasObject{
		accentHeader,
		ins.accentColorCtrl.Object(),
		widget.NewLabel("Background Color"),
		ins.bgColorCtrl.Object(),
	}
	if ins.selCtrl == deck.ControllerKeypad {
		appObjs = append(appObjs,
			widget.NewLabel("Background Image"),
			ins.bgImageCtrl.Object(),
			ins.hideTitleCheck,
		)
	}
	appObjs = append(appObjs, ins.buildSchemaObjects(grouped.appearance, cfg.Settings)...)
	accItems = append(accItems, ins.makeAccordionSection(plugin.SectionAppearance, appObjs))

	if ins.selCtrl == deck.ControllerKeypad || len(grouped.trigger) > 0 {
		var trigObjs []fyne.CanvasObject
		if ins.selCtrl == deck.ControllerKeypad {
			trigObjs = append(trigObjs, widget.NewLabel("Global Hotkey"), hotkeyRow)
		}
		trigObjs = append(trigObjs, ins.buildSchemaObjects(grouped.trigger, cfg.Settings)...)
		accItems = append(accItems, ins.makeAccordionSection(plugin.SectionTrigger, trigObjs))
	}

	if len(grouped.advanced) > 0 {
		advObjs := ins.buildSchemaObjects(grouped.advanced, cfg.Settings)
		accItems = append(accItems, ins.makeAccordionSection(plugin.SectionAdvanced, advObjs))
	}

	ins.accordion = widget.NewAccordion(accItems...)
	ins.accordion.MultiOpen = true
	items = append(items, ins.accordion)

	if _, ok := ins.engine.PropertyInspectorSession(ins.selCtrl, ins.selCoords, ins.selDial); ok {
		ins.openPIBtn = widget.NewButtonWithIcon("Open Full Property Inspector", theme.DocumentCreateIcon(), func() {
			if ins.piViewer == nil {
				if ins.onStatus != nil {
					ins.onStatus("Property Inspector viewer is unavailable")
				}
				return
			}
			sess, ok := ins.engine.PropertyInspectorSession(ins.selCtrl, ins.selCoords, ins.selDial)
			if !ok {
				if ins.onStatus != nil {
					ins.onStatus("Property Inspector session is no longer available")
				}
				return
			}
			if err := ins.piViewer.Open(sess.Title, sess.URL); err != nil {
				if ins.onStatus != nil {
					ins.onStatus(fmt.Sprintf("Open Property Inspector failed: %v", err))
				}
				return
			}
			if ins.onStatus != nil {
				ins.onStatus("Opened Property Inspector: " + sess.Title)
			}
		})
		items = append(items, widget.NewSeparator(), ins.openPIBtn)
	}

	ins.formBox.Objects = items
	ins.formBox.Refresh()
	ins.formScroll.ScrollToTop()

	ins.saveBtn.Enable()
	if desc != nil && !rendered.Disabled && !rendered.Unavailable {
		ins.triggerBtn.Enable()
	} else {
		ins.triggerBtn.Disable()
	}
	ins.clearBtn.Enable()
	if ins.moveBtn != nil {
		ins.moveBtn.Enable()
	}
	if ins.copyBtn != nil {
		ins.copyBtn.Enable()
	}
	ins.slotInitialSnap = ins.snapshotSlotForm()
}

func (ins *ActionInspector) saveSectionOpenState() {
	if ins.accordion == nil {
		return
	}
	if ins.sectionOpen == nil {
		ins.sectionOpen = make(map[string]bool)
	}
	for _, item := range ins.accordion.Items {
		if item != nil {
			ins.sectionOpen[strings.ToLower(item.Title)] = item.Open
		}
	}
}

func (ins *ActionInspector) isSectionOpen(title string) bool {
	if open, ok := ins.sectionOpen[strings.ToLower(title)]; ok {
		return open
	}
	return !strings.EqualFold(title, plugin.SectionAdvanced)
}

func (ins *ActionInspector) makeAccordionSection(title string, objs []fyne.CanvasObject) *widget.AccordionItem {
	item := widget.NewAccordionItem(title, container.NewPadded(container.NewVBox(objs...)))
	item.Open = ins.isSectionOpen(title)
	return item
}

func (ins *ActionInspector) buildSchemaObjects(fields []plugin.SettingField, settings map[string]any) []fyne.CanvasObject {
	return ins.schemaForm.buildFields(fields, settings)
}

// RefreshDynamicOptions repopulates any live SelectEntry dropdowns from registered dynamic providers.
func (ins *ActionInspector) RefreshDynamicOptions() {
	ins.schemaForm.refreshDynamicOptions()
}

func (ins *ActionInspector) handleSave() {
	ins.SaveCurrent()
}

// SaveCurrent validates and persists the currently active slot or sequence step form.
// It returns true if saving succeeded (or there was nothing bound to save).
func (ins *ActionInspector) SaveCurrent() bool {
	if ins.syncing {
		return false
	}
	if ins.stepMode {
		settings, err := ins.collectStepSettings()
		if err != nil {
			ins.reportStatus(err.Error())
			return false
		}
		if ins.onStepSave != nil {
			if err := ins.onStepSave(ins.stepIndex, settings); err != nil {
				ins.reportStatus("Save step failed: " + err.Error())
				return false
			}
		}
		ins.stepData.Settings = cloneStepSettingsMap(settings)
		ins.stepInitialSnap = ins.snapshotStepForm()
		ins.reportStatus("Saved step " + ins.actionLbl.Text)
		return true
	}
	if ins.boundActionID == "" {
		return true
	}
	title := ""
	if ins.titleEntry != nil {
		title = strings.TrimSpace(ins.titleEntry.Text)
	}
	var colHex string
	if ins.accentColorCtrl != nil {
		colHex = ins.accentColorCtrl.Value()
	}
	var bgColHex string
	if ins.bgColorCtrl != nil {
		bgColHex = ins.bgColorCtrl.Value()
	}
	var bgImage string
	if ins.bgImageCtrl != nil {
		bgImage = ins.bgImageCtrl.Value()
	}
	hideTitle := ins.hideTitleCheck != nil && ins.hideTitleCheck.Checked

	var settings map[string]any
	if ins.boundHasSchema {
		var err error
		settings, err = ins.schemaForm.collectAndValidate()
		if err != nil {
			ins.reportStatus(err.Error())
			return false
		}
	}

	canonicalHotkey := ""
	if ins.selCtrl == deck.ControllerKeypad && ins.hotkeyEntry != nil {
		rawHK := strings.TrimSpace(ins.hotkeyEntry.Text)
		if rawHK != "" {
			spec, hkErr := hotkey.Parse(rawHK)
			if hkErr != nil {
				ins.reportStatus("Invalid hotkey: " + hkErr.Error())
				return false
			}
			canonicalHotkey = spec.String()
			ins.hotkeyEntry.SetText(canonicalHotkey)
		}
	}

	appearance := app.SlotAppearance{
		Title:           title,
		Color:           colHex,
		BackgroundColor: bgColHex,
		BackgroundImage: bgImage,
		HideTitle:       hideTitle,
		SetHotkey:       true,
		Hotkey:          canonicalHotkey,
	}

	var saveErr error
	if ins.selCtrl == deck.ControllerEncoder {
		saveErr = ins.engine.UpdateDialSlotAppearance(ins.selDial, appearance, settings)
	} else {
		saveErr = ins.engine.UpdateKeypadSlotAppearance(ins.selCoords, appearance, settings)
	}
	if saveErr != nil {
		ins.reportStatus("Save failed: " + saveErr.Error())
		return false
	}
	ins.slotInitialSnap = ins.snapshotSlotForm()
	ins.reportStatus("Saved " + ins.actionLbl.Text)
	return true
}

// HasUnsavedSlotChanges reports whether the currently bound button or dial form has unpersisted edits.
func (ins *ActionInspector) HasUnsavedSlotChanges() bool {
	if ins.stepMode || ins.boundActionID == "" || ins.boundAutoBack || ins.slotInitialSnap == "" {
		return false
	}
	return ins.snapshotSlotForm() != ins.slotInitialSnap
}

// HasUnsavedStepChanges reports whether the currently bound sequence step form has unpersisted edits.
func (ins *ActionInspector) HasUnsavedStepChanges() bool {
	if !ins.stepMode || ins.stepInitialSnap == "" {
		return false
	}
	return ins.snapshotStepForm() != ins.stepInitialSnap
}

// UnsavedTargetName returns a display label for the slot or action currently being edited.
func (ins *ActionInspector) UnsavedTargetName() string {
	if ins.titleEntry != nil {
		if t := strings.TrimSpace(ins.titleEntry.Text); t != "" {
			return t
		}
	}
	if ins.actionLbl != nil {
		if t := strings.TrimSpace(ins.actionLbl.Text); t != "" {
			return t
		}
	}
	return "current action"
}

// DiscardSlotChanges drops tracking for pending slot edits and forces the next SetSelection to reload saved values.
func (ins *ActionInspector) DiscardSlotChanges() {
	ins.slotInitialSnap = ""
	ins.InvalidateBinding()
}

// FlushStepChanges validates and auto-saves any pending edits on the active sequence step.
func (ins *ActionInspector) FlushStepChanges() error {
	if !ins.HasUnsavedStepChanges() {
		return nil
	}
	settings, err := ins.collectStepSettings()
	if err != nil {
		ins.reportStatus(err.Error())
		return err
	}
	if ins.onStepSave != nil {
		if err := ins.onStepSave(ins.stepIndex, settings); err != nil {
			ins.reportStatus("Auto-save step failed: " + err.Error())
			return err
		}
	}
	ins.stepData.Settings = cloneStepSettingsMap(settings)
	ins.stepInitialSnap = ins.snapshotStepForm()
	ins.reportStatus("Auto-saved step " + ins.actionLbl.Text)
	return nil
}

type slotFormSnapshot struct {
	Title     string         `json:"title"`
	Color     string         `json:"color"`
	BgColor   string         `json:"bgColor"`
	BgImage   string         `json:"bgImage"`
	HideTitle bool           `json:"hideTitle"`
	Hotkey    string         `json:"hotkey"`
	Settings  map[string]any `json:"settings,omitempty"`
}

func (ins *ActionInspector) snapshotSlotForm() string {
	if ins.boundActionID == "" || ins.boundAutoBack {
		return ""
	}
	snap := slotFormSnapshot{}
	if ins.titleEntry != nil {
		snap.Title = strings.TrimSpace(ins.titleEntry.Text)
	}
	if ins.accentColorCtrl != nil {
		snap.Color = ins.accentColorCtrl.Value()
	}
	if ins.bgColorCtrl != nil {
		snap.BgColor = ins.bgColorCtrl.Value()
	}
	if ins.bgImageCtrl != nil {
		snap.BgImage = ins.bgImageCtrl.Value()
	}
	snap.HideTitle = ins.hideTitleCheck != nil && ins.hideTitleCheck.Checked
	if ins.selCtrl == deck.ControllerKeypad && ins.hotkeyEntry != nil {
		snap.Hotkey = strings.TrimSpace(ins.hotkeyEntry.Text)
	}
	if ins.boundHasSchema {
		snap.Settings = ins.schemaForm.collectSettings()
	}
	encoded, err := json.Marshal(snap)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func (ins *ActionInspector) snapshotStepForm() string {
	if !ins.stepMode {
		return ""
	}
	if ins.stepDesc != nil && len(ins.stepDesc.SettingsSchema) == 0 && ins.stepDesc.Provider != plugin.ProviderStreamDeck {
		return ""
	}
	if !ins.stepNoSchema {
		encoded, err := json.Marshal(ins.schemaForm.collectSettings())
		if err != nil {
			return ""
		}
		return string(encoded)
	}
	if ins.stepRawJSONEntry != nil {
		trimmed := strings.TrimSpace(ins.stepRawJSONEntry.Text)
		if trimmed == "" {
			return "{}"
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
			if parsed == nil {
				parsed = make(map[string]any)
			}
			encoded, mErr := json.Marshal(parsed)
			if mErr == nil {
				return string(encoded)
			}
		}
		return "raw:" + trimmed
	}
	return ""
}

func (ins *ActionInspector) handleTrigger() {
	if ins.stepMode {
		stepCopy := cloneMultiActionStep(ins.stepData)
		if settings, err := ins.collectStepSettings(); err == nil && settings != nil {
			stepCopy.Settings = settings
		}
		if ins.onStepTest != nil {
			ins.onStepTest(ins.stepIndex, stepCopy)
		}
		return
	}
	if ins.boundActionID == "" {
		return
	}
	var err error
	if ins.selCtrl == deck.ControllerEncoder {
		err = ins.engine.PressDial(context.Background(), ins.selDial)
	} else {
		err = ins.engine.PressKey(context.Background(), ins.selCoords)
	}
	if err != nil {
		ins.reportStatus("Trigger failed: " + err.Error())
		return
	}
	ins.reportStatus("Triggered " + ins.actionLbl.Text)
}

func (ins *ActionInspector) handleClear() {
	if ins.stepMode {
		if ins.onStepDelete != nil {
			ins.onStepDelete(ins.stepIndex)
		}
		return
	}
	var err error
	if ins.selCtrl == deck.ControllerEncoder {
		err = ins.engine.ClearDialSlot(ins.selDial)
	} else {
		err = ins.engine.ClearKeypadSlot(ins.selCoords)
	}
	if err != nil {
		ins.reportStatus("Clear failed: " + err.Error())
		return
	}
	ins.reportStatus("Cleared slot")
}

func (ins *ActionInspector) reportStatus(msg string) {
	if ins.onStatus != nil {
		ins.onStatus(msg)
	}
}

func (ins *ActionInspector) openHotkeyCapture(title string, target *widget.Entry, mode hotkeyCaptureMode) {
	if ins.win == nil || target == nil {
		return
	}
	ins.lastCapture = openHotkeyCaptureDialog(
		ins.win,
		title,
		target.Text,
		mode,
		ins.onSuspendHotkeys,
		func(captured string) {
			target.SetText(captured)
			if captured == "" {
				ins.reportStatus("Cleared shortcut — click Save to apply")
				return
			}
			ins.reportStatus("Captured " + captured + " — click Save to apply")
		},
	)
}

// SetStepSelection switches the inspector into Step Mode to edit a single Multi-Action block's settings.
func (ins *ActionInspector) SetStepSelection(
	idx, total int,
	step core.MultiActionStep,
	desc *plugin.ActionDescriptor,
	cb stepInspectorCallbacks,
) {
	ins.syncing = true
	defer func() { ins.syncing = false }()

	ins.saveSectionOpenState()
	ins.accordion = nil
	ins.schemaForm.reset()
	ins.titleEntry = nil
	ins.accentColorCtrl = nil
	ins.bgColorCtrl = nil
	ins.bgImageCtrl = nil
	ins.hideTitleCheck = nil
	ins.hotkeyEntry = nil
	ins.hotkeyCaptureBtn = nil
	ins.openPIBtn = nil
	ins.stepRawJSONEntry = nil

	ins.stepMode = true
	ins.stepIndex = idx
	ins.stepTotal = total
	ins.stepData = cloneMultiActionStep(step)
	ins.stepDesc = desc
	ins.onStepSave = cb.onSave
	ins.onStepTest = cb.onTest
	ins.onStepDelete = cb.onDelete
	ins.onStepBackToButton = cb.onBackToButton

	ins.slotLbl.SetText(fmt.Sprintf("Sequence Step %d of %d", idx+1, total))

	name := step.ActionUUID
	meta := "Missing Plugin • " + step.ActionUUID
	if desc != nil {
		name = desc.Name
		meta = fmt.Sprintf("%s • %s", desc.Category, desc.Provider)
		if desc.Tooltip != "" {
			meta += "\n" + desc.Tooltip
		}
	}
	if step.Disabled {
		meta = "[Muted] " + meta
	}
	ins.actionLbl.SetText(name)
	ins.metaLbl.SetText(meta)

	backBtn := widget.NewButtonWithIcon("Button Properties", theme.NavigateBackIcon(), func() {
		if ins.onStepBackToButton != nil {
			ins.onStepBackToButton()
		}
	})
	items := []fyne.CanvasObject{backBtn, widget.NewSeparator()}

	switch {
	case desc == nil:
		ins.stepNoSchema = true
		warnLbl := widget.NewLabel("The plugin for this step is not currently installed. Saved settings are preserved.")
		warnLbl.Wrapping = fyne.TextWrapWord
		warnLbl.Importance = widget.WarningImportance
		ins.stepRawJSONEntry = widget.NewMultiLineEntry()
		ins.stepRawJSONEntry.SetMinRowsVisible(6)
		ins.stepRawJSONEntry.SetText(formatStepSettingsJSON(step.Settings))
		items = append(items, warnLbl, widget.NewLabel("Step Settings JSON"), ins.stepRawJSONEntry)

	case len(desc.SettingsSchema) > 0:
		ins.stepNoSchema = false
		grouped := groupSchemaFields(desc.SettingsSchema)
		var accItems []*widget.AccordionItem
		if len(grouped.general) > 0 {
			accItems = append(accItems, ins.makeAccordionSection(plugin.SectionGeneral, ins.buildSchemaObjects(grouped.general, step.Settings)))
		}
		for _, cg := range grouped.custom {
			accItems = append(accItems, ins.makeAccordionSection(cg.title, ins.buildSchemaObjects(cg.fields, step.Settings)))
		}
		if len(grouped.appearance) > 0 {
			accItems = append(accItems, ins.makeAccordionSection(plugin.SectionAppearance, ins.buildSchemaObjects(grouped.appearance, step.Settings)))
		}
		if len(grouped.trigger) > 0 {
			accItems = append(accItems, ins.makeAccordionSection(plugin.SectionTrigger, ins.buildSchemaObjects(grouped.trigger, step.Settings)))
		}
		if len(grouped.advanced) > 0 {
			accItems = append(accItems, ins.makeAccordionSection(plugin.SectionAdvanced, ins.buildSchemaObjects(grouped.advanced, step.Settings)))
		}
		ins.accordion = widget.NewAccordion(accItems...)
		ins.accordion.MultiOpen = true
		items = append(items, ins.accordion)

	case desc.Provider == plugin.ProviderStreamDeck:
		ins.stepNoSchema = true
		hint := widget.NewLabel("External Stream Deck action without a native form. Edit raw step settings JSON:")
		hint.Wrapping = fyne.TextWrapWord
		ins.stepRawJSONEntry = widget.NewMultiLineEntry()
		ins.stepRawJSONEntry.SetMinRowsVisible(6)
		ins.stepRawJSONEntry.SetText(formatStepSettingsJSON(step.Settings))
		items = append(items, hint, widget.NewLabel("Step Settings JSON"), ins.stepRawJSONEntry)

	default:
		ins.stepNoSchema = true
		noCfg := widget.NewLabel("This action has no configurable settings.")
		noCfg.Wrapping = fyne.TextWrapWord
		items = append(items, noCfg)
	}

	ins.formBox.Objects = items
	ins.formBox.Refresh()
	ins.formScroll.ScrollToTop()

	if ins.moveCopyRow != nil {
		ins.moveCopyRow.Hide()
	}
	ins.clearBtn.SetText("Remove")
	ins.clearBtn.Enable()
	if desc != nil {
		ins.triggerBtn.Enable()
	} else {
		ins.triggerBtn.Disable()
	}
	if desc != nil && len(desc.SettingsSchema) == 0 && desc.Provider != plugin.ProviderStreamDeck {
		ins.saveBtn.Disable()
	} else {
		ins.saveBtn.Enable()
	}
	ins.stepInitialSnap = ins.snapshotStepForm()
}

// SyncStepSettings updates the active step inspector's field values in-place without rebuilding controls.
func (ins *ActionInspector) SyncStepSettings(settings map[string]any) {
	if !ins.stepMode {
		return
	}
	ins.stepData.Settings = cloneStepSettingsMap(settings)
	if !ins.stepNoSchema {
		ins.schemaForm.setFieldValues(settings)
	} else if ins.stepRawJSONEntry != nil {
		formatted := formatStepSettingsJSON(settings)
		if ins.stepRawJSONEntry.Text != formatted {
			ins.stepRawJSONEntry.SetText(formatted)
		}
	}
	ins.stepInitialSnap = ins.snapshotStepForm()
}

// ClearStepSelection exits Step Mode and restores normal slot inspection controls.
func (ins *ActionInspector) ClearStepSelection() {
	if !ins.stepMode {
		return
	}
	ins.stepMode = false
	ins.stepIndex = 0
	ins.stepTotal = 0
	ins.stepData = core.MultiActionStep{}
	ins.stepDesc = nil
	ins.stepNoSchema = false
	ins.stepRawJSONEntry = nil
	ins.stepInitialSnap = ""
	ins.onStepSave = nil
	ins.onStepTest = nil
	ins.onStepDelete = nil
	ins.onStepBackToButton = nil
	ins.clearBtn.SetText("Clear")
	if ins.moveCopyRow != nil {
		ins.moveCopyRow.Show()
	}
	ins.InvalidateBinding()
}

func (ins *ActionInspector) collectStepSettings() (map[string]any, error) {
	if !ins.stepNoSchema {
		return ins.schemaForm.collectAndValidate()
	}
	if ins.stepRawJSONEntry != nil {
		trimmed := strings.TrimSpace(ins.stepRawJSONEntry.Text)
		if trimmed == "" {
			return make(map[string]any), nil
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
			return nil, fmt.Errorf("invalid step settings JSON: %w", err)
		}
		if parsed == nil {
			parsed = make(map[string]any)
		}
		return parsed, nil
	}
	return cloneStepSettingsMap(ins.stepData.Settings), nil
}

func (ins *ActionInspector) buildMultiActionSummaryCard(settings map[string]any) []fyne.CanvasObject {
	cardBg := canvas.NewRectangle(ColorElevatedSlate)
	cardBg.CornerRadius = 8
	cardBg.StrokeColor = ColorSlateBorder
	cardBg.StrokeWidth = 1

	box := container.NewVBox()
	rawSteps, err := core.ParseMultiActionSteps(settings)
	if err != nil {
		warn := widget.NewLabel("Invalid sequence JSON (inspect under Advanced)")
		warn.Wrapping = fyne.TextWrapWord
		warn.Importance = widget.WarningImportance
		box.Add(warn)
	} else {
		steps := core.NormalizeMultiActionSteps(rawSteps)
		headerText := "Sequence (Empty)"
		if len(steps) > 0 {
			estMs := core.MultiActionEstimatedDurationMs(steps)
			if estMs > 0 {
				headerText = fmt.Sprintf("Sequence (%d blocks · ~%s)", len(steps), formatDurationCompact(estMs))
			} else {
				headerText = fmt.Sprintf("Sequence (%d blocks)", len(steps))
			}
		}
		hdrLbl := widget.NewLabelWithStyle(headerText, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
		box.Add(hdrLbl)

		const maxPreview = 4
		reg := ins.engine.Registry()
		for i, st := range steps {
			if i >= maxPreview {
				moreLbl := widget.NewLabel(fmt.Sprintf("… +%d more block(s)", len(steps)-maxPreview))
				box.Add(moreLbl)
				break
			}
			var desc *plugin.ActionDescriptor
			if reg != nil {
				if d, ok := reg.Action(st.ActionUUID); ok {
					dcopy := d
					desc = &dcopy
				}
			}
			name := st.ActionUUID
			if st.ActionUUID == core.ActionDelay {
				name = "Wait"
			} else if desc != nil && desc.Name != "" {
				name = desc.Name
			}
			line := fmt.Sprintf("%d. %s — %s", i+1, name, summarizeMultiActionStep(desc, st))
			lbl := widget.NewLabel(line)
			lbl.Truncation = fyne.TextTruncateEllipsis
			box.Add(lbl)
		}
	}

	if ins.editingMultiAction {
		editingLbl := widget.NewLabel("Editing sequence in center canvas — click a block to configure its settings.")
		editingLbl.Wrapping = fyne.TextWrapWord
		box.Add(editingLbl)
	} else {
		editBtn := widget.NewButtonWithIcon("Edit Sequence...", theme.ListIcon(), func() {
			if ins.onEditMultiAction != nil {
				ins.onEditMultiAction()
			}
		})
		editBtn.Importance = widget.HighImportance
		box.Add(editBtn)
	}

	card := container.NewStack(cardBg, container.NewPadded(box))
	return []fyne.CanvasObject{card}
}

func formatStepSettingsJSON(settings map[string]any) string {
	if len(settings) == 0 {
		return "{}"
	}
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(encoded)
}
