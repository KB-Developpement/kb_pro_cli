package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/KB-Developpement/kb_pro_cli/internal/testutil"
)

// binaryArchive is a release asset holding one file named kb.
func binaryArchive(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	must(t, tw.WriteHeader(&tar.Header{Name: "kb", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}))
	_, err := tw.Write([]byte(body))
	must(t, err)
	must(t, tw.Close())
	must(t, gw.Close())
	return buf.Bytes()
}

// A host on an older kb reaches the new binary with `kb update` alone: the
// release API base is overridable for tests, checksums.txt is verified, the
// in-place swap works, and nothing about identity or licensing is touched (no
// activation request, license cache and machine-id byte-identical).
func TestSelfUpdatePathStaysStable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := filepath.Join(home, ".config", "kb")
	must(t, os.MkdirAll(cfg, 0o700))
	files := map[string]string{"license.json": `{"token":"t"}`, "license.jwt": "t\n", "license_key": "KEY\n", "machine-id": "0123456789abcdef0123456789abcdef\n"}
	hashes := func() map[string]string {
		out := map[string]string{}
		for name := range files {
			sum := sha256.Sum256(testutil.MustRead(t, filepath.Join(cfg, name)))
			out[name] = hex.EncodeToString(sum[:])
		}
		return out
	}
	for name, body := range files {
		must(t, os.WriteFile(filepath.Join(cfg, name), []byte(body), 0o600))
	}
	before := hashes()

	asset := releaseAssetName("v9.9.9")
	archive := binaryArchive(t, "NEW-BINARY")
	sum := sha256.Sum256(archive)
	var activations atomic.Int32
	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v9.9.9", "assets": []map[string]string{
				{"name": asset, "browser_download_url": base + "/asset"},
				{"name": checksumsAssetName, "browser_download_url": base + "/checksums"},
			}})
		case "/asset":
			_, _ = w.Write(archive)
		case "/checksums":
			_, _ = fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
		case "/activate", "/heartbeat":
			activations.Add(1)
		}
	}))
	defer srv.Close()
	base = srv.URL
	old := githubReleasesAPI
	githubReleasesAPI = srv.URL + "/releases/latest"
	defer func() { githubReleasesAPI = old }()

	// kb update --check: no license needed, nothing installed.
	var checkErr error
	out := capture(t, func() { checkErr = runUpdate(context.Background(), true, true) })
	if checkErr != nil || !strings.Contains(out, "v9.9.9") {
		t.Fatalf("kb update --check: %v\n%s", checkErr, out)
	}

	// The in-place swap with the checksum verified.
	exe := filepath.Join(t.TempDir(), "kb")
	must(t, os.WriteFile(exe, []byte("OLD-BINARY"), 0o755))
	oldExe := currentExecutable
	currentExecutable = func() (string, error) { return exe, nil }
	defer func() { currentExecutable = oldExe }()
	if err := downloadAndInstall(srv.URL+"/asset", srv.URL+"/checksums", asset); err != nil {
		t.Fatalf("downloadAndInstall: %v", err)
	}
	if got := string(testutil.MustRead(t, exe)); got != "NEW-BINARY" {
		t.Fatalf("binary = %q", got)
	}
	// A checksum that does not match refuses and leaves the binary alone.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/checksums" {
			_, _ = fmt.Fprintf(w, "%s  %s\n", strings.Repeat("0", 64), asset)
			return
		}
		_, _ = w.Write(archive)
	}))
	defer bad.Close()
	must(t, os.WriteFile(exe, []byte("OLD-BINARY"), 0o755))
	if err := downloadAndInstall(bad.URL+"/asset", bad.URL+"/checksums", asset); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("a checksum mismatch was accepted: %v", err)
	}
	if got := string(testutil.MustRead(t, exe)); got != "OLD-BINARY" {
		t.Errorf("binary replaced despite the mismatch: %q", got)
	}
	if err := downloadAndInstall(srv.URL+"/asset", "", asset); err == nil {
		t.Error("an update without checksums.txt was accepted")
	}

	after := hashes()
	for name := range files {
		if after[name] != before[name] {
			t.Errorf("%s changed during the update", name)
		}
	}
	if n := activations.Load(); n != 0 {
		t.Errorf("the update triggered %d activation/heartbeat requests", n)
	}
}
