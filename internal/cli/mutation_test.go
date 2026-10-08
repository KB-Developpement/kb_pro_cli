package cli

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KB-Developpement/kb_pro_cli/internal/apps"
	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
	"github.com/KB-Developpement/kb_pro_cli/internal/testutil"
	"github.com/KB-Developpement/kb_pro_cli/internal/version"
)

var hex40 = regexp.MustCompile(`^[0-9a-f]{40}$`)

func requireErr(t *testing.T, err error, contains ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	for _, c := range contains {
		if !strings.Contains(err.Error(), c) {
			t.Errorf("error does not contain %q:\n%v", c, err)
		}
	}
}

// ── registration order ──────────────────────────────────────────────────────

// startsNeverOverlap checks the fake bench's log: every "start" is followed by
// its own "end" before anything else starts.
func startsNeverOverlap(t *testing.T, lines []string) {
	t.Helper()
	for i := 0; i < len(lines); i += 2 {
		start := strings.Fields(lines[i])
		if len(start) < 2 || start[0] != "start" {
			t.Fatalf("log line %d is %q, want a start", i, lines[i])
		}
		if i+1 >= len(lines) {
			t.Fatalf("start without end: %q", lines[i])
		}
		end := strings.Fields(lines[i+1])
		if len(end) < 2 || end[0] != "end" || end[1] != start[1] {
			t.Fatalf("registration overlapped: %q was followed by %q", lines[i], lines[i+1])
		}
	}
}

func TestInstallThreeAppsRegistersInInputOrder(t *testing.T) {
	order := []string{"kb_cheque", "kb_pro", "kb_compta"} // not registry order
	for _, tc := range []struct {
		name         string
		concurrency  int
		wantParallel bool
	}{{"concurrency 1", 1, false}, {"default concurrency", 3, true}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newBenchH(t)
			downloadConcurrency = tc.concurrency
			// The first app in the command line downloads slowest, so completion
			// order is the reverse of input order.
			h.srv.Delay["kb_cheque"] = 400 * time.Millisecond
			h.srv.Delay["kb_pro"] = 200 * time.Millisecond
			t.Setenv("KB_FAKE_SLEEP", "0.02")

			out, err := h.exec("install", "--apps", strings.Join(order, ","))
			if err != nil {
				t.Fatalf("install: %v\n%s", err, out)
			}
			want := append([]string{"frappe"}, order...)
			if got := h.appsTxt(); strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("apps.txt = %v, want %v (each name once, in command-line order)", got, want)
			}
			if tc.wantParallel && h.srv.MaxInflight < 2 {
				t.Errorf("downloads did not overlap (max in flight %d)", h.srv.MaxInflight)
			}
			if !tc.wantParallel && h.srv.MaxInflight != 1 {
				t.Errorf("max in flight %d with concurrency 1", h.srv.MaxInflight)
			}

			lines := h.benchLogLines()
			startsNeverOverlap(t, lines)
			// First `setup requirements --python <app>` per app appears in input order.
			var seen []string
			for _, l := range lines {
				if strings.HasPrefix(l, "start") && strings.Contains(l, "setup requirements --python") {
					seen = append(seen, strings.Fields(l)[len(strings.Fields(l))-1])
				}
			}
			if strings.Join(seen, ",") != strings.Join(order, ",") {
				t.Errorf("requirements ran for %v, want %v", seen, order)
			}
			for _, name := range order {
				if rs := h.receipt(name); !rs.Valid() {
					t.Errorf("no receipt for %s: %s", name, rs.Reason)
				}
			}
		})
	}
}

// ── receipts and request selectors ──────────────────────────────────────────

func TestEveryCommandWritesAReceipt(t *testing.T) {
	h := newBenchH(t)
	h.seed("kb_cheque", "v1.0.0", true)
	// init-kb-frappe replaces a clean stock checkout.
	frappe := h.appDir("frappe")
	must(t, os.RemoveAll(frappe))
	testutil.StockClone(t, frappe, "https://github.com/frappe/frappe.git")
	must(t, os.Remove(h.receipt("frappe").Path)) // seedFrappe wrote one; there is none on a stock bench

	steps := [][]string{
		{"init-kb-frappe"}, // first: the other commands refuse while apps/frappe is stock
		{"add", "--apps", "kb_pro"},
		{"install", "--apps", "kb_compta"},
		{"upgrade", "--apps", "kb_cheque"},
	}
	for _, args := range steps {
		if out, err := h.exec(args...); err != nil {
			t.Fatalf("kb %v: %v\n%s", args, err, out)
		}
	}
	for _, c := range []struct{ name, dir, pkg string }{
		{"kb_pro", "kb_pro", "kb_pro"}, {"kb_compta", "kb_compta", "kb_compta"}, {"kb_cheque", "kb_cheque", "kb_cheque"}, {"kb_frappe", "frappe", "frappe"},
	} {
		rs := h.receipt(c.dir)
		if !rs.Valid() {
			t.Fatalf("%s: no valid receipt: %s", c.name, rs.Reason)
		}
		r := rs.Receipt
		a, _ := apps.ByName(c.name)
		wantRef := "v1.2.0"
		if r.SchemaVersion != 1 || r.Provenance != "installed" || !hex40.MatchString(r.Commit) || r.Commit != commitFor(c.name, wantRef) ||
			r.Package != c.pkg || r.Directory != c.dir || r.App != c.name || r.Repository != "KB-Developpement/"+a.Repository() ||
			r.Ref != wantRef || r.Requested != "major=1" || r.Line != "1" || r.State != "archive" || r.ReleaseTag != nil {
			t.Errorf("%s receipt = %+v", c.name, r)
		}
		if want := testutil.Sha256Hex(h.srv.archive(c.name, wantRef)); r.ArchiveSHA256 != want {
			t.Errorf("%s archive_sha256 = %s, want the digest of the bytes served %s", c.name, r.ArchiveSHA256, want)
		}
		raw := string(rs.Raw)
		if !strings.Contains(raw, `"release_tag": null`) || !strings.Contains(raw, `"session_id": null`) {
			t.Errorf("%s: release_tag/session_id are not null:\n%s", c.name, raw)
		}
		if fi, _ := os.Stat(rs.Path); fi.Mode().Perm() != 0o600 {
			t.Errorf("receipt mode = %v", fi.Mode().Perm())
		}
	}
	if fi, err := os.Stat(kbstate.Dir(h.root)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf(".kb mode: %v %v", fi, err)
	}
	if j := h.journal(); j.Step != kbstate.StepFinalized {
		t.Errorf("journal step after a completed run = %s", j.Step)
	}
}

