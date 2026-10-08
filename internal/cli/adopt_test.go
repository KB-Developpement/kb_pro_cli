package cli

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/KB-Developpement/kb_pro_cli/internal/apps"
	"github.com/KB-Developpement/kb_pro_cli/internal/bench"
	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
	"github.com/KB-Developpement/kb_pro_cli/internal/testutil"
)

// legacyBench builds an archive bench the way pre-Phase-0 kb left it: no receipts.
func legacyBench(t *testing.T) *benchH {
	t.Helper()
	h := newBenchH(t)
	must(t, os.RemoveAll(kbstate.Dir(h.root)))
	h.seedFrappe("v1.2.0", false)
	h.seed("kb_pro", "v1.2.0", false)
	h.seed("kb_compta", "v1.2.0", false)
	return h
}

func hasKB(h *benchH) bool {
	_, err := os.Stat(kbstate.Dir(h.root))
	return err == nil
}

func TestAdoptUntouchedBench(t *testing.T) {
	h := legacyBench(t)
	before := h.benchDigest()
	benchCallsBefore := len(h.benchLogLines())

	out, err := h.exec("adopt")
	if err != nil {
		t.Fatalf("adopt: %v\n%s", err, out)
	}
	if h.benchDigest() != before {
		t.Error("adopt changed apps/ or sites/")
	}
	if got := len(h.benchLogLines()); got != benchCallsBefore {
		t.Errorf("adopt called bench (%d new calls); it must make no bench call and no database read", got-benchCallsBefore)
	}
	for _, c := range []struct{ name, dir string }{{"kb_pro", "kb_pro"}, {"kb_compta", "kb_compta"}, {"kb_frappe", "frappe"}} {
		rs := h.receipt(c.dir)
		if !rs.Valid() {
			t.Fatalf("no receipt for %s: %s\n%s", c.dir, rs.Reason, out)
		}
		r := rs.Receipt
		if r.Provenance != "adopted" || r.App != c.name || r.Commit != commitFor(c.name, "v1.2.0") || r.ReleaseTag == nil || *r.ReleaseTag != "v1.2.0" ||
			r.Requested != "v1.2.0" || r.State != "archive" || r.Line != "1" || r.SchemaVersion != 1 {
			t.Errorf("%s receipt = %+v", c.dir, r)
		}
		if r.ArchiveSHA256 != testutil.Sha256Hex(h.srv.archive(c.name, "v1.2.0")) {
			t.Errorf("%s archive_sha256 does not match the bytes served", c.dir)
		}
		if r.Adopt == nil || r.Adopt.ComparedFiles < 5 || len(r.Adopt.Exclusions) != 6 || len(r.Adopt.CandidatesTried) == 0 || r.Adopt.CandidatesTried[0] != "v1.2.0" {
			t.Errorf("%s adopt info = %+v", c.dir, r.Adopt)
		}
		if fi, _ := os.Stat(rs.Path); fi.Mode().Perm() != 0o600 {
			t.Errorf("receipt mode %v", fi.Mode().Perm())
		}
	}
	// kb upgrade still iterates the same apps.All: the framework is not in it.
	if len(apps.All) != 10 {
		t.Errorf("apps.All has %d rows", len(apps.All))
	}
	for _, a := range apps.All {
		if a.Name == "kb_frappe" {
			t.Error("kb_frappe was added to apps.All")
		}
	}
	if _, err := h.exec("upgrade", "--apps", "kb_frappe"); err == nil {
		t.Error("kb upgrade accepted kb_frappe")
	}
	// the journal of a completed adopt is finalized
	if j := h.journal(); j.Pending() {
		t.Errorf("journal pending: %s", j.Step)
	}
	// every candidate download was strict
	for _, d := range h.srv.downloads() {
		if d.Query.Get("release") != "1" || d.Query.Get("v") == "" || d.Query.Get("major") != "" {
			t.Errorf("adopt sent ?%s", queryKeys(d))
		}
	}
	// a second run skips what already has a receipt
	n := len(h.srv.downloads())
	out, err = h.exec("adopt")
	if err != nil || !strings.Contains(out, "already has a adopted receipt") || len(h.srv.downloads()) != n {
		t.Errorf("second run: err=%v downloads=%d->%d\n%s", err, n, len(h.srv.downloads()), out)
	}
}

