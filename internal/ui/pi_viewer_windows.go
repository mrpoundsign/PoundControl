//go:build windows

package ui

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	webview2 "github.com/jchv/go-webview2"

	"github.com/mrpoundsign/PoundDeck/pkg/builtin/apps"
)

type windowsPIViewer struct {
	mu      sync.Mutex
	wv      webview2.WebView
	running bool
	closing bool
}

func newPlatformPIViewer() PIViewer {
	return &windowsPIViewer{}
}

func (v *windowsPIViewer) Open(title, targetURL string) error {
	v.mu.Lock()
	if v.running && v.wv != nil {
		wv := v.wv
		v.mu.Unlock()
		wv.Dispatch(func() {
			wv.SetTitle(title)
			wv.Navigate(targetURL)
		})
		return nil
	}
	if v.running {
		v.mu.Unlock()
		return nil
	}
	v.running = true
	v.closing = false
	v.mu.Unlock()

	ready := make(chan bool, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		wv := webview2.NewWithOptions(webview2.WebViewOptions{
			AutoFocus: true,
			DataPath:  piWebViewDataDir(),
			WindowOptions: webview2.WindowOptions{
				Title:  title,
				Width:  440,
				Height: 640,
				Center: true,
			},
		})
		if wv == nil {
			v.mu.Lock()
			v.wv = nil
			v.running = false
			v.mu.Unlock()
			ready <- false
			return
		}

		v.mu.Lock()
		earlyClose := v.closing
		v.wv = wv
		v.mu.Unlock()

		if earlyClose {
			wv.Destroy()
			v.mu.Lock()
			v.wv = nil
			v.running = false
			v.mu.Unlock()
			ready <- false
			return
		}

		ready <- true
		wv.Navigate(targetURL)
		wv.Run()
		wv.Destroy()

		v.mu.Lock()
		v.wv = nil
		v.running = false
		v.mu.Unlock()
	}()

	if <-ready {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return apps.DefaultDriver().OpenURL(ctx, targetURL)
}

func (v *windowsPIViewer) CloseAll() {
	v.mu.Lock()
	v.closing = true
	wv := v.wv
	v.mu.Unlock()

	if wv != nil {
		wv.Terminate()
	}
}

func piWebViewDataDir() string {
	if cacheDir, err := os.UserCacheDir(); err == nil && cacheDir != "" {
		dir := filepath.Join(cacheDir, "PoundDeck", "WebView2")
		_ = os.MkdirAll(dir, 0o755)
		return dir
	}
	return filepath.Join(os.TempDir(), "pounddeck-webview2")
}
