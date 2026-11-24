package soundboard

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func buildWAVBytes(audioFormat uint16, channels, sampleRate, bitsPerSample int, pcmData, extraChunk []byte) []byte {
	var body bytes.Buffer

	if len(extraChunk) > 0 {
		body.WriteString("JUNK")
		_ = binary.Write(&body, binary.LittleEndian, uint32(len(extraChunk)))
		body.Write(extraChunk)
		if len(extraChunk)%2 == 1 {
			body.WriteByte(0)
		}
	}

	var fmtBuf [16]byte
	blockAlign := channels * (bitsPerSample / 8)
	byteRate := sampleRate * blockAlign
	binary.LittleEndian.PutUint16(fmtBuf[0:2], audioFormat)
	binary.LittleEndian.PutUint16(fmtBuf[2:4], uint16(channels))
	binary.LittleEndian.PutUint32(fmtBuf[4:8], uint32(sampleRate))
	binary.LittleEndian.PutUint32(fmtBuf[8:12], uint32(byteRate))
	binary.LittleEndian.PutUint16(fmtBuf[12:14], uint16(blockAlign))
	binary.LittleEndian.PutUint16(fmtBuf[14:16], uint16(bitsPerSample))

	body.WriteString("fmt ")
	_ = binary.Write(&body, binary.LittleEndian, uint32(len(fmtBuf)))
	body.Write(fmtBuf[:])

	body.WriteString("data")
	_ = binary.Write(&body, binary.LittleEndian, uint32(len(pcmData)))
	body.Write(pcmData)
	if len(pcmData)%2 == 1 {
		body.WriteByte(0)
	}

	var out bytes.Buffer
	out.WriteString("RIFF")
	_ = binary.Write(&out, binary.LittleEndian, uint32(4+body.Len()))
	out.WriteString("WAVE")
	out.Write(body.Bytes())
	return out.Bytes()
}

func buildWAVExtensibleBytes(subFormatCode byte, channels, sampleRate, bitsPerSample int, pcmData []byte) []byte {
	var fmtBuf [40]byte
	blockAlign := channels * (bitsPerSample / 8)
	byteRate := sampleRate * blockAlign
	binary.LittleEndian.PutUint16(fmtBuf[0:2], 0xFFFE)
	binary.LittleEndian.PutUint16(fmtBuf[2:4], uint16(channels))
	binary.LittleEndian.PutUint32(fmtBuf[4:8], uint32(sampleRate))
	binary.LittleEndian.PutUint32(fmtBuf[8:12], uint32(byteRate))
	binary.LittleEndian.PutUint16(fmtBuf[12:14], uint16(blockAlign))
	binary.LittleEndian.PutUint16(fmtBuf[14:16], uint16(bitsPerSample))
	binary.LittleEndian.PutUint16(fmtBuf[16:18], 22)
	binary.LittleEndian.PutUint16(fmtBuf[18:20], uint16(bitsPerSample))
	guid := []byte{
		subFormatCode, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x10, 0x00,
		0x80, 0x00, 0x00, 0xAA,
		0x00, 0x38, 0x9B, 0x71,
	}
	copy(fmtBuf[24:40], guid)

	var body bytes.Buffer
	body.WriteString("fmt ")
	_ = binary.Write(&body, binary.LittleEndian, uint32(len(fmtBuf)))
	body.Write(fmtBuf[:])
	body.WriteString("data")
	_ = binary.Write(&body, binary.LittleEndian, uint32(len(pcmData)))
	body.Write(pcmData)

	var out bytes.Buffer
	out.WriteString("RIFF")
	_ = binary.Write(&out, binary.LittleEndian, uint32(4+body.Len()))
	out.WriteString("WAVE")
	out.Write(body.Bytes())
	return out.Bytes()
}