func queryKeys(r reqRecord) string {
	var keys []string
	for k := range r.Query {
		keys = append(keys, k+"="+r.Query.Get(k))
	}
	for i := range keys {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return strings.Join(keys, "&")
}

func TestCapturedRequestSelectors(t *testing.T) {
	old := version.Version
	version.Version = "0.9.3"
	defer func() { version.Version = old }()

	h := newBenchH(t)
	h.seed("kb_cheque", "v1.0.0", true)
	h.seed("kb_facilite", "v1.0.0", true)
	frappe := h.appDir("frappe")
	must(t, os.RemoveAll(frappe))
	testutil.StockClone(t, frappe, "https://github.com/frappe/frappe.git")

	cases := []struct {
		args []string
		want string
	}{
		{[]string{"init-kb-frappe"}, "major=1"}, // first: the others refuse while apps/frappe is stock
		{[]string{"add", "--apps", "kb_pro"}, "major=1"},
		{[]string{"add", "--apps", "kb_compta", "--version", "v1.1.0"}, "v=v1.1.0"},
		{[]string{"install", "--apps", "kb_stock"}, "major=1"},
		{[]string{"upgrade", "--apps", "kb_cheque"}, "major=1"},
		{[]string{"upgrade", "--to", "kb_facilite=v1.1.0"}, "release=1&v=v1.1.0"},
	}
	for _, c := range cases {
		before := len(h.srv.downloads())
		if out, err := h.exec(c.args...); err != nil {
			t.Fatalf("kb %v: %v\n%s", c.args, err, out)
		}
		got := h.srv.downloads()[before:]
		if len(got) != 1 {
			t.Fatalf("kb %v sent %d downloads", c.args, len(got))
		}
		if q := queryKeys(got[0]); q != c.want {
			t.Errorf("kb %v sent ?%s, want ?%s", c.args, q, c.want)
		}
		if got[0].CLIVersion != "0.9.3" {
			t.Errorf("kb %v: %s = %q", c.args, "X-KB-CLI-Version", got[0].CLIVersion)
		}
	}
	for _, r := range h.srv.all() {
		if r.CLIVersion != "0.9.3" {
			t.Errorf("request %s %s without X-KB-CLI-Version", r.Method, r.Path)
		}
	}
}

func TestDevBuildSendsDev(t *testing.T) {
	h := newBenchH(t)
	if _, err := h.exec("add", "--apps", "kb_pro"); err != nil {
		t.Fatal(err)
	}
	for _, r := range h.srv.downloads() {
		if r.CLIVersion != "dev" {
			t.Errorf("a dev build sent %q", r.CLIVersion)
		}
	}
}

func TestRegistryRowWithoutLineIsRefusedBeforeAnyRequest(t *testing.T) {
	h := newBenchH(t)
	h.seed("kb_cheque", "v1.0.0", true)
	saved := apps.All
	apps.All = append([]apps.App(nil), saved...)
	for i := range apps.All {
		apps.All[i].Line = 0
	}
	defer func() { apps.All = saved }()

	before := h.benchDigest()
	for _, args := range [][]string{{"add", "--apps", "kb_pro"}, {"add", "--apps", "kb_pro", "--version", "v1.1.0"}, {"install", "--apps", "kb_compta"}, {"upgrade", "--apps", "kb_cheque"}, {"upgrade", "--to", "kb_cheque=v1.1.0"}} {
		out, err := h.exec(args...)
		if err == nil || !strings.Contains(out+err.Error(), "no release line") {
			t.Errorf("kb %v: err=%v out=%s", args, err, out)
		}
	}
	if n := h.srv.requestCount(); n != 0 {
		t.Fatalf("%d HTTP requests were sent, want 0", n)
	}
	if h.benchDigest() != before {
		t.Error("the bench changed")
	}
}

// A server that does not send the identity headers (or sends bad ones) is
// refused before anything under apps/ is touched, with the compatibility message.
func TestBadIdentityHeadersRefuseBeforeMutation(t *testing.T) {
	defects := map[string]func(s *dlServer){
		"missing X-KB-Commit":     func(s *dlServer) { s.Omit["X-KB-Commit"] = true },
		"missing X-KB-Ref":        func(s *dlServer) { s.Omit["X-KB-Ref"] = true },
		"missing X-KB-Repository": func(s *dlServer) { s.Omit["X-KB-Repository"] = true },
		"no headers at all": func(s *dlServer) {
			s.Omit["X-KB-Commit"], s.Omit["X-KB-Ref"], s.Omit["X-KB-Repository"] = true, true, true
		},
		"39 hex":    func(s *dlServer) { s.Set["X-KB-Commit"] = strings.Repeat("a", 39) },
		"uppercase": func(s *dlServer) { s.Set["X-KB-Commit"] = strings.Repeat("A", 40) },
		"newline":   func(s *dlServer) { s.Set["X-KB-Commit"] = strings.Repeat("a", 20) + "\n" + strings.Repeat("a", 19) },
	}
	cmds := map[string][]string{
		"add":            {"add", "--apps", "kb_pro"},
		"install":        {"install", "--apps", "kb_pro"},
		"upgrade":        {"upgrade", "--apps", "kb_cheque"},
		"init-kb-frappe": {"init-kb-frappe"},
	}
	for dname, defect := range defects {
		for cname, args := range cmds {
			t.Run(dname+"/"+cname, func(t *testing.T) {
				h := newBenchH(t)
				h.seed("kb_cheque", "v1.0.0", true)
				if cname == "init-kb-frappe" {
					must(t, os.RemoveAll(h.appDir("frappe")))
					testutil.StockClone(t, h.appDir("frappe"), "https://github.com/frappe/frappe.git")
				}
				defect(h.srv)
				before := h.benchDigest()
				oldReceipt, _ := os.ReadFile(h.receipt("kb_cheque").Path)

				out, err := h.exec(args...)
				if err == nil {
					t.Fatalf("kb %v succeeded against a non-conforming server\n%s", args, out)
				}
				if !strings.Contains(out+err.Error(), "server-compatibility") {
					t.Errorf("no server-compatibility message:\n%s\n%v", out, err)
				}
				if h.benchDigest() != before {
					t.Error("the bench changed")
				}
				for _, dir := range []string{"kb_pro"} {
					if h.receipt(dir).Valid() {
						t.Errorf("a receipt for %s was written", dir)
					}
				}
				if got, _ := os.ReadFile(h.receipt("kb_cheque").Path); string(got) != string(oldReceipt) {
					t.Error("the old receipt changed")
				}
				if js := kbstate.ReadJournal(h.root); js.Journal != nil && js.Journal.Pending() {
					t.Error("journal left pending")
				}
			})
		}
	}
}

func TestVersionFlagWithSeveralAppsRefusedBeforeDownload(t *testing.T) {
	h := newBenchH(t)
	before := h.benchDigest()
	for _, verb := range []string{"install", "add"} {
		out, err := h.exec(verb, "--apps", "kb_pro,kb_compta", "--version", "v1.1.0")
		if err == nil || !strings.Contains(err.Error(), "--version") {
			t.Fatalf("kb %s: err=%v out=%s", verb, err, out)
		}
	}
	if n := h.srv.requestCount(); n != 0 {
		t.Errorf("%d requests were sent, want 0", n)
	}
	if h.benchDigest() != before {
		t.Error("the bench changed")
	}
	// With one app the same flag proceeds.
	if out, err := h.exec("add", "--apps", "kb_pro", "--version", "v1.1.0"); err != nil {
		t.Fatalf("one app with --version: %v\n%s", err, out)
	}
	if rs := h.receipt("kb_pro"); !rs.Valid() || rs.Receipt.Ref != "v1.1.0" || rs.Receipt.Requested != "v1.1.0" || rs.Receipt.ReleaseTag != nil {
		t.Errorf("receipt = %+v (%s)", rs.Receipt, rs.Reason)
	}
}

// ── truncated archives ──────────────────────────────────────────────────────

func TestTruncatedArchiveChangesNothing(t *testing.T) {
	cases := map[string][]string{
		"upgrade":        {"upgrade", "--apps", "kb_cheque"},
		"install":        {"install", "--apps", "kb_pro"},
		"init-kb-frappe": {"init-kb-frappe"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			h := newBenchH(t)
			h.seed("kb_cheque", "v1.0.0", true)
			if name == "init-kb-frappe" {
				must(t, os.RemoveAll(h.appDir("frappe")))
				testutil.StockClone(t, h.appDir("frappe"), "https://github.com/frappe/frappe.git")
				must(t, os.Remove(h.receipt("frappe").Path)) // a stock bench has no framework receipt
			}
			// Make the archives incompressible enough that 1 KiB is mid-stream.
			for _, app := range []string{"kb_pro", "kb_cheque", "kb_frappe"} {
				a, _ := apps.ByName(app)
				files := releaseFiles(a, "v1.2.0")
				files = append(files, testutil.File{Name: "big.bin", Body: testutil.IncompressibleBody(30000)})
				h.srv.Files[app+"@v1.2.0"] = files
			}
			h.srv.Cut = 1024

			before := h.benchDigest()
			oldReceipt, _ := os.ReadFile(h.receipt("kb_cheque").Path)
			out, err := h.exec(args...)
			if err == nil {
				t.Fatalf("a truncated archive was accepted\n%s", out)
			}
			if !strings.Contains(out+err.Error(), "unexpected EOF") {
				t.Errorf("not a gzip unexpected-EOF failure:\n%s\n%v", out, err)
			}
			if h.benchDigest() != before {
				t.Error("apps/ or sites/ changed")
			}
			if name != "init-kb-frappe" && name != "install" {
				if got, _ := os.ReadFile(h.receipt("kb_cheque").Path); string(got) != string(oldReceipt) {
					t.Error("the old receipt changed")
				}
			}
			if name == "install" && h.receipt("kb_pro").Valid() {
				t.Error("a receipt was written")
			}
			if name == "init-kb-frappe" && h.receipt("frappe").Valid() {
				t.Error("a frappe receipt was written")
			}
			if js := kbstate.ReadJournal(h.root); js.Journal != nil && js.Journal.Pending() {
				t.Error("journal left pending")
			}
		})
	}
}

