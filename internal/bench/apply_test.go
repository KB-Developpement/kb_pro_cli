package bench

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KB-Developpement/kb_pro_cli/internal/apps"
	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
	"github.com/KB-Developpement/kb_pro_cli/internal/testutil"
)

const testCommit = "0123456789abcdef0123456789abcdef01234567"

var testApp = apps.All[0] // kb_pro: a registered app, so a replay can resolve it

type fixture struct {
	t    *testing.T
	root string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"apps", "sites"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "sites", "apps.txt"), []byte("frappe\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KB_BENCH_ROOT", root)
	t.Setenv("TMPDIR", t.TempDir())
	return &fixture{t: t, root: root}
}

func (f *fixture) appDir(dir string) string { return filepath.Join(f.root, "apps", dir) }

func (f *fixture) writeApp(dir string, files map[string]string) {
	f.t.Helper()
	for name, body := range files {
		p := filepath.Join(f.appDir(dir), name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			f.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *fixture) download(files map[string]string) *license.Download {
	f.t.Helper()
	var fl []testutil.File
	for n, b := range files {
		fl = append(fl, testutil.File{Name: n, Body: b})
	}
	path, data := testutil.TarGz(f.t, "kb_pro-abc", fl)
	return &license.Download{
		Path:       path,
		Repository: "KB-Developpement/kb_pro",
		Ref:        "v1.2.0",
		Commit:     testCommit,
		SHA256:     testutil.Sha256Hex(data),
		Requested:  "major=1",
	}
}

func (f *fixture) seedReceipt(dir string) []byte {
	f.t.Helper()
	r := &kbstate.Receipt{
		SchemaVersion: 1, App: "kb_pro", Directory: dir, Package: "kb_pro",
		Repository: "KB-Developpement/kb_pro", Line: "1", Requested: "major=1", Ref: "v1.0.0",
		Commit: strings.Repeat("c", 40), ArchiveSHA256: strings.Repeat("d", 64),
		InstalledAt: "2026-01-01T00:00:00Z", State: kbstate.StateArchive, Provenance: kbstate.ProvInstalled,
	}
	if err := kbstate.WriteReceipt(f.root, r); err != nil {
		f.t.Fatal(err)
	}
	return testutil.MustRead(f.t, kbstate.ReceiptPath(f.root, dir))
}

func (f *fixture) journal() *kbstate.Journal {
	f.t.Helper()
	js := kbstate.ReadJournal(f.root)
	if js.Journal == nil {
		f.t.Fatalf("no journal: %+v", js)
	}
	return js.Journal
}

type calls struct{ names []string }

func okSteps(c *calls) Steps {
	rec := func(n string) func() (string, error) {
		return func() (string, error) { c.names = append(c.names, n); return n + " ok", nil }
	}
	return Steps{
		AppendAppsTxt: func() (bool, error) {
			c.names = append(c.names, "apps.txt")
			return appendAppToAppsTxt(benchDir(), "kb_pro")
		},
		RemoveAppsTxt: func() error {
			c.names = append(c.names, "rm-apps.txt")
			return removeAppFromAppsTxt(benchDir(), "kb_pro")
		},
		SetupRequirements: rec("requirements"),
		PipInstall:        rec("pip"),
		Build:             rec("build"),
		SyncAppState:      func() error { c.names = append(c.names, "apps.json"); return SyncAppState("kb_pro", "kb_pro") },
	}
}

func upgradeSteps(c *calls) Steps {
	s := okSteps(c)
	s.AppendAppsTxt, s.RemoveAppsTxt = nil, nil
	s.Migrate = func() (string, error) { c.names = append(c.names, "migrate"); return "migrate ok", nil }
	return s
}

func read(t *testing.T, path string) string {
	t.Helper()
	return string(testutil.MustRead(t, path))
}

func TestApplyUpgradeWithValidReceiptDeletesPreviousTree(t *testing.T) {
	f := newFixture(t)
	f.writeApp("kb_pro", map[string]string{"marker.txt": "OLD"})
	f.seedReceipt("kb_pro")
	c := &calls{}

	res, err := applyWithSteps(f.root, ApplyPlan{Command: CmdUpgrade, App: testApp, Download: f.download(map[string]string{"marker.txt": "NEW"})}, upgradeSteps(c))
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(f.appDir("kb_pro"), "marker.txt")); got != "NEW" {
		t.Errorf("marker = %q", got)
	}
	if exists(f.appDir("kb_pro.kb-old")) || exists(f.appDir("kb_pro.kb-new")) || res.Retained != "" {
		t.Errorf("previous tree not deleted: old=%v retained=%q", exists(f.appDir("kb_pro.kb-old")), res.Retained)
	}
	if got := kbstate.ListRetained(f.root); len(got) != 0 {
		t.Errorf("retained copies = %v, want none", got)
	}
	if strings.Join(c.names, ",") != "requirements,pip,build,migrate,apps.json" {
		t.Errorf("steps = %v", c.names)
	}
	rs := kbstate.ReadReceipt(f.root, "kb_pro")
	if !rs.Valid() || rs.Receipt.Commit != testCommit || rs.Receipt.Ref != "v1.2.0" || rs.Receipt.Provenance != kbstate.ProvInstalled || rs.Receipt.Requested != "major=1" {
		t.Errorf("receipt = %+v (%s)", rs.Receipt, rs.Reason)
	}
	if j := f.journal(); j.Pending() || j.Step != kbstate.StepFinalized || j.Aborted {
		t.Errorf("journal = %+v", j)
	}
}

func TestApplyUpgradeWithoutReceiptKeepsPreviousTree(t *testing.T) {
	for _, name := range []string{"no receipt", "tampered receipt"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.writeApp("kb_pro", map[string]string{"marker.txt": "HAND EDITED", "sub/x.py": "x"})
			if name == "tampered receipt" {
				_ = os.MkdirAll(kbstate.ReceiptsDir(f.root), 0o700)
				_ = os.WriteFile(kbstate.ReceiptPath(f.root, "kb_pro"), []byte(`{"schema_version":1,"commit":"zz"}`), 0o600)
			}
			before := testutil.TreeDigest(t, f.appDir("kb_pro"))

			res, err := applyWithSteps(f.root, ApplyPlan{Command: CmdUpgrade, App: testApp, Download: f.download(map[string]string{"marker.txt": "NEW"})}, upgradeSteps(&calls{}))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(res.Retained, filepath.Join(".kb", "recovery", "kb_pro-pre-receipt-")) {
				t.Fatalf("Retained = %q", res.Retained)
			}
			if got := testutil.TreeDigest(t, res.Retained); got != before {
				t.Errorf("retained copy digest %s != pre-upgrade digest %s", got, before)
			}
			if exists(f.appDir("kb_pro.kb-old")) {
				t.Error(".kb-old still exists")
			}
			if rs := kbstate.ReadReceipt(f.root, "kb_pro"); !rs.Valid() {
				t.Errorf("first receipt not written: %s", rs.Reason)
			}
			if got := kbstate.ListRetained(f.root); len(got) != 1 || got[0].Kind != "pre-receipt" {
				t.Errorf("ListRetained = %+v", got)
			}
		})
	}
}

