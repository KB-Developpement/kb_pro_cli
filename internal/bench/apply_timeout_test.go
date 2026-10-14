//go:build !windows

package bench

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
	"github.com/KB-Developpement/kb_pro_cli/internal/testutil"
)

func installMigrationBench(t *testing.T, body string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "bench"), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
}

func TestUpgradeMigrationSurvivesPreparationExpiry(t *testing.T) {
	f := newFixture(t)
	installMigrationBench(t, "printf 'migration completed\\n'\n")
	prep, cancel := context.WithCancel(context.Background())
	cancel()
	steps := defaultSteps(prep, context.Background(), f.root, testApp, CmdUpgrade)
	out, err := steps.Migrate()
	if err != nil || out != "migration completed" {
		t.Fatalf("preparation deadline shortened migration: %v; output %q", err, out)
	}
}

func TestUpgradePreparationStillUsesItsOwnDeadline(t *testing.T) {
	f := newFixture(t)
	installMigrationBench(t, "printf 'must not start\n'\n")
	prep, cancel := context.WithCancel(context.Background())
	cancel()
	steps := defaultSteps(prep, context.Background(), f.root, testApp, CmdUpgrade)
	if _, err := steps.SetupRequirements(); !errors.Is(err, context.Canceled) {
		t.Fatalf("preparation ignored its canceled budget: %v", err)
	}
}

func TestUpgradeMigrationCallerCancellationPreventsLaunch(t *testing.T) {
	f := newFixture(t)
	installMigrationBench(t, "printf 'must not start\n'\n")
	caller, cancel := context.WithCancel(context.Background())
	prep, prepCancel := context.WithCancel(caller)
	defer prepCancel()
	cancel()
	steps := defaultSteps(prep, caller, f.root, testApp, CmdUpgrade)
	out, err := steps.Migrate()
	if !errors.Is(err, context.Canceled) || out != "" {
		t.Fatalf("migration ignored caller cancellation: %v, %q", err, out)
	}
}

func TestUpgradeFinalizesAfterPreparationBudgetEnds(t *testing.T) {
	f := newFixture(t)
	f.writeApp("kb_pro", map[string]string{"marker.txt": "OLD"})
	f.seedReceipt("kb_pro")
	installMigrationBench(t, "printf 'migration completed\n'\n")
	caller := context.Background()
	prep, cancel := context.WithCancel(caller)
	defer cancel()
	steps := upgradeSteps(&calls{})
	build := steps.Build
	steps.Build = func() (string, error) {
		out, err := build()
		cancel()
		return out, err
	}
	steps.Migrate = defaultSteps(prep, caller, f.root, testApp, CmdUpgrade).Migrate
	_, err := applyWithSteps(f.root, ApplyPlan{Command: CmdUpgrade, App: testApp, Download: f.download(map[string]string{"marker.txt": "NEW"})}, steps)
	if err != nil {
		t.Fatal(err)
	}
	if j := f.journal(); j.Pending() || j.Step != kbstate.StepFinalized {
		t.Fatalf("successful independent migration did not finalize: %+v", j)
	}
	if rs := kbstate.ReadReceipt(f.root, "kb_pro"); !rs.Valid() || rs.Receipt.Ref != "v1.2.0" {
		t.Fatal("successful independent migration did not finalize its new receipt")
	}
}

func TestMigrationDeadlineAndCancellationRetainRecoveryEvidence(t *testing.T) {
	for _, kind := range []string{"deadline", "cancellation"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.writeApp("kb_pro", map[string]string{"marker.txt": "OLD"})
			oldDigest := testutil.TreeDigest(t, f.appDir("kb_pro"))
			oldReceipt := f.seedReceipt("kb_pro")
			installMigrationBench(t, "printf 'migration started\n'\nexec /bin/sleep 30\n")
			steps := upgradeSteps(&calls{})
			want := context.DeadlineExceeded
			steps.Migrate = func() (string, error) {
				var caller context.Context
				var cancel context.CancelFunc
				if kind == "deadline" {
					caller, cancel = context.WithTimeout(context.Background(), time.Second)
				} else {
					caller, cancel = context.WithCancel(context.Background())
					timer := time.AfterFunc(time.Second, cancel)
					defer timer.Stop()
				}
				defer cancel()
				return defaultSteps(caller, caller, f.root, testApp, CmdUpgrade).Migrate()
			}
			if kind == "cancellation" {
				want = context.Canceled
			}
			start := time.Now()
			res, err := applyWithSteps(f.root, ApplyPlan{Command: CmdUpgrade, App: testApp, Download: f.download(map[string]string{"marker.txt": "NEW"})}, steps)
			if !errors.Is(err, ErrJournalPending) || !errors.Is(err, want) {
				t.Fatalf("migration cancellation lost pending/cause identity: %v", err)
			}
			if time.Since(start) > 5*time.Second || !strings.Contains(res.Output, "migration started") {
				t.Fatal("native migration did not launch and stop under caller cancellation")
			}
			if j := f.journal(); !j.Pending() || j.Step != kbstate.StepSwapped || j.Aborted {
				t.Fatalf("failed migration changed its pending boundary: %+v", j)
			}
			if got := read(t, filepath.Join(f.appDir("kb_pro"), "marker.txt")); got != "NEW" {
				t.Fatal("failed migration rolled back the live source")
			}
			if testutil.TreeDigest(t, f.appDir("kb_pro.kb-old")) != oldDigest {
				t.Fatal("failed migration changed the retained old source")
			}
			if !bytes.Equal(testutil.MustRead(t, kbstate.ReceiptPath(f.root, "kb_pro")), oldReceipt) {
				t.Fatal("failed migration replaced the previous receipt")
			}
			before := testutil.BenchDigest(t, f.root)
			_, recoverErr := RecoverJournal(context.Background(), RecoverMutate)
			if !errors.Is(recoverErr, ErrJournalPending) || testutil.BenchDigest(t, f.root) != before {
				t.Fatal("next mutation did not refuse without changing recovery evidence")
			}
		})
	}
}
