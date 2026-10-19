package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"

	"github.com/KB-Developpement/kb_pro_cli/internal/apps"
	"github.com/KB-Developpement/kb_pro_cli/internal/bench"
	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
	"github.com/KB-Developpement/kb_pro_cli/internal/ui"
)

// fetchAppsFn is the version-check request; tests replace it.
var fetchAppsFn = license.FetchApps

// States of one `kb updates` row. The values are the JSON keys; stateLabels
// holds the words the table shows.
const (
	stateUpToDate        = "up_to_date"
	stateUpdateAvailable = "update_available"
	stateAhead           = "ahead"
	stateDiffers         = "differs"
	stateUnknown         = "unknown"
	stateNotArchive      = "not_archive_managed"
)

var stateLabels = map[string]string{
	stateUpToDate:        "up to date",
	stateUpdateAvailable: "update available",
	stateAhead:           "ahead",
	stateDiffers:         "differs",
	stateUnknown:         "unknown",
	stateNotArchive:      "not archive-managed",
}

// updateRow is one app of the version check. The commits are the full 40-hex
// ids; the table shortens them to 12 characters.
type updateRow struct {
	App             string `json:"app"`
	InstalledTag    string `json:"installed_tag"`
	InstalledCommit string `json:"installed_commit"`
	Provenance      string `json:"provenance"`
	Line            int    `json:"line"`
	LatestTag       string `json:"latest_tag"`
	LatestCommit    string `json:"latest_commit"`
	State           string `json:"state"`
	Note            string `json:"note,omitempty"`
}

// appInstall is what the check knows about one registry app present in the
// bench: whether it holds a Git checkout and what its receipt says.
type appInstall struct {
	App     apps.App
	Git     bool
	Receipt kbstate.ReceiptState
}

// shortCommit is the 12-character form of a commit id the table shows.
func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

// installLine is the release line to look up: the receipt's line when it is a
// positive integer, else the registry line.
func installLine(in appInstall) int {
	if in.Receipt.Valid() {
		if n, err := strconv.Atoi(in.Receipt.Receipt.Line); err == nil && n >= 1 {
			return n
		}
	}
	return in.App.Line
}

// compareApps decides the state of every installed app against the server's
// answer. It is pure: no file, no network.
func compareApps(installs []appInstall, resp *license.AppsResponse) []updateRow {
	rows := make([]updateRow, 0, len(installs))
	for _, in := range installs {
		rows = append(rows, compareApp(in, resp))
	}
	return rows
}

func compareApp(in appInstall, resp *license.AppsResponse) updateRow {
	row := updateRow{
		App:        in.App.Name,
		Provenance: in.Receipt.Provenance(),
		Line:       installLine(in),
	}
	if in.Receipt.Valid() {
		row.InstalledTag = in.Receipt.Receipt.Tag()
		row.InstalledCommit = in.Receipt.Receipt.Commit
	}

	switch {
	case in.Git:
		row.State, row.Note = stateNotArchive, "Git checkout, kb does not manage its source"
		return row
	case in.Receipt.Newer != nil:
		row.State, row.Note = stateUnknown, "receipt written by a newer kb: run kb update"
		return row
	case !in.Receipt.Valid():
		row.State = stateUnknown
		if r := in.Receipt.Reason; r == "" || r == "no receipt" {
			row.Note = "no receipt: run kb adopt"
		} else {
			row.Note = "no valid receipt (" + r + "): run kb adopt"
		}
		return row
	case in.Receipt.Receipt.Provenance == kbstate.ProvMaintenance || in.Receipt.Receipt.State != kbstate.StateArchive:
		row.State, row.Note = stateNotArchive, "source is not an archive release ("+in.Receipt.Receipt.Provenance+")"
		return row
	case row.Line != in.App.Line:
		// kb upgrade downloads the registry line, so an update on the
		// receipt's line is not something it can apply.
		row.State = stateDiffers
		row.Note = fmt.Sprintf("receipt line %d, kb upgrade installs line %d", row.Line, in.App.Line)
		return row
	}

	entry, ok := resp.App(in.App.Name)
	switch {
	case !ok:
		row.State, row.Note = stateUnknown, "not in the server's /apps answer"
		return row
	case entry.Error != "":
		row.State, row.Note = stateUnknown, entry.Error
		return row
	}
	latest, ok := entry.Line(row.Line)
	switch {
	case !ok:
		row.State, row.Note = stateUnknown, fmt.Sprintf("line %d not in the server's answer", row.Line)
		return row
	case latest.Error != "":
		row.State, row.Note = stateUnknown, latest.Error
		return row
	}
	row.LatestTag, row.LatestCommit = latest.Tag, latest.Commit

	row.State, row.Note = compareReleases(row.InstalledTag, row.InstalledCommit, latest.Tag, latest.Commit)
	if latest.Stale {
		if row.Note != "" {
			row.Note += "; "
		}
		row.Note += "server answer is cached"
	}
	return row
}

// compareReleases is the state of an installed (tag, commit) against the
// newest release on its line.
func compareReleases(instTag, instCommit, latestTag, latestCommit string) (state, note string) {
	if instCommit == latestCommit {
		return stateUpToDate, ""
	}
	if !semver.IsValid(instTag) || !semver.IsValid(latestTag) {
		return stateDiffers, "tags are not comparable"
	}
	switch c := semver.Compare(latestTag, instTag); {
	case c > 0:
		return stateUpdateAvailable, ""
	case c < 0:
		return stateAhead, ""
	case instTag == latestTag:
		return stateUpdateAvailable, "tag moved"
	default:
		return stateDiffers, "tags are not comparable"
	}
}

