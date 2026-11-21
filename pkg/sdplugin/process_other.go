//go:build !windows

package sdplugin

import "os/exec"

func configureBackgroundCmd(_ *exec.Cmd) {}