func TestDecodeWAVFormatsAndResampling(t *testing.T) {
	t.Run("PCM16 stereo with odd JUNK chunk", func(t *testing.T) {
		var pcm bytes.Buffer
		_ = binary.Write(&pcm, binary.LittleEndian, int16(16384))  // +0.5
		_ = binary.Write(&pcm, binary.LittleEndian, int16(-16384)) // -0.5
		wav := buildWAVBytes(1, 2, MixerSampleRate, 16, pcm.Bytes(), []byte{1, 2, 3})

		clip, err := DecodeReader(bytes.NewReader(wav), ".wav")
		if err != nil {
			t.Fatalf("DecodeReader() error = %v", err)
		}
		if clip.SampleRate != MixerSampleRate || len(clip.Samples) != 2 {
			t.Fatalf("unexpected clip metadata: rate=%d samples=%d", clip.SampleRate, len(clip.Samples))
		}
		if math.Abs(float64(clip.Samples[0]-0.5)) > 1e-3 || math.Abs(float64(clip.Samples[1]+0.5)) > 1e-3 {
			t.Fatalf("unexpected decoded PCM16 samples: %v", clip.Samples)
		}
	})

	t.Run("PCM8 mono", func(t *testing.T) {
		// 192 -> (192-128)/128 = +0.5
		wav := buildWAVBytes(1, 1, MixerSampleRate, 8, []byte{192}, nil)
		clip, err := DecodeReader(bytes.NewReader(wav), ".wav")
		if err != nil {
			t.Fatalf("DecodeReader(PCM8) error = %v", err)
		}
		if len(clip.Samples) != 2 || math.Abs(float64(clip.Samples[0]-0.5)) > 1e-3 || clip.Samples[0] != clip.Samples[1] {
			t.Fatalf("expected mono duplicated to stereo +0.5, got %v", clip.Samples)
		}
	})

	t.Run("PCM24 mono resampled 24kHz to 48kHz", func(t *testing.T) {
		// 24000 frames @ 24kHz = 1.0s -> 48000 stereo frames after resampling.
		var pcm bytes.Buffer
		val := int32(4194304) // 0.5 * 2^23
		b0, b1, b2 := byte(val), byte(val>>8), byte(val>>16)
		for range 240 {
			pcm.Write([]byte{b0, b1, b2})
		}
		wav := buildWAVBytes(1, 1, 24000, 24, pcm.Bytes(), nil)
		clip, err := DecodeReader(bytes.NewReader(wav), ".wav")
		if err != nil {
			t.Fatalf("DecodeReader(PCM24) error = %v", err)
		}
		if len(clip.Samples) != 480*2 {
			t.Fatalf("expected 960 stereo samples after 24k->48k resample, got %d", len(clip.Samples))
		}
		if math.Abs(float64(clip.Samples[10]-0.5)) > 1e-3 {
			t.Fatalf("unexpected PCM24 level: %v", clip.Samples[10])
		}
	})

	t.Run("IEEE float32 with NaN sanitized and PCM32", func(t *testing.T) {
		var fbuf bytes.Buffer
		_ = binary.Write(&fbuf, binary.LittleEndian, float32(0.25))
		_ = binary.Write(&fbuf, binary.LittleEndian, float32(math.NaN()))
		wavFloat := buildWAVBytes(3, 2, MixerSampleRate, 32, fbuf.Bytes(), nil)
		clipF, err := DecodeReader(bytes.NewReader(wavFloat), ".wav")
		if err != nil {
			t.Fatalf("DecodeReader(float32) error = %v", err)
		}
		if clipF.Samples[0] != 0.25 || clipF.Samples[1] != 0 {
			t.Fatalf("expected NaN sanitized to 0, got %v", clipF.Samples)
		}

		var p32 bytes.Buffer
		_ = binary.Write(&p32, binary.LittleEndian, int32(1<<30)) // +0.5
		_ = binary.Write(&p32, binary.LittleEndian, int32(-(1 << 30)))
		wav32 := buildWAVBytes(1, 2, MixerSampleRate, 32, p32.Bytes(), nil)
		clip32, err := DecodeReader(bytes.NewReader(wav32), ".wav")
		if err != nil {
			t.Fatalf("DecodeReader(PCM32) error = %v", err)
		}
		if math.Abs(float64(clip32.Samples[0]-0.5)) > 1e-4 {
			t.Fatalf("unexpected PCM32 level: %v", clip32.Samples)
		}
	})

	t.Run("Extensible WAV and 4-channel downmix", func(t *testing.T) {
		var p bytes.Buffer
		for _, v := range []int16{16384, 16384, 0, 0} {
			_ = binary.Write(&p, binary.LittleEndian, v)
		}
		wav := buildWAVExtensibleBytes(1, 4, MixerSampleRate, 16, p.Bytes())
		clip, err := DecodeReader(bytes.NewReader(wav), ".wav")
		if err != nil {
			t.Fatalf("DecodeReader(Extensible) error = %v", err)
		}
		if len(clip.Samples) != 2 || math.Abs(float64(clip.Samples[0]-0.25)) > 1e-3 {
			t.Fatalf("expected 4ch avg 0.25 stereo, got %v", clip.Samples)
		}
	})

	t.Run("Magic-byte sniffing without extension", func(t *testing.T) {
		var p bytes.Buffer
		_ = binary.Write(&p, binary.LittleEndian, int16(8192))
		_ = binary.Write(&p, binary.LittleEndian, int16(8192))
		wav := buildWAVBytes(1, 2, MixerSampleRate, 16, p.Bytes(), nil)
		clip, err := DecodeReader(bytes.NewReader(wav), "")
		if err != nil {
			t.Fatalf("DecodeReader(sniff) error = %v", err)
		}
		if len(clip.Samples) != 2 {
			t.Fatalf("unexpected sniffed clip samples len = %d", len(clip.Samples))
		}
	})
}

