package soundboard

import (
	"bytes"
	"container/list"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hajimehoshi/go-mp3"
	"github.com/jfreymuth/oggvorbis"
	"github.com/mewkiz/flac"
)

const (
	maxInputFileBytes       = 256 * 1024 * 1024
	maxClipDuration         = 15 * time.Minute
	maxDecodedFramesPerFile = MixerSampleRate * int(maxClipDuration/time.Second)
	defaultCacheMaxBytes    = 64 * 1024 * 1024
	maxCacheableClipBytes   = 32 * 1024 * 1024
)

// SupportedExtensions lists audio file extensions supported by the Soundboard plugin.
var SupportedExtensions = []string{".wav", ".mp3", ".ogg", ".flac"}

// DecodeFile reads and decodes path into a MixerSampleRate interleaved stereo Clip.
func DecodeFile(path string) (*Clip, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, errors.New("audio file path is empty")
	}
	info, err := os.Stat(trimmed)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%q is a directory, expected audio file", trimmed)
	}
	if info.Size() > maxInputFileBytes {
		return nil, fmt.Errorf("audio file exceeds maximum supported size (%d MB)", maxInputFileBytes/(1024*1024))
	}
	f, err := os.Open(trimmed)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = f.Close()
	}()
	return DecodeReader(f, filepath.Ext(trimmed))
}

// DecodeReader reads and decodes an audio stream (WAV, MP3, OGG Vorbis, or FLAC) into a stereo MixerSampleRate Clip.
func DecodeReader(r io.Reader, extHint string) (*Clip, error) {
	if r == nil {
		return nil, errors.New("audio reader is nil")
	}
	limited := io.LimitReader(r, maxInputFileBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read audio stream: %w", err)
	}
	if int64(len(data)) > maxInputFileBytes {
		return nil, fmt.Errorf("audio stream exceeds maximum supported size (%d MB)", maxInputFileBytes/(1024*1024))
	}

	format := detectAudioFormat(extHint, data)
	switch format {
	case ".wav":
		return decodeWAV(data)
	case ".mp3":
		return decodeMP3(data)
	case ".ogg":
		return decodeOGG(data)
	case ".flac":
		return decodeFLAC(data)
	default:
		return nil, fmt.Errorf("unsupported audio format %q (expected .wav, .mp3, .ogg, or .flac)", format)
	}
}

