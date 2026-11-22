// Package deck defines core domain types for PoundControl surfaces, grids, slots, and controllers.
package deck

import (
	"errors"
	"fmt"
)

// ControllerType identifies the physical or virtual input control kind for an action slot.
type ControllerType string

const (
	// ControllerKeypad represents a push button with an icon/title display.
	ControllerKeypad ControllerType = "Keypad"
	// ControllerEncoder represents a rotary dial with push and touch feedback display.
	ControllerEncoder ControllerType = "Encoder"
)

// SurfaceKind identifies where a deck UI or hardware surface is rendered.
type SurfaceKind string

const (
	// SurfaceVirtualDesktop represents the native Fyne desktop grid and pop-out pad.
	SurfaceVirtualDesktop SurfaceKind = "virtual_desktop"
	// SurfaceWebRemote represents a LAN Web / Mobile / Tablet touch deck client.
	SurfaceWebRemote SurfaceKind = "web_remote"
	// SurfaceHardware represents a physical USB HID deck attached in a future milestone.
	SurfaceHardware SurfaceKind = "hardware"
)

// GridSize describes the rectangular button matrix dimensions of a deck page.
type GridSize struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// DefaultGrid returns the standard 5x3 (15-key) deck layout.
func DefaultGrid() GridSize {
	return GridSize{Cols: 5, Rows: 3}
}

// Validate checks that the grid dimensions are positive and within reasonable UI bounds.
func (g GridSize) Validate() error {
	if g.Cols <= 0 || g.Rows <= 0 {
		return errors.New("grid columns and rows must both be positive")
	}
	if g.Cols > 16 || g.Rows > 16 {
		return fmt.Errorf("grid dimensions %dx%d exceed maximum supported size (16x16)", g.Cols, g.Rows)
	}
	return nil
}

// TotalSlots returns the number of keypad slots in the grid, or 0 if invalid.
func (g GridSize) TotalSlots() int {
	if g.Cols <= 0 || g.Rows <= 0 {
		return 0
	}
	return g.Cols * g.Rows
}

// Contains reports whether c falls inside the grid bounds.
func (g GridSize) Contains(c Coordinates) bool {
	return c.Row >= 0 && c.Col >= 0 && c.Row < g.Rows && c.Col < g.Cols
}

// Coordinates addresses a single keypad slot on a deck grid (0-indexed).
type Coordinates struct {
	Row int `json:"row"`
	Col int `json:"col"`
}

// SlotKey returns a canonical string key ("row,col") for indexing slot maps.
func (c Coordinates) SlotKey() string {
	return fmt.Sprintf("%d,%d", c.Row, c.Col)
}

// SurfaceInfo describes an active deck surface attached to the engine.
type SurfaceInfo struct {
	ID    string      `json:"id"`
	Name  string      `json:"name"`
	Kind  SurfaceKind `json:"kind"`
	Grid  GridSize    `json:"grid"`
	Dials int         `json:"dials"`
}