// collectInstalls reads the bench: every registry app that has an apps/
// directory, with its Git state and receipt. The framework is not part of the
// registry list `kb upgrade` manages, so it is not checked.
func collectInstalls() []appInstall {
	root := bench.Root()
	present := bench.DetectAppsInBench()
	var out []appInstall
	for _, a := range apps.All {
		if !present[a.Dir()] {
			continue
		}
		out = append(out, appInstall{
			App:     a,
			Git:     bench.GitGuard(root, a.Dir()) != nil,
			Receipt: kbstate.ReadReceipt(root, a.Dir()),
		})
	}
	return out
}

// checkUpdates reads the bench, asks the license server for the newest
// releases and compares. It takes no lock and writes nothing. spin shows the
// spinner (with its non-TTY gate) while the request runs.
func checkUpdates(ctx context.Context, spin bool) ([]updateRow, error) {
	installs := collectInstalls()
	if len(installs) == 0 {
		return []updateRow{}, nil
	}
	token, serverURL, err := licenseTokenAndServer(ctx)
	if err != nil {
		return nil, err
	}
	var resp *license.AppsResponse
	fetch := func(ctx context.Context) error {
		var e error
		resp, e = fetchAppsFn(ctx, serverURL, token)
		return e
	}
	if spin {
		err = runWithSpinnerErr(ctx, "Checking for updates…", fetch)
	} else {
		err = fetch(ctx)
	}
	if err != nil {
		return nil, err
	}
	return compareApps(installs, resp), nil
}

// updatableRows are the rows whose state is "update available".
func updatableRows(rows []updateRow) []updateRow {
	var out []updateRow
	for _, r := range rows {
		if r.State == stateUpdateAvailable {
			out = append(out, r)
		}
	}
	return out
}

// tagCell renders a tag with its short commit, "-" when there is none.
func tagCell(tag, commit string) string {
	if tag == "" && commit == "" {
		return "-"
	}
	if commit == "" {
		return tag
	}
	return tag + " " + shortCommit(commit)
}

// stateCell is the state label with its note.
func stateCell(r updateRow) string {
	label := stateLabels[r.State]
	if label == "" {
		label = r.State
	}
	if r.Note != "" {
		return label + " (" + r.Note + ")"
	}
	return label
}

// updateTableRows builds the cells of the table, one slice per app.
func updateTableRows(rows []updateRow) [][]string {
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, []string{
			r.App,
			tagCell(r.InstalledTag, r.InstalledCommit),
			r.Provenance,
			strconv.Itoa(r.Line),
			tagCell(r.LatestTag, r.LatestCommit),
			stateCell(r),
		})
	}
	return out
}

// printUpdateTable draws the table through lipgloss so a pipe gets no ANSI.
func printUpdateTable(w io.Writer, rows []updateRow) {
	if len(rows) == 0 {
		_, _ = lipgloss.Fprintln(w, ui.Dim.Render("No KB apps found in this bench."))
		return
	}
	cells := updateTableRows(rows)
	t := table.New().
		Headers("App", "Installed", "Source", "Line", "Latest", "State").
		Rows(cells...).
		StyleFunc(func(row, col int) lipgloss.Style {
			base := lipgloss.NewStyle().Padding(0, 1)
			if row == table.HeaderRow {
				return base.Bold(true)
			}
			if col != 5 || row >= len(rows) {
				return base
			}
			switch rows[row].State {
			case stateUpToDate:
				return base.Inherit(ui.Success)
			case stateUpdateAvailable:
				return base.Inherit(ui.Warn)
			default:
				return base.Inherit(ui.Dim)
			}
		})
	_, _ = lipgloss.Fprintln(w, t.String())
}

// runUpdates is `kb updates`: check, then print the table or the rows as JSON.
func runUpdates(ctx context.Context, w io.Writer, asJSON bool) error {
	rows, err := checkUpdates(ctx, false)
	if err != nil {
		return err
	}
	if asJSON {
		data, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(data))
		return err
	}
	printUpdateTable(w, rows)
	if upd := updatableRows(rows); len(upd) > 0 {
		names := make([]string, len(upd))
		for i, r := range upd {
			names[i] = r.App
		}
		_, _ = lipgloss.Fprintln(w, ui.Dim.Render("Upgrade with: kb upgrade --apps "+strings.Join(names, ",")))
	}
	return nil
}

// newUpdatesCmd returns "kb updates": a read-only comparison of the installed
// receipts with the newest release on each line.
func newUpdatesCmd() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "updates",
		Short: "Show which installed KB apps have a newer release on their line",
		Long: `Compare each KB app in the bench with the newest published release on its line.

kb reads the receipt of every app (tag and commit), asks the license server for
the newest release per line (GET /apps) and prints one row per app:

  up to date          the installed commit is the newest release
  update available    a newer release exists, or the same tag now points at another commit
  ahead               the installed tag is newer than the newest release
  differs             the tags cannot be ordered (not both valid semver, or equal versions with
                      different tag names), or the receipt's line is not the one kb upgrade installs
  unknown             no valid receipt (run kb adopt), the app is missing from the server's
                      answer, or the server reports an error for its line
  not archive-managed the app is a Git checkout or came from a maintenance source

It takes no lock, changes nothing in the bench and exits 0 whether or not updates
exist; it exits non-zero when the check itself fails, including a license server
that predates the version check. Apply updates with kb upgrade.

Examples:
  kb updates
  kb updates --json`,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireInitializedForCLI(); err != nil {
				return err
			}
			if !bench.InBenchContainer() {
				return errors.New("kb must be run inside a Frappe bench container — use: ffm shell <bench-name>")
			}
			return runUpdates(cmd.Context(), cmd.OutOrStdout(), asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the rows as JSON")
	return cmd
}
