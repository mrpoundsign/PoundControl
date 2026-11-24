package wasmplugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func buildMinimalTestWasm(t *testing.T, uuid, name, version, outPath string) {
	t.Helper()
	srcDir := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", srcDir, err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "go.mod"), []byte("module testfixture\n\ngo 1.27\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(go.mod) error = %v", err)
	}
	guestCode := fmt.Sprintf(`package main

import (
	"encoding/json"
	"unsafe"
)

var allocations = map[uint32][]byte{}

//go:wasmexport pc_alloc
func pcAlloc(size uint32) uint32 {
	if size == 0 {
		size = 1
	}
	buf := make([]byte, size)
	ptr := uint32(uintptr(unsafe.Pointer(&buf[0])))
	allocations[ptr] = buf
	return ptr
}

//go:wasmexport pc_free
func pcFree(ptr uint32) {
	delete(allocations, ptr)
}

func returnHostBytes(b []byte) uint64 {
	ptr := pcAlloc(uint32(len(b)))
	copy(allocations[ptr], b)
	return (uint64(ptr) << 32) | uint64(len(b))
}

//go:wasmexport pc_manifest
func pcManifest() uint64 {
	manifest := map[string]any{
		"uuid":    %q,
		"name":    %q,
		"version": %q,
		"author":  "Test Author",
		"actions": []map[string]any{
			{"uuid": %q, "name": "Test Action"},
		},
	}
	raw, _ := json.Marshal(manifest)
	return returnHostBytes(raw)
}

//go:wasmexport pc_handle_event
func pcHandleEvent(ptr, size uint32) int32 {
	return 0
}

func main() {}
`, uuid, name, version, uuid+".act")

	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte(guestCode), 0o600); err != nil {
		t.Fatalf("WriteFile(main.go) error = %v", err)
	}
	compileWasmFixture(t, srcDir, outPath)
}

func TestInstallFileUpgradeValidationAndUninstall(t *testing.T) {
	tmp := t.TempDir()
	defaultDir := filepath.Join(tmp, "wasm")
	extraDir := filepath.Join(tmp, "external")
	stagingDir := filepath.Join(tmp, "staging")
	for _, d := range []string{defaultDir, extraDir, stagingDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", d, err)
		}
	}

	const (
		managedUUID  = "com.test.wasm.managed"
		externalUUID = "com.test.wasm.external"
	)

	v1Src := filepath.Join(defaultDir, "custom-legacy-name.wasm")
	buildMinimalTestWasm(t, managedUUID, "Managed Plugin", "1.0.0", v1Src)

	v2Src := filepath.Join(stagingDir, "managed-v2.wasm")
	buildMinimalTestWasm(t, managedUUID, "Managed Plugin", "2.0.0", v2Src)

	extPath := filepath.Join(extraDir, "external.wasm")
	buildMinimalTestWasm(t, externalUUID, "External Plugin", "1.0.0", extPath)

	corruptPath := filepath.Join(stagingDir, "corrupt.wasm")
	if err := os.WriteFile(corruptPath, []byte("not a valid wasm module"), 0o600); err != nil {
		t.Fatalf("WriteFile(corrupt) error = %v", err)
	}

	host := newFakeHostAPI()
	prov := NewProvider(Options{DefaultPluginsDir: defaultDir})
	prov.BindHost(host)
	t.Cleanup(func() { _ = prov.Close() })

	if err := prov.SaveHostConfig(HostConfig{ExtraPaths: []string{extraDir}}); err != nil {
		t.Fatalf("SaveHostConfig() error = %v", err)
	}
	if err := prov.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if !prov.IsUserManagedPlugin(v1Src) {
		t.Fatalf("expected %s inside DefaultPluginsDir to be user-managed", v1Src)
	}
	if prov.IsUserManagedPlugin(extPath) {
		t.Fatalf("expected external plugin %s NOT to be user-managed", extPath)
	}
	if err := prov.UninstallPlugin(externalUUID); err == nil {
		t.Fatal("expected UninstallPlugin to refuse external plugin outside DefaultPluginsDir")
	}

	// Upgrade v1 -> v2 via InstallFile: replaces legacy filename with canonical sanitized UUID filename.
	st, err := prov.InstallFile(v2Src, nil)
	if err != nil {
		t.Fatalf("InstallFile(v2) error = %v", err)
	}
	if st.UUID != managedUUID || st.Version != "2.0.0" || st.State != StatusRunning {
		t.Fatalf("unexpected status after upgrade: %+v", st)
	}
	expectedCanonical := filepath.Join(defaultDir, managedUUID+".wasm")
	if !sameLocalPath(st.Path, expectedCanonical) {
		t.Fatalf("installed path = %q, want %q", st.Path, expectedCanonical)
	}
	if _, statErr := os.Stat(v1Src); !os.IsNotExist(statErr) {
		t.Fatalf("expected legacy duplicate wasm file %q removed after upgrade", v1Src)
	}

	meta := prov.InstalledMetadata()
	if rec, ok := meta[managedUUID]; !ok || rec.Source != "local" || rec.InstalledVersion != "2.0.0" {
		t.Fatalf("unexpected install metadata after upgrade: %+v", meta)
	}

	// Corrupt wasm upload must be rejected BEFORE stopping or breaking the active v2 plugin.
	if _, err := prov.InstallFile(corruptPath, nil); err == nil {
		t.Fatal("expected InstallFile(corrupt) to fail validation")
	}
	var foundV2 bool
	for _, cur := range prov.Plugins() {
		if cur.UUID == managedUUID {
			foundV2 = true
			if cur.Version != "2.0.0" || cur.State != StatusRunning {
				t.Fatalf("expected v2 still running after failed corrupt install, got %+v", cur)
			}
		}
	}
	if !foundV2 {
		t.Fatal("expected v2 plugin still loaded after rejected corrupt install")
	}

	// Uninstall removes managed plugin + metadata while leaving external plugin running.
	if err := prov.UninstallPlugin(managedUUID); err != nil {
		t.Fatalf("UninstallPlugin(managed) error = %v", err)
	}
	if _, statErr := os.Stat(expectedCanonical); !os.IsNotExist(statErr) {
		t.Fatalf("expected %s removed after uninstall", expectedCanonical)
	}
	if _, stillMeta := prov.InstalledMetadata()[managedUUID]; stillMeta {
		t.Fatal("expected metadata entry removed after uninstall")
	}
	for _, cur := range prov.Plugins() {
		if cur.UUID == managedUUID {
			t.Fatalf("did not expect uninstalled plugin in Plugins(): %+v", cur)
		}
	}
}

