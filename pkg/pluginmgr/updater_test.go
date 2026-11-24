package pluginmgr

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/mrpoundsign/PoundControl/pkg/plugin"
	"github.com/mrpoundsign/PoundControl/pkg/sdplugin"
	"github.com/mrpoundsign/PoundControl/pkg/wasmplugin"
)

type fakeWasmManager struct {
	mu           sync.Mutex
	plugins      []wasmplugin.PluginStatus
	managed      map[string]bool
	meta         map[string]wasmplugin.InstallRecord
	installCalls []string
	installVer   string
}

func (f *fakeWasmManager) Plugins() []wasmplugin.PluginStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]wasmplugin.PluginStatus, len(f.plugins))
	copy(out, f.plugins)
	return out
}

func (f *fakeWasmManager) IsUserManagedPlugin(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.managed[path]
}

func (f *fakeWasmManager) InstalledMetadata() map[string]wasmplugin.InstallRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]wasmplugin.InstallRecord, len(f.meta))
	maps.Copy(out, f.meta)
	return out
}

func (f *fakeWasmManager) InstallFromGitHubRelease(
	_ context.Context,
	owner, repo, assetName, _ string,
	_ *http.Client,
	_ func(downloaded, total int64, stage string),
) (wasmplugin.PluginStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installCalls = append(f.installCalls, owner+"/"+repo+":"+assetName)
	ver := f.installVer
	if ver == "" {
		ver = "0.2.0"
	}
	var updated wasmplugin.PluginStatus
	for i := range f.plugins {
		if f.managed[f.plugins[i].Path] {
			f.plugins[i].Version = ver
			updated = f.plugins[i]
			break
		}
	}
	if updated.UUID == "" {
		updated = wasmplugin.PluginStatus{
			UUID:    "com.github.mrpoundsign.poundcontrol.twitch",
			Name:    "Twitch",
			Version: ver,
		}
	}
	return updated, nil
}

type fakeSDManager struct {
	mu           sync.Mutex
	plugins      []sdplugin.PluginStatus
	managed      map[string]bool
	installCalls []string
	installVer   string
}

func (f *fakeSDManager) Plugins() []sdplugin.PluginStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]sdplugin.PluginStatus, len(f.plugins))
	copy(out, f.plugins)
	return out
}

func (f *fakeSDManager) IsUserManagedPlugin(dir string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.managed[dir]
}

func (f *fakeSDManager) InstallFromCatalogue(
	_ context.Context,
	_ *sdplugin.CatalogueClient,
	entry sdplugin.CatalogueEntry,
	_ func(downloaded, total int64, stage string),
) (*sdplugin.LoadedBundle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installCalls = append(f.installCalls, entry.ID)
	ver := f.installVer
	if ver == "" {
		ver = "1.5.0"
	}
	for i := range f.plugins {
		if f.plugins[i].UUID == entry.ID {
			f.plugins[i].Version = ver
		}
	}
	return &sdplugin.LoadedBundle{
		UUID: entry.ID,
		Manifest: sdplugin.SDManifest{
			UUID:    entry.ID,
			Name:    entry.Name,
			Version: ver,
		},
	}, nil
}

