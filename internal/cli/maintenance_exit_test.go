package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/KB-Developpement/kb_pro_cli/internal/bench"
	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
	"github.com/KB-Developpement/kb_pro_cli/internal/testutil"
)

type maintenanceExitFixture struct {
	root, appDir, serverURL, release, target string
	f                                        *maintenanceReportFixture
	mu                                       sync.Mutex
	reports                                  []license.MaintenanceReport
	downloads, closes                        int
	closeStatus                              int
	badTagCommit                             bool
}

func newMaintenanceExitFixture(t *testing.T, bothAffected bool) *maintenanceExitFixture {
	t.Helper()
	f := newMaintenanceReportFixture(t, true)
	saveMaintenanceTestCredential(t, f)
	write(t, filepath.Join(f.root, "sites", "site2", "site_config.json"), "{}")
	write(t, filepath.Join(f.appDir, "pyproject.toml"), "[project]\nname = 'kb_compta'\n")
	runMaintenanceTestGit(t, f.appDir, "add", "pyproject.toml")
	runMaintenanceTestGit(t, f.appDir, "commit", "-q", "-m", "package metadata")
	f.head = runMaintenanceTestGit(t, f.appDir, "rev-parse", "HEAD")
	commits, err := bench.MaintenanceCommits(f.root, "kb_compta", f.base)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(commits)
	refs, err := bench.ProtectMaintenanceCommits(f.root, "kb_compta", f.sessionID, commits)
	if err != nil {
		t.Fatal(err)
	}
	f.session.Apps[0].Head = f.head
	f.session.Apps[0].ReportedHead = f.head
	f.session.Apps[0].Protected = commits
	f.session.Apps[0].ProtectionRefs = refs
	f.session.PatchSnapshot = map[string][]string{"site1": {}, "site2": {}}
	for _, site := range []string{"site1", "site2"} {
		for _, artifact := range []string{"database.sql.gz", "public-files.tar", "private-files.tar"} {
			path := filepath.Join(".kb", "maintenance", "backups", "entry", site, artifact)
			write(t, filepath.Join(f.root, path), "verified entry backup")
			f.session.Backups = append(f.session.Backups, kbstate.MaintenanceBackup{Site: site, Path: path, TakenAt: "2026-01-01T00:00:00Z"})
		}
	}
	if err = kbstate.WriteMaintenance(f.root, f.session); err != nil {
		t.Fatal(err)
	}
	receipt := kbstate.ReadReceipt(f.root, "kb_compta")
	receipt.Receipt.Protected = commits
	receipt.Receipt.ProtectionRefs = refs
	if err = kbstate.WriteReceipt(f.root, receipt.Receipt); err != nil {
		t.Fatal(err)
	}
	if err = bench.SetPreparedMaintenanceOwner(filepath.Join(f.appDir, ".git"), f.sessionID); err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(t.TempDir(), "origin.git")
	runMaintenanceTestGit(t, f.root, "clone", "--bare", f.appDir, remote)
	runMaintenanceTestGit(t, f.appDir, "remote", "add", "origin", "https://github.com/KB-Developpement/kb_compta.git")
	releaseWork := filepath.Join(t.TempDir(), "release")
	runMaintenanceTestGit(t, f.root, "clone", remote, releaseWork)
	runMaintenanceTestGit(t, releaseWork, "config", "user.name", "Maintenance Test")
	runMaintenanceTestGit(t, releaseWork, "config", "user.email", "maintenance-test@example.invalid")
	runMaintenanceTestGit(t, releaseWork, "checkout", "-b", "erpnext")
	write(t, filepath.Join(releaseWork, "kb_compta", "__init__.py"), "__version__ = '1.0.1'\n")
	runMaintenanceTestGit(t, releaseWork, "add", "kb_compta/__init__.py")
	runMaintenanceTestGit(t, releaseWork, "commit", "-q", "-m", "released fix")
	target := runMaintenanceTestGit(t, releaseWork, "rev-parse", "HEAD")
	runMaintenanceTestGit(t, releaseWork, "tag", "v1.0.1")
	runMaintenanceTestGit(t, releaseWork, "push", "-q", "origin", "erpnext", "refs/tags/v1.0.1")
	archiveCmd := exec.Command("git", "-C", releaseWork, "archive", "--format=tar.gz", "--prefix=release/", target)
	archive, err := archiveCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	installMaintenanceFetchShim(t, remote, "https://github.com/KB-Developpement/kb_compta.git")
	fixture := &maintenanceExitFixture{root: f.root, appDir: f.appDir, release: "kb_compta=v1.0.1", target: target, f: f, closeStatus: http.StatusOK}
	priorRefresh := maintenanceRefresh
	t.Cleanup(func() { maintenanceRefresh = priorRefresh })
	maintenanceRefresh = func(context.Context) (string, string, string, map[string]bool, error) {
		return "exit-test-token", f.fingerprint, f.session.LicenseKeySHA256, map[string]bool{"kb_compta": true}, nil
	}
	installMaintenanceExitTools(t, f.root, bothAffected)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		if r.Header.Get("X-KB-Maintenance-Session") != f.credential {
			t.Errorf("missing maintenance credential at %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/download/kb_compta":
			fixture.downloads++
			if r.Method != http.MethodGet || r.URL.Query().Get("v") != "v1.0.1" || r.URL.Query().Get("release") != "1" || r.Header.Get("Authorization") != "Bearer exit-test-token" {
				t.Errorf("strict download identity: %s %s", r.Method, r.URL)
			}
			commit := fixture.target
			if fixture.badTagCommit {
				commit = f.base
			}
			w.Header().Set("X-KB-Repository", "KB-Developpement/kb_compta")
			w.Header().Set("X-KB-Ref", "v1.0.1")
			w.Header().Set("X-KB-Commit", commit)
			w.Header().Set("X-KB-Release-Tag", "v1.0.1")
			_, _ = w.Write(archive)
		case "/maintenance/report":
			var report license.MaintenanceReport
			if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
				t.Errorf("decode report: %v", err)
				http.Error(w, "bad report", 400)
				return
			}
			fixture.reports = append(fixture.reports, report)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(license.ReportReply{AckSeq: report.Seq, ApprovalState: "redeemed", WorkUntil: "2099-01-01T00:00:00Z"})
		case "/maintenance/close":
			fixture.closes++
			if _, err := license.LoadMaintenanceCredential(f.sessionID); err != nil {
				t.Errorf("credential removed before close acknowledgement: %v", err)
			}
			if fixture.closeStatus != http.StatusOK {
				w.WriteHeader(fixture.closeStatus)
				_, _ = fmt.Fprint(w, `{"error":"missing_release_proof"}`)
				return
			}
			var close license.MaintenanceClose
			if err := json.NewDecoder(r.Body).Decode(&close); err != nil || close.SourceState != "archive" {
				t.Errorf("invalid close %+v %v", close, err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"status":"closed","closed_at":"2026-01-01T00:00:00Z"}`)
		default:
			t.Errorf("unexpected HTTP request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	fixture.serverURL = server.URL
	t.Setenv("KB_LICENSE_SERVER", server.URL)
	return fixture
}

func (f *maintenanceExitFixture) run(ctx context.Context, w io.Writer) error {
	return maintenanceExit(ctx, w, f.root, f.release, "none", true)
}
func (f *maintenanceExitFixture) reportStates() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	states := []string{}
	for _, r := range f.reports {
		states = append(states, r.SourceState)
	}
	return states
}

func installMaintenanceExitTools(t *testing.T, root string, bothAffected bool) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/usr/bin/env python3
import os,sys,json
args=sys.argv[1:]
with open(os.environ['KB_EXIT_COMMANDS'],'a') as f:f.write(json.dumps(args)+'\n')
if args==['--version']:print('5.31.0')
elif 'list-apps' in args:
 site=args[1];apps=['frappe','kb_pro']
 if site=='site1' or os.environ.get('KB_EXIT_BOTH')=='1':apps.append('kb_compta')
 print(json.dumps({site:apps}))
elif 'execute' in args:print('[]')
elif 'backup' in args:
 for flag in ['--backup-path-db','--backup-path-files','--backup-path-private-files','--backup-path-conf']:
  path=args[args.index(flag)+1]
  with open(path,'wb') as f:f.write(b'fresh backup')
elif 'requirements' in args and os.environ.get('KB_EXIT_FAIL') in ['setup','restore']:
 sys.exit(1)
elif 'migrate' in args and args[1]==os.environ.get('KB_EXIT_FAIL'):sys.exit(1)
`
	if err := os.WriteFile(filepath.Join(bin, "bench"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	python := filepath.Join(root, "env", "bin", "python")
	if err := os.MkdirAll(filepath.Dir(python), 0755); err != nil {
		t.Fatal(err)
	}
	pipScript := `#!/usr/bin/env python3
import os,sys,json
with open(os.environ['KB_EXIT_COMMANDS'],'a') as f:f.write(json.dumps(['python']+sys.argv[1:])+'\n')
if os.environ.get('KB_EXIT_FAIL')=='restore':sys.exit(1)
`
	if err := os.WriteFile(python, []byte(pipScript), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KB_EXIT_COMMANDS", filepath.Join(root, "exit-commands.jsonl"))
	if bothAffected {
		t.Setenv("KB_EXIT_BOTH", "1")
	} else {
		t.Setenv("KB_EXIT_BOTH", "0")
	}
}
func maintenanceExitCommands(t *testing.T, root string) [][]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "exit-commands.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	commands := [][]string{}
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var args []string
		if err = json.Unmarshal(line, &args); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, args)
	}
	return commands
}
func maintenanceExitMigrations(t *testing.T, root string) []string {
	t.Helper()
	sites := []string{}
	for _, args := range maintenanceExitCommands(t, root) {
		if len(args) == 3 && args[0] == "--site" && args[2] == "migrate" {
			sites = append(sites, args[1])
		}
	}
	return sites
}

func TestMaintenanceExitIntegratedReleasedArchive(t *testing.T) {
	f := newMaintenanceExitFixture(t, false)
	var out bytes.Buffer
	if err := f.run(context.Background(), &out); err != nil {
		t.Fatalf("first exit failure: %v; output %s", err, out.String())
	}
	s, err := kbstate.ReadMaintenance(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if s.SourceState != "archive" || s.ApprovalState != "closed" || s.Pending != nil || s.PendingClose != nil {
		t.Fatalf("completed session %+v", s)
	}
	if !reflect.DeepEqual(s.MigrationOutcomes, map[string]string{"site1": "ok"}) {
		t.Fatalf("completed migration outcomes lost: %v", s.MigrationOutcomes)
	}
	retained, err := os.ReadFile(filepath.Join(kbstate.RecoveryDir(f.root), s.SessionID, "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	var archived kbstate.MaintenanceSession
	if err := json.Unmarshal(retained, &archived); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(archived.MigrationOutcomes, s.MigrationOutcomes) || archived.ApprovalState != "closed" {
		t.Fatal("closed session audit lost migration outcomes")
	}
	r := kbstate.ReadReceipt(f.root, "kb_compta")
	if !r.Valid() || r.Receipt.State != "archive" || r.Receipt.Commit != f.target || r.Receipt.ReleaseTag == nil || *r.Receipt.ReleaseTag != "v1.0.1" || len(r.Receipt.Protected) != 0 || r.Receipt.SessionID != nil {
		t.Fatalf("archive receipt %+v", r)
	}
	if _, err = os.Lstat(filepath.Join(f.appDir, ".git")); !os.IsNotExist(err) {
		t.Fatalf("archive still Git: %v", err)
	}
	if got := maintenanceExitMigrations(t, f.root); !reflect.DeepEqual(got, []string{"site1"}) {
		t.Fatalf("migrate sites %v", got)
	}
	if len(s.Backups) != 9 {
		t.Fatalf("backups %d, want 6 entry artifacts plus affected site's 3 fresh artifacts", len(s.Backups))
	}
	for _, b := range s.Backups[6:] {
		if b.Site != "site1" {
			t.Fatalf("exit backed up unaffected site %s", b.Site)
		}
	}
	for _, b := range s.Backups {
		fi, err := os.Stat(filepath.Join(f.root, b.Path))
		if err != nil || fi.Size() == 0 {
			t.Fatalf("backup %v %v", b, err)
		}
	}
	if len(s.Recovery) < 2 {
		t.Fatalf("recovery paths %v", s.Recovery)
	}
	for _, p := range []string{s.Recovery[0], s.Recovery[1]} {
		if _, err = os.Stat(filepath.Join(f.root, p)); err != nil {
			t.Fatalf("retained path %s: %v", p, err)
		}
	}
	if _, err = license.LoadMaintenanceCredential(f.f.sessionID); err == nil {
		t.Fatal("credential retained after confirmed close")
	}
	if got := f.reportStates(); !reflect.DeepEqual(got, []string{"returning", "archive"}) {
		t.Fatalf("reported states %v", got)
	}
	f.mu.Lock()
	closes := f.closes
	downloads := f.downloads
	f.mu.Unlock()
	if closes != 1 || downloads != 1 {
		t.Fatalf("download/close %d/%d", downloads, closes)
	}
}

func TestMaintenanceExitRefusedTagPeelDoesNotMutateSource(t *testing.T) {
	f := newMaintenanceExitFixture(t, false)
	f.badTagCommit = true
	before := testutil.TreeDigest(t, f.appDir)
	receipt, _ := os.ReadFile(kbstate.ReceiptPath(f.root, "kb_compta"))
	session, _ := os.ReadFile(kbstate.MaintenancePath(f.root))
	err := f.run(context.Background(), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "peel") {
		t.Fatalf("tag mismatch exit %v", err)
	}
	if got := testutil.TreeDigest(t, f.appDir); got != before {
		t.Fatal("tag refusal mutated source/.git")
	}
	after, _ := os.ReadFile(kbstate.ReceiptPath(f.root, "kb_compta"))
	if !bytes.Equal(receipt, after) {
		t.Fatal("tag refusal changed receipt")
	}
	after, _ = os.ReadFile(kbstate.MaintenancePath(f.root))
	if !bytes.Equal(session, after) {
		t.Fatal("tag refusal changed session")
	}
	if len(f.reportStates()) != 0 {
		t.Fatal("tag refusal reported mutation")
	}
	if migrations := maintenanceExitMigrations(t, f.root); len(migrations) != 0 {
		t.Fatal(migrations)
	}
}

func TestMaintenanceExitPreMigrationRestoresExactSourceAndReceipt(t *testing.T) {
	f := newMaintenanceExitFixture(t, false)
	t.Setenv("KB_EXIT_FAIL", "setup")
	before := testutil.TreeDigest(t, f.appDir)
	receipt, _ := os.ReadFile(kbstate.ReceiptPath(f.root, "kb_compta"))
	err := f.run(context.Background(), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "restored and verified") {
		t.Fatalf("setup failure: %v", err)
	}
	if got := testutil.TreeDigest(t, f.appDir); got != before {
		t.Fatal("pre-migration rollback changed complete source")
	}
	after, _ := os.ReadFile(kbstate.ReceiptPath(f.root, "kb_compta"))
	if !bytes.Equal(receipt, after) {
		t.Fatal("rollback changed receipt bytes")
	}
	s, readErr := kbstate.ReadMaintenance(f.root)
	if readErr != nil || s.SourceState != "active" || s.Pending != nil || s.SchemaChanged {
		t.Fatalf("restored session %+v %v", s, readErr)
	}
	if got := maintenanceExitMigrations(t, f.root); len(got) != 0 {
		t.Fatalf("setup failure ran migrations %v", got)
	}
	if got := f.reportStates(); !reflect.DeepEqual(got, []string{"returning", "active"}) {
		t.Fatalf("rollback reports %v", got)
	}
}

func TestMaintenanceExitFailedEditableRestoreKeepsRecovery(t *testing.T) {
	f := newMaintenanceExitFixture(t, false)
	t.Setenv("KB_EXIT_FAIL", "restore")
	err := f.run(context.Background(), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "recovery_needed") {
		t.Fatalf("editable restore failure: %v", err)
	}
	s, readErr := kbstate.ReadMaintenance(f.root)
	if readErr != nil || s.SourceState != "recovery_needed" || s.Pending == nil {
		t.Fatalf("failed restore state %+v %v", s, readErr)
	}
	r := kbstate.ReadReceipt(f.root, "kb_compta")
	if !r.Valid() || r.Receipt.State != "returning" {
		t.Fatalf("failed restore receipt %+v", r)
	}
	if _, err = os.Stat(filepath.Join(f.root, s.Recovery[0])); err != nil {
		t.Fatal(err)
	}
	if got := maintenanceExitMigrations(t, f.root); len(got) != 0 {
		t.Fatal(got)
	}
	if got := f.reportStates(); !reflect.DeepEqual(got, []string{"returning", "recovery_needed"}) {
		t.Fatalf("failed restore report %v", got)
	}
}

func TestMaintenanceExitPartialSiteMigrationRetainsBothSourceSets(t *testing.T) {
	f := newMaintenanceExitFixture(t, true)
	t.Setenv("KB_EXIT_FAIL", "site2")
	err := f.run(context.Background(), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "recovery_needed") {
		t.Fatalf("second migration failure: %v", err)
	}
	s, readErr := kbstate.ReadMaintenance(f.root)
	if readErr != nil || s.SourceState != "recovery_needed" || s.Pending == nil || !s.SchemaChanged {
		t.Fatalf("migration recovery %+v %v", s, readErr)
	}
	if got := s.MigrationOutcomes; !reflect.DeepEqual(got, map[string]string{"site1": "ok", "site2": "failed"}) {
		t.Fatalf("migration outcomes %v", got)
	}
	if got := maintenanceExitMigrations(t, f.root); !reflect.DeepEqual(got, []string{"site1", "site2"}) {
		t.Fatalf("migration invocation %v", got)
	}
	for _, p := range []string{s.Recovery[0], s.Recovery[1]} {
		if _, err = os.Stat(filepath.Join(f.root, p)); err != nil {
			t.Fatalf("source retention %s: %v", p, err)
		}
	}
	if _, err = os.Lstat(filepath.Join(f.appDir, ".git")); !os.IsNotExist(err) {
		t.Fatal("post-migration source automatically rolled back")
	}
	r := kbstate.ReadReceipt(f.root, "kb_compta")
	if !r.Valid() || r.Receipt.State != "returning" {
		t.Fatalf("recovery receipt %+v", r)
	}
	states := f.reportStates()
	if !reflect.DeepEqual(states, []string{"returning", "recovery_needed"}) {
		t.Fatalf("archive live source recovery report %v", states)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.reports[len(f.reports)-1].SchemaChanged || !f.reports[len(f.reports)-1].Apps[0].Dirty {
		t.Fatal("recovery report falsely claims known clean source")
	}
	if f.closes != 0 {
		t.Fatal("failed migration closed server session")
	}
}

func TestMaintenanceExitCloseFailureRetainsCredentialAndRetrySkipsSource(t *testing.T) {
	f := newMaintenanceExitFixture(t, false)
	f.mu.Lock()
	f.closeStatus = http.StatusConflict
	f.mu.Unlock()
	err := f.run(context.Background(), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "missing_release_proof") {
		t.Fatalf("first close failure: %v", err)
	}
	s, readErr := kbstate.ReadMaintenance(f.root)
	if readErr != nil || s.SourceState != "archive" || s.PendingClose == nil || s.ApprovalState == "closed" {
		t.Fatalf("pending close state %+v %v", s, readErr)
	}
	if _, err = license.LoadMaintenanceCredential(f.f.sessionID); err != nil {
		t.Fatalf("credential removed before confirmed close: %v", err)
	}
	sourceBefore := testutil.TreeDigest(t, f.appDir)
	commandsBefore := maintenanceExitCommands(t, f.root)
	f.mu.Lock()
	f.closeStatus = http.StatusOK
	f.mu.Unlock()
	if err = f.run(context.Background(), io.Discard); err != nil {
		t.Fatalf("close retry: %v", err)
	}
	if got := testutil.TreeDigest(t, f.appDir); got != sourceBefore {
		t.Fatal("close retry mutated source")
	}
	if got := maintenanceExitCommands(t, f.root); !reflect.DeepEqual(got, commandsBefore) {
		t.Fatal("close retry reran native bench steps")
	}
	if _, err = license.LoadMaintenanceCredential(f.f.sessionID); err == nil {
		t.Fatal("credential retained after successful retry")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.downloads != 1 || f.closes != 2 {
		t.Fatalf("retry downloads/closes %d/%d", f.downloads, f.closes)
	}
}