// ── retention ───────────────────────────────────────────────────────────────

func TestUpgradeWithoutValidReceiptKeepsPreviousTree(t *testing.T) {
	variants := map[string]func(h *benchH){
		"no receipt": func(h *benchH) {},
		"truncated": func(h *benchH) {
			raw := testutil.MustRead(t, h.receipt("kb_cheque").Path)
			write(t, h.receipt("kb_cheque").Path, string(raw[:len(raw)/2]))
		},
		"not json": func(h *benchH) { write(t, h.receipt("kb_cheque").Path, "not json at all") },
		"bad commit": func(h *benchH) {
			raw := string(testutil.MustRead(t, h.receipt("kb_cheque").Path))
			write(t, h.receipt("kb_cheque").Path, strings.Replace(raw, commitFor("kb_cheque", "v1.0.0"), strings.Repeat("a", 39), 1))
		},
		"bad sha": func(h *benchH) {
			raw := string(testutil.MustRead(t, h.receipt("kb_cheque").Path))
			sum := testutil.Sha256Hex(h.srv.archive("kb_cheque", "v1.0.0"))
			write(t, h.receipt("kb_cheque").Path, strings.Replace(raw, sum, strings.ToUpper(sum), 1))
		},
	}
	for name, prep := range variants {
		t.Run(name, func(t *testing.T) {
			h := newBenchH(t)
			h.seed("kb_cheque", "v1.0.0", name != "no receipt")
			prep(h)
			if name == "no receipt" {
				_ = os.RemoveAll(filepath.Join(h.root, ".kb"))
			}
			before := h.digest("kb_cheque")

			if out, err := h.exec("upgrade", "--apps", "kb_cheque"); err != nil {
				t.Fatalf("upgrade refused an app with unknown provenance: %v\n%s", err, out)
			}
			if rs := h.receipt("kb_cheque"); !rs.Valid() || rs.Receipt.Commit != commitFor("kb_cheque", "v1.2.0") {
				t.Fatalf("first receipt not written: %+v %s", rs.Receipt, rs.Reason)
			}
			matches, _ := filepath.Glob(filepath.Join(h.root, ".kb", "recovery", "kb_cheque-pre-receipt-*"))
			if len(matches) != 1 {
				t.Fatalf("pre-receipt copies = %v", matches)
			}
			if got := testutil.TreeDigest(t, matches[0]); got != before {
				t.Errorf("the retained copy's digest differs from the pre-upgrade app digest")
			}
			if _, err := os.Stat(h.appDir("kb_cheque.kb-old")); err == nil {
				t.Error(".kb-old left in apps/")
			}
			out, err := h.exec("status")
			if err != nil || !strings.Contains(out, matches[0]) {
				t.Errorf("status does not list %s:\n%s", matches[0], out)
			}
			if !regexp.MustCompile(regexp.QuoteMeta(matches[0]) + `\s+\S+ \S+`).MatchString(out) {
				t.Errorf("status does not show a size for the copy:\n%s", out)
			}
		})
	}
}

