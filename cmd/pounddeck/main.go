package main

import (
	"log"

	fyneapp "fyne.io/fyne/v2/app"

	"github.com/mrpoundsign/PoundDeck/assets"
	"github.com/mrpoundsign/PoundDeck/internal/app"
	"github.com/mrpoundsign/PoundDeck/internal/ui"
	"github.com/mrpoundsign/PoundDeck/internal/version"
	"github.com/mrpoundsign/PoundDeck/pkg/builtin/core"
	"github.com/mrpoundsign/PoundDeck/pkg/builtin/obs"
	"github.com/mrpoundsign/PoundDeck/pkg/deck"
	"github.com/mrpoundsign/PoundDeck/pkg/plugin"
	"github.com/mrpoundsign/PoundDeck/pkg/secrets"
)

func main() {
	cfgPath, err := deck.DefaultConfigPath()
	if err != nil {
		log.Fatalf("resolve config path: %v", err)
	}
	store := deck.NewFileStore(cfgPath)
	secretStore := secrets.NewKeyringStore(secrets.ServiceName)

	reg := plugin.NewRegistry()
	if err := reg.RegisterProvider(core.New()); err != nil {
		log.Fatalf("register built-in core plugin: %v", err)
	}
	obsProvider := obs.New(secretStore)
	if err := reg.RegisterProvider(obsProvider); err != nil {
		log.Fatalf("register built-in obs plugin: %v", err)
	}

	eng, err := app.NewEngine(store, reg)
	if err != nil {
		log.Fatalf("initialize deck engine: %v", err)
	}
	defer func() {
		if closeErr := eng.Close(); closeErr != nil {
			log.Printf("close engine: %v", closeErr)
		}
	}()

	myApp := fyneapp.NewWithID(version.AppID)
	myApp.SetIcon(assets.AppIcon)
	myApp.Settings().SetTheme(&ui.StudioTheme{})

	win := ui.NewMainWindow(myApp, eng)
	win.SetOBSController(obsProvider)
	obsProvider.StartFromGlobalSettings()

	win.ShowAndRun()
}