func detectAudioFormat(extHint string, data []byte) string {
	switch strings.ToLower(strings.TrimSpace(extHint)) {
	case ".wav", "wav", ".wave", "wave":
		return ".wav"
	case ".mp3", "mp3":
		return ".mp3"
	case ".ogg", "ogg", ".oga", "oga":
		return ".ogg"
	case ".flac", "flac":
		return ".flac"
	}

	if len(data) >= 12 && bytes.Equal(data[0:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WAVE")) {
		return ".wav"
	}
	if len(data) >= 4 && bytes.Equal(data[0:4], []byte("fLaC")) {
		return ".flac"
	}
	if len(data) >= 4 && bytes.Equal(data[0:4], []byte("OggS")) {
		return ".ogg"
	}
	if (len(data) >= 3 && bytes.Equal(data[0:3], []byte("ID3"))) ||
		(len(data) >= 2 && data[0] == 0xFF && (data[1]&0xE0) == 0xE0) {
		return ".mp3"
	}
	return strings.ToLower(strings.TrimSpace(extHint))
}

func buildNormalizedClip(interleaved []float32, channels, srcRate int) (*Clip, error) {
	if channels <= 0 {
		return nil, errors.New("audio stream has 0 channels")
	}
	if srcRate <= 0 {
		return nil, fmt.Errorf("invalid audio sample rate %d", srcRate)
	}
	if len(interleaved) < channels {
		return nil, errors.New("audio stream contains no samples")
	}
	stereo := toStereo(interleaved, channels)
	resampled := resampleStereo(stereo, srcRate, MixerSampleRate)
	if len(resampled) < MixerChannels {
		return nil, errors.New("decoded audio clip is empty")
	}
	frames := len(resampled) / MixerChannels
	if frames > maxDecodedFramesPerFile {
		return nil, fmt.Errorf("decoded clip exceeds %v limit", maxClipDuration)
	}
	dur := time.Duration(float64(frames) * float64(time.Second) / float64(MixerSampleRate))
	return &Clip{
		Samples:    resampled,
		SampleRate: MixerSampleRate,
		Duration:   dur,
	}, nil
}

func toStereo(interleaved []float32, channels int) []float32 {
	frames := len(interleaved) / channels
	if frames <= 0 {
		return nil
	}
	if channels == MixerChannels {
		return interleaved[:frames*MixerChannels]
	}
	out := make([]float32, frames*MixerChannels)
	if channels == 1 {
		for i := range frames {
			s := interleaved[i]
			out[i*2] = s
			out[i*2+1] = s
		}
		return out
	}

	inv := float32(1.0) / float32(channels)
	for i := range frames {
		base := i * channels
		var sum float32
		for ch := range channels {
			sum += interleaved[base+ch]
		}
		mono := sum * inv
		out[i*2] = mono
		out[i*2+1] = mono
	}
	return out
}

func resampleStereo(stereo []float32, srcRate, dstRate int) []float32 {
	if len(stereo) < MixerChannels || srcRate <= 0 || dstRate <= 0 || srcRate == dstRate {
		return stereo
	}
	srcFrames := len(stereo) / MixerChannels
	if srcFrames <= 1 {
		return stereo
	}
	dstFrames := min(max(int(math.Round(float64(srcFrames)*float64(dstRate)/float64(srcRate))), 1), maxDecodedFramesPerFile)

	out := make([]float32, dstFrames*MixerChannels)
	ratio := float64(srcRate) / float64(dstRate)
	lastSrc := srcFrames - 1

	for i := range dstFrames {
		pos := float64(i) * ratio
		idx := min(int(pos), lastSrc)
		frac := float32(pos - float64(idx))
		next := min(idx+1, lastSrc)

		l0, r0 := stereo[idx*2], stereo[idx*2+1]
		l1, r1 := stereo[next*2], stereo[next*2+1]
		out[i*2] = l0 + (l1-l0)*frac
		out[i*2+1] = r0 + (r1-r0)*frac
	}
	return out
}

func decodeWAV(data []byte) (*Clip, error) {
	if len(data) < 12 || !bytes.Equal(data[0:4], []byte("RIFF")) || !bytes.Equal(data[8:12], []byte("WAVE")) {
		return nil, errors.New("invalid WAV file: missing RIFF/WAVE header")
	}

	var (
		hasFmt        bool
		audioFormat   uint16
		channels      int
		sampleRate    int
		bitsPerSample int
		pcmBytes      []byte
	)

	offset := 12
	for offset+8 <= len(data) {
		chunkID := string(data[offset : offset+4])
		chunkSize := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		offset += 8
		if chunkSize < 0 || offset+chunkSize > len(data) {
			return nil, fmt.Errorf("invalid WAV %q chunk size %d", chunkID, chunkSize)
		}
		payload := data[offset : offset+chunkSize]
		switch chunkID {
		case "fmt ":
			var err error
			audioFormat, channels, sampleRate, bitsPerSample, err = parseWAVFmt(payload)
			if err != nil {
				return nil, err
			}
			hasFmt = true
		case "data":
			pcmBytes = payload
		}
		offset += chunkSize
		if chunkSize%2 == 1 && offset < len(data) {
			offset++
		}
		if hasFmt && pcmBytes != nil {
			break
		}
	}

	if !hasFmt {
		return nil, errors.New("invalid WAV file: missing fmt chunk")
	}
	if len(pcmBytes) == 0 {
		return nil, errors.New("invalid WAV file: missing or empty data chunk")
	}

	bytesPerSample := bitsPerSample / 8
	frameStride := channels * bytesPerSample
	if frameStride <= 0 {
		return nil, errors.New("invalid WAV frame layout")
	}
	frames := len(pcmBytes) / frameStride
	if frames <= 0 {
		return nil, errors.New("WAV data chunk contains no complete frames")
	}
	estOutFrames := int(float64(frames) * float64(MixerSampleRate) / float64(sampleRate))
	if estOutFrames > maxDecodedFramesPerFile {
		return nil, fmt.Errorf("WAV duration exceeds %v limit", maxClipDuration)
	}

	totalSamples := frames * channels
	samples := make([]float32, totalSamples)
	for i := range totalSamples {
		p := pcmBytes[i*bytesPerSample : (i+1)*bytesPerSample]
		samples[i] = decodeWAVSample(audioFormat, bitsPerSample, p)
	}
	return buildNormalizedClip(samples, channels, sampleRate)
}

func parseWAVFmt(payload []byte) (audioFormat uint16, channels, sampleRate, bitsPerSample int, err error) {
	if len(payload) < 16 {
		return 0, 0, 0, 0, errors.New("invalid WAV fmt chunk: too short")
	}
	audioFormat = binary.LittleEndian.Uint16(payload[0:2])
	channels = int(binary.LittleEndian.Uint16(payload[2:4]))
	sampleRate = int(binary.LittleEndian.Uint32(payload[4:8]))
	bitsPerSample = int(binary.LittleEndian.Uint16(payload[14:16]))

	if audioFormat == 0xFFFE {
		if len(payload) < 40 {
			return 0, 0, 0, 0, errors.New("invalid WAV extensible fmt chunk")
		}
		subGUID := payload[24:40]
		switch {
		case isWAVExtensibleSubformat(subGUID, 1):
			audioFormat = 1
		case isWAVExtensibleSubformat(subGUID, 3):
			audioFormat = 3
		default:
			return 0, 0, 0, 0, errors.New("unsupported WAV extensible subformat")
		}
	}

	if channels < 1 || channels > 16 {
		return 0, 0, 0, 0, fmt.Errorf("unsupported WAV channel count %d", channels)
	}
	if sampleRate < 1000 || sampleRate > 384000 {
		return 0, 0, 0, 0, fmt.Errorf("unsupported WAV sample rate %d", sampleRate)
	}

	switch audioFormat {
	case 1:
		switch bitsPerSample {
		case 8, 16, 24, 32:
		default:
			return 0, 0, 0, 0, fmt.Errorf("unsupported WAV PCM bit depth %d", bitsPerSample)
		}
	case 3:
		switch bitsPerSample {
		case 32, 64:
		default:
			return 0, 0, 0, 0, fmt.Errorf("unsupported WAV float bit depth %d", bitsPerSample)
		}
	default:
		return 0, 0, 0, 0, fmt.Errorf("unsupported WAV audio format %d (expected PCM or IEEE float)", audioFormat)
	}
	return audioFormat, channels, sampleRate, bitsPerSample, nil
}

func isWAVExtensibleSubformat(guid []byte, formatCode byte) bool {
	// KSDATAFORMAT_SUBTYPE base suffix: 0000-0010-8000-00aa00389b71
	expectedTail := []byte{0x00, 0x00, 0x10, 0x00, 0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71}
	return len(guid) == 16 &&
		guid[0] == formatCode &&
		guid[1] == 0x00 &&
		guid[2] == 0x00 &&
		guid[3] == 0x00 &&
		bytes.Equal(guid[4:16], expectedTail)
}

func decodeWAVSample(audioFormat uint16, bitsPerSample int, p []byte) float32 {
	if audioFormat == 3 {
		if bitsPerSample == 32 {
			v := math.Float32frombits(binary.LittleEndian.Uint32(p))
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				return 0
			}
			return v
		}
		v := math.Float64frombits(binary.LittleEndian.Uint64(p))
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0
		}
		return float32(v)
	}

	switch bitsPerSample {
	case 8:
		return (float32(p[0]) - 128.0) / 128.0
	case 16:
		return float32(int16(binary.LittleEndian.Uint16(p))) / 32768.0
	case 24:
		v := int32(p[0]) | int32(p[1])<<8 | int32(int8(p[2]))<<16
		return float32(v) / 8388608.0
	case 32:
		v := int32(binary.LittleEndian.Uint32(p))
		return float32(float64(v) / 2147483648.0)
	default:
		return 0
	}
}

