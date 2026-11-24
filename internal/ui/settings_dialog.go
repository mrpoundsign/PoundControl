package ui

import (
	"cmp"
	"context"
	"fmt"
	"image"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/mrpoundsign/appinstaller/pkg/updater"

	"github.com/mrpoundsign/PoundControl/internal/app"
	"github.com/mrpoundsign/PoundControl/internal/version"
	"github.com/mrpoundsign/PoundControl/pkg/builtin/obs"
	"github.com/mrpoundsign/PoundControl/pkg/deck"
	"github.com/mrpoundsign/PoundControl/pkg/plugin"
	"github.com/mrpoundsign/PoundControl/pkg/pluginmgr"
	"github.com/mrpoundsign/PoundControl/pkg/sdplugin"
	"github.com/mrpoundsign/PoundControl/pkg/wasmplugin"
)

const (
	settingsTabGeneral = 0
	settingsTabOBS     = 1
	settingsTabPlugins = 2
	settingsTabWeb     = 3
)

const (
	settingsSubTabGeneral = 0
	settingsSubTabOBS     = 1
	settingsSubTabWeb     = 2
)

type settingsTabsResult struct {
	tabs        *container.AppTabs
	obsTabIdx   int
	webTabIdx   int
	pluginTabs  map[string]int
	pluginViews map[string]*pluginConfigTabView
	cleanup     func()
}

func buildSettingsTabsView(
	win fyne.Window,
	engine *app.Engine,
	obsCtrl OBSController,
	webCtrl WebDeckController,
	cfgPath string,
	assetsDir func() string,
	activeHotkeys int,
	onOpenPopOut func(),
	onSuspendHotkeys func(bool),
	onStatus func(string),
) settingsTabsResult {
	snap := engine.Snapshot()

	if cfgPath == "" {
		cfgPath = "(in-memory store)"
	}

	// General Tab
	cfgLbl := widget.NewLabel(cfgPath)
	cfgLbl.Wrapping = fyne.TextWrapBreak
	popOutBtn := widget.NewButton("Open Pop-Out Pad", func() {
		if onOpenPopOut != nil {
			onOpenPopOut()
		}
	})
	popOutBtn.Importance = widget.HighImportance

	updateStatusLbl := widget.NewLabel("Version v" + version.Version)
	updateStatusLbl.Wrapping = fyne.TextWrapWord

	var checkUpdatesBtn *widget.Button
	checkUpdatesBtn = widget.NewButtonWithIcon("Check for Updates", theme.DownloadIcon(), func() {
		checkForUpdatesManual(win, version.Version, updateStatusLbl, checkUpdatesBtn, onStatus)
	})

	autoUpdateCheck := widget.NewCheck("Check for updates on startup", nil)
	if a := fyne.CurrentApp(); a != nil {
		autoUpdateCheck.Checked = a.Preferences().BoolWithFallback(prefCheckUpdatesOnStartup, true)
	} else {
		autoUpdateCheck.Checked = true
	}
	autoUpdateCheck.OnChanged = func(checked bool) {
		if a := fyne.CurrentApp(); a != nil {
			a.Preferences().SetBool(prefCheckUpdatesOnStartup, checked)
		}
	}

	autoPluginUpdateCheck := widget.NewCheck("Check for plugin updates on startup", nil)
	if a := fyne.CurrentApp(); a != nil {
		autoPluginUpdateCheck.Checked = a.Preferences().BoolWithFallback(prefCheckPluginUpdatesOnStartup, true)
	} else {
		autoPluginUpdateCheck.Checked = true
	}
	autoPluginUpdateCheck.OnChanged = func(checked bool) {
		if a := fyne.CurrentApp(); a != nil {
			a.Preferences().SetBool(prefCheckPluginUpdatesOnStartup, checked)
		}
	}

	paletteSection, generalCleanup := buildPaletteSettingsSection(win, engine, onStatus)
	generalTab := container.NewVBox(
		widget.NewForm(
			widget.NewFormItem("Application", widget.NewLabel(version.AppName+" v"+version.Version)),
			widget.NewFormItem("Config File", cfgLbl),
			widget.NewFormItem("Active Profile", widget.NewLabel(fmt.Sprintf("%s (%dx%d, %d dials, %d pages)",
				snap.ActiveProfileName, snap.Grid.Cols, snap.Grid.Rows, snap.Dials, snap.PageCount))),
			widget.NewFormItem("Global Hotkeys", widget.NewLabel(fmt.Sprintf("%d active on current page/folder", activeHotkeys))),
		),
		widget.NewSeparator(),
		container.NewHBox(popOutBtn, checkUpdatesBtn),
		autoUpdateCheck,
		autoPluginUpdateCheck,
		updateStatusLbl,
		widget.NewSeparator(),
		paletteSection,
	)

	// OBS Studio Tab (only shown when OBS plugin is enabled)
	var (
		obsContent fyne.CanvasObject
		obsCleanup func()
	)
	if engine.IsPluginEnabled(obs.PluginUUID) {
		obsContent, obsCleanup = buildOBSConnectionContent(obsCtrl, onStatus)
	}

	// Web Deck Tab
	webTab, webCleanup := buildWebDeckSettingsTab(engine, webCtrl, onStatus)

	var configurable []plugin.Manifest
	for _, m := range engine.Registry().Manifests() {
		if len(m.GlobalSettingsSchema) > 0 {
			configurable = append(configurable, m)
		}
	}
	slices.SortFunc(configurable, func(a, b plugin.Manifest) int {
		if c := cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return cmp.Compare(a.UUID, b.UUID)
	})

	tabItems := make([]*container.TabItem, 0, 3+len(configurable))
	tabItems = append(tabItems,
		container.NewTabItem("General", container.NewScroll(container.NewPadded(generalTab))),
	)
	obsTabIdx := -1
	if obsContent != nil {
		obsTabIdx = len(tabItems)
		tabItems = append(tabItems, container.NewTabItem("OBS Studio", container.NewScroll(container.NewPadded(obsContent))))
	}
	webTabIdx := len(tabItems)
	tabItems = append(tabItems, container.NewTabItem("Web Deck", webTab))

	pluginTabs := make(map[string]int, len(configurable))
	pluginViews := make(map[string]*pluginConfigTabView, len(configurable))
	var pluginCleanups []func()
	for _, m := range configurable {
		view, pCleanup := buildPluginConfigTab(win, engine, m, assetsDir, onSuspendHotkeys, onStatus)
		pluginTabs[m.UUID] = len(tabItems)
		pluginViews[m.UUID] = view
		if pCleanup != nil {
			pluginCleanups = append(pluginCleanups, pCleanup)
		}
		tabItems = append(tabItems, container.NewTabItem(m.Name, view.root))
	}

	tabs := container.NewAppTabs(tabItems...)

	cleanup := func() {
		if generalCleanup != nil {
			generalCleanup()
		}
		if obsCleanup != nil {
			obsCleanup()
		}
		if webCleanup != nil {
			webCleanup()
		}
		for _, fn := range pluginCleanups {
			fn()
		}
	}
	return settingsTabsResult{
		tabs:        tabs,
		obsTabIdx:   obsTabIdx,
		webTabIdx:   webTabIdx,
		pluginTabs:  pluginTabs,
		pluginViews: pluginViews,
		cleanup:     cleanup,
	}
}

