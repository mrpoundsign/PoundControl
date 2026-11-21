package ui

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/mrpoundsign/PoundDeck/internal/app"
	"github.com/mrpoundsign/PoundDeck/internal/version"
)

func showSettingsDialog(
	win fyne.Window,
	engine *app.Engine,
	obsCtrl OBSController,
	cfgPath string,
	activeHotkeys int,
	onOpenPopOut func(),
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
	pluginRows := container.NewVBox()
	for _, p := range engine.Registry().Providers() {
		cardTitle := widget.NewLabel(fmt.Sprintf("%s (%s) — %d actions", p.Name(), p.Kind(), len(p.Actions())))
		cardTitle.TextStyle = fyne.TextStyle{Bold: true}
		pluginRows.Add(cardTitle)
	}
	pluginRows.Add(widget.NewSeparator())
	upcomingLbl := widget.NewLabel(
		"External plugin hosts coming in upcoming milestones:\n" +
			"  • Milestone 4: Elgato Stream Deck SDK v2/v3 (.sdPlugin) + Property Inspector bridge\n" +
			"  • Milestone 5: Pure-Go wazero WebAssembly (.wasm) dynamic plugins",
	)
	upcomingLbl.Wrapping = fyne.TextWrapWord
	pluginRows.Add(upcomingLbl)
	pluginsTab := container.NewVScroll(container.NewPadded(pluginRows))

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

	dlg := dialog.NewCustom("PoundDeck Settings", "Close", tabs, win)
	dlg.Resize(fyne.NewSize(620, 460))
	dlg.SetOnClosed(obsCleanup)
	dlg.Show()
	return dlg
}
