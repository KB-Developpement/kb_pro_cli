//go:build kbfault && !windows

package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/KB-Developpement/kb_pro_cli/internal/apps"
	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
	"github.com/KB-Developpement/kb_pro_cli/internal/testutil"
)

// TestFaultHelper is the body of the child process the kill-point tests start:
// it runs one kb command in-process, with the license seams stubbed, and dies
// by SIGKILL at the kill point named in KB_FAULT. It does nothing otherwise.
func TestFaultHelper(t *testing.T) {
	args := os.Getenv("KB_TEST_H_ARGS")
	if args == "" {
		t.Skip("helper process only")
	}
	syncLicenseFn = func(context.Context) error { return nil }
	allowedSetFn = func() map[string]bool {
		m := map[string]bool{apps.Framework.Name: true}
		for _, a := range apps.All {
			m[a.Name] = true
		}
		return m
	}
	cachedTokenFn = func() (string, error) { return "tok", nil }
	globalFlags.NoInput = true
	root := newRootCmd()
	root.SetArgs(append(strings.Split(args, "\x1f"), "--no-input"))
	err := root.Execute()
	if err != nil {
		os.Stderr.WriteString("helper: " + err.Error() + "\n")
		os.Exit(1)
	}
	os.Exit(0) // the kill point was never reached
}

// killAt runs `kb <args>` in a child that is SIGKILLed at point, and returns
// once it is gone. It fails the test if the child was not killed by SIGKILL.
func killAt(t *testing.T, point string, args ...string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestFaultHelper$")
	cmd.Env = append(os.Environ(), "KB_FAULT="+point, "KB_TEST_H_ARGS="+strings.Join(args, "\x1f"))
	out, err := cmd.CombinedOutput()
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("child for %q at %s was not killed: err=%v\n%s", args, point, err, out)
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("child for %q at %s exited %v, want SIGKILL\n%s", args, point, ee, out)
	}
}

type faultCase struct {
	name  string
	args  []string
	prep  func(t *testing.T, h *benchH)
	dir   string
	steps []string
}

func faultCases() []faultCase {
	std := []string{"planned", "downloaded", "staged", "swapped", "finalized"}
	withMigrate := []string{"planned", "downloaded", "staged", "swapped", "migrated", "finalized"}
	return []faultCase{
		{"add", []string{"add", "--apps", "kb_pro"}, nil, "kb_pro", std},
		{"install", []string{"install", "--apps", "kb_pro"}, nil, "kb_pro", std},
		{"init-kb-frappe", []string{"init-kb-frappe"}, stockFrappe, "frappe", std},
		{"upgrade", []string{"upgrade", "--apps", "kb_cheque"}, func(t *testing.T, h *benchH) { h.seed("kb_cheque", "v1.0.0", true) }, "kb_cheque", withMigrate},
	}
}

// A fault hook stops the CLI after each step; at each stop the journal holds the
// expected step. Upgrade has a migrated step, the others do not.
func TestJournalStepAtEachStop(t *testing.T) {
	for _, c := range faultCases() {
		for _, step := range c.steps {
			t.Run(c.name+"/"+step, func(t *testing.T) {
				h := newBenchH(t)
				if c.prep != nil {
					c.prep(t, h)
				}
				killAt(t, "after-"+step, c.args...)
				j := h.journal()
				if j.Step != step {
					t.Fatalf("journal step = %q after a kill at after-%s", j.Step, step)
				}
				if step == "finalized" && j.Pending() {
					t.Error("finalized journal reports pending")
				}
				if step != "finalized" && !j.Pending() {
					t.Error("journal not pending mid-transaction")
				}
				if j.Command != strings.TrimSuffix(c.args[0], "") || j.Directory != c.dir {
					t.Errorf("journal = %+v", j)
				}
				if step == "finalized" && !h.receipt(c.dir).Valid() {
					t.Error("no receipt although the journal reached finalized")
				}
				if step != "finalized" && step != "migrated" && c.name != "upgrade" && h.receipt(c.dir).Valid() && c.name != "init-kb-frappe" {
					t.Errorf("receipt exists before finalization at step %s", step)
				}
			})
		}
	}
	t.Run("a complete run ends at finalized", func(t *testing.T) {
		h := newBenchH(t)
		if out, err := h.exec("add", "--apps", "kb_pro"); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if j := h.journal(); j.Step != "finalized" {
			t.Fatalf("journal step = %s", j.Step)
		}
	})
}