func TestUpgradeWithValidReceiptDeletesPreviousTree(t *testing.T) {
	h := newBenchH(t)
	h.seed("kb_cheque", "v1.0.0", true)
	if out, err := h.exec("upgrade", "--apps", "kb_cheque"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if matches, _ := filepath.Glob(filepath.Join(h.root, ".kb", "recovery", "*")); len(matches) != 0 {
		t.Errorf("recovery copies exist for an app that had a valid receipt: %v", matches)
	}
	if _, err := os.Stat(h.appDir("kb_cheque.kb-old")); err == nil {
		t.Error(".kb-old left in apps/")
	}
	if rs := h.receipt("kb_cheque"); !rs.Valid() || rs.Receipt.Commit != commitFor("kb_cheque", "v1.2.0") {
		t.Errorf("receipt not refreshed: %+v", rs.Receipt)
	}
	// the upgrade also refreshed the apps.json entry
	raw := string(testutil.MustRead(t, filepath.Join(h.root, "sites", "apps.json")))
	if !strings.Contains(raw, `"1.2.0"`) {
		t.Errorf("apps.json not updated: %s", raw)
	}
}

func TestLeftoversAreMovedNotDeletedByUpgrade(t *testing.T) {
	h := newBenchH(t)
	h.seed("kb_cheque", "v1.0.0", true)
	write(t, filepath.Join(h.appDir("kb_cheque.kb-old"), "precious.txt"), "kept after a failed migration")
	write(t, filepath.Join(h.appDir("kb_cheque.kb-new"), "half.txt"), "half-extracted")
	oldD, newD := h.digest("kb_cheque.kb-old"), h.digest("kb_cheque.kb-new")

	if out, err := h.exec("upgrade", "--apps", "kb_cheque"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	j := h.journal()
	if len(j.Legacy) != 2 {
		t.Fatalf("journal records %d moved leftovers: %+v", len(j.Legacy), j.Legacy)
	}
	found := map[string]bool{}
	for _, m := range j.Legacy {
		d := testutil.TreeDigest(t, m.Destination)
		switch filepath.Base(m.Original) {
		case "kb_cheque.kb-old":
			found["old"] = d == oldD
		case "kb_cheque.kb-new":
			found["new"] = d == newD
		}
		if !regexp.MustCompile(`kb_cheque-kb-(old|new)-\d{8}T\d{6}Z`).MatchString(m.Destination) {
			t.Errorf("destination name %s", m.Destination)
		}
	}
	if !found["old"] || !found["new"] {
		t.Errorf("a moved leftover's digest changed or is missing: %v", found)
	}
	out, _ := h.exec("status")
	if strings.Count(out, "(legacy)") != 2 {
		t.Errorf("status does not list both leftovers:\n%s", out)
	}
}

// ── upgrade --to ────────────────────────────────────────────────────────────

func TestUpgradeTo(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		h := newBenchH(t)
		h.seed("kb_cheque", "v1.0.0", false) // no receipt: retention rule applies
		before := h.digest("kb_cheque")
		out, err := h.exec("upgrade", "--to", "kb_cheque=v1.1.0")
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		d := h.srv.downloads()
		if len(d) != 1 || queryKeys(d[0]) != "release=1&v=v1.1.0" {
			t.Fatalf("requests = %+v", d)
		}
		r := h.receipt("kb_cheque").Receipt
		if r == nil || r.ReleaseTag == nil || *r.ReleaseTag != "v1.1.0" || r.Ref != "v1.1.0" || r.Commit != commitFor("kb_cheque", "v1.1.0") || r.Requested != "v1.1.0" {
			t.Fatalf("receipt = %+v", r)
		}
		matches, _ := filepath.Glob(filepath.Join(h.root, ".kb", "recovery", "kb_cheque-pre-receipt-*"))
		if len(matches) != 1 || testutil.TreeDigest(t, matches[0]) != before {
			t.Errorf("previous tree not retained: %v", matches)
		}
	})
	t.Run("refusals before any mutation", func(t *testing.T) {
		for name, tag := range map[string]string{"draft": "draft-1", "branch": "main", "bare commit": strings.Repeat("a", 40)} {
			t.Run(name, func(t *testing.T) {
				h := newBenchH(t)
				h.seed("kb_cheque", "v1.0.0", true)
				before := h.benchDigest()
				oldReceipt := testutil.MustRead(t, h.receipt("kb_cheque").Path)
				out, err := h.exec("upgrade", "--to", "kb_cheque="+tag)
				if err == nil {
					t.Fatalf("upgrade --to %s succeeded\n%s", tag, out)
				}
				if h.benchDigest() != before || string(testutil.MustRead(t, h.receipt("kb_cheque").Path)) != string(oldReceipt) {
					t.Error("state changed")
				}
			})
		}
	})
	t.Run("server ignores release=1", func(t *testing.T) {
		h := newBenchH(t)
		h.seed("kb_cheque", "v1.0.0", true)
		h.srv.Omit["X-KB-Release-Tag"] = true
		before := h.benchDigest()
		out, err := h.exec("upgrade", "--to", "kb_cheque=v1.1.0")
		if err == nil || !strings.Contains(out+err.Error(), "X-KB-Release-Tag") {
			t.Fatalf("err=%v out=%s", err, out)
		}
		if h.benchDigest() != before {
			t.Error("the bench changed")
		}
	})
	t.Run("selection and argument rules", func(t *testing.T) {
		h := newBenchH(t)
		h.seed("kb_cheque", "v1.0.0", true)
		for _, args := range [][]string{
			{"upgrade", "--to", "kb_cheque"},
			{"upgrade", "--to", "nope=v1.0.0"},
			{"upgrade", "--to", "kb_cheque=v1.0.0", "--to", "kb_cheque=v1.1.0"},
			{"upgrade", "--to", "kb_cheque=../x"},
			{"upgrade", "--to", "kb_pro=v1.0.0"}, // licensed but not in the bench
		} {
			if _, err := h.exec(args...); err == nil {
				t.Errorf("kb %v succeeded", args)
			}
		}
		if n := h.srv.requestCount(); n != 0 {
			t.Errorf("%d requests were sent for invalid --to arguments", n)
		}
		// --to joins --apps, and with --to alone only the pinned apps upgrade.
		h.seed("kb_facilite", "v1.0.0", true)
		if out, err := h.exec("upgrade", "--apps", "kb_cheque", "--to", "kb_facilite=v1.1.0"); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		got := map[string]string{}
		for _, d := range h.srv.downloads() {
			got[strings.TrimPrefix(d.Path, "/download/")] = queryKeys(d)
		}
		if got["kb_cheque"] != "major=1" || got["kb_facilite"] != "release=1&v=v1.1.0" || len(got) != 2 {
			t.Errorf("requests = %v", got)
		}
	})
}

