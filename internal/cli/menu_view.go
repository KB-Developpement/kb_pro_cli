package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
)

// menuState is what decides which main-menu items are offered.
type menuState struct {
	// Stock is true while apps/frappe is still the stock frappe/frappe repo:
	// the menu is then reduced to what a fresh bench needs.
	Stock bool
}

// menuLabel pads name so the em dashes of all labels line up.
func menuLabel(name, desc string) string {
	return fmt.Sprintf("%-22s— %s", name, desc)
}

// mainMenuOptions returns the items of the main menu for a state. It never
// returns an empty list: huh panics on a Select without options.
func mainMenuOptions(s menuState) []huh.Option[string] {
	if s.Stock {
		// License + Settings are always shown so a freshly-installed bench can
		// reach activation. Init KB Frappe enforces its own license check
		// inside runInitKBFrappe and surfaces a clear error if kb_frappe is
		// not in the activated allowed_apps.
		return []huh.Option[string]{
			huh.NewOption(menuLabel("Init KB Frappe", "replace stock Frappe with KB fork"), menuInitKBFrappe),
			huh.NewOption(menuLabel("Status", "provenance, journal, lock holder (read-only)"), menuStatus),
			huh.NewOption(menuLabel("License", "status, activate, deactivate locally"), menuLicense),
			huh.NewOption(menuLabel("Update kb", "check GitHub, replace this binary"), menuUpdate),
			huh.NewOption(menuLabel("Settings", "license server URL, GitHub token"), menuSettings),
		}
	}
	return []huh.Option[string]{
		huh.NewOption(menuLabel("Install apps", "download and install on this site"), menuInstall),
		huh.NewOption(menuLabel("Add apps to bench", "download only, skip site install"), menuAdd),
		huh.NewOption(menuLabel("Site-install apps", "install already-downloaded apps on site"), menuSiteInstall),
		huh.NewOption(menuLabel("Manage apps", "uninstall / remove from bench"), menuManage),
		huh.NewOption(menuLabel("Upgrade apps", "pull latest changes and migrate"), menuUpgrade),
		huh.NewOption(menuLabel("Check for updates", "compare installed apps with the newest releases"), menuCheckUpdates),
		huh.NewOption(menuLabel("Upgrade to a release", "install one exact published tag of an app"), menuUpgradeTo),
		huh.NewOption(menuLabel("Status", "provenance, journal, lock holder (read-only)"), menuStatus),
		huh.NewOption(menuLabel("Adopt", "match installed apps to releases, write receipts"), menuAdopt),
		huh.NewOption(menuLabel("License", "status, activate, deactivate locally"), menuLicense),
		huh.NewOption(menuLabel("Update kb", "check GitHub, replace this binary"), menuUpdate),
		huh.NewOption(menuLabel("Settings", "license server URL, GitHub token"), menuSettings),
	}
}

// menuHeaderState is everything the menu header shows, read from local state
// only. Empty or zero fields mean "nothing to report".
type menuHeaderState struct {
	Version string
	License *license.State // nil: no usable license cache
	Root    string
	Site    string
	// Journal is a one-line warning about .kb/journal.json, "" when nothing
	// needs attention.
	Journal string
	// LockPID is the PID named in .kb/lock when that process is another live
	// one, else 0.
	LockPID int
	// NewerKB is the latest kb release known from the cached update check,
	// "" when the running kb is current or the cache is empty.
	NewerKB string
}

// headerLine is one line of the menu header. Warn lines are drawn in the
// warning colour.
type headerLine struct {
	Text string
	Warn bool
}

// menuHeader renders the header as plain text lines: two info lines, then one
// line per warning.
func menuHeader(s menuHeaderState) []headerLine {
	lines := []headerLine{
		{Text: fmt.Sprintf("kb %s · License: %s", s.Version, licenseSummary(s.License))},
		{Text: fmt.Sprintf("Bench: %s · Site: %s", s.Root, s.Site)},
	}
	if s.Journal != "" {
		lines = append(lines, headerLine{Text: s.Journal, Warn: true})
	}
	if s.LockPID > 0 {
		lines = append(lines, headerLine{
			Text: fmt.Sprintf("Lock: .kb/lock names PID %d (running) — another kb operation may be in progress", s.LockPID),
			Warn: true,
		})
	}
	if s.NewerKB != "" {
		lines = append(lines, headerLine{
			Text: fmt.Sprintf("Newer kb available: %s (you have %s) — choose Update kb", s.NewerKB, s.Version),
			Warn: true,
		})
	}
	return lines
}

// licenseSummary is the one-line state of the locally cached license.
func licenseSummary(st *license.State) string {
	switch {
	case st == nil:
		return "not activated"
	case !st.Valid:
		return "token expired " + st.ExpiresAt.Format("2006-01-02")
	default:
		return fmt.Sprintf("active (%s, token valid until %s)", st.Tier, st.ExpiresAt.Format("2006-01-02"))
	}
}

// journalWarning describes a journal that needs attention, "" when there is
// none. It mirrors what `kb status` reports.
func journalWarning(js kbstate.JournalState) string {
	switch {
	case js.Missing:
		return ""
	case js.Newer != nil:
		return "Journal was written by a newer kb — update kb before changing this bench"
	case js.Malformed != "":
		return fmt.Sprintf("Journal is unusable (%s) — see Status", js.Malformed)
	case js.Journal.Pending():
		j := js.Journal
		return fmt.Sprintf("Unfinished %s of %s (step %s) — kb resolves it before the next change; see Status", j.Command, j.App, j.Step)
	}
	return ""
}

// lockHolder returns the PID to warn about: the one named in .kb/lock when it
// is alive and is not this process. The lock file keeps its PID after release,
// and after a menu action that took the lock that PID is our own.
func lockHolder(pid int, ok bool, self int, alive func(int) bool) int {
	if !ok || pid == self || !alive(pid) {
		return 0
	}
	return pid
}

// newerKnown returns latest when it is a release newer than the running
// version, using the same rule as the startup notice. Dev builds never see one.
func newerKnown(current, latest string) string {
	if current == "dev" || current == "" || latest == "" || !newerThan(current, latest) {
		return ""
	}
	return latest
}

// adoptOffer reports whether the output and result of `adopt --check` justify
// offering to apply: at least one app would be adopted, and the run was not
// refused or stopped.
func adoptOffer(checkOutput string, checkErr error) bool {
	if !strings.Contains(checkOutput, "would adopt") {
		return false
	}
	if checkErr == nil {
		return true
	}
	return adoptPartial(checkErr)
}

// adoptPartial reports whether err is an adopt result that still adopts the
// matching apps: some app did not match a release, or matched several.
func adoptPartial(err error) bool {
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code == adoptMismatch || ee.Code == adoptAmbiguous
	}
	return false
}