func TestApplyStockConversionKeepsNoCopy(t *testing.T) {
	f := newFixture(t)
	f.writeApp("kb_pro", map[string]string{"marker.txt": "OLD", ".git/HEAD": "ref"})
	res, err := applyWithSteps(f.root, ApplyPlan{Command: CmdInitFrappe, App: testApp, Download: f.download(map[string]string{"marker.txt": "NEW"}), StockConversion: true}, upgradeSteps(&calls{}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Retained != "" || !res.StockDiscarded || len(kbstate.ListRetained(f.root)) != 0 || exists(f.appDir("kb_pro.kb-old")) {
		t.Errorf("a copy of the stock tree was kept: %+v", res)
	}
	if j := f.journal(); j.Step != kbstate.StepFinalized {
		t.Errorf("journal step %s", j.Step)
	}
}

func TestApplyGitGuardRefusesBeforeAnyChange(t *testing.T) {
	for _, kind := range []string{"dir", "file"} {
		for _, cmd := range []string{CmdAdd, CmdInstall, CmdUpgrade, CmdInitFrappe} {
			t.Run(kind+"/"+cmd, func(t *testing.T) {
				f := newFixture(t)
				f.writeApp("kb_pro", map[string]string{"marker.txt": "OLD"})
				if kind == "dir" {
					f.writeApp("kb_pro", map[string]string{".git/HEAD": "ref: refs/heads/main"})
				} else {
					f.writeApp("kb_pro", map[string]string{".git": "gitdir: /elsewhere/.git/worktrees/x"})
				}
				before := testutil.BenchDigest(t, f.root)
				_, err := applyWithSteps(f.root, ApplyPlan{Command: cmd, App: testApp, Download: f.download(map[string]string{"marker.txt": "NEW"})}, upgradeSteps(&calls{}))
				var gr *GitRefusal
				if !errors.As(err, &gr) {
					t.Fatalf("err = %v, want *GitRefusal", err)
				}
				if testutil.BenchDigest(t, f.root) != before {
					t.Error("the bench changed")
				}
				if exists(kbstate.Dir(f.root)) {
					t.Error(".kb was created before the guard refused")
				}
			})
		}
	}
}

func TestApplyLeftoversMovedNotDeleted(t *testing.T) {
	f := newFixture(t)
	f.writeApp("kb_pro", map[string]string{"marker.txt": "OLD"})
	f.writeApp("kb_pro.kb-old", map[string]string{"recovery.txt": "kept after a failed migration"})
	f.writeApp("kb_pro.kb-new", map[string]string{"half.txt": "half extracted"})
	oldDigest := testutil.TreeDigest(t, f.appDir("kb_pro.kb-old"))
	newDigest := testutil.TreeDigest(t, f.appDir("kb_pro.kb-new"))
	f.seedReceipt("kb_pro")

	res, err := applyWithSteps(f.root, ApplyPlan{Command: CmdUpgrade, App: testApp, Download: f.download(map[string]string{"marker.txt": "NEW"})}, upgradeSteps(&calls{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Legacy) != 2 {
		t.Fatalf("Legacy = %v", res.Legacy)
	}
	j := f.journal()
	if len(j.Legacy) != 2 {
		t.Fatalf("journal legacy = %+v", j.Legacy)
	}
	got := map[string]string{}
	for _, m := range j.Legacy {
		got[filepath.Base(m.Original)] = m.Destination
		if !strings.HasPrefix(m.Destination, kbstate.LegacyDir(f.root)+string(filepath.Separator)) {
			t.Errorf("destination %s is not under recovery/legacy", m.Destination)
		}
	}
	if d := testutil.TreeDigest(t, got["kb_pro.kb-old"]); d != oldDigest {
		t.Errorf(".kb-old digest changed by the move")
	}
	if d := testutil.TreeDigest(t, got["kb_pro.kb-new"]); d != newDigest {
		t.Errorf(".kb-new digest changed by the move")
	}
	if !strings.Contains(filepath.Base(got["kb_pro.kb-old"]), "kb_pro-kb-old-") || !strings.Contains(filepath.Base(got["kb_pro.kb-new"]), "kb_pro-kb-new-") {
		t.Errorf("destination names: %v", got)
	}
	if exists(f.appDir("kb_pro.kb-old")) || exists(f.appDir("kb_pro.kb-new")) {
		t.Error("a leftover is still beside the app")
	}
	if n := len(kbstate.ListRetained(f.root)); n != 2 {
		t.Errorf("status would list %d retained copies, want 2", n)
	}
}

func TestApplyRollbackRestoresPreviousVersion(t *testing.T) {
	boom := errors.New("boom")
	for _, tc := range []struct {
		name      string
		fail      func(s *Steps)
		wantPip   int
		wantStage string
	}{
		{"setup requirements", func(s *Steps) { s.SetupRequirements = func() (string, error) { return "", boom } }, 1, "setup requirements"},
		{"pip", func(s *Steps) { s.PipInstall = func() (string, error) { return "", boom } }, 2, "pip install -e"},
		{"build", func(s *Steps) { s.Build = func() (string, error) { return "", boom } }, 2, "build assets"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.writeApp("kb_pro", map[string]string{"marker.txt": "OLD"})
			f.seedReceipt("kb_pro")
			before := testutil.TreeDigest(t, f.appDir("kb_pro"))
			pipCalls := 0
			s := upgradeSteps(&calls{})
			inner := s.PipInstall
			s.PipInstall = func() (string, error) { pipCalls++; return inner() }
			tc.fail(&s)
			if tc.name == "pip" {
				s.PipInstall = func() (string, error) {
					pipCalls++
					if pipCalls == 1 {
						return "", boom
					}
					return "ok", nil
				}
			}

			_, err := applyWithSteps(f.root, ApplyPlan{Command: CmdUpgrade, App: testApp, Download: f.download(map[string]string{"marker.txt": "NEW"})}, s)
			if err == nil || !strings.Contains(err.Error(), "the previous version was restored") || !strings.Contains(err.Error(), tc.wantStage) {
				t.Fatalf("err = %v", err)
			}
			if errors.Is(err, ErrJournalPending) {
				t.Error("a clean rollback must not leave the journal pending")
			}
			if got := testutil.TreeDigest(t, f.appDir("kb_pro")); got != before {
				t.Error("previous tree not restored byte for byte")
			}
			if exists(f.appDir("kb_pro.kb-old")) || exists(f.appDir("kb_pro.kb-new")) {
				t.Error("old/new directory left behind")
			}
			if pipCalls != tc.wantPip {
				t.Errorf("pip calls = %d, want %d", pipCalls, tc.wantPip)
			}
			if j := f.journal(); j.Pending() || !j.Aborted {
				t.Errorf("journal = %+v, want aborted and not pending", j)
			}
			if rs := kbstate.ReadReceipt(f.root, "kb_pro"); !rs.Valid() || rs.Receipt.Ref != "v1.0.0" {
				t.Error("old receipt was not retained")
			}
		})
	}
}

func TestApplyMigrateFailureLeavesJournalAtSwapped(t *testing.T) {
	f := newFixture(t)
	f.writeApp("kb_pro", map[string]string{"marker.txt": "OLD"})
	f.seedReceipt("kb_pro")
	s := upgradeSteps(&calls{})
	s.Migrate = func() (string, error) { return "", errors.New("migrate exploded") }

	_, err := applyWithSteps(f.root, ApplyPlan{Command: CmdUpgrade, App: testApp, Download: f.download(map[string]string{"marker.txt": "NEW"})}, s)
	if !errors.Is(err, ErrJournalPending) {
		t.Fatalf("err = %v, want a pending-journal error", err)
	}
	if !strings.Contains(err.Error(), f.appDir("kb_pro.kb-old")) || !strings.Contains(err.Error(), "NOT rolled back") {
		t.Errorf("message lacks the backup path or the no-rollback statement: %v", err)
	}
	if got := read(t, filepath.Join(f.appDir("kb_pro"), "marker.txt")); got != "NEW" {
		t.Errorf("live marker = %q", got)
	}
	if got := read(t, filepath.Join(f.appDir("kb_pro.kb-old"), "marker.txt")); got != "OLD" {
		t.Errorf("backup marker = %q", got)
	}
	if j := f.journal(); j.Step != kbstate.StepSwapped || !j.Pending() {
		t.Errorf("journal step = %s", j.Step)
	}
	// The next mutating command refuses and prints the old and new paths.
	_, rerr := RecoverJournal(context.Background(), RecoverMutate)
	if rerr == nil {
		t.Fatal("recovery did not refuse a swapped upgrade")
	}
	for _, want := range []string{kbstate.JournalPath(f.root), f.appDir("kb_pro.kb-old"), f.appDir("kb_pro")} {
		if !strings.Contains(rerr.Error(), want) {
			t.Errorf("refusal does not name %s:\n%v", want, rerr)
		}
	}
}

func TestApplyAddRollbackRemovesDirAndAppsTxtEntry(t *testing.T) {
	f := newFixture(t)
	s := okSteps(&calls{})
	s.Build = func() (string, error) { return "", errors.New("build failed") }
	_, err := applyWithSteps(f.root, ApplyPlan{Command: CmdAdd, App: testApp, Download: f.download(map[string]string{"marker.txt": "NEW"})}, s)
	if err == nil || errors.Is(err, ErrJournalPending) || !strings.Contains(err.Error(), "the new app directory was removed") {
		t.Fatalf("err = %v", err)
	}
	if exists(f.appDir("kb_pro")) {
		t.Error("app dir left behind")
	}
	if got := read(t, filepath.Join(f.root, "sites", "apps.txt")); got != "frappe\n" {
		t.Errorf("apps.txt = %q", got)
	}
	if j := f.journal(); j.Pending() {
		t.Error("journal pending after a clean rollback")
	}

	// An apps.txt entry that existed before the add is not removed by the rollback.
	f2 := newFixture(t)
	_ = os.WriteFile(filepath.Join(f2.root, "sites", "apps.txt"), []byte("frappe\nkb_pro\n"), 0o644)
	s = okSteps(&calls{})
	s.Build = func() (string, error) { return "", errors.New("build failed") }
	if _, err := applyWithSteps(f2.root, ApplyPlan{Command: CmdAdd, App: testApp, Download: f2.download(map[string]string{"m": "x"})}, s); err == nil {
		t.Fatal("want failure")
	}
	if got := read(t, filepath.Join(f2.root, "sites", "apps.txt")); got != "frappe\nkb_pro\n" {
		t.Errorf("a pre-existing apps.txt entry was removed: %q", got)
	}
}

func TestApplyAddRefusesExistingDirectory(t *testing.T) {
	f := newFixture(t)
	f.writeApp("kb_pro", map[string]string{"marker.txt": "OLD"})
	before := testutil.BenchDigest(t, f.root)
	_, err := applyWithSteps(f.root, ApplyPlan{Command: CmdAdd, App: testApp, Download: f.download(map[string]string{"marker.txt": "NEW"})}, okSteps(&calls{}))
	if err == nil || !strings.Contains(err.Error(), "already exists") || testutil.BenchDigest(t, f.root) != before {
		t.Fatalf("err = %v", err)
	}
}

// A truncated archive (here cut inside the gzip stream) fails extraction with an
// unexpected-EOF error; nothing under apps/ changes and no receipt is written.
func TestApplyTruncatedArchiveChangesNothing(t *testing.T) {
	f := newFixture(t)
	f.writeApp("kb_pro", map[string]string{"marker.txt": "OLD"})
	oldReceipt := f.seedReceipt("kb_pro")
	before := testutil.TreeDigest(t, f.appDir("kb_pro"))

	path, data := testutil.TarGz(t, "kb_pro-abc", []testutil.File{{Name: "big.bin", Body: testutil.IncompressibleBody(20000)}, {Name: "marker.txt", Body: "NEW"}})
	cut := data[:1024]
	if err := os.WriteFile(path, cut, 0o600); err != nil {
		t.Fatal(err)
	}
	dl := &license.Download{Path: path, Repository: "KB-Developpement/kb_pro", Ref: "v1.2.0", Commit: testCommit, SHA256: testutil.Sha256Hex(cut), Requested: "major=1"}

	_, err := applyWithSteps(f.root, ApplyPlan{Command: CmdUpgrade, App: testApp, Download: dl}, upgradeSteps(&calls{}))
	if err == nil || !strings.Contains(err.Error(), "unexpected EOF") {
		t.Fatalf("err = %v, want an unexpected EOF extraction error", err)
	}
	if testutil.TreeDigest(t, f.appDir("kb_pro")) != before || exists(f.appDir("kb_pro.kb-new")) {
		t.Error("the app directory changed or staging was left behind")
	}
	if got := testutil.MustRead(t, kbstate.ReceiptPath(f.root, "kb_pro")); string(got) != string(oldReceipt) {
		t.Error("the old receipt changed")
	}
	if j := f.journal(); j.Pending() {
		t.Error("journal pending")
	}
}

// A tar stream that ends cleanly but whose gzip trailer is gone is not complete.
func TestExtractVerifiesGzipTrailer(t *testing.T) {
	path, data := testutil.TarGz(t, "app", []testutil.File{{Name: "a.txt", Body: "hello"}})
	if err := os.WriteFile(path, data[:len(data)-4], 0o600); err != nil { // drop the ISIZE field
		t.Fatal(err)
	}
	err := ExtractArchive(path, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "unexpected EOF") {
		t.Fatalf("err = %v, want unexpected EOF", err)
	}
	// and a corrupt CRC is caught too
	path2, data2 := testutil.TarGz(t, "app", []testutil.File{{Name: "a.txt", Body: "hello"}})
	data2[len(data2)-8] ^= 0xff
	_ = os.WriteFile(path2, data2, 0o600)
	if err := ExtractArchive(path2, t.TempDir()); err == nil {
		t.Fatal("corrupt gzip trailer accepted")
	}
	// a good archive still extracts
	path3, _ := testutil.TarGz(t, "app", []testutil.File{{Name: "a.txt", Body: "hello"}})
	dest := t.TempDir()
	if err := ExtractArchive(path3, dest); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dest, "a.txt")); got != "hello" {
		t.Errorf("a.txt = %q", got)
	}
}

func TestApplyChangedArchiveRefused(t *testing.T) {
	f := newFixture(t)
	f.writeApp("kb_pro", map[string]string{"marker.txt": "OLD"})
	dl := f.download(map[string]string{"marker.txt": "NEW"})
	dl.SHA256 = strings.Repeat("0", 64)
	before := testutil.TreeDigest(t, f.appDir("kb_pro"))
	if _, err := applyWithSteps(f.root, ApplyPlan{Command: CmdUpgrade, App: testApp, Download: dl}, upgradeSteps(&calls{})); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("err = %v", err)
	}
	if testutil.TreeDigest(t, f.appDir("kb_pro")) != before {
		t.Error("app changed")
	}
}

