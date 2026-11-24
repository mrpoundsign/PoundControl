package ui

import (
	"fmt"
	"image"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/mrpoundsign/PoundControl/internal/app"
	"github.com/mrpoundsign/PoundControl/internal/webdeck"
)

const (
	bindLabelLAN       = "LAN (All Interfaces)"
	bindLabelLocalhost = "Localhost Only"
	followStudioOption = "Follow Studio"
)

// WebDeckController defines the LAN Web Touch Deck server operations exposed to Studio UI surfaces.
type WebDeckController interface {
	Config() webdeck.Config
	SaveConfig(cfg webdeck.Config) error
	Start() error
	Stop() error
	OpenPairingWindow(ttl time.Duration) (webdeck.Status, error)
	ClosePairingWindow()
	ResolvePendingPair(attemptID string, approve bool) error
	RevokeDevice(deviceID string) error
	RevokeAllSessions() error
	SetDeviceViewport(deviceID, profileID string, followHost bool) error
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

func buildWebDeckSettingsTab(engine *app.Engine, ctrl WebDeckController, onStatus func(string)) (fyne.CanvasObject, func()) {
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
	enabledCheck := widget.NewCheck("Enable LAN Web Touch Deck (auto-starts with PoundControl)", nil)
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

	hostFpLbl := widget.NewLabel(initSt.HostFingerprint)
	hostFpLbl.TextStyle = fyne.TextStyle{Bold: true}

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

	pinLbl := widget.NewLabel("")
	pinLbl.TextStyle = fyne.TextStyle{Bold: true}

	expiryLbl := widget.NewLabel("")

	qrImg := canvas.NewImageFromImage(nil)
	qrImg.ScaleMode = canvas.ImageScalePixels
	qrImg.FillMode = canvas.ImageFillContain
	qrImg.SetMinSize(fyne.NewSize(180, 180))

	copyPairBtn := widget.NewButtonWithIcon("Copy Pair Link", theme.ContentCopyIcon(), func() {
		if curApp := fyne.CurrentApp(); curApp != nil && curApp.Clipboard() != nil && curPairURL != "" {
			curApp.Clipboard().SetContent(curPairURL)
			if onStatus != nil {
				onStatus("Copied one-tap QR pair URL to clipboard")
			}
		}
	})

	openPairBtn := widget.NewButtonWithIcon("Pair New Device", theme.VisibilityIcon(), func() {
		if _, err := ctrl.OpenPairingWindow(webdeck.DefaultPairingTTL); err != nil {
			if onStatus != nil {
				onStatus("Failed to open pairing window: " + err.Error())
			}
			return
		}
		if onStatus != nil {
			onStatus("Pairing window open for 2 minutes")
		}
	})
	openPairBtn.Importance = widget.HighImportance

	regenBtn := widget.NewButtonWithIcon("New PIN & QR", theme.ViewRefreshIcon(), func() {
		if _, err := ctrl.OpenPairingWindow(webdeck.DefaultPairingTTL); err != nil {
			if onStatus != nil {
				onStatus("Regenerate pairing failed: " + err.Error())
			}
			return
		}
		if onStatus != nil {
			onStatus("Generated new 2-minute 6-digit PIN and pairing QR code")
		}
	})

	closePairBtn := widget.NewButtonWithIcon("Close Pairing", theme.CancelIcon(), func() {
		ctrl.ClosePairingWindow()
		if onStatus != nil {
			onStatus("Closed Web Deck pairing window")
		}
	})

	activePairBox := container.NewVBox(
		pinLbl,
		expiryLbl,
		container.NewHBox(copyPairBtn, regenBtn, closePairBtn),
		container.NewCenter(qrImg),
	)
	closedPairLbl := widget.NewLabel("Pairing window is closed. Click Pair New Device to generate a temporary 6-digit PIN & QR code.")
	closedPairLbl.Wrapping = fyne.TextWrapWord

	devicesBox := container.NewVBox()
	revokeAllBtn := widget.NewButtonWithIcon("Revoke All Devices", theme.LogoutIcon(), func() {
		if err := ctrl.RevokeAllSessions(); err != nil {
			if onStatus != nil {
				onStatus("Revoke devices failed: " + err.Error())
			}
			return
		}
		if onStatus != nil {
			onStatus("Revoked all paired Web Deck devices")
		}
	})

	var (
		lastPairURL     string
		pairExpAt       time.Time
		pairingIsOpn    bool
		lastProfilesSig string
		closedFlag      atomic.Bool
	)

	profilesSigFor := func(snap app.Snapshot) string {
		var b strings.Builder
		for _, p := range snap.Profiles {
			b.WriteString(p.ID)
			b.WriteByte('=')
			b.WriteString(p.Name)
			b.WriteByte(';')
		}
		return b.String()
	}

	updateCountdown := func() {
		if !pairingIsOpn || pairExpAt.IsZero() {
			expiryLbl.SetText("")
			return
		}
		rem := time.Until(pairExpAt)
		if rem <= 0 {
			expiryLbl.SetText("Expires in 0:00")
			return
		}
		secs := int(rem.Round(time.Second).Seconds())
		expiryLbl.SetText(fmt.Sprintf("Expires in %d:%02d", secs/60, secs%60))
	}

	applyStatus := func(st webdeck.Status) {
		syncing = true
		if wantChecked := ctrl.Config().Enabled; enabledCheck.Checked != wantChecked {
			enabledCheck.SetChecked(wantChecked)
		}
		syncing = false

		curAccessURL = st.AccessURL
		curPairURL = st.PairURL
		pairingIsOpn = st.PairingOpen
		pairExpAt = st.PairingExpiresAt

		switch {
		case st.Running:
			statusLbl.SetText(fmt.Sprintf("Running on %s (%d connected / %d paired devices)",
				st.AccessURL, st.ConnectedClients, st.PairedDevices))
		case st.LastError != "":
			statusLbl.SetText("Stopped — Error: " + st.LastError)
		default:
			statusLbl.SetText(fmt.Sprintf("Stopped (%d paired devices)", st.PairedDevices))
		}

		hostFpLbl.SetText(st.HostFingerprint)
		urlLbl.SetText(st.AccessURL)

		if st.PairingOpen && st.PIN != "" {
			pinLbl.SetText("Pairing PIN: " + st.PIN)
			updateCountdown()
			activePairBox.Show()
			closedPairLbl.Hide()
			openPairBtn.Hide()
			if st.PairURL != lastPairURL && st.PairURL != "" {
				if img, err := ctrl.QRImage(180); err == nil {
					qrImg.Image = img
					qrImg.Refresh()
					lastPairURL = st.PairURL
				}
			}
		} else {
			lastPairURL = ""
			activePairBox.Hide()
			closedPairLbl.Show()
			openPairBtn.Show()
		}

		devicesBox.Objects = nil
		if len(st.Devices) == 0 {
			noneLbl := widget.NewLabel("No paired devices yet.")
			noneLbl.Wrapping = fyne.TextWrapWord
			devicesBox.Add(noneLbl)
			revokeAllBtn.Disable()
		} else {
			revokeAllBtn.Enable()
			var engSnap app.Snapshot
			if engine != nil {
				engSnap = engine.Snapshot()
				lastProfilesSig = profilesSigFor(engSnap)
			}
			for _, dev := range st.Devices {
				devID := dev.ID
				devName := dev.Name
				title := dev.Name
				if dev.Connected {
					title += " (Connected)"
				}
				titleLbl := widget.NewLabel(title)
				titleLbl.TextStyle = fyne.TextStyle{Bold: true}

				meta := "Key: " + dev.Fingerprint
				if dev.LastSeen != "" {
					meta += " • Last seen " + dev.LastSeen
				} else if dev.PairedAt != "" {
					meta += " • Paired " + dev.PairedAt
				}
				if engine == nil && dev.ProfileName != "" {
					meta += " • Profile: " + dev.ProfileName
				}
				metaLbl := widget.NewLabel(meta)
				metaLbl.Wrapping = fyne.TextWrapBreak

				var profCtrl fyne.CanvasObject
				if engine != nil {
					options := make([]string, 0, len(engSnap.Profiles)+1)
					optionIDs := make([]string, 0, len(engSnap.Profiles)+1)
					options = append(options, followStudioOption)
					optionIDs = append(optionIDs, "")
					selectedIdx := -1
					if dev.FollowHost {
						selectedIdx = 0
					}
					for i, p := range engSnap.Profiles {
						options = append(options, p.Name)
						optionIDs = append(optionIDs, p.ID)
						if !dev.FollowHost && dev.ProfileID != "" && p.ID == dev.ProfileID {
							selectedIdx = i + 1
						}
					}
					if selectedIdx < 0 {
						for i, p := range engSnap.Profiles {
							if p.ID == engSnap.ActiveProfileID {
								selectedIdx = i + 1
								break
							}
						}
					}
					localSync := true
					var sel *widget.Select
					sel = widget.NewSelect(options, func(_ string) {
						if localSync {
							return
						}
						idx := sel.SelectedIndex()
						if idx < 0 || idx >= len(optionIDs) {
							return
						}
						targetID := optionIDs[idx]
						isFollow := targetID == ""
						if err := ctrl.SetDeviceViewport(devID, targetID, isFollow); err != nil {
							if onStatus != nil {
								onStatus("Update device profile failed: " + err.Error())
							}
							return
						}
						if onStatus != nil {
							if isFollow {
								onStatus(devName + " set to Follow Studio")
							} else {
								onStatus(devName + " assigned to " + options[idx])
							}
						}
					})
					if selectedIdx >= 0 && selectedIdx < len(options) {
						sel.SetSelectedIndex(selectedIdx)
					}
					localSync = false
					profCtrl = sel
				}

				revBtn := widget.NewButtonWithIcon("Revoke", theme.DeleteIcon(), func() {
					if err := ctrl.RevokeDevice(devID); err != nil {
						if onStatus != nil {
							onStatus("Revoke device failed: " + err.Error())
						}
						return
					}
					if onStatus != nil {
						onStatus("Revoked device: " + devName)
					}
				})
				var rightControls fyne.CanvasObject = revBtn
				if profCtrl != nil {
					rightControls = container.NewHBox(profCtrl, revBtn)
				}
				row := container.NewBorder(nil, nil, nil, rightControls, container.NewVBox(titleLbl, metaLbl))
				devicesBox.Add(row)
			}
		}
		devicesBox.Refresh()
	}

	applyStatus(initSt)
	unsub := ctrl.SubscribeStatus(func(st webdeck.Status) {
		dispatchUI(func() {
			if closedFlag.Load() {
				return
			}
			applyStatus(st)
		})
	})
	var unsubEngine func()
	if engine != nil {
		lastProfilesSig = profilesSigFor(engine.Snapshot())
		unsubEngine = engine.Subscribe(func(snap app.Snapshot) {
			sig := profilesSigFor(snap)
			dispatchUI(func() {
				if closedFlag.Load() || sig == lastProfilesSig {
					return
				}
				lastProfilesSig = sig
				applyStatus(ctrl.Status())
			})
		})
	}

	stopTick := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopTick:
				return
			case <-ticker.C:
				dispatchUI(func() {
					if closedFlag.Load() {
						return
					}
					updateCountdown()
				})
			}
		}
	}()

	cleanup := func() {
		closedFlag.Store(true)
		close(stopTick)
		if unsub != nil {
			unsub()
		}
		if unsubEngine != nil {
			unsubEngine()
		}
	}

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
		widget.NewLabelWithStyle("Host Identity & Mutual Pairing", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewForm(
			widget.NewFormItem("Host Key", hostFpLbl),
			widget.NewFormItem("Deck URL", urlLbl),
		),
		container.NewHBox(copyURLBtn, openPairBtn),
		closedPairLbl,
		activePairBox,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Paired Devices", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		devicesBox,
		container.NewHBox(revokeAllBtn),
	)

	return container.NewScroll(container.NewPadded(content)), cleanup
}
