package tile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const maxImportedImageBytes = 10 * 1024 * 1024

// ImportImageAsset validates a user-selected image (PNG, JPEG, or SVG) and copies it atomically
// by SHA-256 content hash into assetsDir. When assetsDir is empty, it validates and returns the cleaned source path.
func ImportImageAsset(assetsDir, srcPath string) (string, error) {
	rawPath := strings.TrimSpace(srcPath)
	if rawPath == "" {
		return "", errors.New("image file path is required")
	}
	if strings.HasPrefix(strings.ToLower(rawPath), "file://") {
		if parsed, err := url.Parse(rawPath); err == nil {
			p := parsed.Path
			if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
				p = p[1:]
			}
			rawPath = filepath.FromSlash(p)
		}
	}
	cleaned := filepath.Clean(rawPath)
	ext := strings.ToLower(filepath.Ext(cleaned))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".svg":
	default:
		return "", fmt.Errorf("unsupported image format %q (expected .png, .jpg, .jpeg, or .svg)", ext)
	}

	info, err := os.Stat(cleaned)
	if err != nil {
		return "", fmt.Errorf("stat image %q: %w", cleaned, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("path %q is a directory", cleaned)
	}
	if info.Size() > maxImportedImageBytes {
		return "", fmt.Errorf("image %q exceeds 10 MiB limit (%d bytes)", cleaned, info.Size())
	}

	raw, err := os.ReadFile(cleaned)
	if err != nil {
		return "", fmt.Errorf("read image %q: %w", cleaned, err)
	}
	if _, decErr := DecodeImage(cleaned, defaultTileSize); decErr != nil {
		return "", fmt.Errorf("decode image %q: %w", cleaned, decErr)
	}

	targetDir := strings.TrimSpace(assetsDir)
	if targetDir == "" {
		return cleaned, nil
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", fmt.Errorf("create assets directory %q: %w", targetDir, err)
	}

	sum := sha256.Sum256(raw)
	dstName := hex.EncodeToString(sum[:16]) + ext
	dstPath := filepath.Join(targetDir, dstName)
	if _, statErr := os.Stat(dstPath); statErr == nil {
		return dstPath, nil
	}

	tmp, err := os.CreateTemp(targetDir, ".asset-*")
	if err != nil {
		return "", fmt.Errorf("create temp asset in %q: %w", targetDir, err)
	}
	tmpName := tmp.Name()
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write temp asset: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close temp asset: %w", err)
	}
	if err := os.Rename(tmpName, dstPath); err != nil {
		// Race with another writer for identical content hash is benign.
		if _, statErr := os.Stat(dstPath); statErr == nil {
			return dstPath, nil
		}
		return "", fmt.Errorf("install asset %q: %w", dstPath, err)
	}
	cleanupTmp = false
	return dstPath, nil
}

// CropSquare center-crops src into a 1:1 square image so tile background fills mimic CSS object-fit: cover.
func CropSquare(src image.Image) image.Image {
	if src == nil {
		return nil
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 || w == h {
		return src
	}
	side := min(w, h)
	x0 := b.Min.X + (w-side)/2
	y0 := b.Min.Y + (h-side)/2
	rect := image.Rect(x0, y0, x0+side, y0+side)
	if sub, ok := src.(interface {
		SubImage(image.Rectangle) image.Image
	}); ok {
		return sub.SubImage(rect)
	}
	dst := image.NewNRGBA(image.Rect(0, 0, side, side))
	draw.Draw(dst, dst.Bounds(), src, rect.Min, draw.Src)
	return dst
}
