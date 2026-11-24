package soundboard

import (
	"math"
	"slices"
	"sync"
	"time"
)

type activeVoice struct {
	id            uint64
	slotCtx       string
	samples       []float32
	cursor        int
	gain          float32
	stopping      bool
	fadeRemaining int
	fadeTotal     int
}

// Mixer mixes multiple concurrent stereo float32 soundboard voices into an output buffer.
type Mixer struct {
	mu        sync.Mutex
	voices    []activeVoice
	nextID    uint64
	masterVol float32
	muted     bool
	maxVoices int

	cbMu      sync.RWMutex
	onChange  func()
	notifyCh  chan struct{}
	stopCh    chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// NewMixer creates a polyphonic stereo mixer at full volume with DefaultMaxVoices capacity.
func NewMixer() *Mixer {
	m := &Mixer{
		masterVol: 1.0,
		maxVoices: DefaultMaxVoices,
		notifyCh:  make(chan struct{}, 1),
		stopCh:    make(chan struct{}),
	}
	m.wg.Go(m.notifyLoop)
	return m
}

func (m *Mixer) notifyLoop() {
	for {
		select {
		case <-m.stopCh:
			return
		case <-m.notifyCh:
			m.cbMu.RLock()
			fn := m.onChange
			m.cbMu.RUnlock()
			if fn != nil {
				fn()
			}
		}
	}
}

func (m *Mixer) triggerNotify() {
	select {
	case m.notifyCh <- struct{}{}:
	default:
	}
}

// SetOnChange registers a callback invoked asynchronously whenever active voices start, stop, or finish.
func (m *Mixer) SetOnChange(fn func()) {
	m.cbMu.Lock()
	m.onChange = fn
	m.cbMu.Unlock()
}

// Close shuts down the background notification loop and clears all voices.
func (m *Mixer) Close() error {
	m.closeOnce.Do(func() {
		close(m.stopCh)
		m.wg.Wait()
		m.mu.Lock()
		m.voices = nil
		m.mu.Unlock()
	})
	return nil
}

// SetMasterVolume sets master playback gain clamped to [0.0, 1.0].
func (m *Mixer) SetMasterVolume(vol float32) {
	clamped := clampFloat32(vol, 0.0, 1.0)
	m.mu.Lock()
	m.masterVol = clamped
	m.mu.Unlock()
}

// MasterVolume returns the current master playback gain in [0.0, 1.0].
func (m *Mixer) MasterVolume() float32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.masterVol
}

// SetMuted mutes or unmutes mixer output while continuing active voice playback timing.
func (m *Mixer) SetMuted(muted bool) {
	m.mu.Lock()
	m.muted = muted
	m.mu.Unlock()
}

// IsMuted reports whether master output is currently muted.
func (m *Mixer) IsMuted() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.muted
}

// SetMaxVoices sets the maximum simultaneous voice count clamped to [1, MaxAllowedVoices].
func (m *Mixer) SetMaxVoices(n int) {
	clamped := min(max(n, 1), MaxAllowedVoices)
	trimmed := false
	m.mu.Lock()
	m.maxVoices = clamped
	if len(m.voices) > clamped {
		drop := len(m.voices) - clamped
		m.voices = slices.Delete(m.voices, 0, drop)
		trimmed = true
	}
	m.mu.Unlock()
	if trimmed {
		m.triggerNotify()
	}
}

// AddVoice queues clip for playback attributed to slotCtx at per-voice gain (clamped to [0.0, 1.5]).
func (m *Mixer) AddVoice(slotCtx string, clip *Clip, gain float32) uint64 {
	if clip == nil || len(clip.Samples) < MixerChannels {
		return 0
	}
	clampedGain := clampFloat32(gain, 0.0, 1.5)

	m.mu.Lock()
	m.nextID++
	id := m.nextID
	if len(m.voices) >= m.maxVoices {
		drop := len(m.voices) - m.maxVoices + 1
		m.voices = slices.Delete(m.voices, 0, drop)
	}
	m.voices = append(m.voices, activeVoice{
		id:      id,
		slotCtx: slotCtx,
		samples: clip.Samples,
		gain:    clampedGain,
	})
	m.mu.Unlock()

	m.triggerNotify()
	return id
}

// StopSlot stops all active voices belonging to slotCtx immediately or via a linear fade-out.
func (m *Mixer) StopSlot(slotCtx string, fadeOut time.Duration) {
	m.stopMatching(fadeOut, func(v activeVoice) bool {
		return v.slotCtx == slotCtx
	})
}

