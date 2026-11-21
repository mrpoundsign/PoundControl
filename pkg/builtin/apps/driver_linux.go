//go:build linux

package apps

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type linuxDriver struct{}

func newOSDriver() OSDriver {
	return linuxDriver{}
}

func (linuxDriver) Exec(ctx context.Context, req ExecRequest) error {
	var cmd *exec.Cmd
	if req.Detached {
		cmd = exec.Command(req.Command, req.Args...)
	} else {
		cmd = exec.CommandContext(ctx, req.Command, req.Args...)
	}
	if req.WorkingDir != "" {
		cmd.Dir = req.WorkingDir
	}
	if req.Detached {
		return cmd.Start()
	}
	return cmd.Run()
}

func (linuxDriver) OpenURL(ctx context.Context, target string) error {
	return exec.CommandContext(ctx, "xdg-open", target).Start()
}

func (linuxDriver) SendHotkey(ctx context.Context, ch Chord) error {
	parts := make([]string, 0, 5)
	if ch.Ctrl {
		parts = append(parts, "ctrl")
	}
	if ch.Shift {
		parts = append(parts, "shift")
	}
	if ch.Alt {
		parts = append(parts, "alt")
	}
	if ch.Win {
		parts = append(parts, "super")
	}
	if ch.X11Key != "" {
		parts = append(parts, ch.X11Key)
	}
	if len(parts) == 0 {
		return errors.New("empty hotkey chord")
	}
	return exec.CommandContext(ctx, "xdotool", "key", "--clearmodifiers", strings.Join(parts, "+")).Run()
}

func (linuxDriver) TypeText(ctx context.Context, text string) error {
	if _, err := exec.LookPath("xdotool"); err == nil {
		return exec.CommandContext(ctx, "xdotool", "type", "--clearmodifiers", "--delay", "0", text).Run()
	}
	if _, err := exec.LookPath("wtype"); err == nil {
		return exec.CommandContext(ctx, "wtype", text).Run()
	}
	return errors.New("neither xdotool nor wtype is installed")
}

func (linuxDriver) SendMedia(ctx context.Context, cmd MediaCommand) error {
	switch cmd {
	case MediaPlayPause:
		return exec.CommandContext(ctx, "playerctl", "play-pause").Run()
	case MediaNext:
		return exec.CommandContext(ctx, "playerctl", "next").Run()
	case MediaPrev:
		return exec.CommandContext(ctx, "playerctl", "previous").Run()
	case MediaStop:
		return exec.CommandContext(ctx, "playerctl", "stop").Run()
	case MediaVolumeUp:
		if _, err := exec.LookPath("wpctl"); err == nil {
			return exec.CommandContext(ctx, "wpctl", "set-volume", "@DEFAULT_AUDIO_SINK@", "5%+").Run()
		}
		return exec.CommandContext(ctx, "pactl", "set-sink-volume", "@DEFAULT_SINK@", "+5%").Run()
	case MediaVolumeDown:
		if _, err := exec.LookPath("wpctl"); err == nil {
			return exec.CommandContext(ctx, "wpctl", "set-volume", "@DEFAULT_AUDIO_SINK@", "5%-").Run()
		}
		return exec.CommandContext(ctx, "pactl", "set-sink-volume", "@DEFAULT_SINK@", "-5%").Run()
	case MediaVolumeMute:
		if _, err := exec.LookPath("wpctl"); err == nil {
			return exec.CommandContext(ctx, "wpctl", "set-mute", "@DEFAULT_AUDIO_SINK@", "toggle").Run()
		}
		return exec.CommandContext(ctx, "pactl", "set-sink-mute", "@DEFAULT_SINK@", "toggle").Run()
	default:
		return fmt.Errorf("unsupported media command %q", cmd)
	}
}
