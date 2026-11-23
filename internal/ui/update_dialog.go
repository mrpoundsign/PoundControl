package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
	"github.com/mrpoundsign/appinstaller/pkg/updater"
	"github.com/mrpoundsign/appinstaller/pkg/updaterui"

	"github.com/mrpoundsign/PoundControl/internal/version"
)

const (
	updateRepoOwner = "mrpoundsign"
	updateRepoName  = "PoundControl"

	prefCheckUpdatesOnStartup = "CheckUpdatesOnStartup"
	prefSkippedUpdateVersion  = "SkippedUpdateVersion"
)

var newUpdateChecker = func() *updater.Checker {
	return updater.NewChecker(updateRepoOwner, updateRepoName)
}

func shouldCheckStartupUpdate(currentVersion string) bool {
	v := strings.ToLower(strings.TrimSpace(currentVersion))
	if v == "" || v == "dev" || strings.Contains(v, "-dev") || strings.Contains(v, "snapshot") {
		return false
	}
	return true
}

func showSelfUpdateDialog(parent fyne.Window, currentVersion string, rel *updater.ReleaseInfo, checker *updater.Checker) {
	updaterui.ShowUpdateDialog(updaterui.DialogConfig{
		Parent:         parent,
		AppName:        version.AppName,
		CurrentVersion: currentVersion,
		Release:        rel,
		Checker:        checker,
		OnSkipVersion: func(ver string) {
			if a := fyne.CurrentApp(); a != nil {
				a.Preferences().SetString(prefSkippedUpdateVersion, ver)
			}
		},
		OnQuitApp: func() {
			if a := fyne.CurrentApp(); a != nil {
				a.Quit()
			}
		},
	})
}

// CheckStartupUpdate checks GitHub Releases in the background for non-dev builds
// and displays the SHA-256 verified self-update dialog when a newer release exists.
func CheckStartupUpdate(parent fyne.Window, currentVersion string) {
	if parent == nil || !shouldCheckStartupUpdate(currentVersion) {
		return
	}
	a := fyne.CurrentApp()
	if a == nil {
		return
	}
	prefs := a.Preferences()
	if !prefs.BoolWithFallback(prefCheckUpdatesOnStartup, true) {
		return
	}
	skippedVersion := prefs.String(prefSkippedUpdateVersion)
	checker := newUpdateChecker()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		rel, isNewer, err := checker.CheckLatest(ctx, currentVersion)
		if err != nil || !isNewer || rel == nil {
			return
		}
		if skippedVersion != "" && updater.NormalizeVersion(rel.TagName) == updater.NormalizeVersion(skippedVersion) {
			return
		}

		dispatchUI(func() {
			showSelfUpdateDialog(parent, currentVersion, rel, checker)
		})
	}()
}

func checkForUpdatesManual(
	parent fyne.Window,
	currentVersion string,
	statusLbl *widget.Label,
	checkBtn *widget.Button,
	onStatus func(string),
) {
	if checkBtn != nil {
		checkBtn.Disable()
	}
	if statusLbl != nil {
		statusLbl.SetText("Checking GitHub Releases...")
	}
	checker := newUpdateChecker()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		rel, isNewer, err := checker.CheckLatest(ctx, currentVersion)
		dispatchUI(func() {
			if checkBtn != nil {
				checkBtn.Enable()
			}
			if err != nil {
				msg := "Update check failed: " + err.Error()
				if statusLbl != nil {
					statusLbl.SetText(msg)
				}
				if onStatus != nil {
					onStatus(msg)
				}
				return
			}
			if isNewer && rel != nil {
				ver := updater.NormalizeVersion(rel.TagName)
				msg := "Update available: v" + ver
				if statusLbl != nil {
					statusLbl.SetText(msg)
				}
				if onStatus != nil {
					onStatus(msg)
				}
				showSelfUpdateDialog(parent, currentVersion, rel, checker)
				return
			}

			msg := fmt.Sprintf("%s is up to date (v%s).", version.AppName, updater.NormalizeVersion(currentVersion))
			if statusLbl != nil {
				statusLbl.SetText(msg)
			}
			if onStatus != nil {
				onStatus(msg)
			}
		})
	}()
}