func buildPaletteSettingsSection(
	win fyne.Window,
	engine *app.Engine,
	onStatus func(string),
) (fyne.CanvasObject, func()) {
	listBox := container.NewVBox()
	var lastPalette []deck.ColorSwatch

	var openEditSwatch func(idx int, sw deck.ColorSwatch)
	rebuildList := func(swatches []deck.ColorSwatch) {
		if len(listBox.Objects) > 0 && slices.Equal(lastPalette, swatches) {
			return
		}
		lastPalette = slices.Clone(swatches)
		listBox.Objects = nil
		if len(swatches) == 0 {
			emptyLbl := widget.NewLabel("No custom swatches saved. Add a color below or click Reset Defaults.")
			emptyLbl.Wrapping = fyne.TextWrapWord
			listBox.Add(emptyLbl)
		} else {
			for i, sw := range swatches {
				idx := i
				entry := sw
				chip := canvas.NewRectangle(parseHexColor(entry.Hex, ColorDarkCharcoal))
				chip.CornerRadius = 4
				chip.StrokeColor = ColorSlateBorder
				chip.StrokeWidth = 1
				chip.SetMinSize(fyne.NewSize(22, 22))

				lbl := widget.NewLabel(fmt.Sprintf("%s (%s)", entry.Name, entry.Hex))
				editBtn := widget.NewButton("Edit", func() {
					openEditSwatch(idx, entry)
				})
				delBtn := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
					cur := engine.ColorPalette()
					if idx < 0 || idx >= len(cur) {
						return
					}
					next := slices.Delete(cur, idx, idx+1)
					if err := engine.SaveColorPalette(next); err != nil && onStatus != nil {
						onStatus("Remove swatch failed: " + err.Error())
					}
				})
				row := container.NewBorder(nil, nil, container.NewCenter(chip), container.NewHBox(editBtn, delBtn), lbl)
				listBox.Add(row)
			}
		}
		listBox.Refresh()
	}

	openEditSwatch = func(idx int, sw deck.ColorSwatch) {
		if win == nil {
			return
		}
		nameEntry := widget.NewEntry()
		nameEntry.SetText(sw.Name)
		hexCtrl := newColorFieldControl(win, "Edit Swatch Color", sw.Hex, nil, nil, onStatus)
		items := []*widget.FormItem{
			widget.NewFormItem("Name", nameEntry),
			widget.NewFormItem("Color", hexCtrl.Object()),
		}
		editDlg := dialog.NewForm("Edit Palette Swatch", "Save", "Cancel", items, func(confirmed bool) {
			if !confirmed {
				return
			}
			normHex, ok := deck.NormalizeHexColor(hexCtrl.Value())
			if !ok {
				if onStatus != nil {
					onStatus("Invalid hex color (expected #RRGGBB)")
				}
				return
			}
			cur := engine.ColorPalette()
			if idx < 0 || idx >= len(cur) {
				return
			}
			cur[idx].Name = strings.TrimSpace(nameEntry.Text)
			cur[idx].Hex = normHex
			if err := engine.SaveColorPalette(cur); err != nil && onStatus != nil {
				onStatus("Update swatch failed: " + err.Error())
			}
		}, win)
		editDlg.Resize(fyne.NewSize(420, 220))
		editDlg.Show()
	}

	nameEntry := widget.NewEntry()
	nameEntry.SetPlaceHolder("Swatch name (optional)")
	hexCtrl := newColorFieldControl(win, "Add Palette Swatch", "#38BDF8", nil, nil, onStatus)

	addBtn := widget.NewButtonWithIcon("Add Swatch", theme.ContentAddIcon(), func() {
		if err := engine.AddColorSwatch(nameEntry.Text, hexCtrl.Value()); err != nil {
			if onStatus != nil {
				onStatus(err.Error())
			}
			return
		}
		nameEntry.SetText("")
		if onStatus != nil {
			onStatus("Added color swatch to palette")
		}
	})
	addBtn.Importance = widget.HighImportance

	resetBtn := widget.NewButton("Reset Defaults", func() {
		if err := engine.ResetColorPalette(); err != nil {
			if onStatus != nil {
				onStatus("Reset palette failed: " + err.Error())
			}
			return
		}
		if onStatus != nil {
			onStatus("Reset color palette to defaults")
		}
	})

	rebuildList(engine.ColorPalette())
	unsub := engine.Subscribe(func(snap app.Snapshot) {
		dispatchUI(func() {
			rebuildList(snap.ColorPalette)
		})
	})

	section := container.NewVBox(
		widget.NewLabelWithStyle("Custom Color Palette", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		listBox,
		widget.NewSeparator(),
		widget.NewForm(
			widget.NewFormItem("Name", nameEntry),
			widget.NewFormItem("Hex", hexCtrl.Object()),
		),
		container.NewHBox(addBtn, resetBtn),
	)
	return section, unsub
}

