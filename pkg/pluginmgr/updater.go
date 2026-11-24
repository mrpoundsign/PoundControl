package pluginmgr

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mrpoundsign/appinstaller/pkg/updater"

	"github.com/mrpoundsign/PoundControl/pkg/plugin"
	"github.com/mrpoundsign/PoundControl/pkg/sdplugin"
	"github.com/mrpoundsign/PoundControl/pkg/wasmplugin"
)

const maxConcurrentReleaseChecks = 4

// WasmManager abstracts the subset of wasmplugin.Provider needed by Manager.
type WasmManager interface {
	Plugins() []wasmplugin.PluginStatus
	IsUserManagedPlugin(pluginPath string) bool
	InstalledMetadata() map[string]wasmplugin.InstallRecord
	InstallFromGitHubRelease(
		ctx context.Context,
		owner, repo, assetName, apiBaseURL string,
		httpClient *http.Client,
		onProgress func(downloaded, total int64, stage string),
	) (wasmplugin.PluginStatus, error)
}

// SDManager abstracts the subset of sdplugin.Provider needed by Manager.
type SDManager interface {
	Plugins() []sdplugin.PluginStatus
	IsUserManagedPlugin(pluginDir string) bool
	InstallFromCatalogue(
		ctx context.Context,
		client *sdplugin.CatalogueClient,
		entry sdplugin.CatalogueEntry,
		onProgress func(downloaded, total int64, stage string),
	) (*sdplugin.LoadedBundle, error)
}

// PluginUpdate describes an available upgrade for a user-managed installed plugin.
type PluginUpdate struct {
	UUID            string                   `json:"uuid"`
	Name            string                   `json:"name"`
	Kind            plugin.ProviderKind      `json:"kind"`
	Source          string                   `json:"source"` // "curated", "github", "openaction"
	CurrentVersion  string                   `json:"currentVersion"`
	LatestVersion   string                   `json:"latestVersion"`
	ReleaseNotes    string                   `json:"releaseNotes,omitempty"`
	Repository      string                   `json:"repository,omitempty"`
	AssetName       string                   `json:"assetName,omitempty"`
	CuratedEntry    *CuratedEntry            `json:"curatedEntry,omitempty"`
	OpenActionEntry *sdplugin.CatalogueEntry `json:"openActionEntry,omitempty"`
}

// UpdateSummary captures the latest plugin update check results.
type UpdateSummary struct {
	Updates      []PluginUpdate `json:"updates,omitempty"`
	CheckedCount int            `json:"checkedCount"`
	Errors       []string       `json:"errors,omitempty"`
	CheckedAt    time.Time      `json:"checkedAt"`
}

// Manager coordinates curated plugin installation and unified plugin update checking
// across WebAssembly (.wasm) and OpenAction (.sdPlugin) plugins.
type Manager struct {
	CuratedClient    *CuratedClient
	OpenActionClient *sdplugin.CatalogueClient
	Wasm             WasmManager
	SD               SDManager
	GitHubAPIBaseURL string
	HTTPClient       *http.Client

	mu          sync.RWMutex
	lastSummary UpdateSummary
	nextSubID   int
	subs        map[int]func(UpdateSummary)
}

// NewManager creates a Manager wired to the curated catalog client, OpenAction catalog client,
// and Wasm/Stream Deck plugin providers.
func NewManager(
	curated *CuratedClient,
	oa *sdplugin.CatalogueClient,
	wasm WasmManager,
	sd SDManager,
) *Manager {
	if curated == nil {
		curated = NewCuratedClient()
	}
	if oa == nil {
		oa = sdplugin.NewCatalogueClient()
	}
	return &Manager{
		CuratedClient:    curated,
		OpenActionClient: oa,
		Wasm:             wasm,
		SD:               sd,
		subs:             make(map[int]func(UpdateSummary)),
	}
}

