package cli

import (
	"errors"
	"os"
	"testing"
)

// withDevNullStdout silences printSummary output during tests.
func withDevNullStdout(t *testing.T) {
	t.Helper()
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = devNull
	t.Cleanup(func() {
		os.Stdout = orig
		devNull.Close()
	})
}

func TestPrintSummaryReturnsFailureCount(t *testing.T) {
	withDevNullStdout(t)

	cases := []struct {
		name    string
		results []installResult
		want    int
	}{
		{"empty", nil, 0},
		{"all ok", []installResult{{"a", nil}, {"b", nil}}, 0},
		{"one failure", []installResult{{"a", nil}, {"b", errors.New("boom")}}, 1},
		{"all failed", []installResult{{"a", errors.New("boom")}}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := printSummary(tc.results); got != tc.want {
				t.Fatalf("printSummary = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSummaryError(t *testing.T) {
	if err := summaryError(0, 3); err != nil {
		t.Fatalf("summaryError(0, 3) = %v, want nil", err)
	}
	err := summaryError(1, 1)
	if err == nil {
		t.Fatal("summaryError(1, 1) = nil, want error")
	}
	if got, want := err.Error(), "1 of 1 app(s) failed"; got != want {
		t.Fatalf("summaryError(1, 1) = %q, want %q", got, want)
	}
	if got, want := summaryError(2, 5).Error(), "2 of 5 app(s) failed"; got != want {
		t.Fatalf("summaryError(2, 5) = %q, want %q", got, want)
	}
}

// TestDevServerAction pins the decision table behind maybeRestartDevServer.
// The case that matters is (runningNow=false, wasRunning=true): replacing
// apps/<app> on disk crashes the werkzeug reloader inside `bench serve`, honcho
// exits because a child died, and the bench is left silently down. Before the
// fix that case did nothing at all.
func TestDevServerAction(t *testing.T) {
	tests := []struct {
		name                               string
		runningNow, wasRunning, prodRunnig bool
		want                               string
	}{
		{"still running after the update", true, true, false, devActionRestart},
		{"started by someone else mid-run", true, false, false, devActionRestart},
		{"killed by the update", false, true, false, devActionStart},
		{"killed by the update, prod also seen", false, true, true, devActionStart},
		{"prod bench", false, false, true, devActionWarn},
		{"nothing running", false, false, false, devActionNone},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := devServerAction(tc.runningNow, tc.wasRunning, tc.prodRunnig); got != tc.want {
				t.Fatalf("devServerAction(%v, %v, %v) = %q, want %q",
					tc.runningNow, tc.wasRunning, tc.prodRunnig, got, tc.want)
			}
		})
	}
}
