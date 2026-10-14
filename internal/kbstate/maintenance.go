package kbstate

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/KB-Developpement/kb_pro_cli/internal/fsutil"
	"github.com/KB-Developpement/kb_pro_cli/internal/ident"
)

var MaintenanceIDRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

// NewMaintenanceID returns a cryptographically random bench or request identifier.
func NewMaintenanceID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func MaintenancePath(root string) string {
	return filepath.Join(Dir(root), "maintenance", "session.json")
}

// EnsureBenchID creates an identity exactly once. Existing malformed identities
// are never replaced: that would detach local source from its server approval.
func EnsureBenchID(root string) (string, error) {
	path := filepath.Join(Dir(root), "bench-id")
	if raw, err := os.ReadFile(path); err == nil {
		var v struct {
			SchemaVersion int    `json:"schema_version"`
			BenchID       string `json:"bench_id"`
		}
		if json.Unmarshal(raw, &v) != nil || v.SchemaVersion != SupportedSchema || !MaintenanceIDRE.MatchString(v.BenchID) {
			return "", fmt.Errorf("invalid bench identity at %s", path)
		}
		return v.BenchID, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := EnsureKB(root); err != nil {
		return "", err
	}
	id, err := NewMaintenanceID()
	if err != nil {
		return "", err
	}
	raw, _ := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		BenchID       string `json:"bench_id"`
	}{SupportedSchema, id})
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return EnsureBenchID(root)
	}
	if err != nil {
		return "", err
	}
	_, werr := f.Write(append(raw, '\n'))
	serr := f.Sync()
	cerr := f.Close()
	if werr != nil {
		return "", werr
	}
	if serr != nil {
		return "", serr
	}
	if cerr != nil {
		return "", cerr
	}
	d, err := os.Open(Dir(root))
	if err != nil {
		return "", err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return "", err
	}
	return id, nil
}

type MaintenanceApp struct {
	App             string   `json:"app"`
	Directory       string   `json:"directory"`
	Package         string   `json:"package"`
	Repository      string   `json:"repository"`
	Line            string   `json:"line"`
	BaseCommit      string   `json:"base_commit"`
	Branch          string   `json:"branch"`
	Head            string   `json:"head"`
	ReportedHead    string   `json:"reported_head,omitempty"`
	Protected       []string `json:"protected"`
	ProtectionRefs  []string `json:"protection_refs"`
	OriginalReceipt *Receipt `json:"original_receipt"`
}
type MaintenanceBackup struct {
	Site    string `json:"site"`
	Path    string `json:"path"`
	TakenAt string `json:"taken_at"`
}
type MaintenancePending struct {
	Op          string            `json:"op"`
	Step        string            `json:"step"`
	StartedAt   string            `json:"started_at"`
	Recovery    string            `json:"recovery,omitempty"`
	PreparedGit string            `json:"prepared_git,omitempty"`
	Expected    map[string]string `json:"expected"`
}

// PendingReport keeps the exact body across a crash or a lost acknowledgement.
// It is distinct from Pending, which protects a source operation.
type PendingReport struct {
	Seq  int64           `json:"seq"`
	Body json.RawMessage `json:"body"`
}
type MaintenanceSession struct {
	SchemaVersion     int                 `json:"schema_version"`
	SessionID         string              `json:"session_id"`
	BenchID           string              `json:"bench_id"`
	Fingerprint       string              `json:"fingerprint"`
	LicenseKeySHA256  string              `json:"license_key_sha256"`
	RequestID         string              `json:"request_id,omitempty"`
	RenewRequested    bool                `json:"renew_requested,omitempty"`
	ClientRequestID   string              `json:"client_request_id"`
	Reason            string              `json:"reason"`
	SourceState       string              `json:"source_state"`
	ApprovalState     string              `json:"approval_state"`
	WorkUntil         string              `json:"work_until,omitempty"`
	ApproveBy         string              `json:"approve_by,omitempty"`
	Apps              []MaintenanceApp    `json:"apps"`
	SchemaChanged     bool                `json:"schema_changed"`
	MigrationOutcomes map[string]string   `json:"migration_outcomes,omitempty"`
	PatchSnapshot     map[string][]string `json:"patch_snapshot"`
	Backups           []MaintenanceBackup `json:"backups"`
	LastSeq           int64               `json:"last_seq"`
	LastReportAt      string              `json:"last_report_at,omitempty"`
	Pending           *MaintenancePending `json:"pending"`
	PendingReport     *PendingReport      `json:"pending_report,omitempty"`
	PendingClose      *PendingReport      `json:"pending_close,omitempty"`
	Recovery          []string            `json:"recovery,omitempty"`
}

