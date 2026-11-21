//go:build !windows && !linux && !darwin

package apps

import (
	"context"
	"errors"
)

type otherDriver struct{}

func newOSDriver() OSDriver {
	return otherDriver{}
}

func (otherDriver) Exec(context.Context, ExecRequest) error   { return errors.New("unsupported OS") }
func (otherDriver) OpenURL(context.Context, string) error     { return errors.New("unsupported OS") }
func (otherDriver) SendHotkey(context.Context, Chord) error   { return errors.New("unsupported OS") }
func (otherDriver) TypeText(context.Context, string) error    { return errors.New("unsupported OS") }
func (otherDriver) SendMedia(context.Context, MediaCommand) error { return errors.New("unsupported OS") }
