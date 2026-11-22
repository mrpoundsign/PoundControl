package sdplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// DefaultOpenActionCatalogueURL is the official OpenAction Marketplace plugin index.
const DefaultOpenActionCatalogueURL = "https://openactionapi.github.io/plugins/catalogue.json"

const (
	defaultGitHubAPIBase = "https://api.github.com"
	maxCatalogueBytes    = 8 * 1024 * 1024
	catalogueCacheTTL    = 5 * time.Minute
)

// CatalogueEntry describes a single installable plugin from the OpenAction Marketplace catalogue.
type CatalogueEntry struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Author      string          `json:"author,omitempty"`
	Description string          `json:"description,omitempty"`
	Version     string          `json:"version,omitempty"`
	Category    string          `json:"category,omitempty"`
	Repository  string          `json:"repository,omitempty"`
	DownloadURL string          `json:"download_url,omitempty"`
	SHA256      string          `json:"sha256,omitempty"`
	IconURL     string          `json:"icon,omitempty"`
	OS          []string        `json:"os,omitempty"`
	Flags       map[string]bool `json:"flags,omitempty"`
}

// IsAIAssisted reports whether the catalogue entry carries the ai_assisted flag.
func (e CatalogueEntry) IsAIAssisted() bool {
	return e.Flags["ai_assisted"]
}

// PluginDetails holds extended metadata, release notes, and rendered README HTML for a Marketplace plugin.
type PluginDetails struct {
	Entry         CatalogueEntry
	LatestVersion string
	ReleaseNotes  string
	Stars         int
	License       string
	Homepage      string
	DefaultBranch string
	ReadmeHTML    string
}

// CatalogueClient fetches, caches, and resolves downloadable bundles from the OpenAction Marketplace.
type CatalogueClient struct {
	CatalogueURL     string
	GitHubAPIBaseURL string
	HTTPClient       *http.Client

	mu        sync.Mutex
	cached    []CatalogueEntry
	fetchedAt time.Time
	iconCache map[string][]byte
	details   map[string]PluginDetails
}

// NewCatalogueClient constructs a CatalogueClient pointed at the official OpenAction Marketplace index.
func NewCatalogueClient() *CatalogueClient {
	return &CatalogueClient{
		CatalogueURL:     DefaultOpenActionCatalogueURL,
		GitHubAPIBaseURL: defaultGitHubAPIBase,
		HTTPClient: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

func (c *CatalogueClient) httpClient() *http.Client {
	if c != nil && c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (c *CatalogueClient) catalogueURL() string {
	if c != nil && strings.TrimSpace(c.CatalogueURL) != "" {
		return strings.TrimSpace(c.CatalogueURL)
	}
	return DefaultOpenActionCatalogueURL
}

func (c *CatalogueClient) githubAPIBase() string {
	if c != nil && strings.TrimSpace(c.GitHubAPIBaseURL) != "" {
		return strings.TrimRight(strings.TrimSpace(c.GitHubAPIBaseURL), "/")
	}
	return defaultGitHubAPIBase
}

// Fetch retrieves and parses the OpenAction Marketplace catalogue, returning a cached copy
// unless forceRefresh is true or the cache TTL has expired.
func (c *CatalogueClient) Fetch(ctx context.Context, forceRefresh bool) ([]CatalogueEntry, error) {
	c.mu.Lock()
	if !forceRefresh && len(c.cached) > 0 && time.Since(c.fetchedAt) < catalogueCacheTTL {
		out := slices.Clone(c.cached)
		c.mu.Unlock()
		return out, nil
	}
	c.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.catalogueURL(), nil)
	if err != nil {
		return nil, fmt.Errorf("create catalogue request: %w", err)
	}
	req.Header.Set("User-Agent", "PoundControl-OpenAction-Client")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch OpenAction catalogue: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenAction catalogue returned HTTP %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogueBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read OpenAction catalogue: %w", err)
	}
	if len(raw) > maxCatalogueBytes {
		return nil, errors.New("OpenAction catalogue exceeds maximum size limit")
	}

	entries, err := parseCatalogueJSON(raw, c.catalogueURL())
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.cached = slices.Clone(entries)
	c.fetchedAt = time.Now()
	c.mu.Unlock()
	return entries, nil
}

// FetchIcon downloads and caches the PNG icon for entry (up to 2 MiB).
func (c *CatalogueClient) FetchIcon(ctx context.Context, entry CatalogueEntry) ([]byte, error) {
	iconURL := strings.TrimSpace(entry.IconURL)
	if iconURL == "" {
		return nil, errors.New("plugin has no icon URL")
	}
	c.mu.Lock()
	if data, ok := c.iconCache[iconURL]; ok {
		c.mu.Unlock()
		return slices.Clone(data), nil
	}
	c.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, iconURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "PoundControl-OpenAction-Client")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("icon returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	if c.iconCache == nil {
		c.iconCache = make(map[string][]byte)
	}
	c.iconCache[iconURL] = slices.Clone(data)
	c.mu.Unlock()
	return data, nil
}