func buildPluginsSettingsTab(
	win fyne.Window,
	engine *app.Engine,
	sdCtrl SDPluginController,
	wasmCtrl WasmPluginController,
	catalogueClient *sdplugin.CatalogueClient,
	pluginMgr *pluginmgr.Manager,
	piViewer PIViewer,
	onReloadPlugins func() error,
	onSyncPlugins func() error,
	onConfigurePlugin func(string),
	onSetPluginEnabled func(string, bool) error,
	onStatus func(string),
) (fyne.CanvasObject, func()) {
	providersBox := container.NewVBox()
	var rebuildProviders func()
	rebuildProviders = func() {
		providersBox.Objects = nil
		manifests := engine.Registry().AllManifests()
		slices.SortFunc(manifests, func(a, b plugin.Manifest) int {
			aBuilt := a.Provider == plugin.ProviderBuiltin
			bBuilt := b.Provider == plugin.ProviderBuiltin
			if aBuilt != bBuilt {
				if aBuilt {
					return -1
				}
				return 1
			}
			if c := cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
				return c
			}
			return cmp.Compare(a.UUID, b.UUID)
		})

		for _, m := range manifests {
			curEnabled := engine.IsPluginEnabled(m.UUID)
			var headerRow fyne.CanvasObject
			if m.Required {
				chk := widget.NewCheck(m.Name+" (Required)", nil)
				chk.SetChecked(true)
				chk.Disable()
				headerRow = chk
			} else {
				var chk *widget.Check
				reverting := false
				chk = widget.NewCheck(m.Name, nil)
				chk.SetChecked(curEnabled)
				pid := m.UUID
				chk.OnChanged = func(checked bool) {
					if reverting {
						return
					}
					var err error
					if onSetPluginEnabled != nil {
						err = onSetPluginEnabled(pid, checked)
					} else {
						err = engine.SetPluginEnabled(pid, checked)
					}
					if err != nil {
						reverting = true
						chk.SetChecked(!checked)
						reverting = false
						if onStatus != nil {
							onStatus(err.Error())
						}
						return
					}
					rebuildProviders()
				}

				if curEnabled && onConfigurePlugin != nil && (m.UUID == obs.PluginUUID || len(m.GlobalSettingsSchema) > 0) {
					cfgBtn := widget.NewButtonWithIcon("Configure", theme.SettingsIcon(), func() {
						onConfigurePlugin(pid)
					})
					headerRow = container.NewBorder(nil, nil, nil, cfgBtn, chk)
				} else {
					headerRow = chk
				}
			}

			meta := fmt.Sprintf("%s • %d actions", m.Provider, len(m.Actions))
			if desc := strings.TrimSpace(m.Description); desc != "" {
				meta += " — " + desc
			}
			metaLbl := widget.NewLabel(meta)
			metaLbl.Wrapping = fyne.TextWrapWord
			providersBox.Add(container.NewVBox(headerRow, metaLbl, widget.NewSeparator()))
		}
		providersBox.Refresh()
	}
	rebuildProviders()

	var (
		refreshes []func()
		cleanups  []func()
	)

	applySyncOrReload := func(reason string, rescan bool) {
		var err error
		switch {
		case !rescan && onSyncPlugins != nil:
			err = onSyncPlugins()
		case rescan && onReloadPlugins != nil:
			err = onReloadPlugins()
		case rescan:
			if sdCtrl != nil {
				err = sdCtrl.Reload()
			}
			if err == nil && wasmCtrl != nil {
				err = wasmCtrl.Reload()
			}
		}
		if err != nil {
			if onStatus != nil {
				onStatus(fmt.Sprintf("Plugin reload failed: %v", err))
			}
		} else if reason != "" && onStatus != nil {
			onStatus(reason)
		}
		rebuildProviders()
		for _, fn := range refreshes {
			fn()
		}
	}
	reloadAll := func(reason string) { applySyncOrReload(reason, true) }
	syncAll := func(reason string) { applySyncOrReload(reason, false) }

	if pluginMgr == nil && sdCtrl == nil && wasmCtrl == nil {
		msg := widget.NewLabel("External plugin host controllers are not attached in this mode.")
		msg.Wrapping = fyne.TextWrapWord
		items := []fyne.CanvasObject{
			widget.NewLabelWithStyle("Features & Plugins", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			providersBox,
			widget.NewSeparator(),
			msg,
		}
		return container.NewScroll(container.NewPadded(container.NewVBox(items...))), nil
	}

	effectiveCatalogueClient := catalogueClient
	if effectiveCatalogueClient == nil && pluginMgr != nil && pluginMgr.OpenActionClient != nil {
		effectiveCatalogueClient = pluginMgr.OpenActionClient
	}
	if pluginMgr == nil {
		pluginMgr = pluginmgr.NewManager(nil, effectiveCatalogueClient, wasmCtrl, sdCtrl)
	}

	var items []fyne.CanvasObject
	catObj, catRefresh, catCleanup := buildPluginCatalogsAndUpdatesSection(
		win, pluginMgr, sdCtrl, wasmCtrl, effectiveCatalogueClient, piViewer, syncAll, onConfigurePlugin, onStatus,
	)
	items = append(items,
		catObj,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Features & Plugins", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		providersBox,
	)
	if catRefresh != nil {
		refreshes = append(refreshes, catRefresh)
	}
	if catCleanup != nil {
		cleanups = append(cleanups, catCleanup)
	}

	if sdCtrl != nil {
		sdObj, sdRefresh, sdCleanup := buildSDPluginSection(win, sdCtrl, pluginMgr, reloadAll, syncAll, onStatus)
		items = append(items, widget.NewSeparator(), sdObj)
		if sdRefresh != nil {
			refreshes = append(refreshes, sdRefresh)
		}
		if sdCleanup != nil {
			cleanups = append(cleanups, sdCleanup)
		}
	}

	if wasmCtrl != nil {
		wasmObj, wasmRefresh, wasmCleanup := buildWasmPluginSection(win, wasmCtrl, pluginMgr, reloadAll, syncAll, onConfigurePlugin, onStatus)
		items = append(items, widget.NewSeparator(), wasmObj)
		if wasmRefresh != nil {
			refreshes = append(refreshes, wasmRefresh)
		}
		if wasmCleanup != nil {
			cleanups = append(cleanups, wasmCleanup)
		}
	}

	cleanup := func() {
		for _, fn := range cleanups {
			fn()
		}
	}
	return container.NewScroll(container.NewPadded(container.NewVBox(items...))), cleanup
}