func (m *Manager) githubAPIBase() string {
	if strings.TrimSpace(m.GitHubAPIBaseURL) != "" {
		return strings.TrimRight(strings.TrimSpace(m.GitHubAPIBaseURL), "/")
	}
	if m.CuratedClient != nil && strings.TrimSpace(m.CuratedClient.GitHubAPIBaseURL) != "" {
		return m.CuratedClient.githubAPIBase()
	}
	return defaultGitHubAPIBase
}

func (m *Manager) httpClient() *http.Client {
	if m.HTTPClient != nil {
		return m.HTTPClient
	}
	if m.CuratedClient != nil && m.CuratedClient.HTTPClient != nil {
		return m.CuratedClient.HTTPClient
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// LatestSummary returns a copy of the most recent UpdateSummary.
func (m *Manager) LatestSummary() UpdateSummary {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneUpdateSummary(m.lastSummary)
}

// SubscribeUpdates registers a callback notified whenever update results change.
func (m *Manager) SubscribeUpdates(fn func(UpdateSummary)) func() {
	if fn == nil {
		return func() {}
	}
	m.mu.Lock()
	if m.subs == nil {
		m.subs = make(map[int]func(UpdateSummary))
	}
	id := m.nextSubID
	m.nextSubID++
	m.subs[id] = fn
	m.mu.Unlock()

	return func() {
		m.mu.Lock()
		delete(m.subs, id)
		m.mu.Unlock()
	}
}

func (m *Manager) publishSummary(sum UpdateSummary) {
	m.mu.Lock()
	m.lastSummary = cloneUpdateSummary(sum)
	callbacks := make([]func(UpdateSummary), 0, len(m.subs))
	for _, fn := range m.subs {
		callbacks = append(callbacks, fn)
	}
	m.mu.Unlock()

	for _, fn := range callbacks {
		fn(cloneUpdateSummary(sum))
	}
}

// NotifyPluginInstalled removes any pending update for uuid whose LatestVersion is <= installedVersion.
func (m *Manager) NotifyPluginInstalled(uuid, installedVersion string) {
	target := strings.ToLower(strings.TrimSpace(uuid))
	if target == "" {
		return
	}
	m.mu.Lock()
	if len(m.lastSummary.Updates) == 0 {
		m.mu.Unlock()
		return
	}
	changed := false
	filtered := make([]PluginUpdate, 0, len(m.lastSummary.Updates))
	for _, u := range m.lastSummary.Updates {
		if strings.ToLower(strings.TrimSpace(u.UUID)) == target {
			if strings.TrimSpace(installedVersion) == "" || updater.CompareVersions(u.LatestVersion, installedVersion) <= 0 {
				changed = true
				continue
			}
		}
		filtered = append(filtered, u)
	}
	if !changed {
		m.mu.Unlock()
		return
	}
	m.lastSummary.Updates = filtered
	sum := cloneUpdateSummary(m.lastSummary)
	callbacks := make([]func(UpdateSummary), 0, len(m.subs))
	for _, fn := range m.subs {
		callbacks = append(callbacks, fn)
	}
	m.mu.Unlock()

	for _, fn := range callbacks {
		fn(cloneUpdateSummary(sum))
	}
}

// NotifyPluginRemoved clears any pending update entry for an uninstalled plugin.
func (m *Manager) NotifyPluginRemoved(uuid string) {
	m.NotifyPluginInstalled(uuid, "")
}

// CheckUpdates checks all user-managed Wasm and .sdPlugin plugins for available updates.
// Externally loaded plugins outside DefaultPluginsDir are ignored.
func (m *Manager) CheckUpdates(ctx context.Context, forceRefresh bool) UpdateSummary {
	summary := UpdateSummary{
		CheckedAt: time.Now(),
	}

	curatedByID := make(map[string]CuratedEntry)
	if m.CuratedClient != nil {
		entries, _, err := m.CuratedClient.Fetch(ctx, forceRefresh)
		if err != nil {
			summary.Errors = append(summary.Errors, fmt.Sprintf("curated catalog: %v", err))
		} else {
			for _, e := range entries {
				curatedByID[strings.ToLower(e.ID)] = e
			}
		}
	}

	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		sem     = make(chan struct{}, maxConcurrentReleaseChecks)
		updates []PluginUpdate
		errs    []string
	)
	addErr := func(msg string) {
		mu.Lock()
		errs = append(errs, msg)
		mu.Unlock()
	}
	addUpdate := func(u PluginUpdate) {
		mu.Lock()
		updates = append(updates, u)
		mu.Unlock()
	}

	// 1. Check user-managed Wasm plugins.
	if m.Wasm != nil {
		metaMap := m.Wasm.InstalledMetadata()
		for _, st := range m.Wasm.Plugins() {
			if st.UUID == "" || !m.Wasm.IsUserManagedPlugin(st.Path) {
				continue
			}
			curated, hasCurated := curatedByID[strings.ToLower(st.UUID)]
			rec, hasRec := findInstallRecord(metaMap, st.UUID)

			repoURL := ""
			assetName := ""
			source := ""
			var curatedCopy *CuratedEntry
			if hasCurated && curated.Kind == plugin.ProviderWasm && curated.Repository != "" {
				repoURL = curated.Repository
				assetName = curated.AssetName
				source = "curated"
				cp := curated
				curatedCopy = &cp
			} else if hasRec && rec.Repository != "" {
				repoURL = rec.Repository
				assetName = rec.AssetName
				source = "github"
			}
			owner, repo, ok := ParseGitHubRepo(repoURL)
			if !ok {
				continue
			}

			summary.CheckedCount++
			pluginSt := st
			wg.Go(func() {
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-ctx.Done():
					return
				}
				rel, isNewer, err := m.checkGitHubRelease(ctx, owner, repo, pluginSt.Version)
				if err != nil {
					addErr(fmt.Sprintf("%s: %v", pluginSt.Name, err))
					return
				}
				if !isNewer || rel == nil {
					return
				}
				addUpdate(PluginUpdate{
					UUID:           pluginSt.UUID,
					Name:           pluginSt.Name,
					Kind:           plugin.ProviderWasm,
					Source:         source,
					CurrentVersion: updater.NormalizeVersion(pluginSt.Version),
					LatestVersion:  rel.Version,
					ReleaseNotes:   strings.TrimSpace(rel.Body),
					Repository:     repoURL,
					AssetName:      assetName,
					CuratedEntry:   curatedCopy,
				})
			})
		}
	}

	// 2. Check user-managed .sdPlugin bundles.
	if m.SD != nil {
		var managedSD []sdplugin.PluginStatus
		for _, st := range m.SD.Plugins() {
			if st.UUID != "" && m.SD.IsUserManagedPlugin(st.Dir) {
				managedSD = append(managedSD, st)
			}
		}
		if len(managedSD) > 0 {
			oaByID := make(map[string]sdplugin.CatalogueEntry)
			if m.OpenActionClient != nil {
				oaEntries, oaErr := m.OpenActionClient.Fetch(ctx, forceRefresh)
				if oaErr != nil {
					addErr(fmt.Sprintf("openaction catalog: %v", oaErr))
				} else {
					for _, e := range oaEntries {
						oaByID[strings.ToLower(e.ID)] = e
					}
				}
			}

			for _, st := range managedSD {
				lowerID := strings.ToLower(st.UUID)
				curated, hasCurated := curatedByID[lowerID]
				if hasCurated && curated.Kind != plugin.ProviderStreamDeck {
					hasCurated = false
				}
				oaEntry, hasOA := oaByID[lowerID]
				if !hasCurated && !hasOA {
					continue
				}

				summary.CheckedCount++
				pluginSt := st
				wg.Go(func() {
					upd, err := m.checkSDPluginUpdate(ctx, sem, pluginSt, curated, hasCurated, oaEntry, hasOA)
					if err != nil {
						addErr(fmt.Sprintf("%s: %v", pluginSt.Name, err))
						return
					}
					if upd != nil {
						addUpdate(*upd)
					}
				})
			}
		}
	}

	wg.Wait()

	slices.SortFunc(updates, func(a, b PluginUpdate) int {
		if c := cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return cmp.Compare(strings.ToLower(a.UUID), strings.ToLower(b.UUID))
	})
	summary.Updates = updates
	summary.Errors = append(summary.Errors, errs...)
	m.publishSummary(summary)
	return summary
}

