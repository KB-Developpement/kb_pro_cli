package bench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KB-Developpement/kb_pro_cli/internal/apps"
	"github.com/KB-Developpement/kb_pro_cli/internal/fault"
	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
)

// Commands that run a source transaction. They are recorded in the journal so a
// replay knows whether a migrate boundary applies.
const (
	CmdAdd        = "add"
	CmdInstall    = "install"
	CmdUpgrade    = "upgrade"
	CmdInitFrappe = "init-kb-frappe"
)

// ErrJournalPending is matched (errors.Is) by every error that leaves a
// transaction pending in .kb/journal.json. A multi-app run stops on it: the
// remaining apps are not attempted.
var ErrJournalPending = errors.New("a transaction is pending in the journal")

// PendingError reports that the journal still holds an unfinished transaction.
type PendingError struct {
	Journal string
	Cause   error
}

func (e *PendingError) Error() string { return e.Cause.Error() }
func (e *PendingError) Unwrap() []error {
	return []error{e.Cause, ErrJournalPending}
}

// FinalizeHook is a test seam: when set it runs at the start of finalization
// (receipt and apps.json) and may fail it. It is nil in production.
var FinalizeHook func() error

// ApplyPlan is one app's source transaction.
type ApplyPlan struct {
	Command  string            // CmdAdd, CmdInstall, CmdUpgrade or CmdInitFrappe
	App      apps.App          // identity (the framework identity for init-kb-frappe)
	Download *license.Download // verified archive and the identity the server stated
	// StockConversion marks a stock-Frappe checkout that passed CheckStockFrappe
	// under the lock. It is the only case where a tree with .git is replaced.
	StockConversion bool
}

// ApplyResult is what a finished transaction reports.
type ApplyResult struct {
	Output string
	// Retained is where the previous tree was kept (a pre-receipt copy), or "".
	Retained string
	// Legacy lists leftover .kb-old/.kb-new copies moved into .kb/recovery/legacy.
	Legacy []string
	// StockDiscarded is true when a stock-Frappe tree was replaced and not kept.
	StockDiscarded bool
}

// Steps are the bench operations of a transaction. They are injectable so the
// swap, rollback and replay can be tested without a real bench.
type Steps struct {
	AppendAppsTxt     func() (added bool, err error)
	RemoveAppsTxt     func() error
	SetupRequirements func() (string, error)
	PipInstall        func() (string, error)
	Build             func() (string, error)
	Migrate           func() (string, error) // nil: no migrate for this command
	SyncAppState      func() error
}

func defaultSteps(ctx context.Context, root string, app apps.App, command string) Steps {
	dir, pkg := app.Dir(), app.PackageName()
	step := func(f func(context.Context) (string, error)) func() (string, error) {
		return func() (string, error) {
			c, cancel := context.WithTimeout(ctx, 10*time.Minute)
			defer cancel()
			return f(c)
		}
	}
	s := Steps{
		SetupRequirements: step(func(c context.Context) (string, error) { return setupRequirementsPythonAndNode(c, pkg) }),
		PipInstall:        step(func(c context.Context) (string, error) { return PipInstallEditable(c, dir) }),
		Build:             step(func(c context.Context) (string, error) { return BuildApp(c, pkg) }),
		SyncAppState:      func() error { return SyncAppState(dir, pkg) },
	}
	if command == CmdAdd || command == CmdInstall {
		s.AppendAppsTxt = func() (bool, error) { return appendAppToAppsTxt(root, pkg) }
		s.RemoveAppsTxt = func() error { return removeAppFromAppsTxt(root, pkg) }
	}
	if command == CmdUpgrade || command == CmdInitFrappe {
		s.Migrate = step(func(c context.Context) (string, error) { return runBench(c, "migrate") })
	}
	return s
}