// StopOthers stops all active voices not belonging to exceptSlotCtx immediately or via a linear fade-out.
func (m *Mixer) StopOthers(exceptSlotCtx string, fadeOut time.Duration) {
	m.stopMatching(fadeOut, func(v activeVoice) bool {
		return v.slotCtx != exceptSlotCtx
	})
}

// StopAll stops all currently playing voices immediately or via a linear fade-out.
func (m *Mixer) StopAll(fadeOut time.Duration) {
	m.stopMatching(fadeOut, func(activeVoice) bool {
		return true
	})
}

func (m *Mixer) stopMatching(fadeOut time.Duration, match func(activeVoice) bool) {
	fadeSamples := fadeOutToSamples(fadeOut)
	changed := false

	m.mu.Lock()
	if fadeSamples == 0 {
		kept := m.voices[:0]
		for _, v := range m.voices {
			if match(v) {
				changed = true
				continue
			}
			kept = append(kept, v)
		}
		clear(m.voices[len(kept):])
		m.voices = kept
	} else {
		for i := range m.voices {
			v := &m.voices[i]
			if !match(*v) {
				continue
			}
			if !v.stopping || v.fadeRemaining > fadeSamples {
				v.stopping = true
				v.fadeRemaining = fadeSamples
				v.fadeTotal = fadeSamples
				changed = true
			}
		}
	}
	m.mu.Unlock()

	if changed {
		m.triggerNotify()
	}
}

func fadeOutToSamples(fadeOut time.Duration) int {
	if fadeOut <= 0 {
		return 0
	}
	frames := max(int(fadeOut.Seconds()*float64(MixerSampleRate)), 1)
	return frames * MixerChannels
}

// ActiveForSlot returns the active voice count and longest remaining duration for slotCtx.
func (m *Mixer) ActiveForSlot(slotCtx string) (int, time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	count := 0
	maxRemSamples := 0
	for _, v := range m.voices {
		if v.slotCtx != slotCtx {
			continue
		}
		count++
		rem := len(v.samples) - v.cursor
		if v.stopping && v.fadeRemaining < rem {
			rem = v.fadeRemaining
		}
		if rem > maxRemSamples {
			maxRemSamples = rem
		}
	}
	if count == 0 {
		return 0, 0
	}
	frames := maxRemSamples / MixerChannels
	dur := time.Duration(float64(frames) * float64(time.Second) / float64(MixerSampleRate))
	return count, dur
}

// TotalActive returns the total number of voices currently active in the mixer.
func (m *Mixer) TotalActive() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.voices)
}

// MixInto renders the next interleaved stereo float32 buffer of audio into out.
// It is safe for real-time audio callback threads and never allocates or blocks on callbacks.
func (m *Mixer) MixInto(out []float32) {
	clear(out)
	alignedLen := len(out) - (len(out) % MixerChannels)
	if alignedLen == 0 {
		return
	}

	finishedAny := false

	m.mu.Lock()
	master := m.masterVol
	if m.muted {
		master = 0
	}

	kept := m.voices[:0]
	for i := range m.voices {
		v := m.voices[i]
		for frameIdx := 0; frameIdx < alignedLen; frameIdx += MixerChannels {
			if v.cursor+1 >= len(v.samples) || (v.stopping && v.fadeRemaining <= 0) {
				break
			}
			fadeMult := float32(1.0)
			if v.stopping && v.fadeTotal > 0 {
				fadeMult = float32(v.fadeRemaining) / float32(v.fadeTotal)
				v.fadeRemaining -= MixerChannels
			}
			scale := v.gain * fadeMult * master
			if scale != 0 {
				out[frameIdx] += v.samples[v.cursor] * scale
				out[frameIdx+1] += v.samples[v.cursor+1] * scale
			}
			v.cursor += MixerChannels
		}

		if v.cursor+1 < len(v.samples) && (!v.stopping || v.fadeRemaining > 0) {
			kept = append(kept, v)
		} else {
			finishedAny = true
		}
	}
	clear(m.voices[len(kept):])
	m.voices = kept
	m.mu.Unlock()

	for i := range alignedLen {
		out[i] = softLimitSample(out[i])
	}

	if finishedAny {
		m.triggerNotify()
	}
}

func softLimitSample(x float32) float32 {
	const knee = float32(0.95)
	const headroom = float32(0.05)
	ax := float32(math.Abs(float64(x)))
	if ax <= knee {
		return x
	}
	compressed := knee + headroom*float32(math.Tanh(float64((ax-knee)/headroom)))
	if x < 0 {
		return -compressed
	}
	return compressed
}

func clampFloat32(v, lo, hi float32) float32 {
	return min(max(v, lo), hi)
}
