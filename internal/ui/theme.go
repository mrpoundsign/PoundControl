// Package ui implements the Fyne desktop studio interface and Obsidian Slate theme for PoundControl.
package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// Obsidian Slate & Electric Sky Blue palette tokens shared across the PoundControl UI.
var (
	ColorObsidianDeep   = color.RGBA{R: 2, G: 2, B: 3, A: 255}
	ColorObsidianSlate  = color.RGBA{R: 10, G: 10, B: 12, A: 255}
	ColorDarkCharcoal   = color.RGBA{R: 20, G: 20, B: 22, A: 255}
	ColorHeaderSurface  = color.RGBA{R: 15, G: 17, B: 21, A: 255}
	ColorElevatedSlate  = color.RGBA{R: 30, G: 41, B: 59, A: 255}
	ColorSlateBorder    = color.RGBA{R: 71, G: 85, B: 105, A: 255}
	ColorMutedText      = color.RGBA{R: 100, G: 116, B: 139, A: 255}
	ColorSecondarySlate = color.RGBA{R: 148, G: 163, B: 184, A: 255}
	ColorChromeWhite    = color.RGBA{R: 255, G: 255, B: 255, A: 255}
	ColorSkyBlue        = color.RGBA{R: 56, G: 189, B: 248, A: 255}
	ColorDeepSky        = color.RGBA{R: 2, G: 132, B: 199, A: 255}
	ColorSelectionTint  = color.RGBA{R: 56, G: 189, B: 248, A: 64}
	ColorSuccess        = color.RGBA{R: 34, G: 197, B: 94, A: 255}
	ColorWarning        = color.RGBA{R: 251, G: 191, B: 36, A: 255}
	ColorError          = color.RGBA{R: 239, G: 68, B: 68, A: 255}
)

// StudioTheme implements PoundControl's dark Obsidian Slate, Chrome White, and Electric Sky Blue theme.
type StudioTheme struct{}

var _ fyne.Theme = (*StudioTheme)(nil)

// Color returns the themed color for the given Fyne color token.
func (m *StudioTheme) Color(name fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	switch name {
	case theme.ColorNameBackground:
		return ColorObsidianSlate
	case theme.ColorNameInputBackground:
		return ColorElevatedSlate
	case theme.ColorNameButton:
		return ColorElevatedSlate
	case theme.ColorNamePrimary:
		return ColorSkyBlue
	case theme.ColorNameHeaderBackground:
		return ColorHeaderSurface
	case theme.ColorNameSelection:
		return ColorSelectionTint
	case theme.ColorNameHover:
		return color.RGBA{R: 255, G: 255, B: 255, A: 25}
	case theme.ColorNameForeground:
		return ColorChromeWhite
	case theme.ColorNameDisabled:
		return ColorSecondarySlate
	case theme.ColorNameHyperlink:
		return ColorSkyBlue
	case theme.ColorNameOverlayBackground:
		return ColorDarkCharcoal
	case theme.ColorNamePlaceHolder:
		return ColorMutedText
	case theme.ColorNameScrollBar:
		return color.RGBA{R: 71, G: 85, B: 105, A: 180}
	case theme.ColorNameSeparator:
		return ColorElevatedSlate
	case theme.ColorNameError:
		return ColorError
	case theme.ColorNameWarning:
		return ColorWarning
	case theme.ColorNameSuccess:
		return ColorSuccess
	default:
		return theme.DefaultTheme().Color(name, theme.VariantDark)
	}
}

// Font returns the default Fyne font resource for the requested text style.
func (m *StudioTheme) Font(style fyne.TextStyle) fyne.Resource {
	return theme.DefaultTheme().Font(style)
}

// Icon returns the default Fyne icon resource for the requested icon name.
func (m *StudioTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(name)
}

// Size returns typography and layout metrics tuned for the PoundControl Studio workstation.
func (m *StudioTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNameSeparatorThickness:
		return 2.0
	case theme.SizeNameText:
		return 12.0
	case theme.SizeNameCaptionText:
		return 10.0
	case theme.SizeNameSubHeadingText:
		return 14.0
	case theme.SizeNameHeadingText:
		return 18.0
	case theme.SizeNameInnerPadding:
		return 6.0
	case theme.SizeNamePadding:
		return 4.0
	default:
		return theme.DefaultTheme().Size(name)
	}
}