// ApplyArchive runs one app's source transaction: planned, downloaded, staged,
// swapped (move-in, registration, build and, for upgrade and init-kb-frappe,
// migrate), then finalized (receipt and apps.json). The caller holds the bench
// lock, has recovered any earlier journal, and has already run its preflight.
//
// Nothing here deletes a leftover: .kb-old and .kb-new copies are moved to
// .kb/recovery/legacy, and the replaced tree is deleted only when the app had a
// valid receipt (or is the stock-Frappe conversion).
func ApplyArchive(ctx context.Context, p ApplyPlan) (ApplyResult, error) {
	root := benchDir()
	return applyWithSteps(root, p, defaultSteps(ctx, root, p.App, p.Command))
}

func newPendingReceipt(app apps.App, dl *license.Download) *kbstate.Receipt {
	r := &kbstate.Receipt{
		SchemaVersion: kbstate.SupportedSchema,
		App:           app.LicenseID(),
		Directory:     app.Dir(),
		Package:       app.PackageName(),
		Repository:    dl.Repository,
		Line:          fmt.Sprintf("%d", app.Line),
		Requested:     dl.Requested,
		Ref:           dl.Ref,
		Commit:        dl.Commit,
		ArchiveSHA256: dl.SHA256,
		State:         kbstate.StateArchive,
		Provenance:    kbstate.ProvInstalled,
	}
	if dl.ReleaseTag != "" {
		tag := dl.ReleaseTag
		r.ReleaseTag = &tag
	}
	return r
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func applyWithSteps(root string, p ApplyPlan, st Steps) (ApplyResult, error) {
	var res ApplyResult
	app := p.App
	dir, pkg := app.Dir(), app.PackageName()
	appDir := filepath.Join(root, "apps", dir)
	stagingDir := appDir + ".kb-new"
	oldDir := appDir + ".kb-old"
	dl := p.Download
	isAdd := p.Command == CmdAdd || p.Command == CmdInstall
	if dl == nil {
		return res, errors.New("internal error: no download to apply")
	}

	if !p.StockConversion {
		if err := GitGuard(root, dir); err != nil {
			return res, err
		}
	}
	hadOld := isDir(appDir)
	switch {
	case isAdd && hadOld:
		return res, fmt.Errorf("app %q already exists at %s — remove it or use upgrade", app.Name, appDir)
	case p.Command == CmdUpgrade && !hadOld:
		return res, fmt.Errorf("app %q is not in the bench (%s is missing) — use add or install", app.Name, appDir)
	}

	rs := kbstate.ReadReceipt(root, dir)
	if rs.Newer != nil {
		return res, rs.Newer
	}
	if prior := kbstate.ReadJournal(root); prior.Newer != nil {
		return res, prior.Newer
	} else if prior.Malformed != "" {
		return res, fmt.Errorf("%s is unusable (%s) — resolve it by hand and delete it", kbstate.JournalPath(root), prior.Malformed)
	} else if prior.Journal != nil && prior.Journal.Pending() {
		return res, pendingRefusal(root, prior.Journal, "an earlier transaction is still pending")
	}

	j := kbstate.NewJournal(root)
	j.Command, j.App, j.Directory = p.Command, app.LicenseID(), dir
	j.Op = kbstate.OpRegister
	if hadOld {
		j.Op = kbstate.OpSwap
	}
	j.HadReceipt = rs.Valid()
	j.StockConversion = p.StockConversion
	j.PendingReceipt = newPendingReceipt(app, dl)
	j.Paths = []string{dl.Path}
	if err := j.Advance(kbstate.StepPlanned); err != nil {
		return res, err
	}

	abort := func(cause error) error {
		if jerr := j.Abort(cause); jerr != nil {
			return fmt.Errorf("%w (and the journal could not be closed: %v)", cause, jerr)
		}
		return cause
	}

	// downloaded: the archive on disk is the one whose digest the receipt will carry.
	sum, err := fileSHA256(dl.Path)
	if err != nil {
		return res, abort(fmt.Errorf("read downloaded archive: %w", err))
	}
	if sum != dl.SHA256 {
		return res, abort(fmt.Errorf("downloaded archive for %s changed on disk (sha256 mismatch) — refusing to install it", app.Name))
	}
	if err := j.Advance(kbstate.StepDownloaded); err != nil {
		return res, err
	}

	// Leftovers never block a swap and are never deleted: move them aside.
	for _, lo := range []struct{ kind, path string }{{"kb-old", oldDir}, {"kb-new", stagingDir}} {
		if !exists(lo.path) {
			continue
		}
		if err := kbstate.EnsureTree(root, kbstate.LegacyDir(root)); err != nil {
			return res, abort(err)
		}
		dest := uniquePath(filepath.Join(kbstate.LegacyDir(root), fmt.Sprintf("%s-%s-%s", dir, lo.kind, kbstate.UTCStamp(time.Now()))))
		if err := os.Rename(lo.path, dest); err != nil {
			return res, abort(fmt.Errorf("move leftover %s to %s: %w", lo.path, dest, err))
		}
		j.Legacy = append(j.Legacy, kbstate.LegacyMove{Original: lo.path, Destination: dest, MovedAt: time.Now().UTC().Format(time.RFC3339)})
		res.Legacy = append(res.Legacy, dest)
		if err := j.Save(); err != nil {
			return res, err
		}
	}

	// staged: extract completely (gzip trailer verified) beside the live tree.
	if err := j.AddPath(stagingDir); err != nil {
		return res, err
	}
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return res, abort(fmt.Errorf("create staging dir: %w", err))
	}
	if err := extractTarGzStripped(dl.Path, stagingDir); err != nil {
		_ = os.RemoveAll(stagingDir)
		return res, abort(fmt.Errorf("extract archive: %w", err))
	}
	n, err := kbstate.CountEntries(stagingDir, pkg)
	if err != nil {
		_ = os.RemoveAll(stagingDir)
		return res, abort(fmt.Errorf("count staged files: %w", err))
	}
	j.StagedFiles = n
	if err := j.Advance(kbstate.StepStaged); err != nil {
		return res, err
	}

	// swapped is recorded before the first live-directory rename, so a kill
	// anywhere below leaves the journal at swapped.
	j.NewPath = appDir
	if hadOld {
		j.OldPath = oldDir
	}
	if err := j.Advance(kbstate.StepSwapped); err != nil {
		return res, err
	}

	addedAppsTxt := false
	rollback := func(cause error, stage string) error {
		failed := func(step string, err error) error {
			return &PendingError{Journal: kbstate.JournalPath(root), Cause: fmt.Errorf(
				"%s: %w (ROLLBACK FAILED at %s: %v — the journal %s is still pending; old source %q, live path %q)",
				stage, cause, step, err, kbstate.JournalPath(root), oldDir, appDir)}
		}
		if err := os.RemoveAll(appDir); err != nil {
			return failed("removing the new directory", err)
		}
		if hadOld {
			if err := os.Rename(oldDir, appDir); err != nil {
				return failed("restoring the previous version", err)
			}
			if st.PipInstall != nil {
				_, _ = st.PipInstall()
			}
		} else if addedAppsTxt && st.RemoveAppsTxt != nil {
			if err := st.RemoveAppsTxt(); err != nil {
				return failed("removing the apps.txt entry", err)
			}
		}
		_ = os.RemoveAll(stagingDir)
		outcome := "the new app directory was removed"
		if hadOld {
			outcome = "the previous version was restored"
		}
		if jerr := j.Abort(cause); jerr != nil {
			return &PendingError{Journal: kbstate.JournalPath(root), Cause: fmt.Errorf("%s: %w (%s, but the journal could not be closed: %v)", stage, cause, outcome, jerr)}
		}
		return fmt.Errorf("%s: %w (%s)", stage, cause, outcome)
	}

	if hadOld {
		if err := os.Rename(appDir, oldDir); err != nil {
			_ = os.RemoveAll(stagingDir)
			return res, abort(fmt.Errorf("back up old app dir: %w", err))
		}
		fault.Hit("KP-SWAP-1")
	}
	if err := os.Rename(stagingDir, appDir); err != nil {
		return res, rollback(err, "replace app dir")
	}
	fault.Hit("after-move-in")

	if st.AppendAppsTxt != nil {
		added, err := st.AppendAppsTxt()
		if err != nil {
			return res, rollback(err, "register in apps.txt")
		}
		addedAppsTxt = added
	}
	reqOut, err := st.SetupRequirements()
	if err != nil {
		res.Output = reqOut
		return res, rollback(err, "setup requirements")
	}
	if _, err := st.PipInstall(); err != nil {
		res.Output = reqOut
		return res, rollback(err, "pip install -e")
	}
	buildOut, err := st.Build()
	res.Output = combineBenchOutput(reqOut, buildOut)
	if err != nil {
		return res, rollback(err, "build assets")
	}

	if st.Migrate != nil {
		migOut, err := st.Migrate()
		res.Output = combineBenchOutput(res.Output, migOut)
		if err != nil {
			// Do not roll back: the schema may be half-migrated. The journal
			// stays at swapped and the old source stays where it is.
			msg := fmt.Sprintf("bench migrate: %v (app files were replaced and NOT rolled back", err)
			if hadOld {
				msg += fmt.Sprintf("; the previous source is kept at %s", oldDir)
			}
			msg += fmt.Sprintf("; the journal %s stays pending at step swapped)", kbstate.JournalPath(root))
			return res, &PendingError{Journal: kbstate.JournalPath(root), Cause: errors.New(msg)}
		}
		if p.Command == CmdUpgrade {
			if err := j.Advance(kbstate.StepMigrated); err != nil {
				return res, &PendingError{Journal: kbstate.JournalPath(root), Cause: err}
			}
		}
	}

	retained, err := finalize(root, j, st)
	res.Retained = retained
	res.StockDiscarded = p.StockConversion
	if err != nil {
		return res, &PendingError{Journal: kbstate.JournalPath(root), Cause: fmt.Errorf(
			"finalize %s: %w (the source is in place; the journal %s stays pending at step %s and the next kb command replays it)",
			dir, err, kbstate.JournalPath(root), j.Step)}
	}
	return res, nil
}

