package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/huh"

	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
	"github.com/KB-Developpement/kb_pro_cli/internal/version"
)

func optionValues(opts []huh.Option[string]) []string {
	var out []string
	for _, o := range opts {
		out = append(out, o.Value)
	}
	return out
}

func TestMainMenuOptions(t *testing.T) {
	tests := []struct {
		name  string
		state menuState
		want  []string
	}{
		{"stock frappe", menuState{Stock: true}, []string{
			menuInitKBFrappe, menuStatus, menuLicense, menuUpdate, menuSettings,
		}},
		{"full", menuState{}, []string{
			menuInstall, menuAdd, menuSiteInstall, menuManage, menuUpgrade,
			menuCheckUpdates, menuUpgradeTo, menuStatus, menuAdopt, menuLicense, menuUpdate, menuSettings,
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := mainMenuOptions(tc.state)
			if len(opts) == 0 {
				t.Fatal("empty option list: huh panics on a Select without options")
			}
			got := optionValues(opts)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("values = %v, want %v", got, tc.want)
			}
			seen := map[string]bool{}
			dash := -1
			for _, o := range opts {
				if seen[o.Value] {
					t.Errorf("duplicate value %q", o.Value)
				}
				seen[o.Value] = true
				col := strings.Index(o.Key, "—")
				if col < 0 {
					t.Errorf("label %q has no dash", o.Key)
					continue
				}
				// Compare rune columns: the labels are ASCII before the dash.
				if dash == -1 {
					dash = col
				} else if col != dash {
					t.Errorf("label %q: dash at %d, others at %d", o.Key, col, dash)
				}
			}
		})
	}
}

// Labels that existed before the Status / Adopt / Update kb items keep their
// exact text.
func TestMainMenuOptionsKeepExistingLabels(t *testing.T) {
	existing := map[string]string{
		menuInitKBFrappe: "Init KB Frappe        — replace stock Frappe with KB fork",
		menuInstall:      "Install apps          — download and install on this site",
		menuAdd:          "Add apps to bench     — download only, skip site install",
		menuSiteInstall:  "Site-install apps     — install already-downloaded apps on site",
		menuManage:       "Manage apps           — uninstall / remove from bench",
		menuUpgrade:      "Upgrade apps          — pull latest changes and migrate",
		menuLicense:      "License               — status, activate, deactivate locally",
		menuSettings:     "Settings              — license server URL, GitHub token",
	}
	for _, st := range []menuState{{Stock: true}, {}} {
		for _, o := range mainMenuOptions(st) {
			if want, ok := existing[o.Value]; ok && o.Key != want {
				t.Errorf("stock=%v %s: label %q, want %q", st.Stock, o.Value, o.Key, want)
			}
		}
	}
}

