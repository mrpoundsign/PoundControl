//go:build windows

package apps

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

var (
	user32        = syscall.NewLazyDLL("user32.dll")
	procSendInput = user32.NewProc("SendInput")
)

const (
	inputKeyboard        uint32 = 1
	keyeventfExtendedKey uint32 = 0x0001
	keyeventfKeyUp       uint32 = 0x0002
	keyeventfUnicode     uint32 = 0x0004

	vkControl uint16 = 0x11
	vkMenu    uint16 = 0x12
	vkShift   uint16 = 0x10
	vkLWin    uint16 = 0x5B
	vkReturn  uint16 = 0x0D

	vkVolumeMute    uint16 = 0xAD
	vkVolumeDown    uint16 = 0xAE
	vkVolumeUp      uint16 = 0xAF
	vkMediaNext     uint16 = 0xB0
	vkMediaPrev     uint16 = 0xB1
	vkMediaStop     uint16 = 0xB2
	vkMediaPlayPause uint16 = 0xB3

	createNoWindow uint32 = 0x08000000
)

type winKeybdInput struct {
	WVk         uint16
	WScan       uint16
	DwFlags     uint32
	Time        uint32
	DwExtraInfo uintptr
}

// winInput matches Win32 INPUT union layout (40 bytes on amd64, 28 bytes on 386).
type winInput struct {
	Type uint32
	Ki   winKeybdInput
	_    [8]byte
}

type windowsDriver struct{}

func newOSDriver() OSDriver {
	return windowsDriver{}
}

func (windowsDriver) Exec(ctx context.Context, req ExecRequest) error {
	var cmd *exec.Cmd
	if req.Detached {
		cmd = exec.Command(req.Command, req.Args...)
	} else {
		cmd = exec.CommandContext(ctx, req.Command, req.Args...)
	}
	if req.WorkingDir != "" {
		cmd.Dir = req.WorkingDir
	}
	if req.HideWindow {
		cmd.SysProcAttr = &syscall.SysProcAttr{
			HideWindow:    true,
			CreationFlags: createNoWindow,
		}
	}
	if req.Detached {
		return cmd.Start()
	}
	return cmd.Run()
}

func (windowsDriver) OpenURL(ctx context.Context, target string) error {
	cmd := exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", target)
	return cmd.Start()
}

func (windowsDriver) SendHotkey(_ context.Context, ch Chord) error {
	var mods []uint16
	if ch.Ctrl {
		mods = append(mods, vkControl)
	}
	if ch.Shift {
		mods = append(mods, vkShift)
	}
	if ch.Alt {
		mods = append(mods, vkMenu)
	}
	if ch.Win {
		mods = append(mods, vkLWin)
	}

	inputs := make([]winInput, 0, len(mods)*2+2)
	for _, m := range mods {
		inputs = append(inputs, vkInput(m, false, false))
	}
	inputs = append(inputs, vkInput(ch.WinVK, ch.WinExt, false))
	inputs = append(inputs, vkInput(ch.WinVK, ch.WinExt, true))
	for i := len(mods) - 1; i >= 0; i-- {
		inputs = append(inputs, vkInput(mods[i], false, true))
	}
	return sendWinInputs(inputs)
}

func (windowsDriver) TypeText(_ context.Context, text string) error {
	inputs := buildTextInputs(text)
	if len(inputs) == 0 {
		return nil
	}
	return sendWinInputs(inputs)
}

func (windowsDriver) SendMedia(_ context.Context, cmd MediaCommand) error {
	var vk uint16
	switch cmd {
	case MediaPlayPause:
		vk = vkMediaPlayPause
	case MediaNext:
		vk = vkMediaNext
	case MediaPrev:
		vk = vkMediaPrev
	case MediaStop:
		vk = vkMediaStop
	case MediaVolumeUp:
		vk = vkVolumeUp
	case MediaVolumeDown:
		vk = vkVolumeDown
	case MediaVolumeMute:
		vk = vkVolumeMute
	default:
		return fmt.Errorf("unsupported media command %q", cmd)
	}
	inputs := []winInput{
		vkInput(vk, true, false),
		vkInput(vk, true, true),
	}
	return sendWinInputs(inputs)
}

func vkInput(vk uint16, extended, keyUp bool) winInput {
	var flags uint32
	if extended {
		flags |= keyeventfExtendedKey
	}
	if keyUp {
		flags |= keyeventfKeyUp
	}
	return winInput{
		Type: inputKeyboard,
		Ki: winKeybdInput{
			WVk:     vk,
			DwFlags: flags,
		},
	}
}

func buildTextInputs(text string) []winInput {
	if text == "" {
		return nil
	}
	inputs := make([]winInput, 0, len(text)*2)
	for _, r := range text {
		switch r {
		case '\r':
			continue
		case '\n':
			inputs = append(inputs, vkInput(vkReturn, false, false), vkInput(vkReturn, false, true))
		default:
			for _, u := range utf16.Encode([]rune{r}) {
				inputs = append(inputs,
					winInput{
						Type: inputKeyboard,
						Ki:   winKeybdInput{WScan: u, DwFlags: keyeventfUnicode},
					},
					winInput{
						Type: inputKeyboard,
						Ki:   winKeybdInput{WScan: u, DwFlags: keyeventfUnicode | keyeventfKeyUp},
					},
				)
			}
		}
	}
	return inputs
}

func sendWinInputs(inputs []winInput) error {
	if len(inputs) == 0 {
		return nil
	}
	sent, _, err := procSendInput.Call(
		uintptr(len(inputs)),
		uintptr(unsafe.Pointer(&inputs[0])),
		unsafe.Sizeof(inputs[0]),
	)
	if sent == 0 {
		if err != nil && !errors.Is(err, syscall.Errno(0)) {
			return fmt.Errorf("SendInput failed: %w", err)
		}
		return errors.New("SendInput sent 0 events")
	}
	return nil
}
