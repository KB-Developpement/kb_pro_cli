package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KB-Developpement/kb_pro_cli/internal/bench"
	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
	"github.com/KB-Developpement/kb_pro_cli/internal/testutil"
)

// stockFrappe makes apps/frappe a clean stock checkout without a receipt.
func stockFrappe(t *testing.T, h *benchH) {
	t.Helper()
	must(t, os.RemoveAll(h.appDir("frappe")))
	testutil.StockClone(t, h.appDir("frappe"), "https://github.com/frappe/frappe.git")
	must(t, os.Remove(h.receipt("frappe").Path))
}

// mutatingCommands runs every mutating command once and returns what each said.
func mutatingCommands(h *benchH) map[string]error {
	out := map[string]error{}
	run := func(name string, args ...string) {
		_, err := h.exec(args...)
		out[name] = err
	}
	run("add", "add", "--apps", "kb_facilite")
	run("install", "install", "--apps", "kb_facilite")
	run("site-install", "site-install", "--apps", "kb_cheque")
	run("upgrade", "upgrade", "--apps", "kb_cheque")
	run("init-kb-frappe", "init-kb-frappe", "--force")
	run("adopt", "adopt")
	out["manage"] = runLocked(context.Background(), func() error { return nil })
	return out
}

func TestFinalizeFailureStaysPendingAndIsReplayedByTheNextCommand(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantStep string
		prep     func(t *testing.T, h *benchH)
		check    func(t *testing.T, h *benchH)
	}{
		{"add", []string{"add", "--apps", "kb_pro"}, kbstate.StepSwapped, nil, nil},
		{"install", []string{"install", "--apps", "kb_pro"}, kbstate.StepSwapped, nil, nil},
		{"init-kb-frappe", []string{"init-kb-frappe"}, kbstate.StepSwapped, stockFrappe, func(t *testing.T, h *benchH) {
			if _, err := os.Stat(h.appDir("frappe.kb-old")); err == nil {
				t.Error("the stock tree was kept")
			}
		}},
		{"upgrade", []string{"upgrade", "--apps", "kb_pro"}, kbstate.StepMigrated, func(t *testing.T, h *benchH) { h.seed("kb_pro", "v1.0.0", true) }, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newBenchH(t)
			h.seed("kb_cheque", "v1.0.0", true)
			if c.prep != nil {
				c.prep(t, h)
			}
			dir := "kb_pro"
			if c.name == "init-kb-frappe" {
				dir = "frappe"
			}
			oldReceipt, _ := os.ReadFile(h.receipt(dir).Path)

			bench.FinalizeHook = func() error { return errors.New("stub: finalization fails") }
			out, err := h.exec(c.args...)
			if err == nil {
				t.Fatalf("finalize failure was swallowed (warning-only)\n%s", out)
			}
			if got := h.journal(); got.Step != c.wantStep || !got.Pending() {
				t.Fatalf("journal step = %s, want %s pending", got.Step, c.wantStep)
			}
			if got, _ := os.ReadFile(h.receipt(dir).Path); string(got) != string(oldReceipt) {
				t.Error("the receipt changed although finalization failed")
			}

			// The replay stub fails too: every mutating command refuses and names the journal path.
			before := h.benchDigest()
			for name, cerr := range mutatingCommands(h) {
				if cerr == nil || !strings.Contains(cerr.Error(), kbstate.JournalPath(h.root)) {
					t.Errorf("%s with a failing replay: %v", name, cerr)
				}
			}
			if h.benchDigest() != before {
				t.Error("a refused command changed the bench")
			}
			if !h.journal().Pending() {
				t.Fatal("journal no longer pending")
			}
			if _, err := h.exec("status"); err != nil {
				t.Errorf("status must keep working: %v", err)
			}

			// With the stub healthy again, the next mutating command replays first and proceeds.
			bench.FinalizeHook = nil
			if out, err := h.exec("site-install", "--apps", "kb_cheque"); err != nil {
				t.Fatalf("next command after the stub recovered: %v\n%s", err, out)
			}
			rs := h.receipt(dir)
			if !rs.Valid() || rs.Receipt.Commit != commitFor(rs.Receipt.App, "v1.2.0") {
				t.Fatalf("receipt after replay: %+v %s", rs.Receipt, rs.Reason)
			}
			if j := h.journal(); j.Pending() {
				t.Errorf("journal still pending after the replay: %s", j.Step)
			}
			if c.check != nil {
				c.check(t, h)
			}
		})
	}
}

