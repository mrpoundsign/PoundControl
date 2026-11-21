//go:build windows

package hotkey

import (
	"log"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"github.com/mrpoundsign/PoundDeck/pkg/deck"
)

var (
	user32                 = syscall.NewLazyDLL("user32.dll")
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procRegisterHotKey     = user32.NewProc("RegisterHotKey")
	procUnregisterHotKey   = user32.NewProc("UnregisterHotKey")
	procGetMessageW        = user32.NewProc("GetMessageW")
	procPostThreadMessageW = user32.NewProc("PostThreadMessageW")
	procGetCurrentThreadID = kernel32.NewProc("GetCurrentThreadId")
)

const (
	wmHotkey uint32 = 0x0312
	wmQuit   uint32 = 0x0012
)

type winPoint struct {
	X, Y int32
}

type winMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      winPoint
}

type windowsBackend struct {
	mu       sync.Mutex
	threadID uint32
	doneCh   chan struct{}
}

func newOSBackend() backend {
	return &windowsBackend{}
}

func (b *windowsBackend) sync(bindings []registeredBinding, onTrigger func(deck.Coordinates)) (int, error) {
	b.close()
	if len(bindings) == 0 {
		return 0, nil
	}

	type startResult struct {
		threadID uint32
		count    int
	}
	readyCh := make(chan startResult, 1)
	doneCh := make(chan struct{})

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(doneCh)

		tidRaw, _, _ := procGetCurrentThreadID.Call()
		tid := uint32(tidRaw)

		registered := make(map[uintptr]deck.Coordinates, len(bindings))
		for _, item := range bindings {
			ret, _, regErr := procRegisterHotKey.Call(
				0,
				item.id,
				uintptr(item.spec.WinMod),
				uintptr(item.spec.WinVK),
			)
			if ret == 0 {
				log.Printf("[Hotkey] RegisterHotKey(%q) failed: %v", item.spec.String(), regErr)
				continue
			}
			registered[item.id] = item.coords
		}
		defer func() {
			for id := range registered {
				_, _, _ = procUnregisterHotKey.Call(0, id)
			}
		}()

		readyCh <- startResult{
			threadID: tid,
			count:    len(registered),
		}
		if len(registered) == 0 {
			return
		}

		var msg winMsg
		for {
			r, _, _ := procGetMessageW.Call(
				uintptr(unsafe.Pointer(&msg)),
				0,
				0,
				0,
			)
			if int32(r) <= 0 {
				break
			}
			if msg.Message == wmHotkey {
				if coords, ok := registered[msg.WParam]; ok && onTrigger != nil {
					go onTrigger(coords)
				}
			}
		}
	}()

	res := <-readyCh
	if res.count > 0 {
		b.mu.Lock()
		b.threadID = res.threadID
		b.doneCh = doneCh
		b.mu.Unlock()
	} else {
		<-doneCh
	}
	return res.count, nil
}

func (b *windowsBackend) close() {
	b.mu.Lock()
	tid := b.threadID
	done := b.doneCh
	b.threadID = 0
	b.doneCh = nil
	b.mu.Unlock()

	if tid != 0 {
		_, _, _ = procPostThreadMessageW.Call(uintptr(tid), uintptr(wmQuit), 0, 0)
	}
	if done != nil {
		<-done
	}
}
