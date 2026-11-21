package ui

import (
	"context"

	"github.com/mrpoundsign/PoundDeck/pkg/sdplugin"
)

// PIViewer displays interactive HTML Property Inspector pages for external plugins.
type PIViewer interface {
	Open(title, targetURL string) error
	CloseAll()
}

// SDPluginController exposes external .sdPlugin discovery, lifecycle, and Node.js runtime control to UI surfaces.
type SDPluginController interface {
	HostConfig() sdplugin.HostConfig
	SaveHostConfig(cfg sdplugin.HostConfig) error
	Reload() error
	Plugins() []sdplugin.PluginStatus
	DefaultPluginsDir() string
	ElgatoPluginsDir() string
	NodeStatus(requiredMajor int) sdplugin.NodeRuntimeInfo
	DownloadNode(ctx context.Context, onProgress func(downloaded, total int64, stage string)) (sdplugin.NodeRuntimeInfo, error)
	NeedsNodeForAction(actionUUID string) (minMajor int, ok bool)
}

var _ SDPluginController = (*sdplugin.Provider)(nil)

// NewDefaultPIViewer returns the platform-appropriate Property Inspector viewer.
func NewDefaultPIViewer() PIViewer {
	return newPlatformPIViewer()
}