func TestApplyRefusesReceiptWithNewerSchema(t *testing.T) {
	f := newFixture(t)
	f.writeApp("kb_pro", map[string]string{"marker.txt": "OLD"})
	_ = os.MkdirAll(kbstate.ReceiptsDir(f.root), 0o700)
	raw := []byte(`{"schema_version": 2}`)
	_ = os.WriteFile(kbstate.ReceiptPath(f.root, "kb_pro"), raw, 0o600)
	before := testutil.TreeDigest(t, f.appDir("kb_pro"))
	_, err := applyWithSteps(f.root, ApplyPlan{Command: CmdUpgrade, App: testApp, Download: f.download(map[string]string{"marker.txt": "NEW"})}, upgradeSteps(&calls{}))
	var se *kbstate.SchemaError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v", err)
	}
	if testutil.TreeDigest(t, f.appDir("kb_pro")) != before || string(testutil.MustRead(t, kbstate.ReceiptPath(f.root, "kb_pro"))) != string(raw) {
		t.Error("state changed")
	}
}

// ── finalize failure and replay ─────────────────────────────────────────────

// fakeBenchOnPath puts a `bench` that always succeeds on PATH and a no-op
// env/bin/python in the fixture, so a replay (which uses the real bench steps)
// can run.
func (f *fixture) fakeBenchOnPath() {
	f.t.Helper()
	bin := f.t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "bench"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		f.t.Fatal(err)
	}
	py := filepath.Join(f.root, "env", "bin")
	_ = os.MkdirAll(py, 0o755)
	if err := os.WriteFile(filepath.Join(py, "python"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		f.t.Fatal(err)
	}
	f.t.Setenv("PATH", bin)
}

