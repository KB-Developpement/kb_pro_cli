package cli

import (
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/KB-Developpement/kb_pro_cli/internal/apps"
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

// TestPlanInstall pins the decision table behind "kb install".
//
// The case that matters is "already downloaded into the bench": runInstall used
// to drop those apps from its selectable list, so on a bench where kb_pro sat
// in apps/ but was not installed on the site,
// `kb install --no-input --apps kb_pro` died with
// `app "kb_pro" is not available (not licensed, already in bench/installed, or
// unknown)` instead of just running bench install-app. "kb install" means
// "download if needed, then install on this site".
func TestPlanInstall(t *testing.T) {
	all := []apps.App{
		{Name: "kb_pro"},
		{Name: "kb_compta"},
		{Name: "kb_stock"},
		{Name: "kb_print"},
	}
	allowed := map[string]bool{"kb_pro": true, "kb_compta": true, "kb_stock": true}
	inBench := map[string]bool{"kb_pro": true, "kb_stock": true}
	installed := map[string]bool{"kb_stock": true}

	tests := []struct {
		name         string
		preselected  []string
		wantDownload []string
		wantSiteOnly []string
		wantErr      string
	}{
		{
			name:        "unknown name",
			preselected: []string{"not_an_app"},
			wantErr:     `app "not_an_app" is not a KB app`,
		},
		{
			name:        "not in the license",
			preselected: []string{"kb_print"},
			wantErr:     `app "kb_print" is not in your license`,
		},
		{
			name:        "already installed on this site",
			preselected: []string{"kb_stock"},
			wantErr:     `app "kb_stock" is already installed on this site — use: kb upgrade to update it`,
		},
		{
			name:         "already in the bench — site install only",
			preselected:  []string{"kb_pro"},
			wantSiteOnly: []string{"kb_pro"},
		},
		{
			name:         "fresh app — full download",
			preselected:  []string{"kb_compta"},
			wantDownload: []string{"kb_compta"},
		},
		{
			name:         "mixed list keeps both phases and their order",
			preselected:  []string{"kb_compta", "kb_pro"},
			wantDownload: []string{"kb_compta"},
			wantSiteOnly: []string{"kb_pro"},
		},
		{
			name:        "nil preselected (interactive) plans nothing yet",
			preselected: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := planInstall(all, allowed, inBench, installed, tc.preselected)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("planInstall(%v) = no error, want %q", tc.preselected, tc.wantErr)
				}
				if err.Error() != tc.wantErr {
					t.Fatalf("planInstall(%v) error = %q, want %q", tc.preselected, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("planInstall(%v) = %v, want no error", tc.preselected, err)
			}
			if !reflect.DeepEqual(plan.Download, tc.wantDownload) {
				t.Errorf("Download = %v, want %v", plan.Download, tc.wantDownload)
			}
			if !reflect.DeepEqual(plan.SiteInstallOnly, tc.wantSiteOnly) {
				t.Errorf("SiteInstallOnly = %v, want %v", plan.SiteInstallOnly, tc.wantSiteOnly)
			}
		})
	}
}

// TestInstallSelectable_listsBenchPresentApps proves the "kb install" picker no
// longer hides apps that are downloaded but not installed on the site — hiding
// them is what pushed operators onto the --apps path that then rejected them.
func TestInstallSelectable_listsBenchPresentApps(t *testing.T) {
	all := []apps.App{{Name: "kb_pro"}, {Name: "kb_compta"}, {Name: "kb_stock"}, {Name: "kb_print"}}
	allowed := map[string]bool{"kb_pro": true, "kb_compta": true, "kb_stock": true}
	installed := map[string]bool{"kb_stock": true}

	var got []string
	for _, a := range installSelectable(all, allowed, installed) {
		got = append(got, a.Name)
	}
	if want := []string{"kb_pro", "kb_compta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("installSelectable = %v, want %v", got, want)
	}

	if got, want := installOptionLabel("kb_pro", true), "kb_pro  (already downloaded — will install on site)"; got != want {
		t.Errorf("installOptionLabel(kb_pro, inBench) = %q, want %q", got, want)
	}
	if got, want := installOptionLabel("kb_compta", false), "kb_compta"; got != want {
		t.Errorf("installOptionLabel(kb_compta, fresh) = %q, want %q", got, want)
	}
}
