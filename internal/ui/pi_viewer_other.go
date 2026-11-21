//go:build !windows

package ui

import (
	"context"
	"os/exec"
	"runtime"
	"time"

	"github.com/mrpoundsign/PoundDeck/pkg/builtin/apps"
)

type browserPIViewer struct{}

func newPlatformPIViewer() PIViewer {
	return browserPIViewer{}
}

func (browserPIViewer) Open(_ string, targetURL string) error {
	if bin := findChromelessBrowser(); bin != "" {
		cmd := exec.Command(bin, "--app="+targetURL, "--window-size=440,640")
		if err := cmd.Start(); err == nil {
			go func() { _ = cmd.Wait() }()
			return nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return apps.DefaultDriver().OpenURL(ctx, targetURL)
}

func (browserPIViewer) CloseAll() {}

func findChromelessBrowser() string {
	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
	default:
		candidates = []string{
			"google-chrome",
			"chromium",
			"chromium-browser",
			"microsoft-edge",
			"brave-browser",
		}
	}
	for _, c := range candidates {
		if path, err := exec.LookPath(c); err == nil && path != "" {
			return path
		}
	}
	return ""
}
