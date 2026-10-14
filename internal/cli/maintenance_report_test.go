package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KB-Developpement/kb_pro_cli/internal/apps"
	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
	"github.com/KB-Developpement/kb_pro_cli/internal/testutil"
)

type maintenanceReportFixture struct {
	root, appDir, sessionID, benchID, fingerprint, credential string
	base, head, branch                                        string
	session                                                   *kbstate.MaintenanceSession
}

func newMaintenanceReportFixture(t *testing.T, extraCommit bool) *maintenanceReportFixture {
	t.Helper()
	root, home := t.TempDir(), t.TempDir()
	t.Setenv("KB_BENCH_ROOT", root)
	t.Setenv("HOME", home)
	t.Setenv("TMPDIR", t.TempDir())
	app, ok := apps.ByName("kb_compta")
	if !ok {
		t.Fatal("kb_compta missing from app registry")
	}
	appDir := filepath.Join(root, "apps", app.Dir())
	for _, dir := range []string{appDir, filepath.Join(root, "sites"), filepath.Join(root, "sites", "site1")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(root, "sites", "apps.txt"), "frappe\nkb_compta\n")
	write(t, filepath.Join(root, "sites", "apps.json"), `{"kb_compta":{"required":true,"idx":1,"version":"1.0.0"}}`)
	write(t, filepath.Join(root, "sites", "site1", "site_config.json"), `{}`)
	for _, f := range []struct{ name, body string }{
		{"README.md", "original archive\n"},
		{"kb_compta/__init__.py", "__version__ = '1.0.0'\n"},
		{"kb_compta/hooks.py", "app_name = 'kb_compta'\n"},
	} {
		write(t, filepath.Join(appDir, f.name), f.body)
	}
	branch := "support/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	runMaintenanceTestGit(t, appDir, "init", "-q", "-b", branch)
	runMaintenanceTestGit(t, appDir, "config", "user.name", "Maintenance Test")
	runMaintenanceTestGit(t, appDir, "config", "user.email", "maintenance-test@example.invalid")
	runMaintenanceTestGit(t, appDir, "add", "-A")
	runMaintenanceTestGit(t, appDir, "commit", "-q", "-m", "archive base")
	base := runMaintenanceTestGit(t, appDir, "rev-parse", "HEAD")
	head := base
	if extraCommit {
		write(t, filepath.Join(appDir, "README.md"), "support change\n")
		runMaintenanceTestGit(t, appDir, "add", "README.md")
		runMaintenanceTestGit(t, appDir, "commit", "-q", "-m", "support fix")
		head = runMaintenanceTestGit(t, appDir, "rev-parse", "HEAD")
	}

	const sessionID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const benchID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const credential = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	fingerprint, err := license.Fingerprint()
	if err != nil {
		t.Skipf("machine fingerprint unavailable: %v", err)
	}
	tag := "v1.0.0"
	originalReceipt := &kbstate.Receipt{
		SchemaVersion: kbstate.SupportedSchema, App: app.Name, Directory: app.Dir(), Package: app.PackageName(),
		Repository: "KB-Developpement/" + app.Repository(), Line: "1", Requested: "major=1", Ref: tag,
		Commit: base, ReleaseTag: &tag, ArchiveSHA256: strings.Repeat("e", 64),
		InstalledAt: "2026-01-01T00:00:00Z", State: kbstate.StateArchive, Provenance: kbstate.ProvInstalled,
	}
	protectedReceipt := *originalReceipt
	protectedReceipt.State = "active"
	protectedReceipt.SessionID = stringPointer(sessionID)
	protectedReceipt.Provenance = kbstate.ProvMaintenance
	session := &kbstate.MaintenanceSession{
		SchemaVersion: kbstate.SupportedSchema, SessionID: sessionID, BenchID: benchID,
		Fingerprint: fingerprint, LicenseKeySHA256: strings.Repeat("f", 64),
		ClientRequestID: strings.Repeat("1", 32), Reason: "support fix", SourceState: "active", ApprovalState: "redeemed",
		Apps: []kbstate.MaintenanceApp{{
			App: app.Name, Directory: app.Dir(), Package: app.PackageName(), Repository: originalReceipt.Repository,
			Line: "1", BaseCommit: base, Branch: branch, Head: base, OriginalReceipt: originalReceipt,
		}},
		PatchSnapshot: map[string][]string{}, Backups: []kbstate.MaintenanceBackup{}, LastSeq: 0,
	}
	if err := kbstate.WriteReceipt(root, &protectedReceipt); err != nil {
		t.Fatal(err)
	}
	if err := kbstate.WriteMaintenance(root, session); err != nil {
		t.Fatal(err)
	}
	return &maintenanceReportFixture{root: root, appDir: appDir, sessionID: sessionID, benchID: benchID, fingerprint: fingerprint, credential: credential, base: base, head: head, branch: branch, session: session}
}

