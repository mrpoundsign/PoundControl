//go:build windows

package apps

import (
	"runtime"
	"testing"
	"unsafe"
)

func TestWinInputStructSizeAndTextInputs(t *testing.T) {
	wantSize := uintptr(40)
	if runtime.GOARCH == "386" {
		wantSize = 28
	}
	if got := unsafe.Sizeof(winInput{}); got != wantSize {
		t.Fatalf("unsafe.Sizeof(winInput{}) = %d, want %d", got, wantSize)
	}

	inputs := buildTextInputs("Hi\r\n!")
	// 'H', 'i', '\n', '!' -> 4 characters -> 8 KEYBDINPUT events ('\r' skipped).
	if len(inputs) != 8 {
		t.Fatalf("expected 8 winInput events, got %d", len(inputs))
	}
	if inputs[0].Ki.WScan != 'H' || inputs[0].Ki.DwFlags != keyeventfUnicode {
		t.Errorf("unexpected first unicode down input: %+v", inputs[0])
	}
	if inputs[1].Ki.WScan != 'H' || inputs[1].Ki.DwFlags != (keyeventfUnicode|keyeventfKeyUp) {
		t.Errorf("unexpected first unicode up input: %+v", inputs[1])
	}
	if inputs[4].Ki.WVk != vkReturn || inputs[5].Ki.DwFlags != keyeventfKeyUp {
		t.Errorf("unexpected newline return inputs: %+v, %+v", inputs[4], inputs[5])
	}
}
