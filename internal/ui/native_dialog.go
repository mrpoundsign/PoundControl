package ui

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"github.com/ncruces/zenity"
)

type fileDialogFilter struct {
	Name       string
	Extensions []string
}

type fileDialogRequest struct {
	Title       string
	InitialPath string
	Directory   bool
	Filters     []fileDialogFilter
}

var (
	errFileDialogCanceled      = errors.New("file dialog canceled")
	errNativeDialogUnavailable = errors.New("native file dialog unavailable")

	nativeFileDialogOpen   atomic.Bool
	nativeFileDialogRunner = runPlatformFileDialog
)

// openNativeFileDialog launches an OS-native file or directory picker off the Fyne UI thread
// so drive enumeration and directory listing never block rendering, and marshals the result back
// via dispatchUI. Concurrent calls while a dialog is already open are ignored.
func openNativeFileDialog(win fyne.Window, req fileDialogRequest, onDone func(path string, err error)) {
	if win == nil {
		return
	}
	if !nativeFileDialogOpen.CompareAndSwap(false, true) {
		return
	}

	attach := extractWindowAttach(win)
	go func() {
		req.InitialPath = resolveInitialDialogPath(req.InitialPath)
		path, err := nativeFileDialogRunner(win, attach, req)
		if errors.Is(err, errNativeDialogUnavailable) {
			dispatchUI(func() {
				var once sync.Once
				release := func() {
					once.Do(func() {
						nativeFileDialogOpen.Store(false)
					})
				}
				showFyneFallbackFileDialog(win, req, onDone, release)
			})
			return
		}

		nativeFileDialogOpen.Store(false)
		if errors.Is(err, errFileDialogCanceled) || (err == nil && strings.TrimSpace(path) == "") {
			return
		}
		dispatchUI(func() {
			if onDone != nil {
				onDone(path, err)
			}
		})
	}()
}

// resolveInitialDialogPath resolves an optional entry value (directory path, file path,
// or file:// URI) to an existing local directory suitable as a dialog starting folder.
func resolveInitialDialogPath(raw string) string {
	p := strings.TrimSpace(raw)
	if p == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(p), "file://") {
		if u, err := url.Parse(p); err == nil {
			uriPath := u.Path
			if len(uriPath) >= 3 && uriPath[0] == '/' && uriPath[2] == ':' {
				uriPath = uriPath[1:]
			}
			p = filepath.FromSlash(uriPath)
		}
	} else if strings.Contains(p, "://") {
		return ""
	}

	cleaned := filepath.Clean(p)
	if fi, err := os.Stat(cleaned); err == nil {
		dir := cleaned
		if !fi.IsDir() {
			dir = filepath.Dir(cleaned)
		}
		if abs, absErr := filepath.Abs(dir); absErr == nil {
			return abs
		}
		return dir
	}

	parent := filepath.Dir(cleaned)
	if parent != "" && parent != "." && parent != cleaned {
		if fi, err := os.Stat(parent); err == nil && fi.IsDir() {
			if abs, absErr := filepath.Abs(parent); absErr == nil {
				return abs
			}
			return parent
		}
	}
	return ""
}

func formatFilterLabel(name string, patterns []string) string {
	base := strings.TrimSpace(name)
	if base == "" {
		base = "Supported Files"
	}
	if len(patterns) == 0 {
		return base
	}
	return fmt.Sprintf("%s (%s)", base, strings.Join(patterns, ", "))
}

