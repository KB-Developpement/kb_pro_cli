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
