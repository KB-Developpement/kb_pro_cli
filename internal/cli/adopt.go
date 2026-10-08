package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/KB-Developpement/kb_pro_cli/internal/adopt"
	"github.com/KB-Developpement/kb_pro_cli/internal/apps"
	"github.com/KB-Developpement/kb_pro_cli/internal/bench"
	"github.com/KB-Developpement/kb_pro_cli/internal/ident"
	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
)

// adopt exit codes (contracts 7.3). Overall exit = the worst per-app code.
const (
	adoptOK        = 0
	adoptMismatch  = 2
	adoptAmbiguous = 3
	adoptRefused   = 4
)

// maxDiffLines caps how many paths adopt prints per category and candidate.
const maxDiffLines = 40

func newAdoptCmd() *cobra.Command {
	var tagFlags []string
	var check bool

	cmd := &cobra.Command{
		Use:   "adopt",
		Short: "Give an installed archive bench its first receipt by matching it to a release",
		Long: `Establish where already-installed archive apps (and the KB Frappe framework) came from.

For each installed app that has no .git and no valid receipt, kb downloads the
candidate release v<version> (the version from the app's package or
sites/apps.json is only a hint) and compares the whole installed tree with it:
file set, SHA-256 of every file, file type, executable bit and symlink target.
Generated files (*.egg-info/, __pycache__/, *.pyc, node_modules/, <package>/public/dist/,
.DS_Store) that the release does not contain are ignored. Only a unique full match
writes a receipt (provenance: adopted). Name the tag yourself with --tag <app>=<tag>
(repeatable) when the hint is missing or wrong.

adopt never changes apps/, sites or the database. --check runs the comparison
without the bench lock and creates no file under the bench.

Exit codes: 0 adopted (or nothing to do), 2 mismatch, 3 ambiguous, 4 refused or stopped.`,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireInitializedForCLI(); err != nil {
				return err
			}
			if !bench.InBenchContainer() {
				return fmt.Errorf("kb must be run inside a Frappe bench container — use: ffm shell <bench-name>")
			}
			return runAdopt(cmd.Context(), cmd.OutOrStdout(), tagFlags, check)
		},
	}
	cmd.Flags().StringArrayVar(&tagFlags, "tag", nil, "Candidate release for an app: <app>=<tag> (repeatable)")
	cmd.Flags().BoolVar(&check, "check", false, "Compare only: no lock, nothing is created under the bench")
	return cmd
}

// adoptTarget is one app adopt considers.
type adoptTarget struct {
	app  apps.App
	tags []string // explicit --tag values
}

// parseAdoptTags parses repeated --tag values into per-app tag lists.
func parseAdoptTags(values []string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, v := range values {
		name, tag, ok := strings.Cut(v, "=")
		name, tag = strings.TrimSpace(name), strings.TrimSpace(tag)
		if !ok || name == "" || tag == "" {
			return nil, fmt.Errorf("--tag %q: expected <app>=<tag>, for example --tag kb_pro=v1.2.0", v)
		}
		app, known := apps.ByName(name)
		if !known {
			return nil, fmt.Errorf("--tag %q: %q is not a KB app", v, name)
		}
		if app.Name == "kb_distri" {
			return nil, fmt.Errorf("--tag %q: kb_distri is excluded from adopt until its package mapping is fixed", v)
		}
		if !ident.ValidRef(tag) {
			return nil, fmt.Errorf("--tag %q: %q is not a valid tag name", v, tag)
		}
		out[name] = append(out[name], tag)
	}
	return out, nil
}

// runAdopt runs the adopt (or adopt --check) over every installed registry app
// and the framework, one app at a time.
func runAdopt(ctx context.Context, out io.Writer, tagValues []string, check bool) error {
	tags, err := parseAdoptTags(tagValues)
	if err != nil {
		return &ExitError{Code: adoptRefused, Err: err}
	}
	if check {
		return adoptRun(ctx, out, tags, true)
	}
	root := bench.Root()
	if err := kbstate.CheckBenchOwner(root); err != nil {
		return &ExitError{Code: adoptRefused, Err: err}
	}
	lock, err := kbstate.AcquireLock(root)
	if err != nil {
		return &ExitError{Code: adoptRefused, Err: err}
	}
	defer lock.Release()
	if err := kbstate.CheckSchemas(root); err != nil {
		return &ExitError{Code: adoptRefused, Err: err}
	}
	kbstate.CleanTempFiles(root)
	return adoptRun(ctx, out, tags, false)
}

// adoptStop ends the whole run (licence standing, outage, timeout).
type adoptStop struct{ err error }

