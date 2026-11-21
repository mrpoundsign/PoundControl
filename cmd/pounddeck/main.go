package main

import (
	"fyne.io/fyne/v2/app"

	"github.com/mrpoundsign/PoundDeck/assets"
	"github.com/mrpoundsign/PoundDeck/internal/ui"
	"github.com/mrpoundsign/PoundDeck/internal/version"
)

func main() {
	myApp := app.NewWithID(version.AppID)
	myApp.SetIcon(assets.AppIcon)
	myApp.Settings().SetTheme(&ui.StudioTheme{})

	win := ui.NewMainWindow(myApp)
	win.ShowAndRun()
}