func TestMenuHeader(t *testing.T) {
	exp := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	active := &license.State{Valid: true, Tier: "pro", ExpiresAt: exp}
	tests := []struct {
		name  string
		state menuHeaderState
		want  []headerLine
	}{
		{"clean", menuHeaderState{Version: "0.9.0", License: active, Root: "/b", Site: "s1"}, []headerLine{
			{Text: "kb 0.9.0 · License: active (pro, token valid until 2026-12-01)"},
			{Text: "Bench: /b · Site: s1"},
		}},
		{"no license", menuHeaderState{Version: "dev", Root: "/b", Site: "s1"}, []headerLine{
			{Text: "kb dev · License: not activated"},
			{Text: "Bench: /b · Site: s1"},
		}},
		{"expired", menuHeaderState{Version: "dev", License: &license.State{Tier: "pro", ExpiresAt: exp}, Root: "/b", Site: "s1"}, []headerLine{
			{Text: "kb dev · License: token expired 2026-12-01"},
			{Text: "Bench: /b · Site: s1"},
		}},
		{"all warnings", menuHeaderState{
			Version: "0.9.0", License: active, Root: "/b", Site: "s1",
			Journal: "Unfinished upgrade of kb_pro (step swapped)", LockPID: 42, NewerKB: "0.10.0",
		}, []headerLine{
			{Text: "kb 0.9.0 · License: active (pro, token valid until 2026-12-01)"},
			{Text: "Bench: /b · Site: s1"},
			{Text: "Unfinished upgrade of kb_pro (step swapped)", Warn: true},
			{Text: "Lock: .kb/lock names PID 42 (running) — another kb operation may be in progress", Warn: true},
			{Text: "Newer kb available: 0.10.0 (you have 0.9.0) — choose Update kb", Warn: true},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := menuHeader(tc.state)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d lines %v, want %d", len(got), got, len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("line %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestJournalWarning(t *testing.T) {
	pending := &kbstate.Journal{Command: "upgrade", App: "kb_pro", Step: kbstate.StepSwapped}
	done := &kbstate.Journal{Command: "upgrade", App: "kb_pro", Step: kbstate.StepFinalized}
	tests := []struct {
		name string
		js   kbstate.JournalState
		want string // substring; "" means no warning
	}{
		{"missing", kbstate.JournalState{Missing: true}, ""},
		{"finalized", kbstate.JournalState{Journal: done}, ""},
		{"pending", kbstate.JournalState{Journal: pending}, "Unfinished upgrade of kb_pro (step swapped)"},
		{"malformed", kbstate.JournalState{Malformed: "not valid JSON"}, "Journal is unusable (not valid JSON)"},
		{"newer", kbstate.JournalState{Newer: &kbstate.SchemaError{Found: 9, Supported: 1}}, "newer kb"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := journalWarning(tc.js)
			if tc.want == "" && got != "" || !strings.Contains(got, tc.want) {
				t.Errorf("journalWarning = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

func TestLockHolder(t *testing.T) {
	alive := func(int) bool { return true }
	dead := func(int) bool { return false }
	tests := []struct {
		name  string
		pid   int
		ok    bool
		self  int
		alive func(int) bool
		want  int
	}{
		{"no pid recorded", 0, false, 10, alive, 0},
		{"live other process", 42, true, 10, alive, 42},
		{"dead process", 42, true, 10, dead, 0},
		{"own pid after a menu action", 10, true, 10, alive, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := lockHolder(tc.pid, tc.ok, tc.self, tc.alive); got != tc.want {
				t.Errorf("lockHolder = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestNewerKnown(t *testing.T) {
	tests := []struct{ current, latest, want string }{
		{"v0.9.0", "v0.10.0", "v0.10.0"},
		{"0.9.0", "v0.10.0", "v0.10.0"},
		{"v0.10.0", "v0.10.0", ""},
		{"v0.11.0", "v0.10.0", ""},
		{"dev", "v9.9.9", ""},
		{"", "v9.9.9", ""},
		{"v0.9.0", "", ""},
	}
	for _, tc := range tests {
		if got := newerKnown(tc.current, tc.latest); got != tc.want {
			t.Errorf("newerKnown(%q, %q) = %q, want %q", tc.current, tc.latest, got, tc.want)
		}
	}
}

func TestAdoptOffer(t *testing.T) {
	would := "kb_pro: would adopt v1.2.0 (commit abc, 12 files compared)\n"
	tests := []struct {
		name string
		out  string
		err  error
		want bool
	}{
		{"would adopt, clean", would, nil, true},
		{"nothing to adopt", "kb_pro: skipped — already has a installed receipt (v1.2.0)\n", nil, false},
		{"empty output", "", nil, false},
		{"partial mismatch", would, &ExitError{Code: adoptMismatch, Err: errors.New("x")}, true},
		{"partial ambiguous", would, &ExitError{Code: adoptAmbiguous, Err: errors.New("x")}, true},
		{"refused", would, &ExitError{Code: adoptRefused, Err: errors.New("x")}, false},
		{"plain error", would, errors.New("x"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := adoptOffer(tc.out, tc.err); got != tc.want {
				t.Errorf("adoptOffer = %v, want %v", got, tc.want)
			}
		})
	}
}

// collectMenuHeader reads only local files: a pending journal, a lock named by
// another live process and a cached newer release show up, and nothing is
// requested from the network.
func TestCollectMenuHeaderLocalOnly(t *testing.T) {
	h := newBenchH(t)
	j := kbstate.NewJournal(h.root)
	j.Op, j.Command, j.App, j.Directory = kbstate.OpSwap, "upgrade", "kb_pro", "kb_pro"
	must(t, j.Advance(kbstate.StepSwapped))
	pid := h.holdLock()
	write(t, filepath.Join(h.home, ".config", "kb", ".update_check.json"),
		`{"checked_at":"2026-10-18T00:00:00Z","latest":"v9.9.9"}`)
	old := version.Version
	version.Version = "v0.1.0"
	t.Cleanup(func() { version.Version = old })

	s := collectMenuHeader("site1")
	if s.Root != h.root || s.Site != "site1" || s.Version != "v0.1.0" {
		t.Errorf("identity fields = %+v", s)
	}
	if !strings.Contains(s.Journal, "Unfinished upgrade of kb_pro") {
		t.Errorf("Journal = %q", s.Journal)
	}
	if s.LockPID != pid {
		t.Errorf("LockPID = %d, want %d", s.LockPID, pid)
	}
	if s.NewerKB != "v9.9.9" {
		t.Errorf("NewerKB = %q", s.NewerKB)
	}
	if n := h.srv.requestCount(); n != 0 {
		t.Errorf("building the header made %d requests to the license server", n)
	}
}

// After kb itself took and released the lock the file still names our own PID;
// that must not be reported as a held lock.
func TestCollectMenuHeaderIgnoresOwnLockPID(t *testing.T) {
	h := newBenchH(t)
	lock, err := kbstate.AcquireLock(h.root)
	must(t, err)
	lock.Release()
	if pid, ok := kbstate.HolderPID(h.root); !ok || pid != os.Getpid() {
		t.Fatalf("lock file names %d (ok=%v), want our PID", pid, ok)
	}
	if got := collectMenuHeader("site1").LockPID; got != 0 {
		t.Errorf("LockPID = %d, want 0", got)
	}
}

// The header is styled output: through a pipe it carries no ANSI.
func TestPrintMenuHeaderNoANSIOnPipe(t *testing.T) {
	h := newBenchH(t)
	j := kbstate.NewJournal(h.root)
	j.Op, j.Command, j.App, j.Directory = kbstate.OpSwap, "upgrade", "kb_pro", "kb_pro"
	must(t, j.Advance(kbstate.StepSwapped))

	out := capture(t, func() { printMenuHeader("site1") })
	if strings.Contains(out, "\x1b") {
		t.Errorf("header leaks ANSI into a pipe: %q", out)
	}
	for _, want := range []string{"Bench: " + h.root, "Site: site1", "Unfinished upgrade of kb_pro"} {
		if !strings.Contains(out, want) {
			t.Errorf("header lacks %q:\n%s", want, out)
		}
	}
}