func TestMultiAppRunStopsWhenAnAppLeavesTheJournalPending(t *testing.T) {
	h := newBenchH(t)
	calls := 0
	bench.FinalizeHook = func() error {
		calls++
		return errors.New("stub: finalization fails")
	}
	out, err := h.exec("install", "--apps", "kb_pro,kb_compta,kb_cheque")
	if err == nil {
		t.Fatalf("install succeeded\n%s", out)
	}
	if calls != 1 {
		t.Errorf("finalization was attempted %d times, want 1 (later apps are not attempted)", calls)
	}
	if !strings.Contains(out, "not attempted") {
		t.Errorf("the remaining apps are not reported as not attempted:\n%s", out)
	}
	for _, app := range []string{"kb_compta", "kb_cheque"} {
		if _, err := os.Stat(h.appDir(app)); err == nil {
			t.Errorf("apps/%s was touched although an earlier app left the journal pending", app)
		}
	}
	for _, l := range h.benchLogLines() {
		if strings.Contains(l, "install-app") {
			t.Errorf("site-install ran after a pending journal: %s", l)
		}
	}
	// archives are cleaned up
	matches, _ := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "kb-app-*"))
	if len(matches) != 0 {
		t.Errorf("downloaded archives left in the temp dir: %v", matches)
	}
	if got := h.journal(); got.App != "kb_pro" || !got.Pending() {
		t.Errorf("journal = %+v", got)
	}
}

func TestCleanRollbackLeavesNothingPending(t *testing.T) {
	h := newBenchH(t)
	h.seed("kb_cheque", "v1.0.0", true)
	before := h.digest("kb_cheque")
	t.Setenv("KB_FAKE_FAIL", "build")
	out, err := h.exec("upgrade", "--apps", "kb_cheque")
	if err == nil || !strings.Contains(out+err.Error(), "the previous version was restored") {
		t.Fatalf("err=%v\n%s", err, out)
	}
	if h.digest("kb_cheque") != before {
		t.Error("previous tree not restored")
	}
	if j := h.journal(); j.Pending() {
		t.Errorf("journal pending after a successful rollback: %s", j.Step)
	}
	// and the next mutating command is not blocked
	t.Setenv("KB_FAKE_FAIL", "")
	if out, err := h.exec("upgrade", "--apps", "kb_cheque"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestMigrateFailureBlocksTheNextMutationWithPaths(t *testing.T) {
	h := newBenchH(t)
	h.seed("kb_cheque", "v1.0.0", true)
	t.Setenv("KB_FAKE_FAIL", "migrate")
	out, err := h.exec("upgrade", "--apps", "kb_cheque")
	if err == nil {
		t.Fatalf("migrate failure not reported\n%s", out)
	}
	if j := h.journal(); j.Step != kbstate.StepSwapped {
		t.Fatalf("journal step = %s", j.Step)
	}
	t.Setenv("KB_FAKE_FAIL", "")
	before := h.benchDigest()
	_, err = h.exec("add", "--apps", "kb_pro")
	requireErr(t, err, kbstate.JournalPath(h.root), h.appDir("kb_cheque.kb-old"), h.appDir("kb_cheque"))
	if h.benchDigest() != before {
		t.Error("a refused command changed the bench")
	}
}

func TestRecoveryByStepThroughAdd(t *testing.T) {
	h := newBenchH(t)
	staging := h.appDir("kb_pro.kb-new")
	write(t, filepath.Join(staging, "half.txt"), "half-extracted by the interrupted run")
	decoy := h.appDir("kb_pro.kb-new-decoy")
	write(t, filepath.Join(decoy, "x"), "not recorded")
	kbDecoy := filepath.Join(h.root, ".kb", "similar")
	must(t, os.MkdirAll(kbDecoy, 0o700))
	archive := filepath.Join(os.Getenv("TMPDIR"), "kb-app-interrupted.tar.gz")
	must(t, os.WriteFile(archive, []byte("partial"), 0o600))
	decoyDigest := testutil.TreeDigest(t, decoy)

	j := kbstate.NewJournal(h.root)
	j.Op, j.Command, j.App, j.Directory = kbstate.OpRegister, bench.CmdAdd, "kb_pro", "kb_pro"
	j.Paths = []string{archive, staging}
	must(t, j.Advance(kbstate.StepDownloaded))

	out, err := h.exec("add", "--apps", "kb_compta")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, p := range []string{archive, staging} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("recorded path %s was not discarded", p)
		}
	}
	if testutil.TreeDigest(t, decoy) != decoyDigest {
		t.Error("an unrecorded directory with a similar name was touched")
	}
	if _, err := os.Stat(kbDecoy); err != nil {
		t.Error("an unrecorded path under .kb was touched")
	}
	if !h.receipt("kb_compta").Valid() {
		t.Error("the add did not proceed")
	}
}

