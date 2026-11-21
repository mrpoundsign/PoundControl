package deck

import "testing"

func TestGridSizeValidateAndBounds(t *testing.T) {
	def := DefaultGrid()
	if err := def.Validate(); err != nil {
		t.Fatalf("DefaultGrid().Validate() returned unexpected error: %v", err)
	}
	if got := def.TotalSlots(); got != 15 {
		t.Fatalf("DefaultGrid().TotalSlots() = %d, want 15", got)
	}

	cases := []struct {
		name    string
		grid    GridSize
		wantErr bool
		slots   int
	}{
		{name: "zero cols", grid: GridSize{Cols: 0, Rows: 3}, wantErr: true, slots: 0},
		{name: "negative rows", grid: GridSize{Cols: 5, Rows: -1}, wantErr: true, slots: 0},
		{name: "too large", grid: GridSize{Cols: 17, Rows: 2}, wantErr: true, slots: 34},
		{name: "max valid", grid: GridSize{Cols: 16, Rows: 16}, wantErr: false, slots: 256},
		{name: "mini 3x2", grid: GridSize{Cols: 3, Rows: 2}, wantErr: false, slots: 6},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.grid.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
			if got := tc.grid.TotalSlots(); got != tc.slots {
				t.Fatalf("TotalSlots() = %d, want %d", got, tc.slots)
			}
		})
	}
}

func TestGridContainsAndSlotKey(t *testing.T) {
	grid := GridSize{Cols: 5, Rows: 3}

	inBounds := []Coordinates{
		{Row: 0, Col: 0},
		{Row: 2, Col: 4},
		{Row: 1, Col: 2},
	}
	for _, c := range inBounds {
		if !grid.Contains(c) {
			t.Errorf("grid.Contains(%+v) = false, want true", c)
		}
	}

	outOfBounds := []Coordinates{
		{Row: -1, Col: 0},
		{Row: 0, Col: -1},
		{Row: 3, Col: 0},
		{Row: 0, Col: 5},
	}
	for _, c := range outOfBounds {
		if grid.Contains(c) {
			t.Errorf("grid.Contains(%+v) = true, want false", c)
		}
	}

	c := Coordinates{Row: 2, Col: 4}
	if got := c.SlotKey(); got != "2,4" {
		t.Fatalf("Coordinates.SlotKey() = %q, want %q", got, "2,4")
	}
}