// uniquePath returns p, or p-1, p-2 ... if p exists.
func uniquePath(p string) string {
	if !exists(p) {
		return p
	}
	for i := 1; ; i++ {
		c := fmt.Sprintf("%s-%d", p, i)
		if !exists(c) {
			return c
		}
	}
}

// finalize writes the receipt and the apps.json entry, retires the replaced
// tree, and closes the journal. It is idempotent: a replay runs it again.
func finalize(root string, j *kbstate.Journal, st Steps) (retained string, err error) {
	if FinalizeHook != nil {
		if err := FinalizeHook(); err != nil {
			return "", err
		}
	}
	if j.PendingReceipt == nil {
		return "", errors.New("the journal has no pending receipt")
	}
	r := *j.PendingReceipt
	r.InstalledAt = time.Now().UTC().Format(time.RFC3339)
	if err := kbstate.WriteReceipt(root, &r); err != nil {
		return "", err
	}
	if st.SyncAppState != nil {
		if err := st.SyncAppState(); err != nil {
			return "", fmt.Errorf("update sites/apps.json: %w", err)
		}
	}
	if err := retireOld(root, j); err != nil {
		return j.Retained, err
	}
	if err := j.Advance(kbstate.StepFinalized); err != nil {
		return j.Retained, err
	}
	return j.Retained, nil
}

