// Package pluginmgr provides the Curated Plugin Catalog client and unified plugin
// update checker/installer for both WebAssembly (.wasm) and Stream Deck (.sdPlugin) plugins.
package pluginmgr

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mrpoundsign/PoundControl/pkg/plugin"
	"github.com/mrpoundsign/PoundControl/pkg/sdplugin"
)

// DefaultCuratedCatalogURL is the live curated catalog hosted in mrpoundsign/PoundControl-Plugins.
const DefaultCuratedCatalogURL = "https://raw.githubusercontent.com/mrpoundsign/PoundControl-Plugins/main/catalog.json"

const (
	defaultGitHubAPIBase   = "https://api.github.com"
	maxCuratedCatalogBytes = 4 * 1024 * 1024
	maxIconBytes           = 2 * 1024 * 1024
	curatedCacheTTL        = 5 * time.Minute
)

//go:embed curated_fallback.json
var curatedFallbackJSON []byte

// CuratedEntry describes a verified plugin listed in the PoundControl Curated Plugin Catalog.
type CuratedEntry struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	Kind          plugin.ProviderKind `json:"kind"`
	Author        string              `json:"author,omitempty"`
	Category      string              `json:"category,omitempty"`
	Description   string              `json:"description,omitempty"`
	Repository    string              `json:"repository,omitempty"`
	AssetName     string              `json:"assetName,omitempty"`
	DownloadURL   string              `json:"download_url,omitempty"`
	SHA256        string              `json:"sha256,omitempty"`
	IconURL       string              `json:"icon,omitempty"`
	AllowedHosts  []string            `json:"allowedHosts,omitempty"`
	Featured      bool                `json:"featured,omitempty"`
	MinAppVersion string              `json:"minAppVersion,omitempty"`
}

// ToSDCatalogueEntry converts a CuratedEntry into an OpenAction-compatible CatalogueEntry.
func (e CuratedEntry) ToSDCatalogueEntry() sdplugin.CatalogueEntry {
	return sdplugin.CatalogueEntry{
		ID:          e.ID,
		Name:        e.Name,
		Author:      e.Author,
		Description: e.Description,
		Category:    e.Category,
		Repository:  e.Repository,
		DownloadURL: e.DownloadURL,
		SHA256:      e.SHA256,
		IconURL:     e.IconURL,
	}
}

type cachedIcon struct {
	data        []byte
	contentType string
}

// CuratedClient fetches, caches, and resolves metadata/icons for curated PoundControl plugins.
type CuratedClient struct {
	CatalogURL       string
	GitHubAPIBaseURL string
	HTTPClient       *http.Client

	mu           sync.Mutex
	cached       []CuratedEntry
	fetchedAt    time.Time
	iconCache    map[string]cachedIcon
	detailsCache map[string]sdplugin.PluginDetails
}

