package ui

import (
	"fmt"
	"image"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/mrpoundsign/PoundDeck/internal/webdeck"
)

const (
	bindLabelLAN       = "LAN (All Interfaces)"
	bindLabelLocalhost = "Localhost Only"
)

// WebDeckController defines the LAN Web Touch Deck server operations exposed to Studio UI surfaces.
type WebDeckController interface {
	Config() webdeck.Config
	SaveConfig(cfg webdeck.Config) error
	Start() error
	Stop() error
	RegeneratePairing(revokeSessions bool) (webdeck.Config, error)
	RevokeAllSessions() error
	Status() webdeck.Status
	SubscribeStatus(fn func(webdeck.Status)) func()
	QRImage(size int) (image.Image, error)
}

var _ WebDeckController = (*webdeck.Server)(nil)

func webDeckBadgeLabel(st webdeck.Status) string {
	if !st.Running {
		return "Web Deck: Off"
	}
	portStr := strconv.Itoa(st.Port)
	if st.ConnectedClients > 0 {
		return fmt.Sprintf("Web Deck: :%s (%d)", portStr, st.ConnectedClients)
	}
	return "Web Deck: :" + portStr
}

func buildWebDeckSettingsTab(ctrl WebDeckController, onStatus func(string)) (fyne.CanvasObject, func()) {
	if ctrl == nil {
		msg := widget.NewLabel("LAN Web Touch Deck controller is not attached in this mode.")
		msg.Wrapping = fyne.TextWrapWord
		return container.NewPadded(msg), func() {}
	}

	cfg := ctrl.Config()
	initSt := ctrl.Status()

	statusLbl := widget.NewLabel("")
	statusLbl.Wrapping = fyne.TextWrapWord

	syncing := false
	enabledCheck := widget.NewCheck("Enable LAN Web Touch Deck (auto-starts with PoundDeck)", nil)
	enabledCheck.SetChecked(cfg.Enabled)

	portEntry := widget.NewEntry()
	portEntry.SetText(strconv.Itoa(cfg.Port))
	portEntry.SetPlaceHolder(strconv.Itoa(webdeck.DefaultPort))

	bindSelect := widget.NewSelect([]string{bindLabelLAN, bindLabelLocalhost}, nil)
	if cfg.BindMode == webdeck.BindLocalhost {
		bindSelect.SetSelected(bindLabelLocalhost)
	} else {
		bindSelect.SetSelected(bindLabelLAN)
	}

	ifaceOptions := make([]string, 0, len(initSt.Addresses)+1)
	ifaceIPByOption := make(map[string]string, len(initSt.Addresses)+1)
	ifaceOptions = append(ifaceOptions, "Auto (Primary LAN)")
	ifaceIPByOption["Auto (Primary LAN)"] = ""
	selectedIfaceOpt := "Auto (Primary LAN)"
	for _, a := range initSt.Addresses {
		opt := fmt.Sprintf("%s — %s", a.IP, a.Name)
		ifaceOptions = append(ifaceOptions, opt)
		ifaceIPByOption[opt] = a.IP
		if cfg.PreferredHost != "" && cfg.PreferredHost == a.IP {
			selectedIfaceOpt = opt
		}
	}
	ifaceSelect := widget.NewSelect(ifaceOptions, nil)
	ifaceSelect.SetSelected(selectedIfaceOpt)

	parseAndSave := func(wantEnabled bool) {
		portNum, err := strconv.Atoi(strings.TrimSpace(portEntry.Text))
		if err != nil || portNum < 0 || portNum > 65535 {
			if onStatus != nil {
				onStatus("Web Deck port must be between 0 and 65535")
			}
			return
		}
		if portNum == 0 && cfg.Port != 0 {
			portNum = webdeck.DefaultPort
		}

		mode := webdeck.BindLAN
		if bindSelect.Selected == bindLabelLocalhost {
			mode = webdeck.BindLocalhost
		}

		next := ctrl.Config()
		next.Enabled = wantEnabled
		next.Port = portNum
		next.BindMode = mode
		next.PreferredHost = ifaceIPByOption[ifaceSelect.Selected]
		if err := ctrl.SaveConfig(next); err != nil {
			if onStatus != nil {
				onStatus("Web Deck apply failed: " + err.Error())
			}
			return
		}
		if onStatus != nil {
			if wantEnabled {
				onStatus("Web Deck settings saved and server running")
			} else {
				onStatus("Web Deck stopped")
			}
		}
	}

	enabledCheck.OnChanged = func(checked bool) {
		if syncing {
			return
		}
		parseAndSave(checked)
	}

	applyBtn := widget.NewButtonWithIcon("Save & Apply", theme.ConfirmIcon(), func() {
		parseAndSave(enabledCheck.Checked)
	})
	applyBtn.Importance = widget.HighImportance

	pinLbl := widget.NewLabel("")
	pinLbl.TextStyle = fyne.TextStyle{Bold: true}

	urlLbl := widget.NewLabel("")
	urlLbl.Wrapping = fyne.TextWrapBreak

	var curAccessURL string
	var curPairURL string
	copyURLBtn := widget.NewButtonWithIcon("Copy URL", theme.ContentCopyIcon(), func() {
		if curApp := fyne.CurrentApp(); curApp != nil && curApp.Clipboard() != nil && curAccessURL != "" {
			curApp.Clipboard().SetContent(curAccessURL)
			if onStatus != nil {
				onStatus("Copied Web Deck URL: " + curAccessURL)
			}
		}
	})
	copyPairBtn := widget.NewButtonWithIcon("Copy Pair Link", theme.ContentCopyIcon(), func() {
		if curApp := fyne.CurrentApp(); curApp != nil && curApp.Clipboard() != nil && curPairURL != "" {
			curApp.Clipboard().SetContent(curPairURL)
			if onStatus != nil {
				onStatus("Copied one-tap QR pair URL to clipboard")
			}
		}
	})

	qrImg := canvas.NewImageFromImage(nil)
	qrImg.ScaleMode = canvas.ImageScalePixels
	qrImg.FillMode = canvas.ImageFillContain
	qrImg.SetMinSize(fyne.NewSize(180, 180))

	regenBtn := widget.NewButtonWithIcon("Regenerate PIN & QR", theme.ViewRefreshIcon(), func() {
		if _, err := ctrl.RegeneratePairing(false); err != nil {
			if onStatus != nil {
				onStatus("Regenerate pairing failed: " + err.Error())
			}
			return
		}
		if onStatus != nil {
			onStatus("Generated new 6-digit PIN and pairing QR code")
		}
	})

	revokeBtn := widget.NewButtonWithIcon("Revoke All Devices", theme.LogoutIcon(), func() {
		if err := ctrl.RevokeAllSessions(); err != nil {
			if onStatus != nil {
				onStatus("Revoke sessions failed: " + err.Error())
			}
			return
		}
		if onStatus != nil {
			onStatus("Revoked all paired Web Deck devices")
		}
	})

	var lastPairURL string
	applyStatus := func(st webdeck.Status) {
		syncing = true
		if wantChecked := ctrl.Config().Enabled; enabledCheck.Checked != wantChecked {
			enabledCheck.SetChecked(wantChecked)
		}
		syncing = false

		curAccessURL = st.AccessURL
		curPairURL = st.PairURL

		switch {
		case st.Running:
			statusLbl.SetText(fmt.Sprintf("Running on %s (%d connected / %d paired devices)",
				st.AccessURL, st.ConnectedClients, st.PairedDevices))
		case st.LastError != "":
			statusLbl.SetText("Stopped — Error: " + st.LastError)
		default:
			statusLbl.SetText(fmt.Sprintf("Stopped (%d paired devices)", st.PairedDevices))
		}

		pinLbl.SetText("Pairing PIN: " + st.PIN)
		urlLbl.SetText(st.AccessURL)

		if st.PairURL != lastPairURL && st.PairURL != "" {
			if img, err := ctrl.QRImage(180); err == nil {
				qrImg.Image = img
				qrImg.Refresh()
				lastPairURL = st.PairURL
			}
		}
	}

	applyStatus(initSt)
	unsub := ctrl.SubscribeStatus(func(st webdeck.Status) {
		dispatchUI(func() {
			applyStatus(st)
		})
	})

	content := container.NewVBox(
		widget.NewLabelWithStyle("LAN Touch Deck Server", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		statusLbl,
		enabledCheck,
		widget.NewForm(
			widget.NewFormItem("Port", portEntry),
			widget.NewFormItem("Bind Mode", bindSelect),
			widget.NewFormItem("LAN Interface", ifaceSelect),
		),
		container.NewHBox(applyBtn),
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Mobile & Tablet Pairing", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		pinLbl,
		widget.NewForm(widget.NewFormItem("Deck URL", urlLbl)),
		container.NewHBox(copyURLBtn, copyPairBtn),
		container.NewCenter(qrImg),
		container.NewHBox(regenBtn, revokeBtn),
	)

	return container.NewVScroll(container.NewPadded(content)), unsub
}
