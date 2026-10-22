package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/KB-Developpement/kb_pro_cli/internal/apps"
	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
	"github.com/KB-Developpement/kb_pro_cli/internal/testutil"
)

const (
	commitA = "0123456789abcdef0123456789abcdef01234567"
	commitB = "fedcba9876543210fedcba9876543210fedcba98"
)

// receiptFor is a valid receipt state for kb_pro.
func receiptFor(line, tag, commit, prov, state string) kbstate.ReceiptState {
	t := tag
	return kbstate.ReceiptState{Receipt: &kbstate.Receipt{
		SchemaVersion: 1, App: "kb_pro", Directory: "kb_pro", Package: "kb_pro",
		Repository: "KB-Developpement/kb_pro", Line: line, Ref: tag, Commit: commit, ReleaseTag: &t,
		State: state, Provenance: prov,
	}}
}

func installedKBPro(tag, commit string) appInstall {
	a, _ := apps.ByName("kb_pro")
	return appInstall{App: a, Receipt: receiptFor("1", tag, commit, kbstate.ProvInstalled, kbstate.StateArchive)}
}

// answer is a /apps answer for kb_pro with one line.
func answer(line int, tag, commit, lineErr string) *license.AppsResponse {
	return &license.AppsResponse{Apps: []license.AppEntry{{
		App: "kb_pro", DefaultLine: 1,
		Lines: []license.AppLine{{Line: line, Tag: tag, Commit: commit, Error: lineErr}},
	}}}
}