func TestAdoptMismatchNamesPathsOnly(t *testing.T) {
	h := legacyBench(t)
	write(t, filepath.Join(h.appDir("kb_pro"), "README.md"), "SECRET-EDIT-CONTENT\n")
	write(t, filepath.Join(h.appDir("kb_pro"), "extra_file.py"), "SECRET-EXTRA-CONTENT\n")
	before := h.benchDigest()

	out, err := h.exec("adopt")
	if ExitCode(err) != 2 {
		t.Fatalf("exit code = %d, want 2 (err=%v)\n%s", ExitCode(err), err, out)
	}
	for _, want := range []string{"README.md", "extra_file.py", "kb_pro"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not name %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "SECRET") || strings.Contains(out, testutil.Sha256Hex([]byte("SECRET-EDIT-CONTENT\n"))) {
		t.Errorf("output leaks content or hashes:\n%s", out)
	}
	if h.receipt("kb_pro").Valid() {
		t.Error("a receipt was written for the mismatching app")
	}
	if h.benchDigest() != before {
		t.Error("bench changed")
	}
	// the matching apps are still adopted: the run is per app
	if !h.receipt("kb_compta").Valid() || !h.receipt("frappe").Valid() {
		t.Error("matching apps were not adopted")
	}
}

func TestAdoptWithTagWhenTheHintIsWrongOrMissing(t *testing.T) {
	saved := apps.All
	apps.All = append([]apps.App(nil), saved...)
	defer func() { apps.All = saved }()
	for i := range apps.All {
		if apps.All[i].Name == "kb_compta" {
			apps.All[i].Package = "kb_compta_pkg" // package directory differs from the app name
		}
	}

	// install writes the files of release v1.1.0, except that the package
	// declares the given version (or none), and mirrors it into the server stub
	// so the release really is what is on disk.
	build := func(t *testing.T, declared string) *benchH {
		h := newBenchH(t)
		must(t, os.RemoveAll(kbstate.Dir(h.root)))
		h.seedFrappe("v1.2.0", true) // already receipted: skipped
		files := releaseFiles(h.app("kb_compta"), "v1.1.0")
		for i := range files {
			switch files[i].Name {
			case "kb_compta_pkg/__init__.py":
				files[i].Body = "# no version declared\n"
				if declared != "" {
					files[i].Body = "__version__ = \"" + declared + "\"\n"
				}
			case "kb_compta_pkg/hooks.py":
				files[i].Body = "app_name = 'kb_compta_pkg'\n"
			}
		}
		h.srv.Files["kb_compta@v1.1.0"] = files
		for _, f := range files {
			p := filepath.Join(h.appDir("kb_compta"), f.Name)
			must(t, os.MkdirAll(filepath.Dir(p), 0o755))
			mode := os.FileMode(0o644)
			if f.Mode != 0 {
				mode = os.FileMode(f.Mode)
			}
			must(t, os.WriteFile(p, []byte(f.Body), mode))
		}
		apps := `{}`
		if declared != "" {
			apps = `{"kb_compta_pkg": {"version": "` + declared + `"}}`
		}
		write(t, filepath.Join(h.root, "sites", "apps.json"), apps)
		return h
	}

	for name, declared := range map[string]string{"hint is wrong": "9.9.9", "hint is missing": ""} {
		t.Run(name, func(t *testing.T) {
			h := build(t, declared)
			before := h.benchDigest()
			out, err := h.exec("adopt")
			if ExitCode(err) != 2 {
				t.Fatalf("without --tag: exit %d, err=%v\n%s", ExitCode(err), err, out)
			}
			if h.receipt("kb_compta").Valid() || h.benchDigest() != before {
				t.Fatal("something was written without a match")
			}
			if declared != "" && !strings.Contains(out, "v9.9.9 is missing") {
				t.Errorf("a 404 candidate is not reported as missing:\n%s", out)
			}
			if declared == "" && !strings.Contains(out, "--tag kb_compta=<tag>") {
				t.Errorf("no hint to use --tag:\n%s", out)
			}

			out, err = h.exec("adopt", "--tag", "kb_compta=v1.1.0")
			if err != nil {
				t.Fatalf("with --tag: %v\n%s", err, out)
			}
			rs := h.receipt("kb_compta")
			if !rs.Valid() || rs.Receipt.Provenance != "adopted" || rs.Receipt.Ref != "v1.1.0" || rs.Receipt.Package != "kb_compta_pkg" {
				t.Fatalf("receipt = %+v %s", rs.Receipt, rs.Reason)
			}
			tried := rs.Receipt.Adopt.CandidatesTried
			if declared != "" && (len(tried) != 2 || tried[0] != "v9.9.9" || tried[1] != "v1.1.0") {
				t.Errorf("candidates tried = %v (hint first, then --tag)", tried)
			}
			if h.benchDigest() != before {
				t.Error("bench changed")
			}
		})
	}
}