// FetchPluginDetails retrieves cached GitHub repository metadata, latest release notes, and rendered README HTML
// (including inline screenshots) for entry. Network or rate-limit errors degrade gracefully.
func (c *CatalogueClient) FetchPluginDetails(ctx context.Context, entry CatalogueEntry) PluginDetails {
	key := strings.ToLower(strings.TrimSpace(entry.ID))
	c.mu.Lock()
	if cached, ok := c.details[key]; ok {
		c.mu.Unlock()
		return cached
	}
	c.mu.Unlock()

	details := PluginDetails{
		Entry:         entry,
		LatestVersion: entry.Version,
		DefaultBranch: "main",
	}

	owner, repo, ok := parseGitHubRepo(entry.Repository)
	if !ok {
		return details
	}

	base := c.githubAPIBase()
	escOwner := url.PathEscape(owner)
	escRepo := url.PathEscape(repo)

	// 1. Repository metadata (stars, license, default branch, homepage).
	if req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/repos/%s/%s", base, escOwner, escRepo), nil); err == nil {
		req.Header.Set("User-Agent", "PoundControl-OpenAction-Client")
		req.Header.Set("Accept", "application/vnd.github+json")
		if resp, doErr := c.httpClient().Do(req); doErr == nil {
			if resp.StatusCode == http.StatusOK {
				var repoInfo struct {
					StargazersCount int    `json:"stargazers_count"`
					DefaultBranch   string `json:"default_branch"`
					Homepage        string `json:"homepage"`
					License         *struct {
						SPDXID string `json:"spdx_id"`
						Name   string `json:"name"`
					} `json:"license"`
				}
				if decErr := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&repoInfo); decErr == nil {
					details.Stars = repoInfo.StargazersCount
					if strings.TrimSpace(repoInfo.DefaultBranch) != "" {
						details.DefaultBranch = strings.TrimSpace(repoInfo.DefaultBranch)
					}
					details.Homepage = strings.TrimSpace(repoInfo.Homepage)
					if repoInfo.License != nil {
						if repoInfo.License.SPDXID != "" && repoInfo.License.SPDXID != "NOASSERTION" {
							details.License = repoInfo.License.SPDXID
						} else if repoInfo.License.Name != "" {
							details.License = repoInfo.License.Name
						}
					}
				}
			}
			_ = resp.Body.Close()
		}
	}

	// 2. Latest release tag & notes.
	if req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/repos/%s/%s/releases/latest", base, escOwner, escRepo), nil); err == nil {
		req.Header.Set("User-Agent", "PoundControl-OpenAction-Client")
		req.Header.Set("Accept", "application/vnd.github+json")
		if resp, doErr := c.httpClient().Do(req); doErr == nil {
			if resp.StatusCode == http.StatusOK {
				var rel struct {
					TagName string `json:"tag_name"`
					Body    string `json:"body"`
				}
				if decErr := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&rel); decErr == nil {
					if strings.TrimSpace(rel.TagName) != "" {
						details.LatestVersion = strings.TrimPrefix(strings.TrimSpace(rel.TagName), "v")
					}
					details.ReleaseNotes = strings.TrimSpace(rel.Body)
				}
			}
			_ = resp.Body.Close()
		}
	}

	// 3. Rendered GitHub README HTML (includes screenshots, GIFs, tables, and formatting).
	if req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/repos/%s/%s/readme", base, escOwner, escRepo), nil); err == nil {
		req.Header.Set("User-Agent", "PoundControl-OpenAction-Client")
		req.Header.Set("Accept", "application/vnd.github.html+json")
		if resp, doErr := c.httpClient().Do(req); doErr == nil {
			if resp.StatusCode == http.StatusOK {
				if rawHTML, readErr := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024)); readErr == nil {
					details.ReadmeHTML = sanitizeAndRewriteReadmeHTML(string(rawHTML), owner, repo, details.DefaultBranch)
				}
			}
			_ = resp.Body.Close()
		}
	}

	c.mu.Lock()
	if c.details == nil {
		c.details = make(map[string]PluginDetails)
	}
	c.details[key] = details
	c.mu.Unlock()
	return details
}

