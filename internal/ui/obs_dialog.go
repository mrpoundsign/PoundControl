package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/mrpoundsign/PoundDeck/pkg/builtin/obs"
)

// OBSController defines the OBS connection and status operations exposed to the Studio UI.
type OBSController interface {
	ConnectionConfig() obs.ConnectionConfig
	HasPassword() bool
	SaveConnectionConfig(cfg obs.ConnectionConfig, password string, updatePassword bool) error
	Connect(ctx context.Context) error
	ConnectAndMaintain()
	Disconnect()
	Status() obs.ClientStatus
	SubscribeStatus(fn func(obs.ClientStatus)) func()
}

func obsBadgeLabel(st obs.ClientStatus) string {
	switch st.State {
	case obs.StateConnected:
		if st.OBSVersion != "" {
			return "OBS: Live (" + st.OBSVersion + ")"
		}
		return "OBS: Connected"
	case obs.StateConnecting:
		return "OBS: Connecting…"
	case obs.StateError:
		return "OBS: Error"
	default:
		return "OBS: Off"
	}
}

func obsStatusDescription(st obs.ClientStatus) string {
	switch st.State {
	case obs.StateConnected:
		if st.OBSVersion != "" {
			return fmt.Sprintf("Status: Connected to %s (OBS %s, WS %s)", st.Address, st.OBSVersion, st.WebSocketVersion)
		}
		return "Status: Connected to " + st.Address
	case obs.StateConnecting:
		return "Status: Connecting to " + st.Address + "…"
	case obs.StateError:
		if st.LastError != "" {
			return "Status: Error — " + st.LastError
		}
		return "Status: Connection error"
	default:
		return "Status: Disconnected"
	}
}

func showOBSConnectionDialog(win fyne.Window, obsCtrl OBSController, onStatus func(string)) {
	if obsCtrl == nil {
		return
	}

	cfg := obsCtrl.ConnectionConfig()
	hostEntry := widget.NewEntry()
	hostEntry.SetText(cfg.Host)
	hostEntry.SetPlaceHolder(obs.DefaultHost)

	portEntry := widget.NewEntry()
	portEntry.SetText(strconv.Itoa(cfg.Port))
	portEntry.SetPlaceHolder(strconv.Itoa(obs.DefaultPort))

	pwEntry := widget.NewPasswordEntry()
	if obsCtrl.HasPassword() {
		pwEntry.SetPlaceHolder("•••••••• (saved in OS keyring — enter to replace)")
	} else {
		pwEntry.SetPlaceHolder("Optional OBS WebSocket password")
	}

	clearPwCheck := widget.NewCheck("Clear saved password", nil)
	if !obsCtrl.HasPassword() {
		clearPwCheck.Disable()
	}

	autoCheck := widget.NewCheck("Connect automatically on startup", nil)
	autoCheck.SetChecked(cfg.AutoConnect)

	statusLbl := widget.NewLabel(obsStatusDescription(obsCtrl.Status()))
	statusLbl.Wrapping = fyne.TextWrapWord

	unsub := obsCtrl.SubscribeStatus(func(st obs.ClientStatus) {
		dispatchUI(func() {
			statusLbl.SetText(obsStatusDescription(st))
		})
	})

	connectBtn := widget.NewButton("Save & Connect", func() {
		hostStr := strings.TrimSpace(hostEntry.Text)
		if hostStr == "" {
			hostStr = obs.DefaultHost
		}
		port, err := strconv.Atoi(strings.TrimSpace(portEntry.Text))
		if err != nil || port <= 0 || port > 65535 {
			statusLbl.SetText("Status: Port must be a number between 1 and 65535")
			return
		}

		updatePw := pwEntry.Text != "" || clearPwCheck.Checked
		newPw := pwEntry.Text
		if clearPwCheck.Checked {
			newPw = ""
		}

		nextCfg := obs.ConnectionConfig{
			Host:        hostStr,
			Port:        port,
			AutoConnect: autoCheck.Checked,
		}
		if err := obsCtrl.SaveConnectionConfig(nextCfg, newPw, updatePw); err != nil {
			statusLbl.SetText("Status: Failed saving config — " + err.Error())
			return
		}
		if updatePw {
			pwEntry.SetText("")
			clearPwCheck.SetChecked(false)
			if obsCtrl.HasPassword() {
				pwEntry.SetPlaceHolder("•••••••• (saved in OS keyring — enter to replace)")
				clearPwCheck.Enable()
			} else {
				pwEntry.SetPlaceHolder("Optional OBS WebSocket password")
				clearPwCheck.Disable()
			}
		}

		statusLbl.SetText("Status: Connecting…")
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			connErr := obsCtrl.Connect(ctx)
			dispatchUI(func() {
				if connErr != nil {
					statusLbl.SetText("Status: Connection failed — " + connErr.Error())
					if onStatus != nil {
						onStatus("OBS connection failed: " + connErr.Error())
					}
					return
				}
				statusLbl.SetText(obsStatusDescription(obsCtrl.Status()))
				if onStatus != nil {
					onStatus("Connected to OBS Studio")
				}
			})
		}()
	})
	connectBtn.Importance = widget.HighImportance

	disconnectBtn := widget.NewButton("Disconnect", func() {
		obsCtrl.Disconnect()
		statusLbl.SetText("Status: Disconnected")
		if onStatus != nil {
			onStatus("Disconnected from OBS Studio")
		}
	})

	content := container.NewVBox(
		widget.NewForm(
			widget.NewFormItem("Host", hostEntry),
			widget.NewFormItem("Port", portEntry),
			widget.NewFormItem("Password", pwEntry),
		),
		clearPwCheck,
		autoCheck,
		widget.NewSeparator(),
		statusLbl,
		container.NewGridWithColumns(2, connectBtn, disconnectBtn),
	)

	dlg := dialog.NewCustom("OBS Studio (WebSocket v5)", "Close", content, win)
	dlg.Resize(fyne.NewSize(430, 340))
	dlg.SetOnClosed(unsub)
	dlg.Show()
}
