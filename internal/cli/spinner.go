package cli

import (
	"fmt"
	"os"

	"github.com/charmbracelet/huh/spinner"
	"github.com/mattn/go-isatty"

	"github.com/KB-Developpement/kb_pro_cli/internal/ui"
)

// runWithSpinner runs fn while showing an animated spinner titled title, and is
// the only place in the CLI that may start one.
//
// The animation repaints by writing a new frame to stdout. On a terminal those
// frames overwrite each other; on a pipe (CI logs, `ffm shell --exec`, a file)
// every frame survives as its own fragment, so a single long bench call used to
// emit thousands of copies of the title. When stdout is not a terminal — or the
// run is explicitly non-interactive (--no-input) or quiet (--quiet) — the title
// is printed once as a dim line and fn runs synchronously instead.
//
// The returned error is the spinner's own failure (it can fail to start on a
// degraded terminal); fn reports its own result through the variables it closes
// over, exactly as with spinner.Action.
func runWithSpinner(title string, fn func()) error {
	if plainSpinnerOutput() {
		fmt.Fprintln(os.Stdout, ui.Dim.Render(title))
		fn()
		return nil
	}
	return spinner.New().Title(title).Action(fn).Run()
}

// plainSpinnerOutput reports whether spinner animation must be suppressed.
func plainSpinnerOutput() bool {
	return globalFlags.NoInput || globalFlags.Quiet || !isatty.IsTerminal(os.Stdout.Fd())
}
