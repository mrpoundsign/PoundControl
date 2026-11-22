package ui

import (
	"context"
	"fmt"
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

	"github.com/mrpoundsign/PoundDeck/internal/app"
	"github.com/mrpoundsign/PoundDeck/internal/version"
	"github.com/mrpoundsign/PoundDeck/pkg/deck"
	"github.com/mrpoundsign/PoundDeck/pkg/sdplugin"
	"github.com/mrpoundsign/PoundDeck/pkg/wasmplugin"
)

const (
	settingsTabGeneral = 0
	settingsTabOBS     = 1
	settingsTabPlugins = 2
	settingsTabWeb     = 3
)

func showSettingsDialog(
	win fyne.Window,
	engine *app.Engine,
	obsCtrl OBSController,
	sdCtrl SDPluginController,
	wasmCtrl WasmPluginController,
	webCtrl WebDeckController,
	cfgPath string,
	activeHotkeys int,
	initialTab int,
	onOpenPopOut func(),
	onReloadPlugins func() error,
	onStatus func(string),
) *dialog.CustomDialog {
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
		popOutBtn,
		widget.NewSeparator(),
		paletteSection,
	)

	// OBS Studio Tab
	obsContent, obsCleanup := buildOBSConnectionContent(obsCtrl, onStatus)

	// Plugins Tab
	pluginsTab, pluginsCleanup := buildPluginsSettingsTab(win, engine, sdCtrl, wasmCtrl, onReloadPlugins, onStatus)

	// Web Deck Tab
	webTab, webCleanup := buildWebDeckSettingsTab(webCtrl, onStatus)

	tabs := container.NewAppTabs(
		container.NewTabItem("General", container.NewVScroll(container.NewPadded(generalTab))),
		container.NewTabItem("OBS Studio", container.NewPadded(obsContent)),
		container.NewTabItem("Plugins", pluginsTab),
		container.NewTabItem("Web Deck", webTab),
	)
	if initialTab >= 0 && initialTab < len(tabs.Items) {
		tabs.SelectIndex(initialTab)
	}

	dlg := dialog.NewCustom("PoundDeck Settings", "Close", tabs, win)
	dlg.Resize(fyne.NewSize(680, 600))
	dlg.SetOnClosed(func() {
		if generalCleanup != nil {
			generalCleanup()
		}
		if obsCleanup != nil {
			obsCleanup()
		}
		if pluginsCleanup != nil {
			pluginsCleanup()
		}
		if webCleanup != nil {
			webCleanup()
		}
	})
	dlg.Show()
	return dlg
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
	onReloadPlugins func() error,
	onStatus func(string),
) (fyne.CanvasObject, func()) {
	providersBox := container.NewVBox()
	rebuildProviders := func() {
		providersBox.Objects = nil
		for _, p := range engine.Registry().Providers() {
			cardTitle := widget.NewLabel(fmt.Sprintf("%s (%s) — %d actions", p.Name(), p.Kind(), len(p.Actions())))
			cardTitle.TextStyle = fyne.TextStyle{Bold: true}
			providersBox.Add(cardTitle)
		}
		providersBox.Refresh()
	}
	rebuildProviders()

	var (
		refreshes []func()
		cleanups  []func()
	)

	reloadAll := func(reason string) {
		var err error
		switch {
		case onReloadPlugins != nil:
			err = onReloadPlugins()
		default:
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

	items := []fyne.CanvasObject{
		widget.NewLabelWithStyle("Active Providers", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		providersBox,
	}

	if sdCtrl == nil && wasmCtrl == nil {
		msg := widget.NewLabel("External plugin host controllers are not attached in this mode.")
		msg.Wrapping = fyne.TextWrapWord
		items = append(items, widget.NewSeparator(), msg)
		return container.NewVScroll(container.NewPadded(container.NewVBox(items...))), nil
	}

	if sdCtrl != nil {
		sdObj, sdRefresh, sdCleanup := buildSDPluginSection(win, sdCtrl, reloadAll, onStatus)
		items = append(items, widget.NewSeparator(), sdObj)
		if sdRefresh != nil {
			refreshes = append(refreshes, sdRefresh)
		}
		if sdCleanup != nil {
			cleanups = append(cleanups, sdCleanup)
		}
	}

	if wasmCtrl != nil {
		wasmObj, wasmRefresh := buildWasmPluginSection(win, wasmCtrl, reloadAll, onStatus)
		items = append(items, widget.NewSeparator(), wasmObj)
		if wasmRefresh != nil {
			refreshes = append(refreshes, wasmRefresh)
		}
	}

	cleanup := func() {
		for _, fn := range cleanups {
			fn()
		}
	}
	return container.NewVScroll(container.NewPadded(container.NewVBox(items...))), cleanup
}

func buildSDPluginSection(
	win fyne.Window,
	sdCtrl SDPluginController,
	reloadAll func(string),
	onStatus func(string),
) (fyne.CanvasObject, func(), func()) {
	var (
		closed       atomic.Bool
		dlMu         sync.Mutex
		dlCancel     context.CancelFunc
		nodeStatus   *widget.Label
		dlStageLbl   *widget.Label
		dlProgress   *widget.ProgressBar
		dlBtn        *widget.Button
		cancelDlBtn  *widget.Button
		scanElgatoCh *widget.Check
		nodePathEnt  *widget.Entry
	)
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

	scanElgatoTitle := "Scan Elgato Stream Deck Plugins folder"
	if elgatoDir := sdCtrl.ElgatoPluginsDir(); elgatoDir != "" {
		scanElgatoTitle = fmt.Sprintf("Scan Elgato Stream Deck Plugins folder (%s)", elgatoDir)
	}
	scanElgatoCh = widget.NewCheck(scanElgatoTitle, func(checked bool) {
		cur := sdCtrl.HostConfig()
		if cur.ScanElgato == checked {
			return
		}
		cur.ScanElgato = checked
		saveAndReload(cur, "Updated Elgato plugin scan setting")
	})
	scanElgatoCh.SetChecked(cfg.ScanElgato)

	addPathEnt := widget.NewEntry()
	addPathEnt.SetPlaceHolder("Path to a .sdPlugin bundle or parent plugins directory")
	browseBtn := widget.NewButtonWithIcon("Browse...", theme.FolderOpenIcon(), func() {
		dlg := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
			if err == nil && uri != nil {
				addPathEnt.SetText(uri.Path())
			}
		}, win)
		dlg.Show()
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
					metaText += "\n" + st.Error
				}
				metaLbl := widget.NewLabel(metaText)
				metaLbl.Wrapping = fyne.TextWrapBreak

				pluginsContainer.Add(container.NewVBox(titleLbl, metaLbl, widget.NewSeparator()))
			}
		}
		pluginsContainer.Refresh()
	}

	refreshState()

	content := container.NewVBox(
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
		scanElgatoCh,
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
		dlMu.Lock()
		if dlCancel != nil {
			dlCancel()
		}
		dlMu.Unlock()
	}

	return content, refreshState, cleanup
}

