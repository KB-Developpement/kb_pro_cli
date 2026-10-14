package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/KB-Developpement/kb_pro_cli/internal/apps"
	"github.com/KB-Developpement/kb_pro_cli/internal/bench"
	"github.com/KB-Developpement/kb_pro_cli/internal/config"
	"github.com/KB-Developpement/kb_pro_cli/internal/fault"
	"github.com/KB-Developpement/kb_pro_cli/internal/ident"
	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
	"github.com/KB-Developpement/kb_pro_cli/internal/version"
	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
)

var maintenanceNow = time.Now
var maintenanceRefresh = license.RefreshMaintenanceLicense
var maintenanceFingerprint = license.Fingerprint
var maintenanceCodePrompt = func() (string, error) {
	if globalFlags.NoInput {
		return "", errors.New("enter requires an interactive no-echo entry-code prompt; codes are never accepted on argv")
	}
	var code string
	err := huh.NewForm(huh.NewGroup(huh.NewInput().Title("Maintenance entry code").EchoMode(huh.EchoModePassword).Value(&code))).WithKeyMap(formKeyMap()).Run()
	return code, err
}

func newMaintenanceCmd() *cobra.Command {
	parent := &cobra.Command{Use: "maintenance", Short: "Maintain one approved app with native Git and return to a verified release", Annotations: map[string]string{"skipChecks": "true"}, Args: cobra.NoArgs}
	var appFlag, reason string
	var renew bool
	request := &cobra.Command{Use: "request", Short: "Request approval for one READY app, or renew the frozen session", Args: cobra.NoArgs, Annotations: map[string]string{"skipChecks": "true"}, RunE: func(c *cobra.Command, _ []string) error {
		return maintenanceLocked(c.Context(), func(root string) error {
			return maintenanceRequest(c.Context(), c.OutOrStdout(), root, appFlag, reason, renew)
		})
	}}
	request.Flags().StringVar(&appFlag, "apps", "", "Exactly one READY non-framework app (kb_compta or kb_stock)")
	request.Flags().StringVar(&reason, "reason", "", "Reason for maintenance (1 to 200 bytes)")
	request.Flags().BoolVar(&renew, "renew", false, "Request a fresh approval for the existing frozen session")
	var branch string
	enter := &cobra.Command{Use: "enter", Short: "Redeem approval and attach Git without replacing working files", Args: cobra.NoArgs, Annotations: map[string]string{"skipChecks": "true"}, RunE: func(c *cobra.Command, _ []string) error {
		return maintenanceLocked(c.Context(), func(root string) error { return maintenanceEnter(c.Context(), c.OutOrStdout(), root, branch) })
	}}
	enter.Flags().StringVar(&branch, "branch", "", "Support branch name (defaults to support/<session_id>)")
	status := &cobra.Command{Use: "status", Short: "Inspect maintenance offline without locks or writes", Args: cobra.NoArgs, Annotations: map[string]string{"skipChecks": "true"}, RunE: func(c *cobra.Command, _ []string) error { return runStatus(c.OutOrStdout()) }}
	report := &cobra.Command{Use: "report", Short: "Durably report the observed source state", Args: cobra.NoArgs, Annotations: map[string]string{"skipChecks": "true"}, RunE: func(c *cobra.Command, _ []string) error {
		return maintenanceLocked(c.Context(), func(root string) error {
			s, e := openMaintenance(root)
			if e != nil {
				return e
			}
			if s.Pending != nil {
				s.SourceState = "recovery_needed"
				if err := kbstate.WriteMaintenance(root, s); err != nil {
					return err
				}
			}
			return sendMaintenanceReport(c.Context(), c.OutOrStdout(), root, s)
		})
	}}
	watch := &cobra.Command{Use: "watch", Short: "Report every five minutes; show unknown state after thirty minutes without acknowledgement", Args: cobra.NoArgs, Annotations: map[string]string{"skipChecks": "true"}, RunE: func(c *cobra.Command, _ []string) error { return maintenanceWatch(c.Context(), c.OutOrStdout()) }}
	var attestation string
	finish := &cobra.Command{Use: "finish", Short: "Protect clean pushed commits while awaiting a release", Args: cobra.NoArgs, Annotations: map[string]string{"skipChecks": "true"}, RunE: func(c *cobra.Command, _ []string) error {
		return maintenanceLocked(c.Context(), func(root string) error { return maintenanceFinish(c.Context(), c.OutOrStdout(), root, attestation) })
	}}
	finish.Flags().StringVar(&attestation, "migration-attestation", "", "Operator attestation: none or changed; omission marks schema_changed")
	var release, exitAttestation string
	var stopped bool
	exit := &cobra.Command{Use: "exit", Short: "Return to an explicit release containing every protected commit", Long: "Return to a published stable release after containment checks, a complete recovery copy, verified site backups and per-site migrations. Squash-merged fixes are refused. Stop traffic and workers with your host procedure before exit. Recovery copies are never deleted automatically.", Args: cobra.NoArgs, Annotations: map[string]string{"skipChecks": "true"}, RunE: func(c *cobra.Command, _ []string) error {
		return maintenanceLocked(c.Context(), func(root string) error {
			return maintenanceExit(c.Context(), c.OutOrStdout(), root, release, exitAttestation, stopped)
		})
	}}
	exit.Flags().StringVar(&release, "release", "", "Required explicit <app>=<published-release-tag>")
	exit.Flags().StringVar(&exitAttestation, "migration-attestation", "", "Operator attestation: none or changed; omission marks schema_changed")
	exit.Flags().BoolVar(&stopped, "services-stopped", false, "Confirm traffic and workers are stopped for affected sites using the host procedure")
	parent.AddCommand(request, enter, status, report, watch, finish, exit)
	return parent
}