func validMaintenanceState(s string) bool {
	switch s {
	case "archive", "attaching", "active", "pending_release", "returning", "recovery_needed":
		return true
	}
	return false
}
func (s *MaintenanceSession) Validate() error {
	if s.SchemaVersion != SupportedSchema || !MaintenanceIDRE.MatchString(s.BenchID) || (s.SessionID != "" && !MaintenanceIDRE.MatchString(s.SessionID)) || !MaintenanceIDRE.MatchString(s.ClientRequestID) || !ident.SHA256RE.MatchString(s.Fingerprint) || !ident.SHA256RE.MatchString(s.LicenseKeySHA256) || !validMaintenanceState(s.SourceState) || len(s.Apps) != 1 || s.LastSeq < 0 {
		return errors.New("invalid maintenance session identity, version or source state")
	}
	a := s.Apps[0]
	if !safeComponent(a.Directory) || !safeComponent(a.App) || !safeComponent(a.Package) || !ident.RepositoryRE.MatchString(a.Repository) || a.Line != "1" || !ident.CommitRE.MatchString(a.BaseCommit) || !ident.CommitRE.MatchString(a.Head) || !ident.ValidRef(a.Branch) || len(a.Protected) > 64 || len(a.Protected) != len(a.ProtectionRefs) {
		return errors.New("invalid maintenance app mapping or protection")
	}
	for i, commit := range a.Protected {
		if !ident.CommitRE.MatchString(commit) || a.ProtectionRefs[i] != "refs/kb-maintenance/"+s.SessionID+"/"+commit {
			return errors.New("invalid protected commit or ref")
		}
	}
	if a.OriginalReceipt == nil || a.OriginalReceipt.Validate(a.Directory) != nil || a.OriginalReceipt.State != StateArchive {
		return errors.New("invalid original maintenance receipt")
	}
	if s.PendingReport != nil && (s.PendingReport.Seq != s.LastSeq+1 || !json.Valid(s.PendingReport.Body)) {
		return errors.New("invalid pending report")
	}
	if s.PendingClose != nil && (s.PendingReport != nil || s.SourceState != StateArchive || s.PendingClose.Seq != s.LastSeq+1 || !json.Valid(s.PendingClose.Body)) {
		return errors.New("invalid or overlapping pending close")
	}
	for _, pending := range []*PendingReport{s.PendingReport, s.PendingClose} {
		if pending == nil {
			continue
		}
		var body struct {
			Seq           int64  `json:"seq"`
			PayloadSHA256 string `json:"payload_sha256"`
			ObservedAt    string `json:"observed_at"`
			SourceState   string `json:"source_state"`
		}
		if json.Unmarshal(pending.Body, &body) != nil || body.Seq != pending.Seq || !ident.SHA256RE.MatchString(body.PayloadSHA256) || !validMaintenanceState(body.SourceState) {
			return errors.New("invalid persisted report or close body")
		}
		if _, err := time.Parse(time.RFC3339, body.ObservedAt); err != nil {
			return err
		}
	}
	for _, b := range s.Backups {
		if !safeComponent(b.Site) || !safeRelative(b.Path) {
			return errors.New("invalid backup path")
		}
		if _, err := time.Parse(time.RFC3339, b.TakenAt); err != nil {
			return err
		}
	}
	for site, outcome := range s.MigrationOutcomes {
		if !safeComponent(site) || !s.SchemaChanged {
			return errors.New("invalid migration outcome identity or schema flag")
		}
		switch outcome {
		case "unknown", "ok", "failed":
		default:
			return errors.New("invalid migration outcome")
		}
	}
	for _, p := range s.Recovery {
		if !safeRelative(p) || !strings.HasPrefix(filepath.ToSlash(p), ".kb/recovery/") {
			return errors.New("invalid recovery path")
		}
	}
	if p := s.Pending; p != nil {
		switch p.Op {
		case "enter", "finish", "exit", "recovery":
		default:
			return errors.New("invalid pending operation")
		}
		if p.Recovery != "" && (!safeRelative(p.Recovery) || !strings.HasPrefix(filepath.ToSlash(p.Recovery), ".kb/recovery/")) {
			return errors.New("invalid pending recovery path")
		}
		if p.PreparedGit != "" && (!safeRelative(p.PreparedGit) || !strings.HasPrefix(filepath.ToSlash(p.PreparedGit), ".kb/")) {
			return errors.New("invalid prepared Git path")
		}
	}
	return nil
}
func safeComponent(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00\n\r")
}
func safeRelative(p string) bool {
	c := filepath.Clean(p)
	return p != "" && !filepath.IsAbs(p) && c != "." && c != ".." && !strings.HasPrefix(c, ".."+string(filepath.Separator)) && c == p
}