func (m *Manager) checkSDPluginUpdate(
	ctx context.Context,
	sem chan struct{},
	st sdplugin.PluginStatus,
	curated CuratedEntry,
	hasCurated bool,
	oaEntry sdplugin.CatalogueEntry,
	hasOA bool,
) (*PluginUpdate, error) {
	source := "openaction"
	repoURL := ""
	var curatedCopy *CuratedEntry
	var oaCopy *sdplugin.CatalogueEntry

	if hasCurated {
		source = "curated"
		cp := curated
		curatedCopy = &cp
		repoURL = curated.Repository
		derived := curated.ToSDCatalogueEntry()
		oaCopy = &derived
	}
	if hasOA {
		cp := oaEntry
		oaCopy = &cp
		if repoURL == "" {
			repoURL = oaEntry.Repository
		}
	}

	if hasOA && strings.TrimSpace(oaEntry.Version) != "" {
		latest := updater.NormalizeVersion(oaEntry.Version)
		if latest != "" && updater.CompareVersions(latest, st.Version) > 0 {
			return &PluginUpdate{
				UUID:            st.UUID,
				Name:            st.Name,
				Kind:            plugin.ProviderStreamDeck,
				Source:          source,
				CurrentVersion:  updater.NormalizeVersion(st.Version),
				LatestVersion:   latest,
				Repository:      repoURL,
				CuratedEntry:    curatedCopy,
				OpenActionEntry: oaCopy,
			}, nil
		}
		return nil, nil
	}

	owner, repo, ok := ParseGitHubRepo(repoURL)
	if !ok {
		return nil, nil
	}

	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	rel, isNewer, err := m.checkGitHubRelease(ctx, owner, repo, st.Version)
	if err != nil || !isNewer || rel == nil {
		return nil, err
	}
	return &PluginUpdate{
		UUID:            st.UUID,
		Name:            st.Name,
		Kind:            plugin.ProviderStreamDeck,
		Source:          source,
		CurrentVersion:  updater.NormalizeVersion(st.Version),
		LatestVersion:   rel.Version,
		ReleaseNotes:    strings.TrimSpace(rel.Body),
		Repository:      repoURL,
		CuratedEntry:    curatedCopy,
		OpenActionEntry: oaCopy,
	}, nil
}

