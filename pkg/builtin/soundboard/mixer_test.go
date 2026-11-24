package soundboard

import (
	"math"
	"sync/atomic"
	"testing"
	"time"
)

func makeConstClip(frames int, l, r float32) *Clip {
	samples := make([]float32, frames*MixerChannels)
	for i := range frames {
		samples[i*2] = l
		samples[i*2+1] = r
	}
	dur := time.Duration(float64(frames) * float64(time.Second) / float64(MixerSampleRate))
	return &Clip{
		Samples:    samples,
		SampleRate: MixerSampleRate,
		Duration:   dur,
	}
}

func TestMixerPolyphonicMasterVolumeMuteAndSoftLimit(t *testing.T) {
	m := NewMixer()
	t.Cleanup(func() { _ = m.Close() })

	clipA := makeConstClip(8, 0.25, 0.25)
	clipB := makeConstClip(8, 0.30, -0.10)
	m.AddVoice("slotA", clipA, 1.0)
	m.AddVoice("slotB", clipB, 1.0)

	buf := make([]float32, 4*MixerChannels)
	m.MixInto(buf)
	if math.Abs(float64(buf[0]-0.55)) > 1e-5 || math.Abs(float64(buf[1]-0.15)) > 1e-5 {
		t.Fatalf("unexpected mixed stereo frame 0: (%v, %v)", buf[0], buf[1])
	}

	m.SetMasterVolume(0.5)
	m.MixInto(buf)
	if math.Abs(float64(buf[0]-0.275)) > 1e-5 {
		t.Fatalf("expected halved master volume 0.275, got %v", buf[0])
	}

	// Verify Mute zeroes output while advancing frame cursor so voice completes.
	m.SetMuted(true)
	bufTail := make([]float32, 4*MixerChannels)
	m.MixInto(bufTail)
	for i, s := range bufTail {
		if s != 0 {
			t.Fatalf("expected silence while muted at idx %d, got %v", i, s)
		}
	}
	if m.TotalActive() != 0 {
		t.Fatalf("expected voices finished after 8 total frames, got %d", m.TotalActive())
	}

	// Verify soft-clipping compresses moderate and extreme overloads inside [-1, 1].
	m.SetMuted(false)
	m.SetMasterVolume(1.0)
	mod := makeConstClip(4, 0.5, -0.5)
	m.AddVoice("m1", mod, 1.0)
	m.AddVoice("m2", mod, 1.0) // sum = 1.0 -> strictly in (0.95, 1.0)
	outMod := make([]float32, 4*MixerChannels)
	m.MixInto(outMod)
	if outMod[0] <= 0.95 || outMod[0] >= 1.0 || outMod[1] >= -0.95 || outMod[1] <= -1.0 {
		t.Fatalf("expected moderate soft-limited output in (0.95, 1.0), got (%v, %v)", outMod[0], outMod[1])
	}
	m.StopAll(0)

	loud := makeConstClip(4, 0.9, -0.9)
	m.AddVoice("l1", loud, 1.0)
	m.AddVoice("l2", loud, 1.0) // sum = 1.8 -> bounded <= 1.0
	out := make([]float32, 4*MixerChannels)
	m.MixInto(out)
	if out[0] <= 0.95 || out[0] > 1.0 || out[1] >= -0.95 || out[1] < -1.0 {
		t.Fatalf("expected extreme soft-limited output in (0.95, 1.0], got (%v, %v)", out[0], out[1])
	}
}

func TestMixerStopSlotFadeOutAndMaxVoices(t *testing.T) {
	m := NewMixer()
	t.Cleanup(func() { _ = m.Close() })

	var changes atomic.Int32
	doneCh := make(chan struct{}, 8)
	m.SetOnChange(func() {
		changes.Add(1)
		select {
		case doneCh <- struct{}{}:
		default:
		}
	})

	longClip := makeConstClip(MixerSampleRate/10, 0.5, 0.5) // 100ms
	m.AddVoice("slotA", longClip, 1.0)
	m.AddVoice("slotB", longClip, 1.0)

	if count, rem := m.ActiveForSlot("slotA"); count != 1 || rem <= 0 {
		t.Fatalf("unexpected ActiveForSlot(slotA): count=%d rem=%v", count, rem)
	}

	m.StopSlot("slotA", 0)
	if count, _ := m.ActiveForSlot("slotA"); count != 0 {
		t.Fatalf("expected slotA stopped immediately, got count=%d", count)
	}
	if count, _ := m.ActiveForSlot("slotB"); count != 1 {
		t.Fatalf("expected slotB untouched, got count=%d", count)
	}

	// Fade slotB over 1ms (48 frames) and ensure sample amplitude tapers down monotonically and stops.
	m.StopSlot("slotB", 1*time.Millisecond)
	fadeBuf := make([]float32, 60*MixerChannels)
	m.MixInto(fadeBuf)
	if fadeBuf[0] <= 0.4 || fadeBuf[40*2] >= fadeBuf[0] || fadeBuf[50*2] != 0 {
		t.Fatalf("unexpected fade envelope: start=%v mid=%v after=%v", fadeBuf[0], fadeBuf[40*2], fadeBuf[50*2])
	}
	if m.TotalActive() != 0 {
		t.Fatalf("expected faded voice removed, got TotalActive=%d", m.TotalActive())
	}

	// Verify maxVoices drops oldest voice cleanly.
	m.SetMaxVoices(2)
	m.AddVoice("s1", longClip, 1.0)
	m.AddVoice("s2", longClip, 1.0)
	m.AddVoice("s3", longClip, 1.0)
	if total := m.TotalActive(); total != 2 {
		t.Fatalf("expected TotalActive capped at 2, got %d", total)
	}
	if c1, _ := m.ActiveForSlot("s1"); c1 != 0 {
		t.Fatalf("expected oldest voice s1 evicted, got count=%d", c1)
	}

	m.StopAll(0)
	if m.TotalActive() != 0 {
		t.Fatalf("expected 0 voices after StopAll, got %d", m.TotalActive())
	}

	select {
	case <-doneCh:
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for SetOnChange callback")
	}
}

func TestMixerStopOthersPreservesTargetSlot(t *testing.T) {
	m := NewMixer()
	t.Cleanup(func() { _ = m.Close() })

	longClip := makeConstClip(MixerSampleRate/10, 0.5, 0.5)
	m.AddVoice("slotA", longClip, 1.0)
	m.AddVoice("slotB", longClip, 1.0)
	m.AddVoice("slotC", longClip, 1.0)

	m.StopOthers("slotB", 0)

	if c, _ := m.ActiveForSlot("slotA"); c != 0 {
		t.Fatalf("expected slotA stopped by StopOthers, got count=%d", c)
	}
	if c, _ := m.ActiveForSlot("slotB"); c != 1 {
		t.Fatalf("expected target slotB preserved, got count=%d", c)
	}
	if c, _ := m.ActiveForSlot("slotC"); c != 0 {
		t.Fatalf("expected slotC stopped by StopOthers, got count=%d", c)
	}
}
