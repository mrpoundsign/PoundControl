//go:build windows

package sdplugin

import (
	"os/exec"
	"syscall"
)

const createNoWindow uint32 = 0x08000000

func configureBackgroundCmd(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
}
