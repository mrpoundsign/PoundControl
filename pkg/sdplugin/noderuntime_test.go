package sdplugin

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestNodeManagerResolvePriorityAndVersionCheck(t *testing.T) {
	tmp := t.TempDir()
	customBin := filepath.Join(tmp, "custom-node")
	managedDir := filepath.Join(tmp, "managed")
	sysBin := filepath.Join(tmp, "sys-node")

	var managedBin string
	if runtime.GOOS == "windows" {
		managedBin = filepath.Join(managedDir, "node.exe")
	} else {
		managedBin = filepath.Join(managedDir, "bin", "node")
	}
	for _, f := range []string{customBin, managedBin, sysBin} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		if err := os.WriteFile(f, []byte("stub"), 0o755); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", f, err)
		}
	}

	mgr := NewNodeManager(managedDir)
	mgr.elgatoPaths = nil
	mgr.lookPath = func(string) (string, error) { return sysBin, nil }
	versions := map[string]string{
		customBin:  "v18.0.0", // too old for minMajor=20
		managedBin: "v22.14.0",
		sysBin:     "v20.11.0",
	}
	mgr.probeVersion = func(p string) (string, error) {
		return versions[p], nil
	}

	// Skips custom v18 (too old), picks managed v22.14.0.
	info := mgr.Resolve(customBin, 20)
	if !info.Found || !info.Usable || info.Source != "managed" || info.Major != 22 {
		t.Fatalf("unexpected Resolve() with old custom path: %+v", info)
	}

	// Custom >= 20 takes highest priority.
	versions[customBin] = "v24.1.0"
	info = mgr.Resolve(customBin, 20)
	if !info.Usable || info.Source != "custom" || info.Major != 24 {
		t.Fatalf("expected custom source priority, got %+v", info)
	}

	// When nothing meets minMajor, returns Found=true, Usable=false with descriptive error.
	versions[customBin] = "v18.0.0"
	versions[managedBin] = "v18.1.0"
	versions[sysBin] = "v16.0.0"
	info = mgr.Resolve("", 20)
	if !info.Found || info.Usable || info.Error == "" {
		t.Fatalf("expected Found=true Usable=false error when only older Node exists, got %+v", info)
	}
}

func TestNodeManagerDownloadSHA256AndCorruptionRejection(t *testing.T) {
	managedDir := t.TempDir()
	mgr := NewNodeManager(managedDir)
	mgr.elgatoPaths = nil
	mgr.lookPath = func(string) (string, error) { return "", os.ErrNotExist }

	archiveName, innerPath, destRel, isZip, err := nodeArchiveSpec(mgr.nodeVersion, mgr.goos, mgr.goarch)
	if err != nil {
		t.Skipf("host %s/%s unsupported for Node spec: %v", mgr.goos, mgr.goarch, err)
	}

	fakeNodePayload := []byte("#!/bin/sh\necho v22.14.0\n")
	var archiveBuf bytes.Buffer
	if isZip {
		zw := zip.NewWriter(&archiveBuf)
		w, createErr := zw.Create(innerPath)
		if createErr != nil {
			t.Fatalf("zip Create error = %v", createErr)
		}
		_, _ = w.Write(fakeNodePayload)
		_ = zw.Close()
	} else {
		gzw := gzip.NewWriter(&archiveBuf)
		tw := tar.NewWriter(gzw)
		if hdrErr := tw.WriteHeader(&tar.Header{
			Name:     innerPath,
			Mode:     0o755,
			Size:     int64(len(fakeNodePayload)),
			Typeflag: tar.TypeReg,
		}); hdrErr != nil {
			t.Fatalf("tar WriteHeader error = %v", hdrErr)
		}
		_, _ = tw.Write(fakeNodePayload)
		_ = tw.Close()
		_ = gzw.Close()
	}

	archiveBytes := archiveBuf.Bytes()
	sum := sha256.Sum256(archiveBytes)
	goodHex := hex.EncodeToString(sum[:])

	serveCorrupt := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/SHASUMS256.txt"):
			_, _ = fmt.Fprintf(w, "%s  %s\n", goodHex, archiveName)
		case strings.HasSuffix(r.URL.Path, "/"+archiveName):
			data := archiveBytes
			if serveCorrupt {
				data = []byte("corrupted-payload")
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			_, _ = w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	mgr.baseURL = srv.URL
	mgr.probeVersion = func(string) (string, error) { return "v22.14.0\n", nil }

	// Corrupted archive must fail SHA-256 check before extracting anything.
	serveCorrupt = true
	if _, dlErr := mgr.DownloadManaged(context.Background(), nil); dlErr == nil || !strings.Contains(dlErr.Error(), "SHA-256") {
		t.Fatalf("expected SHA-256 failure on corrupted archive, got %v", dlErr)
	}
	installedPath := filepath.Join(managedDir, filepath.FromSlash(destRel))
	if _, statErr := os.Stat(installedPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected no binary installed on checksum failure, statErr=%v", statErr)
	}

	// Valid archive extracts only the target binary and resolves cleanly.
	serveCorrupt = false
	var sawStage bool
	info, err := mgr.DownloadManaged(context.Background(), func(_ int64, _ int64, stage string) {
		if stage != "" {
			sawStage = true
		}
	})
	if err != nil {
		t.Fatalf("DownloadManaged() error = %v", err)
	}
	if !sawStage || !info.Usable || info.Source != "managed" || info.Path != installedPath {
		t.Fatalf("unexpected DownloadManaged result: %+v (sawStage=%v)", info, sawStage)
	}
	gotBytes, err := os.ReadFile(installedPath)
	if err != nil || !bytes.Equal(gotBytes, fakeNodePayload) {
		t.Fatalf("extracted binary mismatch: got %q, want %q, err=%v", gotBytes, fakeNodePayload, err)
	}
}