func TestFinalizeFailureStaysPendingAndReplays(t *testing.T) {
	for _, cmd := range []string{CmdAdd, CmdInstall, CmdInitFrappe, CmdUpgrade} {
		t.Run(cmd, func(t *testing.T) {
			f := newFixture(t)
			app := testApp
			if cmd == CmdInitFrappe {
				app = apps.Framework
			}
			dir := app.Dir()
			if cmd == CmdUpgrade || cmd == CmdInitFrappe {
				f.writeApp(dir, map[string]string{"marker.txt": "OLD"})
				f.seedReceipt(dir)
			}
			steps := okSteps(&calls{})
			if cmd == CmdUpgrade || cmd == CmdInitFrappe {
				steps = upgradeSteps(&calls{})
			}
			dl := f.download(map[string]string{"marker.txt": "NEW", app.PackageName() + "/hooks.py": "app_version = '1.2.0'"})
			dl.Repository = "KB-Developpement/" + app.Repository()

			FinalizeHook = func() error { return errors.New("stub: finalize fails") }
			defer func() { FinalizeHook = nil }()

			_, err := applyWithSteps(f.root, ApplyPlan{Command: cmd, App: app, Download: dl}, steps)
			if !errors.Is(err, ErrJournalPending) {
				t.Fatalf("err = %v, want pending", err)
			}
			wantStep := kbstate.StepSwapped
			if cmd == CmdUpgrade {
				wantStep = kbstate.StepMigrated
			}
			if j := f.journal(); j.Step != wantStep {
				t.Fatalf("journal step = %s, want %s", j.Step, wantStep)
			}
			if cmd == CmdAdd || cmd == CmdInstall {
				if rs := kbstate.ReadReceipt(f.root, dir); rs.Valid() {
					t.Error("receipt written although finalization failed")
				}
			}

			f.fakeBenchOnPath()
			// The replay stub fails too: the mutation refuses and names the journal.
			_, rerr := RecoverJournal(context.Background(), RecoverMutate)
			if rerr == nil || !strings.Contains(rerr.Error(), kbstate.JournalPath(f.root)) {
				t.Fatalf("failing replay: %v", rerr)
			}
			if !f.journal().Pending() {
				t.Fatal("a failed replay must leave the journal pending")
			}

			// With the stub healthy, the next mutating command replays first.
			FinalizeHook = nil
			notes, rerr := RecoverJournal(context.Background(), RecoverMutate)
			if rerr != nil || len(notes) != 1 {
				t.Fatalf("replay: notes=%v err=%v", notes, rerr)
			}
			rs := kbstate.ReadReceipt(f.root, dir)
			if !rs.Valid() || rs.Receipt.Commit != testCommit {
				t.Errorf("receipt after replay: %+v %s", rs.Receipt, rs.Reason)
			}
			if j := f.journal(); j.Pending() || j.Step != kbstate.StepFinalized {
				t.Errorf("journal after replay: %+v", j)
			}
			if exists(f.appDir(dir + ".kb-old")) {
				t.Error("old tree not retired by the replay")
			}
			if got := read(t, filepath.Join(f.root, "sites", "apps.json")); !strings.Contains(got, `"`+app.PackageName()+`"`) {
				t.Errorf("apps.json has no entry after replay: %s", got)
			}
		})
	}
}

