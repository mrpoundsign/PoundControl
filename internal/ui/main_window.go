package ui

import (
	"bytes"
	"context"
	"fmt"
	"image/color"
	"image/png"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/mrpoundsign/PoundControl/internal/app"
	"github.com/mrpoundsign/PoundControl/internal/version"
	"github.com/mrpoundsign/PoundControl/internal/webdeck"
	"github.com/mrpoundsign/PoundControl/pkg/builtin/obs"
	"github.com/mrpoundsign/PoundControl/pkg/deck"
	"github.com/mrpoundsign/PoundControl/pkg/plugin"
	"github.com/mrpoundsign/PoundControl/pkg/sdplugin"
	"github.com/mrpoundsign/PoundControl/pkg/tile"
)

// dispatchUI marshals UI state updates onto the Fyne main thread.
var dispatchUI = fyne.Do

type gridPreset struct {
	label string
	grid  deck.GridSize
}

const customGridPresetLabel = "Custom"

var studioGridPresets = []gridPreset{
	{label: "3×2 (Mini)", grid: deck.GridSize{Cols: 3, Rows: 2}},
	{label: "4×2 (Compact)", grid: deck.GridSize{Cols: 4, Rows: 2}},
	{label: "5×2 (Phone)", grid: deck.GridSize{Cols: 5, Rows: 2}},
	{label: "5×3 (Standard)", grid: deck.GridSize{Cols: 5, Rows: 3}},
	{label: "6×4 (XL)", grid: deck.GridSize{Cols: 6, Rows: 4}},
	{label: "8×4 (XL+)", grid: deck.GridSize{Cols: 8, Rows: 4}},
}

const pageHoverFlipDelay = 450 * time.Millisecond

type slotClipMode uint8

const (
	clipNone slotClipMode = iota
	clipMove
	clipCopy
)

const (
	mainTabControl  = 0
	mainTabPlugins  = 1
	mainTabSettings = 2
)

// MainWindow hosts the PoundControl desktop studio and interactive virtual macro deck.
type MainWindow struct {
	app          fyne.App
	win          fyne.Window
	engine       *app.Engine
	unsub        func()
	obsCtrl         OBSController
	obsUnsub        func()
	sdCtrl          SDPluginController
	wasmCtrl        WasmPluginController
	webCtrl         WebDeckController
	webUnsub        func()
	catalogueClient *sdplugin.CatalogueClient
	piViewer        PIViewer
	popOut      *PopOutPad
	cfgPath         string
	hotkeyCount     func() int
	hotkeySuspender func(bool)

	selectedController deck.ControllerType
	selectedSlot       deck.Coordinates
	selectedDial       int
	runMode            bool
	syncing            bool
	suppressTabRebuild bool
	statusNote         string
	lastSnap           app.Snapshot
	profileIDs         []string

	profileSelect  *widget.Select
	undoBtn        *widget.Button
	redoBtn        *widget.Button
	obsBtn         *widget.Button
	webBtn         *widget.Button
	runModeCheck        *widget.Check
	mainTabs             *container.AppTabs
	pluginsTabItem       *container.TabItem
	settingsTabItem      *container.TabItem
	settingsTabs         *container.AppTabs
	settingsPluginTabs   map[string]int
	settingsPluginViews  map[string]*pluginConfigTabView
	pluginsCleanup       func()
	settingsCleanup      func()
	catalogSearch        *widget.Entry
	catalogClearBtn      *widget.Button
	catalogList          *fyne.Container
	catalogButtons       map[string]*widget.Button
	catalogGroupHeaders  map[string]*widget.Button
	catalogConfigButtons map[string]*widget.Button
	catalogCollapsed     map[string]bool
	catalogIconCache    map[string]fyne.Resource
	surface             *deckSurfaceView
	deckTitle      *canvas.Text
	deckHint       *canvas.Text
	inspector      *ActionInspector
	statusBarLbl   *widget.Label
	confirmDlg     *dialog.ConfirmDialog
	shortcuts      fyne.ShortcutHandler

	activePairAttemptID string
	pairDialog          *dialog.CustomDialog
	pairStatusLbl       *widget.Label
	pairApproveBtn      *widget.Button
	lastPairedCount     int

	pageStrip     *fyne.Container
	pageButtons   []*widget.Button
	pageAddBtn    *widget.Button
	pageDeleteBtn *widget.Button
	pageStripKey string

	clipBanner    *fyne.Container
	clipBannerBg  *canvas.Rectangle
	clipBannerLbl *widget.Label
	clipPasteBtn  *widget.Button
	clipCancelBtn *widget.Button

	clipMode         slotClipMode
	clipSrcLoc       app.SlotLocation
	clipSlot         *deck.SlotConfig
	clipSrcProfileID string
	clipLabel        string
	clipController   deck.ControllerType

	dragActive       bool
	dragSrcLoc       app.SlotLocation
	hasHoverDropKey  bool
	hoverDropKey     deck.Coordinates
	hasHoverDropDial bool
	hoverDropDial    int
	hoverPageIdx     int
	hoverAddPage     bool
	hoverPageTimer   *time.Timer
}

// NewMainWindow constructs and initializes the PoundControl studio window wired to engine.
func NewMainWindow(a fyne.App, engine *app.Engine) *MainWindow {
	win := a.NewWindow(version.AppName)
	win.Resize(fyne.NewSize(1240, 760))

	mw := &MainWindow{
		app:                a,
		win:                win,
		engine:             engine,
		selectedController: deck.ControllerKeypad,
		selectedSlot:       deck.Coordinates{Row: 0, Col: 0},
		statusNote:          "Ready",
		catalogButtons:       make(map[string]*widget.Button),
		catalogGroupHeaders:  make(map[string]*widget.Button),
		catalogConfigButtons: make(map[string]*widget.Button),
		catalogCollapsed:     make(map[string]bool),
		catalogIconCache:    make(map[string]fyne.Resource),
		hoverPageIdx:        -1,
	}

	mw.inspector = NewActionInspector(win, engine, mw.setStatus)
	mw.inspector.SetOnOpenPluginsSettings(func() {
		mw.ShowSettingsTab(settingsTabPlugins)
	})
	mw.inspector.SetOnOpenPaletteSettings(func() {
		mw.ShowSettingsTab(settingsTabGeneral)
	})
	mw.inspector.SetOnMoveSelected(mw.StartMoveSelected)
	mw.inspector.SetOnCopySelected(mw.CopySelected)

	mw.surface = newDeckSurfaceView(
		mw.handleKeyTap,
		mw.handleDialTap,
		mw.handleDialDelta,
		mw.handleDialPress,
	)
	mw.surface.SetEditHandlers(surfaceEditHandlers{
		canEdit:         func() bool { return !mw.runMode },
		onKeySecondary:  mw.handleKeySecondaryTap,
		onKeyDragMove:   mw.handleTileDragMove,
		onKeyDragEnd:    mw.handleTileDragEnd,
		onDialSecondary: mw.handleDialSecondaryTap,
		onDialDragMove:  mw.handleDialDragMove,
		onDialDragEnd:   mw.handleDialDragEnd,
	})

	header := mw.buildHeader()
	catalog := mw.buildActionCatalog()
	center := mw.buildDeckCenter()
	statusBar := mw.buildStatusBar()

	splitRight := container.NewHSplit(center, mw.inspector.Object())
	splitRight.SetOffset(0.67)

	splitMain := container.NewHSplit(catalog, splitRight)
	splitMain.SetOffset(0.23)

	mw.pluginsTabItem = container.NewTabItemWithIcon("Plugins", theme.FileApplicationIcon(), container.NewVBox())
	mw.settingsTabItem = container.NewTabItemWithIcon("Settings", theme.SettingsIcon(), container.NewVBox())
	mw.mainTabs = container.NewAppTabs(
		container.NewTabItemWithIcon("Control", theme.GridIcon(), splitMain),
		mw.pluginsTabItem,
		mw.settingsTabItem,
	)
	mw.mainTabs.OnSelected = func(item *container.TabItem) {
		if mw.suppressTabRebuild {
			return
		}
		switch item {
		case mw.pluginsTabItem:
			mw.rebuildPluginsTab()
		case mw.settingsTabItem:
			mw.rebuildSettingsTab(settingsSubTabGeneral)
		}
	}

	root := container.NewBorder(header, statusBar, nil, nil, mw.mainTabs)
	win.SetContent(root)

	mw.addCanvasShortcut(&fyne.ShortcutUndo{}, func(fyne.Shortcut) {
		if mw.isControlTabActive() && len(mw.win.Canvas().Overlays().List()) == 0 {
			mw.handleUndo()
		}
	})
	mw.addCanvasShortcut(&fyne.ShortcutRedo{}, func(fyne.Shortcut) {
		if mw.isControlTabActive() && len(mw.win.Canvas().Overlays().List()) == 0 {
			mw.handleRedo()
		}
	})
	mw.addCanvasShortcut(&desktop.CustomShortcut{
		KeyName:  fyne.KeyZ,
		Modifier: fyne.KeyModifierShortcutDefault | fyne.KeyModifierShift,
	}, func(fyne.Shortcut) {
		if mw.isControlTabActive() && len(mw.win.Canvas().Overlays().List()) == 0 {
			mw.handleRedo()
		}
	})
	mw.addCanvasShortcut(&fyne.ShortcutCut{}, func(s fyne.Shortcut) {
		if mw.forwardShortcutToFocused(s) || !mw.isControlTabActive() || len(mw.win.Canvas().Overlays().List()) > 0 {
			return
		}
		mw.StartMoveSelected()
	})
	mw.addCanvasShortcut(&fyne.ShortcutCopy{}, func(s fyne.Shortcut) {
		if mw.forwardShortcutToFocused(s) || !mw.isControlTabActive() || len(mw.win.Canvas().Overlays().List()) > 0 {
			return
		}
		mw.CopySelected()
	})
	mw.addCanvasShortcut(&fyne.ShortcutPaste{}, func(s fyne.Shortcut) {
		if mw.forwardShortcutToFocused(s) || !mw.isControlTabActive() || len(mw.win.Canvas().Overlays().List()) > 0 {
			return
		}
		mw.PasteSelected()
	})
	mw.addCanvasShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyEscape}, func(fyne.Shortcut) {
		if mw.isControlTabActive() && len(mw.win.Canvas().Overlays().List()) == 0 && mw.clipMode != clipNone {
			mw.ClearClipboard()
		}
	})
	win.Canvas().SetOnTypedKey(func(ev *fyne.KeyEvent) {
		if ev.Name == fyne.KeyEscape && mw.isControlTabActive() && len(mw.win.Canvas().Overlays().List()) == 0 && mw.clipMode != clipNone {
			mw.ClearClipboard()
		}
	})

	mw.applySnapshot(engine.Snapshot())
	mw.unsub = engine.Subscribe(func(snap app.Snapshot) {
		dispatchUI(func() {
			mw.applySnapshot(snap)
		})
	})

	win.SetOnClosed(func() {
		mw.cancelHoverPageTimer()
		if mw.pluginsCleanup != nil {
			mw.pluginsCleanup()
			mw.pluginsCleanup = nil
		}
		if mw.settingsCleanup != nil {
			mw.settingsCleanup()
			mw.settingsCleanup = nil
		}
		if mw.obsUnsub != nil {
			mw.obsUnsub()
			mw.obsUnsub = nil
		}
		if mw.webUnsub != nil {
			mw.webUnsub()
			mw.webUnsub = nil
		}
		if mw.unsub != nil {
			mw.unsub()
			mw.unsub = nil
		}
		if mw.popOut != nil {
			mw.popOut.Close()
			mw.popOut = nil
		}
		if mw.piViewer != nil {
			mw.piViewer.CloseAll()
		}
		if mw.pairDialog != nil {
			mw.pairDialog.Hide()
			mw.pairDialog = nil
		}
	})

	return mw
}