func TestAdoptFrameworkWritesAFrameworkReceipt(t *testing.T) {
	h := newBenchH(t)
	must(t, os.RemoveAll(kbstate.Dir(h.root)))
	h.seedFrappe("v1.2.0", false)
	if out, err := h.exec("adopt"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	rs := h.receipt("frappe")
	if !rs.Valid() || rs.Receipt.App != "kb_frappe" || rs.Receipt.Directory != "frappe" || rs.Receipt.Package != "frappe" || rs.Receipt.Line != "1" ||
		rs.Receipt.Repository != "KB-Developpement/kb_frappe" || rs.Receipt.Provenance != "adopted" {
		t.Fatalf("framework receipt = %+v %s", rs.Receipt, rs.Reason)
	}
}

func TestAdoptWithAnUnfinishedJournal(t *testing.T) {
	h := legacyBench(t)
	scratch := filepath.Join(os.Getenv("TMPDIR"), "kb-adopt-left-behind")
	write(t, filepath.Join(scratch, "x"), "x")
	other := filepath.Join(os.Getenv("TMPDIR"), "kb-adopt-left-behind-2")
	write(t, filepath.Join(other, "x"), "unrecorded")
	j := kbstate.NewJournal(h.root)
	j.Op, j.Command, j.App, j.Directory = kbstate.OpAdopt, "adopt", "kb_pro", "kb_pro"
	j.Paths = []string{scratch}
	must(t, j.Advance(kbstate.StepDownloaded))
	before := h.benchDigest()

	// --check never deletes
	if out, err := h.exec("adopt", "--check"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Stat(scratch); err != nil {
		t.Fatal("adopt --check deleted the recorded path")
	}

	if out, err := h.exec("adopt"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Stat(scratch); err == nil {
		t.Error("the recorded path was not deleted")
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("an unrecorded path was deleted")
	}
	if h.benchDigest() != before {
		t.Error("app digest changed")
	}
	if !h.receipt("kb_pro").Valid() {
		t.Error("adopt did not proceed")
	}
}

func TestAdoptRefusesAtSwappedAndMigrated(t *testing.T) {
	for _, step := range []string{kbstate.StepSwapped, kbstate.StepMigrated} {
		h := legacyBench(t)
		j := kbstate.NewJournal(h.root)
		j.Op, j.Command, j.App, j.Directory = kbstate.OpSwap, bench.CmdUpgrade, "kb_pro", "kb_pro"
		j.OldPath, j.NewPath = h.appDir("kb_pro.kb-old"), h.appDir("kb_pro")
		must(t, j.Advance(step))
		for _, args := range [][]string{{"adopt"}, {"adopt", "--check"}} {
			_, err := h.exec(args...)
			if ExitCode(err) != 4 || !strings.Contains(err.Error(), kbstate.JournalPath(h.root)) {
				t.Errorf("step %s, kb %v: exit %d err=%v", step, args, ExitCode(err), err)
			}
		}
	}
}

func TestAdoptRefusesGitCheckouts(t *testing.T) {
	h := legacyBench(t)
	must(t, os.MkdirAll(filepath.Join(h.appDir("kb_pro"), ".git"), 0o755))
	before := h.benchDigest()
	out, err := h.exec("adopt")
	if ExitCode(err) != 4 {
		t.Fatalf("exit %d, err=%v\n%s", ExitCode(err), err, out)
	}
	if !strings.Contains(out, "kb_pro: REFUSED") || !strings.Contains(out, ".git") {
		t.Errorf("output does not name the app:\n%s", out)
	}
	if h.receipt("kb_pro").Valid() {
		t.Error("receipt written for a Git checkout")
	}
	if h.benchDigest() != before {
		t.Error("bench changed")
	}
}

// adopt --check creates nothing under the bench and does not take the lock.
func TestAdoptCheckCreatesNothing(t *testing.T) {
	t.Run("no .kb before, matching bench", func(t *testing.T) {
		h := legacyBench(t)
		before := h.benchDigest()
		out, err := h.exec("adopt", "--check")
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if hasKB(h) {
			t.Error(".kb was created by --check")
		}
		if !strings.Contains(out, "would adopt") {
			t.Errorf("output:\n%s", out)
		}
		if h.benchDigest() != before {
			t.Error("bench changed")
		}
	})
	t.Run("no .kb before, mismatching bench", func(t *testing.T) {
		h := legacyBench(t)
		write(t, filepath.Join(h.appDir("kb_pro"), "extra.py"), "x")
		_, err := h.exec("adopt", "--check")
		if ExitCode(err) != 2 {
			t.Fatalf("exit %d: %v", ExitCode(err), err)
		}
		if hasKB(h) {
			t.Error(".kb was created by --check")
		}
	})
	t.Run(".kb exists, lock held by someone else", func(t *testing.T) {
		h := legacyBench(t)
		pid := h.holdLock()
		kbBefore := testutil.TreeDigestExcluding(t, kbstate.Dir(h.root), nil)
		if out, err := h.exec("adopt", "--check"); err != nil {
			t.Fatalf("a held lock blocked --check (PID %d): %v\n%s", pid, err, out)
		}
		if got := testutil.TreeDigestExcluding(t, kbstate.Dir(h.root), nil); got != kbBefore {
			t.Error(".kb changed under --check")
		}
		if _, err := os.Stat(kbstate.LockPath(h.root)); err != nil {
			t.Error(".kb/lock disappeared")
		}
		for _, r := range []string{"kb_pro", "kb_compta", "frappe"} {
			if h.receipt(r).Valid() {
				t.Errorf("--check wrote a receipt for %s", r)
			}
		}
	})
	t.Run("temp scratch is removed", func(t *testing.T) {
		h := legacyBench(t)
		if _, err := h.exec("adopt", "--check"); err != nil {
			t.Fatal(err)
		}
		m, _ := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "kb-*"))
		if len(m) != 0 {
			t.Errorf("scratch left behind: %v", m)
		}
	})
}

// A tree installed under umask 022 matches when adopted under umask 077, but a
// changed executable bit does not.
func TestAdoptPermissionRule(t *testing.T) {
	h := newBenchH(t)
	must(t, os.RemoveAll(kbstate.Dir(h.root)))
	h.seedFrappe("v1.2.0", true)
	a := h.app("kb_pro")

	old022 := syscall.Umask(0o022)
	archive := filepath.Join(t.TempDir(), "x.tar.gz")
	must(t, os.WriteFile(archive, h.srv.archive("kb_pro", "v1.2.0"), 0o600))
	must(t, os.MkdirAll(h.appDir(a.Dir()), 0o755))
	must(t, bench.ExtractArchive(archive, h.appDir(a.Dir())))
	write(t, filepath.Join(h.root, "sites", "apps.txt"), "frappe\nkb_pro\n")
	syscall.Umask(0o077)
	defer syscall.Umask(old022)

	out, err := h.exec("adopt")
	if err != nil {
		t.Fatalf("installed under umask 022, adopted under 077: %v\n%s", err, out)
	}
	if !h.receipt("kb_pro").Valid() {
		t.Fatal("no receipt")
	}

	// A different mode that keeps the exec bit state is fine; flipping it is not.
	must(t, os.Remove(h.receipt("kb_pro").Path))
	must(t, os.Chmod(filepath.Join(h.appDir("kb_pro"), "README.md"), 0o755))
	out, err = h.exec("adopt")
	if ExitCode(err) != 2 || !strings.Contains(out, "README.md") {
		t.Fatalf("exec-bit change: exit %d err=%v\n%s", ExitCode(err), err, out)
	}
	must(t, os.Chmod(filepath.Join(h.appDir("kb_pro"), "README.md"), 0o600))
	must(t, os.Chmod(filepath.Join(h.appDir("kb_pro"), "scripts", "run.sh"), 0o700)) // still executable
	if out, err = h.exec("adopt"); err != nil {
		t.Fatalf("other permission bits must not matter: %v\n%s", err, out)
	}
}

func TestAdoptIgnoresGeneratedFilesButNotReleaseContent(t *testing.T) {
	h := legacyBench(t)
	app := h.appDir("kb_pro")
	write(t, filepath.Join(app, "kb_pro.egg-info", "PKG-INFO"), "x")
	write(t, filepath.Join(app, "kb_pro", "__pycache__", "hooks.cpython-312.pyc"), "x")
	write(t, filepath.Join(app, "kb_pro", "stray.pyc"), "x")
	write(t, filepath.Join(app, "node_modules", "left-pad", "index.js"), "x")
	write(t, filepath.Join(app, "kb_pro", "public", "dist", "app.js"), "x")
	write(t, filepath.Join(app, ".DS_Store"), "x")
	if out, err := h.exec("adopt"); err != nil {
		t.Fatalf("generated files made the tree unadoptable: %v\n%s", err, out)
	}
	// editor leftovers and .env count
	h2 := legacyBench(t)
	write(t, filepath.Join(h2.appDir("kb_pro"), ".env"), "x")
	write(t, filepath.Join(h2.appDir("kb_pro"), "a.py.swp"), "x")
	out, err := h2.exec("adopt")
	if ExitCode(err) != 2 || !strings.Contains(out, ".env") || !strings.Contains(out, "a.py.swp") {
		t.Fatalf("exit %d\n%s", ExitCode(err), out)
	}
	// a release file that happens to match a pattern is still compared
	h3 := legacyBench(t)
	h3.srv.Files["kb_pro@v1.2.0"] = append(releaseFiles(h3.app("kb_pro"), "v1.2.0"), testutil.File{Name: "kb_pro/public/dist/shipped.js", Body: "release content"})
	// the installed tree lacks it -> mismatch (an exclusion never hides release content)
	out, err = h3.exec("adopt")
	if ExitCode(err) != 2 || !strings.Contains(out, "shipped.js") {
		t.Fatalf("exit %d\n%s", ExitCode(err), out)
	}
	// ...and a changed copy of it is a difference too
	write(t, filepath.Join(h3.appDir("kb_pro"), "kb_pro", "public", "dist", "shipped.js"), "edited")
	out, err = h3.exec("adopt")
	if ExitCode(err) != 2 || !strings.Contains(out, "shipped.js") {
		t.Fatalf("exit %d\n%s", ExitCode(err), out)
	}
}

func TestAdoptAmbiguousAndSameCommitCountsOnce(t *testing.T) {
	// two tags, identical trees, different commits -> ambiguous
	h := legacyBench(t)
	h.srv.Files["kb_pro@v1.1.0"] = releaseFiles(h.app("kb_pro"), "v1.2.0")
	// the files are identical but the pax-less archives differ only by top directory name; the tree compare ignores it
	out, err := h.exec("adopt", "--tag", "kb_pro=v1.1.0")
	if ExitCode(err) != 3 {
		t.Fatalf("exit %d\n%s", ExitCode(err), out)
	}
	if !strings.Contains(out, "AMBIGUOUS") || !strings.Contains(out, commitFor("kb_pro", "v1.1.0")) || !strings.Contains(out, commitFor("kb_pro", "v1.2.0")) {
		t.Errorf("output:\n%s", out)
	}
	if h.receipt("kb_pro").Valid() {
		t.Error("a receipt was written for an ambiguous match")
	}

	// two tags, one commit -> adopted once
	h = legacyBench(t)
	h.srv.Released["kb_pro@v1.2.0"], h.srv.Released["kb_pro@v1.2.0-rc"] = true, true
	h.srv.Released["kb_compta@v1.2.0"], h.srv.Released["kb_cheque@v1.2.0"] = true, true
	h.srv.Released["kb_frappe@v1.2.0"] = true
	h.srv.Files["kb_pro@v1.2.0-rc"] = releaseFiles(h.app("kb_pro"), "v1.2.0")
	h.srv.CommitOf["kb_pro@v1.2.0-rc"] = commitFor("kb_pro", "v1.2.0")
	if out, err := h.exec("adopt", "--tag", "kb_pro=v1.2.0-rc"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if rs := h.receipt("kb_pro"); !rs.Valid() || rs.Receipt.Ref != "v1.2.0" || len(rs.Receipt.Adopt.CandidatesTried) != 2 {
		t.Errorf("receipt = %+v", rs.Receipt)
	}
}

func TestAdoptSkipsAndRefusesByRule(t *testing.T) {
	h := legacyBench(t)
	h.seed("kb_distri", "v1.2.0", false)
	h.seed("kb_cheque", "v1.2.0", true) // valid receipt: skipped
	h.seed("kb_stock", "v1.2.0", false) // not licensed: skipped
	allowed := allowedSetFn
	allowedSetFn = func() map[string]bool {
		m := allowed()
		delete(m, "kb_stock")
		return m
	}
	defer func() { allowedSetFn = allowed }()

	out, err := h.exec("adopt")
	if err != nil {
		t.Fatalf("skipped apps must not affect the exit code: %v\n%s", err, out)
	}
	for _, want := range []string{"kb_distri: skipped", "kb_cheque: skipped", "kb_stock: skipped — not in your license"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, d := range h.srv.downloads() {
		if p := strings.TrimPrefix(d.Path, "/download/"); p == "kb_distri" || p == "kb_cheque" || p == "kb_stock" {
			t.Errorf("a skipped app was downloaded: %s", p)
		}
	}
	if h.receipt("kb_distri").Valid() || h.receipt("kb_stock").Valid() {
		t.Error("receipts written for skipped apps")
	}

	for _, args := range [][]string{
		{"adopt", "--tag", "kb_distri=v0.5.0"},
		{"adopt", "--tag", "kb_pro"},
		{"adopt", "--tag", "nope=v1"},
		{"adopt", "--tag", "kb_pro=../x"},
		{"adopt", "--tag", "kb_print=v1.0.0"}, // not installed
	} {
		if _, err := h.exec(args...); ExitCode(err) != 4 {
			t.Errorf("kb %v: exit %d err=%v", args, ExitCode(err), err)
		}
	}
}

func TestAdoptCandidateOutcomes(t *testing.T) {
	t.Run("404 is missing and the run continues", func(t *testing.T) {
		h := legacyBench(t)
		h.srv.Status["kb_pro"], h.srv.StatusK["kb_pro"] = 404, "version_not_found"
		out, err := h.exec("adopt")
		if ExitCode(err) != 2 {
			t.Fatalf("exit %d\n%s", ExitCode(err), out)
		}
		if !strings.Contains(out, "is missing") {
			t.Errorf("missing candidate not reported:\n%s", out)
		}
		if !h.receipt("kb_compta").Valid() || !h.receipt("frappe").Valid() {
			t.Error("the run did not continue after a 404")
		}
		if h.receipt("kb_pro").Valid() {
			t.Error("receipt written after an error")
		}
	})
	for name, st := range map[string]struct {
		status int
		key    string
	}{
		"402 stops": {402, "contract_expired"}, "403 stops": {403, "license_revoked"},
		"502 stops": {502, "upstream_error"}, "500 stops": {500, ""}, "401 stops": {401, "token_expired"},
	} {
		t.Run(name, func(t *testing.T) {
			h := legacyBench(t)
			h.srv.Status["kb_pro"], h.srv.StatusK["kb_pro"] = st.status, st.key
			before := h.benchDigest()
			out, err := h.exec("adopt")
			if ExitCode(err) != 4 {
				t.Fatalf("exit %d\n%s", ExitCode(err), out)
			}
			for _, d := range h.srv.downloads() {
				if !strings.HasSuffix(d.Path, "/kb_pro") {
					t.Errorf("the run continued to %s after the stop", d.Path)
				}
			}
			for _, r := range []string{"kb_pro", "kb_compta", "frappe"} {
				if h.receipt(r).Valid() {
					t.Errorf("a receipt for %s was written after the run stopped", r)
				}
			}
			if h.benchDigest() != before {
				t.Error("bench changed")
			}
			if j := h.journal(); j.Pending() {
				t.Errorf("journal pending: %s", j.Step)
			}
		})
	}
	t.Run("a stall past the header timeout stops the run", func(t *testing.T) {
		h := legacyBench(t)
		h.srv.Stall["kb_pro"] = true
		old := license.ResponseHeaderTimeout
		license.ResponseHeaderTimeout = 200 * time.Millisecond
		defer func() { license.ResponseHeaderTimeout = old }()
		start := time.Now()
		out, err := h.exec("adopt")
		if ExitCode(err) != 4 || time.Since(start) > 5*time.Second {
			t.Fatalf("exit %d after %v\n%s", ExitCode(err), time.Since(start), out)
		}
		if !strings.Contains(out, "retry later") {
			t.Errorf("output:\n%s", out)
		}
		if h.receipt("kb_compta").Valid() {
			t.Error("the run continued after the stall")
		}
	})
	t.Run("a server without the identity headers refuses the app", func(t *testing.T) {
		h := legacyBench(t)
		h.srv.Omit["X-KB-Release-Tag"] = true
		before := h.benchDigest()
		out, err := h.exec("adopt")
		if ExitCode(err) != 4 || !strings.Contains(out, "X-KB-Release-Tag") {
			t.Fatalf("exit %d\n%s", ExitCode(err), out)
		}
		if h.benchDigest() != before || h.receipt("kb_pro").Valid() {
			t.Error("state changed")
		}
	})
}

func TestAdoptHonoursTheNoLicenseCase(t *testing.T) {
	h := legacyBench(t)
	allowed := allowedSetFn
	allowedSetFn = func() map[string]bool { return nil }
	defer func() { allowedSetFn = allowed }()
	_, err := h.exec("adopt")
	if ExitCode(err) != 4 || h.srv.requestCount() != 0 {
		t.Fatalf("exit %d, %d requests", ExitCode(err), h.srv.requestCount())
	}
}
