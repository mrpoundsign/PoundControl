//go:build darwin

package apps

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type darwinDriver struct{}

func newOSDriver() OSDriver {
	return darwinDriver{}
}

func (darwinDriver) Exec(ctx context.Context, req ExecRequest) error {
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

func (darwinDriver) OpenURL(ctx context.Context, target string) error {
	return exec.CommandContext(ctx, "open", target).Start()
}

func (darwinDriver) SendHotkey(ctx context.Context, ch Chord) error {
	mods := make([]string, 0, 4)
	if ch.Ctrl {
		mods = append(mods, "control down")
	}
	if ch.Shift {
		mods = append(mods, "shift down")
	}
	if ch.Alt {
		mods = append(mods, "option down")
	}
	if ch.Win {
		mods = append(mods, "command down")
	}

	using := ""
	if len(mods) > 0 {
		using = " using {" + strings.Join(mods, ", ") + "}"
	}

	var script string
	if ch.MacKeyText != "" {
		escaped := strings.ReplaceAll(ch.MacKeyText, `"`, `\"`)
		script = fmt.Sprintf(`tell application "System Events" to keystroke %q%s`, escaped, using)
	} else {
		script = fmt.Sprintf(`tell application "System Events" to key code %d%s`, ch.MacKeyCode, using)
	}
	return exec.CommandContext(ctx, "osascript", "-e", script).Run()
}

func (darwinDriver) TypeText(ctx context.Context, text string) error {
	escaped := strings.ReplaceAll(text, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	script := fmt.Sprintf(`tell application "System Events" to keystroke %q`, escaped)
	return exec.CommandContext(ctx, "osascript", "-e", script).Run()
}

func (darwinDriver) SendMedia(ctx context.Context, cmd MediaCommand) error {
	var script string
	switch cmd {
	case MediaVolumeUp:
		script = "set volume output volume ((output volume of (get volume settings)) + 5)"
	case MediaVolumeDown:
		script = "set volume output volume ((output volume of (get volume settings)) - 5)"
	case MediaVolumeMute:
		script = "set volume output muted (not (output muted of (get volume settings)))"
	case MediaPlayPause:
		script = `tell application "System Events" to key code 49`
	default:
		return fmt.Errorf("media command %q is not natively bound via osascript on macOS", cmd)
	}
	return exec.CommandContext(ctx, "osascript", "-e", script).Run()
}
