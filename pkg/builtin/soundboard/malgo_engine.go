package soundboard

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gen2brain/malgo"
)

// MalgoEngine implements AudioEngine on top of miniaudio (github.com/gen2brain/malgo) and Mixer.
type MalgoEngine struct {
	mixer        *Mixer
	mu           sync.Mutex
	ctx          *malgo.AllocatedContext
	device       *malgo.Device
	targetDevice string
	closed       bool
	closeOnce    sync.Once

	// Owned exclusively by the miniaudio callback while a device is started.
	cbBuf []float32
}

// NewMalgoEngine constructs an AudioEngine that opens its playback device lazily on first Play call.
func NewMalgoEngine() *MalgoEngine {
	return &MalgoEngine{
		mixer: NewMixer(),
	}
}

// ListOutputDevices enumerates available host audio playback endpoints.
func (e *MalgoEngine) ListOutputDevices() ([]OutputDevice, error) {
	if testing.Testing() {
		return nil, nil
	}

	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("init audio context: %w", err)
	}
	defer func() {
		_ = ctx.Uninit()
		ctx.Free()
	}()

	return enumerateMalgoPlaybackDevices(ctx)
}

func enumerateMalgoPlaybackDevices(ctx *malgo.AllocatedContext) ([]OutputDevice, error) {
	raw, err := ctx.Devices(malgo.Playback)
	if err != nil {
		return nil, fmt.Errorf("list playback devices: %w", err)
	}

	out := make([]OutputDevice, 0, len(raw))
	nameCounts := make(map[string]int, len(raw))
	for i, dev := range raw {
		baseName := strings.TrimRight(dev.Name(), "\x00\r\n ")
		if baseName == "" {
			baseName = "Audio Output #" + strconv.Itoa(i+1)
		}
		nameCounts[baseName]++
		displayName := baseName
		if nameCounts[baseName] > 1 {
			displayName = baseName + " (#" + strconv.Itoa(nameCounts[baseName]) + ")"
		}
		out = append(out, OutputDevice{
			ID:        strings.ToLower(dev.ID.String()),
			Name:      displayName,
			IsDefault: dev.IsDefault != 0,
		})
	}
	return out, nil
}

// OutputDevice returns the configured output device name, or SystemDefaultDevice when using OS default.
func (e *MalgoEngine) OutputDevice() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.targetDevice == "" {
		return SystemDefaultDevice
	}
	return e.targetDevice
}

// SetOutputDevice configures the single playback device used for all soundboard audio.
func (e *MalgoEngine) SetOutputDevice(deviceName string) error {
	normalized := normalizeDeviceName(deviceName)

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return errors.New("soundboard audio engine is closed")
	}
	if e.targetDevice == normalized {
		return nil
	}
	e.targetDevice = normalized

	// If no device is open yet, defer initialization until first playback.
	if e.device == nil {
		return nil
	}
	return e.reopenDeviceLocked()
}

func normalizeDeviceName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || strings.EqualFold(trimmed, SystemDefaultDevice) {
		return ""
	}
	return trimmed
}

func (e *MalgoEngine) ensureStartedLocked() error {
	if e.closed {
		return errors.New("soundboard audio engine is closed")
	}
	if e.device != nil {
		return nil
	}
	if testing.Testing() {
		return errors.New("hardware audio playback is disabled during unit tests")
	}
	if e.ctx == nil {
		ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
		if err != nil {
			return fmt.Errorf("init audio context: %w", err)
		}
		e.ctx = ctx
	}
	return e.openDeviceLocked(e.targetDevice, false)
}

func (e *MalgoEngine) reopenDeviceLocked() error {
	if e.device != nil {
		e.device.Uninit()
		e.device = nil
	}
	if testing.Testing() {
		return nil
	}
	if e.ctx == nil {
		ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
		if err != nil {
			return fmt.Errorf("init audio context: %w", err)
		}
		e.ctx = ctx
	}
	return e.openDeviceLocked(e.targetDevice, false)
}

func (e *MalgoEngine) openDeviceLocked(targetName string, isFallback bool) error {
	cfg := malgo.DefaultDeviceConfig(malgo.Playback)
	cfg.Playback.Format = malgo.FormatF32
	cfg.Playback.Channels = MixerChannels
	cfg.SampleRate = MixerSampleRate
	cfg.Alsa.NoMMap = 1

	var targetID malgo.DeviceID
	if targetName != "" && !isFallback {
		if resolved, ok := resolveMalgoDeviceID(e.ctx, targetName); ok {
			targetID = resolved
			cfg.Playback.DeviceID = targetID.Pointer()
		}
	}

	callbacks := malgo.DeviceCallbacks{
		Data: e.onAudioCallback,
	}

	dev, err := malgo.InitDevice(e.ctx.Context, cfg, callbacks)
	if err != nil {
		if targetName != "" && !isFallback {
			if fbErr := e.openDeviceLocked("", true); fbErr == nil {
				return nil
			}
		}
		return fmt.Errorf("init audio playback device: %w", err)
	}

	if err := dev.Start(); err != nil {
		dev.Uninit()
		if targetName != "" && !isFallback {
			if fbErr := e.openDeviceLocked("", true); fbErr == nil {
				return nil
			}
		}
		return fmt.Errorf("start audio playback device: %w", err)
	}

	e.device = dev
	return nil
}

