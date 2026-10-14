package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
	"github.com/KB-Developpement/kb_pro_cli/internal/testutil"
)

func installFlowBench(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	writeExec(t, filepath.Join(bin, "bench"), `#!/bin/sh
if [ "$*" = "--version" ]; then echo 5.31.0; exit 0; fi
case "$*" in
  *"list-apps"*) echo '{"site1":["kb_pro","kb_compta"]}'; exit 0 ;;
esac
exit 0
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func setFlowRefresh(t *testing.T, f *maintenanceReportFixture, err error, fingerprint string) {
	t.Helper()
	old := maintenanceRefresh
	maintenanceRefresh = func(context.Context) (string, string, string, map[string]bool, error) {
		if err != nil {
			return "", "", "", nil, err
		}
		if fingerprint == "" {
			fingerprint = f.fingerprint
		}
		return "maintenance-token", fingerprint, f.session.LicenseKeySHA256, map[string]bool{"kb_compta": true}, nil
	}
	t.Cleanup(func() { maintenanceRefresh = old })
}

func archiveRequestSession(t *testing.T, f *maintenanceReportFixture) *kbstate.MaintenanceSession {
	t.Helper()
	s, err := kbstate.ReadMaintenance(f.root)
	if err != nil {
		t.Fatal(err)
	}
	s.SourceState = kbstate.StateArchive
	s.ApprovalState = "requested"
	s.RequestID = strings.Repeat("2", 32)
	s.Pending = nil
	if err := kbstate.WriteReceipt(f.root, s.Apps[0].OriginalReceipt); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(f.appDir, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := kbstate.WriteMaintenance(f.root, s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMaintenanceRequestRejectsMissingOrMalformedReceiptBeforeHTTP(t *testing.T) {
	for _, tc := range []struct {
		name      string
		malformed bool
	}{
		{name: "missing receipt"},
		{name: "malformed receipt", malformed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMaintenanceReportFixture(t, false)
			receiptPath := kbstate.ReceiptPath(f.root, "kb_compta")
			if tc.malformed {
				if err := os.WriteFile(receiptPath, []byte(`{"schema_version":`), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(receiptPath); err != nil {
				t.Fatal(err)
			}
			var requests, refreshes atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
			defer srv.Close()
			t.Setenv("KB_LICENSE_SERVER", srv.URL)
			old := maintenanceRefresh
			maintenanceRefresh = func(context.Context) (string, string, string, map[string]bool, error) {
				refreshes.Add(1)
				return "tok", f.fingerprint, f.session.LicenseKeySHA256, map[string]bool{"kb_compta": true}, nil
			}
			t.Cleanup(func() { maintenanceRefresh = old })
			beforeSession, err := os.ReadFile(kbstate.MaintenancePath(f.root))
			if err != nil {
				t.Fatal(err)
			}
			err = maintenanceRequest(context.Background(), io.Discard, f.root, "kb_compta", "repair", false)
			if err == nil {
				t.Fatal("request accepted without trusted receipt")
			}
			if requests.Load() != 0 || refreshes.Load() != 0 {
				t.Fatalf("preflight made %d HTTP requests and %d license refreshes", requests.Load(), refreshes.Load())
			}
			afterSession, err := os.ReadFile(kbstate.MaintenancePath(f.root))
			if err != nil || !bytes.Equal(afterSession, beforeSession) {
				t.Fatalf("rejected request changed session: %v", err)
			}
			if tc.malformed {
				got, err := os.ReadFile(receiptPath)
				if err != nil || string(got) != `{"schema_version":` {
					t.Fatalf("malformed receipt changed: %q, %v", got, err)
				}
			} else if _, err := os.Stat(receiptPath); !os.IsNotExist(err) {
				t.Fatalf("missing receipt was recreated; stat error=%v", err)
			}
		})
	}
}

func TestMaintenanceRequestPersistsScopeThenRedeemMismatchLeavesArchiveUntouched(t *testing.T) {
	f := newMaintenanceReportFixture(t, false)
	if err := kbstate.WriteReceipt(f.root, f.session.Apps[0].OriginalReceipt); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(f.appDir, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(kbstate.MaintenancePath(f.root)); err != nil {
		t.Fatal(err)
	}
	installFlowBench(t)
	setFlowRefresh(t, f, nil, "")
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/maintenance/request":
			var got license.MaintenanceRequest
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Errorf("decode request: %v", err)
			}
			if got.Apps[0] != "kb_compta" || got.Reason != "repair" || got.Scope != "apps" {
				t.Errorf("request scope = %+v", got)
			}
			_, _ = io.WriteString(w, `{"request_id":"33333333333333333333333333333333","session_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","status":"requested","approve_by":"2026-10-14T00:00:00Z","apps":["kb_compta"]}`)
		case "/maintenance/redeem":
			_, _ = io.WriteString(w, `{"session_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","session_credential":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","apps":["kb_stock"],"work_until":"2026-10-14T00:00:00Z"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	t.Setenv("KB_LICENSE_SERVER", srv.URL)
	var out bytes.Buffer
	if err := maintenanceRequest(context.Background(), &out, f.root, "kb_compta", "repair", false); err != nil {
		t.Fatalf("request maintenance: %v", err)
	}
	requested, err := kbstate.ReadMaintenance(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if requested.SourceState != kbstate.StateArchive || requested.ApprovalState != "requested" || requested.SessionID != f.sessionID || requested.RequestID != "33333333333333333333333333333333" {
		t.Fatalf("persisted request = %+v", requested)
	}
	beforeSession, err := os.ReadFile(kbstate.MaintenancePath(f.root))
	if err != nil {
		t.Fatal(err)
	}
	beforeReceipt, err := os.ReadFile(kbstate.ReceiptPath(f.root, "kb_compta"))
	if err != nil {
		t.Fatal(err)
	}
	beforeApp := testutil.TreeDigest(t, f.appDir)
	oldPrompt := maintenanceCodePrompt
	maintenanceCodePrompt = func() (string, error) { return "entry-code", nil }
	t.Cleanup(func() { maintenanceCodePrompt = oldPrompt })
	err = maintenanceEnter(context.Background(), io.Discard, f.root, "")
	if err == nil || !strings.Contains(err.Error(), "frozen session scope") {
		t.Fatalf("scope-mismatched redeem = %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("request + redeem calls = %d, want 2", requests.Load())
	}
	afterSession, err := os.ReadFile(kbstate.MaintenancePath(f.root))
	if err != nil || !bytes.Equal(afterSession, beforeSession) {
		t.Fatalf("scope mismatch changed request session: %v", err)
	}
	afterReceipt, err := os.ReadFile(kbstate.ReceiptPath(f.root, "kb_compta"))
	if err != nil || !bytes.Equal(afterReceipt, beforeReceipt) {
		t.Fatalf("scope mismatch changed archive receipt: %v", err)
	}
	if got := testutil.TreeDigest(t, f.appDir); got != beforeApp {
		t.Fatalf("scope mismatch changed archive tree: %s != %s", got, beforeApp)
	}
	if _, err := os.Lstat(filepath.Join(f.appDir, ".git")); !os.IsNotExist(err) {
		t.Fatalf("scope mismatch attached Git; stat error=%v", err)
	}
}

func TestMaintenanceEnterRefusalAndScopeMismatchDoNotAttach(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{name: "server refuses redeem", status: http.StatusForbidden, body: `{"error":"not_approved"}`, wantErr: "HTTP 403"},
		{name: "redeem scope differs", status: http.StatusOK, body: `{"session_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","session_credential":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","apps":["kb_stock"],"work_until":"2026-01-02T00:00:00Z"}`, wantErr: "frozen session scope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMaintenanceReportFixture(t, false)
			s := archiveRequestSession(t, f)
			installFlowBench(t)
			setFlowRefresh(t, f, nil, "")
			oldPrompt := maintenanceCodePrompt
			maintenanceCodePrompt = func() (string, error) { return "entry-code", nil }
			t.Cleanup(func() { maintenanceCodePrompt = oldPrompt })
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path != "/maintenance/redeem" {
					t.Errorf("redeem path = %s", r.URL.Path)
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			t.Setenv("KB_LICENSE_SERVER", srv.URL)
			beforeSession, err := os.ReadFile(kbstate.MaintenancePath(f.root))
			if err != nil {
				t.Fatal(err)
			}
			beforeReceipt, err := os.ReadFile(kbstate.ReceiptPath(f.root, "kb_compta"))
			if err != nil {
				t.Fatal(err)
			}
			beforeApp := testutil.TreeDigest(t, f.appDir)
			err = maintenanceEnter(context.Background(), io.Discard, f.root, "")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("enter error = %v; want %q", err, tc.wantErr)
			}
			if requests.Load() != 1 {
				t.Fatalf("redeem sent %d requests, want 1", requests.Load())
			}
			afterSession, err := os.ReadFile(kbstate.MaintenancePath(f.root))
			if err != nil || !bytes.Equal(afterSession, beforeSession) {
				t.Fatalf("redeem refusal changed session: %v", err)
			}
			afterReceipt, err := os.ReadFile(kbstate.ReceiptPath(f.root, "kb_compta"))
			if err != nil || !bytes.Equal(afterReceipt, beforeReceipt) {
				t.Fatalf("redeem refusal changed receipt: %v", err)
			}
			if got := testutil.TreeDigest(t, f.appDir); got != beforeApp {
				t.Fatalf("redeem refusal changed app tree: before %s after %s", beforeApp, got)
			}
			if _, err := os.Lstat(filepath.Join(f.appDir, ".git")); !os.IsNotExist(err) {
				t.Fatalf("redeem refusal attached Git; lstat error=%v", err)
			}
			if _, err := license.LoadMaintenanceCredential(s.SessionID); err == nil {
				t.Fatal("redeem refusal persisted operator credential")
			}
		})
	}
}

func setPreparedEntryState(t *testing.T, f *maintenanceReportFixture, sourceState string) *kbstate.MaintenanceSession {
	t.Helper()
	s, err := kbstate.ReadMaintenance(f.root)
	if err != nil {
		t.Fatal(err)
	}
	s.SourceState = sourceState
	s.ApprovalState = "redeemed"
	s.Pending = &kbstate.MaintenancePending{
		Op: "enter", Step: "prepared", StartedAt: "2026-01-01T00:00:00Z",
		PreparedGit: ".kb/maintenance/scratch/attach-crash/.git",
		Expected:    map[string]string{"kb_compta": f.base},
	}
	if err := kbstate.WriteMaintenance(f.root, s); err != nil {
		t.Fatal(err)
	}
	saveMaintenanceTestCredential(t, f)
	return s
}

func TestEntryReplayCompletesOwnedAttachAndActiveEnterIsNoOp(t *testing.T) {
	f := newMaintenanceReportFixture(t, false)
	setPreparedEntryState(t, f, "attaching")
	owner, err := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		SessionID     string `json:"session_id"`
	}{SchemaVersion: 1, SessionID: f.sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.appDir, ".git", "kb-maintenance-session"), append(owner, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	gitInfo, err := os.Stat(filepath.Join(f.appDir, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/maintenance/report" {
			t.Errorf("resume request path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"ack_seq":1,"approval_state":"approved","work_until":"2026-01-02T00:00:00Z"}`)
	}))
	defer srv.Close()
	t.Setenv("KB_LICENSE_SERVER", srv.URL)
	beforeBench := testutil.BenchDigest(t, f.root)
	beforeKB := testutil.TreeDigest(t, kbstate.Dir(f.root))
	var status bytes.Buffer
	if err := runStatus(&status); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status.String(), "Maintenance: recovery_needed") {
		t.Fatalf("status omitted pending recovery state:\n%s", status.String())
	}
	if requests.Load() != 0 || testutil.BenchDigest(t, f.root) != beforeBench || testutil.TreeDigest(t, kbstate.Dir(f.root)) != beforeKB {
		t.Fatal("status made HTTP request or changed bench state")
	}
	var out bytes.Buffer
	if err := maintenanceEnter(context.Background(), &out, f.root, ""); err != nil {
		t.Fatalf("resume prepared entry: %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("recovery report requests = %d, want 1", got)
	}
	afterGit, err := os.Stat(filepath.Join(f.appDir, ".git"))
	if err != nil || !os.SameFile(gitInfo, afterGit) {
		t.Fatalf("replay replaced attached .git directory: %v", err)
	}
	current, err := kbstate.ReadMaintenance(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if current.SourceState != "active" || current.Pending != nil || current.LastSeq != 1 || current.Apps[0].ReportedHead != f.base {
		t.Fatalf("completed recovery state = %+v", current)
	}
	receipt := kbstate.ReadReceipt(f.root, "kb_compta")
	if !receipt.Valid() || receipt.Receipt.State != "active" || receipt.Receipt.SessionID == nil || *receipt.Receipt.SessionID != f.sessionID {
		t.Fatalf("completed recovery receipt = %+v", receipt)
	}
	sessionBytes, err := os.ReadFile(kbstate.MaintenancePath(f.root))
	if err != nil {
		t.Fatal(err)
	}
	receiptBytes, err := os.ReadFile(kbstate.ReceiptPath(f.root, "kb_compta"))
	if err != nil {
		t.Fatal(err)
	}
	gitInfo, err = os.Stat(filepath.Join(f.appDir, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	err = maintenanceEnter(context.Background(), io.Discard, f.root, "")
	if err == nil || !strings.Contains(err.Error(), "already entered") {
		t.Fatalf("active repeated enter = %v; want already-entered refusal", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("active repeated enter made HTTP; request count %d", requests.Load())
	}
	currentSession, err := os.ReadFile(kbstate.MaintenancePath(f.root))
	if err != nil || !bytes.Equal(currentSession, sessionBytes) {
		t.Fatalf("active repeated enter changed session: %v", err)
	}
	currentReceipt, err := os.ReadFile(kbstate.ReceiptPath(f.root, "kb_compta"))
	if err != nil || !bytes.Equal(currentReceipt, receiptBytes) {
		t.Fatalf("active repeated enter changed receipt: %v", err)
	}
	afterGit, err = os.Stat(filepath.Join(f.appDir, ".git"))
	if err != nil || !os.SameFile(gitInfo, afterGit) {
		t.Fatalf("active repeated enter replaced .git: %v", err)
	}
}

func TestPreparedEntryRaceWithForeignGitRefusesWithoutChanges(t *testing.T) {
	f := newMaintenanceReportFixture(t, false)
	setPreparedEntryState(t, f, "attaching")
	if err := os.WriteFile(filepath.Join(f.appDir, ".git", "foreign-marker"), []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeSession, err := os.ReadFile(kbstate.MaintenancePath(f.root))
	if err != nil {
		t.Fatal(err)
	}
	beforeReceipt, err := os.ReadFile(kbstate.ReceiptPath(f.root, "kb_compta"))
	if err != nil {
		t.Fatal(err)
	}
	beforeGit, err := os.Stat(filepath.Join(f.appDir, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer srv.Close()
	t.Setenv("KB_LICENSE_SERVER", srv.URL)
	err = maintenanceEnter(context.Background(), io.Discard, f.root, "")
	if err == nil || !strings.Contains(err.Error(), "holds a Git checkout") {
		t.Fatalf("foreign attach race = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("foreign attach race made %d HTTP requests", requests.Load())
	}
	afterSession, err := os.ReadFile(kbstate.MaintenancePath(f.root))
	if err != nil || !bytes.Equal(afterSession, beforeSession) {
		t.Fatalf("foreign attach race changed session: %v", err)
	}
	afterReceipt, err := os.ReadFile(kbstate.ReceiptPath(f.root, "kb_compta"))
	if err != nil || !bytes.Equal(afterReceipt, beforeReceipt) {
		t.Fatalf("foreign attach race changed receipt: %v", err)
	}
	afterGit, err := os.Stat(filepath.Join(f.appDir, ".git"))
	if err != nil || !os.SameFile(beforeGit, afterGit) {
		t.Fatalf("foreign attach race replaced .git: %v", err)
	}
}

func TestMaintenanceCloseRetriesSameBodyThenSecondExitIsOffline(t *testing.T) {
	f := newMaintenanceReportFixture(t, false)
	saveMaintenanceTestCredential(t, f)
	session, err := kbstate.ReadMaintenance(f.root)
	if err != nil {
		t.Fatal(err)
	}
	session.SourceState = kbstate.StateArchive
	session.ApprovalState = "redeemed"
	session.LastSeq = 4
	if err := kbstate.WriteMaintenance(f.root, session); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/maintenance/close" {
			t.Errorf("close path = %s", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read close request: %v", err)
		}
		bodies = append(bodies, body)
		if requests.Add(1) == 1 {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Error("HTTP test server cannot hijack lost acknowledgement")
				return
			}
			conn, _, err := hijacker.Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		_, _ = io.WriteString(w, `{"status":"closed","closed_at":"2026-01-02T00:00:00Z"}`)
	}))
	defer srv.Close()
	t.Setenv("KB_LICENSE_SERVER", srv.URL)

	err = maintenanceExit(context.Background(), io.Discard, f.root, "", "", false)
	if err == nil {
		t.Fatal("close reported success despite lost acknowledgement")
	}
	pending, err := kbstate.ReadMaintenance(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if pending.LastSeq != 4 || pending.PendingClose == nil || pending.PendingClose.Seq != 5 {
		t.Fatalf("close retry state = last seq %d, pending %+v", pending.LastSeq, pending.PendingClose)
	}
	var closeBody license.MaintenanceClose
	if err := json.Unmarshal(pending.PendingClose.Body, &closeBody); err != nil {
		t.Fatal(err)
	}
	if closeBody.Seq != 5 || closeBody.SourceState != "archive" {
		t.Fatalf("pending close body = %+v", closeBody)
	}
	withoutHash := closeBody
	withoutHash.PayloadSHA256 = ""
	canonical, err := json.Marshal(withoutHash)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	if closeBody.PayloadSHA256 != fmt.Sprintf("%x", sum) {
		t.Fatalf("close payload hash = %s, want %x", closeBody.PayloadSHA256, sum)
	}
	if err := maintenanceExit(context.Background(), io.Discard, f.root, "", "", false); err != nil {
		t.Fatalf("retry close: %v", err)
	}
	if requests.Load() != 2 || len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("close request replay count=%d bodies=%q", requests.Load(), bodies)
	}
	closed, err := kbstate.ReadMaintenance(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if closed.ApprovalState != "closed" || closed.LastSeq != 5 || closed.PendingClose != nil {
		t.Fatalf("closed session = approval %s seq %d pending %+v", closed.ApprovalState, closed.LastSeq, closed.PendingClose)
	}
	if err := maintenanceExit(context.Background(), io.Discard, f.root, "", "", false); err == nil || !strings.Contains(err.Error(), "no active session") {
		t.Fatalf("second exit = %v; want offline no-active refusal", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("second exit made HTTP request; total %d", requests.Load())
	}
}

func TestMaintenanceExitPreflightsHaveNoSourceSideEffectsOrHTTP(t *testing.T) {
	for _, tc := range []struct {
		name        string
		release     string
		stopped     bool
		refreshErr  error
		fingerprint string
		wantErr     string
	}{
		{name: "release required", stopped: true, wantErr: "requires --release"},
		{name: "services must be stopped", release: "kb_compta=v1.2.0", wantErr: "stop affected"},
		{name: "standing refresh unavailable", release: "kb_compta=v1.2.0", stopped: true, refreshErr: context.DeadlineExceeded, wantErr: "context deadline"},
		{name: "fingerprint mismatch", release: "kb_compta=v1.2.0", stopped: true, fingerprint: strings.Repeat("e", 64), wantErr: "identity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMaintenanceReportFixture(t, false)
			saveMaintenanceTestCredential(t, f)
			setFlowRefresh(t, f, tc.refreshErr, tc.fingerprint)
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
			defer srv.Close()
			t.Setenv("KB_LICENSE_SERVER", srv.URL)
			beforeBench := testutil.BenchDigest(t, f.root)
			beforeKB := testutil.TreeDigest(t, kbstate.Dir(f.root))
			beforeSession, err := os.ReadFile(kbstate.MaintenancePath(f.root))
			if err != nil {
				t.Fatal(err)
			}
			beforeReceipt, err := os.ReadFile(kbstate.ReceiptPath(f.root, "kb_compta"))
			if err != nil {
				t.Fatal(err)
			}
			err = maintenanceExit(context.Background(), io.Discard, f.root, tc.release, "none", tc.stopped)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("exit preflight = %v; want %q", err, tc.wantErr)
			}
			if requests.Load() != 0 {
				t.Fatalf("exit preflight made %d HTTP requests", requests.Load())
			}
			if got := testutil.BenchDigest(t, f.root); got != beforeBench {
				t.Errorf("exit preflight changed apps/sites: before %s after %s", beforeBench, got)
			}
			if got := testutil.TreeDigest(t, kbstate.Dir(f.root)); got != beforeKB {
				t.Errorf("exit preflight changed .kb: before %s after %s", beforeKB, got)
			}
			afterSession, err := os.ReadFile(kbstate.MaintenancePath(f.root))
			if err != nil || !bytes.Equal(afterSession, beforeSession) {
				t.Errorf("exit preflight changed session: %v", err)
			}
			afterReceipt, err := os.ReadFile(kbstate.ReceiptPath(f.root, "kb_compta"))
			if err != nil || !bytes.Equal(afterReceipt, beforeReceipt) {
				t.Errorf("exit preflight changed receipt: %v", err)
			}
		})
	}
}

func TestDirtyMaintenanceExitDownloadsThenRefusesBeforeSwap(t *testing.T) {
	f := newMaintenanceReportFixture(t, false)
	saveMaintenanceTestCredential(t, f)
	setFlowRefresh(t, f, nil, "")
	installFlowBench(t)
	write(t, filepath.Join(f.appDir, "README.md"), "uncommitted support edit\n")
	const releaseTag = "v1.2.0"
	const releaseCommit = "9999999999999999999999999999999999999999"
	var downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/download/") {
			downloads.Add(1)
			w.Header().Set("X-KB-Repository", "KB-Developpement/kb_compta")
			w.Header().Set("X-KB-Ref", releaseTag)
			w.Header().Set("X-KB-Commit", releaseCommit)
			w.Header().Set("X-KB-Release-Tag", releaseTag)
			_, _ = io.WriteString(w, "release archive bytes")
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	t.Setenv("KB_LICENSE_SERVER", srv.URL)
	beforeBench := testutil.BenchDigest(t, f.root)
	beforeKB := testutil.TreeDigest(t, kbstate.Dir(f.root))
	beforeSession, err := os.ReadFile(kbstate.MaintenancePath(f.root))
	if err != nil {
		t.Fatal(err)
	}
	beforeReceipt, err := os.ReadFile(kbstate.ReceiptPath(f.root, "kb_compta"))
	if err != nil {
		t.Fatal(err)
	}
	err = maintenanceExit(context.Background(), io.Discard, f.root, "kb_compta="+releaseTag, "none", true)
	if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("dirty exit = %v; want dirty-source refusal", err)
	}
	if downloads.Load() != 1 {
		t.Fatalf("strict release requests = %d, want 1", downloads.Load())
	}
	if got := testutil.BenchDigest(t, f.root); got != beforeBench {
		t.Fatalf("dirty exit changed apps/sites before refusal: %s != %s", got, beforeBench)
	}
	if got := testutil.TreeDigest(t, kbstate.Dir(f.root)); got != beforeKB {
		t.Fatalf("dirty exit changed .kb before refusal: %s != %s", got, beforeKB)
	}
	afterSession, err := os.ReadFile(kbstate.MaintenancePath(f.root))
	if err != nil || !bytes.Equal(afterSession, beforeSession) {
		t.Fatalf("dirty exit changed session: %v", err)
	}
	afterReceipt, err := os.ReadFile(kbstate.ReceiptPath(f.root, "kb_compta"))
	if err != nil || !bytes.Equal(afterReceipt, beforeReceipt) {
		t.Fatalf("dirty exit changed receipt: %v", err)
	}
}