// retireOld deals with the replaced tree after a successful transaction: it is
// deleted only when it was reproducible (the app had a valid receipt) or is the
// stock-Frappe conversion; otherwise it may hold hand edits and is kept under
// .kb/recovery/<dir>-pre-receipt-<UTC>.
func retireOld(root string, j *kbstate.Journal) error {
	if j.OldPath == "" || !exists(j.OldPath) {
		return nil
	}
	if j.StockConversion || j.HadReceipt {
		if err := os.RemoveAll(j.OldPath); err != nil {
			return fmt.Errorf("remove previous source %s: %w", j.OldPath, err)
		}
		return nil
	}
	if j.Retained == "" {
		j.Retained = uniquePath(filepath.Join(kbstate.RecoveryDir(root), fmt.Sprintf("%s-pre-receipt-%s", j.Directory, kbstate.UTCStamp(time.Now()))))
		if err := j.Save(); err != nil {
			return err
		}
	}
	if err := kbstate.EnsureTree(root, kbstate.RecoveryDir(root)); err != nil {
		return err
	}
	if err := os.Rename(j.OldPath, j.Retained); err != nil {
		return fmt.Errorf("keep previous source at %s: %w", j.Retained, err)
	}
	return nil
}

// pendingRefusal builds the operator message for a journal that cannot be
// resolved automatically. It names the journal and every recorded path.
func pendingRefusal(root string, j *kbstate.Journal, reason string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "an interrupted %s of %s left %s at step %q: %s.", j.Command, j.App, kbstate.JournalPath(root), j.Step, reason)
	if j.OldPath != "" {
		fmt.Fprintf(&b, "\n  old source (backup): %s", j.OldPath)
	}
	if j.NewPath != "" {
		fmt.Fprintf(&b, "\n  new source (live path): %s", j.NewPath)
	}
	for _, p := range j.Paths {
		fmt.Fprintf(&b, "\n  recorded path: %s", p)
	}
	fmt.Fprintf(&b, "\nRestore by hand (for an upgrade: move the old source back to the live path), then delete %s. "+
		"kb will not change this bench until the journal is resolved.", kbstate.JournalPath(root))
	return &PendingError{Journal: kbstate.JournalPath(root), Cause: errors.New(b.String())}
}

