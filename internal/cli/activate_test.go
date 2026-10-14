package cli

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KB-Developpement/kb_pro_cli/internal/license"
)

func executeActivation(t *testing.T, input io.Reader, args ...string) (string, error) {
	t.Helper()
	var err error
	out := capture(t, func() {
		cmd := newRootCmd()
		cmd.SetArgs(append(args, "--no-input"))
		cmd.SetIn(input)
		err = cmd.Execute()
	})
	return out, err
}

func TestActivateRejectsPositionalSecretsBeforeSideEffects(t *testing.T) {
	h := newBenchH(t)
	const secret = "TEST-ARGV-SECRET"
	for _, args := range [][]string{
		{"activate", secret}, {"a", secret}, {"activate", secret, "another-key"},
	} {
		out, err := executeActivation(t, strings.NewReader(""), args...)
		if err == nil || !strings.Contains(err.Error(), "not accepted as arguments") {
			t.Fatalf("expected safe positional refusal, got %v", err)
		}
		if strings.Contains(out+err.Error(), secret) {
			t.Fatal("positional key leaked in output or returned error")
		}
	}
	if h.srv.requestCount() != 0 {
		t.Fatal("refused positional input contacted the server")
	}
	if _, err := os.Stat(filepath.Join(h.home, ".config", "kb", "machine-id")); !os.IsNotExist(err) {
		t.Fatal("refused positional input created an installation identity")
	}
}

func TestActivateInputFailuresNeverUseSavedKey(t *testing.T) {
	h := newBenchH(t)
	must(t, license.SaveLicenseKey("TEST-SAVED-KEY"))
	before, err := os.ReadFile(filepath.Join(h.home, ".config", "kb", "license_key"))
	must(t, err)
	missing := filepath.Join(t.TempDir(), "missing")
	for _, args := range [][]string{
		{"activate", "--key-file", missing},
		{"activate", "--key-file="},
		{"activate", "--key-file", "-"},
		{"activate", "--prompt"},
		{"activate", "--prompt", "--key-file", "-"},
	} {
		if _, err := executeActivation(t, strings.NewReader(""), args...); err == nil {
			t.Fatalf("input failure unexpectedly succeeded: %v", args)
		}
	}
	if h.srv.requestCount() != 0 {
		t.Fatal("input failure fell back to saved key or contacted server")
	}
	after, err := os.ReadFile(filepath.Join(h.home, ".config", "kb", "license_key"))
	must(t, err)
	if string(before) != string(after) {
		t.Fatal("input failure changed the saved key")
	}
}

func TestActivateFileStdinAndSavedKey(t *testing.T) {
	for _, source := range []string{"file", "stdin", "saved"} {
		t.Run(source, func(t *testing.T) {
			h := newBenchH(t)
			const key = "TEST-ACTIVATION-KEY"
			sent := make(chan string, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/activate" || r.Method != http.MethodPost {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				sent <- string(body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"token":"test-token"}`)
			}))
			defer srv.Close()
			t.Setenv("KB_LICENSE_SERVER", srv.URL)
			args := []string{"activate"}
			input := strings.NewReader("")
			must(t, license.SaveLicenseKey("TEST-OLD-KEY"))
			storedPath := filepath.Join(h.home, ".config", "kb", "license_key")
			must(t, os.Chmod(storedPath, 0o644))
			switch source {
			case "file":
				path := filepath.Join(t.TempDir(), "key")
				must(t, os.WriteFile(path, []byte(key+"\n"), 0o600))
				args = append(args, "--key-file", path)
			case "stdin":
				args = []string{"a", "--key-file", "-"}
				input = strings.NewReader(key + "\r\n")
			case "saved":
				must(t, license.SaveLicenseKey(key))
			}
			out, err := executeActivation(t, input, args...)
			must(t, err)
			body := <-sent
			if !strings.Contains(body, `"license_key":"`+key+`"`) || !strings.Contains(body, `"fingerprint":`) {
				t.Fatal("activation did not send the selected key and fingerprint")
			}
			if strings.Contains(out, key) || strings.Contains(out, "TEST-OLD-KEY") {
				t.Fatal("activation printed key material")
			}
			if license.LoadLicenseKey() != key {
				t.Fatal("successful activation did not save selected key")
			}
			info, err := os.Stat(storedPath)
			must(t, err)
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("stored key has mode %04o, want 0600", info.Mode().Perm())
			}
			token, err := license.GetCachedToken()
			must(t, err)
			if token != "test-token" {
				t.Fatal("activation did not persist the returned token")
			}
		})
	}
}

