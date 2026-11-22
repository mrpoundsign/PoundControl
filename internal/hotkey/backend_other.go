//go:build !windows

package hotkey

import "github.com/mrpoundsign/PoundControl/pkg/deck"

type noopBackend struct{}

func newOSBackend() backend {
	return noopBackend{}
}

func (noopBackend) sync(bindings []registeredBinding, _ func(deck.Coordinates)) (int, error) {
	return len(bindings), nil
}

func (noopBackend) close() {}
