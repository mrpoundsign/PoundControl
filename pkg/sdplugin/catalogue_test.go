package sdplugin

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func buildTestZipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip.Create(%q) error = %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip.Write(%q) error = %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip.Close() error = %v", err)
	}
	return buf.Bytes()
}

func TestCatalogueFetchFilterInstallUpgradeUninstall(t *testing.T) {
	v1Zip := buildTestZipBytes(t, map[string]string{
		"com.example.market.sdPlugin/manifest.json": `{
			"SDKVersion": 2,
			"UUID": "com.example.market",
			"Name": "Market Plugin",
			"Version": "1.0.0",
			"CodePath": "bin/plugin.js",
			"Actions": [{"UUID": "com.example.market.act", "Name": "Market Act"}]
		}`,
		"com.example.market.sdPlugin/bin/plugin.js": "// v1",
	})
	v1Sum := sha256.Sum256(v1Zip)
	v1Hex := hex.EncodeToString(v1Sum[:])

	v2Zip := buildTestZipBytes(t, map[string]string{
		"com.example.market.sdPlugin/manifest.json": `{
			"SDKVersion": 2,
			"UUID": "com.example.market",
			"Name": "Market Plugin",
			"Version": "2.0.0",
			"CodePath": "bin/plugin.js",
			"Actions": [{"UUID": "com.example.market.act", "Name": "Market Act v2"}]
		}`,
		"com.example.market.sdPlugin/bin/plugin.js": "// v2",
	})

	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/catalogue.json":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"com.example.market": map[string]any{
					"name":         "Market Plugin",
					"author":       "OpenAction Dev",
					"description":  "Awesome open-source plugin",
					"version":      "1.0.0",
					"category":     "Utilities",
					"download_url": srvURL + "/downloads/market-v1.streamDeckPlugin",
					"sha256":       v1Hex,
				},
				"com.example.gh": map[string]any{
					"name":        "GitHub Release Plugin",
					"author":      "Octocat",
					"description": "Fetched via GitHub releases",
					"repository":  "https://github.com/example/gh-plugin",
				},
			})
		case "/downloads/market-v1.streamDeckPlugin":
			_, _ = w.Write(v1Zip)
		case "/downloads/market-v2.streamDeckPlugin":
			_, _ = w.Write(v2Zip)
		case "/repos/example/gh-plugin/releases/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"assets": []map[string]any{
					{
						"name":                 "com.example.gh.streamDeckPlugin",
						"browser_download_url": srvURL + "/downloads/market-v2.streamDeckPlugin",
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL

	client := &CatalogueClient{
		CatalogueURL:     srv.URL + "/catalogue.json",
		GitHubAPIBaseURL: srv.URL,
		HTTPClient:       srv.Client(),
	}

	entries, err := client.Fetch(context.Background(), false)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 catalogue entries, got %d", len(entries))
	}

	filtered := FilterCatalogue(entries, "utilities")
	if len(filtered) != 1 || filtered[0].ID != "com.example.market" {
		t.Fatalf("FilterCatalogue('utilities') = %+v", filtered)
	}

	// Verify GitHub release URL resolution fallback.
	var ghEntry CatalogueEntry
	for _, e := range entries {
		if e.ID == "com.example.gh" {
			ghEntry = e
		}
	}
	resolvedURL, _, err := client.ResolveDownload(context.Background(), ghEntry)
	if err != nil || !strings.HasSuffix(resolvedURL, "/downloads/market-v2.streamDeckPlugin") {
		t.Fatalf("ResolveDownload(github) = (%q, %v)", resolvedURL, err)
	}

	// Install v1 via InstallFromCatalogue with SHA-256 verification.
	root := t.TempDir()
	defaultDir := filepath.Join(root, "plugins")
	externalDir := filepath.Join(root, "external")
	createSyntheticBundle(t, externalDir, "com.example.ext.sdPlugin", `{
		"SDKVersion": 2,
		"UUID": "com.example.ext",
		"Name": "External Plugin",
		"Version": "1.0.0",
		"CodePath": "bin/plugin.js",
		"Actions": [{"UUID": "com.example.ext.act", "Name": "External Act"}]
	}`, "")

	host := newRecordedHost()
	prov := NewProvider(Options{
		DefaultPluginsDir: defaultDir,
		spawnOverride:     func(_ *pluginInstance, _ int) error { return nil },
	})
	prov.BindHost(host)
	defer func() { _ = prov.Close() }()
	if err := prov.SaveHostConfig(HostConfig{ExtraPaths: []string{externalDir}}); err != nil {
		t.Fatalf("SaveHostConfig() error = %v", err)
	}
	if err := prov.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	var marketEntry CatalogueEntry
	for _, e := range entries {
		if e.ID == "com.example.market" {
			marketEntry = e
		}
	}
	installed, err := prov.InstallFromCatalogue(context.Background(), client, marketEntry, nil)
	if err != nil {
		t.Fatalf("InstallFromCatalogue(v1) error = %v", err)
	}
	if installed.Manifest.Version != "1.0.0" {
		t.Fatalf("installed version = %q, want 1.0.0", installed.Manifest.Version)
	}
	if !prov.IsUserManagedPlugin(installed.Dir) {
		t.Fatalf("expected %s to be user-managed", installed.Dir)
	}

	// SHA-256 mismatch must fail cleanly.
	badEntry := marketEntry
	badEntry.SHA256 = strings.Repeat("0", 64)
	if _, err := prov.InstallFromCatalogue(context.Background(), client, badEntry, nil); err == nil {
		t.Fatal("expected SHA-256 mismatch error")
	}

	// Upgrade to v2 via InstallArchive.
	v2Path := filepath.Join(root, "market-v2.streamDeckPlugin")
	if err := os.WriteFile(v2Path, v2Zip, 0o600); err != nil {
		t.Fatalf("WriteFile(v2) error = %v", err)
	}
	upgraded, err := prov.InstallArchive(v2Path)
	if err != nil {
		t.Fatalf("InstallArchive(v2) error = %v", err)
	}
	if upgraded.Manifest.Version != "2.0.0" {
		t.Fatalf("upgraded version = %q, want 2.0.0", upgraded.Manifest.Version)
	}

	// Refusal to uninstall an external plugin outside DefaultPluginsDir.
	if err := prov.UninstallPlugin("com.example.ext"); err == nil {
		t.Fatal("expected refusal to uninstall external plugin outside DefaultPluginsDir")
	}

	// Uninstall user-managed plugin cleanly.
	if err := prov.UninstallPlugin("com.example.market"); err != nil {
		t.Fatalf("UninstallPlugin(com.example.market) error = %v", err)
	}
	for _, st := range prov.Plugins() {
		if st.UUID == "com.example.market" {
			t.Fatalf("expected com.example.market to be removed, still found: %+v", st)
		}
	}
}

