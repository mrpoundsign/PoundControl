package ui

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

func TestStudioThemeColorsAndSizes(t *testing.T) {
	st := &StudioTheme{}

	cases := []struct {
		name  fyne.ThemeColorName
		want  fyne.ThemeColorName
		color any
	}{
		{name: theme.ColorNameBackground, color: ColorObsidianSlate},
		{name: theme.ColorNamePrimary, color: ColorSkyBlue},
		{name: theme.ColorNameForeground, color: ColorChromeWhite},
		{name: theme.ColorNameInputBackground, color: ColorElevatedSlate},
		{name: theme.ColorNameOverlayBackground, color: ColorDarkCharcoal},
		{name: theme.ColorNameSuccess, color: ColorSuccess},
		{name: theme.ColorNameWarning, color: ColorWarning},
		{name: theme.ColorNameError, color: ColorError},
	}

	for _, tc := range cases {
		got := st.Color(tc.name, theme.VariantLight)
		if got != tc.color {
			t.Errorf("Color(%q) = %v, want %v", tc.name, got, tc.color)
		}
	}

	if got := st.Size(theme.SizeNameText); got != 12.0 {
		t.Errorf("Size(SizeNameText) = %v, want 12.0", got)
	}
	if got := st.Size(theme.SizeNameHeadingText); got != 18.0 {
		t.Errorf("Size(SizeNameHeadingText) = %v, want 18.0", got)
	}
}