// ── recovery by step ────────────────────────────────────────────────────────

func writeJournal(t *testing.T, root string, mutate func(j *kbstate.Journal)) {
	t.Helper()
	j := kbstate.NewJournal(root)
	j.Op, j.Command, j.App, j.Directory = kbstate.OpRegister, CmdAdd, "kb_pro", "kb_pro"
	mutate(j)
	if err := j.Save(); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverDiscardsOnlyRecordedPaths(t *testing.T) {
	for _, step := range []string{kbstate.StepPlanned, kbstate.StepDownloaded, kbstate.StepStaged} {
		t.Run(step, func(t *testing.T) {
			f := newFixture(t)
			f.writeApp("kb_pro.kb-new", map[string]string{"half.txt": "recorded staging"})
			f.writeApp("kb_pro.kb-new-2", map[string]string{"decoy.txt": "similar name, not recorded"})
			f.writeApp("kb_pro", map[string]string{"live.txt": "live app"})
			_ = os.MkdirAll(filepath.Join(kbstate.Dir(f.root), "tmp-decoy"), 0o700)
			archive := filepath.Join(t.TempDir(), "kb-app-1.tar.gz")
			_ = os.WriteFile(archive, []byte("x"), 0o600)
			decoy := testutil.TreeDigest(t, f.appDir("kb_pro.kb-new-2"))
			live := testutil.TreeDigest(t, f.appDir("kb_pro"))
			writeJournal(t, f.root, func(j *kbstate.Journal) {
				j.Step = step
				j.Paths = []string{archive, f.appDir("kb_pro.kb-new")}
			})

			notes, err := RecoverJournal(context.Background(), RecoverMutate)
			if err != nil {
				t.Fatal(err)
			}
			if len(notes) != 2 {
				t.Errorf("notes = %v", notes)
			}
			if exists(archive) || exists(f.appDir("kb_pro.kb-new")) {
				t.Error("recorded paths were not discarded")
			}
			if testutil.TreeDigest(t, f.appDir("kb_pro.kb-new-2")) != decoy || testutil.TreeDigest(t, f.appDir("kb_pro")) != live || !exists(filepath.Join(kbstate.Dir(f.root), "tmp-decoy")) {
				t.Error("a path the journal did not record was touched")
			}
			if f.journal().Pending() {
				t.Error("journal still pending")
			}
			// idempotent: a second recovery does nothing
			if n, err := RecoverJournal(context.Background(), RecoverMutate); err != nil || len(n) != 0 {
				t.Errorf("second recovery: %v %v", n, err)
			}
		})
	}
}

func TestRecoverAdoptModes(t *testing.T) {
	f := newFixture(t)
	scratch := filepath.Join(t.TempDir(), "kb-adopt-x")
	_ = os.MkdirAll(scratch, 0o700)
	writeJournal(t, f.root, func(j *kbstate.Journal) {
		j.Op, j.Command, j.Step = kbstate.OpAdopt, "adopt", kbstate.StepDownloaded
		j.Paths = []string{scratch}
	})
	// --check never deletes
	if _, err := RecoverJournal(context.Background(), RecoverAdoptCheck); err != nil || !exists(scratch) {
		t.Fatalf("check mode: err=%v exists=%v", err, exists(scratch))
	}
	if _, err := RecoverJournal(context.Background(), RecoverAdopt); err != nil || exists(scratch) {
		t.Fatalf("adopt mode: err=%v exists=%v", err, exists(scratch))
	}
	// swapped / migrated refuse in adopt and adopt --check
	for _, step := range []string{kbstate.StepSwapped, kbstate.StepMigrated} {
		writeJournal(t, f.root, func(j *kbstate.Journal) { j.Step = step; j.OldPath = "/old"; j.NewPath = "/new" })
		for _, mode := range []RecoverMode{RecoverAdopt, RecoverAdoptCheck} {
			_, err := RecoverJournal(context.Background(), mode)
			if err == nil || !strings.Contains(err.Error(), kbstate.JournalPath(f.root)) {
				t.Errorf("step %s mode %d: err = %v", step, mode, err)
			}
		}
	}
}

func TestRecoverRefusals(t *testing.T) {
	t.Run("unsafe recorded path", func(t *testing.T) {
		f := newFixture(t)
		f.writeApp("kb_pro", map[string]string{"live.txt": "x"})
		for _, p := range []string{f.root, filepath.Dir(f.root), "relative/path", filepath.Join(f.root, "apps"), kbstate.Dir(f.root), filepath.Join(kbstate.RecoveryDir(f.root), "legacy", "x"), ""} {
			writeJournal(t, f.root, func(j *kbstate.Journal) { j.Step = kbstate.StepPlanned; j.Paths = []string{p} })
			if _, err := RecoverJournal(context.Background(), RecoverMutate); err == nil {
				t.Errorf("recorded path %q was accepted", p)
			}
		}
		if !exists(f.appDir("kb_pro")) {
			t.Error("a protected path was deleted")
		}
	})
	t.Run("malformed and newer", func(t *testing.T) {
		f := newFixture(t)
		_ = os.MkdirAll(kbstate.Dir(f.root), 0o700)
		_ = os.WriteFile(kbstate.JournalPath(f.root), []byte("{not json"), 0o600)
		if _, err := RecoverJournal(context.Background(), RecoverMutate); err == nil || !strings.Contains(err.Error(), "journal.json") {
			t.Errorf("malformed: %v", err)
		}
		_ = os.WriteFile(kbstate.JournalPath(f.root), []byte(`{"schema_version":2,"step":"planned"}`), 0o600)
		var se *kbstate.SchemaError
		if _, err := RecoverJournal(context.Background(), RecoverMutate); !errors.As(err, &se) {
			t.Errorf("newer: %v", err)
		}
	})
	t.Run("swapped add with a different live tree", func(t *testing.T) {
		f := newFixture(t)
		f.writeApp("kb_pro", map[string]string{"a": "1", "b": "2"})
		writeJournal(t, f.root, func(j *kbstate.Journal) {
			j.Step, j.StagedFiles, j.NewPath = kbstate.StepSwapped, 99, f.appDir("kb_pro")
		})
		_, err := RecoverJournal(context.Background(), RecoverMutate)
		if err == nil || !strings.Contains(err.Error(), "99") || !strings.Contains(err.Error(), kbstate.JournalPath(f.root)) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("swapped add with the new source missing", func(t *testing.T) {
		f := newFixture(t)
		writeJournal(t, f.root, func(j *kbstate.Journal) { j.Step, j.NewPath = kbstate.StepSwapped, f.appDir("kb_pro") })
		if _, err := RecoverJournal(context.Background(), RecoverMutate); err == nil || !strings.Contains(err.Error(), "missing") {
			t.Fatalf("err = %v", err)
		}
	})
}

// KP-SWAP-1 state: killed between moving the live directory aside and moving the
// new tree in. The next mutation refuses, naming the journal and the backup, and
// moving the backup back restores the pre-swap digest.
func TestRecoverKilledBetweenRenames(t *testing.T) {
	f := newFixture(t)
	f.writeApp("kb_pro.kb-old", map[string]string{"marker.txt": "OLD", "sub/x.py": "x"})
	f.writeApp("kb_pro.kb-new", map[string]string{"marker.txt": "NEW"})
	pre := testutil.TreeDigest(t, f.appDir("kb_pro.kb-old"))
	writeJournal(t, f.root, func(j *kbstate.Journal) {
		j.Op, j.Command, j.Step = kbstate.OpSwap, CmdUpgrade, kbstate.StepSwapped
		j.OldPath, j.NewPath = f.appDir("kb_pro.kb-old"), f.appDir("kb_pro")
		j.Paths = []string{f.appDir("kb_pro.kb-new")}
	})
	_, err := RecoverJournal(context.Background(), RecoverMutate)
	if err == nil {
		t.Fatal("no refusal")
	}
	for _, want := range []string{kbstate.JournalPath(f.root), f.appDir("kb_pro.kb-old")} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not name %s: %v", want, err)
		}
	}
	if exists(f.appDir("kb_pro")) {
		t.Error("apps/kb_pro should not exist at this kill point")
	}
	if err := os.Rename(f.appDir("kb_pro.kb-old"), f.appDir("kb_pro")); err != nil {
		t.Fatal(err)
	}
	if testutil.TreeDigest(t, f.appDir("kb_pro")) != pre {
		t.Error("restoring the backup did not restore the pre-swap digest")
	}
}