func TestCompareAppsStates(t *testing.T) {
	a, _ := apps.ByName("kb_pro")
	tests := []struct {
		name  string
		in    appInstall
		resp  *license.AppsResponse
		state string
		note  string // substring of the note
		line  int
	}{
		{"same commit", installedKBPro("v1.2.0", commitA), answer(1, "v1.2.0", commitA, ""), stateUpToDate, "", 1},
		{"same commit other tag", installedKBPro("v1.1.0", commitA), answer(1, "v1.2.0", commitA, ""), stateUpToDate, "", 1},
		{"newer tag", installedKBPro("v1.1.0", commitA), answer(1, "v1.2.0", commitB, ""), stateUpdateAvailable, "", 1},
		{"numeric not lexical", installedKBPro("v1.9.0", commitA), answer(1, "v1.10.0", commitB, ""), stateUpdateAvailable, "", 1},
		{"tag moved", installedKBPro("v1.2.0", commitA), answer(1, "v1.2.0", commitB, ""), stateUpdateAvailable, "tag moved", 1},
		{"ahead", installedKBPro("v1.3.0", commitA), answer(1, "v1.2.0", commitB, ""), stateAhead, "", 1},
		{"prerelease installed is behind its release", installedKBPro("v1.2.0-rc1", commitA), answer(1, "v1.2.0", commitB, ""), stateUpdateAvailable, "", 1},
		{"installed ref is not a semver", installedKBPro("main", commitA), answer(1, "v1.2.0", commitB, ""), stateDiffers, "not comparable", 1},
		{"same non-semver ref", installedKBPro("main", commitA), answer(1, "main", commitB, ""), stateDiffers, "not comparable", 1},
		{"build metadata only", installedKBPro("v1.2.0+a", commitA), answer(1, "v1.2.0+b", commitB, ""), stateDiffers, "not comparable", 1},
		{"stale answer", installedKBPro("v1.1.0", commitA), &license.AppsResponse{Apps: []license.AppEntry{{
			App: "kb_pro", Lines: []license.AppLine{{Line: 1, Tag: "v1.2.0", Commit: commitB, Stale: true}}}}}, stateUpdateAvailable, "cached", 1},

		{"app missing from answer", installedKBPro("v1.1.0", commitA), &license.AppsResponse{}, stateUnknown, "not in the server's", 1},
		{"app error", installedKBPro("v1.1.0", commitA), &license.AppsResponse{Apps: []license.AppEntry{{App: "kb_pro", Error: "line_not_found"}}}, stateUnknown, "line_not_found", 1},
		{"line missing", installedKBPro("v1.1.0", commitA), answer(2, "v2.0.0", commitB, ""), stateUnknown, "line 1 not in", 1},
		{"line error", installedKBPro("v1.1.0", commitA), answer(1, "", "", "upstream_error"), stateUnknown, "upstream_error", 1},
		{"malformed line", installedKBPro("v1.1.0", commitA), answer(1, "", "", license.ErrorMalformed), stateUnknown, license.ErrorMalformed, 1},

		{"receipt line other than the registry line is not offered", appInstall{App: a, Receipt: receiptFor("2", "v2.0.0", commitA, kbstate.ProvAdopted, kbstate.StateArchive)}, answer(2, "v2.1.0", commitB, ""), stateDiffers, "receipt line 2, kb upgrade installs line 1", 2},
		{"bad receipt line falls back to the registry", appInstall{App: a, Receipt: receiptFor("x", "v1.0.0", commitA, kbstate.ProvInstalled, kbstate.StateArchive)}, answer(1, "v1.0.0", commitA, ""), stateUpToDate, "", 1},
		{"zero receipt line falls back to the registry", appInstall{App: a, Receipt: receiptFor("0", "v1.0.0", commitA, kbstate.ProvInstalled, kbstate.StateArchive)}, answer(1, "v1.0.0", commitA, ""), stateUpToDate, "", 1},

		{"no receipt", appInstall{App: a, Receipt: kbstate.ReceiptState{Reason: "no receipt"}}, answer(1, "v1.2.0", commitB, ""), stateUnknown, "run kb adopt", 1},
		{"invalid receipt", appInstall{App: a, Receipt: kbstate.ReceiptState{Reason: "receipt is invalid: line is empty"}}, answer(1, "v1.2.0", commitB, ""), stateUnknown, "line is empty", 1},
		{"newer receipt schema", appInstall{App: a, Receipt: kbstate.ReceiptState{Reason: "x", Newer: &kbstate.SchemaError{}}}, answer(1, "v1.2.0", commitB, ""), stateUnknown, "kb update", 1},

		{"git checkout", appInstall{App: a, Git: true, Receipt: kbstate.ReceiptState{Reason: "no receipt"}}, answer(1, "v1.2.0", commitB, ""), stateNotArchive, "Git checkout", 1},
		{"git checkout with receipt", appInstall{App: a, Git: true, Receipt: receiptFor("1", "v1.0.0", commitA, kbstate.ProvInstalled, kbstate.StateArchive)}, answer(1, "v1.2.0", commitB, ""), stateNotArchive, "Git checkout", 1},
		{"maintenance provenance", appInstall{App: a, Receipt: receiptFor("1", "v1.0.0", commitA, kbstate.ProvMaintenance, kbstate.StateArchive)}, answer(1, "v1.2.0", commitB, ""), stateNotArchive, "maintenance", 1},
		{"non-archive receipt state", appInstall{App: a, Receipt: receiptFor("1", "v1.0.0", commitA, kbstate.ProvInstalled, "active")}, answer(1, "v1.2.0", commitB, ""), stateNotArchive, "", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rows := compareApps([]appInstall{tc.in}, tc.resp)
			if len(rows) != 1 {
				t.Fatalf("rows = %+v", rows)
			}
			r := rows[0]
			if r.State != tc.state {
				t.Errorf("state = %q (note %q), want %q", r.State, r.Note, tc.state)
			}
			if !strings.Contains(r.Note, tc.note) {
				t.Errorf("note = %q, want it to contain %q", r.Note, tc.note)
			}
			if r.Line != tc.line {
				t.Errorf("line = %d, want %d", r.Line, tc.line)
			}
			if r.App != "kb_pro" {
				t.Errorf("app = %q", r.App)
			}
			if r.State == stateUnknown || r.State == stateNotArchive {
				if r.LatestTag != "" || r.LatestCommit != "" {
					t.Errorf("an %s row carries latest %q %q", r.State, r.LatestTag, r.LatestCommit)
				}
			}
		})
	}
}