// ── lock, owner ─────────────────────────────────────────────────────────────

func TestLockedMutationsFailFastNamingHolder(t *testing.T) {
	h := newBenchH(t)
	h.seed("kb_cheque", "v1.0.0", true)
	pid := h.holdLock()
	before := h.benchDigest()

	ctx := context.Background()
	calls := map[string]func() error{
		"add":            func() error { return runAdd(ctx, []string{"kb_pro"}, "") },
		"install (menu)": func() error { return runInstall(ctx, "site1", nil, "") },
		"site-install":   func() error { return runSiteInstall(ctx, "site1", nil) },
		"upgrade (menu)": func() error { return runUpgrade(ctx, nil, nil) },
		"init-kb-frappe": func() error { return runInitKBFrappe(ctx, true) },
		"manage":         func() error { return runLocked(ctx, func() error { return nil }) },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			err := call()
			if time.Since(start) > time.Second {
				t.Errorf("took %v", time.Since(start))
			}
			requireErr(t, err, "PID "+itoa(pid))
		})
	}
	if h.benchDigest() != before {
		t.Error("the bench changed")
	}
	if n := h.srv.requestCount(); n != 0 {
		t.Errorf("%d requests while the bench was locked", n)
	}

	// CLI path too: the cobra command fails with the same message.
	_, err := h.exec("add", "--apps", "kb_pro")
	requireErr(t, err, "PID "+itoa(pid))
	_, err = h.exec("adopt")
	requireErr(t, err, "PID "+itoa(pid))
	if ExitCode(err) != 4 {
		t.Errorf("adopt exit code = %d, want 4", ExitCode(err))
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestNonOwnerRefusesEveryMutatingCommand(t *testing.T) {
	h := newBenchH(t)
	h.seed("kb_cheque", "v1.0.0", true)
	orig := kbstate.Geteuid
	kbstate.Geteuid = func() int { return os.Geteuid() + 1 }
	defer func() { kbstate.Geteuid = orig }()
	before := h.benchDigest()
	kbBefore := testutil.TreeDigest(t, kbstate.Dir(h.root))

	for _, args := range [][]string{
		{"add", "--apps", "kb_pro"}, {"install", "--apps", "kb_pro"}, {"site-install", "--apps", "kb_cheque"},
		{"upgrade", "--apps", "kb_cheque"}, {"init-kb-frappe", "--force"}, {"adopt"},
	} {
		_, err := h.exec(args...)
		requireErr(t, err, "bench user")
	}
	if err := runLocked(context.Background(), func() error { return nil }); err == nil || !strings.Contains(err.Error(), "bench user") {
		t.Errorf("manage path: %v", err)
	}
	if h.benchDigest() != before {
		t.Error("the bench changed")
	}
	if testutil.TreeDigest(t, kbstate.Dir(h.root)) != kbBefore {
		t.Error(".kb changed under a refused command (a root-owned .kb is exactly what the check prevents)")
	}
	if _, err := os.Stat(kbstate.LockPath(h.root)); err == nil {
		t.Error("the lock file was created by a refused command")
	}
	if n := h.srv.requestCount(); n != 0 {
		t.Errorf("%d requests", n)
	}
	// read-only commands are not affected
	if _, err := h.exec("status"); err != nil {
		t.Errorf("status: %v", err)
	}
}

// ── Git guard ───────────────────────────────────────────────────────────────

// gitApp turns apps/<dir> into a Git checkout in one of the states the spec
// names. kind is "dir" (a clone) or "worktree" (a .git file).
func gitApp(t *testing.T, h *benchH, dir, kind, state string) {
	t.Helper()
	target := h.appDir(dir)
	must(t, os.RemoveAll(target))
	if kind == "dir" {
		testutil.StockClone(t, target, "https://github.com/KB-Developpement/"+dir)
	} else {
		main := filepath.Join(t.TempDir(), "main")
		testutil.StockClone(t, main, "https://github.com/KB-Developpement/"+dir)
		testutil.Git(t, main, "worktree", "add", "-q", "--detach", target)
	}
	switch state {
	case "dirty":
		write(t, filepath.Join(target, "README.md"), "edited\n")
	case "unpushed":
		write(t, filepath.Join(target, "mine.py"), "x")
		testutil.Git(t, target, "add", ".")
		testutil.Git(t, target, "commit", "-q", "-m", "local")
	}
}

func TestGitCheckoutsAreRefusedByEveryMutation(t *testing.T) {
	for _, kind := range []string{"dir", "worktree"} {
		for _, state := range []string{"clean", "dirty", "unpushed"} {
			t.Run(kind+"/"+state, func(t *testing.T) {
				h := newBenchH(t)
				h.seed("kb_cheque", "v1.0.0", true)
				h.seed("kb_facilite", "v1.0.0", true)
				gitApp(t, h, "kb_cheque", kind, state)
				before := h.benchDigest()
				gitBefore := len(h.gitCalls())

				for _, args := range [][]string{
					{"upgrade", "--apps", "kb_cheque"},
					{"upgrade", "--apps", "kb_facilite,kb_cheque"}, // whole selection is refused, not just the Git app
					{"add", "--apps", "kb_cheque"},
					{"install", "--apps", "kb_cheque"},
					{"install", "--apps", "kb_cheque,kb_pro"},
					{"upgrade", "--to", "kb_cheque=v1.1.0"},
				} {
					out, err := h.exec(args...)
					if err == nil || !strings.Contains(err.Error(), "apps/kb_cheque") || !strings.Contains(err.Error(), ".git") {
						t.Errorf("kb %v: err=%v\n%s", args, err, out)
					}
					if h.benchDigest() != before {
						t.Fatalf("kb %v changed the bench", args)
					}
				}
				if n := h.srv.requestCount(); n != 0 {
					t.Errorf("%d requests: the refusal must come before any download", n)
				}
				if len(h.gitCalls()) != gitBefore {
					t.Error("kb ran git against an app checkout")
				}

				// manage removal: a selection with one failing app is refused whole,
				// before the first uninstall.
				os.Setenv("KB_FAKE_INSTALLED", `"frappe","kb_facilite","kb_cheque"`)
				_, rerr := removeApps(context.Background(), "site1", []string{"kb_facilite", "kb_cheque"}, map[string]bool{"kb_facilite": true, "kb_cheque": true}, false)
				requireErr(t, rerr, "apps/kb_cheque")
				for _, l := range h.benchLogLines() {
					if strings.Contains(l, "uninstall-app") || strings.Contains(l, "remove-app") {
						t.Errorf("bench was called before the preflight refused: %s", l)
					}
				}
				if h.benchDigest() != before {
					t.Error("manage removal changed the bench")
				}
			})
		}
	}
}

func TestInitKBFrappeRefusesOtherGitCheckouts(t *testing.T) {
	for _, force := range []bool{false, true} {
		for name, setup := range map[string]func(t *testing.T, h *benchH){
			"kb fork clone": func(t *testing.T, h *benchH) {
				testutil.StockClone(t, h.appDir("frappe"), "https://github.com/KB-Developpement/kb_frappe.git")
			},
			"dirty":     func(t *testing.T, h *benchH) { write(t, filepath.Join(h.appDir("frappe"), "README.md"), "edited\n") },
			"untracked": func(t *testing.T, h *benchH) { write(t, filepath.Join(h.appDir("frappe"), "new.py"), "x") },
			"stash": func(t *testing.T, h *benchH) {
				write(t, filepath.Join(h.appDir("frappe"), "README.md"), "edited\n")
				testutil.Git(t, h.appDir("frappe"), "stash", "-q")
			},
			"remote http": func(t *testing.T, h *benchH) {
				testutil.Git(t, h.appDir("frappe"), "remote", "set-url", "origin", "http://github.com/frappe/frappe")
			},
			"remote other": func(t *testing.T, h *benchH) {
				testutil.Git(t, h.appDir("frappe"), "remote", "set-url", "origin", "https://example.com/frappe/frappe")
			},
			"remote fork": func(t *testing.T, h *benchH) {
				testutil.Git(t, h.appDir("frappe"), "remote", "set-url", "origin", "https://github.com/frappe/frappe-fork.git")
			},
			"extra branch": func(t *testing.T, h *benchH) { testutil.Git(t, h.appDir("frappe"), "branch", "other") },
			"linked worktree .git file": func(t *testing.T, h *benchH) {
				main := filepath.Join(t.TempDir(), "main")
				testutil.StockClone(t, main, "https://github.com/frappe/frappe.git")
				must(t, os.RemoveAll(h.appDir("frappe")))
				testutil.Git(t, main, "worktree", "add", "-q", "--detach", h.appDir("frappe"))
			},
			"extra worktree": func(t *testing.T, h *benchH) {
				testutil.Git(t, h.appDir("frappe"), "worktree", "add", "-q", "--detach", filepath.Join(t.TempDir(), "wt"))
			},
			"unpushed commit": func(t *testing.T, h *benchH) {
				write(t, filepath.Join(h.appDir("frappe"), "mine.py"), "x")
				testutil.Git(t, h.appDir("frappe"), "add", ".")
				testutil.Git(t, h.appDir("frappe"), "commit", "-q", "-m", "local")
			},
		} {
			t.Run(name+"/force="+map[bool]string{false: "no", true: "yes"}[force], func(t *testing.T) {
				h := newBenchH(t)
				must(t, os.RemoveAll(h.appDir("frappe")))
				testutil.StockClone(t, h.appDir("frappe"), "https://github.com/frappe/frappe.git")
				if name == "kb fork clone" {
					must(t, os.RemoveAll(h.appDir("frappe")))
				}
				setup(t, h)
				before := h.benchDigest()
				args := []string{"init-kb-frappe"}
				if force {
					args = append(args, "--force")
				}
				out, err := h.exec(args...)
				if err == nil {
					t.Fatalf("init-kb-frappe converted a checkout that fails the predicate\n%s", out)
				}
				if h.benchDigest() != before {
					t.Error("the checkout changed")
				}
				if n := h.srv.requestCount(); n != 0 {
					t.Errorf("%d requests before the predicate refused", n)
				}
			})
		}
	}
}

func TestInitKBFrappeConvertsCleanStockCheckout(t *testing.T) {
	remotes := map[string]bool{
		"https://github.com/frappe/frappe":     true,
		"git@github.com:frappe/frappe":         true,
		"ssh://git@github.com/frappe/frappe":   true,
		"https://GitHub.com/frappe/frappe/":    true,
		"https://github.com/frappe/frappe.git": true,
		"http://github.com/frappe/frappe":      false,
		"https://example.com/frappe/frappe":    false,
	}
	for remote, ok := range remotes {
		for _, force := range []bool{false, true} {
			t.Run(remote+"/force="+map[bool]string{false: "no", true: "yes"}[force], func(t *testing.T) {
				h := newBenchH(t)
				must(t, os.RemoveAll(h.appDir("frappe")))
				testutil.StockClone(t, h.appDir("frappe"), remote)
				must(t, os.Remove(h.receipt("frappe").Path))
				before := h.benchDigest()
				_ = os.Remove(h.gitLog) // forget the fixture's own git calls
				args := []string{"init-kb-frappe"}
				if force {
					args = append(args, "--force")
				}
				out, err := h.exec(args...)
				if !ok {
					if err == nil {
						t.Fatalf("converted a checkout with remote %s\n%s", remote, out)
					}
					if h.benchDigest() != before {
						t.Error("digest changed")
					}
					return
				}
				if err != nil {
					t.Fatalf("clean stock checkout with remote %s: %v\n%s", remote, err, out)
				}
				if _, err := os.Stat(filepath.Join(h.appDir("frappe"), ".git")); err == nil {
					t.Error("apps/frappe still holds the stock .git")
				}
				if matches, _ := filepath.Glob(filepath.Join(h.root, ".kb", "recovery", "*")); len(matches) != 0 {
					t.Errorf("a copy of the stock tree was kept: %v", matches)
				}
				if _, err := os.Stat(h.appDir("frappe.kb-old")); err == nil {
					t.Error("frappe.kb-old left behind")
				}
				if !strings.Contains(out, "stock Frappe tree was not kept") {
					t.Errorf("the command does not say the stock tree was not kept:\n%s", out)
				}
				if rs := h.receipt("frappe"); !rs.Valid() || rs.Receipt.App != "kb_frappe" || rs.Receipt.Package != "frappe" {
					t.Errorf("framework receipt = %+v %s", rs.Receipt, rs.Reason)
				}
				// the stock predicate ran offline: only local git commands, all pinned to apps/frappe
				for _, c := range h.gitCalls() {
					if !strings.Contains(c, "--git-dir="+h.appDir("frappe")+"/.git") {
						t.Errorf("git call outside apps/frappe: %s", c)
					}
					if strings.Contains(c, " fetch") || strings.Contains(c, " pull") || strings.Contains(c, " ls-remote") || strings.Contains(c, " clone") {
						t.Errorf("git touched the network: %s", c)
					}
				}
			})
		}
	}
}

// An app directory with no .git inside a parent Git repository is not refused,
// and no git call ever resolves to the parent repository.
func TestParentRepositoryIsIgnored(t *testing.T) {
	h := newBenchH(t)
	h.seed("kb_cheque", "v1.0.0", true)
	// the BENCH is a Git repository whose origin is frappe/frappe
	testutil.StockClone(t, h.root, "https://github.com/frappe/frappe.git")
	// apps/frappe has a .git of its own pointing at the KB fork, so requireKBFrappe consults git
	must(t, os.RemoveAll(h.appDir("frappe")))
	testutil.StockClone(t, h.appDir("frappe"), "https://github.com/KB-Developpement/kb_frappe.git")
	h.seed("kb_cheque", "v1.0.0", true)
	_ = os.Remove(h.gitLog)

	if out, err := h.exec("upgrade", "--apps", "kb_cheque"); err != nil {
		t.Fatalf("upgrade in a bench under Git: %v\n%s", err, out)
	}
	if out, err := h.exec("add", "--apps", "kb_pro"); err != nil {
		t.Fatalf("add in a bench under Git: %v\n%s", err, out)
	}
	calls := h.gitCalls()
	if len(calls) == 0 {
		t.Fatal("expected the origin check to run git against apps/frappe")
	}
	for _, c := range calls {
		if !strings.Contains(c, "--git-dir="+h.appDir("frappe")+"/.git") || strings.Contains(c, "--git-dir="+h.root+"/.git") {
			t.Errorf("git call not pinned to the app's own .git: %s", c)
		}
	}
}
