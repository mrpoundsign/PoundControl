// Package soundboard implements the built-in PoundControl audio soundboard plugin.
package soundboard

import "time"

const (
	// MixerSampleRate is the normalized stereo output rate (48 kHz) used across decoders and mixer.
	MixerSampleRate = 48000
	// MixerChannels is the stereo channel count processed by the polyphonic mixer.
	MixerChannels = 2
	// SystemDefaultDevice selects the host operating system's default playback device.
	SystemDefaultDevice = "System Default"
	// DefaultMaxVoices caps simultaneously overlapping sound clips by default.
	DefaultMaxVoices = 16
	// MaxAllowedVoices caps the user-configurable maximum simultaneous voices.
	MaxAllowedVoices = 64
)

// OutputDevice describes one enumerated host audio playback endpoint.
type OutputDevice struct {
	ID        string
	Name      string
	IsDefault bool
}

// Clip holds decoded interleaved stereo float32 PCM samples normalized to MixerSampleRate.
type Clip struct {
	Samples    []float32
	SampleRate int
	Duration   time.Duration
}

// AudioEngine abstracts polyphonic clip playback and output device routing for the Soundboard provider.
type AudioEngine interface {
	ListOutputDevices() ([]OutputDevice, error)
	SetOutputDevice(deviceName string) error
	OutputDevice() string
	Play(slotCtx string, clip *Clip, gain float32) (uint64, error)
	StopSlot(slotCtx string, fadeOut time.Duration)
	StopOthers(exceptSlotCtx string, fadeOut time.Duration)
	StopAll(fadeOut time.Duration)
	ActiveForSlot(slotCtx string) (count int, maxRemaining time.Duration)
	TotalActive() int
	SetMasterVolume(vol float32)
	MasterVolume() float32
	SetMuted(muted bool)
	IsMuted() bool
	SetMaxVoices(n int)
	SetOnChange(fn func())
	Close() error
}