// Window returns the underlying Fyne window.
func (mw *MainWindow) Window() fyne.Window {
	return mw.win
}

// ShowAndRun displays the main window and starts the Fyne event loop.
func (mw *MainWindow) ShowAndRun() {
	mw.win.ShowAndRun()
}

// SelectedSlot returns the currently selected Keypad coordinates.
func (mw *MainWindow) SelectedSlot() deck.Coordinates {
	return mw.selectedSlot
}

// SelectedController returns whether a Keypad slot or Encoder dial is currently selected.
func (mw *MainWindow) SelectedController() deck.ControllerType {
	return mw.selectedController
}

// SelectedDial returns the 0-based index of the currently selected dial.
func (mw *MainWindow) SelectedDial() int {
	return mw.selectedDial
}

// SelectSlot focuses keypad coordinates c and updates the surface, inspector, and status bar.
// Out-of-bounds coordinates are ignored.
func (mw *MainWindow) SelectSlot(c deck.Coordinates) {
	snap := mw.engine.Snapshot()
	if !snap.Grid.Contains(c) {
		return
	}
	mw.selectedController = deck.ControllerKeypad
	mw.selectedSlot = c
	mw.applySnapshot(snap)
}

// SelectDial focuses encoder dial index and updates the surface, inspector, and status bar.
func (mw *MainWindow) SelectDial(dial int) {
	snap := mw.engine.Snapshot()
	if dial < 0 || dial >= snap.Dials {
		return
	}
	mw.selectedController = deck.ControllerEncoder
	mw.selectedDial = dial
	mw.applySnapshot(snap)
}

// SetRunMode toggles between Edit Mode (false) and Run Mode (true).
func (mw *MainWindow) SetRunMode(run bool) {
	mw.runMode = run
	if mw.runModeCheck != nil && mw.runModeCheck.Checked != run {
		mw.syncing = true
		mw.runModeCheck.SetChecked(run)
		mw.syncing = false
	}
	if run {
		if mw.clipMode == clipMove || mw.dragActive {
			mw.clearDragState()
			if mw.clipMode == clipMove {
				mw.ClearClipboard()
			}
		}
		mw.deckHint.Text = "Run Mode: clicking tiles triggers actions live"
		mw.setStatus("Run Mode enabled")
	} else {
		mw.deckHint.Text = "Edit Mode: click any slot to inspect, drag to move/swap, right-click for menu"
		mw.setStatus("Edit Mode enabled")
	}
	mw.deckHint.Refresh()
}

// OpenPopOutPad opens (or focuses) the floating Run Mode Pop-Out Pad window.
func (mw *MainWindow) OpenPopOutPad() *PopOutPad {
	if mw.popOut != nil {
		mw.popOut.Show()
		return mw.popOut
	}
	mw.popOut = NewPopOutPad(mw.app, mw.engine, func() {
		mw.popOut = nil
	})
	mw.popOut.Show()
	return mw.popOut
}

// SetConfigPath records the config file path displayed in the General settings tab.
func (mw *MainWindow) SetConfigPath(path string) {
	mw.cfgPath = path
	if mw.inspector != nil {
		if trimmed := strings.TrimSpace(path); trimmed != "" {
			mw.inspector.SetAssetsDir(filepath.Join(filepath.Dir(trimmed), "assets"))
		} else {
			mw.inspector.SetAssetsDir("")
		}
	}
}

// SetHotkeyCountProvider sets a callback returning active OS global hotkey registrations.
func (mw *MainWindow) SetHotkeyCountProvider(fn func() int) {
	mw.hotkeyCount = fn
}

// SetHotkeySuspender sets a callback invoked to suspend or resume global OS hotkeys while capturing shortcuts.
func (mw *MainWindow) SetHotkeySuspender(fn func(bool)) {
	mw.hotkeySuspender = fn
	if mw.inspector != nil {
		mw.inspector.SetHotkeySuspender(fn)
	}
}

func (mw *MainWindow) isControlTabActive() bool {
	if mw.mainTabs == nil {
		return true
	}
	return mw.mainTabs.SelectedIndex() == mainTabControl
}

// ShowControlTab switches the main Studio view to the Control (Virtual Deck) tab.
func (mw *MainWindow) ShowControlTab() {
	if mw.mainTabs != nil {
		mw.mainTabs.SelectIndex(mainTabControl)
	}
}

func (mw *MainWindow) selectMainTabNoRebuild(idx int) {
	if mw.mainTabs == nil {
		return
	}
	mw.suppressTabRebuild = true
	mw.mainTabs.SelectIndex(idx)
	mw.suppressTabRebuild = false
}

// ShowPluginsTab switches the main Studio view to the Plugins tab and refreshes its contents.
func (mw *MainWindow) ShowPluginsTab() {
	mw.rebuildPluginsTab()
	mw.selectMainTabNoRebuild(mainTabPlugins)
}

// ShowSettingsTab switches the main Studio view to the Plugins or Settings tab (selecting the requested sub-tab).
func (mw *MainWindow) ShowSettingsTab(tab int) {
	if tab == settingsTabPlugins {
		mw.ShowPluginsTab()
		return
	}
	sub := settingsSubTabGeneral
	switch tab {
	case settingsTabOBS:
		sub = settingsSubTabOBS
	case settingsTabWeb:
		sub = settingsSubTabWeb
	}
	mw.rebuildSettingsTab(sub)
	mw.selectMainTabNoRebuild(mainTabSettings)
}

// ShowPluginConfigTab switches the main Studio view to the Settings sub-tab for pluginUUID.
func (mw *MainWindow) ShowPluginConfigTab(pluginUUID string) {
	pid := strings.TrimSpace(pluginUUID)
	if pid == "" {
		return
	}
	if pid == obs.PluginUUID {
		mw.ShowSettingsTab(settingsTabOBS)
		return
	}
	mw.rebuildSettingsTab(settingsSubTabGeneral)
	if idx, ok := mw.settingsPluginTabs[pid]; ok && mw.settingsTabs != nil && idx >= 0 && idx < len(mw.settingsTabs.Items) {
		mw.settingsTabs.SelectIndex(idx)
	}
	mw.selectMainTabNoRebuild(mainTabSettings)
}

func (mw *MainWindow) rebuildPluginsTab() {
	if mw.pluginsTabItem == nil {
		return
	}
	if mw.pluginsCleanup != nil {
		mw.pluginsCleanup()
		mw.pluginsCleanup = nil
	}
	content, cleanup := buildPluginsSettingsTab(
		mw.win,
		mw.engine,
		mw.sdCtrl,
		mw.wasmCtrl,
		mw.catalogueClient,
		mw.piViewer,
		mw.ReloadExternalPlugins,
		mw.ShowPluginConfigTab,
		mw.setStatus,
	)
	mw.pluginsCleanup = cleanup
	mw.pluginsTabItem.Content = content
	if mw.mainTabs != nil {
		mw.mainTabs.Refresh()
	}
}

func (mw *MainWindow) rebuildSettingsTab(subTab int) {
	if mw.settingsTabItem == nil {
		return
	}
	if mw.settingsCleanup != nil {
		mw.settingsCleanup()
		mw.settingsCleanup = nil
	}
	hotkeys := 0
	if mw.hotkeyCount != nil {
		hotkeys = mw.hotkeyCount()
	}
	assetsDir := func() string {
		if trimmed := strings.TrimSpace(mw.cfgPath); trimmed != "" {
			return filepath.Join(filepath.Dir(trimmed), "assets")
		}
		return ""
	}
	res := buildSettingsTabsView(
		mw.win,
		mw.engine,
		mw.obsCtrl,
		mw.webCtrl,
		mw.cfgPath,
		assetsDir,
		hotkeys,
		func() { mw.OpenPopOutPad() },
		mw.hotkeySuspender,
		mw.setStatus,
	)
	if subTab >= 0 && subTab < len(res.tabs.Items) {
		res.tabs.SelectIndex(subTab)
	}
	mw.settingsTabs = res.tabs
	mw.settingsPluginTabs = res.pluginTabs
	mw.settingsPluginViews = res.pluginViews
	mw.settingsCleanup = res.cleanup
	mw.settingsTabItem.Content = res.tabs
	if mw.mainTabs != nil {
		mw.mainTabs.Refresh()
	}
}

// SetCatalogueClient sets an OpenAction Marketplace CatalogueClient (primarily for testing or custom endpoints).
func (mw *MainWindow) SetCatalogueClient(client *sdplugin.CatalogueClient) {
	mw.catalogueClient = client
}

// SetSDPluginController attaches an external .sdPlugin host controller and refreshes inspector hints.
func (mw *MainWindow) SetSDPluginController(ctrl SDPluginController) {
	mw.sdCtrl = ctrl
	if mw.inspector != nil {
		mw.inspector.SetSDPluginController(ctrl)
		mw.inspector.InvalidateBinding()
	}
	mw.applySnapshot(mw.engine.Snapshot())
}

// SetWasmPluginController attaches a WebAssembly (.wasm) plugin host controller for Settings management.
func (mw *MainWindow) SetWasmPluginController(ctrl WasmPluginController) {
	mw.wasmCtrl = ctrl
}

// SetPIViewer attaches the Property Inspector viewer for interactive HTML plugin views.
func (mw *MainWindow) SetPIViewer(v PIViewer) {
	mw.piViewer = v
	if mw.inspector != nil {
		mw.inspector.SetPIViewer(v)
	}
}

// ReloadExternalPlugins rescans .sdPlugin and .wasm plugins, refreshes the catalog, and updates visible tiles.
func (mw *MainWindow) ReloadExternalPlugins() error {
	if mw.sdCtrl != nil {
		if err := mw.sdCtrl.Reload(); err != nil {
			return err
		}
		if prov, ok := mw.sdCtrl.(plugin.Provider); ok {
			if err := mw.engine.Registry().RefreshProvider(prov); err != nil {
				return err
			}
		}
	}
	if mw.wasmCtrl != nil {
		if err := mw.wasmCtrl.Reload(); err != nil {
			return err
		}
		if prov, ok := mw.wasmCtrl.(plugin.Provider); ok {
			if err := mw.engine.Registry().RefreshProvider(prov); err != nil {
				return err
			}
		}
	}
	mw.engine.ReloadVisibleActions()
	mw.RefreshActionCatalog()
	if mw.inspector != nil {
		mw.inspector.InvalidateBinding()
	}
	mw.applySnapshot(mw.engine.Snapshot())
	return nil
}

