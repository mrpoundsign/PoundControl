//go:build linux

package ui

import (
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
	"fyne.io/fyne/v2/storage"
	"github.com/ncruces/zenity"
	"github.com/rymdport/portal"
	"github.com/rymdport/portal/filechooser"
)

func extractWindowAttach(win fyne.Window) any {
	nw, ok := win.(driver.NativeWindow)
	if !ok {
		return nil
	}
	var handle uintptr
	nw.RunNative(func(raw any) {
		if xc, ok := raw.(driver.X11WindowContext); ok {
			handle = xc.WindowHandle
		}
	})
	if handle == 0 {
		return nil
	}
	return handle
}

func runPlatformFileDialog(_ fyne.Window, attach any, req fileDialogRequest) (string, error) {
	title := strings.TrimSpace(req.Title)
	if title == "" {
		if req.Directory {
			title = "Open Folder"
		} else {
			title = "Open File"
		}
	}

	parentHandle := ""
	if h, ok := attach.(uintptr); ok && h != 0 {
		parentHandle = portal.FormatX11WindowHandle(h)
	}

	opts := &filechooser.OpenFileOptions{
		Directory:     req.Directory,
		CurrentFolder: req.InitialPath,
	}
	if !req.Directory {
		opts.Filters, opts.CurrentFilter = convertFiltersForPortal(req.Filters)
	}

	if uris, err := filechooser.OpenFile(parentHandle, title, opts); err == nil {
		if len(uris) == 0 {
			return "", errFileDialogCanceled
		}
		u, parseErr := storage.ParseURI(uris[0])
		if parseErr != nil {
			return "", parseErr
		}
		return uriToLocalPath(u), nil
	}

	var zenAttach any
	if h, ok := attach.(uintptr); ok && h != 0 {
		zenAttach = int(h)
	}
	path, err := zenity.SelectFile(buildZenityOptions(zenAttach, req)...)
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

func convertFiltersForPortal(filters []fileDialogFilter) ([]*filechooser.Filter, *filechooser.Filter) {
	if len(filters) == 0 {
		return nil, nil
	}
	list := make([]*filechooser.Filter, 0, len(filters)+1)
	for _, f := range filters {
		exts := normalizeFileExtensions(f.Extensions)
		if len(exts) == 0 {
			continue
		}
		rules := make([]filechooser.Rule, 0, len(exts)*2)
		displayPatterns := make([]string, 0, len(exts))
		seenRules := make([]string, 0, len(exts)*3)
		seenDisplay := make([]string, 0, len(exts))
		for _, ext := range exts {
			lowerExt := strings.ToLower(ext)
			if !slices.Contains(seenDisplay, lowerExt) {
				seenDisplay = append(seenDisplay, lowerExt)
				displayPatterns = append(displayPatterns, "*"+ext)
			}
			for _, pattern := range []string{"*" + lowerExt, "*" + strings.ToUpper(ext), "*" + ext} {
				if !slices.Contains(seenRules, pattern) {
					seenRules = append(seenRules, pattern)
					rules = append(rules, filechooser.Rule{
						Type:    filechooser.GlobPattern,
						Pattern: pattern,
					})
				}
			}
		}
		if len(rules) == 0 {
			continue
		}
		list = append(list, &filechooser.Filter{
			Name:  formatFilterLabel(f.Name, displayPatterns),
			Rules: rules,
		})
	}
	if len(list) == 0 {
		return nil, nil
	}
	list = append(list, &filechooser.Filter{
		Name: "All Files (*.*)",
		Rules: []filechooser.Rule{
			{Type: filechooser.GlobPattern, Pattern: "*"},
		},
	})
	return list, list[0]
}
