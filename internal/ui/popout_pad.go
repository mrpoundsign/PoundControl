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

// PopOutPad presents a distraction-free, always-live Virtual Deck window synchronized with the Engine.
type PopOutPad struct {
	win     fyne.Window
	engine  *app.Engine
	surface *deckSurfaceView
	unsub   func()

	headerLbl *widget.Label
	prevBtn   *widget.Button
	nextBtn   *widget.Button
	backBtn   *widget.Button
}

// NewPopOutPad constructs a floating Run Mode deck pad attached to engine.
func NewPopOutPad(a fyne.App, engine *app.Engine, onClosed func()) *PopOutPad {
	win := a.NewWindow("PoundControl Pad")
	win.Resize(fyne.NewSize(560, 460))

	pad := &PopOutPad{
		win:    win,
		engine: engine,
	}

	pad.headerLbl = widget.NewLabel("Default • Page 1 / 1")
	pad.headerLbl.TextStyle = fyne.TextStyle{Bold: true}
	pad.prevBtn = widget.NewButtonWithIcon("", theme.NavigateBackIcon(), func() {
		_ = engine.PrevPage(true)
	})
	pad.nextBtn = widget.NewButtonWithIcon("", theme.NavigateNextIcon(), func() {
		_ = engine.NextPage(true)
	})
	pad.backBtn = widget.NewButton("← Back", func() {
		_ = engine.CloseFolder()
	})

	pad.surface = newDeckSurfaceView(
		func(c deck.Coordinates) {
			_ = engine.PressKey(context.Background(), c)
		},
		func(dial int) {
			_ = engine.PressDial(context.Background(), dial)
		},
		func(dial, delta int) {
			_ = engine.RotateDial(context.Background(), dial, delta)
		},
		func(dial int) {
			_ = engine.PressDial(context.Background(), dial)
		},
	)

	topBar := container.NewStack(
		canvas.NewRectangle(ColorHeaderSurface),
		container.NewPadded(container.NewHBox(
			pad.headerLbl,
			layout.NewSpacer(),
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

	pad.ApplySnapshot(engine.Snapshot())
	pad.unsub = engine.Subscribe(func(s app.Snapshot) {
		dispatchUI(func() {
			pad.ApplySnapshot(s)
		})
	})

	win.SetOnClosed(func() {
		if pad.unsub != nil {
			pad.unsub()
			pad.unsub = nil
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

// ApplySnapshot updates the pop-out pad tiles, dials, and breadcrumb header to reflect snap.
func (p *PopOutPad) ApplySnapshot(snap app.Snapshot) {
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
