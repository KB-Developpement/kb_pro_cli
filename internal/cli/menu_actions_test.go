package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
	"github.com/KB-Developpement/kb_pro_cli/internal/testutil"
	"github.com/KB-Developpement/kb_pro_cli/internal/version"
)

// stubRelease serves a GitHub "latest release" answer for tag with a
// platform archive holding body and its checksum manifest.
func stubRelease(t *testing.T, tag, body string) {
	t.Helper()
	asset := releaseAssetName(tag)
	archive := binaryArchive(t, body)
	sum := sha256.Sum256(archive)
	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": tag, "assets": []map[string]string{
				{"name": asset, "browser_download_url": base + "/asset"},
				{"name": checksumsAssetName, "browser_download_url": base + "/checksums"},
			}})
		case "/asset":
			_, _ = w.Write(archive)
		case "/checksums":
			_, _ = fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	base = srv.URL
	old := githubReleasesAPI
	githubReleasesAPI = srv.URL + "/releases/latest"
	t.Cleanup(func() { githubReleasesAPI = old })
}

func setVersion(t *testing.T, v string) {
	t.Helper()
	old := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = old })
}

// useNoInput runs a test non-interactively so no spinner animates.
func useNoInput(t *testing.T) {
	t.Helper()
	orig := globalFlags
	globalFlags.NoInput = true
	t.Cleanup(func() { globalFlags = orig })
}

func TestCheckLatestRelease(t *testing.T) {
	stubRelease(t, "v9.9.9", "NEW")
	useNoInput(t)
	tests := []struct {
		current string
		newer   bool
		dev     bool
	}{
		{"v1.0.0", true, false},
		{"v9.9.9", false, false},
		{"v10.0.0", false, false},
		{"dev", true, true},
		{"", true, true},
	}
	for _, tc := range tests {
		t.Run("current "+tc.current, func(t *testing.T) {
			setVersion(t, tc.current)
			var info updateInfo
			var err error
			capture(t, func() { info, err = checkLatestRelease() })
			if err != nil {
				t.Fatal(err)
			}
			if info.Current != tc.current || info.Latest != "v9.9.9" || info.Newer != tc.newer || info.Dev != tc.dev {
				t.Errorf("info = %+v", info)
			}
		})
	}
}