// SetOBSController attaches an OBSController so the Studio header badge displays live OBS status
// and clicking it opens the OBS connection configuration dialog.
func (mw *MainWindow) SetOBSController(ctrl OBSController) {
	if mw.obsUnsub != nil {
		mw.obsUnsub()
		mw.obsUnsub = nil
	}
	mw.obsCtrl = ctrl
	if ctrl == nil {
		mw.obsBtn.Hide()
		return
	}

	mw.obsBtn.Show()
	mw.applyOBSStatus(ctrl.Status())
	mw.obsUnsub = ctrl.SubscribeStatus(func(st obs.ClientStatus) {
		dispatchUI(func() {
			mw.applyOBSStatus(st)
		})
	})
}

func (mw *MainWindow) applyOBSStatus(st obs.ClientStatus) {
	if mw.obsBtn == nil {
		return
	}
	mw.obsBtn.SetText(obsBadgeLabel(st))
	if st.State == obs.StateConnected {
		mw.obsBtn.Importance = widget.HighImportance
		mw.inspector.RefreshDynamicOptions()
	} else {
		mw.obsBtn.Importance = widget.MediumImportance
	}
	mw.obsBtn.Refresh()
}

// SetWebDeckController attaches a WebDeckController so the Studio header badge displays live LAN touch deck
// status and clicking it opens Settings directly on the Web Deck tab.
func (mw *MainWindow) SetWebDeckController(ctrl WebDeckController) {
	if mw.webUnsub != nil {
		mw.webUnsub()
		mw.webUnsub = nil
	}
	mw.webCtrl = ctrl
	if ctrl == nil {
		mw.webBtn.Hide()
		return
	}

	mw.webBtn.Show()
	mw.applyWebDeckStatus(ctrl.Status())
	mw.webUnsub = ctrl.SubscribeStatus(func(st webdeck.Status) {
		dispatchUI(func() {
			mw.applyWebDeckStatus(st)
		})
	})
}

func (mw *MainWindow) applyWebDeckStatus(st webdeck.Status) {
	if mw.webBtn == nil {
		return
	}
	mw.webBtn.SetText(webDeckBadgeLabel(st))
	if st.Running {
		mw.webBtn.Importance = widget.HighImportance
	} else {
		mw.webBtn.Importance = widget.MediumImportance
	}
	mw.webBtn.Refresh()
	mw.syncPendingPairDialog(st)
	mw.lastPairedCount = st.PairedDevices
}