func TestInstallArchiveRejectsZipSlip(t *testing.T) {
	root := t.TempDir()
	defaultDir := filepath.Join(root, "plugins")

	prov := NewProvider(Options{
		DefaultPluginsDir: defaultDir,
		spawnOverride:     func(_ *pluginInstance, _ int) error { return nil },
	})
	prov.BindHost(newRecordedHost())
	defer func() { _ = prov.Close() }()
	if err := prov.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	maliciousZip := buildTestZipBytes(t, map[string]string{
		"../escaped.txt": "pwned",
		"com.evil.sdPlugin/manifest.json": `{
			"SDKVersion": 2,
			"UUID": "com.evil",
			"Name": "Evil",
			"CodePath": "bin/plugin.js",
			"Actions": [{"UUID": "com.evil.a", "Name": "Evil"}]
		}`,
	})
	archivePath := filepath.Join(root, "evil.streamDeckPlugin")
	if err := os.WriteFile(archivePath, maliciousZip, 0o600); err != nil {
		t.Fatalf("WriteFile(evil) error = %v", err)
	}

	if _, err := prov.InstallArchive(archivePath); err == nil {
		t.Fatal("expected InstallArchive to reject zip-slip traversal archive")
	}
	if _, err := os.Stat(filepath.Join(root, "escaped.txt")); !os.IsNotExist(err) {
		t.Fatal("zip-slip file escaped staging directory")
	}
}