func decodeMP3(data []byte) (*Clip, error) {
	dec, err := mp3.NewDecoder(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode MP3: %w", err)
	}
	srcRate := dec.SampleRate()
	if srcRate <= 0 {
		return nil, fmt.Errorf("invalid MP3 sample rate %d", srcRate)
	}

	maxFramesSrc := int(float64(maxDecodedFramesPerFile)*float64(srcRate)/float64(MixerSampleRate)) + srcRate
	maxRawBytes := int64(maxFramesSrc) * 4
	raw, err := io.ReadAll(io.LimitReader(dec, maxRawBytes+4))
	if err != nil {
		return nil, fmt.Errorf("read MP3 frames: %w", err)
	}
	if int64(len(raw)) > maxRawBytes {
		return nil, fmt.Errorf("MP3 duration exceeds %v limit", maxClipDuration)
	}

	sampleCount := (len(raw) / 4) * 2
	if sampleCount == 0 {
		return nil, errors.New("MP3 stream contains no audio samples")
	}
	samples := make([]float32, sampleCount)
	for i := range sampleCount {
		samples[i] = float32(int16(binary.LittleEndian.Uint16(raw[i*2:i*2+2]))) / 32768.0
	}
	return buildNormalizedClip(samples, 2, srcRate)
}