// ReadMaintenance returns nil only when no session file exists. Corrupt state
// fails closed; readers can display the error without changing the file.
func ReadMaintenance(root string) (*MaintenanceSession, error) {
	raw, err := os.ReadFile(MaintenancePath(root))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s MaintenanceSession
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("maintenance recovery_needed: %s is malformed", MaintenancePath(root))
	}
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("maintenance recovery_needed: %s: %w", MaintenancePath(root), err)
	}
	return &s, nil
}
func WriteMaintenance(root string, s *MaintenanceSession) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if err := EnsureTree(root, filepath.Dir(MaintenancePath(root))); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileDurable(MaintenancePath(root), append(raw, '\n'), 0600)
}

// ArchiveMaintenanceSession retains completed outcomes before a later request
// replaces the bench's current session file. It contains no credential.
func ArchiveMaintenanceSession(root string, s *MaintenanceSession) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if !MaintenanceIDRE.MatchString(s.SessionID) || s.SourceState != StateArchive || s.ApprovalState != "closed" || s.Pending != nil || s.PendingReport != nil || s.PendingClose != nil {
		return errors.New("only a fully closed archive session can be retained")
	}
	dir := filepath.Join(RecoveryDir(root), s.SessionID)
	if err := EnsureTree(root, dir); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	path := filepath.Join(dir, "session.json")
	verifyExisting := func() error {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return fmt.Errorf("completed session record must be a regular file with mode 0600 at %s", path)
		}
		existing, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(existing, raw) {
			return fmt.Errorf("completed session record already differs at %s", path)
		}
		return fsutil.SyncDir(dir)
	}
	if _, err := os.Lstat(path); err == nil {
		return verifyExisting()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temp, err := os.CreateTemp(dir, ".session.json.tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(raw); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	// A hard link publishes the complete file atomically without replacing an
	// existing audit record, including one that appears after the preflight.
	if err := os.Link(temp.Name(), path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return verifyExisting()
		}
		return err
	}
	return fsutil.SyncDir(dir)
}

// MaintenanceGate refuses ordinary mutations before journal replay or cleanup.
// Receipts are checked too, so a missing session cannot unlock protected source.
func MaintenanceGate(root string) error {
	s, err := ReadMaintenance(root)
	if err != nil {
		return err
	}
	if s != nil && (s.SourceState != StateArchive || s.Pending != nil) {
		return fmt.Errorf("maintenance %s protects this whole bench; use kb maintenance status, report, finish or exit (%s)", s.SourceState, MaintenancePath(root))
	}
	paths, err := filepath.Glob(filepath.Join(ReceiptsDir(root), "*.json"))
	if err != nil {
		return err
	}
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var probe struct {
			State     string  `json:"state"`
			SessionID *string `json:"session_id"`
		}
		if json.Unmarshal(raw, &probe) != nil {
			continue
		} // Ordinary legacy unknown receipts remain usable.
		if probe.State != "" && probe.State != StateArchive || probe.SessionID != nil {
			return fmt.Errorf("maintenance receipt %s protects this whole bench", p)
		}
	}
	return nil
}