// Maintenance uses the existing bench lock, but never replays ordinary source
// journals while protected source exists.
func maintenanceLocked(ctx context.Context, fn func(string) error) error {
	root := bench.Root()
	if err := kbstate.CheckBenchOwner(root); err != nil {
		return err
	}
	lock, err := kbstate.AcquireLock(root)
	if err != nil {
		return err
	}
	defer lock.Release()
	if err := kbstate.CheckSchemas(root); err != nil {
		return err
	}
	js := kbstate.ReadJournal(root)
	if js.Newer != nil {
		return js.Newer
	}
	if js.Malformed != "" {
		return fmt.Errorf("ordinary journal is malformed: %s", js.Malformed)
	}
	if !js.Missing && js.Journal.Pending() {
		return fmt.Errorf("ordinary transaction is pending at %s; recover it before maintenance", kbstate.JournalPath(root))
	}
	return fn(root)
}
func openMaintenance(root string) (*kbstate.MaintenanceSession, error) {
	s, e := kbstate.ReadMaintenance(root)
	if e != nil {
		return nil, e
	}
	if s == nil || s.SessionID == "" || s.ApprovalState == "closed" {
		return nil, errors.New("no active session")
	}
	return s, nil
}

func maintenanceReceipt(root string, a apps.App) (*kbstate.Receipt, error) {
	rs := kbstate.ReadReceipt(root, a.Dir())
	if !rs.Valid() {
		return nil, fmt.Errorf("%s needs a trusted receipt before entry: %s", a.Name, rs.Reason)
	}
	r := rs.Receipt
	if r.State != kbstate.StateArchive || r.SessionID != nil || r.App != a.Name || r.Directory != a.Dir() || r.Package != a.PackageName() || r.Repository != ident.Repository(a.Repository()) || r.Line != "1" {
		return nil, errors.New("receipt mapping or archive state does not match the READY app")
	}
	if err := bench.GitGuard(root, a.Dir()); err != nil {
		return nil, err
	}
	return r, nil
}
func readyMaintenanceApp(name string) (apps.App, error) {
	if name != "kb_compta" && name != "kb_stock" {
		return apps.App{}, errors.New("Phase 2 requires exactly one READY app: kb_compta or kb_stock; multi-app, --all and framework scope are not supported")
	}
	a, _ := apps.ByName(name)
	return a, nil
}

func maintenanceRequest(ctx context.Context, w io.Writer, root, name, reason string, renew bool) error {
	if len(reason) < 1 || len(reason) > 200 || strings.IndexFunc(reason, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
		return errors.New("reason must contain 1 to 200 bytes without control characters")
	}
	s, err := kbstate.ReadMaintenance(root)
	if err != nil {
		return err
	}
	if s != nil && s.Pending != nil {
		return errors.New("recovery_needed: resume the pending entry or recover the source operation before requesting approval")
	}
	if renew {
		if s == nil || s.SessionID == "" || s.Pending != nil || (s.SourceState != "active" && s.SourceState != "pending_release") {
			return errors.New("renew requires an active or pending-release session without recovery work")
		}
		if name != "" && name != s.Apps[0].App {
			return errors.New("renew cannot change the frozen app scope")
		}
	} else {
		a, err := readyMaintenanceApp(name)
		if err != nil {
			return err
		}
		r, err := maintenanceReceipt(root, a)
		if err != nil {
			return err
		}
		if s != nil && s.ApprovalState != "closed" && s.ApprovalState != "denied" {
			if s.SourceState != "archive" || s.Apps[0].App != name || s.Reason != reason {
				return errors.New("an existing request/session must be resumed; scope and reason cannot change")
			}
		}
		if s == nil || s.ApprovalState == "closed" || s.ApprovalState == "denied" {
			id, e := kbstate.EnsureBenchID(root)
			if e != nil {
				return e
			}
			requestID, e := kbstate.NewMaintenanceID()
			if e != nil {
				return e
			}
			s = &kbstate.MaintenanceSession{SchemaVersion: 1, BenchID: id, ClientRequestID: requestID, Reason: reason, SourceState: "archive", ApprovalState: "request_pending", Apps: []kbstate.MaintenanceApp{{App: a.Name, Directory: a.Dir(), Package: a.PackageName(), Repository: ident.Repository(a.Repository()), Line: "1", BaseCommit: r.Commit, Head: r.Commit, Branch: "support/" + requestID, Protected: []string{}, ProtectionRefs: []string{}, OriginalReceipt: r}}}
		}
	}
	token, fp, keyHash, allowed, err := maintenanceRefresh(ctx)
	if err != nil {
		return err
	}
	if !allowed[s.Apps[0].App] {
		return errors.New("app is not currently entitled")
	}
	if s.Fingerprint != "" && (s.Fingerprint != fp || s.LicenseKeySHA256 != keyHash) {
		return errors.New("maintenance identity changed; reactivate the original installation/key")
	}
	s.Fingerprint = fp
	s.LicenseKeySHA256 = keyHash
	if renew && !s.RenewRequested {
		id, e := kbstate.NewMaintenanceID()
		if e != nil {
			return e
		}
		s.ClientRequestID = id
		s.Reason = reason
		s.ApprovalState = "renew_pending"
		s.RenewRequested = true
		s.RequestID = ""
	}
	if err := kbstate.WriteMaintenance(root, s); err != nil {
		return err
	}
	req := license.MaintenanceRequest{ClientRequestID: s.ClientRequestID, BenchID: s.BenchID, Fingerprint: fp, LicenseKeySHA256: keyHash, Apps: []string{s.Apps[0].App}, Scope: "apps", Reason: s.Reason}
	var reply *license.RequestReply
	if renew {
		req.SessionID = s.SessionID
		reply, err = license.RenewMaintenance(ctx, "", token, req)
	} else {
		reply, err = license.RequestMaintenance(ctx, "", token, req)
	}
	if err != nil {
		return err
	}
	if reply.Status != "requested" || !kbstate.MaintenanceIDRE.MatchString(reply.RequestID) || !kbstate.MaintenanceIDRE.MatchString(reply.SessionID) || (s.SessionID != "" && reply.SessionID != s.SessionID) || (!renew && !reflect.DeepEqual(reply.Apps, req.Apps)) {
		return errors.New("maintenance request response identity/scope mismatch")
	}
	s.RequestID = reply.RequestID
	s.SessionID = reply.SessionID
	s.ApproveBy = reply.ApproveBy
	if !renew {
		s.ApprovalState = "requested"
	}
	if err := kbstate.WriteMaintenance(root, s); err != nil {
		return err
	}
	fmt.Fprintf(w, "Requested %s for %s; approve in /admin before %s. Then run kb maintenance enter.\n", s.RequestID, s.Apps[0].App, s.ApproveBy)
	return nil
}

