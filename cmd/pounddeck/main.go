package main

import (
	"context"
	"log"

	fyneapp "fyne.io/fyne/v2/app"

	"github.com/mrpoundsign/PoundDeck/assets"
	"github.com/mrpoundsign/PoundDeck/internal/app"
	"github.com/mrpoundsign/PoundDeck/internal/hotkey"
	"github.com/mrpoundsign/PoundDeck/internal/ui"
	"github.com/mrpoundsign/PoundDeck/internal/version"
	"github.com/mrpoundsign/PoundDeck/pkg/builtin/apps"
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
	if err := reg.RegisterProvider(apps.New(nil)); err != nil {
		log.Fatalf("register built-in apps plugin: %v", err)
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

	hkMgr := hotkey.NewManager(func(c deck.Coordinates) {
		if pressErr := eng.PressKey(context.Background(), c); pressErr != nil {
			log.Printf("[Hotkey] slot (%d,%d) failed: %v", c.Row, c.Col, pressErr)
		}
	})
	defer hkMgr.Close()

	syncHotkeys := func(snap app.Snapshot) {
		bindings := collectHotkeyBindings(snap)
		hkMgr.SyncBindings(bindings)
	}
	syncHotkeys(eng.Snapshot())
	unsubHotkeys := eng.Subscribe(syncHotkeys)
	defer unsubHotkeys()

	myApp := fyneapp.NewWithID(version.AppID)
	myApp.SetIcon(assets.AppIcon)
	myApp.Settings().SetTheme(&ui.StudioTheme{})

	win := ui.NewMainWindow(myApp, eng)
	win.SetConfigPath(cfgPath)
	win.SetHotkeyCountProvider(hkMgr.ActiveCount)
	win.SetHotkeySuspender(hkMgr.SetSuspended)
	win.SetOBSController(obsProvider)
	obsProvider.StartFromGlobalSettings()

	win.ShowAndRun()
}

func collectHotkeyBindings(snap app.Snapshot) []hotkey.Binding {
	bindings := make([]hotkey.Binding, 0, len(snap.Slots))
	for row := range snap.Grid.Rows {
		for col := range snap.Grid.Cols {
			c := deck.Coordinates{Row: row, Col: col}
			r, ok := snap.Slots[c.SlotKey()]
			if !ok || r.Config == nil || r.Config.Hotkey == "" {
				continue
			}
			bindings = append(bindings, hotkey.Binding{
				Coords: c,
				Hotkey: r.Config.Hotkey,
			})
		}
	}
	return bindings
}
