package ui

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"
	"sync"
	"sync/atomic"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/mrpoundsign/PoundControl/pkg/plugin"
	"github.com/mrpoundsign/PoundControl/pkg/pluginmgr"
	"github.com/mrpoundsign/PoundControl/pkg/sdplugin"
	"github.com/mrpoundsign/PoundControl/pkg/tile"
	"github.com/mrpoundsign/PoundControl/pkg/wasmplugin"
)

func decodeCatalogIcon(raw []byte) (image.Image, error) {
	if len(raw) == 0 {
		return nil, errors.New("empty icon payload")
	}
	if decoded, _, err := image.Decode(bytes.NewReader(raw)); err == nil && decoded != nil {
		return decoded, nil
	}
	head := bytes.ToLower(bytes.TrimSpace(raw[:min(len(raw), 512)]))
	if bytes.Contains(head, []byte("<svg")) {
		uri := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(raw)
		return tile.DecodeImage(uri, 64)
	}
	return nil, errors.New("unsupported icon image format")
}

func showPluginDetailsViewer(
	win fyne.Window,
	sdCtrl SDPluginController,
	piViewer PIViewer,
	details sdplugin.PluginDetails,
	titleSuffix string,
) error {
	title := details.Entry.Name + " — " + titleSuffix
	if sdCtrl != nil && piViewer != nil {
		htmlPage := sdplugin.RenderPluginDetailsHTML(details)
		if detailURL, hasURL := sdCtrl.RegisterMarketplaceDetailPage(htmlPage); hasURL {
			return piViewer.Open(title, detailURL)
		}
	}

	var b strings.Builder
	b.WriteString(details.Entry.Name)
	if details.LatestVersion != "" {
		b.WriteString(" v" + details.LatestVersion)
	}
	if details.Entry.Author != "" {
		b.WriteString("\nAuthor: " + details.Entry.Author)
	}
	if details.Entry.Repository != "" {
		b.WriteString("\nRepository: " + details.Entry.Repository)
	}
	if details.Entry.Description != "" {
		b.WriteString("\n\n" + details.Entry.Description)
	}
	if details.ReleaseNotes != "" {
		b.WriteString("\n\nLatest Release Notes:\n" + details.ReleaseNotes)
	}
	lbl := widget.NewLabel(b.String())
	lbl.Wrapping = fyne.TextWrapWord
	infoDlg := dialog.NewCustom(title, "Close", container.NewVScroll(container.NewPadded(lbl)), win)
	infoDlg.Resize(fyne.NewSize(540, 420))
	infoDlg.Show()
	return nil
}

func buildAsyncCatalogIconSlot(
	ctx context.Context,
	iconURL string,
	fetchIcon func() ([]byte, error),
	iconMu *sync.RWMutex,
	icons map[string]image.Image,
	iconSem chan struct{},
	closed *atomic.Bool,
	gen *atomic.Uint64,
	renderGen uint64,
) fyne.CanvasObject {
	iconBg := canvas.NewRectangle(ColorElevatedSlate)
	iconBg.CornerRadius = 8
	iconBg.SetMinSize(fyne.NewSize(44, 44))

	iconImg := canvas.NewImageFromResource(theme.FileApplicationIcon())
	iconImg.FillMode = canvas.ImageFillContain
	iconImg.SetMinSize(fyne.NewSize(40, 40))

	if iconURL != "" && fetchIcon != nil {
		iconMu.RLock()
		cachedImg, hasIcon := icons[iconURL]
		iconMu.RUnlock()
		if hasIcon && cachedImg != nil {
			iconImg.Resource = nil
			iconImg.Image = cachedImg
		} else if !hasIcon {
			go func(target *canvas.Image) {
				select {
				case iconSem <- struct{}{}:
					defer func() { <-iconSem }()
				case <-ctx.Done():
					return
				}
				if closed.Load() || gen.Load() != renderGen {
					return
				}
				raw, err := fetchIcon()
				if err != nil || len(raw) == 0 {
					return
				}
				decoded, decErr := decodeCatalogIcon(raw)
				if decErr != nil || decoded == nil {
					return
				}
				iconMu.Lock()
				icons[iconURL] = decoded
				iconMu.Unlock()
				dispatchUI(func() {
					if closed.Load() || gen.Load() != renderGen {
						return
					}
					target.Resource = nil
					target.Image = decoded
					target.Refresh()
				})
			}(iconImg)
		}
	}
	return container.NewCenter(container.NewStack(iconBg, container.NewPadded(iconImg)))
}