func TestCheckLatestReleaseErrors(t *testing.T) {
	useNoInput(t)
	for name, handler := range map[string]http.HandlerFunc{
		"http error": func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", http.StatusBadGateway) },
		"no tag":     func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"tag_name":""}`)) },
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()
			old := githubReleasesAPI
			githubReleasesAPI = srv.URL
			defer func() { githubReleasesAPI = old }()
			var err error
			capture(t, func() { _, err = checkLatestRelease() })
			if err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// kb update --check keeps its exact messages.
func TestRunUpdateCheckOutputUnchanged(t *testing.T) {
	stubRelease(t, "v9.9.9", "NEW")
	useNoInput(t)
	tests := []struct{ current, want string }{
		{"v1.0.0", "Update available: v1.0.0 → v9.9.9\n"},
		{"v9.9.9", "Already up to date (v9.9.9)\n"},
		{"dev", "Running a dev build. Latest release: v9.9.9\n"},
	}
	for _, tc := range tests {
		t.Run(tc.current, func(t *testing.T) {
			setVersion(t, tc.current)
			var err error
			out := capture(t, func() { err = runUpdate(context.Background(), true, true) })
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(out, tc.want) {
				t.Errorf("output = %q, want it to end with %q", out, tc.want)
			}
		})
	}
}

func scriptConfirm(t *testing.T, answer bool, err error) *int {
	t.Helper()
	asked := 0
	old := menuConfirm
	menuConfirm = func(string, string) (bool, error) { asked++; return answer, err }
	t.Cleanup(func() { menuConfirm = old })
	return &asked
}

func allowUpdateLicense(t *testing.T, err error) {
	t.Helper()
	old := updateLicenseGate
	updateLicenseGate = func(context.Context) error { return err }
	t.Cleanup(func() { updateLicenseGate = old })
}

func pointExecutableAt(t *testing.T, content string) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "kb")
	must(t, os.WriteFile(exe, []byte(content), 0o755))
	old := currentExecutable
	currentExecutable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { currentExecutable = old })
	return exe
}

func TestMenuUpdate(t *testing.T) {
	useNoInput(t)
	tests := []struct {
		name        string
		current     string
		answer      bool
		confirmErr  error
		gateErr     error
		wantLeave   bool
		wantAsked   int
		wantBinary  string
		wantContain string
	}{
		{"confirmed", "v1.0.0", true, nil, nil, true, 1, "NEW-BINARY", "Restart kb"},
		{"declined", "v1.0.0", false, nil, nil, false, 1, "OLD-BINARY", "Update cancelled."},
		{"form error", "v1.0.0", false, errors.New("could not open a new TTY"), nil, false, 1, "OLD-BINARY", "could not open a new TTY"},
		{"up to date", "v9.9.9", true, nil, nil, false, 0, "OLD-BINARY", "Already up to date"},
		{"no license", "v1.0.0", true, nil, errors.New("active license required to update"), false, 0, "OLD-BINARY", "active license required"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stubRelease(t, "v9.9.9", "NEW-BINARY")
			setVersion(t, tc.current)
			allowUpdateLicense(t, tc.gateErr)
			asked := scriptConfirm(t, tc.answer, tc.confirmErr)
			exe := pointExecutableAt(t, "OLD-BINARY")

			var leave bool
			out := capture(t, func() { leave = runMenuUpdate(context.Background()) })
			if leave != tc.wantLeave {
				t.Errorf("leave = %v, want %v\n%s", leave, tc.wantLeave, out)
			}
			if *asked != tc.wantAsked {
				t.Errorf("confirm asked %d times, want %d", *asked, tc.wantAsked)
			}
			if got := string(testutil.MustRead(t, exe)); got != tc.wantBinary {
				t.Errorf("binary = %q, want %q", got, tc.wantBinary)
			}
			if !strings.Contains(out, tc.wantContain) {
				t.Errorf("output lacks %q:\n%s", tc.wantContain, out)
			}
			// The two spinner titles (existing runWithSpinner) are outside this
			// item's styled output; everything the menu item prints itself is
			// plain, so any other line with an escape code is a leak.
			for _, line := range strings.Split(out, "\n") {
				spinnerTitle := strings.Contains(line, "Checking for updates…") || strings.Contains(line, "Downloading kb v")
				if strings.Contains(line, "\x1b") && !spinnerTitle {
					t.Errorf("line leaks ANSI into a pipe: %q", line)
				}
			}
		})
	}
}

func TestMenuStatusPrintsReport(t *testing.T) {
	h := newBenchH(t)
	h.seed("kb_pro", "v1.2.0", true)
	out := capture(t, runMenuStatus)
	for _, want := range []string{"Bench: " + h.root, "kb_pro", "Journal: none"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output lacks %q:\n%s", want, out)
		}
	}
}

func TestMenuAdopt(t *testing.T) {
	t.Run("confirmed applies through the locked run", func(t *testing.T) {
		h := legacyBench(t)
		asked := scriptConfirm(t, true, nil)
		out := capture(t, func() { runMenuAdopt(context.Background()) })
		if *asked != 1 {
			t.Fatalf("confirm asked %d times\n%s", *asked, out)
		}
		if !strings.Contains(out, "would adopt v1.2.0") || !strings.Contains(out, "Adopt finished.") {
			t.Errorf("output:\n%s", out)
		}
		if !h.receipt("kb_pro").Valid() || h.receipt("kb_pro").Receipt.Provenance != kbstate.ProvAdopted {
			t.Error("no adopted receipt after Confirm")
		}
		if _, err := os.Stat(kbstate.LockPath(h.root)); err != nil {
			t.Error("the apply did not take the bench lock (no .kb/lock)")
		}
	})
	t.Run("declined writes nothing", func(t *testing.T) {
		h := legacyBench(t)
		before := h.benchDigest()
		asked := scriptConfirm(t, false, nil)
		out := capture(t, func() { runMenuAdopt(context.Background()) })
		if *asked != 1 || !strings.Contains(out, "Cancelled.") {
			t.Errorf("asked=%d\n%s", *asked, out)
		}
		if h.receipt("kb_pro").Valid() || hasKB(h) || h.benchDigest() != before {
			t.Error("a declined adopt changed the bench")
		}
	})
	t.Run("form error is shown and nothing is written", func(t *testing.T) {
		h := legacyBench(t)
		scriptConfirm(t, false, errors.New("could not open a new TTY"))
		out := capture(t, func() { runMenuAdopt(context.Background()) })
		if !strings.Contains(out, "could not open a new TTY") || h.receipt("kb_pro").Valid() {
			t.Errorf("form error not shown or receipt written:\n%s", out)
		}
	})
	t.Run("nothing to adopt skips the confirm", func(t *testing.T) {
		h := newBenchH(t)
		h.seed("kb_pro", "v1.2.0", true)
		asked := scriptConfirm(t, true, nil)
		out := capture(t, func() { runMenuAdopt(context.Background()) })
		if *asked != 0 || !strings.Contains(out, "Nothing to adopt.") {
			t.Errorf("asked=%d\n%s", *asked, out)
		}
	})
	t.Run("partial mismatch adopts the rest and warns instead of failing", func(t *testing.T) {
		h := legacyBench(t)
		write(t, filepath.Join(h.appDir("kb_pro"), "extra_file.py"), "x\n")
		asked := scriptConfirm(t, true, nil)
		out := capture(t, func() { runMenuAdopt(context.Background()) })
		if *asked != 1 {
			t.Fatalf("confirm asked %d times\n%s", *asked, out)
		}
		if !strings.Contains(out, "Adopted the matching apps;") || strings.Contains(out, "Error:") || strings.Contains(out, "Adopt finished.") {
			t.Errorf("output:\n%s", out)
		}
		if !h.receipt("kb_compta").Valid() || h.receipt("kb_pro").Valid() {
			t.Error("expected kb_compta adopted and kb_pro not")
		}
	})
	t.Run("mismatch is not offered", func(t *testing.T) {
		h := legacyBench(t)
		write(t, filepath.Join(h.appDir("kb_pro"), "extra_file.py"), "x\n")
		write(t, filepath.Join(h.appDir("kb_compta"), "extra_file.py"), "x\n")
		write(t, filepath.Join(h.appDir("frappe"), "extra_file.py"), "x\n")
		asked := scriptConfirm(t, true, nil)
		out := capture(t, func() { runMenuAdopt(context.Background()) })
		if *asked != 0 || !strings.Contains(out, "MISMATCH") {
			t.Errorf("asked=%d\n%s", *asked, out)
		}
	})
}

func TestActivateAndRefreshReloadsStateOnlyOnSuccess(t *testing.T) {
	calls := 0
	prev := runCheckFn
	runCheckFn = func() { calls++ }
	t.Cleanup(func() { runCheckFn = prev })

	if err := activateAndRefresh(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("license state reloaded %d times after success, want 1", calls)
	}

	want := errors.New("activation failed")
	if err := activateAndRefresh(func() error { return want }); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if calls != 1 {
		t.Fatalf("license state reloaded after a failed activation (calls=%d)", calls)
	}
}