func TestManagerCheckUpdatesApplyAndNotifications(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/curated.json":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": 1,
				"plugins": []map[string]any{
					{
						"id":          "com.github.mrpoundsign.poundcontrol.twitch",
						"name":        "Twitch",
						"kind":        "wasm",
						"repository":  "https://github.com/mrpoundsign/PoundControl-Twitch",
						"assetName":   "poundcontrol-twitch.wasm",
						"description": "Twitch controls",
						"featured":    true,
					},
				},
			})
		case "/openaction.json":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"com.example.oa.update": map[string]any{
					"name":       "OA Needs Update",
					"version":    "1.5.0",
					"repository": "https://github.com/example/oa-update",
				},
				"com.example.oa.ahead": map[string]any{
					"name":       "OA Dev Ahead",
					"version":    "1.5.0",
					"repository": "https://github.com/example/oa-ahead",
				},
				"com.example.oa.external": map[string]any{
					"name":       "OA External",
					"version":    "9.9.9",
					"repository": "https://github.com/example/oa-ext",
				},
			})
		case "/repos/mrpoundsign/PoundControl-Twitch/releases/latest":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v0.2.0",
				"body":     "- Added Raid action",
			})
		case "/repos/example/custom-wasm/releases/latest":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v1.0.0",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	wasmMgr := &fakeWasmManager{
		plugins: []wasmplugin.PluginStatus{
			{
				UUID:    "com.github.mrpoundsign.poundcontrol.twitch",
				Name:    "Twitch",
				Version: "0.1.0",
				Path:    `C:\managed\twitch.wasm`,
			},
			{
				UUID:    "com.example.custom.wasm",
				Name:    "Custom Wasm",
				Version: "1.2.0", // ahead of release v1.0.0 -> no update
				Path:    `C:\managed\custom.wasm`,
			},
			{
				UUID:    "com.example.ext.wasm",
				Name:    "External Wasm",
				Version: "0.0.1", // external -> ignored
				Path:    `C:\external\ext.wasm`,
			},
		},
		managed: map[string]bool{
			`C:\managed\twitch.wasm`: true,
			`C:\managed\custom.wasm`: true,
			`C:\external\ext.wasm`:   false,
		},
		meta: map[string]wasmplugin.InstallRecord{
			"com.example.custom.wasm": {
				UUID:       "com.example.custom.wasm",
				Repository: "https://github.com/example/custom-wasm",
				AssetName:  "custom.wasm",
			},
		},
	}

	sdMgr := &fakeSDManager{
		plugins: []sdplugin.PluginStatus{
			{
				UUID:    "com.example.oa.update",
				Name:    "OA Needs Update",
				Version: "1.0.0",
				Dir:     `C:\managed\com.example.oa.update.sdPlugin`,
			},
			{
				UUID:    "com.example.oa.ahead",
				Name:    "OA Dev Ahead",
				Version: "2.0.0",
				Dir:     `C:\managed\com.example.oa.ahead.sdPlugin`,
			},
			{
				UUID:    "com.example.oa.external",
				Name:    "OA External",
				Version: "0.1.0",
				Dir:     `C:\elgato\com.example.oa.external.sdPlugin`,
			},
		},
		managed: map[string]bool{
			`C:\managed\com.example.oa.update.sdPlugin`:  true,
			`C:\managed\com.example.oa.ahead.sdPlugin`:   true,
			`C:\elgato\com.example.oa.external.sdPlugin`: false,
		},
	}

	curatedCli := &CuratedClient{
		CatalogURL:       srv.URL + "/curated.json",
		GitHubAPIBaseURL: srv.URL,
		HTTPClient:       srv.Client(),
	}
	oaCli := &sdplugin.CatalogueClient{
		CatalogueURL:     srv.URL + "/openaction.json",
		GitHubAPIBaseURL: srv.URL,
		HTTPClient:       srv.Client(),
	}

	mgr := NewManager(curatedCli, oaCli, wasmMgr, sdMgr)
	mgr.GitHubAPIBaseURL = srv.URL
	mgr.HTTPClient = srv.Client()

	var (
		subMu    sync.Mutex
		subCalls []int
	)
	unsub := mgr.SubscribeUpdates(func(sum UpdateSummary) {
		subMu.Lock()
		subCalls = append(subCalls, len(sum.Updates))
		subMu.Unlock()
	})
	defer unsub()

	sum := mgr.CheckUpdates(context.Background(), true)
	if len(sum.Errors) != 0 {
		t.Fatalf("unexpected CheckUpdates errors: %v", sum.Errors)
	}
	if sum.CheckedCount != 4 {
		t.Fatalf("CheckedCount = %d, want 4 (2 managed wasm + 2 managed sd)", sum.CheckedCount)
	}
	if len(sum.Updates) != 2 {
		t.Fatalf("expected 2 updates, got %d: %+v", len(sum.Updates), sum.Updates)
	}

	var oaUpd, twitchUpd PluginUpdate
	for _, u := range sum.Updates {
		switch u.UUID {
		case "com.example.oa.update":
			oaUpd = u
		case "com.github.mrpoundsign.poundcontrol.twitch":
			twitchUpd = u
		default:
			t.Fatalf("unexpected plugin in Updates: %+v", u)
		}
	}
	if twitchUpd.Kind != plugin.ProviderWasm || twitchUpd.Source != "curated" || twitchUpd.LatestVersion != "0.2.0" || twitchUpd.ReleaseNotes != "- Added Raid action" {
		t.Fatalf("unexpected Twitch update entry: %+v", twitchUpd)
	}
	if oaUpd.Kind != plugin.ProviderStreamDeck || oaUpd.Source != "openaction" || oaUpd.LatestVersion != "1.5.0" {
		t.Fatalf("unexpected OpenAction update entry: %+v", oaUpd)
	}

	// Applying Twitch update invokes InstallFromGitHubRelease and clears Twitch from LatestSummary.
	if err := mgr.ApplyUpdate(context.Background(), twitchUpd, nil); err != nil {
		t.Fatalf("ApplyUpdate(twitch) error = %v", err)
	}
	if len(wasmMgr.installCalls) != 1 || wasmMgr.installCalls[0] != "mrpoundsign/PoundControl-Twitch:poundcontrol-twitch.wasm" {
		t.Fatalf("unexpected wasm install calls: %v", wasmMgr.installCalls)
	}
	if got := len(mgr.LatestSummary().Updates); got != 1 {
		t.Fatalf("expected 1 remaining update after applying twitch, got %d", got)
	}

	// Applying OA update invokes InstallFromCatalogue and clears OA from LatestSummary.
	if err := mgr.ApplyUpdate(context.Background(), oaUpd, nil); err != nil {
		t.Fatalf("ApplyUpdate(oa) error = %v", err)
	}
	if len(sdMgr.installCalls) != 1 || sdMgr.installCalls[0] != "com.example.oa.update" {
		t.Fatalf("unexpected sd install calls: %v", sdMgr.installCalls)
	}
	if got := len(mgr.LatestSummary().Updates); got != 0 {
		t.Fatalf("expected 0 remaining updates, got %d", got)
	}

	subMu.Lock()
	gotCalls := append([]int(nil), subCalls...)
	subMu.Unlock()
	if len(gotCalls) != 3 || gotCalls[0] != 2 || gotCalls[1] != 1 || gotCalls[2] != 0 {
		t.Fatalf("unexpected subscriber update counts: %v", gotCalls)
	}
}