func maintenanceEnter(ctx context.Context, w io.Writer, root, branch string) error {
	s, err := openMaintenance(root)
	if err != nil {
		return err
	}
	a := &s.Apps[0]
	if s.Pending != nil {
		return resumeMaintenanceAttach(ctx, w, root, s)
	}
	renewing := s.RenewRequested
	if !renewing && s.SourceState != "archive" {
		return errors.New("maintenance already entered; use status, report, finish or exit")
	}
	if s.RequestID == "" {
		return errors.New("no approved request to redeem")
	}
	if !renewing {
		app, e := readyMaintenanceApp(a.App)
		if e != nil {
			return e
		}
		r, e := maintenanceReceipt(root, app)
		if e != nil {
			return e
		}
		if r.Commit != a.BaseCommit {
			return errors.New("archive receipt changed after request")
		}
		if branch == "" {
			branch = "support/" + s.SessionID
		}
		if err := bench.ValidateMaintenanceBranch(branch); err != nil {
			return err
		}
		a.Branch = branch
		if err := bench.CheckMaintenanceBench(ctx, root); err != nil {
			return err
		}
	}
	token, fp, keyHash, allowed, err := maintenanceRefresh(ctx)
	if err != nil {
		return err
	}
	if fp != s.Fingerprint || keyHash != s.LicenseKeySHA256 || !allowed[a.App] {
		return errors.New("maintenance standing or installation identity mismatch")
	}
	// A successful redeem cannot be repeated after a lost response. Record only
	// after its scope has been checked; an admin must reissue a lost credential.
	code, err := maintenanceCodePrompt()
	if err != nil {
		return err
	}
	if renewing && s.PendingReport != nil {
		credential, e := maintenanceCredential(root, s)
		if e != nil {
			return e
		}
		if e := flushMaintenanceReport(ctx, w, root, s, credential); e != nil {
			return e
		}
	}
	reply, err := license.RedeemMaintenance(ctx, "", token, license.RedeemRequest{RequestID: s.RequestID, EntryCode: code, BenchID: s.BenchID, Fingerprint: fp})
	code = ""
	if err != nil {
		return err
	}
	if reply.SessionID != s.SessionID || !reflect.DeepEqual(reply.Apps, []string{a.App}) {
		return errors.New("redeem response differs from the frozen session scope; no Git attached")
	}
	credential := &license.MaintenanceCredential{SchemaVersion: 1, SessionID: s.SessionID, BenchID: s.BenchID, Fingerprint: s.Fingerprint, SessionCredential: reply.SessionCredential, SavedAt: maintenanceNow().UTC().Format(time.RFC3339)}
	if err := license.SaveMaintenanceCredential(*credential); err != nil {
		return err
	}
	s.WorkUntil = reply.WorkUntil
	s.ApprovalState = "redeemed"
	if renewing {
		s.RenewRequested = false
		s.SourceState = "active"
		rs := kbstate.ReadReceipt(root, a.Directory)
		if !rs.Valid() {
			return errors.New("maintenance receipt is invalid")
		}
		r := rs.Receipt
		r.State = "active"
		if err := kbstate.WriteReceipt(root, r); err != nil {
			return err
		}
		if err := kbstate.WriteMaintenance(root, s); err != nil {
			return err
		}
		return sendMaintenanceReport(ctx, w, root, s)
	}
	// Persist ownership before backups or fetch. Pre-attach failures retain the
	// credential, so the same entry can be resumed without consuming another code.
	s.Pending = &kbstate.MaintenancePending{Op: "enter", Step: "redeemed", StartedAt: maintenanceNow().UTC().Format(time.RFC3339), Expected: map[string]string{a.App: a.BaseCommit}}
	if err := kbstate.WriteMaintenance(root, s); err != nil {
		return err
	}
	return resumeMaintenanceAttach(ctx, w, root, s)
}

