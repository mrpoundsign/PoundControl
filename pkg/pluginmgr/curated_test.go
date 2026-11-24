package pluginmgr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mrpoundsign/PoundControl/pkg/plugin"
)

func TestCuratedClientFetchCacheAndFallback(t *testing.T) {
	var (
		catalogHits atomic.Int32
		failMode    atomic.Bool
	)
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/catalog.json":
			catalogHits.Add(1)
			if failMode.Load() {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": 1,
				"plugins": []map[string]any{
					{
						"id":          "com.example.zeta",
						"name":        "Zeta Tool",
						"kind":        "sdplugin",
						"author":      "DevZ",
						"category":    "Utilities",
						"description": "Utility tool",
						"repository":  "https://github.com/example/zeta",
						"featured":    false,
					},
					{
						"id":           "com.github.mrpoundsign.poundcontrol.twitch",
						"name":         "Twitch",
						"kind":         "wasm",
						"author":       "PoundControl",
						"category":     "Streaming",
						"description":  "Twitch controls",
						"repository":   "https://github.com/mrpoundsign/PoundControl-Twitch",
						"assetName":    "poundcontrol-twitch.wasm",
						"icon":         srvURL + "/logo.svg",
						"allowedHosts": []string{"id.twitch.tv", "api.twitch.tv"},
						"featured":     true,
					},
					{
						"id":   "com.example.invalidkind",
						"name": "Bad Kind",
						"kind": "python",
					},
					{
						"id":   "COM.EXAMPLE.ZETA",
						"name": "Duplicate Zeta",
						"kind": "wasm",
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL

	client := &CuratedClient{
		CatalogURL: srv.URL + "/catalog.json",
		HTTPClient: srv.Client(),
	}

	entries, fromFallback, err := client.Fetch(context.Background(), false)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if fromFallback {
		t.Fatal("expected remote fetch, got fallback")
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 valid unique entries, got %d (%+v)", len(entries), entries)
	}
	// Featured entry sorts first.
	if entries[0].ID != "com.github.mrpoundsign.poundcontrol.twitch" || !entries[0].Featured {
		t.Fatalf("expected featured Twitch entry first, got %+v", entries)
	}

	// Second call within TTL uses cache.
	_, _, err = client.Fetch(context.Background(), false)
	if err != nil {
		t.Fatalf("cached Fetch() error = %v", err)
	}
	if hits := catalogHits.Load(); hits != 1 {
		t.Fatalf("expected 1 remote hit due to TTL cache, got %d", hits)
	}

	// Force refresh with failing upstream falls back to embedded curated_fallback.json.
	failMode.Store(true)
	fbEntries, fbUsed, err := client.Fetch(context.Background(), true)
	if err != nil {
		t.Fatalf("fallback Fetch() error = %v", err)
	}
	if !fbUsed {
		t.Fatal("expected fromFallback=true when remote fails")
	}
	if len(fbEntries) == 0 || fbEntries[0].ID != "com.github.mrpoundsign.poundcontrol.twitch" {
		t.Fatalf("expected embedded Twitch fallback entry, got %+v", fbEntries)
	}
}

func TestCuratedClientFetchIconDetailsAndFilter(t *testing.T) {
	var iconHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/logo.svg":
			iconHits.Add(1)
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><rect width="24" height="24"/></svg>`))
		case "/repos/mrpoundsign/PoundControl-Twitch":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"stargazers_count": 42,
				"default_branch":   "main",
				"license":          map[string]any{"spdx_id": "MIT"},
			})
		case "/repos/mrpoundsign/PoundControl-Twitch/releases/latest":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v0.1.0",
				"body":     "Initial release",
			})
		case "/repos/mrpoundsign/PoundControl-Twitch/readme":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<p>Twitch README</p>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := &CuratedClient{
		GitHubAPIBaseURL: srv.URL,
		HTTPClient:       srv.Client(),
	}

	entry := CuratedEntry{
		ID:          "com.github.mrpoundsign.poundcontrol.twitch",
		Name:        "Twitch",
		Kind:        plugin.ProviderWasm,
		Author:      "PoundControl",
		Category:    "Streaming",
		Description: "Twitch controls",
		Repository:  "https://github.com/mrpoundsign/PoundControl-Twitch",
		IconURL:     srv.URL + "/logo.svg",
	}

	raw, ctype, err := client.FetchIcon(context.Background(), entry)
	if err != nil {
		t.Fatalf("FetchIcon() error = %v", err)
	}
	if !strings.Contains(ctype, "svg") || !strings.Contains(string(raw), "<svg") {
		t.Fatalf("unexpected icon response: ctype=%q body=%q", ctype, string(raw))
	}
	// Cached hit
	if _, _, err := client.FetchIcon(context.Background(), entry); err != nil {
		t.Fatalf("cached FetchIcon() error = %v", err)
	}
	if iconHits.Load() != 1 {
		t.Fatalf("expected 1 icon fetch due to cache, got %d", iconHits.Load())
	}

	// Reject javascript:/file: icon URLs.
	if _, _, err := client.FetchIcon(context.Background(), CuratedEntry{IconURL: "file:///etc/passwd"}); err == nil {
		t.Fatal("expected unsupported icon scheme error")
	}

	details := client.FetchPluginDetails(context.Background(), entry)
	if details.Stars != 42 || details.License != "MIT" || details.LatestVersion != "0.1.0" || !strings.Contains(details.ReadmeHTML, "Twitch README") {
		t.Fatalf("unexpected plugin details: %+v", details)
	}

	entries := []CuratedEntry{
		entry,
		{ID: "com.example.obs", Name: "OBS Helper", Kind: plugin.ProviderStreamDeck, Author: "Someone", Category: "Video"},
	}
	if got := FilterCurated(entries, "wasm"); len(got) != 1 || got[0].ID != entry.ID {
		t.Fatalf("FilterCurated('wasm') = %+v", got)
	}
	if got := FilterCurated(entries, "video"); len(got) != 1 || got[0].ID != "com.example.obs" {
		t.Fatalf("FilterCurated('video') = %+v", got)
	}
}