// RecoverMode says how much of the journal a command may act on.
type RecoverMode int

const (
	// RecoverMutate is for add, install, site-install, upgrade, manage and
	// init-kb-frappe: discard what a pre-swap journal recorded, replay a
	// finalization, refuse what needs an operator.
	RecoverMutate RecoverMode = iota
	// RecoverAdopt discards pre-swap paths and refuses swapped and migrated.
	RecoverAdopt
	// RecoverAdoptCheck deletes nothing and refuses only swapped and migrated.
	RecoverAdoptCheck
)

// RecoverJournal resolves an earlier transaction before the current command
// starts. It must run under the bench lock (except RecoverAdoptCheck). Rules
// (contracts 6.3 as amended): planned, downloaded or staged: discard only the
// paths the journal recorded and go on; migrated: replay finalization; swapped
// for add, install and init-kb-frappe: re-run the idempotent registration and
// finalize; swapped for upgrade: refuse and print the recorded paths.
func RecoverJournal(ctx context.Context, mode RecoverMode) (notes []string, err error) {
	root := benchDir()
	js := kbstate.ReadJournal(root)
	switch {
	case js.Missing:
		return nil, nil
	case js.Newer != nil:
		return nil, js.Newer
	case js.Malformed != "":
		return nil, fmt.Errorf("%s is unusable (%s) — if a kb command was interrupted, restore the bench by hand, then delete the file", kbstate.JournalPath(root), js.Malformed)
	}
	j := js.Journal
	if !j.Pending() {
		return nil, nil
	}

	switch j.Step {
	case kbstate.StepPlanned, kbstate.StepDownloaded, kbstate.StepStaged:
		if mode == RecoverAdoptCheck {
			return nil, nil
		}
		for _, p := range j.Paths {
			if err := discardRecorded(root, p); err != nil {
				return notes, pendingRefusal(root, j, err.Error())
			}
			notes = append(notes, fmt.Sprintf("discarded %s recorded by the interrupted %s of %s", p, j.Command, j.App))
		}
		if err := j.Abort(errors.New("interrupted before the swap; recorded paths discarded")); err != nil {
			return notes, err
		}
		return notes, nil
	case kbstate.StepSwapped, kbstate.StepMigrated:
	default:
		return nil, pendingRefusal(root, j, "unknown step")
	}

	if mode != RecoverMutate {
		return nil, pendingRefusal(root, j, "adopt never touches apps/, so it cannot resolve this")
	}
	if j.Op == kbstate.OpAdopt {
		return nil, pendingRefusal(root, j, "an adopt journal cannot be at this step")
	}
	app, ok := apps.ByName(j.App)
	if !ok || app.Dir() != j.Directory {
		return nil, pendingRefusal(root, j, "the app is not in this kb's registry")
	}

	switch {
	case j.Step == kbstate.StepSwapped && j.Command == CmdUpgrade:
		return nil, pendingRefusal(root, j, "the upgrade stopped before bench migrate finished (migration boundary)")
	case j.Command != CmdAdd && j.Command != CmdInstall && j.Command != CmdInitFrappe && j.Command != CmdUpgrade:
		return nil, pendingRefusal(root, j, "unknown command")
	}

	st := defaultSteps(ctx, root, app, j.Command)
	if j.Step == kbstate.StepSwapped {
		// The move-in, registration or build may be incomplete: check that the
		// live tree is the one that was staged, then redo the idempotent steps.
		live := filepath.Join(root, "apps", j.Directory)
		if !isDir(live) {
			return nil, pendingRefusal(root, j, "the recorded new source is missing")
		}
		n, cerr := kbstate.CountEntries(live, app.PackageName())
		if cerr != nil || n != j.StagedFiles {
			return nil, pendingRefusal(root, j, fmt.Sprintf("the live tree has %d entries but the journal staged %d", n, j.StagedFiles))
		}
		if st.AppendAppsTxt != nil {
			if _, e := st.AppendAppsTxt(); e != nil {
				return nil, pendingRefusal(root, j, "replay: "+e.Error())
			}
		}
		for _, step := range []struct {
			name string
			f    func() (string, error)
		}{{"setup requirements", st.SetupRequirements}, {"pip install -e", st.PipInstall}, {"build assets", st.Build}, {"bench migrate", st.Migrate}} {
			if step.f == nil {
				continue
			}
			if _, e := step.f(); e != nil {
				return nil, pendingRefusal(root, j, fmt.Sprintf("replay of %s failed: %v", step.name, e))
			}
		}
	}
	if _, e := finalize(root, j, st); e != nil {
		return nil, pendingRefusal(root, j, "replay of finalization failed: "+e.Error())
	}
	notes = append(notes, fmt.Sprintf("replayed the interrupted %s of %s from step %s and finalized it", j.Command, j.App, mustStep(js)))
	return notes, nil
}

func mustStep(js kbstate.JournalState) string {
	if js.Journal == nil {
		return ""
	}
	return js.Journal.Step
}

// discardRecorded removes one path the journal recorded, refusing anything that
// is not clearly scratch: a relative path, the bench root or an ancestor, the
// apps directory, or anything under .kb/recovery.
func discardRecorded(root, p string) error {
	clean := filepath.Clean(p)
	rootClean := filepath.Clean(root)
	switch {
	case p == "" || !filepath.IsAbs(clean):
		return fmt.Errorf("recorded path %q is not absolute — not discarding it", p)
	case clean == rootClean || strings.HasPrefix(rootClean+string(filepath.Separator), clean+string(filepath.Separator)):
		return fmt.Errorf("recorded path %q is the bench root or one of its parents — not discarding it", p)
	case clean == filepath.Join(rootClean, "apps") || clean == kbstate.Dir(rootClean) || strings.HasPrefix(clean, kbstate.RecoveryDir(rootClean)+string(filepath.Separator)):
		return fmt.Errorf("recorded path %q is protected — not discarding it", p)
	}
	if err := os.RemoveAll(clean); err != nil {
		return fmt.Errorf("discard %s: %w", clean, err)
	}
	return nil
}
