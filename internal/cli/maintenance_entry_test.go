package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KB-Developpement/kb_pro_cli/internal/bench"
	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
	"github.com/KB-Developpement/kb_pro_cli/internal/testutil"
)

func TestMaintenanceRequestAndEnterPreserveWorkingSourceWithBothSiteBackups(t *testing.T) {
	exit := newMaintenanceExitFixture(t, false)
	f := exit.f
	if err := os.RemoveAll(filepath.Join(f.appDir, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := kbstate.WriteReceipt(f.root, f.session.Apps[0].OriginalReceipt); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(kbstate.MaintenancePath(f.root)); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.appDir, "README.md"), "pre-existing tracked support edit\n")
	write(t, filepath.Join(f.appDir, "untracked.txt"), "preserve untracked\n")
	script := filepath.Join(f.appDir, "script.sh")
	write(t, script, "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(script, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("README.md", filepath.Join(f.appDir, "readme-link")); err != nil {
		t.Fatal(err)
	}
	before := testutil.TreeDigest(t, f.appDir)
	var mu sync.Mutex
	states := []string{}
	var request license.MaintenanceRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/maintenance/request":
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				return
			}
			_ = json.NewEncoder(w).Encode(license.RequestReply{RequestID: strings.Repeat("1", 32), SessionID: f.sessionID, Status: "requested", ApproveBy: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339), Apps: request.Apps})
		case "/maintenance/redeem":
			_ = json.NewEncoder(w).Encode(license.RedeemReply{SessionID: f.sessionID, SessionCredential: f.credential, Apps: []string{"kb_compta"}, WorkUntil: time.Now().Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339)})
		case "/maintenance/report":
			var report license.MaintenanceReport
			if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			states = append(states, report.SourceState)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(license.ReportReply{AckSeq: report.Seq, ApprovalState: "redeemed", WorkUntil: time.Now().Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339)})
		default:
			t.Errorf("unexpected entry endpoint %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	t.Setenv("KB_LICENSE_SERVER", server.URL)
	oldPrompt := maintenanceCodePrompt
	maintenanceCodePrompt = func() (string, error) { return "entry-code-in-memory-only", nil }
	t.Cleanup(func() { maintenanceCodePrompt = oldPrompt })
	if err := maintenanceRequest(context.Background(), io.Discard, f.root, "kb_compta", "repair", false); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(request.Apps, []string{"kb_compta"}) || request.Scope != "apps" {
		t.Fatalf("wrong frozen request %v", request.Apps)
	}
	if err := maintenanceEnter(context.Background(), io.Discard, f.root, ""); err != nil {
		t.Fatal(err)
	}
	after := testutil.TreeDigestExcluding(t, f.appDir, func(p string) bool { return p == ".git" || strings.HasPrefix(filepath.ToSlash(p), ".git/") })
	if after != before {
		t.Fatal("entry replaced working files, modes or symlinks")
	}
	s, err := kbstate.ReadMaintenance(f.root)
	if err != nil || s.SourceState != "active" || s.Pending != nil {
		t.Fatalf("entry state: %+v %v", s, err)
	}
	if len(s.Backups) != 6 || len(s.PatchSnapshot) != 2 {
		t.Fatalf("entry backups/snapshots: %d/%d", len(s.Backups), len(s.PatchSnapshot))
	}
	for _, b := range s.Backups {
		fi, e := os.Stat(filepath.Join(f.root, b.Path))
		if e != nil || fi.Size() == 0 {
			t.Fatalf("unverified backup %s: %v", b.Path, e)
		}
	}
	obs, err := bench.ObserveMaintenanceGit(f.root, "kb_compta")
	if err != nil || obs.Head != f.base || !obs.Dirty || obs.UntrackedFiles == 0 {
		t.Fatalf("preserved edits were misreported: %+v %v", obs, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(states, []string{"attaching", "active"}) {
		t.Fatalf("entry transitions %v", states)
	}
}

func TestMaintenanceRequestCannotRewritePendingEntry(t *testing.T) {
	f := newMaintenanceReportFixture(t, false)
	s := archiveRequestSession(t, f)
	s.Pending = &kbstate.MaintenancePending{Op: "enter", Step: "redeemed", StartedAt: time.Now().UTC().Format(time.RFC3339), Expected: map[string]string{"kb_compta": f.base}}
	if err := kbstate.WriteMaintenance(f.root, s); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(kbstate.MaintenancePath(f.root))
	if err != nil {
		t.Fatal(err)
	}
	old := maintenanceRefresh
	maintenanceRefresh = func(context.Context) (string, string, string, map[string]bool, error) {
		t.Fatal("pending request reached online license refresh")
		return "", "", "", nil, nil
	}
	t.Cleanup(func() { maintenanceRefresh = old })
	err = maintenanceRequest(context.Background(), io.Discard, f.root, "kb_compta", s.Reason, false)
	if err == nil || !strings.Contains(err.Error(), "recovery_needed") {
		t.Fatalf("pending entry request: %v", err)
	}
	after, err := os.ReadFile(kbstate.MaintenancePath(f.root))
	if err != nil || string(after) != string(before) {
		t.Fatal("new request rewrote pending entry")
	}
}