// ResolveDownload returns the downloadable archive URL and optional SHA-256 hex digest for entry.
// If entry.DownloadURL is empty and entry.Repository points to a GitHub repository, it queries the
// repository's latest release for a .streamDeckPlugin or .zip asset.
func (c *CatalogueClient) ResolveDownload(ctx context.Context, entry CatalogueEntry) (downloadURL, sha256Hex string, err error) {
	if u := strings.TrimSpace(entry.DownloadURL); u != "" {
		return u, strings.TrimSpace(entry.SHA256), nil
	}
	owner, repo, ok := parseGitHubRepo(entry.Repository)
	if !ok {
		return "", "", fmt.Errorf("catalogue entry %q has no download_url or GitHub repository", entry.ID)
	}

	apiURL := fmt.Sprintf("%s/repos/%s/%s/releases/latest", c.githubAPIBase(), url.PathEscape(owner), url.PathEscape(repo))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("create GitHub release request: %w", err)
	}
	req.Header.Set("User-Agent", "PoundControl-OpenAction-Client")
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", "", fmt.Errorf("fetch latest release for %s/%s: %w", owner, repo, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("GitHub latest release for %s/%s returned HTTP %d", owner, repo, resp.StatusCode)
	}

	var release struct {
		Assets []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
			Digest             string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024)).Decode(&release); err != nil {
		return "", "", fmt.Errorf("decode GitHub release for %s/%s: %w", owner, repo, err)
	}

	var zipFallbackURL, zipFallbackSHA string
	for _, a := range release.Assets {
		lower := strings.ToLower(strings.TrimSpace(a.Name))
		dl := strings.TrimSpace(a.BrowserDownloadURL)
		if dl == "" {
			continue
		}
		sha := strings.TrimPrefix(strings.TrimSpace(a.Digest), "sha256:")
		if strings.HasSuffix(lower, ".streamdeckplugin") {
			return dl, sha, nil
		}
		if zipFallbackURL == "" && strings.HasSuffix(lower, ".zip") {
			zipFallbackURL = dl
			zipFallbackSHA = sha
		}
	}
	if zipFallbackURL != "" {
		return zipFallbackURL, zipFallbackSHA, nil
	}
	return "", "", fmt.Errorf("no .streamDeckPlugin or .zip release asset found for %s/%s", owner, repo)
}

// FilterCatalogue filters catalogue entries case-insensitively across ID, Name, Author, Description, and Category.
func FilterCatalogue(entries []CatalogueEntry, query string) []CatalogueEntry {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return slices.Clone(entries)
	}
	out := make([]CatalogueEntry, 0, len(entries))
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Name), q) ||
			strings.Contains(strings.ToLower(e.ID), q) ||
			strings.Contains(strings.ToLower(e.Author), q) ||
			strings.Contains(strings.ToLower(e.Description), q) ||
			strings.Contains(strings.ToLower(e.Category), q) {
			out = append(out, e)
		}
	}
	return out
}