// A receipt on a line other than the registry line must not reach the menu
// upgrade: kb upgrade would install the registry line, not the one shown.
func TestUpdatableRowsExcludesReceiptLineMismatch(t *testing.T) {
	a, _ := apps.ByName("kb_pro")
	in := appInstall{App: a, Receipt: receiptFor("2", "v2.0.0", commitA, kbstate.ProvInstalled, kbstate.StateArchive)}
	rows := compareApps([]appInstall{in, installedKBPro("v1.1.0", commitA)}, &license.AppsResponse{Apps: []license.AppEntry{{
		App: "kb_pro", Lines: []license.AppLine{
			{Line: 1, Tag: "v1.2.0", Commit: commitB},
			{Line: 2, Tag: "v2.1.0", Commit: commitB},
		}}}})
	if rows[0].State != stateDiffers || rows[1].State != stateUpdateAvailable {
		t.Fatalf("states = %q, %q", rows[0].State, rows[1].State)
	}
	if got := updatableRows(rows[:1]); len(got) != 0 {
		t.Errorf("updatableRows offered a line mismatch: %+v", got)
	}
	if got := updatableRows(rows); len(got) != 1 || got[0].Line != 1 {
		t.Errorf("updatableRows = %+v, want only the registry-line row", got)
	}
}

// A compared row carries both sides; the table shows 12 commit characters.
func TestCompareAppsRowFields(t *testing.T) {
	rows := compareApps([]appInstall{installedKBPro("v1.1.0", commitA)}, answer(1, "v1.2.0", commitB, ""))
	r := rows[0]
	if r.InstalledTag != "v1.1.0" || r.InstalledCommit != commitA || r.Provenance != kbstate.ProvInstalled ||
		r.LatestTag != "v1.2.0" || r.LatestCommit != commitB {
		t.Fatalf("row = %+v", r)
	}
	cells := updateTableRows(rows)[0]
	if cells[1] != "v1.1.0 "+commitA[:12] || cells[4] != "v1.2.0 "+commitB[:12] || cells[2] != "installed" || cells[3] != "1" {
		t.Errorf("cells = %q", cells)
	}
	if cells[5] != "update available" {
		t.Errorf("state cell = %q", cells[5])
	}
	if got := shortCommit("abc"); got != "abc" {
		t.Errorf("shortCommit(abc) = %q", got)
	}
}

func TestStateCellAndLabels(t *testing.T) {
	for state := range stateLabels {
		if stateCell(updateRow{State: state}) == "" {
			t.Errorf("no label for %s", state)
		}
	}
	if got := stateCell(updateRow{State: stateUpdateAvailable, Note: "tag moved"}); got != "update available (tag moved)" {
		t.Errorf("stateCell = %q", got)
	}
}

func TestPrintUpdateTableIsPlainOnAPipe(t *testing.T) {
	rows := compareApps([]appInstall{installedKBPro("v1.1.0", commitA)}, answer(1, "v1.2.0", commitB, ""))
	var buf bytes.Buffer
	printUpdateTable(&buf, rows)
	out := buf.String()
	if strings.Contains(out, "\x1b[") {
		t.Errorf("ANSI in non-terminal output: %q", out)
	}
	for _, want := range []string{"App", "Installed", "Source", "Line", "Latest", "State", "kb_pro", "v1.1.0 " + commitA[:12], "v1.2.0 " + commitB[:12], "update available"} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, commitA[:13]) {
		t.Errorf("table shows more than 12 commit characters:\n%s", out)
	}

	buf.Reset()
	printUpdateTable(&buf, nil)
	if !strings.Contains(buf.String(), "No KB apps found") {
		t.Errorf("empty table output = %q", buf.String())
	}
}

