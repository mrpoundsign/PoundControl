//go:build windows

package ui

import (
	"errors"
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
	"github.com/ncruces/zenity"
)

func extractWindowAttach(win fyne.Window) any {
	nw, ok := win.(driver.NativeWindow)
	if !ok {
		return nil
	}
	var hwnd uintptr
	nw.RunNative(func(raw any) {
		if wc, ok := raw.(driver.WindowsWindowContext); ok {
			hwnd = wc.HWND
		}
	})
	if hwnd == 0 {
		return nil
	}
	return hwnd
}

func runPlatformFileDialog(_ fyne.Window, attach any, req fileDialogRequest) (string, error) {
	path, err := zenity.SelectFile(buildZenityOptions(attach, req)...)
	if errors.Is(err, zenity.ErrCanceled) {
		return "", errFileDialogCanceled
	}
	if err != nil {
		return "", err
	}
	return filepath.Clean(path), nil
}