func resumeMaintenanceAttach(ctx context.Context, w io.Writer, root string, s *kbstate.MaintenanceSession) error {
	if s.Pending == nil || s.Pending.Op != "enter" {
		return errors.New("recovery_needed: pending operation requires deliberate recovery")
	}
	a := &s.Apps[0]
	credential, err := maintenanceCredential(root, s)
	if err != nil {
		return err
	}
	_ = credential
	if s.Pending.Step == "redeemed" {
		if err := bench.GitGuard(root, a.Directory); err != nil {
			return err
		}
		sites, err := bench.MaintenanceSites(root)
		if err != nil {
			return err
		}
		for _, site := range sites {
			installed, err := bench.DetectInstalledApps(site)
			if err != nil {
				return err
			}
			if !installed["kb_pro"] {
				return fmt.Errorf("site %s lacks kb_pro", site)
			}
		}
		backups, err := bench.BackupMaintenanceSites(ctx, root, sites)
		if err != nil {
			return err
		}
		s.Backups = nil
		for _, b := range backups {
			path, e := filepath.Rel(root, b.Path)
			if e != nil {
				return e
			}
			s.Backups = append(s.Backups, kbstate.MaintenanceBackup{Site: b.Site, Path: path, TakenAt: b.TakenAt})
		}
		snapshot, err := bench.PatchLogs(ctx, root, sites)
		if err != nil {
			return err
		}
		s.PatchSnapshot = snapshot
		prepared, err := bench.PrepareMaintenanceGit(ctx, root, a.Directory, a.Repository, a.BaseCommit, a.Branch)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, prepared)
		if err != nil {
			return err
		}
		if err := bench.SetPreparedMaintenanceOwner(prepared, s.SessionID); err != nil {
			return err
		}
		s.Pending.PreparedGit = rel
		s.Pending.Step = "prepared"
		if err := kbstate.WriteMaintenance(root, s); err != nil {
			return err
		}
	}
	if s.Pending.Step == "prepared" {
		if err := bench.GitGuard(root, a.Directory); err != nil {
			if !attachedMaintenanceOwned(root, a.Directory, s.SessionID) {
				return err
			}
		} else {
			// Fetch/auth failure happens before attaching is reported.
			s.SourceState = "attaching"
			if err := kbstate.WriteMaintenance(root, s); err != nil {
				return err
			}
			if err := sendMaintenanceReport(ctx, w, root, s); err != nil {
				return err
			}
			if err := bench.PublishMaintenanceGit(root, a.Directory, filepath.Join(root, s.Pending.PreparedGit)); err != nil {
				return err
			}
			fault.Hit("KP-ATTACH-1")
		}
		s.Pending.Step = "attached"
		if err := kbstate.WriteMaintenance(root, s); err != nil {
			return err
		}
	}
	if s.Pending.Step != "attached" {
		return errors.New("recovery_needed: unknown entry journal step")
	}
	if s.PendingReport != nil {
		credential, e := maintenanceCredential(root, s)
		if e != nil {
			return e
		}
		if e := flushMaintenanceReport(ctx, w, root, s, credential); e != nil {
			return e
		}
	}
	if !attachedMaintenanceOwned(root, a.Directory, s.SessionID) {
		return errors.New("recovery_needed: attached Git ownership mismatch")
	}
	obs, err := bench.ObserveMaintenanceGit(root, a.Directory)
	if err != nil {
		return err
	}
	if obs.Head != a.BaseCommit || obs.Branch != a.Branch {
		return errors.New("attached repository differs from the pending base/branch")
	}
	r := *a.OriginalReceipt
	r.State = "active"
	r.SessionID = &s.SessionID
	r.Provenance = kbstate.ProvMaintenance
	if err := kbstate.WriteReceipt(root, &r); err != nil {
		return err
	}
	if err := bench.SyncMaintenanceAppState(root, a.Directory, a.Package, obs.Head, obs.Branch); err != nil {
		return err
	}
	s.SourceState = "active"
	s.Pending = nil
	if err := kbstate.WriteMaintenance(root, s); err != nil {
		return err
	}
	return sendMaintenanceReport(ctx, w, root, s)
}
func attachedMaintenanceOwned(root, directory, id string) bool {
	return bench.VerifyAttachedMaintenanceOwner(root, directory, id) == nil
}

func maintenanceCredential(root string, s *kbstate.MaintenanceSession) (*license.MaintenanceCredential, error) {
	c, e := license.LoadMaintenanceCredential(s.SessionID)
	if e != nil {
		return nil, e
	}
	if c.BenchID != s.BenchID || c.Fingerprint != s.Fingerprint {
		return nil, errors.New("operator credential does not belong to this bench/session")
	}
	fp, err := maintenanceFingerprint()
	if err != nil {
		return nil, err
	}
	if fp != s.Fingerprint {
		return nil, errors.New("installation fingerprint changed; reactivate the original installation before reporting or closing")
	}
	return c, nil
}
func maintenanceVersion() string {
	v := strings.TrimPrefix(version.Version, "v")
	if regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(v) {
		return v
	}
	return "0.0.0"
}

