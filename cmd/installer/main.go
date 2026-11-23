// Command installer launches the guided Obsidian Slate setup and uninstall wizard for PoundControl.
package main

import (
	"embed"
	"os"
	"path/filepath"

	"github.com/mrpoundsign/appinstaller/pkg/installer"
	"github.com/mrpoundsign/appinstaller/pkg/installerui"

	"github.com/mrpoundsign/PoundControl/assets"
	"github.com/mrpoundsign/PoundControl/internal/version"
)

//go:embed payload/*
var payloadFS embed.FS

// AppVersion is the installer version (overridden via -ldflags on release builds).
var AppVersion = version.Version

func main() {
	os.Exit(installerui.RunMain(buildAppSpec(), assets.AppIcon))
}

func buildAppSpec() installer.AppSpec {
	return installer.AppSpec{
		AppName:       version.AppName,
		AppID:         version.AppID,
		Description:   "Programmable Macro & Stream Deck Studio",
		Publisher:     "mrpoundsign",
		Version:       AppVersion,
		RepoOwner:     "mrpoundsign",
		RepoName:      "PoundControl",
		BinaryName:    "poundcontrol.exe",
		PayloadFS:     payloadFS,
		PayloadSubdir: "payload",
		DataDirsToPurge: func() []string {
			dirs := make([]string, 0, 3)
			if cfgDir, err := os.UserConfigDir(); err == nil && cfgDir != "" {
				dirs = append(dirs,
					filepath.Join(cfgDir, version.AppName),
					filepath.Join(cfgDir, "fyne", version.AppID),
				)
			}
			if localApp := os.Getenv("LOCALAPPDATA"); localApp != "" {
				dirs = append(dirs, filepath.Join(localApp, version.AppID))
			} else if cacheDir, err := os.UserCacheDir(); err == nil && cacheDir != "" {
				dirs = append(dirs, filepath.Join(cacheDir, "fyne", version.AppID))
			}
			return dirs
		},
	}
}