// KP-SWAP-1: killed between renaming the live app directory aside and moving the
// new tree in.
func TestKPSwap1(t *testing.T) {
	h := newBenchH(t)
	h.seed("kb_cheque", "v1.0.0", true)
	pre := h.digest("kb_cheque")

	killAt(t, "KP-SWAP-1", "upgrade", "--apps", "kb_cheque")

	if j := h.journal(); j.Step != kbstate.StepSwapped {
		t.Fatalf("journal step = %s, want swapped", j.Step)
	}
	if _, err := os.Stat(h.appDir("kb_cheque")); err == nil {
		t.Fatal("apps/kb_cheque exists at KP-SWAP-1")
	}
	backup := h.appDir("kb_cheque.kb-old")
	if got := testutil.TreeDigest(t, backup); got != pre {
		t.Fatalf("the backup's digest %s differs from the pre-swap digest %s", got, pre)
	}
	before := h.benchDigest()
	for _, args := range [][]string{{"add", "--apps", "kb_pro"}, {"upgrade", "--apps", "kb_facilite"}, {"init-kb-frappe", "--force"}} {
		_, err := h.exec(args...)
		requireErr(t, err, kbstate.JournalPath(h.root), backup)
	}
	if h.benchDigest() != before {
		t.Error("a refused command changed the bench")
	}
	must(t, os.Rename(backup, h.appDir("kb_cheque")))
	if got := h.digest("kb_cheque"); got != pre {
		t.Fatalf("restoring the backup did not restore the pre-swap digest")
	}
}

