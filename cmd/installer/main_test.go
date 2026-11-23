package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mrpoundsign/appinstaller/pkg/installer"

	"github.com/mrpoundsign/PoundControl/internal/version"
)

func TestBuildAppSpec(t *testing.T) {
	spec := buildAppSpec()
	if spec.AppName != version.AppName {
		t.Fatalf("expected AppName %q, got %q", version.AppName, spec.AppName)
	}
	if spec.AppID != version.AppID {
		t.Fatalf("expected AppID %q, got %q", version.AppID, spec.AppID)
	}
	if spec.RepoOwner != "mrpoundsign" || spec.RepoName != "PoundControl" {
		t.Fatalf("unexpected GitHub repo %s/%s", spec.RepoOwner, spec.RepoName)
	}
	if spec.BinaryName != "poundcontrol.exe" {
		t.Fatalf("expected BinaryName poundcontrol.exe, got %q", spec.BinaryName)
	}
	if spec.PayloadSubdir != "payload" {
		t.Fatalf("expected PayloadSubdir payload, got %q", spec.PayloadSubdir)
	}
	if len(spec.OptionalComponents) != 0 {
		t.Fatalf("expected no optional components, got %+v", spec.OptionalComponents)
	}
	if spec.DataDirsToPurge == nil {
		t.Fatal("expected non-nil DataDirsToPurge")
	}

	installDir := filepath.Join(t.TempDir(), "PoundControl")
	inst := installer.NewInstaller(spec)
	manifest, err := inst.Install(context.Background(), installer.Options{
		InstallDir: installDir,
	}, nil)
	if err != nil {
		t.Fatalf("Install into TempDir failed: %v", err)
	}
	if manifest == nil || manifest.AppName != version.AppName {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
	if _, err := os.Stat(filepath.Join(installDir, installer.ManifestFileName)); err != nil {
		t.Fatalf("expected %s written in installDir: %v", installer.ManifestFileName, err)
	}
}