func buildZenityFilters(filters []fileDialogFilter) zenity.FileFilters {
	if len(filters) == 0 {
		return nil
	}
	out := make(zenity.FileFilters, 0, len(filters)+1)
	for _, f := range filters {
		exts := normalizeFileExtensions(f.Extensions)
		if len(exts) == 0 {
			continue
		}
		patterns := make([]string, 0, len(exts))
		seenLower := make([]string, 0, len(exts))
		for _, ext := range exts {
			lower := strings.ToLower(ext)
			if slices.Contains(seenLower, lower) {
				continue
			}
			seenLower = append(seenLower, lower)
			patterns = append(patterns, "*"+ext)
		}
		if len(patterns) == 0 {
			continue
		}
		out = append(out, zenity.FileFilter{
			Name:     formatFilterLabel(f.Name, patterns),
			Patterns: patterns,
			CaseFold: true,
		})
	}
	if len(out) == 0 {
		return nil
	}
	out = append(out, zenity.FileFilter{
		Name:     "All Files (*.*)",
		Patterns: []string{"*"},
		CaseFold: true,
	})
	return out
}

func buildZenityOptions(attach any, req fileDialogRequest) []zenity.Option {
	opts := make([]zenity.Option, 0, 5)
	if title := strings.TrimSpace(req.Title); title != "" {
		opts = append(opts, zenity.Title(title))
	}
	if req.Directory {
		opts = append(opts, zenity.Directory())
	}
	if req.InitialPath != "" {
		opts = append(opts, zenity.Filename(req.InitialPath))
	}
	if attach != nil {
		opts = append(opts, zenity.Attach(attach))
	}
	if !req.Directory {
		if filters := buildZenityFilters(req.Filters); len(filters) > 0 {
			opts = append(opts, filters)
		}
	}
	return opts
}

func showFyneFallbackFileDialog(win fyne.Window, req fileDialogRequest, onDone func(path string, err error), release func()) {
	if win == nil {
		if release != nil {
			release()
		}
		return
	}

	if req.Directory {
		dlg := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
			if release != nil {
				release()
			}
			if err != nil {
				if onDone != nil {
					onDone("", err)
				}
				return
			}
			if uri == nil {
				return
			}
			if onDone != nil {
				onDone(uriToLocalPath(uri), nil)
			}
		}, win)
		if title := strings.TrimSpace(req.Title); title != "" {
			dlg.SetTitleText(title)
		}
		if req.InitialPath != "" {
			if lister, err := storage.ListerForURI(storage.NewFileURI(req.InitialPath)); err == nil && lister != nil {
				dlg.SetLocation(lister)
			}
		}
		if release != nil {
			dlg.SetOnClosed(release)
		}
		dlg.Resize(fyne.NewSize(760, 520))
		dlg.Show()
		return
	}

	fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
		if release != nil {
			release()
		}
		if err != nil {
			if onDone != nil {
				onDone("", err)
			}
			return
		}
		if r == nil {
			return
		}
		filePath := uriToLocalPath(r.URI())
		_ = r.Close()
		if onDone != nil {
			onDone(filePath, nil)
		}
	}, win)
	if title := strings.TrimSpace(req.Title); title != "" {
		fd.SetTitleText(title)
	}
	if req.InitialPath != "" {
		if lister, err := storage.ListerForURI(storage.NewFileURI(req.InitialPath)); err == nil && lister != nil {
			fd.SetLocation(lister)
		}
	}
	if exts := collectFilterExtensions(req.Filters); len(exts) > 0 {
		fd.SetFilter(storage.NewExtensionFileFilter(exts))
	}
	if release != nil {
		fd.SetOnClosed(release)
	}
	fd.Resize(fyne.NewSize(760, 520))
	fd.Show()
}

func collectFilterExtensions(filters []fileDialogFilter) []string {
	if len(filters) == 0 {
		return nil
	}
	var raw []string
	for _, f := range filters {
		raw = append(raw, f.Extensions...)
	}
	return normalizeFileExtensions(raw)
}

func uriToLocalPath(u fyne.URI) string {
	if u == nil {
		return ""
	}
	p := u.Path()
	if strings.EqualFold(u.Scheme(), "file") {
		if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
			p = p[1:]
		}
		p = filepath.FromSlash(p)
	}
	return filepath.Clean(p)
}