func decodeOGG(data []byte) (*Clip, error) {
	rdr, err := oggvorbis.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode OGG Vorbis: %w", err)
	}
	srcRate := rdr.SampleRate()
	channels := rdr.Channels()
	if srcRate <= 0 || channels <= 0 {
		return nil, fmt.Errorf("invalid OGG stream format (%d ch, %d Hz)", channels, srcRate)
	}

	maxSamplesSrc := int(float64(maxDecodedFramesPerFile)*float64(srcRate)/float64(MixerSampleRate))*channels + srcRate*channels
	buf := make([]float32, 4096)
	var samples []float32
	for {
		n, readErr := rdr.Read(buf)
		if n > 0 {
			samples = append(samples, buf[:n]...)
			if len(samples) > maxSamplesSrc {
				return nil, fmt.Errorf("OGG duration exceeds %v limit", maxClipDuration)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("read OGG Vorbis frames: %w", readErr)
		}
	}
	return buildNormalizedClip(samples, channels, srcRate)
}

func decodeFLAC(data []byte) (*Clip, error) {
	stream, err := flac.New(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode FLAC: %w", err)
	}
	defer func() {
		_ = stream.Close()
	}()

	var (
		srcRate  int
		channels int
		samples  []float32
	)
	if stream.Info != nil {
		srcRate = int(stream.Info.SampleRate)
		channels = int(stream.Info.NChannels)
	}

	maxSamplesSrc := 0
	for {
		fr, parseErr := stream.ParseNext()
		if errors.Is(parseErr, io.EOF) {
			break
		}
		if parseErr != nil {
			return nil, fmt.Errorf("parse FLAC frame: %w", parseErr)
		}

		frameRate := int(fr.SampleRate)
		if frameRate == 0 {
			frameRate = srcRate
		}
		if frameRate <= 0 {
			return nil, errors.New("FLAC stream has unknown sample rate")
		}
		if srcRate == 0 {
			srcRate = frameRate
		}

		frameCh := len(fr.Subframes)
		if frameCh == 0 {
			continue
		}
		if channels == 0 {
			channels = frameCh
		}
		if maxSamplesSrc == 0 {
			maxSamplesSrc = int(float64(maxDecodedFramesPerFile)*float64(srcRate)/float64(MixerSampleRate))*channels + srcRate*channels
		}

		bps := int(fr.BitsPerSample)
		if bps == 0 && stream.Info != nil {
			bps = int(stream.Info.BitsPerSample)
		}
		if bps <= 1 || bps > 32 {
			return nil, fmt.Errorf("unsupported FLAC bits-per-sample %d", bps)
		}
		scale := float32(math.Exp2(float64(bps - 1)))

		nSamples := len(fr.Subframes[0].Samples)
		for ch := 1; ch < frameCh; ch++ {
			if len(fr.Subframes[ch].Samples) < nSamples {
				nSamples = len(fr.Subframes[ch].Samples)
			}
		}
		if nSamples <= 0 {
			continue
		}

		needed := len(samples) + nSamples*frameCh
		if needed > maxSamplesSrc {
			return nil, fmt.Errorf("FLAC duration exceeds %v limit", maxClipDuration)
		}
		for sIdx := range nSamples {
			for ch := range frameCh {
				samples = append(samples, float32(fr.Subframes[ch].Samples[sIdx])/scale)
			}
		}
	}
	return buildNormalizedClip(samples, channels, srcRate)
}