func TestActivateLimitErrorNeverIncludesKey(t *testing.T) {
	h := newBenchH(t)
	const key = "TEST-LIMIT-SECRET"
	must(t, license.SaveLicenseKey(key))
	write(t, filepath.Join(h.home, ".config", "kb", "previous-fingerprint"), "old-fingerprint\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()
	t.Setenv("KB_LICENSE_SERVER", srv.URL)
	out, err := executeActivation(t, strings.NewReader(""), "activate")
	if !errors.Is(err, license.ErrActivationLimitReached) {
		t.Fatalf("expected activation limit, got %v", err)
	}
	if strings.Contains(out+err.Error(), key) {
		t.Fatal("key leaked in output or error destined for the error log")
	}
	if !strings.Contains(err.Error(), "--key <license-key> --fingerprint old-fingerprint") {
		t.Fatal("limit error lost its operator remedy")
	}
	if _, err := license.GetCachedToken(); err == nil {
		t.Fatal("refused activation saved a token")
	}
}

func TestActivationKeyInputValidation(t *testing.T) {
	for _, input := range []string{"", " \n", "TEST-KEY\nOTHER-KEY", "TEST KEY", "TEST\x00KEY", strings.Repeat("x", license.MaxLicenseKeyBytes+1)} {
		if _, err := readActivationKeyInput(strings.NewReader(input)); err == nil {
			t.Fatal("invalid input accepted")
		} else if strings.Contains(err.Error(), "TEST") {
			t.Fatal("validation echoed secret input")
		}
	}
	got, err := readActivationKeyInput(strings.NewReader("  TEST-KEY\r\n"))
	must(t, err)
	if got != "TEST-KEY" {
		t.Fatal("single key was not trimmed")
	}
	if _, err := readActivationKeyInput(failingKeyReader{}); err == nil || strings.Contains(err.Error(), "TEST-SECRET") {
		t.Fatal("reader error was missing or exposed input")
	}
}

type failingKeyReader struct{}

func (failingKeyReader) Read([]byte) (int, error) { return 0, errors.New("TEST-SECRET") }

func TestActivationKeyFileRefusesUnsafeSources(t *testing.T) {
	newBenchH(t)
	private := filepath.Join(t.TempDir(), "key")
	must(t, os.WriteFile(private, []byte("TEST-PRIVATE-KEY\n"), 0o600))
	link := filepath.Join(t.TempDir(), "key-link")
	must(t, os.Symlink(private, link))
	broad := filepath.Join(t.TempDir(), "broad-key")
	must(t, os.WriteFile(broad, []byte("TEST-PRIVATE-KEY\n"), 0o644))
	must(t, os.Chmod(broad, 0o644))
	for _, path := range []string{link, broad, t.TempDir()} {
		if _, err := readActivationKey(path, false, strings.NewReader("")); err == nil {
			t.Fatal("unsafe key source accepted")
		}
	}
	got, err := readActivationKey(private, false, strings.NewReader(""))
	must(t, err)
	if got != "TEST-PRIVATE-KEY" {
		t.Fatal("private key source not read")
	}
}

func TestActivateDoesNotLogReflectedServerError(t *testing.T) {
	newBenchH(t)
	const key = "TEST-REFLECTED-SECRET"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":"`+key+`"}`)
	}))
	defer srv.Close()
	t.Setenv("KB_LICENSE_SERVER", srv.URL)
	out, err := executeActivation(t, strings.NewReader(key), "activate", "--key-file", "-")
	if err == nil || strings.Contains(out+err.Error(), key) {
		t.Fatal("server error was missing or reflected the key into the logged error")
	}
}

func TestMaximumActivationKeyAcceptsLineEndings(t *testing.T) {
	key := strings.Repeat("x", license.MaxLicenseKeyBytes)
	for _, ending := range []string{"", "\n", "\r\n"} {
		got, err := readActivationKeyInput(strings.NewReader(key + ending))
		if err != nil || got != key {
			t.Fatal("maximum-size key with line ending was rejected")
		}
	}
}
