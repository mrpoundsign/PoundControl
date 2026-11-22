// Package apps provides PoundControl's built-in cross-platform application launch, URL, HTTP/WebSocket,
// keyboard macro, and OS media control plugin.
package apps

import "context"

// ExecRequest describes an application or script execution request.
type ExecRequest struct {
	Command    string
	Args       []string
	WorkingDir string
	Detached   bool
	HideWindow bool
}

// MediaCommand identifies an OS media playback or system audio command.
type MediaCommand string

const (
	MediaPlayPause  MediaCommand = "play_pause"
	MediaNext       MediaCommand = "next"
	MediaPrev       MediaCommand = "previous"
	MediaStop       MediaCommand = "stop"
	MediaVolumeUp   MediaCommand = "volume_up"
	MediaVolumeDown MediaCommand = "volume_down"
	MediaVolumeMute MediaCommand = "volume_mute"
)

// OSDriver abstracts platform-specific process, URL, input synthesis, and media control calls
// so tests remain strictly isolated from the host operating system.
type OSDriver interface {
	Exec(ctx context.Context, req ExecRequest) error
	OpenURL(ctx context.Context, target string) error
	SendHotkey(ctx context.Context, chord Chord) error
	TypeText(ctx context.Context, text string) error
	SendMedia(ctx context.Context, cmd MediaCommand) error
}

// DefaultDriver returns the native OSDriver for the current runtime platform.
func DefaultDriver() OSDriver {
	return newOSDriver()
}
