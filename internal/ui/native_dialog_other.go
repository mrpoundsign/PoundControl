//go:build !windows && !linux

package ui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"fyne.io/fyne/v2"
	"github.com/ncruces/zenity"
)

func extractWindowAttach(_ fyne.Window) any {
	if runtime.GOOS == "darwin" {
		return os.Getpid()
	}
	return nil
}

func runPlatformFileDialog(_ fyne.Window, attach any, req fileDialogRequest) (string, error) {
	path, err := zenity.SelectFile(buildZenityOptions(attach, req)...)
	if errors.Is(err, zenity.ErrCanceled) {
		return "", errFileDialogCanceled
	}
	if err != nil {
		if errors.Is(err, zenity.ErrUnsupported) || errors.Is(err, exec.ErrNotFound) {
			return "", errNativeDialogUnavailable
		}
		return "", err
	}
	return filepath.Clean(path), nil
}