func (m *Manager) checkGitHubRelease(ctx context.Context, owner, repo, currentVersion string) (*updater.ReleaseInfo, bool, error) {
	checker := updater.NewChecker(owner, repo)
	checker.UserAgent = "PoundControl-Plugin-Client"
	checker.APIBaseURL = m.githubAPIBase()
	checker.HTTPClient = m.httpClient()
	return checker.CheckLatest(ctx, currentVersion)
}

// InstallCurated downloads, verifies, and installs (or upgrades) a curated catalog plugin.
func (m *Manager) InstallCurated(
	ctx context.Context,
	entry CuratedEntry,
	onProgress func(downloaded, total int64, stage string),
) error {
	switch entry.Kind {
	case plugin.ProviderWasm:
		if m.Wasm == nil {
			return errors.New("wasm plugin host is not available")
		}
		owner, repo, ok := ParseGitHubRepo(entry.Repository)
		if !ok {
			return fmt.Errorf("curated wasm plugin %q requires a valid GitHub repository URL", entry.Name)
		}
		st, err := m.Wasm.InstallFromGitHubRelease(ctx, owner, repo, entry.AssetName, m.githubAPIBase(), m.httpClient(), onProgress)
		if err != nil {
			return err
		}
		m.NotifyPluginInstalled(st.UUID, st.Version)
		return nil

	case plugin.ProviderStreamDeck:
		if m.SD == nil {
			return errors.New("stream deck plugin host is not available")
		}
		oaCli := m.oaClientForInstall()
		bundle, err := m.SD.InstallFromCatalogue(ctx, oaCli, entry.ToSDCatalogueEntry(), onProgress)
		if err != nil {
			return err
		}
		m.NotifyPluginInstalled(bundle.UUID, bundle.Manifest.Version)
		return nil

	default:
		return fmt.Errorf("unsupported curated plugin kind %q", entry.Kind)
	}
}

