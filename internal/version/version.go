// Package version holds application identity and build version metadata for PoundDeck.
package version

const (
	// AppName is the display name of the desktop studio.
	AppName = "PoundDeck"
	// AppID is the reverse-DNS application identifier used by Fyne and OS packaging.
	AppID = "com.github.mrpoundsign.pounddeck"
)

// Version is the semantic version string, overridden at build time via -ldflags when releasing.
var Version = "0.1.0-dev"