func parseCatalogueJSON(raw []byte, catalogueURL string) ([]CatalogueEntry, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, errors.New("empty OpenAction catalogue response")
	}

	var entries []CatalogueEntry
	switch trimmed[0] {
	case '[':
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, fmt.Errorf("parse OpenAction catalogue array: %w", err)
		}
	case '{':
		// Support either {"plugins": [...]} or OpenAction's map[pluginID]entry format.
		var wrapped struct {
			Plugins []CatalogueEntry `json:"plugins"`
		}
		if err := json.Unmarshal(raw, &wrapped); err == nil && len(wrapped.Plugins) > 0 {
			entries = wrapped.Plugins
		} else {
			var byID map[string]CatalogueEntry
			if err := json.Unmarshal(raw, &byID); err != nil {
				return nil, fmt.Errorf("parse OpenAction catalogue object: %w", err)
			}
			entries = make([]CatalogueEntry, 0, len(byID))
			for id, item := range byID {
				if strings.TrimSpace(item.ID) == "" {
					item.ID = strings.TrimSpace(id)
				}
				entries = append(entries, item)
			}
		}
	default:
		return nil, errors.New("unexpected OpenAction catalogue JSON format")
	}

	iconBase := deriveIconBaseURL(catalogueURL)

	cleaned := make([]CatalogueEntry, 0, len(entries))
	for _, e := range entries {
		e.ID = strings.TrimSpace(e.ID)
		e.Name = strings.TrimSpace(e.Name)
		e.Author = strings.TrimSpace(e.Author)
		e.Description = strings.TrimSpace(e.Description)
		e.Version = strings.TrimSpace(e.Version)
		e.Category = strings.TrimSpace(e.Category)
		e.Repository = strings.TrimSpace(e.Repository)
		e.DownloadURL = strings.TrimSpace(e.DownloadURL)
		e.SHA256 = strings.TrimSpace(e.SHA256)
		e.IconURL = strings.TrimSpace(e.IconURL)
		if e.ID == "" {
			continue
		}
		if e.Name == "" {
			e.Name = e.ID
		}
		if e.IconURL == "" && iconBase != "" {
			e.IconURL = iconBase + "/" + url.PathEscape(e.ID) + ".png"
		}
		cleaned = append(cleaned, e)
	}

	slices.SortFunc(cleaned, func(a, b CatalogueEntry) int {
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return cleaned, nil
}

func deriveIconBaseURL(catalogueURL string) string {
	u, err := url.Parse(strings.TrimSpace(catalogueURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	dir := path.Dir(u.Path)
	if dir == "." || dir == "/" {
		dir = ""
	}
	u.Path = strings.TrimRight(dir, "/") + "/icons"
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}

var (
	reScriptTag = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	reImgSrc    = regexp.MustCompile(`(?i)(\bsrc\s*=\s*")([^"]+)(")`)
	reLinkHref  = regexp.MustCompile(`(?i)(\bhref\s*=\s*")([^"]+)(")`)
)

func sanitizeAndRewriteReadmeHTML(rawHTML, owner, repo, branch string) string {
	cleaned := reScriptTag.ReplaceAllString(rawHTML, "")
	if branch == "" {
		branch = "main"
	}
	rawBase := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(branch))
	blobBase := fmt.Sprintf("https://github.com/%s/%s/blob/%s/", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(branch))

	cleaned = reImgSrc.ReplaceAllStringFunc(cleaned, func(match string) string {
		parts := reImgSrc.FindStringSubmatch(match)
		if len(parts) != 4 {
			return match
		}
		src := strings.TrimSpace(parts[2])
		if isAbsoluteOrDataURL(src) || strings.HasPrefix(src, "#") {
			return match
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(src, "./"), "/")
		return parts[1] + rawBase + rel + parts[3]
	})

	cleaned = reLinkHref.ReplaceAllStringFunc(cleaned, func(match string) string {
		parts := reLinkHref.FindStringSubmatch(match)
		if len(parts) != 4 {
			return match
		}
		href := strings.TrimSpace(parts[2])
		if strings.HasPrefix(strings.ToLower(href), "javascript:") {
			return parts[1] + "#" + parts[3]
		}
		if isAbsoluteOrDataURL(href) || strings.HasPrefix(href, "#") {
			return match
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(href, "./"), "/")
		return parts[1] + blobBase + rel + parts[3]
	})
	return cleaned
}

func isAbsoluteOrDataURL(raw string) bool {
	lower := strings.ToLower(strings.TrimSpace(raw))
	return strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "//") ||
		strings.HasPrefix(lower, "data:") ||
		strings.HasPrefix(lower, "mailto:")
}

// RenderPluginDetailsHTML generates a self-contained Obsidian Slate HTML detail page showing the plugin's
// icon, metadata, release notes, and rendered GitHub README (including inline screenshots and badges).
func RenderPluginDetailsHTML(d PluginDetails) string {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">`)
	b.WriteString(`<title>` + html.EscapeString(d.Entry.Name) + ` — OpenAction Plugin Info</title>`)
	b.WriteString(`<style>
		:root { color-scheme: dark; }
		body { margin: 0; padding: 20px 24px 32px; background: #0A0A0C; color: #E2E8F0; font-family: "Segoe UI", system-ui, -apple-system, sans-serif; line-height: 1.55; }
		a { color: #38BDF8; text-decoration: none; }
		a:hover { text-decoration: underline; }
		.hero { display: flex; gap: 16px; align-items: center; background: #141416; border: 1px solid #1E293B; border-radius: 12px; padding: 16px; margin-bottom: 18px; }
		.hero img.icon { width: 64px; height: 64px; border-radius: 12px; object-fit: contain; background: #1E293B; flex-shrink: 0; }
		.hero h1 { margin: 0 0 4px; font-size: 20px; color: #FFFFFF; }
		.meta { font-size: 13px; color: #94A3B8; display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
		.pill { display: inline-block; background: #1E293B; color: #E2E8F0; border: 1px solid #334155; border-radius: 999px; padding: 2px 10px; font-size: 12px; }
		.pill.accent { border-color: #38BDF8; color: #38BDF8; }
		.desc { margin: 10px 0 0; font-size: 14px; color: #CBD5E1; }
		.links { margin-top: 10px; display: flex; gap: 12px; flex-wrap: wrap; font-size: 13px; }
		.card { background: #141416; border: 1px solid #1E293B; border-radius: 12px; padding: 16px 20px; margin-bottom: 18px; overflow-x: auto; }
		.card h2.section { margin: 0 0 10px; font-size: 14px; text-transform: uppercase; letter-spacing: 0.05em; color: #38BDF8; }
		.readme img { max-width: 100%; height: auto; border-radius: 8px; }
		.readme pre { background: #0A0A0C; border: 1px solid #1E293B; border-radius: 8px; padding: 12px; overflow-x: auto; }
		.readme code { font-family: ui-monospace, Consolas, monospace; font-size: 13px; background: #1E293B; padding: 1px 5px; border-radius: 4px; }
		.readme pre code { background: transparent; padding: 0; }
		.readme table { border-collapse: collapse; width: 100%; margin: 12px 0; }
		.readme th, .readme td { border: 1px solid #1E293B; padding: 6px 10px; text-align: left; }
		.readme th { background: #1E293B; }
		.readme h1, .readme h2, .readme h3 { color: #FFFFFF; border-bottom: 1px solid #1E293B; padding-bottom: 4px; }
		.notes { white-space: pre-wrap; font-size: 13px; color: #CBD5E1; margin: 0; }
	</style></head><body>`)

	b.WriteString(`<div class="hero">`)
	if d.Entry.IconURL != "" {
		b.WriteString(`<img class="icon" src="` + html.EscapeString(d.Entry.IconURL) + `" alt="">`)
	}
	b.WriteString(`<div><h1>` + html.EscapeString(d.Entry.Name) + `</h1><div class="meta">`)
	if d.Entry.Author != "" {
		b.WriteString(`<span>by ` + html.EscapeString(d.Entry.Author) + `</span>`)
	}
	if d.LatestVersion != "" {
		b.WriteString(`<span class="pill accent">v` + html.EscapeString(d.LatestVersion) + `</span>`)
	}
	if d.Stars > 0 {
		fmt.Fprintf(&b, `<span class="pill">★ %d</span>`, d.Stars)
	}
	if d.License != "" {
		b.WriteString(`<span class="pill">` + html.EscapeString(d.License) + `</span>`)
	}
	if d.Entry.IsAIAssisted() {
		b.WriteString(`<span class="pill">AI-Assisted</span>`)
	}
	b.WriteString(`</div>`)
	if d.Entry.Description != "" {
		b.WriteString(`<p class="desc">` + html.EscapeString(d.Entry.Description) + `</p>`)
	}
	b.WriteString(`<div class="links">`)
	if d.Entry.Repository != "" {
		b.WriteString(`<a href="` + html.EscapeString(d.Entry.Repository) + `" target="_blank" rel="noopener">GitHub Repository ↗</a>`)
	}
	if d.Homepage != "" && d.Homepage != d.Entry.Repository {
		b.WriteString(`<a href="` + html.EscapeString(d.Homepage) + `" target="_blank" rel="noopener">Homepage ↗</a>`)
	}
	b.WriteString(`</div></div></div>`)

	if d.ReleaseNotes != "" {
		b.WriteString(`<div class="card"><h2 class="section">Latest Release Notes</h2><pre class="notes">` + html.EscapeString(d.ReleaseNotes) + `</pre></div>`)
	}

	b.WriteString(`<div class="card readme"><h2 class="section">README &amp; Screenshots</h2>`)
	if strings.TrimSpace(d.ReadmeHTML) != "" {
		b.WriteString(d.ReadmeHTML)
	} else {
		b.WriteString(`<p>Full README could not be loaded from GitHub (offline or API rate limit reached). Use the GitHub Repository link above to view documentation and screenshots.</p>`)
	}
	b.WriteString(`</div></body></html>`)
	return b.String()
}

func parseGitHubRepo(rawURL string) (owner, repo string, ok bool) {
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