func (mw *MainWindow) syncPendingPairDialog(st webdeck.Status) {
	p := st.PendingPair
	if p == nil {
		if mw.pairDialog != nil {
			mw.pairDialog.Hide()
			mw.pairDialog = nil
			mw.activePairAttemptID = ""
			mw.pairStatusLbl = nil
			mw.pairApproveBtn = nil
			if st.PairedDevices > mw.lastPairedCount {
				mw.setStatus("Paired remote Touch Deck device")
			}
		}
		return
	}

	if mw.activePairAttemptID != p.AttemptID || mw.pairDialog == nil {
		if mw.pairDialog != nil {
			mw.pairDialog.Hide()
			mw.pairDialog = nil
		}
		attemptID := p.AttemptID
		devName := p.DeviceName

		intro := widget.NewLabel("A remote device is requesting pairing. Verify this code matches your device screen before approving:")
		intro.Wrapping = fyne.TextWrapWord

		sasLbl := widget.NewLabelWithStyle("Verification Code: "+p.SAS, fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
		mw.pairStatusLbl = widget.NewLabel("")
		mw.pairStatusLbl.Wrapping = fyne.TextWrapWord

		mw.pairApproveBtn = widget.NewButtonWithIcon("Approve Device", theme.ConfirmIcon(), func() {
			if mw.webCtrl == nil {
				return
			}
			if err := mw.webCtrl.ResolvePendingPair(attemptID, true); err != nil {
				mw.setStatus("Approve pairing failed: " + err.Error())
				return
			}
			mw.setStatus("Approved pairing for " + devName)
		})
		mw.pairApproveBtn.Importance = widget.HighImportance

		denyBtn := widget.NewButtonWithIcon("Deny", theme.CancelIcon(), func() {
			if mw.webCtrl == nil {
				return
			}
			if err := mw.webCtrl.ResolvePendingPair(attemptID, false); err != nil {
				mw.setStatus("Deny pairing failed: " + err.Error())
				return
			}
			mw.setStatus("Denied pairing request from " + devName)
		})
		denyBtn.Importance = widget.DangerImportance

		content := container.NewVBox(
			intro,
			widget.NewSeparator(),
			sasLbl,
			widget.NewForm(
				widget.NewFormItem("Device", widget.NewLabel(p.DeviceName)),
				widget.NewFormItem("IP Address", widget.NewLabel(p.RemoteAddr)),
				widget.NewFormItem("Device Key", widget.NewLabel(p.ClientFingerprint)),
			),
			mw.pairStatusLbl,
			container.NewHBox(layout.NewSpacer(), denyBtn, mw.pairApproveBtn),
		)

		dlg := dialog.NewCustomWithoutButtons("Confirm Touch Deck Pairing", content, mw.win)
		dlg.Resize(fyne.NewSize(460, 320))
		mw.activePairAttemptID = attemptID
		mw.pairDialog = dlg
		dlg.Show()
	}

	if mw.pairStatusLbl == nil || mw.pairApproveBtn == nil {
		return
	}
	switch {
	case p.HostApproved && !p.ClientConfirmed:
		mw.pairApproveBtn.Disable()
		mw.pairStatusLbl.SetText("Approved on PC — waiting for Confirm Match on device...")
	case !p.HostApproved && p.ClientConfirmed:
		mw.pairApproveBtn.Enable()
		mw.pairStatusLbl.SetText("Device confirmed code — click Approve Device to finish.")
	default:
		mw.pairApproveBtn.Enable()
		mw.pairStatusLbl.SetText("Verify the code matches both screens, then approve on both devices.")
	}
}

func (mw *MainWindow) setStatus(msg string) {
	mw.statusNote = msg
	mw.refreshStatusBar(mw.lastSnap)
}

func (mw *MainWindow) addCanvasShortcut(s fyne.Shortcut, fn func(fyne.Shortcut)) {
	mw.win.Canvas().AddShortcut(s, fn)
	mw.shortcuts.AddShortcut(s, fn)
}

func (mw *MainWindow) handleUndo() {
	label, err := mw.engine.Undo()
	if err != nil {
		mw.setStatus("Undo failed: " + err.Error())
		return
	}
	if label == "" {
		mw.setStatus("Nothing to undo")
		return
	}
	if mw.clipMode == clipMove {
		mw.ClearClipboard()
	}
	mw.setStatus("Undid: " + label)
}

func (mw *MainWindow) handleRedo() {
	label, err := mw.engine.Redo()
	if err != nil {
		mw.setStatus("Redo failed: " + err.Error())
		return
	}
	if label == "" {
		mw.setStatus("Nothing to redo")
		return
	}
	if mw.clipMode == clipMove {
		mw.ClearClipboard()
	}
	mw.setStatus("Redid: " + label)
}

func (mw *MainWindow) assignAction(actionUUID string) {
	newDesc, hasDesc := mw.engine.Registry().Action(actionUUID)
	if hasDesc && !newDesc.SupportsController(mw.selectedController) {
		mw.performAssignAction(mw.selectedController, mw.selectedSlot, mw.selectedDial, actionUUID)
		return
	}

	targetCtrl := mw.selectedController
	targetCoords := mw.selectedSlot
	targetDial := mw.selectedDial
	newActionName := actionUUID
	if hasDesc && newDesc.Name != "" {
		newActionName = newDesc.Name
	}

	if mw.confirmOverwriteSlot(targetCtrl, targetCoords, targetDial, newActionName, func() {
		mw.performAssignAction(targetCtrl, targetCoords, targetDial, actionUUID)
	}) {
		return
	}
	mw.performAssignAction(targetCtrl, targetCoords, targetDial, actionUUID)
}

func (mw *MainWindow) confirmOverwriteSlot(
	targetCtrl deck.ControllerType,
	targetCoords deck.Coordinates,
	targetDial int,
	newActionName string,
	onConfirm func(),
) bool {
	snap := mw.engine.Snapshot()
	var (
		occupied  bool
		existing  app.RenderedSlot
		targetLbl string
	)
	if targetCtrl == deck.ControllerEncoder {
		existing, occupied = snap.DialSlots[targetDial]
		occupied = occupied && existing.Config != nil
		targetLbl = fmt.Sprintf("Encoder Dial %d", targetDial+1)
	} else {
		existing, occupied = snap.Slots[targetCoords.SlotKey()]
		occupied = occupied && existing.Config != nil && !existing.AutoBack
		targetLbl = fmt.Sprintf("Slot (%d, %d)", targetCoords.Row, targetCoords.Col)
	}
	if !occupied {
		return false
	}

	curActionName := existing.Config.ActionUUID
	if existing.Action != nil && existing.Action.Name != "" {
		curActionName = existing.Action.Name
	}
	curDesc := curActionName
	if curTitle := strings.TrimSpace(existing.Config.Title); curTitle != "" && !strings.EqualFold(curTitle, curActionName) {
		curDesc = fmt.Sprintf("%q (%s)", curTitle, curActionName)
	}

	msg := fmt.Sprintf(
		"%s currently has %s configured.\n\nReplace it with %s?\n\nYou can still undo with Ctrl+Z or Undo.",
		targetLbl, curDesc, newActionName,
	)

	dlg := dialog.NewConfirm("Replace Existing Action?", msg, func(confirmed bool) {
		mw.confirmDlg = nil
		if !confirmed {
			mw.setStatus("Kept existing action")
			return
		}
		if onConfirm != nil {
			onConfirm()
		}
	}, mw.win)
	dlg.SetConfirmText("Replace")
	dlg.SetDismissText("Cancel")
	dlg.SetConfirmImportance(widget.DangerImportance)
	mw.confirmDlg = dlg
	dlg.Show()
	return true
}

func (mw *MainWindow) performAssignAction(ctrl deck.ControllerType, coords deck.Coordinates, dial int, actionUUID string) {
	var err error
	if ctrl == deck.ControllerEncoder {
		_, err = mw.engine.AssignDialAction(dial, actionUUID)
	} else {
		_, err = mw.engine.AssignKeypadAction(coords, actionUUID)
	}
	if err != nil {
		mw.setStatus(err.Error())
		return
	}
	if desc, ok := mw.engine.Registry().Action(actionUUID); ok {
		mw.setStatus("Assigned " + desc.Name)
	} else {
		mw.setStatus("Assigned action")
	}
}

func (mw *MainWindow) handleKeyTap(c deck.Coordinates) {
	if mw.runMode {
		if err := mw.engine.PressKey(context.Background(), c); err != nil {
			mw.setStatus("Action error: " + err.Error())
		}
		return
	}
	if mw.clipMode == clipMove && mw.clipController == deck.ControllerKeypad {
		dstLoc := mw.engine.CurrentSlotLocation(deck.ControllerKeypad, c, 0)
		if mw.locationsEqual(mw.clipSrcLoc, dstLoc) {
			mw.ClearClipboard()
			mw.SelectSlot(c)
			mw.setStatus("Move canceled")
			return
		}
		if err := mw.performMove(mw.clipSrcLoc, dstLoc); err != nil {
			mw.setStatus(err.Error())
			return
		}
		mw.ClearClipboard()
		mw.SelectSlot(c)
		return
	}
	mw.SelectSlot(c)
}

func (mw *MainWindow) handleDialTap(dial int) {
	if mw.runMode {
		if err := mw.engine.PressDial(context.Background(), dial); err != nil {
			mw.setStatus("Dial error: " + err.Error())
		}
		return
	}
	if mw.clipMode == clipMove && mw.clipController == deck.ControllerEncoder {
		dstLoc := mw.engine.CurrentSlotLocation(deck.ControllerEncoder, deck.Coordinates{}, dial)
		if mw.locationsEqual(mw.clipSrcLoc, dstLoc) {
			mw.ClearClipboard()
			mw.SelectDial(dial)
			mw.setStatus("Move canceled")
			return
		}
		if err := mw.performMove(mw.clipSrcLoc, dstLoc); err != nil {
			mw.setStatus(err.Error())
			return
		}
		mw.ClearClipboard()
		mw.SelectDial(dial)
		return
	}
	mw.SelectDial(dial)
}

func (mw *MainWindow) handleDialDelta(dial, delta int) {
	if err := mw.engine.RotateDial(context.Background(), dial, delta); err != nil {
		mw.setStatus("Dial error: " + err.Error())
	}
}

func (mw *MainWindow) handleDialPress(dial int) {
	if err := mw.engine.PressDial(context.Background(), dial); err != nil {
		mw.setStatus("Dial error: " + err.Error())
	}
}

func (mw *MainWindow) applySnapshot(snap app.Snapshot) {
	mw.lastSnap = snap
	mw.syncing = true

	// Clamp selection when switching into a smaller grid or dial count.
	if snap.Grid.Rows > 0 && snap.Grid.Cols > 0 && !snap.Grid.Contains(mw.selectedSlot) {
		mw.selectedSlot = deck.Coordinates{
			Row: min(mw.selectedSlot.Row, snap.Grid.Rows-1),
			Col: min(mw.selectedSlot.Col, snap.Grid.Cols-1),
		}
	}
	if mw.selectedController == deck.ControllerEncoder && (snap.Dials <= 0 || mw.selectedDial >= snap.Dials) {
		mw.selectedController = deck.ControllerKeypad
		mw.selectedDial = 0
	}

	// Sync Profile selector.
	if mw.profileSelect != nil {
		names := make([]string, len(snap.Profiles))
		ids := make([]string, len(snap.Profiles))
		activeName := ""
		for i, p := range snap.Profiles {
			names[i] = p.Name
			ids[i] = p.ID
			if p.ID == snap.ActiveProfileID {
				activeName = p.Name
			}
		}
		mw.profileIDs = ids
		mw.profileSelect.SetOptions(names)
		if activeName != "" {
			mw.profileSelect.SetSelected(activeName)
		}
	}

	if mw.deckTitle != nil {
		mw.deckTitle.Text = fmt.Sprintf("Virtual Deck (%dx%d)", snap.Grid.Cols, snap.Grid.Rows)
		mw.deckTitle.Refresh()
	}

	if mw.undoBtn != nil {
		if snap.CanUndo {
			mw.undoBtn.Enable()
		} else {
			mw.undoBtn.Disable()
		}
	}
	if mw.redoBtn != nil {
		if snap.CanRedo {
			mw.redoBtn.Enable()
		} else {
			mw.redoBtn.Disable()
		}
	}

	mw.syncing = false

	mw.syncPageStrip(snap)
	mw.surface.ApplySnapshot(snap, true, mw.selectedController, mw.selectedSlot, mw.selectedDial)
	mw.syncSurfaceHighlights()
	mw.syncClipboardUI()
	mw.inspector.SetSelection(snap, mw.selectedController, mw.selectedSlot, mw.selectedDial)
	mw.refreshStatusBar(snap)
}

func (mw *MainWindow) refreshStatusBar(snap app.Snapshot) {
	if mw.statusBarLbl == nil {
		return
	}
	selDesc := fmt.Sprintf("(%d, %d)", mw.selectedSlot.Row, mw.selectedSlot.Col)
	if mw.selectedController == deck.ControllerEncoder {
		selDesc = fmt.Sprintf("Dial %d", mw.selectedDial+1)
	}
	note := mw.statusNote
	if note == "" {
		note = "Ready"
	}
	mw.statusBarLbl.SetText(fmt.Sprintf(
		"%s | Selected: %s | Grid: %dx%d | Plugins: %d | Web Deck: Off",
		note,
		selDesc,
		snap.Grid.Cols,
		snap.Grid.Rows,
		snap.PluginCount,
	))
}

func (mw *MainWindow) buildHeader() fyne.CanvasObject {
	title := canvas.NewText(version.AppName, ColorChromeWhite)
	title.TextSize = 18
	title.TextStyle = fyne.TextStyle{Bold: true}

	ver := canvas.NewText("v"+version.Version, ColorSkyBlue)
	ver.TextSize = 12
	ver.TextStyle = fyne.TextStyle{Bold: true}

	mw.profileSelect = widget.NewSelect([]string{"Default"}, func(_ string) {
		if mw.syncing {
			return
		}
		idx := mw.profileSelect.SelectedIndex()
		if idx >= 0 && idx < len(mw.profileIDs) {
			if err := mw.engine.SwitchProfile(mw.profileIDs[idx]); err != nil {
				mw.setStatus("Switch profile failed: " + err.Error())
			}
		}
	})

	newProfileBtn := widget.NewButtonWithIcon("", theme.ContentAddIcon(), mw.showCreateProfileDialog)
	gridBtn := widget.NewButtonWithIcon("Grid", theme.GridIcon(), mw.showGridDialog)

	mw.runModeCheck = widget.NewCheck("Run Mode", func(checked bool) {
		if mw.syncing {
			return
		}
		mw.SetRunMode(checked)
	})

	mw.undoBtn = widget.NewButtonWithIcon("", theme.ContentUndoIcon(), mw.handleUndo)
	mw.undoBtn.Disable()
	mw.redoBtn = widget.NewButtonWithIcon("", theme.ContentRedoIcon(), mw.handleRedo)
	mw.redoBtn.Disable()

	mw.obsBtn = widget.NewButton("OBS: Off", func() {
		mw.ShowSettingsTab(settingsTabOBS)
	})
	mw.obsBtn.Hide()

	mw.webBtn = widget.NewButton("Web Deck: Off", func() {
		mw.ShowSettingsTab(settingsTabWeb)
	})
	mw.webBtn.Hide()

	popOutBtn := widget.NewButton("Pop-Out Pad", func() {
		mw.OpenPopOutPad()
	})
	popOutBtn.Importance = widget.HighImportance

	row := container.NewHBox(
		title,
		ver,
		widget.NewSeparator(),
		widget.NewLabel("Profile:"),
		mw.profileSelect,
		newProfileBtn,
		gridBtn,
		layout.NewSpacer(),
		mw.undoBtn,
		mw.redoBtn,
		mw.obsBtn,
		mw.webBtn,
		mw.runModeCheck,
		popOutBtn,
	)

	bg := canvas.NewRectangle(ColorHeaderSurface)
	sep := canvas.NewRectangle(ColorElevatedSlate)
	sep.SetMinSize(fyne.NewSize(0, 1))

	return container.NewBorder(nil, sep, nil, nil,
		container.NewStack(bg, container.NewPadded(row)),
	)
}

func (mw *MainWindow) buildActionCatalog() fyne.CanvasObject {
	heading := widget.NewLabelWithStyle("Action Catalog", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	sub := widget.NewLabel("Click an action to assign to the selected slot")
	sub.Wrapping = fyne.TextWrapWord

	mw.catalogSearch = widget.NewEntry()
	mw.catalogSearch.SetPlaceHolder("Search plugins or actions...")
	mw.catalogClearBtn = widget.NewButtonWithIcon("", theme.CancelIcon(), func() {
		mw.catalogSearch.SetText("")
	})
	mw.catalogClearBtn.Importance = widget.LowImportance
	mw.catalogClearBtn.Hide()

	mw.catalogSearch.OnChanged = func(text string) {
		if strings.TrimSpace(text) == "" {
			mw.catalogClearBtn.Hide()
		} else {
			mw.catalogClearBtn.Show()
		}
		mw.RefreshActionCatalog()
	}

	searchRow := container.NewBorder(nil, nil, nil, mw.catalogClearBtn, mw.catalogSearch)

	mw.catalogList = container.NewVBox()
	mw.RefreshActionCatalog()

	panelBg := canvas.NewRectangle(ColorDarkCharcoal)
	return container.NewStack(
		panelBg,
		container.NewBorder(
			container.NewPadded(container.NewVBox(heading, sub, searchRow, widget.NewSeparator())),
			nil, nil, nil,
			container.NewVScroll(container.NewPadded(mw.catalogList)),
		),
	)
}

// RefreshActionCatalog rebuilds the left-hand Action Catalog from currently registered plugin providers,
// applying any active search filter and preserving category fold states.
func (mw *MainWindow) RefreshActionCatalog() {
	if mw.catalogList == nil {
		return
	}
	clear(mw.catalogButtons)
	clear(mw.catalogGroupHeaders)
	clear(mw.catalogConfigButtons)

	actions := mw.engine.Registry().Actions()
	manifests := mw.engine.Registry().Manifests()
	manifestByUUID := make(map[string]plugin.Manifest, len(manifests))
	for _, m := range manifests {
		manifestByUUID[m.UUID] = m
	}

	type configurablePlugin struct {
		uuid string
		name string
	}
	type catalogGroup struct {
		category     string
		pluginNames  []string
		pluginUUIDs  map[string]struct{}
		configurable []configurablePlugin
		iconURI      string
		actions      []plugin.ActionDescriptor
	}

	groupIndex := make(map[string]int)
	var groups []catalogGroup

	for _, act := range actions {
		cat := strings.TrimSpace(act.Category)
		if cat == "" {
			cat = "General"
		}
		idx, ok := groupIndex[cat]
		if !ok {
			idx = len(groups)
			groupIndex[cat] = idx
			groups = append(groups, catalogGroup{
				category:    cat,
				pluginUUIDs: make(map[string]struct{}),
			})
		}
		g := &groups[idx]
		g.actions = append(g.actions, act)
		if m, hasM := manifestByUUID[act.PluginUUID]; hasM {
			if _, seen := g.pluginUUIDs[m.UUID]; !seen {
				g.pluginUUIDs[m.UUID] = struct{}{}
				if m.UUID == obs.PluginUUID || len(m.GlobalSettingsSchema) > 0 {
					name := strings.TrimSpace(m.Name)
					if name == "" {
						name = cat
					}
					g.configurable = append(g.configurable, configurablePlugin{uuid: m.UUID, name: name})
				}
			}
			if m.Name != "" && !strings.EqualFold(m.Name, cat) {
				g.pluginNames = append(g.pluginNames, m.Name)
			}
			if g.iconURI == "" {
				if m.CategoryIcon != "" {
					g.iconURI = m.CategoryIcon
				} else if m.Icon != "" {
					g.iconURI = m.Icon
				}
			}
		}
	}

	query := ""
	if mw.catalogSearch != nil {
		query = strings.ToLower(strings.TrimSpace(mw.catalogSearch.Text))
	}

	var items []fyne.CanvasObject
	for _, g := range groups {
		var visibleActions []plugin.ActionDescriptor
		if query == "" {
			visibleActions = g.actions
		} else {
			catMatches := strings.Contains(strings.ToLower(g.category), query)
			if !catMatches {
				for _, pn := range g.pluginNames {
					if strings.Contains(strings.ToLower(pn), query) {
						catMatches = true
						break
					}
				}
			}
			if catMatches {
				visibleActions = g.actions
			} else {
				for _, act := range g.actions {
					if strings.Contains(strings.ToLower(act.Name), query) ||
						strings.Contains(strings.ToLower(act.Tooltip), query) {
						visibleActions = append(visibleActions, act)
					}
				}
			}
		}
		if len(visibleActions) == 0 {
			continue
		}

		if len(items) > 0 {
			items = append(items, widget.NewSeparator())
		}

		catName := g.category
		collapsed := query == "" && mw.catalogCollapsed[catName]
		chevron := "▾"
		if collapsed {
			chevron = "▸"
		}
		headerLabel := fmt.Sprintf("%s %s (%d)", chevron, catName, len(visibleActions))
		catIcon := mw.resolveCategoryIcon(catName, g.iconURI, visibleActions)
		headerBtn := widget.NewButtonWithIcon(headerLabel, catIcon, func() {
			if query != "" {
				return
			}
			mw.catalogCollapsed[catName] = !mw.catalogCollapsed[catName]
			mw.RefreshActionCatalog()
		})
		headerBtn.Alignment = widget.ButtonAlignLeading
		headerBtn.Importance = widget.LowImportance
		mw.catalogGroupHeaders[catName] = headerBtn
		if len(g.configurable) > 0 {
			cfgPlugins := g.configurable
			var gearBtn *widget.Button
			gearBtn = widget.NewButtonWithIcon("", theme.SettingsIcon(), func() {
				if len(cfgPlugins) == 1 {
					mw.ShowPluginConfigTab(cfgPlugins[0].uuid)
					return
				}
				if mw.win == nil || mw.win.Canvas() == nil {
					mw.ShowPluginConfigTab(cfgPlugins[0].uuid)
					return
				}
				menuItems := make([]*fyne.MenuItem, 0, len(cfgPlugins))
				for _, cp := range cfgPlugins {
					pid := cp.uuid
					menuItems = append(menuItems, fyne.NewMenuItem("Configure "+cp.name, func() {
						mw.ShowPluginConfigTab(pid)
					}))
				}
				pos := fyne.CurrentApp().Driver().AbsolutePositionForObject(gearBtn)
				pop := widget.NewPopUpMenu(fyne.NewMenu("", menuItems...), mw.win.Canvas())
				pop.ShowAtPosition(pos.Add(fyne.NewPos(0, gearBtn.Size().Height)))
			})
			gearBtn.Importance = widget.LowImportance
			mw.catalogConfigButtons[catName] = gearBtn
			items = append(items, container.NewBorder(nil, nil, nil, gearBtn, headerBtn))
		} else {
			items = append(items, headerBtn)
		}

		if collapsed {
			continue
		}

		for _, act := range visibleActions {
			uuid := act.UUID
			label := act.Name
			if act.SupportsController(deck.ControllerEncoder) {
				label += " (Key / Dial)"
			}
			actIcon := mw.resolveActionIcon(act)
			btn := widget.NewButtonWithIcon(label, actIcon, func() {
				mw.assignAction(uuid)
			})
			btn.Alignment = widget.ButtonAlignLeading
			mw.catalogButtons[uuid] = btn
			items = append(items, btn)
		}
	}

	if len(items) == 0 && query != "" {
		emptyLbl := widget.NewLabel(fmt.Sprintf("No actions match %q", strings.TrimSpace(mw.catalogSearch.Text)))
		emptyLbl.Wrapping = fyne.TextWrapWord
		items = append(items, emptyLbl)
	}

	mw.catalogList.Objects = items
	mw.catalogList.Refresh()
}

func (mw *MainWindow) loadCatalogIconResource(uri string) fyne.Resource {
	key := strings.TrimSpace(uri)
	if key == "" {
		return nil
	}
	if res, ok := mw.catalogIconCache[key]; ok {
		return res
	}
	img, err := tile.DecodeImage(key, 48)
	if err != nil || img == nil {
		mw.catalogIconCache[key] = nil
		return nil
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		mw.catalogIconCache[key] = nil
		return nil
	}
	res := fyne.NewStaticResource("catalog-"+filepath.Base(key)+".png", buf.Bytes())
	mw.catalogIconCache[key] = res
	return res
}

func (mw *MainWindow) resolveCategoryIcon(category, iconURI string, actions []plugin.ActionDescriptor) fyne.Resource {
	if res := mw.loadCatalogIconResource(iconURI); res != nil {
		return res
	}
	for _, a := range actions {
		if res := mw.loadCatalogIconResource(a.Icon); res != nil {
			return res
		}
		if res := mw.loadCatalogIconResource(a.DefaultImage); res != nil {
			return res
		}
	}
	lower := strings.ToLower(category)
	switch {
	case strings.Contains(lower, "obs"):
		return theme.MediaVideoIcon()
	case strings.Contains(lower, "app") || strings.Contains(lower, "system"):
		return theme.ComputerIcon()
	case strings.Contains(lower, "media") || strings.Contains(lower, "audio"):
		return theme.VolumeUpIcon()
	case strings.Contains(lower, "nav") || strings.Contains(lower, "core"):
		return theme.GridIcon()
	case strings.Contains(lower, "web") || strings.Contains(lower, "http"):
		return theme.MailSendIcon()
	default:
		return theme.FolderIcon()
	}
}

func (mw *MainWindow) resolveActionIcon(act plugin.ActionDescriptor) fyne.Resource {
	if res := mw.loadCatalogIconResource(act.Icon); res != nil {
		return res
	}
	if res := mw.loadCatalogIconResource(act.DefaultImage); res != nil {
		return res
	}
	lowerUUID := strings.ToLower(act.UUID)
	lowerName := strings.ToLower(act.Name)
	switch {
	case strings.Contains(lowerUUID, "folder") || strings.Contains(lowerName, "folder"):
		return theme.FolderIcon()
	case strings.Contains(lowerUUID, "next") || strings.Contains(lowerName, "next"):
		return theme.NavigateNextIcon()
	case strings.Contains(lowerUUID, "prev") || strings.Contains(lowerName, "previous"):
		return theme.NavigateBackIcon()
	case strings.Contains(lowerUUID, "profile") || strings.Contains(lowerName, "profile"):
		return theme.AccountIcon()
	case strings.Contains(lowerUUID, "multi") || strings.Contains(lowerName, "multi"):
		return theme.ListIcon()
	case strings.Contains(lowerUUID, "record") || strings.Contains(lowerUUID, "stream"):
		return theme.MediaRecordIcon()
	case strings.Contains(lowerUUID, "mute") || strings.Contains(lowerUUID, "volume") || strings.Contains(lowerUUID, "audio"):
		return theme.VolumeUpIcon()
	case strings.Contains(lowerUUID, "scene") || strings.Contains(lowerUUID, "source"):
		return theme.VisibilityIcon()
	case strings.Contains(lowerUUID, "url") || strings.Contains(lowerUUID, "webhook") || strings.Contains(lowerUUID, "http"):
		return theme.MailSendIcon()
	case strings.Contains(lowerUUID, "media") || strings.Contains(lowerUUID, "play"):
		return theme.MediaPlayIcon()
	case strings.Contains(lowerUUID, "hotkey") || strings.Contains(lowerUUID, "macro"):
		return theme.ComputerIcon()
	default:
		return theme.ConfirmIcon()
	}
}

func (mw *MainWindow) buildDeckCenter() fyne.CanvasObject {
	mw.deckTitle = canvas.NewText("Virtual Deck (5×3)", ColorChromeWhite)
	mw.deckTitle.TextSize = 14
	mw.deckTitle.TextStyle = fyne.TextStyle{Bold: true}

	mw.deckHint = canvas.NewText("Edit Mode: click any slot to inspect, drag to move/swap, right-click for menu", ColorSecondarySlate)
	mw.deckHint.TextSize = 11

	mw.clipBannerBg = canvas.NewRectangle(ColorElevatedSlate)
	mw.clipBannerBg.CornerRadius = 8
	mw.clipBannerBg.StrokeColor = ColorSkyBlue
	mw.clipBannerBg.StrokeWidth = 1.5
	mw.clipBannerLbl = widget.NewLabel("")
	mw.clipBannerLbl.Wrapping = fyne.TextWrapWord
	mw.clipPasteBtn = widget.NewButton("Paste Here", func() {
		mw.PasteSelected()
	})
	mw.clipPasteBtn.Importance = widget.HighImportance
	mw.clipPasteBtn.Hide()
	mw.clipCancelBtn = widget.NewButtonWithIcon("Cancel", theme.CancelIcon(), func() {
		mw.ClearClipboard()
	})
	clipRow := container.NewBorder(nil, nil, nil, container.NewHBox(mw.clipPasteBtn, mw.clipCancelBtn), mw.clipBannerLbl)
	mw.clipBanner = container.NewStack(mw.clipBannerBg, container.NewPadded(clipRow))
	mw.clipBanner.Hide()

	mw.pageStrip = container.NewHBox()

	padFrameBg := canvas.NewRectangle(color.RGBA{R: 15, G: 17, B: 21, A: 255})
	padFrameBg.CornerRadius = 14
	padFrameBg.StrokeColor = ColorElevatedSlate
	padFrameBg.StrokeWidth = 2

	padBody := container.NewVBox(
		container.NewHBox(mw.deckTitle, layout.NewSpacer(), mw.deckHint),
		mw.clipBanner,
		mw.pageStrip,
		widget.NewSeparator(),
		mw.surface.Object(),
	)

	centered := container.NewCenter(
		container.NewStack(padFrameBg, container.NewPadded(container.NewPadded(padBody))),
	)

	canvasBg := canvas.NewRectangle(ColorObsidianSlate)
	return container.NewStack(canvasBg, container.NewScroll(centered))
}

func (mw *MainWindow) forwardShortcutToFocused(s fyne.Shortcut) bool {
	if mw.win == nil || mw.win.Canvas() == nil {
		return false
	}
	if foc := mw.win.Canvas().Focused(); foc != nil {
		if target, ok := foc.(fyne.Shortcutable); ok {
			target.TypedShortcut(s)
			return true
		}
	}
	return false
}

// StartMoveSelected enters Move pick-target mode for the currently selected button or dial.
func (mw *MainWindow) StartMoveSelected() {
	if mw.runMode {
		return
	}
	mw.startMoveAt(mw.selectedController, mw.selectedSlot, mw.selectedDial)
}

// CopySelected copies the currently selected button or dial onto Studio's internal clipboard for cross-page/folder/profile pasting.
func (mw *MainWindow) CopySelected() {
	if mw.runMode {
		return
	}
	mw.copyAt(mw.selectedController, mw.selectedSlot, mw.selectedDial)
}

// PasteSelected applies the active Move or Copy clipboard onto the currently selected destination slot.
func (mw *MainWindow) PasteSelected() {
	if mw.runMode || mw.clipMode == clipNone {
		return
	}
	dstLoc := mw.engine.CurrentSlotLocation(mw.selectedController, mw.selectedSlot, mw.selectedDial)
	switch mw.clipMode {
	case clipMove:
		if mw.clipController != mw.selectedController {
			mw.setStatus(fmt.Sprintf("Cannot move %s into %s slot", mw.clipController, mw.selectedController))
			return
		}
		if mw.locationsEqual(mw.clipSrcLoc, dstLoc) {
			mw.ClearClipboard()
			return
		}
		if err := mw.performMove(mw.clipSrcLoc, dstLoc); err != nil {
			mw.setStatus(err.Error())
			return
		}
		mw.ClearClipboard()
	case clipCopy:
		if mw.clipSlot == nil {
			return
		}
		slotCopy := mw.clipSlot.Clone()
		srcProfID := mw.clipSrcProfileID
		label := mw.clipLabel
		if desc, ok := mw.engine.Registry().Action(slotCopy.ActionUUID); ok && !desc.SupportsController(dstLoc.Controller) {
			mw.performPaste(slotCopy, srcProfID, label, dstLoc)
			return
		}
		pasteDesc := "copied action"
		if strings.TrimSpace(label) != "" {
			pasteDesc = fmt.Sprintf("copied %q", label)
		}
		if mw.confirmOverwriteSlot(mw.selectedController, mw.selectedSlot, mw.selectedDial, pasteDesc, func() {
			mw.performPaste(slotCopy, srcProfID, label, dstLoc)
		}) {
			return
		}
		mw.performPaste(slotCopy, srcProfID, label, dstLoc)
	}
}

func (mw *MainWindow) performPaste(slot *deck.SlotConfig, srcProfileID, label string, dstLoc app.SlotLocation) {
	if _, err := mw.engine.PasteSlotConfig(slot, srcProfileID, dstLoc); err != nil {
		mw.setStatus("Paste failed: " + err.Error())
		return
	}
	mw.setStatus("Pasted " + label)
}

// ClearClipboard cancels Move mode or clears any copied button/dial from Studio's clipboard.
func (mw *MainWindow) ClearClipboard() {
	mw.clipMode = clipNone
	mw.clipSrcLoc = app.SlotLocation{}
	mw.clipSlot = nil
	mw.clipSrcProfileID = ""
	mw.clipLabel = ""
	mw.clipController = ""
	mw.syncClipboardUI()
	mw.syncSurfaceHighlights()
}

func (mw *MainWindow) slotLookup(snap app.Snapshot, ctrl deck.ControllerType, coords deck.Coordinates, dial int) (app.RenderedSlot, bool) {
	if ctrl == deck.ControllerEncoder {
		r, ok := snap.DialSlots[dial]
		return r, ok && r.Config != nil
	}
	r, ok := snap.Slots[coords.SlotKey()]
	if !ok || r.AutoBack || r.Config == nil {
		return app.RenderedSlot{}, false
	}
	return r, true
}

func slotDisplayLabel(r app.RenderedSlot) string {
	if title := strings.TrimSpace(r.Visual.Title); title != "" {
		return title
	}
	if r.Config != nil && strings.TrimSpace(r.Config.Title) != "" {
		return strings.TrimSpace(r.Config.Title)
	}
	if r.Action != nil && r.Action.Name != "" {
		return r.Action.Name
	}
	if r.Config != nil {
		return r.Config.ActionUUID
	}
	return "Action"
}

func (mw *MainWindow) startMoveAt(ctrl deck.ControllerType, coords deck.Coordinates, dial int) {
	snap := mw.engine.Snapshot()
	rendered, ok := mw.slotLookup(snap, ctrl, coords, dial)
	if !ok || rendered.Config == nil {
		mw.setStatus("Select an assigned button or dial to move")
		return
	}
	mw.clipMode = clipMove
	mw.clipController = ctrl
	mw.clipSrcLoc = mw.engine.CurrentSlotLocation(ctrl, coords, dial)
	mw.clipSlot = rendered.Config.Clone()
	mw.clipSrcProfileID = snap.ActiveProfileID
	mw.clipLabel = slotDisplayLabel(rendered)
	mw.syncClipboardUI()
	mw.syncSurfaceHighlights()
	mw.setStatus("Moving " + mw.clipLabel + " — click destination slot or switch page/folder")
}

func (mw *MainWindow) copyAt(ctrl deck.ControllerType, coords deck.Coordinates, dial int) {
	snap := mw.engine.Snapshot()
	rendered, ok := mw.slotLookup(snap, ctrl, coords, dial)
	if !ok || rendered.Config == nil {
		mw.setStatus("Select an assigned button or dial to copy")
		return
	}
	mw.clipMode = clipCopy
	mw.clipController = ctrl
	mw.clipSrcLoc = mw.engine.CurrentSlotLocation(ctrl, coords, dial)
	mw.clipSlot = rendered.Config.Clone()
	mw.clipSrcProfileID = snap.ActiveProfileID
	mw.clipLabel = slotDisplayLabel(rendered)
	mw.syncClipboardUI()
	mw.syncSurfaceHighlights()
	mw.setStatus("Copied " + mw.clipLabel + " (press Ctrl+V on any page, folder, or profile)")
}

func (mw *MainWindow) performMove(src, dst app.SlotLocation) error {
	swapped, err := mw.engine.MoveSlot(src, dst)
	if err != nil {
		return err
	}
	if swapped {
		mw.setStatus("Swapped slots")
	} else {
		mw.setStatus("Moved slot")
	}
	return nil
}

func (mw *MainWindow) locationsEqual(a, b app.SlotLocation) bool {
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

func (mw *MainWindow) isLocationVisible(snap app.Snapshot, loc app.SlotLocation) bool {
	if loc.ProfileID != snap.ActiveProfileID {
		return false
	}
	if snap.InFolder {
		return loc.FolderID != "" && loc.FolderID == snap.ActiveFolderID
	}
	return loc.FolderID == "" && loc.PageIndex == snap.PageIndex
}

func (mw *MainWindow) syncClipboardUI() {
	if mw.clipBanner == nil {
		return
	}
	switch mw.clipMode {
	case clipMove:
		mw.clipBannerBg.StrokeColor = ColorWarning
		locDesc := fmt.Sprintf("Page %d (%d,%d)", mw.clipSrcLoc.PageIndex+1, mw.clipSrcLoc.Coords.Row, mw.clipSrcLoc.Coords.Col)
		if mw.clipSrcLoc.FolderID != "" {
			locDesc = fmt.Sprintf("Folder (%d,%d)", mw.clipSrcLoc.Coords.Row, mw.clipSrcLoc.Coords.Col)
		}
		if mw.clipController == deck.ControllerEncoder {
			locDesc = fmt.Sprintf("Dial %d", mw.clipSrcLoc.Dial+1)
		}
		mw.clipBannerLbl.SetText(fmt.Sprintf("Moving %q from %s — click destination slot on any page, folder, or profile (Esc to cancel)", mw.clipLabel, locDesc))
		mw.clipCancelBtn.SetText("Cancel")
		mw.clipPasteBtn.Hide()
		mw.clipBanner.Show()
	case clipCopy:
		mw.clipBannerBg.StrokeColor = ColorSkyBlue
		mw.clipBannerLbl.SetText(fmt.Sprintf("Copied %q — select any slot (any page, folder, or profile) and press Ctrl+V", mw.clipLabel))
		mw.clipCancelBtn.SetText("Clear")
		mw.clipPasteBtn.Show()
		mw.clipBanner.Show()
	default:
		mw.clipBanner.Hide()
	}
	mw.clipBanner.Refresh()
}

func (mw *MainWindow) syncSurfaceHighlights() {
	if mw.surface == nil {
		return
	}
	var (
		hasMoveKey  bool
		moveKey     deck.Coordinates
		hasMoveDial bool
		moveDial    int
	)
	if mw.clipMode == clipMove && mw.isLocationVisible(mw.lastSnap, mw.clipSrcLoc) {
		if mw.clipSrcLoc.Controller == deck.ControllerEncoder {
			hasMoveDial = true
			moveDial = mw.clipSrcLoc.Dial
		} else {
			hasMoveKey = true
			moveKey = mw.clipSrcLoc.Coords
		}
	}
	if mw.dragActive && mw.isLocationVisible(mw.lastSnap, mw.dragSrcLoc) {
		if mw.dragSrcLoc.Controller == deck.ControllerEncoder {
			hasMoveDial = true
			moveDial = mw.dragSrcLoc.Dial
		} else {
			hasMoveKey = true
			moveKey = mw.dragSrcLoc.Coords
		}
	}
	mw.surface.SetMoveHighlights(
		hasMoveKey, moveKey,
		hasMoveDial, moveDial,
		mw.dragActive && mw.hasHoverDropKey, mw.hoverDropKey,
		mw.dragActive && mw.hasHoverDropDial, mw.hoverDropDial,
	)
}

func (mw *MainWindow) syncPageStrip(snap app.Snapshot) {
	if mw.pageStrip == nil {
		return
	}
	key := fmt.Sprintf("f=%v|%s|%s", snap.InFolder, strings.Join(snap.FolderPath, "/"), strings.Join(snap.PageNames, "|"))
	if key == mw.pageStripKey {
		mw.refreshPageButtonImportance(snap)
		return
	}
	mw.pageStripKey = key

	if snap.InFolder {
		mw.pageButtons = nil
		mw.pageAddBtn = nil
		mw.pageDeleteBtn = nil
		folderBtn := widget.NewButton("← Exit Folder ("+strings.Join(snap.FolderPath, " / ")+")", func() {
			_ = mw.engine.CloseFolder()
		})
		hint := widget.NewLabel("Switch to root pages or cancel Move to pick another location")
		mw.pageStrip.Objects = []fyne.CanvasObject{folderBtn, hint}
		mw.pageStrip.Refresh()
		return
	}

	pageNames := snap.PageNames
	if len(pageNames) == 0 {
		pageNames = []string{"Page 1"}
	}
	btns := make([]*widget.Button, len(pageNames))
	objs := make([]fyne.CanvasObject, 0, len(pageNames)+2)
	for i, name := range pageNames {
		pageNum := i + 1
		b := widget.NewButton(name, func() {
			if err := mw.engine.GoToPage(pageNum); err != nil {
				mw.setStatus("Page switch failed: " + err.Error())
			}
		})
		btns[i] = b
		objs = append(objs, b)
	}
	addBtn := widget.NewButtonWithIcon("", theme.ContentAddIcon(), func() {
		if _, err := mw.engine.AddPage(""); err != nil {
			mw.setStatus("Add page failed: " + err.Error())
		}
	})
	delBtn := widget.NewButtonWithIcon("", theme.DeleteIcon(), mw.confirmDeleteCurrentPage)
	if len(pageNames) <= 1 {
		delBtn.Disable()
	}
	objs = append(objs, addBtn, delBtn)

	mw.pageButtons = btns
	mw.pageAddBtn = addBtn
	mw.pageDeleteBtn = delBtn
	mw.refreshPageButtonImportance(snap)
	mw.pageStrip.Objects = objs
	mw.pageStrip.Refresh()
}

func (mw *MainWindow) confirmDeleteCurrentPage() {
	snap := mw.engine.Snapshot()
	if snap.InFolder || snap.PageCount <= 1 {
		return
	}
	pageIdx := snap.PageIndex
	pageName := fmt.Sprintf("Page %d", pageIdx+1)
	if pageIdx >= 0 && pageIdx < len(snap.PageNames) && strings.TrimSpace(snap.PageNames[pageIdx]) != "" {
		pageName = snap.PageNames[pageIdx]
	}

	assignedCount := 0
	for _, slot := range snap.Slots {
		if slot.Config != nil && strings.TrimSpace(slot.Config.ActionUUID) != "" {
			assignedCount++
		}
	}
	for _, d := range snap.DialSlots {
		if d.Config != nil && strings.TrimSpace(d.Config.ActionUUID) != "" {
			assignedCount++
		}
	}

	msg := fmt.Sprintf("Delete %q? This will permanently remove the page.", pageName)
	if assignedCount == 1 {
		msg = fmt.Sprintf("Delete %q (1 assigned action)? This will permanently remove the page.", pageName)
	} else if assignedCount > 1 {
		msg = fmt.Sprintf("Delete %q (%d assigned actions)? This will permanently remove the page.", pageName, assignedCount)
	}

	dlg := dialog.NewConfirm("Delete Page", msg, func(confirmed bool) {
		mw.confirmDlg = nil
		if !confirmed {
			return
		}
		if err := mw.engine.DeletePage(pageIdx); err != nil {
			mw.setStatus("Delete page failed: " + err.Error())
			return
		}
		mw.setStatus("Deleted " + pageName)
	}, mw.win)
	dlg.SetConfirmText("Delete")
	dlg.SetConfirmImportance(widget.DangerImportance)
	dlg.SetDismissText("Cancel")
	mw.confirmDlg = dlg
	dlg.Show()
}

func (mw *MainWindow) refreshPageButtonImportance(snap app.Snapshot) {
	for i, b := range mw.pageButtons {
		if b == nil {
			continue
		}
		var want widget.Importance
		switch {
		case mw.dragActive && mw.hoverPageIdx == i:
			want = widget.WarningImportance
		case !snap.InFolder && i == snap.PageIndex:
			want = widget.HighImportance
		default:
			want = widget.MediumImportance
		}
		if b.Importance != want {
			b.Importance = want
			b.Refresh()
		}
	}
	if mw.pageAddBtn != nil {
		want := widget.MediumImportance
		if mw.dragActive && mw.hoverAddPage {
			want = widget.WarningImportance
		}
		if mw.pageAddBtn.Importance != want {
			mw.pageAddBtn.Importance = want
			mw.pageAddBtn.Refresh()
		}
	}
}

func (mw *MainWindow) pagePillAtPosition(absPos fyne.Position) (pageIdx int, isAddPage bool) {
	curApp := fyne.CurrentApp()
	if curApp == nil || curApp.Driver() == nil {
		return -1, false
	}
	drv := curApp.Driver()
	for i, b := range mw.pageButtons {
		if b == nil {
			continue
		}
		sz := b.Size()
		if sz.Width <= 0 || sz.Height <= 0 {
			continue
		}
		pos := drv.AbsolutePositionForObject(b)
		if absPos.X >= pos.X && absPos.X <= pos.X+sz.Width &&
			absPos.Y >= pos.Y && absPos.Y <= pos.Y+sz.Height {
			return i, false
		}
	}
	if mw.pageAddBtn != nil {
		sz := mw.pageAddBtn.Size()
		if sz.Width > 0 && sz.Height > 0 {
			pos := drv.AbsolutePositionForObject(mw.pageAddBtn)
			if absPos.X >= pos.X && absPos.X <= pos.X+sz.Width &&
				absPos.Y >= pos.Y && absPos.Y <= pos.Y+sz.Height {
				return -1, true
			}
		}
	}
	return -1, false
}

func (mw *MainWindow) cancelHoverPageTimer() {
	if mw.hoverPageTimer != nil {
		mw.hoverPageTimer.Stop()
		mw.hoverPageTimer = nil
	}
	mw.hoverPageIdx = -1
	mw.hoverAddPage = false
}

func (mw *MainWindow) clearDragState() {
	mw.dragActive = false
	mw.dragSrcLoc = app.SlotLocation{}
	mw.hasHoverDropKey = false
	mw.hasHoverDropDial = false
	mw.cancelHoverPageTimer()
	mw.refreshPageButtonImportance(mw.lastSnap)
	mw.syncSurfaceHighlights()
}

func (mw *MainWindow) handleTileDragMove(srcCoords deck.Coordinates, absPos fyne.Position) {
	if mw.runMode {
		return
	}
	if !mw.dragActive {
		if _, ok := mw.slotLookup(mw.lastSnap, deck.ControllerKeypad, srcCoords, 0); !ok {
			return
		}
		mw.dragActive = true
		mw.dragSrcLoc = mw.engine.CurrentSlotLocation(deck.ControllerKeypad, srcCoords, 0)
	}

	if targetCoords, hit := mw.surface.TileAtPosition(absPos); hit {
		if mw.lastSnap.InFolder && targetCoords == (deck.Coordinates{Row: 0, Col: 0}) {
			mw.hasHoverDropKey = false
		} else {
			mw.hasHoverDropKey = true
			mw.hoverDropKey = targetCoords
		}
		mw.hasHoverDropDial = false
		mw.cancelHoverPageTimer()
		mw.refreshPageButtonImportance(mw.lastSnap)
		mw.syncSurfaceHighlights()
		return
	}

	mw.hasHoverDropKey = false
	mw.hasHoverDropDial = false
	pageIdx, isAddPage := mw.pagePillAtPosition(absPos)
	switch {
	case isAddPage:
		mw.cancelHoverPageTimer()
		mw.hoverAddPage = true
	case pageIdx >= 0:
		if mw.hoverPageIdx != pageIdx {
			mw.cancelHoverPageTimer()
			mw.hoverPageIdx = pageIdx
			if mw.lastSnap.InFolder || pageIdx != mw.lastSnap.PageIndex {
				targetIdx := pageIdx
				mw.hoverPageTimer = time.AfterFunc(pageHoverFlipDelay, func() {
					dispatchUI(func() {
						if !mw.dragActive || mw.hoverPageIdx != targetIdx {
							return
						}
						_ = mw.engine.GoToPage(targetIdx + 1)
					})
				})
			}
		}
	default:
		mw.cancelHoverPageTimer()
	}

	mw.refreshPageButtonImportance(mw.lastSnap)
	mw.syncSurfaceHighlights()
}

func (mw *MainWindow) handleTileDragEnd(_ deck.Coordinates, absPos fyne.Position) {
	if !mw.dragActive {
		return
	}
	srcLoc := mw.dragSrcLoc
	mw.clearDragState()
	if mw.runMode {
		return
	}

	if dstCoords, hit := mw.surface.TileAtPosition(absPos); hit {
		dstLoc := mw.engine.CurrentSlotLocation(deck.ControllerKeypad, dstCoords, 0)
		if mw.locationsEqual(srcLoc, dstLoc) {
			mw.SelectSlot(dstCoords)
			return
		}
		if err := mw.performMove(srcLoc, dstLoc); err != nil {
			mw.setStatus(err.Error())
			return
		}
		mw.SelectSlot(dstCoords)
		return
	}

	pageIdx, isAddPage := mw.pagePillAtPosition(absPos)
	switch {
	case isAddPage:
		newIdx, dstCoord, err := mw.engine.MoveSlotToNewPage(srcLoc)
		if err != nil {
			mw.setStatus("Move to new page failed: " + err.Error())
			return
		}
		mw.setStatus(fmt.Sprintf("Moved to Page %d (%d, %d)", newIdx+1, dstCoord.Row, dstCoord.Col))
	case pageIdx >= 0:
		dstCoord, err := mw.engine.MoveSlotToPage(srcLoc, pageIdx)
		if err != nil {
			mw.setStatus("Move failed: " + err.Error())
			return
		}
		mw.setStatus(fmt.Sprintf("Moved to Page %d (%d, %d)", pageIdx+1, dstCoord.Row, dstCoord.Col))
	}
}

func (mw *MainWindow) handleDialDragMove(srcDial int, absPos fyne.Position) {
	if mw.runMode {
		return
	}
	if !mw.dragActive {
		if _, ok := mw.slotLookup(mw.lastSnap, deck.ControllerEncoder, deck.Coordinates{}, srcDial); !ok {
			return
		}
		mw.dragActive = true
		mw.dragSrcLoc = mw.engine.CurrentSlotLocation(deck.ControllerEncoder, deck.Coordinates{}, srcDial)
	}
	mw.hasHoverDropKey = false
	if dstDial, hit := mw.surface.DialAtPosition(absPos); hit {
		mw.hasHoverDropDial = true
		mw.hoverDropDial = dstDial
	} else {
		mw.hasHoverDropDial = false
	}
	mw.syncSurfaceHighlights()
}

func (mw *MainWindow) handleDialDragEnd(_ int, absPos fyne.Position) {
	if !mw.dragActive {
		return
	}
	srcLoc := mw.dragSrcLoc
	mw.clearDragState()
	if mw.runMode {
		return
	}
	if dstDial, hit := mw.surface.DialAtPosition(absPos); hit {
		dstLoc := mw.engine.CurrentSlotLocation(deck.ControllerEncoder, deck.Coordinates{}, dstDial)
		if mw.locationsEqual(srcLoc, dstLoc) {
			mw.SelectDial(dstDial)
			return
		}
		if err := mw.performMove(srcLoc, dstLoc); err != nil {
			mw.setStatus(err.Error())
			return
		}
		mw.SelectDial(dstDial)
	}
}

func (mw *MainWindow) handleKeySecondaryTap(c deck.Coordinates, absPos fyne.Position) {
	if mw.runMode || mw.win == nil || mw.win.Canvas() == nil {
		return
	}
	mw.SelectSlot(c)
	snap := mw.engine.Snapshot()
	rendered, hasRendered := snap.Slots[c.SlotKey()]
	if hasRendered && rendered.AutoBack {
		item := fyne.NewMenuItem("Auto Back Button (Reserved)", nil)
		item.Disabled = true
		widget.ShowPopUpMenuAtPosition(fyne.NewMenu("", item), mw.win.Canvas(), absPos)
		return
	}

	assigned := hasRendered && rendered.Config != nil
	var items []*fyne.MenuItem
	if assigned {
		srcLoc := mw.engine.CurrentSlotLocation(deck.ControllerKeypad, c, 0)
		items = append(items,
			fyne.NewMenuItem("Move (Pick Slot)", func() {
				mw.startMoveAt(deck.ControllerKeypad, c, 0)
			}),
			fyne.NewMenuItem("Copy", func() {
				mw.copyAt(deck.ControllerKeypad, c, 0)
			}),
		)

		pageNames := snap.PageNames
		if len(pageNames) == 0 {
			pageNames = []string{"Page 1"}
		}
		var pageSub []*fyne.MenuItem
		for i, name := range pageNames {
			if !snap.InFolder && i == snap.PageIndex {
				continue
			}
			targetIdx := i
			pageLabel := name
			pageSub = append(pageSub, fyne.NewMenuItem(pageLabel+" (first open)", func() {
				dstCoord, err := mw.engine.MoveSlotToPage(srcLoc, targetIdx)
				if err != nil {
					mw.setStatus("Move failed: " + err.Error())
					return
				}
				mw.setStatus(fmt.Sprintf("Moved to %s (%d, %d)", pageLabel, dstCoord.Row, dstCoord.Col))
			}))
		}
		pageSub = append(pageSub, fyne.NewMenuItem("+ Move to New Page", func() {
			newIdx, dstCoord, err := mw.engine.MoveSlotToNewPage(srcLoc)
			if err != nil {
				mw.setStatus("Move failed: " + err.Error())
				return
			}
			mw.setStatus(fmt.Sprintf("Moved to Page %d (%d, %d)", newIdx+1, dstCoord.Row, dstCoord.Col))
		}))
		movePageItem := fyne.NewMenuItem("Move to Page", nil)
		movePageItem.ChildMenu = fyne.NewMenu("", pageSub...)
		items = append(items, movePageItem)

		var selfFolderID string
		if rendered.Config.ActionUUID == "com.github.mrpoundsign.poundcontrol.core.folder.open" {
			if fid, ok := rendered.Config.Settings["folderId"].(string); ok && strings.TrimSpace(fid) != "" {
				selfFolderID = strings.TrimSpace(fid)
			} else {
				selfFolderID = rendered.Config.Context
			}
		}
		var folderSub []*fyne.MenuItem
		for _, f := range snap.Folders {
			if f.ID == snap.ActiveFolderID || f.ID == selfFolderID {
				continue
			}
			targetFID := f.ID
			targetName := f.Name
			folderSub = append(folderSub, fyne.NewMenuItem(targetName, func() {
				dstCoord, err := mw.engine.MoveSlotToFolder(srcLoc, targetFID)
				if err != nil {
					mw.setStatus("Move into folder failed: " + err.Error())
					return
				}
				mw.setStatus(fmt.Sprintf("Moved to folder %s (%d, %d)", targetName, dstCoord.Row, dstCoord.Col))
			}))
		}
		if len(folderSub) > 0 {
			moveFolderItem := fyne.NewMenuItem("Move into Folder", nil)
			moveFolderItem.ChildMenu = fyne.NewMenu("", folderSub...)
			items = append(items, moveFolderItem)
		}
	}

	if mw.clipMode != clipNone {
		label := "Paste Copy Here"
		if mw.clipMode == clipMove {
			if assigned {
				label = "Swap With " + mw.clipLabel
			} else {
				label = "Move " + mw.clipLabel + " Here"
			}
		}
		items = append(items, fyne.NewMenuItem(label, func() {
			mw.PasteSelected()
		}))
	}

	if assigned {
		items = append(items, fyne.NewMenuItemSeparator(), fyne.NewMenuItem("Clear Slot", func() {
			if err := mw.engine.ClearKeypadSlot(c); err != nil {
				mw.setStatus("Clear slot failed: " + err.Error())
			}
		}))
	}

	if len(items) == 0 {
		return
	}
	widget.ShowPopUpMenuAtPosition(fyne.NewMenu("", items...), mw.win.Canvas(), absPos)
}

func (mw *MainWindow) handleDialSecondaryTap(dial int, absPos fyne.Position) {
	if mw.runMode || mw.win == nil || mw.win.Canvas() == nil {
		return
	}
	mw.SelectDial(dial)
	snap := mw.engine.Snapshot()
	_, assigned := mw.slotLookup(snap, deck.ControllerEncoder, deck.Coordinates{}, dial)

	var items []*fyne.MenuItem
	if assigned {
		items = append(items,
			fyne.NewMenuItem("Move Dial", func() {
				mw.startMoveAt(deck.ControllerEncoder, deck.Coordinates{}, dial)
			}),
			fyne.NewMenuItem("Copy Dial", func() {
				mw.copyAt(deck.ControllerEncoder, deck.Coordinates{}, dial)
			}),
		)
	}
	if mw.clipMode != clipNone {
		label := "Paste Copy on Dial"
		if mw.clipMode == clipMove {
			if assigned {
				label = "Swap With " + mw.clipLabel
			} else {
				label = "Move " + mw.clipLabel + " Here"
			}
		}
		items = append(items, fyne.NewMenuItem(label, func() {
			mw.PasteSelected()
		}))
	}
	if assigned {
		items = append(items, fyne.NewMenuItemSeparator(), fyne.NewMenuItem("Clear Dial", func() {
			if err := mw.engine.ClearDialSlot(dial); err != nil {
				mw.setStatus("Clear dial failed: " + err.Error())
			}
		}))
	}
	if len(items) == 0 {
		return
	}
	widget.ShowPopUpMenuAtPosition(fyne.NewMenu("", items...), mw.win.Canvas(), absPos)
}

func (mw *MainWindow) buildStatusBar() fyne.CanvasObject {
	mw.statusBarLbl = widget.NewLabel("Ready")
	bg := canvas.NewRectangle(ColorHeaderSurface)
	sep := canvas.NewRectangle(ColorElevatedSlate)
	sep.SetMinSize(fyne.NewSize(0, 1))

	return container.NewBorder(sep, nil, nil, nil,
		container.NewStack(bg, container.NewPadded(mw.statusBarLbl)),
	)
}

func buildGridDimensionControls(initial deck.GridSize, initialDials int) (
	presetSelect *widget.Select,
	colsEntry *widget.Entry,
	rowsEntry *widget.Entry,
	dialsSelect *widget.Select,
	resolve func() (deck.GridSize, int, error),
) {
	if initial.Validate() != nil {
		initial = deck.DefaultGrid()
	}

	presetLabels := make([]string, 0, len(studioGridPresets)+1)
	matchedPreset := customGridPresetLabel
	for _, p := range studioGridPresets {
		presetLabels = append(presetLabels, p.label)
		if p.grid == initial {
			matchedPreset = p.label
		}
	}
	presetLabels = append(presetLabels, customGridPresetLabel)

	colsEntry = widget.NewEntry()
	colsEntry.SetPlaceHolder("1–16")
	colsEntry.SetText(strconv.Itoa(initial.Cols))

	rowsEntry = widget.NewEntry()
	rowsEntry.SetPlaceHolder("1–16")
	rowsEntry.SetText(strconv.Itoa(initial.Rows))

	syncing := false
	presetSelect = widget.NewSelect(presetLabels, func(choice string) {
		if syncing {
			return
		}
		for _, p := range studioGridPresets {
			if p.label == choice {
				syncing = true
				colsEntry.SetText(strconv.Itoa(p.grid.Cols))
				rowsEntry.SetText(strconv.Itoa(p.grid.Rows))
				syncing = false
				return
			}
		}
	})
	presetSelect.SetSelected(matchedPreset)

	syncPresetFromEntries := func(_ string) {
		if syncing {
			return
		}
		cols, cErr := strconv.Atoi(strings.TrimSpace(colsEntry.Text))
		rows, rErr := strconv.Atoi(strings.TrimSpace(rowsEntry.Text))
		target := customGridPresetLabel
		if cErr == nil && rErr == nil {
			for _, p := range studioGridPresets {
				if p.grid.Cols == cols && p.grid.Rows == rows {
					target = p.label
					break
				}
			}
		}
		if presetSelect.Selected != target {
			syncing = true
			presetSelect.SetSelected(target)
			syncing = false
		}
	}
	colsEntry.OnChanged = syncPresetFromEntries
	rowsEntry.OnChanged = syncPresetFromEntries

	dialOptions := []string{"0", "1", "2", "3", "4", "5", "6", "7", "8"}
	dialsSelect = widget.NewSelect(dialOptions, nil)
	dialStr := strconv.Itoa(max(0, min(8, initialDials)))
	dialsSelect.SetSelected(dialStr)

	resolve = func() (deck.GridSize, int, error) {
		cols, err := strconv.Atoi(strings.TrimSpace(colsEntry.Text))
		if err != nil {
			return deck.GridSize{}, 0, fmt.Errorf("invalid columns value %q", colsEntry.Text)
		}
		rows, err := strconv.Atoi(strings.TrimSpace(rowsEntry.Text))
		if err != nil {
			return deck.GridSize{}, 0, fmt.Errorf("invalid rows value %q", rowsEntry.Text)
		}
		grid := deck.GridSize{Cols: cols, Rows: rows}
		if err := grid.Validate(); err != nil {
			return deck.GridSize{}, 0, err
		}
		dials, err := strconv.Atoi(dialsSelect.Selected)
		if err != nil {
			dials = initialDials
		}
		return grid, dials, nil
	}

	return presetSelect, colsEntry, rowsEntry, dialsSelect, resolve
}

func (mw *MainWindow) showCreateProfileDialog() {
	nameEntry := widget.NewEntry()
	nameEntry.SetPlaceHolder("e.g. Phone 5×2 / Streaming")

	presetSelect, colsEntry, rowsEntry, dialsSelect, resolveGrid := buildGridDimensionControls(deck.DefaultGrid(), 4)

	items := []*widget.FormItem{
		widget.NewFormItem("Name", nameEntry),
		widget.NewFormItem("Preset", presetSelect),
		widget.NewFormItem("Columns", colsEntry),
		widget.NewFormItem("Rows", rowsEntry),
		widget.NewFormItem("Encoder Dials", dialsSelect),
	}

	dlg := dialog.NewForm("Create Profile", "Create", "Cancel", items, func(confirmed bool) {
		if !confirmed {
			return
		}
		grid, dials, err := resolveGrid()
		if err != nil {
			mw.setStatus("Invalid grid configuration: " + err.Error())
			return
		}
		if _, err := mw.engine.CreateProfile(nameEntry.Text, grid, dials); err != nil {
			mw.setStatus("Create profile failed: " + err.Error())
			return
		}
		mw.setStatus("Created profile " + strings.TrimSpace(nameEntry.Text))
	}, mw.win)
	dlg.Resize(fyne.NewSize(400, 290))
	dlg.Show()
}

func (mw *MainWindow) showGridDialog() {
	snap := mw.engine.Snapshot()
	presetSelect, colsEntry, rowsEntry, dialsSelect, resolveGrid := buildGridDimensionControls(snap.Grid, snap.Dials)

	items := []*widget.FormItem{
		widget.NewFormItem("Preset", presetSelect),
		widget.NewFormItem("Columns", colsEntry),
		widget.NewFormItem("Rows", rowsEntry),
		widget.NewFormItem("Encoder Dials", dialsSelect),
	}

	dlg := dialog.NewForm("Configure Grid & Dials", "Apply", "Cancel", items, func(confirmed bool) {
		if !confirmed {
			return
		}
		grid, dials, err := resolveGrid()
		if err != nil {
			mw.setStatus("Invalid grid configuration: " + err.Error())
			return
		}
		if err := mw.engine.UpdateProfileGrid(snap.ActiveProfileID, grid, dials); err != nil {
			mw.setStatus("Resize grid failed: " + err.Error())
			return
		}
		mw.setStatus(fmt.Sprintf("Grid updated to %dx%d (%d dials)", grid.Cols, grid.Rows, dials))
	}, mw.win)
	dlg.Resize(fyne.NewSize(380, 260))
	dlg.Show()
}