func TestInstallArchiveWindowsBackslashDirectories(t *testing.T) {
	root := t.TempDir()
	defaultDir := filepath.Join(root, "plugins")

	prov := NewProvider(Options{
		DefaultPluginsDir: defaultDir,
		spawnOverride:     func(_ *pluginInstance, _ int) error { return nil },
	})
	prov.BindHost(newRecordedHost())
	defer func() { _ = prov.Close() }()
	if err := prov.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, dirEntry := range []string{
		`com.example.winzip.sdPlugin\`,
		`com.example.winzip.sdPlugin\bin\`,
		`com.example.winzip.sdPlugin\ColorDefinitions\`,
	} {
		hdr := &zip.FileHeader{
			Name:          dirEntry,
			ExternalAttrs: 0x10, // MS-DOS directory attribute
		}
		if _, err := zw.CreateHeader(hdr); err != nil {
			t.Fatalf("CreateHeader(%q) error = %v", dirEntry, err)
		}
	}
	for name, content := range map[string]string{
		`com.example.winzip.sdPlugin\manifest.json`: `{
			"SDKVersion": 2,
			"UUID": "com.example.winzip",
			"Name": "WinZip Plugin",
			"Version": "1.0.0",
			"CodePath": "bin/plugin.js",
			"Actions": [{"UUID": "com.example.winzip.act", "Name": "WinZip Act"}]
		}`,
		`com.example.winzip.sdPlugin\bin\plugin.js`:              "// ok",
		`com.example.winzip.sdPlugin\ColorDefinitions\defs.json`: "{}",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("Create(%q) error = %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("Write(%q) error = %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	archivePath := filepath.Join(root, "winzip.streamDeckPlugin")
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	bundle, err := prov.InstallArchive(archivePath)
	if err != nil {
		t.Fatalf("InstallArchive(windows backslash dirs) error = %v", err)
	}
	if bundle.UUID != "com.example.winzip" {
		t.Fatalf("UUID = %q, want com.example.winzip", bundle.UUID)
	}
}

func TestCatalogueIconsDetailsAndRenderedHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/plugins/catalogue.json":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"com.example.rich": map[string]any{
					"name":        "Rich Plugin",
					"author":      "Dev",
					"description": "Short summary",
					"repository":  "https://github.com/example/rich-plugin",
					"flags":       map[string]bool{"ai_assisted": true},
				},
			})
		case "/plugins/icons/com.example.rich.png":
			_, _ = w.Write([]byte("fake-png-bytes"))
		case "/repos/example/rich-plugin":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"stargazers_count": 42,
				"default_branch":   "main",
				"homepage":         "https://rich.example.com",
				"license":          map[string]string{"spdx_id": "MIT"},
			})
		case "/repos/example/rich-plugin/releases/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v1.4.0",
				"body":     "Added screenshots support.",
			})
		case "/repos/example/rich-plugin/readme":
			_, _ = w.Write([]byte(`<h1>Rich Plugin</h1><img src="./docs/screen.png"><script>alert(1)</script><a href="CHANGELOG.md">Log</a>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := &CatalogueClient{
		CatalogueURL:     srv.URL + "/plugins/catalogue.json",
		GitHubAPIBaseURL: srv.URL,
		HTTPClient:       srv.Client(),
	}

	entries, err := client.Fetch(context.Background(), true)
	if err != nil || len(entries) != 1 {
		t.Fatalf("Fetch() = (%d, %v)", len(entries), err)
	}
	entry := entries[0]
	if !entry.IsAIAssisted() {
		t.Fatal("expected IsAIAssisted() == true")
	}
	wantIconURL := srv.URL + "/plugins/icons/com.example.rich.png"
	if entry.IconURL != wantIconURL {
		t.Fatalf("IconURL = %q, want %q", entry.IconURL, wantIconURL)
	}
	iconBytes, err := client.FetchIcon(context.Background(), entry)
	if err != nil || string(iconBytes) != "fake-png-bytes" {
		t.Fatalf("FetchIcon() = (%q, %v)", string(iconBytes), err)
	}

	details := client.FetchPluginDetails(context.Background(), entry)
	if details.LatestVersion != "1.4.0" || details.Stars != 42 || details.License != "MIT" {
		t.Fatalf("unexpected details metadata: %+v", details)
	}
	if strings.Contains(details.ReadmeHTML, "<script>") {
		t.Fatalf("expected script tag stripped from README HTML: %s", details.ReadmeHTML)
	}
	if !strings.Contains(details.ReadmeHTML, "https://raw.githubusercontent.com/example/rich-plugin/main/docs/screen.png") {
		t.Fatalf("expected relative screenshot URL rewritten, got: %s", details.ReadmeHTML)
	}
	if !strings.Contains(details.ReadmeHTML, "https://github.com/example/rich-plugin/blob/main/CHANGELOG.md") {
		t.Fatalf("expected relative link rewritten, got: %s", details.ReadmeHTML)
	}

	rendered := RenderPluginDetailsHTML(details)
	if !strings.Contains(rendered, "Added screenshots support.") || !strings.Contains(rendered, "AI-Assisted") {
		t.Fatalf("rendered detail HTML missing expected sections: %s", rendered)
	}

	prov := NewProvider(Options{
		DefaultPluginsDir: t.TempDir(),
		spawnOverride:     func(_ *pluginInstance, _ int) error { return nil },
	})
	prov.BindHost(newRecordedHost())
	defer func() { _ = prov.Close() }()
	if err := prov.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	detailURL, ok := prov.RegisterMarketplaceDetailPage(rendered)
	if !ok || !strings.Contains(detailURL, "/marketplace/info/") {
		t.Fatalf("RegisterMarketplaceDetailPage() = (%q, %v)", detailURL, ok)
	}
	resp, err := http.Get(detailURL)
	if err != nil {
		t.Fatalf("http.Get(detailURL) error = %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("detailURL status = %d, want 200", resp.StatusCode)
	}
}