// ── the command, against a throw-away bench ────────────────────────────────

// stubFetchApps replaces the /apps request for one test and records its
// arguments.
func stubFetchApps(t *testing.T, resp *license.AppsResponse, err error) *[]string {
	t.Helper()
	var calls []string
	old := fetchAppsFn
	fetchAppsFn = func(_ context.Context, serverURL, token string) (*license.AppsResponse, error) {
		calls = append(calls, serverURL+" "+token)
		return resp, err
	}
	t.Cleanup(func() { fetchAppsFn = old })
	return &calls
}

// updatesBench seeds kb_pro (installed v1.0.0), kb_compta (installed v1.2.0),
// kb_cheque (no receipt) and kb_facilite (a Git checkout).
func updatesBench(t *testing.T) *benchH {
	t.Helper()
	h := newBenchH(t)
	h.seed("kb_pro", "v1.0.0", true)
	h.seed("kb_compta", "v1.2.0", true)
	h.seed("kb_cheque", "v1.0.0", false)
	h.seed("kb_facilite", "v1.0.0", false)
	must(t, os.MkdirAll(h.appDir("kb_facilite")+"/.git", 0o755))
	return h
}

func serverAnswer() *license.AppsResponse {
	return &license.AppsResponse{CheckedAt: "2026-10-18T10:00:00Z", Apps: []license.AppEntry{
		{App: "kb_pro", DefaultLine: 1, Lines: []license.AppLine{{Line: 1, Tag: "v1.2.0", Commit: commitFor("kb_pro", "v1.2.0")}}},
		{App: "kb_compta", DefaultLine: 1, Lines: []license.AppLine{{Line: 1, Tag: "v1.2.0", Commit: commitFor("kb_compta", "v1.2.0")}}},
		{App: "kb_cheque", DefaultLine: 1, Lines: []license.AppLine{{Line: 1, Tag: "v1.2.0", Commit: commitFor("kb_cheque", "v1.2.0")}}},
	}}
}

func TestUpdatesCommandJSONIsReadOnly(t *testing.T) {
	h := updatesBench(t)
	calls := stubFetchApps(t, serverAnswer(), nil)
	h.holdLock() // the check takes no lock, so a held one does not stop it
	before := h.benchDigest()
	kbBefore := testutil.TreeDigestExcluding(t, kbstate.Dir(h.root), func(rel string) bool { return rel == "lock" })

	out, err := h.exec("updates", "--json")
	if err != nil {
		t.Fatalf("kb updates --json: %v\n%s", err, out)
	}
	var rows []updateRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("output is not the rows as JSON: %v\n%s", err, out)
	}
	byApp := map[string]updateRow{}
	for _, r := range rows {
		byApp[r.App] = r
	}
	if len(rows) != 4 {
		t.Fatalf("rows = %+v", rows)
	}
	want := map[string]string{
		"kb_pro": stateUpdateAvailable, "kb_compta": stateUpToDate,
		"kb_cheque": stateUnknown, "kb_facilite": stateNotArchive,
	}
	for app, state := range want {
		if byApp[app].State != state {
			t.Errorf("%s state = %q (%s), want %q", app, byApp[app].State, byApp[app].Note, state)
		}
	}
	if r := byApp["kb_pro"]; r.InstalledTag != "v1.0.0" || r.InstalledCommit != commitFor("kb_pro", "v1.0.0") ||
		r.LatestTag != "v1.2.0" || r.Line != 1 || r.Provenance != kbstate.ProvInstalled {
		t.Errorf("kb_pro row = %+v", r)
	}
	if len(*calls) != 1 || (*calls)[0] != h.srv.srv.URL+" tok" {
		t.Errorf("fetch calls = %q", *calls)
	}
	if h.benchDigest() != before || testutil.TreeDigestExcluding(t, kbstate.Dir(h.root), func(rel string) bool { return rel == "lock" }) != kbBefore {
		t.Error("kb updates changed the bench or .kb")
	}
	if n := len(h.srv.downloads()); n != 0 {
		t.Errorf("kb updates downloaded %d archives", n)
	}
}