func TestDecodeFileAndErrors(t *testing.T) {
	dir := t.TempDir()

	if _, err := DecodeFile(""); err == nil {
		t.Fatal("expected error for empty path")
	}
	if _, err := DecodeFile(filepath.Join(dir, "missing.wav")); err == nil {
		t.Fatal("expected error for missing file")
	}
	if _, err := DecodeFile(dir); err == nil {
		t.Fatal("expected error for directory path")
	}

	badPath := filepath.Join(dir, "bad.mp3")
	if err := os.WriteFile(badPath, []byte("not real audio"), 0o644); err != nil {
		t.Fatalf("WriteFile(bad) error = %v", err)
	}
	if _, err := DecodeFile(badPath); err == nil {
		t.Fatal("expected decode error for corrupt mp3 data")
	}

	var p bytes.Buffer
	_ = binary.Write(&p, binary.LittleEndian, int16(16384))
	_ = binary.Write(&p, binary.LittleEndian, int16(16384))
	goodPath := filepath.Join(dir, "ok.wav")
	if err := os.WriteFile(goodPath, buildWAVBytes(1, 2, MixerSampleRate, 16, p.Bytes(), nil), 0o644); err != nil {
		t.Fatalf("WriteFile(good) error = %v", err)
	}
	clip, err := DecodeFile(goodPath)
	if err != nil {
		t.Fatalf("DecodeFile(good) error = %v", err)
	}
	if len(clip.Samples) != 2 {
		t.Fatalf("expected 2 samples, got %d", len(clip.Samples))
	}
}

func TestClipCacheHitModTimeAndEviction(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.wav")
	pathB := filepath.Join(dir, "b.wav")

	if err := os.WriteFile(pathA, []byte("a"), 0o644); err != nil {
		t.Fatalf("WriteFile(a) error = %v", err)
	}
	if err := os.WriteFile(pathB, []byte("b"), 0o644); err != nil {
		t.Fatalf("WriteFile(b) error = %v", err)
	}

	var decodeCount atomic.Int32
	// Budget = 24 bytes -> fits one 4-sample (16-byte) clip, evicts on second.
	cache := newClipCache(24, func(string) (*Clip, error) {
		decodeCount.Add(1)
		return &Clip{
			Samples:    []float32{0.1, 0.1, 0.2, 0.2}, // 16 bytes
			SampleRate: MixerSampleRate,
		}, nil
	})

	infoA1, err := os.Stat(pathA)
	if err != nil {
		t.Fatalf("Stat(a) error = %v", err)
	}
	if _, ok := cache.lookup(pathA, infoA1); ok {
		t.Fatal("expected cache miss before store")
	}
	clipA, err := cache.decode(pathA)
	if err != nil {
		t.Fatalf("decode(a) error = %v", err)
	}
	cache.store(pathA, infoA1, clipA)

	if got, ok := cache.lookup(pathA, infoA1); !ok || got != clipA {
		t.Fatal("expected cache hit for unchanged a.wav")
	}
	if decodeCount.Load() != 1 {
		t.Fatalf("expected 1 decode so far, got %d", decodeCount.Load())
	}

	// Updating file size + mtime creates a cache miss.
	if err := os.WriteFile(pathA, []byte("updated-bytes"), 0o644); err != nil {
		t.Fatalf("WriteFile(a2) error = %v", err)
	}
	future := time.Now().Add(5 * time.Second)
	_ = os.Chtimes(pathA, future, future)
	infoA2, _ := os.Stat(pathA)
	if _, ok := cache.lookup(pathA, infoA2); ok {
		t.Fatal("expected cache miss after file size/mtime changed")
	}

	// Storing clipB within 24-byte budget evicts clipA1.
	infoB, _ := os.Stat(pathB)
	clipB, _ := cache.decode(pathB)
	cache.store(pathB, infoB, clipB)
	if _, ok := cache.lookup(pathA, infoA1); ok {
		t.Fatal("expected clipA1 evicted when exceeding maxBytes budget")
	}
	if _, ok := cache.lookup(pathB, infoB); !ok {
		t.Fatal("expected clipB retained in cache")
	}
}
