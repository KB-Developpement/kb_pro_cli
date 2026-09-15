package cli

import (
	"io"
	"os"
	"strings"
	"testing"
)

// spinnerFrames are the braille glyphs huh's spinner animates through. When
// stdout is not a terminal each frame used to be flushed as its own fragment,
// flooding CI logs and `ffm shell --exec` output with thousands of copies of
// the title.
const spinnerFrames = "⣾⣽⣻⢿⡿⣟⣯⣷⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"

// captureStdout redirects os.Stdout to a pipe (never a TTY) for the duration of
// fn and returns everything written to it.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestRunWithSpinner_NonTTYPrintsTitleOnce(t *testing.T) {
	const title = "Installing kb_pro on acme.localhost…"

	ran := 0
	var runErr error
	out := captureStdout(t, func() {
		runErr = runWithSpinner(title, func() { ran++ })
	})

	if runErr != nil {
		t.Fatalf("runWithSpinner returned %v, want nil", runErr)
	}
	if ran != 1 {
		t.Fatalf("action ran %d times, want 1", ran)
	}
	if got := strings.Count(out, title); got != 1 {
		t.Fatalf("title printed %d times, want exactly 1 (output %q)", got, out)
	}
	if got := strings.Count(out, "\n"); got != 1 {
		t.Fatalf("output has %d lines, want exactly 1 (output %q)", got, out)
	}
	if strings.ContainsAny(out, spinnerFrames) {
		t.Fatalf("animation frames leaked into non-TTY output: %q", out)
	}
}

func TestRunWithSpinner_NonTTYPropagatesNothingAndStillRunsOnQuiet(t *testing.T) {
	defer func(no, q bool) { globalFlags.NoInput, globalFlags.Quiet = no, q }(globalFlags.NoInput, globalFlags.Quiet)
	globalFlags.NoInput, globalFlags.Quiet = true, true

	ran := false
	out := captureStdout(t, func() {
		if err := runWithSpinner("Building assets…", func() { ran = true }); err != nil {
			t.Errorf("runWithSpinner returned %v, want nil", err)
		}
	})
	if !ran {
		t.Fatal("action did not run under --no-input --quiet")
	}
	if got := strings.Count(out, "Building assets…"); got != 1 {
		t.Fatalf("title printed %d times, want exactly 1 (output %q)", got, out)
	}
}