func (e *adoptStop) Error() string { return e.err.Error() }

func adoptRun(ctx context.Context, out io.Writer, tags map[string][]string, check bool) error {
	root := bench.Root()
	mode := bench.RecoverAdopt
	if check {
		mode = bench.RecoverAdoptCheck
	}
	notes, err := bench.RecoverJournal(ctx, mode)
	for _, n := range notes {
		fmt.Fprintln(out, n)
	}
	if err != nil {
		return &ExitError{Code: adoptRefused, Err: err}
	}

	token, serverURL, err := licenseTokenAndServer(ctx)
	if err != nil {
		return &ExitError{Code: adoptRefused, Err: fmt.Errorf("adopt needs a valid license for the candidate downloads: %w", err)}
	}
	allowed := allowedSetFn()

	var targets []adoptTarget
	for _, a := range apps.All {
		targets = append(targets, adoptTarget{app: a, tags: tags[a.Name]})
	}
	targets = append(targets, adoptTarget{app: apps.Framework, tags: tags[apps.Framework.Name]})

	// A --tag for an app that is not installed is a mistake worth stopping on.
	for _, t := range targets {
		if len(t.tags) > 0 && !isInstalledDir(root, t.app.Dir()) {
			return &ExitError{Code: adoptRefused, Err: fmt.Errorf("--tag names %s but apps/%s is not in the bench", t.app.Name, t.app.Dir())}
		}
	}

	worst := adoptOK
	raise := func(code int) {
		rank := map[int]int{adoptOK: 0, adoptMismatch: 1, adoptAmbiguous: 2, adoptRefused: 3}
		if rank[code] > rank[worst] {
			worst = code
		}
	}
	verb := "adopted"
	if check {
		verb = "would adopt"
	}
	for _, t := range targets {
		if !isInstalledDir(root, t.app.Dir()) {
			continue
		}
		code, err := adoptOne(ctx, out, root, serverURL, token, allowed, t, check, verb)
		var stop *adoptStop
		if errors.As(err, &stop) {
			fmt.Fprintf(out, "%s: stopped — %v\n", t.app.Name, stop.err)
			return &ExitError{Code: adoptRefused, Err: fmt.Errorf("adopt stopped at %s: %w", t.app.Name, stop.err)}
		}
		raise(code)
	}
	if worst == adoptOK {
		return nil
	}
	return &ExitError{Code: worst, Err: fmt.Errorf("adopt finished with exit code %d (%s)", worst, map[int]string{
		adoptMismatch: "at least one app does not match a release", adoptAmbiguous: "at least one app matches several releases", adoptRefused: "at least one app was refused"}[worst])}
}

func isInstalledDir(root, dir string) bool {
	fi, err := os.Stat(filepath.Join(root, "apps", dir))
	return err == nil && fi.IsDir()
}

// hintTags returns the candidate tags derived from the version hints: the
// package __version__ and the version in sites/apps.json. Hints never select a
// match; they only name candidates.
func hintTags(root string, app apps.App) []string {
	var versions []string
	if v := bench.ReadAppVersion(root, app.Dir(), app.PackageName()); v != "" {
		versions = append(versions, v)
	}
	if v := appsJSONVersion(root, app.PackageName()); v != "" {
		versions = append(versions, v)
	}
	var tags []string
	for _, v := range versions {
		tag := v
		if !strings.HasPrefix(tag, "v") {
			tag = "v" + tag
		}
		if ident.ValidRef(tag) {
			tags = append(tags, tag)
		}
	}
	return tags
}

type candidate struct {
	tag string
	dl  *license.Download
	man adopt.Manifest
}