func sendMaintenanceReport(ctx context.Context, w io.Writer, root string, s *kbstate.MaintenanceSession) error {
	if s.PendingClose != nil {
		return errors.New("session close is pending; retry kb maintenance exit before another report")
	}
	credential, err := maintenanceCredential(root, s)
	if err != nil {
		return err
	}
	if s.PendingReport != nil {
		var pending license.MaintenanceReport
		if err := json.Unmarshal(s.PendingReport.Body, &pending); err != nil {
			return err
		}
		if err := flushMaintenanceReport(ctx, w, root, s, credential); err != nil {
			return err
		}
		if pending.SourceState == s.SourceState {
			return nil
		}
	}
	a := &s.Apps[0]
	obs, err := bench.ObserveMaintenanceGit(root, a.Directory)
	if err != nil && s.SourceState != "archive" && s.SourceState != "attaching" && s.SourceState != "returning" && s.SourceState != "recovery_needed" {
		return err
	}
	ra := license.ReportApp{App: a.App, Directory: a.Directory, Repository: a.Repository, Line: a.Line, Branch: a.Branch, Head: a.Head, Base: a.BaseCommit, Protected: a.Protected}
	if obs == nil && s.SourceState == "recovery_needed" {
		ra.Dirty = true
	}
	if obs != nil {
		commits, err := bench.MaintenanceCommits(root, a.Directory, a.BaseCommit)
		if err != nil {
			return err
		}
		merged := append([]string{}, a.Protected...)
		for _, c := range commits {
			if !containsString(merged, c) {
				merged = append(merged, c)
			}
		}
		if len(merged) > 64 {
			return errors.New("more than 64 protected commits; finish/release this session")
		}
		sort.Strings(merged)
		refs, err := bench.ProtectMaintenanceCommits(root, a.Directory, s.SessionID, merged)
		if err != nil {
			return err
		}
		a.Protected = merged
		a.ProtectionRefs = refs
		a.Head = obs.Head
		ra.Head = obs.Head
		ra.Branch = obs.Branch
		ra.Protected = merged
		ra.Dirty = obs.Dirty
		ra.UntrackedFiles = obs.UntrackedFiles
		ra.Stashes = obs.Stashes
		ra.LinkedWorktrees = obs.LinkedWorktrees
		rs := kbstate.ReadReceipt(root, a.Directory)
		if !rs.Valid() {
			return errors.New("maintenance receipt is not valid")
		}
		r := rs.Receipt
		r.Protected = merged
		r.ProtectionRefs = refs
		if err := kbstate.WriteReceipt(root, r); err != nil {
			return err
		}
	}
	recovery := []string{}
	paths := s.Recovery
	if len(paths) > 8 {
		paths = paths[len(paths)-8:]
	}
	for _, p := range paths {
		recovery = append(recovery, filepath.Base(p))
	}
	body := license.MaintenanceReport{Seq: s.LastSeq + 1, ObservedAt: maintenanceNow().UTC().Format(time.RFC3339), CLIVersion: maintenanceVersion(), SourceState: s.SourceState, SchemaChanged: s.SchemaChanged, Apps: []license.ReportApp{ra}, Recovery: recovery}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	body.PayloadSHA256 = fmt.Sprintf("%x", sha256.Sum256(raw))
	raw, err = json.Marshal(body)
	if err != nil {
		return err
	}
	s.PendingReport = &kbstate.PendingReport{Seq: body.Seq, Body: raw}
	if err := kbstate.WriteMaintenance(root, s); err != nil {
		return err
	}
	fault.Hit("KP-REPORT-0")
	return flushMaintenanceReport(ctx, w, root, s, credential)
}
func flushMaintenanceReport(ctx context.Context, w io.Writer, root string, s *kbstate.MaintenanceSession, c *license.MaintenanceCredential) error {
	var report license.MaintenanceReport
	if err := json.Unmarshal(s.PendingReport.Body, &report); err != nil {
		return err
	}
	reply, err := license.ReportMaintenance(ctx, "", c.SessionCredential, report)
	if err != nil {
		return err
	}
	if reply.AckSeq != s.PendingReport.Seq {
		return errors.New("maintenance acknowledgement sequence mismatch")
	}
	s.LastSeq = reply.AckSeq
	for _, a := range report.Apps {
		for i := range s.Apps {
			if s.Apps[i].App == a.App {
				s.Apps[i].ReportedHead = a.Head
			}
		}
	}
	s.PendingReport = nil
	s.LastReportAt = maintenanceNow().UTC().Format(time.RFC3339)
	s.ApprovalState = reply.ApprovalState
	s.WorkUntil = reply.WorkUntil
	if err := kbstate.WriteMaintenance(root, s); err != nil {
		return err
	}
	fmt.Fprintf(w, "Reported %s, seq %d, approval %s.\n", report.SourceState, s.LastSeq, s.ApprovalState)
	return nil
}
func containsString(values []string, v string) bool {
	for _, s := range values {
		if s == v {
			return true
		}
	}
	return false
}