func buildPluginCatalogsAndUpdatesSection(
	win fyne.Window,
	pluginMgr *pluginmgr.Manager,
	sdCtrl SDPluginController,
	wasmCtrl WasmPluginController,
	catalogueClient *sdplugin.CatalogueClient,
	piViewer PIViewer,
	reloadAll func(string),
	onConfigurePlugin func(string),
	onStatus func(string),
) (fyne.CanvasObject, func(), func()) {
	var (
		closed atomic.Bool
		busy   atomic.Bool
	)
	ctx, cancel := context.WithCancel(context.Background())

	updatesStatusLbl := widget.NewLabel("Browse verified curated plugins or check installed plugins for updates.")
	updatesStatusLbl.Wrapping = fyne.TextWrapWord

	updatesBox := container.NewVBox()
	var updateAllBtn *widget.Button
	var checkUpdatesBtn *widget.Button

	curatedBtn := widget.NewButtonWithIcon("Browse Curated Plugins...", theme.SearchIcon(), func() {
		showCuratedPluginsDialog(win, pluginMgr, sdCtrl, wasmCtrl, piViewer, reloadAll, onConfigurePlugin, onStatus)
	})
	curatedBtn.Importance = widget.HighImportance

	btnRow := []fyne.CanvasObject{curatedBtn}
	if sdCtrl != nil {
		btnRow = append(btnRow, widget.NewButtonWithIcon("Browse OpenAction Marketplace...", theme.SearchIcon(), func() {
			showOpenActionMarketplaceDialog(win, sdCtrl, catalogueClient, pluginMgr, piViewer, reloadAll, onStatus)
		}))
	}

	runSingleUpdate := func(upd pluginmgr.PluginUpdate) {
		applyPluginUpdateAsync(ctx, &closed, &busy, pluginMgr, upd, func(msg string) {
			updatesStatusLbl.SetText(msg)
		}, reloadAll, onStatus)
	}

	refreshUpdates := func() {
		sum := pluginMgr.LatestSummary()
		updatesBox.Objects = nil

		if !sum.CheckedAt.IsZero() {
			switch {
			case len(sum.Updates) == 0 && len(sum.Errors) == 0:
				updatesStatusLbl.SetText(fmt.Sprintf("All %d managed plugin(s) are up to date.", sum.CheckedCount))
			case len(sum.Updates) == 0:
				updatesStatusLbl.SetText(fmt.Sprintf("Checked %d managed plugin(s) with %d warning(s): %s",
					sum.CheckedCount, len(sum.Errors), sum.Errors[0]))
			case len(sum.Updates) == 1:
				updatesStatusLbl.SetText("1 plugin update available.")
			default:
				updatesStatusLbl.SetText(fmt.Sprintf("%d plugin updates available.", len(sum.Updates)))
			}
		}

		if len(sum.Updates) == 0 {
			updateAllBtn.Hide()
			updatesBox.Refresh()
			return
		}

		updateAllBtn.SetText(fmt.Sprintf("Update All (%d)", len(sum.Updates)))
		updateAllBtn.Show()

		for _, item := range sum.Updates {
			upd := item
			title := fmt.Sprintf("%s [%s] • v%s -> v%s", upd.Name, upd.Kind, upd.CurrentVersion, upd.LatestVersion)
			titleLbl := widget.NewLabel(title)
			titleLbl.TextStyle = fyne.TextStyle{Bold: true}
			titleLbl.Wrapping = fyne.TextWrapWord

			updBtn := widget.NewButtonWithIcon("Update", theme.DownloadIcon(), func() {
				runSingleUpdate(upd)
			})
			updBtn.Importance = widget.HighImportance

			row := container.NewBorder(nil, nil, nil, updBtn, titleLbl)
			subText := "Source: " + upd.Source
			if upd.ReleaseNotes != "" {
				firstLine, _, _ := strings.Cut(upd.ReleaseNotes, "\n")
				if firstLine = strings.TrimSpace(firstLine); firstLine != "" {
					subText += " — " + firstLine
				}
			}
			subLbl := widget.NewLabel(subText)
			subLbl.Wrapping = fyne.TextWrapWord
			updatesBox.Add(container.NewVBox(row, subLbl, widget.NewSeparator()))
		}
		updatesBox.Refresh()
	}

	checkUpdatesBtn = widget.NewButtonWithIcon("Check for Plugin Updates", theme.ViewRefreshIcon(), func() {
		if !busy.CompareAndSwap(false, true) {
			return
		}
		checkUpdatesBtn.Disable()
		updatesStatusLbl.SetText("Checking installed plugins for updates...")
		go func() {
			pluginMgr.CheckUpdates(ctx, true)
			busy.Store(false)
			dispatchUI(func() {
				if closed.Load() {
					return
				}
				checkUpdatesBtn.Enable()
				refreshUpdates()
			})
		}()
	})
	btnRow = append(btnRow, checkUpdatesBtn)

	updateAllBtn = widget.NewButtonWithIcon("Update All", theme.DownloadIcon(), func() {
		pending := pluginMgr.LatestSummary().Updates
		if len(pending) == 0 || !busy.CompareAndSwap(false, true) {
			return
		}
		updateAllBtn.Disable()
		checkUpdatesBtn.Disable()
		go func(list []pluginmgr.PluginUpdate) {
			updatedCount := 0
			var failures []string
			for _, item := range list {
				upd := item
				dispatchUI(func() {
					if !closed.Load() {
						updatesStatusLbl.SetText(fmt.Sprintf("Updating %s to v%s...", upd.Name, upd.LatestVersion))
					}
				})
				if err := pluginMgr.ApplyUpdate(ctx, upd, nil); err != nil {
					failures = append(failures, fmt.Sprintf("%s: %v", upd.Name, err))
				} else {
					updatedCount++
				}
			}
			busy.Store(false)
			dispatchUI(func() {
				if closed.Load() {
					return
				}
				updateAllBtn.Enable()
				checkUpdatesBtn.Enable()
				var msg string
				if len(failures) > 0 {
					msg = fmt.Sprintf("Updated %d plugin(s); %d failed (%s)", updatedCount, len(failures), failures[0])
				} else {
					msg = fmt.Sprintf("Updated %d plugin(s)", updatedCount)
				}
				updatesStatusLbl.SetText(msg)
				reloadAll(msg)
			})
		}(pending)
	})
	updateAllBtn.Importance = widget.HighImportance
	updateAllBtn.Hide()
	btnRow = append(btnRow, updateAllBtn)

	unsub := pluginMgr.SubscribeUpdates(func(_ pluginmgr.UpdateSummary) {
		dispatchUI(func() {
			if !closed.Load() {
				refreshUpdates()
			}
		})
	})

	refreshUpdates()

	section := container.NewVBox(
		widget.NewLabelWithStyle("Plugin Catalogs & Updates", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		container.NewHBox(btnRow...),
		updatesStatusLbl,
		updatesBox,
	)
	cleanup := func() {
		closed.Store(true)
		unsub()
		cancel()
	}
	return section, refreshUpdates, cleanup
}

func pendingUpdatesByID(mgr *pluginmgr.Manager) map[string]pluginmgr.PluginUpdate {
	out := make(map[string]pluginmgr.PluginUpdate)
	if mgr == nil {
		return out
	}
	for _, upd := range mgr.LatestSummary().Updates {
		out[strings.ToLower(upd.UUID)] = upd
	}
	return out
}

func applyPluginUpdateAsync(
	ctx context.Context,
	closed *atomic.Bool,
	busy *atomic.Bool,
	pluginMgr *pluginmgr.Manager,
	upd pluginmgr.PluginUpdate,
	setProgress func(string),
	reloadAll func(string),
	onStatus func(string),
) {
	if pluginMgr == nil || !busy.CompareAndSwap(false, true) {
		return
	}
	startMsg := fmt.Sprintf("Updating %s to v%s...", upd.Name, upd.LatestVersion)
	if setProgress != nil {
		setProgress(startMsg)
	}
	if onStatus != nil {
		onStatus(startMsg)
	}
	go func() {
		err := pluginMgr.ApplyUpdate(ctx, upd, func(_, _ int64, stage string) {
			dispatchUI(func() {
				if closed.Load() {
					return
				}
				msg := fmt.Sprintf("%s: %s", upd.Name, stage)
				if setProgress != nil {
					setProgress(msg)
				}
			})
		})
		busy.Store(false)
		dispatchUI(func() {
			if closed.Load() {
				return
			}
			if err != nil {
				msg := fmt.Sprintf("Update %s failed: %v", upd.Name, err)
				if setProgress != nil {
					setProgress(msg)
				}
				if onStatus != nil {
					onStatus(msg)
				}
				return
			}
			msg := fmt.Sprintf("Updated %s to v%s", upd.Name, upd.LatestVersion)
			if setProgress != nil {
				setProgress(msg)
			}
			if reloadAll != nil {
				reloadAll(msg)
			}
		})
	}()
}

func buildSDPluginSection(
	win fyne.Window,
	sdCtrl SDPluginController,
	pluginMgr *pluginmgr.Manager,
	reloadAll func(string),
	syncAll func(string),
	onStatus func(string),
) (fyne.CanvasObject, func(), func()) {
	var (
		closed         atomic.Bool
		updBusy        atomic.Bool
		dlMu           sync.Mutex
		dlCancel       context.CancelFunc
		nodeStatus     *widget.Label
		dlStageLbl     *widget.Label
		dlProgress     *widget.ProgressBar
		dlBtn          *widget.Button
		cancelDlBtn    *widget.Button
		scanElgatoCh   *widget.Check
		scanOpenDeckCh *widget.Check
		nodePathEnt    *widget.Entry
	)
	updCtx, updCancel := context.WithCancel(context.Background())
	extraContainer := container.NewVBox()
	pluginsContainer := container.NewVBox()

	var refreshState func()

	saveAndReload := func(cfg sdplugin.HostConfig, reason string) {
		if err := sdCtrl.SaveHostConfig(cfg); err != nil {
			if onStatus != nil {
				onStatus(fmt.Sprintf("Failed to save plugin settings: %v", err))
			}
			return
		}
		reloadAll(reason)
	}

	nodeStatus = widget.NewLabel("")
	nodeStatus.Wrapping = fyne.TextWrapWord

	dlStageLbl = widget.NewLabel("")
	dlStageLbl.Wrapping = fyne.TextWrapWord
	dlStageLbl.Hide()

	dlProgress = widget.NewProgressBar()
	dlProgress.Hide()

	dlBtn = widget.NewButtonWithIcon("Download Node.js Runtime", theme.DownloadIcon(), nil)
	dlBtn.Importance = widget.HighImportance
	cancelDlBtn = widget.NewButtonWithIcon("Cancel", theme.CancelIcon(), func() {
		dlMu.Lock()
		if dlCancel != nil {
			dlCancel()
		}
		dlMu.Unlock()
	})
	cancelDlBtn.Hide()

	dlBtn.OnTapped = func() {
		dlMu.Lock()
		if dlCancel != nil {
			dlMu.Unlock()
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		dlCancel = cancel
		dlMu.Unlock()

		dlBtn.Disable()
		cancelDlBtn.Show()
		dlProgress.SetValue(0)
		dlProgress.Show()
		dlStageLbl.SetText("Starting Node.js runtime download...")
		dlStageLbl.Show()

		go func() {
			info, err := sdCtrl.DownloadNode(ctx, func(downloaded, total int64, stage string) {
				dispatchUI(func() {
					if closed.Load() {
						return
					}
					if total > 0 {
						dlProgress.SetValue(float64(downloaded) / float64(total))
						dlStageLbl.SetText(fmt.Sprintf("%s (%.1f / %.1f MB)",
							stage, float64(downloaded)/(1024*1024), float64(total)/(1024*1024)))
					} else {
						dlStageLbl.SetText(stage)
					}
				})
			})

			dlMu.Lock()
			dlCancel = nil
			dlMu.Unlock()
			cancel()

			dispatchUI(func() {
				if closed.Load() {
					return
				}
				dlBtn.Enable()
				cancelDlBtn.Hide()
				dlProgress.Hide()
				if err != nil {
					dlStageLbl.SetText(fmt.Sprintf("Node.js install failed: %v", err))
					dlStageLbl.Show()
					if onStatus != nil {
						onStatus(fmt.Sprintf("Node.js install failed: %v", err))
					}
					return
				}
				dlStageLbl.SetText(fmt.Sprintf("Installed Node.js %s to %s", info.Version, info.Path))
				dlStageLbl.Show()
				reloadAll(fmt.Sprintf("Node.js %s ready; plugins reloaded", info.Version))
			})
		}()
	}

	cfg := sdCtrl.HostConfig()
	nodePathEnt = widget.NewEntry()
	nodePathEnt.SetPlaceHolder("Optional custom node / node.exe binary path")
	nodePathEnt.SetText(cfg.CustomNodePath)
	applyNodeBtn := widget.NewButton("Apply", func() {
		cur := sdCtrl.HostConfig()
		cur.CustomNodePath = strings.TrimSpace(nodePathEnt.Text)
		saveAndReload(cur, "Updated custom Node.js path and reloaded plugins")
	})
	nodeOverrideRow := container.NewBorder(nil, nil, nil, applyNodeBtn, nodePathEnt)

	defaultDirLbl := widget.NewLabel(sdCtrl.DefaultPluginsDir())
	defaultDirLbl.Wrapping = fyne.TextWrapBreak

	scanElgatoCh = widget.NewCheck("Scan Elgato Stream Deck Plugins folder", func(checked bool) {
		cur := sdCtrl.HostConfig()
		if cur.ScanElgato == checked {
			return
		}
		cur.ScanElgato = checked
		saveAndReload(cur, "Updated Elgato plugin scan setting")
	})
	scanElgatoCh.SetChecked(cfg.ScanElgato)

	var scanElgatoBox fyne.CanvasObject = scanElgatoCh
	if elgatoDir := sdCtrl.ElgatoPluginsDir(); elgatoDir != "" {
		dirLbl := widget.NewLabel("    " + elgatoDir)
		dirLbl.Wrapping = fyne.TextWrapBreak
		scanElgatoBox = container.NewVBox(scanElgatoCh, dirLbl)
	}

	scanOpenDeckCh = widget.NewCheck("Scan OpenDeck plugins folder", func(checked bool) {
		cur := sdCtrl.HostConfig()
		if cur.ScanOpenDeck == checked {
			return
		}
		cur.ScanOpenDeck = checked
		saveAndReload(cur, "Updated OpenDeck plugin scan setting")
	})
	scanOpenDeckCh.SetChecked(cfg.ScanOpenDeck)

	var scanOpenDeckBox fyne.CanvasObject = scanOpenDeckCh
	if odDir := sdCtrl.OpenDeckPluginsDir(); odDir != "" {
		dirLbl := widget.NewLabel("    " + odDir)
		dirLbl.Wrapping = fyne.TextWrapBreak
		scanOpenDeckBox = container.NewVBox(scanOpenDeckCh, dirLbl)
	}

	var installArchiveBtn *widget.Button
	installArchiveBtn = widget.NewButtonWithIcon("Install Archive (.streamDeckPlugin / .zip)...", theme.FileApplicationIcon(), func() {
		openNativeFileDialog(win, fileDialogRequest{
			Title: "Install Stream Deck Plugin Archive",
			Filters: []fileDialogFilter{
				{
					Name:       "Stream Deck Plugin Archives",
					Extensions: []string{".streamDeckPlugin", ".streamdeckplugin", ".zip"},
				},
			},
		}, func(archivePath string, err error) {
			if closed.Load() {
				return
			}
			if err != nil {
				if onStatus != nil {
					onStatus(fmt.Sprintf("Plugin archive select failed: %v", err))
				}
				dialog.ShowError(err, win)
				return
			}
			if archivePath == "" {
				return
			}
			installArchiveBtn.Disable()
			if onStatus != nil {
				onStatus("Installing plugin archive...")
			}
			go func() {
				bundle, instErr := sdCtrl.InstallArchive(archivePath)
				dispatchUI(func() {
					if closed.Load() {
						return
					}
					installArchiveBtn.Enable()
					if instErr != nil {
						if onStatus != nil {
							onStatus(fmt.Sprintf("Plugin archive install failed: %v", instErr))
						}
						dialog.ShowError(instErr, win)
						return
					}
					if pluginMgr != nil {
						pluginMgr.NotifyPluginInstalled(bundle.UUID, bundle.Manifest.Version)
					}
					syncAll(fmt.Sprintf("Installed plugin %s (%s)", bundle.Manifest.Name, bundle.UUID))
				})
			}()
		})
	})

	addPathEnt := widget.NewEntry()
	addPathEnt.SetPlaceHolder("Path to a .sdPlugin bundle or parent plugins directory")
	browseBtn := widget.NewButtonWithIcon("Browse...", theme.FolderOpenIcon(), func() {
		openNativeFileDialog(win, fileDialogRequest{
			Title:       "Select Stream Deck Plugins Folder",
			InitialPath: strings.TrimSpace(addPathEnt.Text),
			Directory:   true,
		}, func(dir string, err error) {
			if closed.Load() || err != nil || dir == "" {
				return
			}
			addPathEnt.SetText(dir)
		})
	})
	addPathBtn := widget.NewButtonWithIcon("Add Path", theme.ContentAddIcon(), func() {
		p := strings.TrimSpace(addPathEnt.Text)
		if p == "" {
			return
		}
		cur := sdCtrl.HostConfig()
		cur.ExtraPaths = append(cur.ExtraPaths, p)
		addPathEnt.SetText("")
		saveAndReload(cur, "Added external plugin search path")
	})
	addPathRow := container.NewBorder(nil, nil, nil, container.NewHBox(browseBtn, addPathBtn), addPathEnt)

	reloadBtn := widget.NewButtonWithIcon("Reload Plugins", theme.ViewRefreshIcon(), func() {
		reloadAll("Reloaded external plugins")
	})

	refreshState = func() {
		curCfg := sdCtrl.HostConfig()
		if scanElgatoCh.Checked != curCfg.ScanElgato {
			scanElgatoCh.SetChecked(curCfg.ScanElgato)
		}
		if scanOpenDeckCh.Checked != curCfg.ScanOpenDeck {
			scanOpenDeckCh.SetChecked(curCfg.ScanOpenDeck)
		}

		ni := sdCtrl.NodeStatus(0)
		switch {
		case ni.Found && ni.Usable:
			nodeStatus.SetText(fmt.Sprintf("Node.js %s (%s) ready at %s", ni.Version, ni.Source, ni.Path))
		case ni.Found:
			nodeStatus.SetText(fmt.Sprintf("Node.js %s (%s) found at %s, but %s", ni.Version, ni.Source, ni.Path, ni.Error))
		default:
			nodeStatus.SetText(fmt.Sprintf("Node.js 20+ not found (%s). Download the standalone runtime or specify a custom binary path.", ni.Error))
		}

		extraContainer.Objects = nil
		if len(curCfg.ExtraPaths) == 0 {
			emptyPaths := widget.NewLabel("No custom .sdPlugin paths configured.")
			emptyPaths.Wrapping = fyne.TextWrapWord
			extraContainer.Add(emptyPaths)
		} else {
			for i, pathItem := range curCfg.ExtraPaths {
				idx := i
				pathLbl := widget.NewLabel(pathItem)
				pathLbl.Wrapping = fyne.TextWrapBreak
				rmBtn := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
					latest := sdCtrl.HostConfig()
					if idx < 0 || idx >= len(latest.ExtraPaths) {
						return
					}
					next := make([]string, 0, len(latest.ExtraPaths)-1)
					next = append(next, latest.ExtraPaths[:idx]...)
					next = append(next, latest.ExtraPaths[idx+1:]...)
					latest.ExtraPaths = next
					saveAndReload(latest, "Removed .sdPlugin search path")
				})
				extraContainer.Add(container.NewBorder(nil, nil, nil, rmBtn, pathLbl))
			}
		}
		extraContainer.Refresh()

		pluginsContainer.Objects = nil
		statuses := sdCtrl.Plugins()
		updatesByID := pendingUpdatesByID(pluginMgr)
		if len(statuses) == 0 {
			noneLbl := widget.NewLabel("No .sdPlugin bundles discovered yet. Place bundles in the default folder or add a custom path above.")
			noneLbl.Wrapping = fyne.TextWrapWord
			pluginsContainer.Add(noneLbl)
		} else {
			for _, st := range statuses {
				stateLabel := string(st.State)
				switch st.State {
				case sdplugin.StatusRunning:
					if st.Connected {
						stateLabel = fmt.Sprintf("Running (PID %d, connected)", st.PID)
					} else {
						stateLabel = fmt.Sprintf("Starting (PID %d)", st.PID)
					}
				case sdplugin.StatusNeedsNode:
					req := st.MinNodeMajor
					if req <= 0 {
						req = 20
					}
					stateLabel = fmt.Sprintf("Needs Node.js v%d+", req)
				case sdplugin.StatusError:
					stateLabel = "Error"
				case sdplugin.StatusStopped:
					stateLabel = "Stopped"
				}

				titleText := fmt.Sprintf("%s v%s — %s (%d actions)", st.Name, st.Version, stateLabel, st.ActionCount)
				if st.Version == "" {
					titleText = fmt.Sprintf("%s — %s (%d actions)", st.Name, stateLabel, st.ActionCount)
				}
				titleLbl := widget.NewLabel(titleText)
				titleLbl.TextStyle = fyne.TextStyle{Bold: true}
				titleLbl.Wrapping = fyne.TextWrapWord

				metaText := st.UUID
				if st.Author != "" {
					metaText += " • by " + st.Author
				}
				metaText += "\n" + st.Dir
				if st.Error != "" {
					metaText += "\nError: " + st.Error
				}
				metaLbl := widget.NewLabel(metaText)
				metaLbl.Wrapping = fyne.TextWrapBreak

				var headerRow fyne.CanvasObject = titleLbl
				if sdCtrl.IsUserManagedPlugin(st.Dir) {
					pluginUUID := st.UUID
					pluginName := st.Name
					var btns []fyne.CanvasObject
					if upd, hasUpdate := updatesByID[strings.ToLower(st.UUID)]; hasUpdate {
						updCopy := upd
						updBtn := widget.NewButtonWithIcon("Update to v"+updCopy.LatestVersion, theme.DownloadIcon(), func() {
							applyPluginUpdateAsync(updCtx, &closed, &updBusy, pluginMgr, updCopy, nil, syncAll, onStatus)
						})
						updBtn.Importance = widget.HighImportance
						btns = append(btns, updBtn)
					}
					uninstBtn := widget.NewButtonWithIcon("Uninstall", theme.DeleteIcon(), func() {
						if err := sdCtrl.UninstallPlugin(pluginUUID); err != nil {
							if onStatus != nil {
								onStatus(fmt.Sprintf("Uninstall %s failed: %v", pluginName, err))
							}
							dialog.ShowError(err, win)
							return
						}
						if pluginMgr != nil {
							pluginMgr.NotifyPluginRemoved(pluginUUID)
						}
						syncAll("Uninstalled plugin " + pluginName)
					})
					btns = append(btns, uninstBtn)
					headerRow = container.NewBorder(nil, nil, nil, container.NewHBox(btns...), titleLbl)
				}

				pluginsContainer.Add(container.NewVBox(headerRow, metaLbl, widget.NewSeparator()))
			}
		}
		pluginsContainer.Refresh()
	}

	refreshState()

	content := container.NewVBox(
		widget.NewLabelWithStyle("Local .sdPlugin Archives & Node.js Runtime", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		container.NewHBox(installArchiveBtn),
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Node.js Runtime (for .sdPlugin bundles)", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		nodeStatus,
		container.NewHBox(dlBtn, cancelDlBtn),
		dlProgress,
		dlStageLbl,
		widget.NewLabel("Custom Node.js Binary Override"),
		nodeOverrideRow,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("External .sdPlugin Directories", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewForm(widget.NewFormItem("Default Folder", defaultDirLbl)),
		scanElgatoBox,
		scanOpenDeckBox,
		widget.NewLabel("Extra Plugin Paths"),
		extraContainer,
		addPathRow,
		container.NewHBox(reloadBtn),
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Discovered .sdPlugin Bundles", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		pluginsContainer,
	)

	cleanup := func() {
		closed.Store(true)
		updCancel()
		dlMu.Lock()
		if dlCancel != nil {
			dlCancel()
		}
		dlMu.Unlock()
	}

	return content, refreshState, cleanup
}

func showOpenActionMarketplaceDialog(
	win fyne.Window,
	sdCtrl SDPluginController,
	client *sdplugin.CatalogueClient,
	pluginMgr *pluginmgr.Manager,
	piViewer PIViewer,
	reloadAll func(string),
	onStatus func(string),
) *dialog.CustomDialog {
	if client == nil {
		client = sdplugin.NewCatalogueClient()
	}

	var (
		closed  atomic.Bool
		busy    atomic.Bool
		entries []sdplugin.CatalogueEntry
		iconMu  sync.RWMutex
		icons   = make(map[string]image.Image)
		iconSem = make(chan struct{}, 6)
		gen     atomic.Uint64
	)
	ctx, cancel := context.WithCancel(context.Background())

	searchEnt := widget.NewEntry()
	searchEnt.SetPlaceHolder("Search OpenAction plugins by name, author, category, or ID...")

	statusLbl := widget.NewLabel("Loading OpenAction Marketplace catalogue...")
	statusLbl.Wrapping = fyne.TextWrapWord

	listBox := container.NewVBox()

	openMoreInfo := func(item sdplugin.CatalogueEntry) {
		statusLbl.SetText("Loading full info & README for " + item.Name + "...")
		go func() {
			details := client.FetchPluginDetails(ctx, item)
			dispatchUI(func() {
				if closed.Load() {
					return
				}
				if err := showPluginDetailsViewer(win, sdCtrl, piViewer, details, "Plugin Info"); err != nil {
					statusLbl.SetText(fmt.Sprintf("Failed to open info for %s: %v", item.Name, err))
					return
				}
				statusLbl.SetText("Opened plugin details for " + item.Name)
			})
		}()
	}

	var renderList func()
	renderList = func() {
		curGen := gen.Add(1)
		listBox.Objects = nil
		filtered := sdplugin.FilterCatalogue(entries, searchEnt.Text)
		if len(filtered) == 0 {
			emptyLbl := widget.NewLabel("No matching OpenAction plugins found.")
			emptyLbl.Wrapping = fyne.TextWrapWord
			listBox.Add(emptyLbl)
			listBox.Refresh()
			return
		}

		installedByID := make(map[string]sdplugin.PluginStatus)
		for _, st := range sdCtrl.Plugins() {
			installedByID[strings.ToLower(st.UUID)] = st
		}

		for _, entry := range filtered {
			item := entry
			inst, isInstalled := installedByID[strings.ToLower(item.ID)]
			userManaged := isInstalled && sdCtrl.IsUserManagedPlugin(inst.Dir)
			hasNewer := isInstalled && item.Version != "" && updater.CompareVersions(item.Version, inst.Version) > 0

			title := item.Name
			if item.Version != "" {
				title += " v" + item.Version
			}
			var badges []string
			switch {
			case isInstalled && !userManaged:
				badges = append(badges, "[Installed Externally]")
			case hasNewer:
				badges = append(badges, fmt.Sprintf("[Update: %s -> %s]", inst.Version, item.Version))
			case isInstalled:
				badges = append(badges, "[Installed]")
			}
			if item.IsAIAssisted() {
				badges = append(badges, "[AI-Assisted]")
			}
			if len(badges) > 0 {
				title += " " + strings.Join(badges, " ")
			}

			titleLbl := widget.NewLabel(title)
			titleLbl.TextStyle = fyne.TextStyle{Bold: true}
			titleLbl.Wrapping = fyne.TextWrapWord

			var metaParts []string
			metaParts = append(metaParts, item.ID)
			if item.Author != "" {
				metaParts = append(metaParts, "by "+item.Author)
			}
			if item.Category != "" {
				metaParts = append(metaParts, item.Category)
			}
			metaText := strings.Join(metaParts, " • ")
			if item.Description != "" {
				metaText += "\n" + item.Description
			}
			metaLbl := widget.NewLabel(metaText)
			metaLbl.Wrapping = fyne.TextWrapWord

			iconBox := buildAsyncCatalogIconSlot(
				ctx,
				item.IconURL,
				func() ([]byte, error) {
					return client.FetchIcon(ctx, item)
				},
				&iconMu,
				icons,
				iconSem,
				&closed,
				&gen,
				curGen,
			)

			var actionBtns []fyne.CanvasObject
			infoBtn := widget.NewButtonWithIcon("More Info", theme.InfoIcon(), func() {
				openMoreInfo(item)
			})
			actionBtns = append(actionBtns, infoBtn)

			installLabel := "Install"
			if isInstalled && userManaged {
				if hasNewer {
					installLabel = "Update"
				} else {
					installLabel = "Reinstall"
				}
			}
			installBtn := widget.NewButton(installLabel, func() {
				if !busy.CompareAndSwap(false, true) {
					return
				}
				statusLbl.SetText(fmt.Sprintf("Installing %s...", item.Name))
				go func() {
					bundle, err := sdCtrl.InstallFromCatalogue(ctx, client, item, func(_, _ int64, stage string) {
						dispatchUI(func() {
							if !closed.Load() {
								statusLbl.SetText(fmt.Sprintf("%s: %s", item.Name, stage))
							}
						})
					})
					busy.Store(false)
					dispatchUI(func() {
						if closed.Load() {
							return
						}
						if err != nil {
							statusLbl.SetText(fmt.Sprintf("Install %s failed: %v", item.Name, err))
							if onStatus != nil {
								onStatus(fmt.Sprintf("Install %s failed: %v", item.Name, err))
							}
							return
						}
						if pluginMgr != nil {
							pluginMgr.NotifyPluginInstalled(bundle.UUID, bundle.Manifest.Version)
						}
						msg := fmt.Sprintf("Installed %s (%s)", bundle.Manifest.Name, bundle.UUID)
						statusLbl.SetText(msg)
						if reloadAll != nil {
							reloadAll(msg)
						}
						renderList()
					})
				}()
			})
			if !isInstalled || (userManaged && hasNewer) {
				installBtn.Importance = widget.HighImportance
			}
			actionBtns = append(actionBtns, installBtn)

			if userManaged {
				uninstBtn := widget.NewButton("Uninstall", func() {
					if !busy.CompareAndSwap(false, true) {
						return
					}
					err := sdCtrl.UninstallPlugin(inst.UUID)
					busy.Store(false)
					if err != nil {
						statusLbl.SetText(fmt.Sprintf("Uninstall %s failed: %v", item.Name, err))
						return
					}
					if pluginMgr != nil {
						pluginMgr.NotifyPluginRemoved(inst.UUID)
					}
					msg := "Uninstalled " + item.Name
					statusLbl.SetText(msg)
					if reloadAll != nil {
						reloadAll(msg)
					}
					renderList()
				})
				actionBtns = append(actionBtns, uninstBtn)
			}

			header := container.NewBorder(nil, nil, nil, container.NewHBox(actionBtns...), titleLbl)
			bodyCol := container.NewVBox(header, metaLbl)
			row := container.NewBorder(nil, nil, iconBox, nil, bodyCol)
			listBox.Add(container.NewVBox(row, widget.NewSeparator()))
		}
		listBox.Refresh()
	}

	loadCatalogue := func(force bool) {
		statusLbl.SetText("Fetching OpenAction Marketplace catalogue...")
		go func() {
			fetched, err := client.Fetch(ctx, force)
			dispatchUI(func() {
				if closed.Load() {
					return
				}
				if err != nil {
					statusLbl.SetText(fmt.Sprintf("Failed to load OpenAction Marketplace: %v", err))
					return
				}
				entries = fetched
				statusLbl.SetText(fmt.Sprintf("Loaded %d OpenAction Marketplace plugins", len(entries)))
				renderList()
			})
		}()
	}

	searchEnt.OnChanged = func(_ string) {
		renderList()
	}
	refreshBtn := widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), func() {
		loadCatalogue(true)
	})

	topBar := container.NewVBox(
		container.NewBorder(nil, nil, nil, refreshBtn, searchEnt),
		statusLbl,
		widget.NewSeparator(),
	)
	body := container.NewBorder(topBar, nil, nil, nil, container.NewScroll(container.NewPadded(listBox)))

	dlg := dialog.NewCustom("OpenAction Marketplace", "Close", body, win)
	resizePluginCatalogDialog(dlg, win)
	dlg.SetOnClosed(func() {
		closed.Store(true)
		cancel()
	})
	dlg.Show()
	loadCatalogue(false)
	return dlg
}