type clipCacheKey struct {
	path    string
	modUnix int64
	size    int64
}

type clipCacheEntry struct {
	key  clipCacheKey
	clip *Clip
	size int
}

type clipCache struct {
	mu       sync.Mutex
	maxBytes int
	curBytes int
	items    map[clipCacheKey]*list.Element
	order    *list.List
	decode   func(path string) (*Clip, error)
}

func newClipCache(maxBytes int, decodeFn func(string) (*Clip, error)) *clipCache {
	if maxBytes <= 0 {
		maxBytes = defaultCacheMaxBytes
	}
	if decodeFn == nil {
		decodeFn = DecodeFile
	}
	return &clipCache{
		maxBytes: maxBytes,
		items:    make(map[clipCacheKey]*list.Element),
		order:    list.New(),
		decode:   decodeFn,
	}
}

func (c *clipCache) lookup(path string, info os.FileInfo) (*Clip, bool) {
	if info == nil {
		return nil, false
	}
	key := clipCacheKey{
		path:    filepath.Clean(path),
		modUnix: info.ModTime().UnixNano(),
		size:    info.Size(),
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.items[key]; ok {
		c.order.MoveToFront(elem)
		return elem.Value.(*clipCacheEntry).clip, true
	}
	return nil, false
}

func (c *clipCache) store(path string, info os.FileInfo, clip *Clip) {
	if clip == nil || info == nil {
		return
	}
	clipBytes := len(clip.Samples) * 4
	if clipBytes > maxCacheableClipBytes || clipBytes > c.maxBytes {
		return
	}
	key := clipCacheKey{
		path:    filepath.Clean(path),
		modUnix: info.ModTime().UnixNano(),
		size:    info.Size(),
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.items[key]; ok {
		old := elem.Value.(*clipCacheEntry)
		c.curBytes -= old.size
		old.clip = clip
		old.size = clipBytes
		c.curBytes += clipBytes
		c.order.MoveToFront(elem)
		c.evictLocked()
		return
	}

	entry := &clipCacheEntry{
		key:  key,
		clip: clip,
		size: clipBytes,
	}
	elem := c.order.PushFront(entry)
	c.items[key] = elem
	c.curBytes += clipBytes
	c.evictLocked()
}

func (c *clipCache) evictLocked() {
	for c.curBytes > c.maxBytes && c.order.Len() > 0 {
		back := c.order.Back()
		if back == nil {
			break
		}
		entry := back.Value.(*clipCacheEntry)
		c.order.Remove(back)
		delete(c.items, entry.key)
		c.curBytes -= entry.size
	}
}

func (c *clipCache) invalidatePath(path string) {
	clean := filepath.Clean(path)
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, elem := range c.items {
		if k.path == clean {
			entry := elem.Value.(*clipCacheEntry)
			c.order.Remove(elem)
			delete(c.items, k)
			c.curBytes -= entry.size
		}
	}
}