func printMaintenanceStatus(w io.Writer, root string) {
	s, err := kbstate.ReadMaintenance(root)
	if err != nil {
		fmt.Fprintf(w, "Maintenance: recovery_needed (%v)\n", err)
		return
	}
	if s == nil {
		return
	}
	state := s.SourceState
	if s.Pending != nil {
		state = "recovery_needed"
	}
	approval := s.ApprovalState
	if until, e := time.Parse(time.RFC3339, s.WorkUntil); e == nil && maintenanceNow().After(until) && approval == "redeemed" {
		approval = "expired"
	}
	fmt.Fprintf(w, "Maintenance: %s, session %s, approval %s, schema_changed %t\n", state, s.SessionID, approval, s.SchemaChanged)
	if s.Pending != nil {
		fmt.Fprintf(w, "  Pending %s at %s; recovery %s; prepared Git %s\n", s.Pending.Op, s.Pending.Step, s.Pending.Recovery, s.Pending.PreparedGit)
	}
	for site, outcome := range s.MigrationOutcomes {
		fmt.Fprintf(w, "  Migration %s: %s\n", site, outcome)
	}
	if s.SourceState != "archive" {
		if last, e := time.Parse(time.RFC3339, s.LastReportAt); e != nil || maintenanceNow().Sub(last) >= 30*time.Minute {
			fmt.Fprintln(w, "  Stale: state unknown (no acknowledged report in thirty minutes)")
		}
	}
	for _, a := range s.Apps {
		obs, e := bench.ObserveMaintenanceGit(root, a.Directory)
		if e != nil {
			fmt.Fprintf(w, "  %s: Git unavailable; recorded HEAD %s, protected %v\n", a.App, a.Head, a.Protected)
			continue
		}
		recorded := obs.Head == a.ReportedHead && !obs.Dirty
		fmt.Fprintf(w, "  %s HEAD %s branch %s dirty %t untracked %d protected %v recorded %t\n", a.App, obs.Head, obs.Branch, obs.Dirty, obs.UntrackedFiles, a.Protected, recorded)
		if !recorded {
			fmt.Fprintln(w, "  Unrecorded live source; do not deploy before push and report.")
		}
	}
	for _, p := range s.Recovery {
		fmt.Fprintf(w, "  Recovery copy: %s\n", p)
	}
}

func maintenanceWatch(ctx context.Context, w io.Writer) error {
	tick := time.NewTicker(5 * time.Minute)
	defer tick.Stop()
	return watchMaintenanceTicks(ctx, w, tick.C)
}
func watchMaintenanceTicks(ctx context.Context, w io.Writer, ticks <-chan time.Time) error {
	s, err := openMaintenance(bench.Root())
	if err != nil {
		return err
	}
	if _, err := maintenanceCredential(bench.Root(), s); err != nil {
		return err
	}
	for {
		fatal := false
		err := maintenanceLocked(ctx, func(root string) error {
			s, e := openMaintenance(root)
			if e != nil {
				fatal = true
				return e
			}
			return sendMaintenanceReport(ctx, w, root, s)
		})
		if fatal {
			return err
		}
		if err != nil {
			fmt.Fprintf(w, "Report failed: %v\n", err)
			printMaintenanceStatus(w, bench.Root())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-ticks:
			if !ok {
				return nil
			}
		}
	}
}

func updateMaintenanceSchema(ctx context.Context, root string, s *kbstate.MaintenanceSession, attestation string) error {
	if attestation != "" && attestation != "none" && attestation != "changed" {
		return errors.New("migration-attestation must be none or changed")
	}
	sites, err := bench.MaintenanceSites(root)
	if err != nil {
		return err
	}
	current, err := bench.PatchLogs(ctx, root, sites)
	if err != nil {
		return err
	}
	if attestation != "none" || !reflect.DeepEqual(current, s.PatchSnapshot) {
		s.SchemaChanged = true
	}
	return nil
}
func maintenanceFinish(ctx context.Context, w io.Writer, root, attestation string) error {
	s, err := openMaintenance(root)
	if err != nil {
		return err
	}
	if s.Pending != nil {
		return errors.New("recovery_needed: finish cannot clear pending source work")
	}
	if s.SourceState != "active" && s.SourceState != "pending_release" {
		return errors.New("finish requires active source")
	}
	if _, err := maintenanceCredential(root, s); err != nil {
		return err
	}
	if s.PendingReport != nil {
		credential, e := maintenanceCredential(root, s)
		if e != nil {
			return e
		}
		if e := flushMaintenanceReport(ctx, w, root, s, credential); e != nil {
			return e
		}
	}
	a := &s.Apps[0]
	obs, err := bench.ObserveMaintenanceGit(root, a.Directory)
	if err != nil {
		return err
	}
	if obs.Dirty {
		return fmt.Errorf("finish requires a clean tree: %s", strings.Join(obs.Changes, ", "))
	}
	commits, err := bench.MaintenanceCommits(root, a.Directory, a.BaseCommit)
	if err != nil {
		return err
	}
	protected := append([]string{}, a.Protected...)
	for _, c := range commits {
		if !containsString(protected, c) {
			protected = append(protected, c)
		}
	}
	if err := bench.VerifyMaintenancePushed(ctx, root, a.Directory, protected); err != nil {
		return err
	}
	if s.SourceState == "pending_release" && obs.Head == a.Head && s.PendingReport == nil {
		return nil
	}
	if err := updateMaintenanceSchema(ctx, root, s, attestation); err != nil {
		return err
	}
	after, err := bench.ObserveMaintenanceGit(root, a.Directory)
	if err != nil {
		return err
	}
	if after.Head != obs.Head || after.Branch != obs.Branch || after.Dirty || after.Stashes != obs.Stashes || after.LinkedWorktrees != obs.LinkedWorktrees {
		return errors.New("maintenance source changed during finish preflight; nothing finalized")
	}
	s.Pending = &kbstate.MaintenancePending{Op: "finish", Step: "protecting", StartedAt: maintenanceNow().UTC().Format(time.RFC3339), Expected: map[string]string{a.App: obs.Head}}
	if err := kbstate.WriteMaintenance(root, s); err != nil {
		return err
	}
	sort.Strings(protected)
	refs, err := bench.ProtectMaintenanceCommits(root, a.Directory, s.SessionID, protected)
	if err != nil {
		return err
	}
	a.Head = obs.Head
	a.Protected = protected
	a.ProtectionRefs = refs
	rs := kbstate.ReadReceipt(root, a.Directory)
	if !rs.Valid() {
		return errors.New("maintenance receipt is invalid")
	}
	r := rs.Receipt
	r.State = "pending_release"
	r.Protected = protected
	r.ProtectionRefs = refs
	if err := kbstate.WriteReceipt(root, r); err != nil {
		return err
	}
	s.SourceState = "pending_release"
	s.Pending = nil
	if err := kbstate.WriteMaintenance(root, s); err != nil {
		return err
	}
	return sendMaintenanceReport(ctx, w, root, s)
}