func buildWasmPluginSection(
	win fyne.Window,
	wasmCtrl WasmPluginController,
	pluginMgr *pluginmgr.Manager,
	reloadAll func(string),
	syncAll func(string),
	onConfigurePlugin func(string),
	onStatus func(string),
) (fyne.CanvasObject, func(), func()) {
	var (
		closed  atomic.Bool
		updBusy atomic.Bool
	)
	updCtx, updCancel := context.WithCancel(context.Background())

	extraContainer := container.NewVBox()
	pluginsContainer := container.NewVBox()

	saveAndReload := func(cfg wasmplugin.HostConfig, reason string) {
		if err := wasmCtrl.SaveHostConfig(cfg); err != nil {
			if onStatus != nil {
				onStatus("Failed to save WebAssembly settings: " + err.Error())
			}
			return
		}
		reloadAll(reason)
	}

	defaultDirLbl := widget.NewLabel(wasmCtrl.DefaultPluginsDir())
	defaultDirLbl.Wrapping = fyne.TextWrapBreak

	var installWasmBtn *widget.Button
	installWasmBtn = widget.NewButtonWithIcon("Install Local .wasm...", theme.FileApplicationIcon(), func() {
		openNativeFileDialog(win, fileDialogRequest{
			Title: "Install Local WebAssembly Plugin",
			Filters: []fileDialogFilter{
				{
					Name:       "WebAssembly Plugins",
					Extensions: []string{".wasm"},
				},
			},
		}, func(wasmPath string, err error) {
			if closed.Load() {
				return
			}
			if err != nil {
				if onStatus != nil {
					onStatus(fmt.Sprintf("Wasm plugin select failed: %v", err))
				}
				dialog.ShowError(err, win)
				return
			}
			if wasmPath == "" {
				return
			}
			installWasmBtn.Disable()
			if onStatus != nil {
				onStatus("Installing WebAssembly plugin...")
			}
			go func() {
				st, instErr := wasmCtrl.InstallFile(wasmPath, &wasmplugin.InstallRecord{Source: "local"})
				dispatchUI(func() {
					if closed.Load() {
						return
					}
					installWasmBtn.Enable()
					if instErr != nil {
						if onStatus != nil {
							onStatus(fmt.Sprintf("Wasm plugin install failed: %v", instErr))
						}
						dialog.ShowError(instErr, win)
						return
					}
					if pluginMgr != nil {
						pluginMgr.NotifyPluginInstalled(st.UUID, st.Version)
					}
					syncAll(fmt.Sprintf("Installed WebAssembly plugin %s (%s)", st.Name, st.UUID))
				})
			}()
		})
	})

	addPathEnt := widget.NewEntry()
	addPathEnt.SetPlaceHolder("Path to a .wasm plugin file or directory")
	browseBtn := widget.NewButtonWithIcon("Browse...", theme.FolderOpenIcon(), func() {
		openNativeFileDialog(win, fileDialogRequest{
			Title:       "Select WebAssembly Plugins Folder",
			InitialPath: strings.TrimSpace(addPathEnt.Text),
			Directory:   true,
		}, func(dir string, err error) {
			if closed.Load() || err != nil || dir == "" {
				return
			}
			addPathEnt.SetText(dir)
		})
	})
	addPathBtn := widget.NewButtonWithIcon("Add Path", theme.ContentAddIcon(), func() {
		p := strings.TrimSpace(addPathEnt.Text)
		if p == "" {
			return
		}
		cur := wasmCtrl.HostConfig()
		cur.ExtraPaths = append(cur.ExtraPaths, p)
		addPathEnt.SetText("")
		saveAndReload(cur, "Added WebAssembly search path")
	})
	addPathRow := container.NewBorder(nil, nil, nil, container.NewHBox(browseBtn, addPathBtn), addPathEnt)

	reloadBtn := widget.NewButtonWithIcon("Reload Plugins", theme.ViewRefreshIcon(), func() {
		reloadAll("Reloaded external plugins")
	})

	refreshState := func() {
		curCfg := wasmCtrl.HostConfig()
		extraContainer.Objects = nil
		if len(curCfg.ExtraPaths) == 0 {
			emptyLbl := widget.NewLabel("No custom WebAssembly paths configured.")
			emptyLbl.Wrapping = fyne.TextWrapWord
			extraContainer.Add(emptyLbl)
		} else {
			for i, pathItem := range curCfg.ExtraPaths {
				idx := i
				pathLbl := widget.NewLabel(pathItem)
				pathLbl.Wrapping = fyne.TextWrapBreak
				rmBtn := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
					latest := wasmCtrl.HostConfig()
					if idx < 0 || idx >= len(latest.ExtraPaths) {
						return
					}
					next := make([]string, 0, len(latest.ExtraPaths)-1)
					next = append(next, latest.ExtraPaths[:idx]...)
					next = append(next, latest.ExtraPaths[idx+1:]...)
					latest.ExtraPaths = next
					saveAndReload(latest, "Removed WebAssembly search path")
				})
				extraContainer.Add(container.NewBorder(nil, nil, nil, rmBtn, pathLbl))
			}
		}
		extraContainer.Refresh()

		pluginsContainer.Objects = nil
		statuses := wasmCtrl.Plugins()
		updatesByID := pendingUpdatesByID(pluginMgr)
		if len(statuses) == 0 {
			noneLbl := widget.NewLabel("No WebAssembly (.wasm) plugins discovered yet. Install from Curated Plugins, drop .wasm files in the default folder, or add a path above.")
			noneLbl.Wrapping = fyne.TextWrapWord
			pluginsContainer.Add(noneLbl)
		} else {
			for _, st := range statuses {
				stateLabel := string(st.State)
				switch st.State {
				case wasmplugin.StatusRunning:
					stateLabel = "Running (sandboxed)"
				case wasmplugin.StatusError:
					stateLabel = "Error"
				case wasmplugin.StatusStopped:
					stateLabel = "Stopped"
				}

				titleText := fmt.Sprintf("%s v%s — %s (%d actions)", st.Name, st.Version, stateLabel, st.ActionCount)
				if st.Version == "" {
					titleText = fmt.Sprintf("%s — %s (%d actions)", st.Name, stateLabel, st.ActionCount)
				}
				titleLbl := widget.NewLabel(titleText)
				titleLbl.TextStyle = fyne.TextStyle{Bold: true}
				titleLbl.Wrapping = fyne.TextWrapWord

				metaText := st.UUID
				if st.Author != "" {
					metaText += " • by " + st.Author
				}
				if len(st.AllowedHosts) > 0 {
					metaText += "\nAllowed HTTP hosts: " + strings.Join(st.AllowedHosts, ", ")
				}
				metaText += "\n" + st.Path
				if st.Error != "" {
					metaText += "\n" + st.Error
				}
				metaLbl := widget.NewLabel(metaText)
				metaLbl.Wrapping = fyne.TextWrapBreak

				managed := wasmCtrl.IsUserManagedPlugin(st.Path)
				var btns []fyne.CanvasObject
				if managed {
					if upd, hasUpdate := updatesByID[strings.ToLower(st.UUID)]; hasUpdate {
						updCopy := upd
						updBtn := widget.NewButtonWithIcon("Update to v"+updCopy.LatestVersion, theme.DownloadIcon(), func() {
							applyPluginUpdateAsync(updCtx, &closed, &updBusy, pluginMgr, updCopy, nil, syncAll, onStatus)
						})
						updBtn.Importance = widget.HighImportance
						btns = append(btns, updBtn)
					}
				}
				if st.HasGlobalSettings && onConfigurePlugin != nil {
					pluginUUID := st.UUID
					btns = append(btns, widget.NewButtonWithIcon("Configure", theme.SettingsIcon(), func() {
						onConfigurePlugin(pluginUUID)
					}))
				}
				if managed && st.UUID != "" {
					pluginUUID := st.UUID
					pluginName := st.Name
					btns = append(btns, widget.NewButtonWithIcon("Uninstall", theme.DeleteIcon(), func() {
						if err := wasmCtrl.UninstallPlugin(pluginUUID); err != nil {
							if onStatus != nil {
								onStatus(fmt.Sprintf("Uninstall %s failed: %v", pluginName, err))
							}
							dialog.ShowError(err, win)
							return
						}
						if pluginMgr != nil {
							pluginMgr.NotifyPluginRemoved(pluginUUID)
						}
						syncAll("Uninstalled WebAssembly plugin " + pluginName)
					}))
				}

				var headerRow fyne.CanvasObject = titleLbl
				if len(btns) > 0 {
					headerRow = container.NewBorder(nil, nil, nil, container.NewHBox(btns...), titleLbl)
				}

				pluginsContainer.Add(container.NewVBox(headerRow, metaLbl, widget.NewSeparator()))
			}
		}
		pluginsContainer.Refresh()
	}

	refreshState()

	content := container.NewVBox(
		widget.NewLabelWithStyle("WebAssembly (.wasm) Plugins (wazero)", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		container.NewHBox(installWasmBtn),
		widget.NewForm(widget.NewFormItem("Default Folder", defaultDirLbl)),
		widget.NewLabel("Extra WebAssembly Paths"),
		extraContainer,
		addPathRow,
		container.NewHBox(reloadBtn),
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Discovered .wasm Modules", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		pluginsContainer,
	)
	cleanup := func() {
		closed.Store(true)
		updCancel()
	}
	return content, refreshState, cleanup
}
