package ui

import (
	"context"
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/mrpoundsign/PoundControl/internal/app"
	"github.com/mrpoundsign/PoundControl/pkg/deck"
)

// PopOutPad presents a distraction-free, always-live Virtual Deck window with an independent viewport.
type PopOutPad struct {
	win        fyne.Window
	engine     *app.Engine
	viewportID string
	unregVP    func()
	surface    *deckSurfaceView
	unsub      func()
	lastSnap   app.Snapshot

	headerLbl     *widget.Label
	profileSelect *widget.Select
	profileIDs    []string
	syncingSelect bool
	prevBtn       *widget.Button
	nextBtn       *widget.Button
	backBtn       *widget.Button
}

// NewPopOutPad constructs a floating Run Mode deck pad attached to engine on its own independent viewport.
func NewPopOutPad(a fyne.App, engine *app.Engine, onClosed func()) *PopOutPad {
	win := a.NewWindow("PoundControl Pad")
	win.Resize(fyne.NewSize(560, 460))

	initProfileID := engine.Snapshot().ActiveProfileID
	vpID, unregVP := engine.RegisterViewport(app.Viewport{
		ProfileID:  initProfileID,
		FollowHost: false,
	})

	pad := &PopOutPad{
		win:        win,
		engine:     engine,
		viewportID: vpID,
		unregVP:    unregVP,
	}

	pad.headerLbl = widget.NewLabel("Default • Page 1 / 1")
	pad.headerLbl.TextStyle = fyne.TextStyle{Bold: true}
	pad.profileSelect = widget.NewSelect([]string{followStudioOption}, func(_ string) {
		if pad.syncingSelect {
			return
		}
		idx := pad.profileSelect.SelectedIndex()
		if idx < 0 || idx >= len(pad.profileIDs) {
			return
		}
		targetID := pad.profileIDs[idx]
		if targetID == "" {
			_ = engine.SetViewportFollowHost(pad.viewportID, true)
		} else {
			_ = engine.ViewportSwitchProfile(pad.viewportID, targetID)
		}
	})
	pad.prevBtn = widget.NewButtonWithIcon("", theme.NavigateBackIcon(), func() {
		_ = engine.ViewportPrevPage(pad.viewportID, true)
	})
	pad.nextBtn = widget.NewButtonWithIcon("", theme.NavigateNextIcon(), func() {
		_ = engine.ViewportNextPage(pad.viewportID, true)
	})
	pad.backBtn = widget.NewButton("← Back", func() {
		_ = engine.ViewportCloseFolder(pad.viewportID)
	})

	pad.surface = newDeckSurfaceView(
		func(c deck.Coordinates) {
			_ = engine.ViewportPressKey(context.Background(), pad.viewportID, c)
		},
		func(dial int) {
			_ = engine.ViewportPressDial(context.Background(), pad.viewportID, dial)
		},
		func(dial, delta int) {
			_ = engine.ViewportRotateDial(context.Background(), pad.viewportID, dial, delta)
		},
		func(dial int) {
			_ = engine.ViewportPressDial(context.Background(), pad.viewportID, dial)
		},
	)

	topBar := container.NewStack(
		canvas.NewRectangle(ColorHeaderSurface),
		container.NewPadded(container.NewHBox(
			pad.headerLbl,
			layout.NewSpacer(),
			pad.profileSelect,
			pad.backBtn,
			pad.prevBtn,
			pad.nextBtn,
		)),
	)

	body := container.NewStack(
		canvas.NewRectangle(ColorObsidianSlate),
		container.NewScroll(container.NewCenter(container.NewPadded(pad.surface.Object()))),
	)

	win.SetContent(container.NewBorder(topBar, nil, nil, nil, body))

	pad.ApplySnapshot(engine.ViewportSnapshot(pad.viewportID))
	pad.unsub = engine.SubscribeViewport(pad.viewportID, func(s app.Snapshot) {
		dispatchUI(func() {
			pad.ApplySnapshot(s)
		})
	})

	win.SetOnClosed(func() {
		if pad.surface != nil {
			pad.surface.Close()
		}
		if pad.unsub != nil {
			pad.unsub()
			pad.unsub = nil
		}
		if pad.unregVP != nil {
			pad.unregVP()
			pad.unregVP = nil
		}
		if onClosed != nil {
			onClosed()
		}
	})

	return pad
}

// Window returns the underlying Fyne window.
func (p *PopOutPad) Window() fyne.Window {
	return p.win
}

// Show presents and focuses the pop-out pad window.
func (p *PopOutPad) Show() {
	p.win.Show()
	p.win.RequestFocus()
}

// Close closes the pop-out pad window and unsubscribes from engine updates.
func (p *PopOutPad) Close() {
	p.win.Close()
}

// ApplySnapshot updates the pop-out pad tiles, dials, profile selector, and breadcrumb header to reflect snap.
func (p *PopOutPad) ApplySnapshot(snap app.Snapshot) {
	p.lastSnap = snap
	if p.profileSelect != nil {
		p.syncingSelect = true
		names := make([]string, 0, len(snap.Profiles)+1)
		ids := make([]string, 0, len(snap.Profiles)+1)
		names = append(names, followStudioOption)
		ids = append(ids, "")
		selectedIdx := 0
		for i, prof := range snap.Profiles {
			names = append(names, prof.Name)
			ids = append(ids, prof.ID)
			if !snap.FollowHost && prof.ID == snap.ActiveProfileID {
				selectedIdx = i + 1
			}
		}
		p.profileIDs = ids
		p.profileSelect.SetOptions(names)
		p.profileSelect.SetSelectedIndex(selectedIdx)
		p.syncingSelect = false
	}
	if snap.InFolder {
		p.headerLbl.SetText(fmt.Sprintf("%s • Folder: %s", snap.ActiveProfileName, strings.Join(snap.FolderPath, " / ")))
		p.backBtn.Enable()
		p.backBtn.Show()
	} else {
		p.headerLbl.SetText(fmt.Sprintf("%s • Page %d / %d", snap.ActiveProfileName, snap.PageIndex+1, max(1, snap.PageCount)))
		p.backBtn.Hide()
	}
	p.surface.ApplySnapshot(snap, false, deck.ControllerKeypad, deck.Coordinates{}, 0)
}