func TestUpdatesCommandTable(t *testing.T) {
	h := updatesBench(t)
	stubFetchApps(t, serverAnswer(), nil)

	out, err := h.exec("updates")
	if err != nil {
		t.Fatalf("kb updates: %v\n%s", err, out)
	}
	for _, want := range []string{"update available", "up to date", "unknown", "not archive-managed", "run kb adopt",
		"Upgrade with: kb upgrade --apps kb_pro"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b[") {
		t.Errorf("ANSI in captured output: %q", out)
	}
}

// Exit 0 whether or not updates exist.
func TestUpdatesCommandNothingToUpdate(t *testing.T) {
	h := newBenchH(t)
	h.seed("kb_pro", "v1.2.0", true)
	stubFetchApps(t, &license.AppsResponse{Apps: []license.AppEntry{
		{App: "kb_pro", Lines: []license.AppLine{{Line: 1, Tag: "v1.2.0", Commit: commitFor("kb_pro", "v1.2.0")}}}}}, nil)
	out, err := h.exec("updates")
	if err != nil {
		t.Fatalf("kb updates: %v\n%s", err, out)
	}
	if !strings.Contains(out, "up to date") || strings.Contains(out, "Upgrade with") {
		t.Errorf("output:\n%s", out)
	}
}

func TestUpdatesCommandEmptyBench(t *testing.T) {
	h := newBenchH(t)
	calls := stubFetchApps(t, nil, errors.New("must not be called"))
	out, err := h.exec("updates", "--json")
	if err != nil {
		t.Fatalf("kb updates: %v\n%s", err, out)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("output = %q, want []", out)
	}
	if len(*calls) != 0 {
		t.Errorf("fetched with nothing to compare: %q", *calls)
	}
}

func TestUpdatesCommandFetchFailureExitsNonZero(t *testing.T) {
	for name, fetchErr := range map[string]error{
		"unsupported": license.ErrAppsUnsupported,
		"http":        &license.HTTPError{App: "/apps", Status: 402, Key: "token_expired"},
		"network":     errors.New("dial tcp: refused"),
	} {
		t.Run(name, func(t *testing.T) {
			h := newBenchH(t)
			h.seed("kb_pro", "v1.0.0", true)
			stubFetchApps(t, nil, fetchErr)
			_, err := h.exec("updates")
			if !errors.Is(err, fetchErr) {
				t.Fatalf("err = %v, want %v", err, fetchErr)
			}
			if ExitCode(err) == 0 {
				t.Error("exit code 0 on a failed check")
			}
		})
	}
}

func TestUpdatesCommandNeedsALicense(t *testing.T) {
	h := newBenchH(t)
	h.seed("kb_pro", "v1.0.0", true)
	calls := stubFetchApps(t, serverAnswer(), nil)
	old := allowedSetFn
	allowedSetFn = func() map[string]bool { return nil }
	t.Cleanup(func() { allowedSetFn = old })
	_, err := h.exec("updates")
	if err == nil || !strings.Contains(err.Error(), "kb activate") {
		t.Fatalf("err = %v, want the activate hint", err)
	}
	if len(*calls) != 0 {
		t.Error("fetched without a license")
	}
}

func TestUpdatesCommandRegistered(t *testing.T) {
	cmd, _, err := newRootCmd().Find([]string{"updates"})
	if err != nil || cmd.Name() != "updates" {
		t.Fatalf("updates not registered: %v", err)
	}
	if cmd.Short == "" || cmd.Long == "" || cmd.Flags().Lookup("json") == nil {
		t.Error("updates lacks help text or --json")
	}
	if cmd.Annotations["skipLicenseCheck"] == "true" || cmd.Annotations["skipChecks"] == "true" {
		t.Errorf("updates must use the normal license check, annotations = %v", cmd.Annotations)
	}
}