func TestSchemaVersion2RefusesMutationsButNotStatus(t *testing.T) {
	h := newBenchH(t)
	h.seed("kb_cheque", "v1.0.0", true)
	path := h.receipt("kb_cheque").Path
	raw := `{"schema_version": 2, "app": "kb_cheque"}`
	must(t, os.WriteFile(path, []byte(raw), 0o600))
	before := h.benchDigest()

	for _, args := range [][]string{
		{"add", "--apps", "kb_pro"}, {"install", "--apps", "kb_pro"}, {"upgrade", "--apps", "kb_cheque"},
		{"init-kb-frappe", "--force"}, {"site-install", "--apps", "kb_cheque"}, {"adopt"},
	} {
		_, err := h.exec(args...)
		requireErr(t, err, path, "2", "1")
	}
	if h.benchDigest() != before {
		t.Error("the bench changed")
	}
	if got, _ := os.ReadFile(path); string(got) != raw {
		t.Error("the newer receipt was rewritten")
	}
	out, err := h.exec("status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "kb_cheque") || !strings.Contains(out, "newer") {
		t.Errorf("status does not report the newer receipt:\n%s", out)
	}
	if n := h.srv.requestCount(); n != 0 {
		t.Errorf("%d requests", n)
	}
}

func TestSchemaVersion2JournalRefuses(t *testing.T) {
	h := newBenchH(t)
	must(t, os.WriteFile(kbstate.JournalPath(h.root), []byte(`{"schema_version": 2, "step": "swapped"}`), 0o600))
	_, err := h.exec("add", "--apps", "kb_pro")
	requireErr(t, err, kbstate.JournalPath(h.root))
}

func TestManageRemoveDeletesTheReceipt(t *testing.T) {
	h := newBenchH(t)
	h.seed("kb_cheque", "v1.0.0", true)
	results, err := removeApps(context.Background(), "site1", []string{"kb_cheque"}, map[string]bool{}, false)
	if err != nil || len(results) != 1 || results[0].err != nil {
		t.Fatalf("removeApps: %v %+v", err, results)
	}
	if h.receipt("kb_cheque").Valid() {
		t.Error("a removed app's receipt was kept; it could vouch for a different tree later")
	}
	found := false
	for _, l := range h.benchLogLines() {
		if strings.Contains(l, "remove-app kb_cheque") {
			found = true
		}
	}
	if !found {
		t.Error("bench remove-app was not called")
	}
}

func TestNoMaintenanceCommandInPhase0(t *testing.T) {
	h := newBenchH(t)
	_, err := h.exec("maintenance")
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("kb maintenance: %v", err)
	}
	out, err := h.exec("--help")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(out), "maintenance") {
		t.Errorf("--help lists a maintenance entry:\n%s", out)
	}
	for _, want := range []string{"status", "adopt", "upgrade"} {
		if !strings.Contains(out, want) {
			t.Errorf("--help lacks %q", want)
		}
	}
}