func buildWasmPluginSection(
	win fyne.Window,
	wasmCtrl WasmPluginController,
	reloadAll func(string),
	onStatus func(string),
) (fyne.CanvasObject, func()) {
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

	addPathEnt := widget.NewEntry()
	addPathEnt.SetPlaceHolder("Path to a .wasm plugin file or directory")
	browseBtn := widget.NewButtonWithIcon("Browse...", theme.FolderOpenIcon(), func() {
		dlg := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
			if err == nil && uri != nil {
				addPathEnt.SetText(uri.Path())
			}
		}, win)
		dlg.Show()
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
		if len(statuses) == 0 {
			noneLbl := widget.NewLabel("No WebAssembly (.wasm) plugins discovered yet. Drop .wasm files in the default folder or add a path above.")
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

				pluginsContainer.Add(container.NewVBox(titleLbl, metaLbl, widget.NewSeparator()))
			}
		}
		pluginsContainer.Refresh()
	}

	refreshState()

	content := container.NewVBox(
		widget.NewLabelWithStyle("WebAssembly (.wasm) Plugins (wazero)", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewForm(widget.NewFormItem("Default Folder", defaultDirLbl)),
		widget.NewLabel("Extra WebAssembly Paths"),
		extraContainer,
		addPathRow,
		container.NewHBox(reloadBtn),
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Discovered .wasm Modules", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		pluginsContainer,
	)
	return content, refreshState
}
