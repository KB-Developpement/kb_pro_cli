//go:build kbfault

package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KB-Developpement/kb_pro_cli/internal/adopt"
	"github.com/KB-Developpement/kb_pro_cli/internal/bench"
	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
	"github.com/KB-Developpement/kb_pro_cli/internal/testutil"
)

func maintenanceFilesDigest(t *testing.T, root string) [32]byte {
	t.Helper()
	m, err := adopt.BuildManifest(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	for p := range m {
		if p == ".git" || strings.HasPrefix(p, ".git/") {
			delete(m, p)
		}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(raw)
}

func TestMaintenanceFaultChild(t *testing.T) {
	mode := os.Getenv("KB_MAINTENANCE_FAULT_CHILD")
	if mode == "" {
		return
	}
	s, err := kbstate.ReadMaintenance(bench.Root())
	if err != nil || s == nil {
		t.Fatalf("read child session: %v", err)
	}
	maintenanceRefresh = func(context.Context) (string, string, string, map[string]bool, error) {
		return "exit-test-token", s.Fingerprint, s.LicenseKeySHA256, map[string]bool{"kb_compta": true}, nil
	}
	switch mode {
	case "attach":
		err = resumeMaintenanceAttach(context.Background(), os.Stdout, bench.Root(), s)
	case "report":
		err = sendMaintenanceReport(context.Background(), os.Stdout, bench.Root(), s)
	case "exit":
		err = maintenanceExit(context.Background(), os.Stdout, bench.Root(), os.Getenv("KB_MAINTENANCE_RELEASE"), "none", true)
	default:
		t.Fatal("unknown maintenance fault child")
	}
	if err != nil {
		t.Fatal(err)
	}
}
func killMaintenanceChild(t *testing.T, mode, point string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestMaintenanceFaultChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "KB_MAINTENANCE_FAULT_CHILD="+mode, "KB_FAULT="+point)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("child did not die at %s: %s", point, out)
	}
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ProcessState.ExitCode() != -1 {
		t.Fatalf("child failed without SIGKILL at %s: %v %s", point, err, out)
	}
}
func maintenanceFaultReportServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Seq int64 `json:"seq"`
		}
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			t.Error(e)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ack_seq": body.Seq, "approval_state": "redeemed", "work_until": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	}))
	t.Cleanup(server.Close)
	t.Setenv("KB_LICENSE_SERVER", server.URL)
	return server
}
func TestMaintenanceAttachKillPreservesSourceAndGitInode(t *testing.T) {
	f := newMaintenanceReportFixture(t, false)
	saveMaintenanceTestCredential(t, f)
	maintenanceFaultReportServer(t)
	s := f.session
	s.SourceState = "archive"
	s.WorkUntil = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	prepared := filepath.Join(kbstate.Dir(f.root), "maintenance", "scratch", "prepared", ".git")
	if err := os.MkdirAll(filepath.Dir(prepared), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(f.appDir, ".git"), prepared); err != nil {
		t.Fatal(err)
	}
	if err := bench.SetPreparedMaintenanceOwner(prepared, s.SessionID); err != nil {
		t.Fatal(err)
	}
	rel, _ := filepath.Rel(f.root, prepared)
	s.Pending = &kbstate.MaintenancePending{Op: "enter", Step: "prepared", StartedAt: time.Now().UTC().Format(time.RFC3339), PreparedGit: rel, Expected: map[string]string{"kb_compta": f.base}}
	if err := kbstate.WriteReceipt(f.root, s.Apps[0].OriginalReceipt); err != nil {
		t.Fatal(err)
	}
	if err := kbstate.WriteMaintenance(f.root, s); err != nil {
		t.Fatal(err)
	}
	before := maintenanceFilesDigest(t, f.appDir)
	killMaintenanceChild(t, "attach", "KP-ATTACH-1")
	gitPath := filepath.Join(f.appDir, ".git")
	inode, err := os.Stat(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := maintenanceFilesDigest(t, f.appDir); got != before {
		t.Fatal("attach kill changed working source")
	}
	s, err = kbstate.ReadMaintenance(f.root)
	if err != nil || s.Pending == nil {
		t.Fatalf("missing attach journal: %v", err)
	}
	var status strings.Builder
	printMaintenanceStatus(&status, f.root)
	if !strings.Contains(status.String(), "recovery_needed") {
		t.Fatal(status.String())
	}
	if err := ordinaryMaintenanceGate(); err == nil {
		t.Fatal("ordinary mutation allowed after attach kill")
	}
	if err := resumeMaintenanceAttach(context.Background(), io.Discard, f.root, s); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(gitPath)
	if err != nil || !os.SameFile(inode, after) {
		t.Fatal("entry replay replaced attached Git inode")
	}
	if got := maintenanceFilesDigest(t, f.appDir); got != before {
		t.Fatal("entry replay changed source")
	}
	rs := kbstate.ReadReceipt(f.root, "kb_compta")
	if !rs.Valid() || rs.Receipt.State != "active" {
		t.Fatal("receipt did not complete")
	}
}
func TestMaintenanceReportKillPersistsExactNextSequence(t *testing.T) {
	f := newMaintenanceReportFixture(t, true)
	saveMaintenanceTestCredential(t, f)
	killMaintenanceChild(t, "report", "KP-REPORT-0")
	s, err := kbstate.ReadMaintenance(f.root)
	if err != nil || s.PendingReport == nil {
		t.Fatalf("missing pending report: %v", err)
	}
	var expected map[string]any
	if err := json.Unmarshal(s.PendingReport.Body, &expected); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		_ = json.NewDecoder(r.Body).Decode(&got)
		want, _ := json.Marshal(expected)
		body, _ := json.Marshal(got)
		if string(want) != string(body) {
			t.Error("kill retry changed payload")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ack_seq": s.PendingReport.Seq, "approval_state": "redeemed", "work_until": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	}))
	defer server.Close()
	t.Setenv("KB_LICENSE_SERVER", server.URL)
	if err := sendMaintenanceReport(context.Background(), io.Discard, f.root, s); err != nil {
		t.Fatal(err)
	}
	if s.LastSeq != 1 || s.PendingReport != nil {
		t.Fatal("kill retry did not acknowledge same sequence")
	}
}

func TestMaintenanceExitKillBeforeSwapRetainsVerifiedCopy(t *testing.T) {
	f := newMaintenanceExitFixture(t, false)
	t.Setenv("KB_MAINTENANCE_RELEASE", f.release)
	before := testutil.TreeDigest(t, f.appDir)
	killMaintenanceChild(t, "exit", "KP-EXIT-0")
	if got := testutil.TreeDigest(t, f.appDir); got != before {
		t.Fatal("exit kill before swap changed live source")
	}
	s, err := kbstate.ReadMaintenance(f.root)
	if err != nil || s.Pending == nil || s.Pending.Recovery == "" {
		t.Fatalf("missing exit recovery journal: %v", err)
	}
	recovery := filepath.Join(f.root, s.Pending.Recovery)
	if err := bench.VerifyMaintenanceRecovery(f.appDir, recovery, recovery+".manifest.json"); err != nil {
		t.Fatal(err)
	}
	if len(maintenanceExitMigrations(t, f.root)) != 0 {
		t.Fatal("migration ran before exit swap")
	}
	var status strings.Builder
	printMaintenanceStatus(&status, f.root)
	if !strings.Contains(status.String(), "recovery_needed") || !strings.Contains(status.String(), s.Pending.Recovery) {
		t.Fatal(status.String())
	}
}

func TestMaintenanceExitKillAfterMigrateStartsKeepsUnknownOutcome(t *testing.T) {
	f := newMaintenanceExitFixture(t, true)
	t.Setenv("KB_MAINTENANCE_RELEASE", f.release)
	killMaintenanceChild(t, "exit", "KP-EXIT-1")
	s, err := kbstate.ReadMaintenance(f.root)
	if err != nil || s.Pending == nil || !s.SchemaChanged {
		t.Fatalf("missing migration boundary: %v", err)
	}
	if s.MigrationOutcomes["site1"] != "unknown" {
		t.Fatalf("killed migration outcome: %v", s.MigrationOutcomes)
	}
	r := kbstate.ReadReceipt(f.root, "kb_compta")
	if !r.Valid() || r.Receipt.State != "returning" {
		t.Fatal("killed migration receipt did not remain returning")
	}
	recovery := filepath.Join(f.root, s.Pending.Recovery)
	old, _ := bench.MaintenanceReturnPaths(recovery)
	if _, err := os.Stat(filepath.Join(old, ".git")); err != nil {
		t.Fatal("maintenance source was not retained")
	}
	if _, err := os.Stat(filepath.Join(f.appDir, "kb_compta", "__init__.py")); err != nil {
		t.Fatal("release source was not retained")
	}
	if _, err := os.Stat(recovery); err != nil {
		t.Fatal("verified recovery copy was not retained")
	}
	var status strings.Builder
	printMaintenanceStatus(&status, f.root)
	if !strings.Contains(status.String(), "recovery_needed") || !strings.Contains(status.String(), "unknown") {
		t.Fatal(status.String())
	}
}
