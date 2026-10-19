package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/huh"

	"github.com/KB-Developpement/kb_pro_cli/internal/bench"
	"github.com/KB-Developpement/kb_pro_cli/internal/errlog"
	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
	"github.com/KB-Developpement/kb_pro_cli/internal/ui"
	"github.com/KB-Developpement/kb_pro_cli/internal/version"
)

// collectMenuHeader gathers the header state from local files and the license
// cache already loaded at startup. It makes no network request.
func collectMenuHeader(site string) menuHeaderState {
	root := bench.Root()
	s := menuHeaderState{
		Version: version.Version,
		License: license.CurrentState(),
		Root:    root,
		Site:    site,
		Journal: journalWarning(kbstate.ReadJournal(root)),
	}
	pid, ok := kbstate.HolderPID(root)
	s.LockPID = lockHolder(pid, ok, os.Getpid(), kbstate.ProcessAlive)
	if cache, err := readUpdateCheck(updateCheckPath()); err == nil {
		s.NewerKB = newerKnown(version.Version, cache.Latest)
	}
	return s
}

// printMenuHeader draws the header above the main menu on stderr.
func printMenuHeader(site string) {
	if globalFlags.Quiet {
		return
	}
	for _, l := range menuHeader(collectMenuHeader(site)) {
		style := ui.Dim
		if l.Warn {
			style = ui.Warn
		}
		_, _ = lipgloss.Fprintln(os.Stderr, style.Render(l.Text))
	}
}

// menuError shows the error of a menu action. Styled output goes through
// lipgloss so a pipe gets no ANSI.
func menuError(err error) {
	errlog.Log(err)
	_, _ = lipgloss.Fprintln(os.Stderr, "\n"+ui.Failure.Render("Error:")+" "+err.Error())
}

// menuConfirm is a variable only so tests can script the answer.
var menuConfirm = askConfirm

// askConfirm asks a yes/no question. Esc / Ctrl+C count as "no"; any other
// form error is returned. The answer is read only after Run succeeded, since
// the bound value moves while the user toggles.
func askConfirm(title, description string) (bool, error) {
	var confirmed bool
	err := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title(title).
				Description(description).
				Value(&confirmed),
		),
	).WithKeyMap(formKeyMap()).Run()
	if errors.Is(err, huh.ErrUserAborted) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return confirmed, nil
}

// runMenuStatus is the Status item: the `kb status` report.
func runMenuStatus() {
	if err := runStatus(os.Stdout); err != nil {
		menuError(err)
	}
}

// teeWriter copies everything written to it into buf as well.
type teeWriter struct {
	w   io.Writer
	buf bytes.Buffer
}

func (t *teeWriter) Write(p []byte) (int, error) {
	t.buf.Write(p)
	return t.w.Write(p)
}

// runMenuAdopt is the Adopt item: run `adopt --check`, show its output, and
// after a Confirm apply through runAdopt, which takes the bench lock.
func runMenuAdopt(ctx context.Context) {
	out := &teeWriter{w: os.Stdout}
	checkErr := runAdopt(ctx, out, nil, true)
	if checkErr != nil {
		_, _ = lipgloss.Fprintln(os.Stdout, ui.Warn.Render(checkErr.Error()))
	}
	if !adoptOffer(out.buf.String(), checkErr) {
		if checkErr == nil {
			_, _ = lipgloss.Fprintln(os.Stdout, ui.Dim.Render("Nothing to adopt."))
		}
		return
	}

	ok, err := menuConfirm("Apply adopt now?",
		"Writes a receipt for each app listed as \"would adopt\". apps/, sites and the database are not changed.")
	if err != nil {
		menuError(err)
		return
	}
	if !ok {
		_, _ = lipgloss.Fprintln(os.Stdout, ui.Dim.Render("Cancelled."))
		return
	}
	if err := runAdopt(ctx, os.Stdout, nil, false); err != nil {
		if adoptPartial(err) {
			// The matching apps were adopted; the apply output lists the rest.
			_, _ = lipgloss.Fprintln(os.Stdout, ui.Warn.Render("Adopted the matching apps; "+err.Error()))
			return
		}
		menuError(err)
		return
	}
	_, _ = lipgloss.Fprintln(os.Stdout, ui.Success.Render("Adopt finished."))
}

// runMenuUpdate is the Update kb item: check GitHub, show the result and,
// after a Confirm, install the new binary. It returns true when the binary was
// replaced; the menu must then be left because the running process is the old
// version.
func runMenuUpdate(ctx context.Context) bool {
	info, err := checkLatestRelease()
	if err != nil {
		menuError(err)
		return false
	}
	reportUpdateCheck(info)
	if !info.Newer {
		return false
	}

	downloadURL, checksumsURL, target, err := updateAssetURLs(info)
	if err != nil {
		menuError(err)
		return false
	}
	if err := updateLicenseGate(ctx); err != nil {
		menuError(err)
		return false
	}

	ok, err := menuConfirm(fmt.Sprintf("Install kb %s?", info.Latest),
		"The running binary is replaced; kb then has to be restarted.")
	if err != nil {
		menuError(err)
		return false
	}
	if !ok {
		_, _ = lipgloss.Fprintln(os.Stdout, ui.Dim.Render("Update cancelled."))
		return false
	}
	if err := installUpdate(info.Latest, downloadURL, checksumsURL, target); err != nil {
		menuError(err)
		return false
	}
	_, _ = lipgloss.Fprintln(os.Stdout, ui.Success.Render(fmt.Sprintf("kb %s is installed. Restart kb to use it; leaving the menu now.", info.Latest)))
	return true
}
