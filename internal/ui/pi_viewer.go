package ui

import (
	"context"
	"net/http"

	"github.com/mrpoundsign/PoundControl/pkg/sdplugin"
	"github.com/mrpoundsign/PoundControl/pkg/wasmplugin"
)

// PIViewer displays interactive HTML Property Inspector pages for external plugins.
type PIViewer interface {
	Open(title, targetURL string) error
	CloseAll()
}

// SDPluginController exposes external .sdPlugin discovery, lifecycle, Marketplace installation, and Node.js runtime control to UI surfaces.
type SDPluginController interface {
	HostConfig() sdplugin.HostConfig
	SaveHostConfig(cfg sdplugin.HostConfig) error
	Reload() error
	Plugins() []sdplugin.PluginStatus
	DefaultPluginsDir() string
	ElgatoPluginsDir() string
	OpenDeckPluginsDir() string
	IsUserManagedPlugin(pluginDir string) bool
	InstallArchive(archivePath string) (*sdplugin.LoadedBundle, error)
	InstallFromCatalogue(ctx context.Context, client *sdplugin.CatalogueClient, entry sdplugin.CatalogueEntry, onProgress func(downloaded, total int64, stage string)) (*sdplugin.LoadedBundle, error)
	UninstallPlugin(pluginUUID string) error
	RegisterMarketplaceDetailPage(htmlContent string) (string, bool)
	NodeStatus(requiredMajor int) sdplugin.NodeRuntimeInfo
	DownloadNode(ctx context.Context, onProgress func(downloaded, total int64, stage string)) (sdplugin.NodeRuntimeInfo, error)
	NeedsNodeForAction(actionUUID string) (minMajor int, ok bool)
}

// WasmPluginController exposes external WebAssembly (.wasm) discovery, lifecycle, installation, and status to UI surfaces.
type WasmPluginController interface {
	HostConfig() wasmplugin.HostConfig
	SaveHostConfig(cfg wasmplugin.HostConfig) error
	Reload() error
	Plugins() []wasmplugin.PluginStatus
	DefaultPluginsDir() string
	IsUserManagedPlugin(pluginPath string) bool
	InstalledMetadata() map[string]wasmplugin.InstallRecord
	InstallFile(srcPath string, meta *wasmplugin.InstallRecord) (wasmplugin.PluginStatus, error)
	InstallFromGitHubRelease(
		ctx context.Context,
		owner, repo, assetName, apiBaseURL string,
		httpClient *http.Client,
		onProgress func(downloaded, total int64, stage string),
	) (wasmplugin.PluginStatus, error)
	UninstallPlugin(pluginUUID string) error
}

var (
	_ SDPluginController   = (*sdplugin.Provider)(nil)
	_ WasmPluginController = (*wasmplugin.Provider)(nil)
)

// NewDefaultPIViewer returns the platform-appropriate Property Inspector viewer.
func NewDefaultPIViewer() PIViewer {
	return newPlatformPIViewer()
}