func resizePluginCatalogDialog(dlg *dialog.CustomDialog, win fyne.Window) {
	dlgW, dlgH := float32(780), float32(600)
	if win != nil && win.Canvas() != nil {
		cs := win.Canvas().Size()
		if cs.Width > 120 {
			dlgW = min(dlgW, cs.Width-48)
		}
		if cs.Height > 120 {
			dlgH = min(dlgH, cs.Height-48)
		}
	}
	dlg.Resize(fyne.NewSize(dlgW, dlgH))
}

func showCuratedPluginsDialog(
	win fyne.Window,
	mgr *pluginmgr.Manager,
	sdCtrl SDPluginController,
	wasmCtrl WasmPluginController,
	piViewer PIViewer,
	reloadAll func(string),
	onConfigurePlugin func(string),
	onStatus func(string),
) *dialog.CustomDialog {
	if mgr == nil {
		mgr = pluginmgr.NewManager(nil, nil, wasmCtrl, sdCtrl)
	}
	if mgr.CuratedClient == nil {
		mgr.CuratedClient = pluginmgr.NewCuratedClient()
	}

	var (
		closed  atomic.Bool
		busy    atomic.Bool
		entries []pluginmgr.CuratedEntry
		iconMu  sync.RWMutex
		icons   = make(map[string]image.Image)
		iconSem = make(chan struct{}, 6)
		gen     atomic.Uint64
	)
	ctx, cancel := context.WithCancel(context.Background())

	searchEnt := widget.NewEntry()
	searchEnt.SetPlaceHolder("Search curated plugins by name, author, category, or ID...")

	statusLbl := widget.NewLabel("Loading Curated Plugins catalog...")
	statusLbl.Wrapping = fyne.TextWrapWord

	listBox := container.NewVBox()

	var dlg *dialog.CustomDialog

	openMoreInfo := func(item pluginmgr.CuratedEntry) {
		statusLbl.SetText("Loading full info & README for " + item.Name + "...")
		go func() {
			details := mgr.CuratedClient.FetchPluginDetails(ctx, item)
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
		filtered := pluginmgr.FilterCurated(entries, searchEnt.Text)
		if len(filtered) == 0 {
			emptyLbl := widget.NewLabel("No matching curated plugins found.")
			emptyLbl.Wrapping = fyne.TextWrapWord
			listBox.Add(emptyLbl)
			listBox.Refresh()
			return
		}

		wasmByID := make(map[string]wasmplugin.PluginStatus)
		if wasmCtrl != nil {
			for _, st := range wasmCtrl.Plugins() {
				wasmByID[strings.ToLower(st.UUID)] = st
			}
		}
		sdByID := make(map[string]sdplugin.PluginStatus)
		if sdCtrl != nil {
			for _, st := range sdCtrl.Plugins() {
				sdByID[strings.ToLower(st.UUID)] = st
			}
		}
		updatesByID := make(map[string]pluginmgr.PluginUpdate)
		for _, upd := range mgr.LatestSummary().Updates {
			updatesByID[strings.ToLower(upd.UUID)] = upd
		}

		for _, entry := range filtered {
			item := entry
			lowerID := strings.ToLower(item.ID)

			var (
				isInstalled bool
				userManaged bool
				hasGlobal   bool
				instUUID    = item.ID
			)
			switch item.Kind {
			case plugin.ProviderWasm:
				if st, ok := wasmByID[lowerID]; ok {
					isInstalled = true
					instUUID = st.UUID
					hasGlobal = st.HasGlobalSettings
					if wasmCtrl != nil {
						userManaged = wasmCtrl.IsUserManagedPlugin(st.Path)
					}
				}
			case plugin.ProviderStreamDeck:
				if st, ok := sdByID[lowerID]; ok {
					isInstalled = true
					instUUID = st.UUID
					if sdCtrl != nil {
						userManaged = sdCtrl.IsUserManagedPlugin(st.Dir)
					}
				}
			}
			upd, hasUpdate := updatesByID[lowerID]

			badges := []string{"[" + string(item.Kind) + "]"}
			if item.Featured {
				badges = append(badges, "[Featured]")
			}
			switch {
			case isInstalled && !userManaged:
				badges = append(badges, "[Installed Externally]")
			case isInstalled && hasUpdate:
				badges = append(badges, fmt.Sprintf("[Update: v%s -> v%s]", upd.CurrentVersion, upd.LatestVersion))
			case isInstalled:
				badges = append(badges, "[Installed]")
			}

			titleLbl := widget.NewLabel(item.Name + " " + strings.Join(badges, " "))
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
			if item.Kind == plugin.ProviderWasm && len(item.AllowedHosts) > 0 {
				metaText += "\nAllowed HTTP hosts: " + strings.Join(item.AllowedHosts, ", ")
			}
			metaLbl := widget.NewLabel(metaText)
			metaLbl.Wrapping = fyne.TextWrapWord

			iconBox := buildAsyncCatalogIconSlot(
				ctx,
				item.IconURL,
				func() ([]byte, error) {
					raw, _, err := mgr.CuratedClient.FetchIcon(ctx, item)
					return raw, err
				},
				&iconMu,
				icons,
				iconSem,
				&closed,
				&gen,
				curGen,
			)

			var actionBtns []fyne.CanvasObject
			actionBtns = append(actionBtns, widget.NewButtonWithIcon("More Info", theme.InfoIcon(), func() {
				openMoreInfo(item)
			}))

			if isInstalled && hasGlobal && onConfigurePlugin != nil {
				targetID := instUUID
				actionBtns = append(actionBtns, widget.NewButtonWithIcon("Configure", theme.SettingsIcon(), func() {
					if dlg != nil {
						dlg.Hide()
					}
					onConfigurePlugin(targetID)
				}))
			}

			installLabel := "Install"
			if isInstalled && userManaged {
				if hasUpdate {
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
					err := mgr.InstallCurated(ctx, item, func(_, _ int64, stage string) {
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
							msg := fmt.Sprintf("Install %s failed: %v", item.Name, err)
							statusLbl.SetText(msg)
							if onStatus != nil {
								onStatus(msg)
							}
							return
						}
						msg := fmt.Sprintf("Installed %s (%s)", item.Name, item.ID)
						statusLbl.SetText(msg)
						if reloadAll != nil {
							reloadAll(msg)
						}
						renderList()
					})
				}()
			})
			if !isInstalled || hasUpdate {
				installBtn.Importance = widget.HighImportance
			}
			actionBtns = append(actionBtns, installBtn)

			if isInstalled && userManaged {
				actionBtns = append(actionBtns, widget.NewButton("Uninstall", func() {
					if !busy.CompareAndSwap(false, true) {
						return
					}
					var err error
					switch item.Kind {
					case plugin.ProviderWasm:
						if wasmCtrl != nil {
							err = wasmCtrl.UninstallPlugin(instUUID)
						}
					case plugin.ProviderStreamDeck:
						if sdCtrl != nil {
							err = sdCtrl.UninstallPlugin(instUUID)
						}
					}
					busy.Store(false)
					if err != nil {
						statusLbl.SetText(fmt.Sprintf("Uninstall %s failed: %v", item.Name, err))
						return
					}
					mgr.NotifyPluginRemoved(instUUID)
					msg := "Uninstalled " + item.Name
					statusLbl.SetText(msg)
					if reloadAll != nil {
						reloadAll(msg)
					}
					renderList()
				}))
			}

			header := container.NewBorder(nil, nil, nil, container.NewHBox(actionBtns...), titleLbl)
			bodyCol := container.NewVBox(header, metaLbl)
			row := container.NewBorder(nil, nil, iconBox, nil, bodyCol)
			listBox.Add(container.NewVBox(row, widget.NewSeparator()))
		}
		listBox.Refresh()
	}

	loadCatalog := func(force bool) {
		statusLbl.SetText("Fetching Curated Plugins catalog...")
		go func() {
			fetched, fromFallback, err := mgr.CuratedClient.Fetch(ctx, force)
			dispatchUI(func() {
				if closed.Load() {
					return
				}
				if err != nil {
					statusLbl.SetText(fmt.Sprintf("Failed to load Curated Plugins catalog: %v", err))
					return
				}
				entries = fetched
				if fromFallback {
					statusLbl.SetText(fmt.Sprintf("Loaded %d curated plugin(s) (built-in offline fallback)", len(entries)))
				} else {
					statusLbl.SetText(fmt.Sprintf("Loaded %d curated plugin(s)", len(entries)))
				}
				renderList()
			})
		}()
	}

	unsub := mgr.SubscribeUpdates(func(_ pluginmgr.UpdateSummary) {
		dispatchUI(func() {
			if !closed.Load() {
				renderList()
			}
		})
	})

	searchEnt.OnChanged = func(_ string) {
		renderList()
	}
	refreshBtn := widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), func() {
		loadCatalog(true)
	})

	topBar := container.NewVBox(
		container.NewBorder(nil, nil, nil, refreshBtn, searchEnt),
		statusLbl,
		widget.NewSeparator(),
	)
	body := container.NewBorder(topBar, nil, nil, nil, container.NewScroll(container.NewPadded(listBox)))

	dlg = dialog.NewCustom("Curated Plugins", "Close", body, win)
	resizePluginCatalogDialog(dlg, win)
	dlg.SetOnClosed(func() {
		closed.Store(true)
		unsub()
		cancel()
	})
	dlg.Show()
	loadCatalog(false)
	return dlg
}