// downloadStatus classifies a candidate download error (contracts 7.3):
// missing means "report and continue"; refuse means this app is refused;
// anything else stops the run.
func classifyCandidateErr(err error) (missing, refuse bool) {
	var he *license.HTTPError
	if errors.As(err, &he) {
		switch he.Status {
		case 404, 409, 400:
			return true, false
		}
		return false, false
	}
	var ce *license.CompatError
	if errors.As(err, &ce) {
		return false, true
	}
	return false, false
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

func adoptOne(ctx context.Context, out io.Writer, root, serverURL, token string, allowed map[string]bool, t adoptTarget, check bool, verb string) (int, error) {
	app := t.app
	name := app.Name
	dir := app.Dir()
	appPath := filepath.Join(root, "apps", dir)

	rs := kbstate.ReadReceipt(root, dir)
	switch {
	case rs.Newer != nil:
		fmt.Fprintf(out, "%s: REFUSED — %v\n", name, rs.Newer)
		return adoptRefused, nil
	case rs.Valid():
		fmt.Fprintf(out, "%s: skipped — already has a %s receipt (%s)\n", name, rs.Receipt.Provenance, rs.Receipt.Tag())
		return adoptOK, nil
	}
	if name == "kb_distri" {
		fmt.Fprintf(out, "%s: skipped — excluded from adopt until its package mapping is fixed\n", name)
		return adoptOK, nil
	}
	if !allowed[name] {
		fmt.Fprintf(out, "%s: skipped — not in your license\n", name)
		return adoptOK, nil
	}
	if gerr := bench.GitGuard(root, dir); gerr != nil {
		fmt.Fprintf(out, "%s: REFUSED — apps/%s holds a Git checkout (.git); adopt only matches archive installs\n", name, dir)
		return adoptRefused, nil
	}

	// Candidates: hint-derived tags first, then explicit --tag values.
	var tagList []string
	seen := map[string]bool{}
	for _, tg := range append(hintTags(root, app), t.tags...) {
		if !seen[tg] {
			seen[tg] = true
			tagList = append(tagList, tg)
		}
	}
	if len(tagList) == 0 {
		fmt.Fprintf(out, "%s: MISMATCH — no candidate release (no version hint); name one with --tag %s=<tag>\n", name, name)
		return adoptMismatch, nil
	}

	// Scratch space lives outside the bench and is removed on every exit path.
	tmp, err := os.MkdirTemp("", "kb-adopt-"+dir+"-*")
	if err != nil {
		return adoptRefused, &adoptStop{err: fmt.Errorf("create scratch directory: %w", err)}
	}
	defer os.RemoveAll(tmp)

	var j *kbstate.Journal
	if !check {
		j = kbstate.NewJournal(root)
		j.Op, j.Command, j.App, j.Directory = kbstate.OpAdopt, "adopt", app.LicenseID(), dir
		j.Paths = []string{tmp}
		if err := j.Advance(kbstate.StepPlanned); err != nil {
			return adoptRefused, &adoptStop{err: err}
		}
	}
	closeJournal := func(cause error) {
		if j != nil && j.Step != kbstate.StepFinalized {
			_ = j.Abort(cause)
		}
	}

	// Download every candidate (strict, release=1) into the scratch space.
	var cands []candidate
	var tried []string
	for i, tag := range tagList {
		dl, derr := license.DownloadApp(ctx, serverURL, token, license.DownloadRequest{
			App: app.LicenseID(), Repository: app.Repository(), Line: app.Line, Ref: tag, Release: true,
		})
		if derr != nil {
			missing, refuse := classifyCandidateErr(derr)
			switch {
			case refuse:
				fmt.Fprintf(out, "%s: REFUSED — %v\n", name, derr)
				closeJournal(derr)
				return adoptRefused, nil
			case missing:
				fmt.Fprintf(out, "%s: candidate %s is missing (%v)\n", name, tag, derr)
				tried = append(tried, tag)
				continue
			}
			closeJournal(derr)
			if isTimeout(derr) {
				derr = fmt.Errorf("the license server did not answer in time (%v) — retry later", derr)
			}
			return adoptRefused, &adoptStop{err: derr}
		}
		tried = append(tried, tag)
		cdir := filepath.Join(tmp, "c"+strconv.Itoa(i))
		xerr := os.MkdirAll(cdir, 0o700)
		if xerr == nil {
			xerr = bench.ExtractArchive(dl.Path, cdir)
		}
		os.Remove(dl.Path)
		if xerr != nil {
			closeJournal(xerr)
			return adoptRefused, &adoptStop{err: fmt.Errorf("candidate %s: %w", tag, xerr)}
		}
		cands = append(cands, candidate{tag: tag, dl: dl})
		cands[len(cands)-1].man, xerr = adopt.BuildManifest(cdir, nil)
		if xerr != nil {
			closeJournal(xerr)
			return adoptRefused, &adoptStop{err: fmt.Errorf("candidate %s: %w", tag, xerr)}
		}
	}
	if j != nil {
		if err := j.Advance(kbstate.StepDownloaded); err != nil {
			return adoptRefused, &adoptStop{err: err}
		}
	}
	if len(cands) == 0 {
		fmt.Fprintf(out, "%s: MISMATCH — none of the candidate releases (%s) could be fetched; name the right tag with --tag %s=<tag>\n", name, strings.Join(tried, ", "), name)
		closeJournal(errors.New("no candidate"))
		return adoptMismatch, nil
	}

	// Hash only what can be compared: anything an archive contains, or is not excluded.
	inArchive := func(rel string) bool {
		for _, c := range cands {
			if _, ok := c.man[rel]; ok {
				return true
			}
		}
		return false
	}
	pkg := app.PackageName()
	installed, merr := adopt.BuildManifest(appPath, func(rel string) bool {
		return inArchive(rel) || !kbstate.Excluded(rel, false, pkg)
	})
	if merr != nil {
		closeJournal(merr)
		return adoptRefused, &adoptStop{err: fmt.Errorf("read installed tree: %w", merr)}
	}
	if j != nil {
		if err := j.Advance(kbstate.StepStaged); err != nil {
			return adoptRefused, &adoptStop{err: err}
		}
	}

	// Uniqueness counts commits, not tags.
	matchByCommit := map[string]candidate{}
	var matchCommits []string
	var diffs []adopt.Diff
	var compared int
	for _, c := range cands {
		d := adopt.Compare(installed, c.man, pkg)
		diffs = append(diffs, d)
		if d.Match() {
			if _, dup := matchByCommit[c.dl.Commit]; !dup {
				matchByCommit[c.dl.Commit] = c
				matchCommits = append(matchCommits, c.dl.Commit)
			}
			compared = d.Compared
		}
	}

	switch {
	case len(matchCommits) == 0:
		fmt.Fprintf(out, "%s: MISMATCH — the installed tree is not a full match of %s\n", name, strings.Join(tried, ", "))
		for i, c := range cands {
			printDiff(out, c.tag, diffs[i])
		}
		closeJournal(errors.New("mismatch"))
		return adoptMismatch, nil
	case len(matchCommits) > 1:
		fmt.Fprintf(out, "%s: AMBIGUOUS — the installed tree fully matches %d different commits:\n", name, len(matchCommits))
		for _, cm := range matchCommits {
			fmt.Fprintf(out, "  %s  commit %s\n", matchByCommit[cm].tag, cm)
		}
		closeJournal(errors.New("ambiguous"))
		return adoptAmbiguous, nil
	}

	m := matchByCommit[matchCommits[0]]
	if check {
		fmt.Fprintf(out, "%s: %s %s (commit %s, %d files compared)\n", name, verb, m.tag, m.dl.Commit, compared)
		return adoptOK, nil
	}
	r := &kbstate.Receipt{
		SchemaVersion: kbstate.SupportedSchema,
		App:           app.LicenseID(),
		Directory:     dir,
		Package:       pkg,
		Repository:    m.dl.Repository,
		Line:          strconv.Itoa(app.Line),
		Requested:     m.dl.Requested,
		Ref:           m.dl.Ref,
		Commit:        m.dl.Commit,
		ArchiveSHA256: m.dl.SHA256,
		InstalledAt:   time.Now().UTC().Format(time.RFC3339),
		State:         kbstate.StateArchive,
		Provenance:    kbstate.ProvAdopted,
		Adopt:         &kbstate.AdoptInfo{ComparedFiles: compared, Exclusions: kbstate.Exclusions(pkg), CandidatesTried: tried},
	}
	if m.dl.ReleaseTag != "" {
		tag := m.dl.ReleaseTag
		r.ReleaseTag = &tag
	}
	j.PendingReceipt = r
	if err := j.Save(); err != nil {
		return adoptRefused, &adoptStop{err: err}
	}
	if err := kbstate.WriteReceipt(root, r); err != nil {
		closeJournal(err)
		return adoptRefused, &adoptStop{err: fmt.Errorf("write receipt: %w", err)}
	}
	if err := j.Advance(kbstate.StepFinalized); err != nil {
		return adoptRefused, &adoptStop{err: err}
	}
	fmt.Fprintf(out, "%s: %s %s (commit %s, %d files compared)\n", name, verb, m.tag, m.dl.Commit, compared)
	return adoptOK, nil
}

// printDiff lists differing paths by name only: never content or hashes.
func printDiff(out io.Writer, tag string, d adopt.Diff) {
	fmt.Fprintf(out, "  against %s:\n", tag)
	for _, cat := range []struct {
		label string
		paths []string
	}{{"added (installed only)", d.Added}, {"removed (release only)", d.Removed}, {"changed", d.Changed}} {
		if len(cat.paths) == 0 {
			continue
		}
		paths := append([]string(nil), cat.paths...)
		sort.Strings(paths)
		fmt.Fprintf(out, "    %s: %d\n", cat.label, len(paths))
		for i, p := range paths {
			if i >= maxDiffLines {
				fmt.Fprintf(out, "      … and %d more\n", len(paths)-maxDiffLines)
				break
			}
			fmt.Fprintf(out, "      %s\n", strconv.Quote(p))
		}
	}
}
