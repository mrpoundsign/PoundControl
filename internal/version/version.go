// Package version holds application identity and build version metadata for PoundControl.
package version

const (
	// AppName is the display name of the desktop studio.
	AppName = "PoundControl"
	// AppID is the reverse-DNS application identifier used by Fyne and OS packaging.
	AppID = "com.github.mrpoundsign.poundcontrol"
)

// Version is the semantic version string, overridden at build time via -ldflags when releasing.
var Version = "0.1.3"