func maintenanceExit(ctx context.Context, w io.Writer, root, release, attestation string, stopped bool) error {
	s, err := openMaintenance(root)
	if err != nil {
		return err
	}
	if s.Pending != nil {
		return errors.New("recovery_needed: exit cannot clear pending source work")
	}
	if s.SourceState != "active" && s.SourceState != "pending_release" && s.SourceState != "archive" {
		return errors.New("exit requires active or pending-release source")
	}
	if s.SourceState == "archive" {
		return closeMaintenance(ctx, w, root, s)
	}
	a := &s.Apps[0]
	parts := strings.SplitN(release, "=", 2)
	if len(parts) != 2 || parts[0] != a.App || !ident.ValidRef(parts[1]) {
		return errors.New("exit requires --release <session-app>=<published-release-tag>")
	}
	if !stopped {
		return errors.New("stop affected site traffic and workers with the host procedure, then confirm --services-stopped")
	}
	credential, err := maintenanceCredential(root, s)
	if err != nil {
		return err
	}
	token, fp, keyHash, allowed, err := maintenanceRefresh(ctx)
	if err != nil {
		return err
	}
	if fp != s.Fingerprint || keyHash != s.LicenseKeySHA256 || !allowed[a.App] {
		return errors.New("maintenance identity or current entitlement mismatch")
	}
	if s.PendingReport != nil {
		if err := flushMaintenanceReport(ctx, w, root, s, credential); err != nil {
			return err
		}
	}
	sites, err := bench.InstalledMaintenanceSites(ctx, root, a.Package)
	if err != nil {
		return err
	}
	app, _ := apps.ByName(a.App)
	downloadCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	dl, err := license.DownloadApp(downloadCtx, config.ResolveLicenseServerURL(), token, license.DownloadRequest{App: a.App, Repository: app.Repository(), Line: app.Line, Ref: parts[1], Release: true, SessionCredential: credential.SessionCredential})
	if err != nil {
		return err
	}
	defer os.Remove(dl.Path)
	if dl.Repository != a.Repository || dl.ReleaseTag != parts[1] {
		return errors.New("strict release identity differs from session")
	}
	if err := bench.VerifyMaintenanceRelease(ctx, root, a.Directory, a.Repository, dl.ReleaseTag, dl.Commit, a.Protected); err != nil {
		return err
	}
	if err := updateMaintenanceSchema(ctx, root, s, attestation); err != nil {
		return err
	}
	backup, err := bench.BackupMaintenanceSites(ctx, root, sites)
	if err != nil {
		return err
	}
	for _, b := range backup {
		path, e := filepath.Rel(root, b.Path)
		if e != nil {
			return e
		}
		s.Backups = append(s.Backups, kbstate.MaintenanceBackup{Site: b.Site, Path: path, TakenAt: b.TakenAt})
	}
	recovery, manifest, err := bench.RecoveryCopyMaintenance(root, a.Directory, s.SessionID)
	if err != nil {
		return err
	}
	_ = manifest
	rel, err := filepath.Rel(root, recovery)
	if err != nil {
		return err
	}
	s.Recovery = append(s.Recovery, rel)
	previousState := s.SourceState
	oldSource, newSource := bench.MaintenanceReturnPaths(recovery)
	for _, p := range []string{oldSource, newSource, recovery + ".failed-release"} {
		path, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		s.Recovery = append(s.Recovery, path)
	}
	s.MigrationOutcomes = map[string]string{}
	s.Pending = &kbstate.MaintenancePending{Op: "exit", Step: "recovery_verified", StartedAt: maintenanceNow().UTC().Format(time.RFC3339), Recovery: rel, Expected: map[string]string{a.App: dl.Commit}}
	if err := kbstate.WriteMaintenance(root, s); err != nil {
		return err
	}
	fault.Hit("KP-EXIT-0")
	s.SourceState = "returning"
	if err := kbstate.WriteMaintenance(root, s); err != nil {
		return err
	}
	if err := sendMaintenanceReport(ctx, w, root, s); err != nil {
		return err
	}
	original := kbstate.ReadReceipt(root, a.Directory)
	if !original.Valid() {
		return errors.New("maintenance receipt is invalid")
	}
	r := *original.Receipt
	r.State = "returning"
	if err := kbstate.WriteReceipt(root, &r); err != nil {
		return err
	}
	migrationStarted := false
	restored := false
	swapAttempted := false
	record := func(step, site, outcome string) error {
		if step == "swapped" && outcome == "unknown" {
			if err := bench.VerifyMaintenanceRelease(ctx, root, a.Directory, a.Repository, dl.ReleaseTag, dl.Commit, a.Protected); err != nil {
				return err
			}
			if err := bench.VerifyMaintenanceRecovery(filepath.Join(root, "apps", a.Directory), recovery, manifest); err != nil {
				return err
			}
			swapAttempted = true
		}
		s.Pending.Step = step
		if step == "rollback" && outcome == "ok" {
			restored = true
		}
		if site != "" {
			migrationStarted = true
			s.SchemaChanged = true
			s.MigrationOutcomes[site] = outcome
		}
		return kbstate.WriteMaintenance(root, s)
	}
	if err := bench.ReturnMaintenanceArchive(ctx, root, app, dl, recovery, s.SchemaChanged, sites, record); err != nil {
		unchanged := bench.VerifyMaintenanceRecovery(filepath.Join(root, "apps", a.Directory), recovery, manifest) == nil
		if migrationStarted || s.SchemaChanged || (!restored && (swapAttempted || !unchanged)) {
			s.SourceState = "recovery_needed"
			cause := fmt.Errorf("recovery_needed; retain both source versions and %s: %w", rel, err)
			if saveErr := kbstate.WriteMaintenance(root, s); saveErr != nil {
				return errors.Join(cause, saveErr)
			}
			if reportErr := sendMaintenanceReport(ctx, w, root, s); reportErr != nil {
				return errors.Join(cause, reportErr)
			}
			return cause
		}
		// The bench layer returns before-migration errors only after verified restore.
		if restoreErr := kbstate.WriteReceipt(root, original.Receipt); restoreErr != nil {
			return errors.Join(err, restoreErr)
		}
		s.SourceState = previousState
		s.Pending = nil
		if saveErr := kbstate.WriteMaintenance(root, s); saveErr != nil {
			return errors.Join(err, saveErr)
		}
		if reportErr := sendMaintenanceReport(ctx, w, root, s); reportErr != nil {
			return errors.Join(err, reportErr)
		}
		return err
	}
	r = *a.OriginalReceipt
	r.Requested = parts[1]
	r.Ref = dl.Ref
	r.Commit = dl.Commit
	r.ReleaseTag = &dl.ReleaseTag
	r.ArchiveSHA256 = dl.SHA256
	r.InstalledAt = maintenanceNow().UTC().Format(time.RFC3339)
	r.State = "archive"
	r.SessionID = nil
	r.Provenance = kbstate.ProvInstalled
	r.Adopt = nil
	r.Protected = nil
	r.ProtectionRefs = nil
	if err := kbstate.WriteReceipt(root, &r); err != nil {
		return err
	}
	if err := bench.SyncAppState(a.Directory, a.Package); err != nil {
		return err
	}
	s.SourceState = "archive"
	s.Pending = nil
	a.Head = dl.Commit
	if err := kbstate.WriteMaintenance(root, s); err != nil {
		return err
	}
	if err := sendMaintenanceReport(ctx, w, root, s); err != nil {
		return err
	}
	return closeMaintenance(ctx, w, root, s)
}
func closeMaintenance(ctx context.Context, w io.Writer, root string, s *kbstate.MaintenanceSession) error {
	c, err := maintenanceCredential(root, s)
	if err != nil {
		return err
	}
	if s.PendingReport != nil {
		if err := flushMaintenanceReport(ctx, w, root, s, c); err != nil {
			return err
		}
	}
	var body license.MaintenanceClose
	if s.PendingClose == nil {
		body = license.MaintenanceClose{Seq: s.LastSeq + 1, ObservedAt: maintenanceNow().UTC().Format(time.RFC3339), SourceState: "archive"}
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		body.PayloadSHA256 = fmt.Sprintf("%x", sha256.Sum256(raw))
		raw, err = json.Marshal(body)
		if err != nil {
			return err
		}
		s.PendingClose = &kbstate.PendingReport{Seq: body.Seq, Body: raw}
		if err := kbstate.WriteMaintenance(root, s); err != nil {
			return err
		}
	} else if err := json.Unmarshal(s.PendingClose.Body, &body); err != nil {
		return err
	}
	reply, err := license.CloseMaintenance(ctx, "", c.SessionCredential, body)
	if err != nil {
		return err
	}
	if reply.Status != "closed" {
		return errors.New("server did not confirm closed session")
	}
	s.ApprovalState = "closed"
	s.LastSeq = body.Seq
	s.PendingClose = nil
	if err := kbstate.ArchiveMaintenanceSession(root, s); err != nil {
		return err
	}
	if err := kbstate.WriteMaintenance(root, s); err != nil {
		return err
	}
	if err := license.DeleteMaintenanceCredential(s.SessionID); err != nil {
		return err
	}
	fmt.Fprintln(w, "Maintenance closed; recovery copies retained. Resume services using the host procedure.")
	return nil
}