func TestRecoveryAfterKills(t *testing.T) {
	t.Run("killed before the swap: the next add discards only the recorded paths", func(t *testing.T) {
		for _, step := range []string{"planned", "downloaded", "staged"} {
			h := newBenchH(t)
			decoy := h.appDir("kb_pro.kb-new-decoy")
			write(t, filepath.Join(decoy, "x"), "not recorded")
			decoyDigest := testutil.TreeDigest(t, decoy)
			killAt(t, "after-"+step, "add", "--apps", "kb_pro")
			j := h.journal()
			var archives []string
			for _, p := range j.Paths {
				if strings.Contains(p, "kb-app-") {
					archives = append(archives, p)
					if _, err := os.Stat(p); err != nil {
						t.Fatalf("step %s: archive %s missing", step, p)
					}
				}
			}
			if len(archives) != 1 {
				t.Fatalf("step %s: journal paths %v", step, j.Paths)
			}
			out, err := h.exec("add", "--apps", "kb_pro")
			if err != nil {
				t.Fatalf("step %s: %v\n%s", step, err, out)
			}
			if _, err := os.Stat(archives[0]); err == nil {
				t.Errorf("step %s: the recorded archive was not discarded", step)
			}
			if testutil.TreeDigest(t, decoy) != decoyDigest {
				t.Errorf("step %s: an unrecorded directory was touched", step)
			}
			if !h.receipt("kb_pro").Valid() {
				t.Errorf("step %s: the add did not complete", step)
			}
			if m, _ := filepath.Glob(filepath.Join(h.root, ".kb", "recovery", "legacy", "*")); len(m) != 0 {
				t.Errorf("step %s: a recorded staging path was moved to legacy instead of discarded: %v", step, m)
			}
		}
	})
	t.Run("killed right after the journal says swapped, before any rename: the next command refuses", func(t *testing.T) {
		h := newBenchH(t)
		killAt(t, "after-swapped", "add", "--apps", "kb_pro")
		if _, err := os.Stat(h.appDir("kb_pro")); err == nil {
			t.Fatal("apps/kb_pro exists before the move-in")
		}
		before := h.benchDigest()
		_, err := h.exec("add", "--apps", "kb_compta")
		requireErr(t, err, kbstate.JournalPath(h.root), h.appDir("kb_pro"), h.appDir("kb_pro.kb-new"), "missing")
		if h.benchDigest() != before {
			t.Error("the bench changed")
		}
	})
	t.Run("killed after the move-in of an add: the next command replays registration and finalizes", func(t *testing.T) {
		h := newBenchH(t)
		killAt(t, "after-move-in", "add", "--apps", "kb_pro")
		if j := h.journal(); j.Step != kbstate.StepSwapped {
			t.Fatalf("journal step = %s", j.Step)
		}
		if h.receipt("kb_pro").Valid() {
			t.Fatal("receipt before finalization")
		}
		before := len(h.benchLogLines())
		if out, err := h.exec("add", "--apps", "kb_compta"); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if !h.receipt("kb_pro").Valid() || !h.receipt("kb_compta").Valid() {
			t.Fatal("replay or the new add did not complete")
		}
		replayed := strings.Join(h.benchLogLines()[before:], "\n")
		if !strings.Contains(replayed, "setup requirements --python kb_pro") || !strings.Contains(replayed, "build --app kb_pro") {
			t.Errorf("registration was not re-run for kb_pro:\n%s", replayed)
		}
		count := 0
		for _, l := range h.appsTxt() {
			if l == "kb_pro" {
				count++
			}
		}
		if count != 1 {
			t.Errorf("apps.txt lists kb_pro %d times", count)
		}
	})
	t.Run("killed after migrate of an upgrade: the next command finalizes", func(t *testing.T) {
		h := newBenchH(t)
		h.seed("kb_cheque", "v1.0.0", true)
		killAt(t, "after-migrated", "upgrade", "--apps", "kb_cheque")
		if rs := h.receipt("kb_cheque"); rs.Receipt == nil || rs.Receipt.Ref != "v1.0.0" {
			t.Fatal("the receipt changed before finalization")
		}
		if out, err := h.exec("site-install", "--apps", "kb_cheque"); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if rs := h.receipt("kb_cheque"); rs.Receipt == nil || rs.Receipt.Ref != "v1.2.0" {
			t.Fatalf("receipt after the replay: %+v", rs.Receipt)
		}
		if _, err := os.Stat(h.appDir("kb_cheque.kb-old")); err == nil {
			t.Error("the old tree was not retired by the replay")
		}
	})
	t.Run("killed after the move-in of an upgrade: the next command refuses with the paths", func(t *testing.T) {
		h := newBenchH(t)
		h.seed("kb_cheque", "v1.0.0", true)
		killAt(t, "after-move-in", "upgrade", "--apps", "kb_cheque")
		before := h.benchDigest()
		_, err := h.exec("add", "--apps", "kb_pro")
		requireErr(t, err, kbstate.JournalPath(h.root), h.appDir("kb_cheque.kb-old"), h.appDir("kb_cheque"))
		if h.benchDigest() != before {
			t.Error("the bench changed")
		}
	})
	t.Run("killed after the move-in of init-kb-frappe: replay re-runs registration, build and migrate", func(t *testing.T) {
		h := newBenchH(t)
		stockFrappe(t, h)
		killAt(t, "after-move-in", "init-kb-frappe")
		before := len(h.benchLogLines())
		if out, err := h.exec("site-install", "--apps", "kb_cheque"); err != nil && !strings.Contains(err.Error(), "not available") {
			t.Fatalf("%v\n%s", err, out)
		}
		replayed := strings.Join(h.benchLogLines()[before:], "\n")
		for _, want := range []string{"setup requirements --python frappe", "build --app frappe", "migrate"} {
			if !strings.Contains(replayed, want) {
				t.Errorf("replay did not run %q:\n%s", want, replayed)
			}
		}
		if rs := h.receipt("frappe"); !rs.Valid() || rs.Receipt.App != "kb_frappe" {
			t.Errorf("framework receipt after the replay: %+v", rs.Receipt)
		}
		if _, err := os.Stat(h.appDir("frappe.kb-old")); err == nil {
			t.Error("the stock tree was kept")
		}
	})
}