func TestInstallFromGitHubReleaseWithSHA256Verification(t *testing.T) {
	tmp := t.TempDir()
	defaultDir := filepath.Join(tmp, "wasm")
	buildPath := filepath.Join(tmp, "sample.wasm")
	const pluginUUID = "com.test.wasm.github"
	buildMinimalTestWasm(t, pluginUUID, "GitHub Wasm", "1.2.3", buildPath)

	wasmBytes, err := os.ReadFile(buildPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", buildPath, err)
	}
	sum := sha256.Sum256(wasmBytes)
	validHash := hex.EncodeToString(sum[:])

	checksumBody := strings.Repeat("0", 64) + "  sample.wasm\n"
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/mrpoundsign/sample-wasm/releases/latest":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v1.2.3",
				"name":     "v1.2.3",
				"assets": []map[string]any{
					{
						"name":                 "sample.wasm",
						"browser_download_url": srvURL + "/downloads/sample.wasm",
						"size":                 len(wasmBytes),
					},
					{
						"name":                 "checksums.txt",
						"browser_download_url": srvURL + "/downloads/checksums.txt",
						"size":                 len(checksumBody),
					},
				},
			})
		case "/downloads/sample.wasm":
			_, _ = w.Write(wasmBytes)
		case "/downloads/checksums.txt":
			_, _ = w.Write([]byte(checksumBody))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL

	prov := NewProvider(Options{DefaultPluginsDir: defaultDir})
	prov.BindHost(newFakeHostAPI())
	t.Cleanup(func() { _ = prov.Close() })

	// 1. Mismatched SHA-256 must fail and leave DefaultPluginsDir empty.
	if _, err := prov.InstallFromGitHubRelease(context.Background(), "mrpoundsign", "sample-wasm", "sample.wasm", srv.URL, srv.Client(), nil); err == nil {
		t.Fatal("expected SHA-256 mismatch error")
	}
	if len(prov.Plugins()) != 0 {
		t.Fatalf("expected no plugins installed on SHA-256 mismatch, got %+v", prov.Plugins())
	}

	// 2. Valid SHA-256 installs cleanly and records GitHub provenance.
	checksumBody = validHash + "  sample.wasm\n"
	var stages []string
	st, err := prov.InstallFromGitHubRelease(context.Background(), "mrpoundsign", "sample-wasm", "sample.wasm", srv.URL, srv.Client(), func(_, _ int64, stage string) {
		stages = append(stages, stage)
	})
	if err != nil {
		t.Fatalf("InstallFromGitHubRelease() error = %v", err)
	}
	if st.UUID != pluginUUID || st.Version != "1.2.3" || st.State != StatusRunning {
		t.Fatalf("unexpected installed plugin status: %+v", st)
	}
	if len(stages) == 0 {
		t.Fatal("expected progress callbacks during GitHub release install")
	}

	meta := prov.InstalledMetadata()[pluginUUID]
	if meta.Source != wasmPluginSourceRepo ||
		meta.Repository != "https://github.com/mrpoundsign/sample-wasm" ||
		meta.AssetName != "sample.wasm" ||
		meta.InstalledVersion != "1.2.3" {
		t.Fatalf("unexpected GitHub install metadata: %+v", meta)
	}
}