func stringPointer(s string) *string { return &s }

func runMaintenanceTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func installMaintenanceFetchShim(t *testing.T, bareRemote, approvedURL string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	shimDir := t.TempDir()
	script := fmt.Sprintf("#!/usr/bin/env python3\nimport os, sys\nreal = %q\nbare = %q\napproved = %q\nargs = sys.argv[1:]\nif 'fetch' in args:\n    args = [bare if item in (approved, approved[:-4] if approved.endswith('.git') else approved, 'origin') else item for item in args]\nos.execv(real, [real] + args)\n", realGit, bareRemote, approvedURL)
	path := filepath.Join(shimDir, "git")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func saveMaintenanceTestCredential(t *testing.T, f *maintenanceReportFixture) {
	t.Helper()
	err := license.SaveMaintenanceCredential(license.MaintenanceCredential{
		SchemaVersion: 1, SessionID: f.sessionID, BenchID: f.benchID, Fingerprint: f.fingerprint,
		SessionCredential: f.credential, SavedAt: "2026-01-01T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMaintenanceReportRetriesIdenticalDurablePayloadAfterLostAck(t *testing.T) {
	f := newMaintenanceReportFixture(t, true)
	saveMaintenanceTestCredential(t, f)
	var requestCount atomic.Int32
	var mu sync.Mutex
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/maintenance/report" || r.Method != http.MethodPost {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-KB-Maintenance-Session"); got != f.credential {
			t.Errorf("maintenance credential header = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
		}
		mu.Lock()
		bodies = append(bodies, append([]byte(nil), body...))
		mu.Unlock()
		if requestCount.Add(1) == 1 {
			// Server accepted and recorded report, then lost acknowledgement.
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Error("HTTP test server does not support connection hijacking")
				return
			}
			conn, _, err := hijacker.Hijack()
			if err != nil {
				t.Errorf("hijack response: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"ack_seq":1,"approval_state":"approved","work_until":"2026-01-02T00:00:00Z"}`)
	}))
	defer srv.Close()
	t.Setenv("KB_LICENSE_SERVER", srv.URL)

	var out bytes.Buffer
	reportErr := sendMaintenanceReport(context.Background(), &out, f.root, f.session)
	if reportErr == nil {
		t.Fatal("first report returned success despite lost acknowledgement")
	}
	first, err := kbstate.ReadMaintenance(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if first.LastSeq != 0 || first.PendingReport == nil || first.PendingReport.Seq != 1 {
		t.Fatalf("durable pending report after lost ack = seq %d, pending %+v; report error: %v", first.LastSeq, first.PendingReport, reportErr)
	}
	if first.Apps[0].ReportedHead != "" {
		t.Fatalf("unacknowledged report advanced reported head to %s", first.Apps[0].ReportedHead)
	}
	firstBody := append([]byte(nil), first.PendingReport.Body...)
	var firstReport license.MaintenanceReport
	if err := json.Unmarshal(firstBody, &firstReport); err != nil {
		t.Fatal(err)
	}
	if firstReport.Seq != 1 || len(firstReport.Apps) != 1 || firstReport.Apps[0].Head != f.head {
		t.Fatalf("first report payload = %+v", firstReport)
	}
	withoutHash := firstReport
	withoutHash.PayloadSHA256 = ""
	canonical, err := json.Marshal(withoutHash)
	if err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256(canonical)
	if firstReport.PayloadSHA256 != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("payload hash = %s, want %x", firstReport.PayloadSHA256, wantHash)
	}

	if err := sendMaintenanceReport(context.Background(), &out, f.root, first); err != nil {
		t.Fatalf("retry pending report: %v", err)
	}
	if requestCount.Load() != 2 {
		t.Fatalf("server received %d report requests, want initial plus retry", requestCount.Load())
	}
	mu.Lock()
	if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
		mu.Unlock()
		t.Fatalf("retry body changed: requests %q", bodies)
	}
	var sentReport license.MaintenanceReport
	if err := json.Unmarshal(bodies[0], &sentReport); err != nil {
		mu.Unlock()
		t.Fatalf("decode sent report: %v", err)
	}
	if !reflect.DeepEqual(sentReport, firstReport) {
		mu.Unlock()
		t.Fatalf("sent report differs from durable report: sent %+v; saved %+v", sentReport, firstReport)
	}
	mu.Unlock()
	final, err := kbstate.ReadMaintenance(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if final.LastSeq != 1 || final.PendingReport != nil || final.ApprovalState != "approved" {
		t.Fatalf("after acknowledgement = seq %d pending %+v approval %s", final.LastSeq, final.PendingReport, final.ApprovalState)
	}
	if final.Apps[0].Head != f.head || len(final.Apps[0].Protected) != 1 || final.Apps[0].Protected[0] != f.head {
		t.Fatalf("reported Git protection = head %s, commits %v", final.Apps[0].Head, final.Apps[0].Protected)
	}
	if final.Apps[0].ReportedHead != f.head {
		t.Fatalf("acknowledgement did not advance reported head: %s", final.Apps[0].ReportedHead)
	}
	receiptState := kbstate.ReadReceipt(f.root, "kb_compta")
	if !receiptState.Valid() || len(receiptState.Receipt.Protected) != 1 || receiptState.Receipt.Protected[0] != f.head || len(receiptState.Receipt.ProtectionRefs) != 1 || receiptState.Receipt.ProtectionRefs[0] != final.Apps[0].ProtectionRefs[0] {
		t.Fatalf("receipt protection = %+v", receiptState)
	}
	ref := "refs/kb-maintenance/" + f.sessionID + "/" + f.head
	if got := runMaintenanceTestGit(t, f.appDir, "show-ref", "--verify", ref); !strings.HasSuffix(got, " "+ref) {
		t.Fatalf("protected Git ref = %q, want %s", got, ref)
	}
}

func TestMaintenanceReportMissingCredentialMakesNoHTTPRequest(t *testing.T) {
	f := newMaintenanceReportFixture(t, false)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer srv.Close()
	t.Setenv("KB_LICENSE_SERVER", srv.URL)
	var out bytes.Buffer
	err := sendMaintenanceReport(context.Background(), &out, f.root, f.session)
	if err == nil || !strings.Contains(err.Error(), "credential") {
		t.Fatalf("report without saved credential = %v; want credential error", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("report without credential sent %d HTTP requests", got)
	}
}

func TestPendingCloseBlocksAnotherReportWithoutHTTPRequest(t *testing.T) {
	f := newMaintenanceReportFixture(t, false)
	saveMaintenanceTestCredential(t, f)
	session, err := kbstate.ReadMaintenance(f.root)
	if err != nil {
		t.Fatal(err)
	}
	session.SourceState = kbstate.StateArchive
	session.PendingClose = &kbstate.PendingReport{Seq: session.LastSeq + 1, Body: json.RawMessage(`{"seq":1,"payload_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","observed_at":"2026-01-01T00:00:00Z","source_state":"archive"}`)}
	if err := kbstate.WriteMaintenance(f.root, session); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer srv.Close()
	t.Setenv("KB_LICENSE_SERVER", srv.URL)
	var out bytes.Buffer
	err = sendMaintenanceReport(context.Background(), &out, f.root, session)
	if err == nil || !strings.Contains(err.Error(), "close is pending") {
		t.Fatalf("report with pending close = %v; want close-pending refusal", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("report during pending close sent %d HTTP requests", got)
	}
}

func TestMaintenanceStatusIsOfflineAndLeavesFilesUnchanged(t *testing.T) {
	f := newMaintenanceReportFixture(t, false)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer srv.Close()
	t.Setenv("KB_LICENSE_SERVER", srv.URL)
	beforeBench := testutil.BenchDigest(t, f.root)
	beforeKB := testutil.TreeDigest(t, kbstate.Dir(f.root))
	var out bytes.Buffer
	if err := runStatus(&out); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("offline status sent %d HTTP requests", got)
	}
	if got := testutil.BenchDigest(t, f.root); got != beforeBench {
		t.Errorf("status changed apps/sites: before %s after %s", beforeBench, got)
	}
	if got := testutil.TreeDigest(t, kbstate.Dir(f.root)); got != beforeKB {
		t.Errorf("status changed .kb state: before %s after %s", beforeKB, got)
	}
}

func TestMaintenanceFinishRefusalsLeaveSessionAndReceiptUntouched(t *testing.T) {
	for _, tc := range []struct {
		name      string
		makeDirty bool
		makeAhead bool
	}{
		{name: "dirty tree", makeDirty: true},
		{name: "unpublished commit", makeAhead: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMaintenanceReportFixture(t, false)
			saveMaintenanceTestCredential(t, f)
			if tc.makeDirty {
				write(t, filepath.Join(f.appDir, "README.md"), "uncommitted edit\n")
			}
			if tc.makeAhead {
				write(t, filepath.Join(f.appDir, "README.md"), "unpublished support edit\n")
				runMaintenanceTestGit(t, f.appDir, "add", "README.md")
				runMaintenanceTestGit(t, f.appDir, "commit", "-q", "-m", "not pushed")
			}
			beforeSession, err := os.ReadFile(kbstate.MaintenancePath(f.root))
			if err != nil {
				t.Fatal(err)
			}
			receiptPath := kbstate.ReceiptPath(f.root, "kb_compta")
			beforeReceipt, err := os.ReadFile(receiptPath)
			if err != nil {
				t.Fatal(err)
			}
			beforeInfo, err := os.Stat(receiptPath)
			if err != nil {
				t.Fatal(err)
			}
			err = maintenanceFinish(context.Background(), io.Discard, f.root, "none")
			if err == nil {
				t.Fatal("finish accepted dirty or unpushed source")
			}
			if tc.makeDirty && !strings.Contains(err.Error(), "clean tree") {
				t.Fatalf("dirty finish error = %v", err)
			}
			if tc.makeAhead && !strings.Contains(err.Error(), "remote") {
				t.Fatalf("unpushed finish error = %v", err)
			}
			afterSession, err := os.ReadFile(kbstate.MaintenancePath(f.root))
			if err != nil || !bytes.Equal(afterSession, beforeSession) {
				t.Fatalf("finish refusal changed session: err=%v", err)
			}
			afterReceipt, err := os.ReadFile(receiptPath)
			if err != nil || !bytes.Equal(afterReceipt, beforeReceipt) {
				t.Fatalf("finish refusal changed receipt: err=%v", err)
			}
			afterInfo, err := os.Stat(receiptPath)
			if err != nil || !os.SameFile(beforeInfo, afterInfo) {
				t.Fatalf("finish refusal replaced receipt file: err=%v", err)
			}
		})
	}
}

func TestPendingReleaseFinishIsIdempotentWithoutReceiptWrite(t *testing.T) {
	f := newMaintenanceReportFixture(t, false)
	saveMaintenanceTestCredential(t, f)
	bare := filepath.Join(t.TempDir(), "origin.git")
	if out, err := exec.Command("git", "init", "--bare", "-q", bare).CombinedOutput(); err != nil {
		t.Fatalf("init bare origin: %v\n%s", err, out)
	}
	runMaintenanceTestGit(t, f.appDir, "remote", "add", "origin", bare)
	runMaintenanceTestGit(t, f.appDir, "push", "-q", "origin", "HEAD:refs/heads/"+f.branch)
	approvedURL := "https://github.com/KB-Developpement/kb_compta.git"
	runMaintenanceTestGit(t, f.appDir, "remote", "set-url", "origin", approvedURL)
	installMaintenanceFetchShim(t, bare, approvedURL)
	session, err := kbstate.ReadMaintenance(f.root)
	if err != nil {
		t.Fatal(err)
	}
	session.SourceState = "pending_release"
	session.Apps[0].Head = f.base
	session.LastSeq = 7
	if err := kbstate.WriteMaintenance(f.root, session); err != nil {
		t.Fatal(err)
	}
	receiptPath := kbstate.ReceiptPath(f.root, "kb_compta")
	receiptState := kbstate.ReadReceipt(f.root, "kb_compta")
	if !receiptState.Valid() {
		t.Fatalf("pending-release receipt invalid: %s", receiptState.Reason)
	}
	receiptState.Receipt.State = "pending_release"
	if err := kbstate.WriteReceipt(f.root, receiptState.Receipt); err != nil {
		t.Fatal(err)
	}
	beforeSession, err := os.ReadFile(kbstate.MaintenancePath(f.root))
	if err != nil {
		t.Fatal(err)
	}
	beforeReceipt, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeReceiptInfo, err := os.Stat(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := maintenanceFinish(context.Background(), io.Discard, f.root, "none"); err != nil {
			t.Fatalf("finish attempt %d: %v", attempt+1, err)
		}
	}
	afterSession, err := os.ReadFile(kbstate.MaintenancePath(f.root))
	if err != nil || !bytes.Equal(afterSession, beforeSession) {
		t.Fatalf("idempotent finish changed session: err=%v", err)
	}
	afterReceipt, err := os.ReadFile(receiptPath)
	if err != nil || !bytes.Equal(afterReceipt, beforeReceipt) {
		t.Fatalf("idempotent finish changed receipt: err=%v", err)
	}
	afterReceiptInfo, err := os.Stat(receiptPath)
	if err != nil || !os.SameFile(beforeReceiptInfo, afterReceiptInfo) {
		t.Fatalf("idempotent finish rewrote receipt: err=%v", err)
	}
	final, err := kbstate.ReadMaintenance(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if final.LastSeq != 7 || final.PendingReport != nil {
		t.Fatalf("idempotent finish advanced reporting state: seq %d pending %+v", final.LastSeq, final.PendingReport)
	}
}

func TestMaintenanceStatusStaleBoundaryIsThirtyMinutes(t *testing.T) {
	f := newMaintenanceReportFixture(t, false)
	session, err := kbstate.ReadMaintenance(f.root)
	if err != nil {
		t.Fatal(err)
	}
	session.LastReportAt = "2026-04-05T12:00:00Z"
	if err := kbstate.WriteMaintenance(f.root, session); err != nil {
		t.Fatal(err)
	}
	oldNow := maintenanceNow
	t.Cleanup(func() { maintenanceNow = oldNow })
	for _, tc := range []struct {
		name string
		now  string
		want bool
	}{
		{name: "before boundary", now: "2026-04-05T12:29:59Z", want: false},
		{name: "at boundary", now: "2026-04-05T12:30:00Z", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now, err := time.Parse(time.RFC3339, tc.now)
			if err != nil {
				t.Fatal(err)
			}
			maintenanceNow = func() time.Time { return now }
			var out bytes.Buffer
			printMaintenanceStatus(&out, f.root)
			got := strings.Contains(out.String(), "Stale: state unknown")
			if got != tc.want {
				t.Fatalf("stale line = %v at %s; output:\n%s", got, tc.now, out.String())
			}
		})
	}
}
