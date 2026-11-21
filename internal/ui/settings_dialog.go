package ui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/mrpoundsign/PoundDeck/internal/app"
	"github.com/mrpoundsign/PoundDeck/internal/version"
	"github.com/mrpoundsign/PoundDeck/pkg/sdplugin"
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
	)

	// OBS Studio Tab
	obsContent, obsCleanup := buildOBSConnectionContent(obsCtrl, onStatus)

	// Plugins Tab
	pluginsTab, pluginsCleanup := buildPluginsSettingsTab(win, engine, sdCtrl, onReloadPlugins, onStatus)

	// Web Deck Tab (Milestone 6 preview)
	webLbl := widget.NewLabel(
		"LAN Web / Mobile / Tablet Touch Deck arrives in Milestone 6.\n\n" +
			"It will expose the same live virtual buttons and encoder dials over an authenticated local " +
			"HTTP/WebSocket server with QR pairing so phones and tablets can control PoundDeck over Wi-Fi.",
	)
	webLbl.Wrapping = fyne.TextWrapWord
	webTab := container.NewPadded(webLbl)

	tabs := container.NewAppTabs(
		container.NewTabItem("General", container.NewPadded(generalTab)),
		container.NewTabItem("OBS Studio", container.NewPadded(obsContent)),
		container.NewTabItem("Plugins", pluginsTab),
		container.NewTabItem("Web Deck", webTab),
	)
	if initialTab >= 0 && initialTab < len(tabs.Items) {
		tabs.SelectIndex(initialTab)
	}

	dlg := dialog.NewCustom("PoundDeck Settings", "Close", tabs, win)
	dlg.Resize(fyne.NewSize(660, 520))
	dlg.SetOnClosed(func() {
		if obsCleanup != nil {
			obsCleanup()
		}
		if pluginsCleanup != nil {
			pluginsCleanup()
		}
	})
	dlg.Show()
	return dlg
}

func buildPluginsSettingsTab(
	win fyne.Window,
	engine *app.Engine,
	sdCtrl SDPluginController,
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

	if sdCtrl == nil {
		upcomingLbl := widget.NewLabel(
			"External plugin host controller is not attached in this mode.\n" +
				"  • Milestone 5 adds pure-Go wazero WebAssembly (.wasm) dynamic plugins",
		)
		upcomingLbl.Wrapping = fyne.TextWrapWord
		return container.NewVScroll(container.NewPadded(container.NewVBox(
			providersBox,
			widget.NewSeparator(),
			upcomingLbl,
		))), nil
	}

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

	reloadAll := func(reason string) {
		var err error
		if onReloadPlugins != nil {
			err = onReloadPlugins()
		} else {
			err = sdCtrl.Reload()
		}
		if err != nil {
			if onStatus != nil {
				onStatus(fmt.Sprintf("Plugin reload failed: %v", err))
			}
		} else if reason != "" && onStatus != nil {
			onStatus(reason)
		}
		refreshState()
	}

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
		reloadAll("Reloaded external .sdPlugin bundles")
	})

	refreshState = func() {
		rebuildProviders()

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
			emptyPaths := widget.NewLabel("No custom external plugin paths configured.")
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
					saveAndReload(latest, "Removed external plugin search path")
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

	upcomingLbl := widget.NewLabel("Milestone 5 will add pure-Go wazero WebAssembly (.wasm) dynamic plugins.")
	upcomingLbl.Wrapping = fyne.TextWrapWord

	content := container.NewVBox(
		widget.NewLabelWithStyle("Active Providers", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		providersBox,
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
		scanElgatoCh,
		widget.NewLabel("Extra Plugin Paths"),
		extraContainer,
		addPathRow,
		container.NewHBox(reloadBtn),
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Discovered .sdPlugin Bundles", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		pluginsContainer,
		upcomingLbl,
	)

	cleanup := func() {
		closed.Store(true)
		dlMu.Lock()
		if dlCancel != nil {
			dlCancel()
		}
		dlMu.Unlock()
	}

	return container.NewVScroll(container.NewPadded(content)), cleanup
}
