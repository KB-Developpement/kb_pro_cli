package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
	"github.com/KB-Developpement/kb_pro_cli/internal/testutil"
)

func TestStatusReportsProvenanceJournalAndRetainedCopies(t *testing.T) {
	h := newBenchH(t)
	h.seed("kb_pro", "v1.2.0", true)
	h.seed("kb_compta", "v1.1.0", false)
	h.seed("kb_cheque", "v1.0.0", false)
	h.writeReceipt("kb_cheque", "v1.0.0", kbstate.ProvAdopted)
	// a retained leftover and a pre-receipt copy, with known sizes
	legacy := filepath.Join(kbstate.LegacyDir(h.root), "kb_pro-kb-old-20261008T100000Z")
	pre := filepath.Join(kbstate.RecoveryDir(h.root), "kb_compta-pre-receipt-20261008T100000Z")
	write(t, filepath.Join(legacy, "f.bin"), strings.Repeat("x", 2048))
	write(t, filepath.Join(pre, "f.bin"), strings.Repeat("y", 10))
	// a pending journal
	j := kbstate.NewJournal(h.root)
	j.Op, j.Command, j.App, j.Directory = kbstate.OpSwap, "upgrade", "kb_pro", "kb_pro"
	must(t, j.Advance(kbstate.StepSwapped))
	// a Git checkout
	h.seed("kb_stock", "v1.0.0", false)
	must(t, os.MkdirAll(filepath.Join(h.appDir("kb_stock"), ".git"), 0o755))

	pid := h.holdLock()
	before := h.benchDigest()
	kbBefore := testutil.TreeDigestExcluding(t, kbstate.Dir(h.root), func(rel string) bool { return rel == "lock" })

	out, err := h.exec("status")
	if err != nil {
		t.Fatalf("status with the lock held: %v\n%s", err, out)
	}
	for _, want := range []string{
		"Bench: " + h.root,
		"PID " + itoa(pid),
		"PENDING upgrade of kb_pro at step swapped",
		legacy, pre,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	line := func(dir string) string {
		for _, l := range strings.Split(out, "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), dir+" ") {
				return l
			}
		}
		return ""
	}
	if l := line("kb_pro"); !regexp.MustCompile(`installed\s+tag v1\.2\.0\s+commit ` + commitFor("kb_pro", "v1.2.0")).MatchString(l) {
		t.Errorf("kb_pro line: %q", l)
	}
	if l := line("kb_cheque"); !strings.Contains(l, "adopted") || !strings.Contains(l, commitFor("kb_cheque", "v1.0.0")) {
		t.Errorf("kb_cheque line: %q", l)
	}
	if l := line("kb_compta"); !strings.Contains(l, "unknown") || !strings.Contains(l, "no receipt") {
		t.Errorf("kb_compta line: %q", l)
	}
	if l := line("kb_stock"); !strings.Contains(l, "[Git checkout]") {
		t.Errorf("kb_stock line: %q", l)
	}
	if !regexp.MustCompile(regexp.QuoteMeta(legacy) + `\s+2\.0 KiB`).MatchString(out) {
		t.Errorf("legacy copy size not shown:\n%s", out)
	}
	if !regexp.MustCompile(regexp.QuoteMeta(pre) + `\s+10 B`).MatchString(out) {
		t.Errorf("pre-receipt copy size not shown:\n%s", out)
	}
	if h.benchDigest() != before || testutil.TreeDigestExcluding(t, kbstate.Dir(h.root), func(rel string) bool { return rel == "lock" }) != kbBefore {
		t.Error("status changed the bench or .kb")
	}
	if n := h.srv.requestCount(); n != 0 {
		t.Errorf("status sent %d HTTP requests", n)
	}
}

func TestStatusWithTheLicenseServerStoppedAndNoKBDirectory(t *testing.T) {
	h := newBenchH(t)
	must(t, os.RemoveAll(kbstate.Dir(h.root)))
	h.srv.srv.Close() // the license server is down
	before := h.benchDigest()
	out, err := h.exec("status")
	if err != nil {
		t.Fatalf("status with the server stopped: %v\n%s", err, out)
	}
	if h.benchDigest() != before {
		t.Error("bench changed")
	}
	if _, err := os.Stat(kbstate.Dir(h.root)); err == nil {
		t.Error("status created .kb")
	}
	if !strings.Contains(out, "Journal: none") || !strings.Contains(out, "Lock: no holder recorded") {
		t.Errorf("output:\n%s", out)
	}
	if n := h.srv.requestCount(); n != 0 {
		t.Errorf("status sent %d HTTP requests", n)
	}
}

func TestStatusHasNoStartupNetworkHooks(t *testing.T) {
	cmd := newStatusCmd()
	if cmd.Annotations["skipChecks"] != "true" || cmd.Annotations["skipLicenseCheck"] != "true" {
		t.Fatalf("status annotations = %v", cmd.Annotations)
	}
}

func TestStatusMissingBench(t *testing.T) {
	h := newBenchH(t)
	t.Setenv("KB_BENCH_ROOT", filepath.Join(h.root, "nope"))
	if _, err := h.exec("status"); err == nil {
		t.Fatal("status on a missing bench succeeded")
	}
}