// NewCuratedClient constructs a CuratedClient targeting the official PoundControl-Plugins catalog.
func NewCuratedClient() *CuratedClient {
	return &CuratedClient{
		CatalogURL:       DefaultCuratedCatalogURL,
		GitHubAPIBaseURL: defaultGitHubAPIBase,
		HTTPClient:       &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *CuratedClient) httpClient() *http.Client {
	if c != nil && c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (c *CuratedClient) catalogURL() string {
	if c != nil && strings.TrimSpace(c.CatalogURL) != "" {
		return strings.TrimSpace(c.CatalogURL)
	}
	return DefaultCuratedCatalogURL
}

func (c *CuratedClient) githubAPIBase() string {
	if c != nil && strings.TrimSpace(c.GitHubAPIBaseURL) != "" {
		return strings.TrimRight(strings.TrimSpace(c.GitHubAPIBaseURL), "/")
	}
	return defaultGitHubAPIBase
}

// Fetch retrieves and parses the curated catalog. If the remote catalog cannot be reached or parsed,
// it falls back to the embedded snapshot and returns fromFallback=true.
func (c *CuratedClient) Fetch(ctx context.Context, forceRefresh bool) (entries []CuratedEntry, fromFallback bool, err error) {
	c.mu.Lock()
	if !forceRefresh && len(c.cached) > 0 && time.Since(c.fetchedAt) < curatedCacheTTL {
		out := cloneCuratedEntries(c.cached)
		c.mu.Unlock()
		return out, false, nil
	}
	c.mu.Unlock()

	if remote, remErr := c.fetchRemote(ctx); remErr == nil {
		c.mu.Lock()
		c.cached = cloneCuratedEntries(remote)
		c.fetchedAt = time.Now()
		c.mu.Unlock()
		return remote, false, nil
	}

	fallback, fbErr := parseCuratedCatalogJSON(curatedFallbackJSON)
	if fbErr != nil {
		return nil, true, fmt.Errorf("parse embedded fallback catalog: %w", fbErr)
	}
	return fallback, true, nil
}

func (c *CuratedClient) fetchRemote(ctx context.Context) ([]CuratedEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.catalogURL(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "PoundControl-Plugin-Client")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("curated catalog returned HTTP %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxCuratedCatalogBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxCuratedCatalogBytes {
		return nil, errors.New("curated catalog exceeds size limit")
	}
	return parseCuratedCatalogJSON(raw)
}

// FetchIcon downloads and caches the plugin icon bytes and content type (PNG, JPEG, or SVG).
func (c *CuratedClient) FetchIcon(ctx context.Context, entry CuratedEntry) ([]byte, string, error) {
	iconURL := strings.TrimSpace(entry.IconURL)
	if iconURL == "" {
		return nil, "", errors.New("curated plugin has no icon URL")
	}
	parsed, err := url.Parse(iconURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return nil, "", fmt.Errorf("unsupported icon URL scheme %q", iconURL)
	}

	c.mu.Lock()
	if cached, ok := c.iconCache[iconURL]; ok {
		c.mu.Unlock()
		return slices.Clone(cached.data), cached.contentType, nil
	}
	c.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, iconURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "PoundControl-Plugin-Client")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("icon returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxIconBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxIconBytes {
		return nil, "", errors.New("icon exceeds 2 MiB limit")
	}
	ctype := strings.TrimSpace(resp.Header.Get("Content-Type"))

	c.mu.Lock()
	if c.iconCache == nil {
		c.iconCache = make(map[string]cachedIcon)
	}
	c.iconCache[iconURL] = cachedIcon{
		data:        slices.Clone(data),
		contentType: ctype,
	}
	c.mu.Unlock()
	return data, ctype, nil
}

// FetchPluginDetails loads GitHub repository metadata, latest release notes, and rendered README HTML.
func (c *CuratedClient) FetchPluginDetails(ctx context.Context, entry CuratedEntry) sdplugin.PluginDetails {
	key := strings.ToLower(strings.TrimSpace(entry.ID))
	c.mu.Lock()
	if cached, ok := c.detailsCache[key]; ok {
		c.mu.Unlock()
		return cached
	}
	c.mu.Unlock()

	oa := sdplugin.NewCatalogueClient()
	oa.GitHubAPIBaseURL = c.githubAPIBase()
	oa.HTTPClient = c.httpClient()
	details := oa.FetchPluginDetails(ctx, entry.ToSDCatalogueEntry())

	c.mu.Lock()
	if c.detailsCache == nil {
		c.detailsCache = make(map[string]sdplugin.PluginDetails)
	}
	c.detailsCache[key] = details
	c.mu.Unlock()
	return details
}

// FilterCurated filters curated entries case-insensitively by query across Name, ID, Author, Category, Description, and Kind.
func FilterCurated(entries []CuratedEntry, query string) []CuratedEntry {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return cloneCuratedEntries(entries)
	}
	out := make([]CuratedEntry, 0, len(entries))
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Name), q) ||
			strings.Contains(strings.ToLower(e.ID), q) ||
			strings.Contains(strings.ToLower(e.Author), q) ||
			strings.Contains(strings.ToLower(e.Category), q) ||
			strings.Contains(strings.ToLower(e.Description), q) ||
			strings.Contains(strings.ToLower(string(e.Kind)), q) {
			out = append(out, e)
		}
	}
	return out
}

// ParseGitHubRepo extracts owner and repository name from a github.com URL.
func ParseGitHubRepo(rawURL string) (owner, repo string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !strings.EqualFold(u.Host, "github.com") {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), true
}

func parseCuratedCatalogJSON(raw []byte) ([]CuratedEntry, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, errors.New("empty curated catalog JSON")
	}

	var rawEntries []CuratedEntry
	switch trimmed[0] {
	case '[':
		if err := json.Unmarshal(raw, &rawEntries); err != nil {
			return nil, fmt.Errorf("parse curated catalog array: %w", err)
		}
	case '{':
		var doc struct {
			Version int            `json:"version"`
			Plugins []CuratedEntry `json:"plugins"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("parse curated catalog document: %w", err)
		}
		rawEntries = doc.Plugins
	default:
		return nil, errors.New("unexpected curated catalog JSON format")
	}

	seen := make(map[string]struct{}, len(rawEntries))
	cleaned := make([]CuratedEntry, 0, len(rawEntries))
	for _, e := range rawEntries {
		e.ID = strings.TrimSpace(e.ID)
		e.Name = strings.TrimSpace(e.Name)
		e.Kind = plugin.ProviderKind(strings.ToLower(strings.TrimSpace(string(e.Kind))))
		e.Author = strings.TrimSpace(e.Author)
		e.Category = strings.TrimSpace(e.Category)
		e.Description = strings.TrimSpace(e.Description)
		e.Repository = strings.TrimSpace(e.Repository)
		e.AssetName = strings.TrimSpace(e.AssetName)
		e.DownloadURL = strings.TrimSpace(e.DownloadURL)
		e.SHA256 = strings.TrimSpace(e.SHA256)
		e.IconURL = strings.TrimSpace(e.IconURL)
		e.MinAppVersion = strings.TrimSpace(e.MinAppVersion)

		if e.ID == "" {
			continue
		}
		lowerID := strings.ToLower(e.ID)
		if _, dup := seen[lowerID]; dup {
			continue
		}
		seen[lowerID] = struct{}{}

		if e.Name == "" {
			e.Name = e.ID
		}
		if e.Kind != plugin.ProviderWasm && e.Kind != plugin.ProviderStreamDeck {
			continue
		}
		cleaned = append(cleaned, e)
	}

	slices.SortFunc(cleaned, func(a, b CuratedEntry) int {
		if a.Featured != b.Featured {
			if a.Featured {
				return -1
			}
			return 1
		}
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return cleaned, nil
}

func cloneCuratedEntries(src []CuratedEntry) []CuratedEntry {
	out := make([]CuratedEntry, len(src))
	for i, e := range src {
		e.AllowedHosts = slices.Clone(e.AllowedHosts)
		out[i] = e
	}
	return out
}