// ApplyUpdate downloads, verifies, and installs the target PluginUpdate.
func (m *Manager) ApplyUpdate(
	ctx context.Context,
	upd PluginUpdate,
	onProgress func(downloaded, total int64, stage string),
) error {
	switch upd.Kind {
	case plugin.ProviderWasm:
		if m.Wasm == nil {
			return errors.New("wasm plugin host is not available")
		}
		owner, repo, ok := ParseGitHubRepo(upd.Repository)
		if !ok {
			return fmt.Errorf("cannot update wasm plugin %q: missing GitHub repository URL", upd.Name)
		}
		st, err := m.Wasm.InstallFromGitHubRelease(ctx, owner, repo, upd.AssetName, m.githubAPIBase(), m.httpClient(), onProgress)
		if err != nil {
			return err
		}
		m.NotifyPluginInstalled(st.UUID, st.Version)
		return nil

	case plugin.ProviderStreamDeck:
		if m.SD == nil {
			return errors.New("stream deck plugin host is not available")
		}
		var entry sdplugin.CatalogueEntry
		switch {
		case upd.OpenActionEntry != nil:
			entry = *upd.OpenActionEntry
		case upd.CuratedEntry != nil:
			entry = upd.CuratedEntry.ToSDCatalogueEntry()
		default:
			entry = sdplugin.CatalogueEntry{
				ID:         upd.UUID,
				Name:       upd.Name,
				Repository: upd.Repository,
			}
		}
		bundle, err := m.SD.InstallFromCatalogue(ctx, m.oaClientForInstall(), entry, onProgress)
		if err != nil {
			return err
		}
		m.NotifyPluginInstalled(bundle.UUID, bundle.Manifest.Version)
		return nil

	default:
		return fmt.Errorf("unsupported plugin kind %q for update", upd.Kind)
	}
}

func (m *Manager) oaClientForInstall() *sdplugin.CatalogueClient {
	if m.OpenActionClient != nil {
		return m.OpenActionClient
	}
	oa := sdplugin.NewCatalogueClient()
	oa.GitHubAPIBaseURL = m.githubAPIBase()
	oa.HTTPClient = m.httpClient()
	return oa
}

func findInstallRecord(records map[string]wasmplugin.InstallRecord, uuid string) (wasmplugin.InstallRecord, bool) {
	if rec, ok := records[uuid]; ok {
		return rec, true
	}
	for k, rec := range records {
		if strings.EqualFold(k, uuid) {
			return rec, true
		}
	}
	return wasmplugin.InstallRecord{}, false
}

func cloneUpdateSummary(sum UpdateSummary) UpdateSummary {
	out := UpdateSummary{
		CheckedCount: sum.CheckedCount,
		CheckedAt:    sum.CheckedAt,
		Errors:       slices.Clone(sum.Errors),
	}
	if len(sum.Updates) > 0 {
		out.Updates = make([]PluginUpdate, len(sum.Updates))
		for i, u := range sum.Updates {
			if u.CuratedEntry != nil {
				cp := *u.CuratedEntry
				cp.AllowedHosts = slices.Clone(cp.AllowedHosts)
				u.CuratedEntry = &cp
			}
			if u.OpenActionEntry != nil {
				cp := *u.OpenActionEntry
				cp.OS = slices.Clone(cp.OS)
				u.OpenActionEntry = &cp
			}
			out.Updates[i] = u
		}
	}
	return out
}