func resolveMalgoDeviceID(ctx *malgo.AllocatedContext, targetName string) (malgo.DeviceID, bool) {
	if ctx == nil || targetName == "" {
		return malgo.DeviceID{}, false
	}
	raw, err := ctx.Devices(malgo.Playback)
	if err != nil || len(raw) == 0 {
		return malgo.DeviceID{}, false
	}
	listed, listErr := enumerateMalgoPlaybackDevices(ctx)
	if listErr != nil || len(listed) != len(raw) {
		return malgo.DeviceID{}, false
	}
	for i, dev := range listed {
		if strings.EqualFold(dev.Name, targetName) || strings.EqualFold(dev.ID, targetName) {
			return raw[i].ID, true
		}
	}
	return malgo.DeviceID{}, false
}

func (e *MalgoEngine) onAudioCallback(pOutput, _ []byte, frameCount uint32) {
	frames := int(frameCount)
	if frames <= 0 {
		return
	}
	sampleCount := frames * MixerChannels
	neededBytes := sampleCount * 4
	if len(pOutput) < neededBytes {
		clear(pOutput)
		return
	}

	if cap(e.cbBuf) < sampleCount {
		e.cbBuf = make([]float32, sampleCount)
	} else {
		e.cbBuf = e.cbBuf[:sampleCount]
	}

	e.mixer.MixInto(e.cbBuf)

	for i, sample := range e.cbBuf {
		off := i * 4
		binary.LittleEndian.PutUint32(pOutput[off:off+4], math.Float32bits(sample))
	}
}

// Play starts playback of clip attributed to slotCtx at per-clip gain.
func (e *MalgoEngine) Play(slotCtx string, clip *Clip, gain float32) (uint64, error) {
	if clip == nil || len(clip.Samples) < MixerChannels {
		return 0, errors.New("audio clip is empty")
	}

	e.mu.Lock()
	err := e.ensureStartedLocked()
	e.mu.Unlock()
	if err != nil {
		return 0, err
	}

	return e.mixer.AddVoice(slotCtx, clip, gain), nil
}

// StopSlot stops active voices belonging to slotCtx.
func (e *MalgoEngine) StopSlot(slotCtx string, fadeOut time.Duration) {
	e.mixer.StopSlot(slotCtx, fadeOut)
}

// StopOthers stops all active voices not belonging to exceptSlotCtx.
func (e *MalgoEngine) StopOthers(exceptSlotCtx string, fadeOut time.Duration) {
	e.mixer.StopOthers(exceptSlotCtx, fadeOut)
}

// StopAll stops every active soundboard voice.
func (e *MalgoEngine) StopAll(fadeOut time.Duration) {
	e.mixer.StopAll(fadeOut)
}

// ActiveForSlot returns active voice count and longest remaining duration for slotCtx.
func (e *MalgoEngine) ActiveForSlot(slotCtx string) (int, time.Duration) {
	return e.mixer.ActiveForSlot(slotCtx)
}

// TotalActive returns total active voices across the soundboard.
func (e *MalgoEngine) TotalActive() int {
	return e.mixer.TotalActive()
}

// SetMasterVolume sets master soundboard output gain in [0.0, 1.0].
func (e *MalgoEngine) SetMasterVolume(vol float32) {
	e.mixer.SetMasterVolume(vol)
}

// MasterVolume returns current master soundboard gain in [0.0, 1.0].
func (e *MalgoEngine) MasterVolume() float32 {
	return e.mixer.MasterVolume()
}

// SetMuted sets master soundboard mute state.
func (e *MalgoEngine) SetMuted(muted bool) {
	e.mixer.SetMuted(muted)
}

// IsMuted reports whether master soundboard audio is muted.
func (e *MalgoEngine) IsMuted() bool {
	return e.mixer.IsMuted()
}

// SetMaxVoices updates the polyphonic voice cap.
func (e *MalgoEngine) SetMaxVoices(n int) {
	e.mixer.SetMaxVoices(n)
}

// SetOnChange registers a callback triggered whenever voices start, stop, or finish.
func (e *MalgoEngine) SetOnChange(fn func()) {
	e.mixer.SetOnChange(fn)
}

// Close stops audio hardware output and tears down the mixer cleanly.
func (e *MalgoEngine) Close() error {
	e.closeOnce.Do(func() {
		e.mu.Lock()
		e.closed = true
		dev := e.device
		e.device = nil
		ctx := e.ctx
		e.ctx = nil
		e.mu.Unlock()

		if dev != nil {
			dev.Uninit()
		}
		if ctx != nil {
			_ = ctx.Uninit()
			ctx.Free()
		}
		e.cbBuf = nil
		_ = e.mixer.Close()
	})
	return nil
}

// Ensure MalgoEngine always satisfies AudioEngine.
var _ AudioEngine = (*MalgoEngine)(nil)

func sortDeviceNames(devices []OutputDevice) []OutputDevice {
	return slices.Clone(devices)
}
