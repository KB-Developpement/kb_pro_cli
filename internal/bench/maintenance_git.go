package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/KB-Developpement/kb_pro_cli/internal/fsutil"
	"github.com/KB-Developpement/kb_pro_cli/internal/ident"
)

// GitObservation is an offline observation of the app's own repository.
type GitObservation struct {
	Head, Branch                             string
	Dirty                                    bool
	UntrackedFiles, Stashes, LinkedWorktrees int
	Changes                                  []string
}

func maintenanceComponent(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\") && strings.IndexFunc(s, func(r rune) bool { return r <= 32 || r == 127 }) < 0 && !strings.HasPrefix(s, "-")
}

func maintenanceAppPath(root, directory string) (string, error) {
	if !maintenanceComponent(directory) {
		return "", fmt.Errorf("invalid app directory %q", directory)
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	app := filepath.Join(root, "apps", directory)
	for _, p := range []string{root, filepath.Join(root, "apps"), app} {
		fi, err := os.Lstat(p)
		if err != nil {
			return "", err
		}
		if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%s is not a real directory", p)
		}
	}
	return app, nil
}

func ownMaintenanceGit(app string) error {
	fi, err := os.Lstat(filepath.Join(app, ".git"))
	if err != nil {
		return fmt.Errorf("app has no own Git directory: %w", err)
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return errors.New("maintenance requires a self-contained .git directory")
	}
	for _, p := range []string{"objects/info/alternates", "commondir", "info/grafts"} {
		if _, err := os.Lstat(filepath.Join(app, ".git", p)); err == nil {
			return fmt.Errorf("Git metadata is not self-contained (%s)", p)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return filepath.WalkDir(filepath.Join(app, ".git"), func(p string, de os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if de.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("Git metadata contains external symlink %s", p)
		}
		return nil
	})
}

// Git-specific environment overrides can redirect explicit repository paths.
// Keep the normal credential-helper environment, but remove all Git overrides.
func maintenanceGitEnv() []string {
	env := []string{}
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			env = append(env, e)
		}
	}
	return append(env, "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1")
}

func maintenanceGit(ctx context.Context, gitDir, workTree string, args ...string) (string, error) {
	full := []string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "--git-dir=" + gitDir}
	if workTree != "" {
		full = append(full, "--work-tree="+workTree)
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = filepath.Dir(gitDir)
	cmd.Env = maintenanceGitEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w", args[0], err)
	} // Helpers may print secrets; do not include stderr.
	return strings.TrimSuffix(string(out), "\n"), nil
}

func ObserveMaintenanceGit(root, directory string) (*GitObservation, error) {
	app, err := maintenanceAppPath(root, directory)
	if err != nil {
		return nil, err
	}
	if err = ownMaintenanceGit(app); err != nil {
		return nil, err
	}
	ctx := context.Background()
	g := func(a ...string) (string, error) { return maintenanceGit(ctx, filepath.Join(app, ".git"), app, a...) }
	o := &GitObservation{}
	if o.Head, err = g("rev-parse", "--verify", "HEAD^{commit}"); err != nil {
		return nil, err
	}
	o.Branch, _ = g("symbolic-ref", "--quiet", "--short", "HEAD")
	status, err := g("status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	// NUL records preserve paths containing newlines and spaces. Rename records
	// have a second path, which is skipped after recording the destination.
	records := strings.Split(status, "\x00")
	for i := 0; i < len(records); i++ {
		r := records[i]
		if r == "" {
			continue
		}
		o.Changes = append(o.Changes, r)
		if strings.HasPrefix(r, "?? ") {
			o.UntrackedFiles++
		}
		if len(r) >= 2 && (r[0] == 'R' || r[1] == 'R' || r[0] == 'C' || r[1] == 'C') {
			i++
		}
	}
	o.Dirty = len(o.Changes) > 0
	stash, err := g("stash", "list", "--format=%H")
	if err != nil {
		return nil, err
	}
	if stash != "" {
		o.Stashes = len(strings.Split(stash, "\n"))
	}
	wt, err := g("worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	o.LinkedWorktrees = strings.Count(wt, "worktree ") - 1
	if o.LinkedWorktrees < 0 {
		return nil, errors.New("cannot determine worktree count")
	}
	return o, nil
}

// Override only in package fixture tests. Production always uses HTTPS and the
// maintainer's native Git credential helper, never the licence-server token.
// Use the registry's canonical HTTPS form. Adding a .git suffix changes the
// credential path when a maintainer uses repository-scoped useHttpPath helpers.
var maintenanceRepositoryURL = func(repository string) string { return "https://github.com/" + repository }

func maintenanceRepository(repository string) error {
	if !ident.RepositoryRE.MatchString(repository) || !strings.HasPrefix(repository, "KB-Developpement/") {
		return fmt.Errorf("unapproved repository %q", repository)
	}
	return nil
}

// PrepareMaintenanceGit creates history and an index without touching live files.
func ValidateMaintenanceBranch(branch string) error {
	if !ident.ValidRef(branch) {
		return errors.New("invalid support branch")
	}
	cmd := exec.Command("git", "check-ref-format", "--branch", branch)
	cmd.Env = maintenanceGitEnv()
	if err := cmd.Run(); err != nil {
		return errors.New("invalid support branch")
	}
	return nil
}

func PrepareMaintenanceGit(ctx context.Context, root, directory, repository, base, branch string) (preparedPath string, err error) {
	app, err := maintenanceAppPath(root, directory)
	if err != nil {
		return "", err
	}
	if err = GitGuard(root, directory); err != nil {
		return "", err
	}
	if err = maintenanceRepository(repository); err != nil {
		return "", err
	}
	if !ident.CommitRE.MatchString(base) {
		return "", errors.New("base is not an immutable commit")
	}
	scratch, err := maintenanceScratch(root, "attach-")
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(scratch)
		}
	}()
	preparedPath = filepath.Join(scratch, ".git")
	if err = os.Mkdir(preparedPath, 0700); err != nil {
		return "", err
	}
	g := func(a ...string) (string, error) { return maintenanceGit(ctx, preparedPath, app, a...) }
	if _, err = g("check-ref-format", "--branch", branch); err != nil {
		return "", fmt.Errorf("invalid support branch: %w", err)
	}
	if _, err = g("init", "--initial-branch="+branch); err != nil {
		return "", err
	}
	if _, err = g("remote", "add", "origin", maintenanceRepositoryURL(repository)); err != nil {
		return "", err
	}
	if _, err = g("fetch", "--no-tags", "--no-write-fetch-head", "origin", base); err != nil {
		return "", err
	}
	head, e := g("rev-parse", "--verify", base+"^{commit}")
	if e != nil || head != base {
		return "", errors.New("fetched base does not match receipt commit")
	}
	// Archives with export transformations cannot be treated as ordinary edits.
	// Fail closed until a transformation has a proven archive comparison path.
	attrs, e := g("ls-tree", "-r", "--name-only", base)
	if e != nil {
		return "", e
	}
	for _, p := range strings.Split(attrs, "\n") {
		if filepath.Base(p) != ".gitattributes" {
			continue
		}
		content, e := g("show", base+":"+p)
		if e != nil {
			return "", e
		}
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "#") {
				continue
			}
			for _, f := range strings.Fields(line) {
				if f == "export-ignore" || f == "export-subst" || strings.HasPrefix(f, "export-ignore=") || strings.HasPrefix(f, "export-subst=") {
					return "", fmt.Errorf("archive transformations in %s require reconciliation before attachment", p)
				}
			}
		}
	}
	if _, err = g("update-ref", "refs/heads/"+branch, base); err != nil {
		return "", err
	}
	if _, err = g("symbolic-ref", "HEAD", "refs/heads/"+branch); err != nil {
		return "", err
	}
	if _, err = g("read-tree", base); err != nil {
		return "", err
	}
	if _, err = g("config", "core.bare", "false"); err != nil {
		return "", err
	}
	// No temporary worktree path is retained in the prepared metadata.
	if _, err = g("config", "core.worktree", ".."); err != nil {
		return "", err
	}
	if _, err = os.Lstat(filepath.Join(preparedPath, "objects", "info", "alternates")); !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("prepared repository has alternates")
	}
	err = nil
	if err = ownMaintenanceGit(filepath.Dir(preparedPath)); err != nil {
		return "", err
	}
	if err = syncMaintenanceTree(preparedPath); err != nil {
		return "", err
	}
	return preparedPath, nil
}

func maintenanceScratch(root, prefix string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, ".kb", "maintenance", "scratch")
	if err := maintenanceEnsureTree(root, dir); err != nil {
		return "", err
	}
	return os.MkdirTemp(dir, prefix)
}

// PublishMaintenanceGit atomically refuses every existing destination, even an
// empty foreign directory created after preflight. No rename fallback is safe.
func PublishMaintenanceGit(root, directory, preparedPath string) error {
	app, err := maintenanceAppPath(root, directory)
	if err != nil {
		return err
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	scratch := filepath.Join(absoluteRoot, ".kb", "maintenance", "scratch")
	rel, err := filepath.Rel(scratch, preparedPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return errors.New("prepared repository is outside maintenance scratch")
	}
	fi, err := os.Lstat(preparedPath)
	if err != nil {
		return err
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return errors.New("prepared Git metadata is not a directory")
	}
	if err = maintenanceRenameExclusive(preparedPath, filepath.Join(app, ".git")); err != nil {
		return fmt.Errorf("attach .git without replacement: %w", err)
	}
	if err = fsutil.SyncDir(app); err != nil {
		return err
	}
	return fsutil.SyncDir(filepath.Dir(preparedPath))
}

// SyncMaintenanceAppState writes the captured Bench 5.31.0 Git shape.
func SyncMaintenanceAppState(root, directory, pkg, head, branch string) error {
	if _, err := maintenanceAppPath(root, directory); err != nil {
		return err
	}
	if !ident.CommitRE.MatchString(head) {
		return errors.New("invalid Git commit")
	}
	p := filepath.Join(root, "sites", "apps.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	state := map[string]json.RawMessage{}
	if err = json.Unmarshal(raw, &state); err != nil || state == nil {
		return errors.New("apps.json is not a JSON object")
	}
	entry := map[string]json.RawMessage{}
	existing, ok := state[pkg]
	if !ok {
		return fmt.Errorf("apps.json is missing %s", pkg)
	}
	if err = json.Unmarshal(existing, &entry); err != nil || entry == nil {
		return fmt.Errorf("apps.json entry for %s is not an object", pkg)
	}
	for _, key := range []string{"required", "idx", "version"} {
		if _, ok := entry[key]; !ok {
			return fmt.Errorf("apps.json entry is missing %s", key)
		}
	}
	entry["is_repo"] = json.RawMessage("true")
	entry["resolution"], err = json.Marshal(map[string]string{"commit_hash": head, "branch": branch})
	if err != nil {
		return err
	}
	state[pkg], err = json.Marshal(entry)
	if err != nil {
		return err
	}
	out, err := json.MarshalIndent(state, "", "\t")
	if err != nil {
		return err
	}
	return fsutil.WriteFileDurable(p, out, 0644)
}

func ProtectMaintenanceCommits(root, directory, sessionID string, commits []string) ([]string, error) {
	if len(commits) > 64 {
		return nil, errors.New("protected commit limit exceeded")
	}
	if !maintenanceComponent(sessionID) {
		return nil, errors.New("invalid session id")
	}
	app, err := maintenanceAppPath(root, directory)
	if err != nil {
		return nil, err
	}
	if err = ownMaintenanceGit(app); err != nil {
		return nil, err
	}
	refs := []string{}
	seen := map[string]bool{}
	for _, c := range commits {
		if !ident.CommitRE.MatchString(c) {
			return nil, errors.New("invalid protected commit")
		}
		if seen[c] {
			continue
		}
		seen[c] = true
		head, e := maintenanceGit(context.Background(), filepath.Join(app, ".git"), app, "rev-parse", "--verify", c+"^{commit}")
		if e != nil || head != c {
			return nil, fmt.Errorf("protected commit %s is unavailable", c)
		}
		refs = append(refs, "refs/kb-maintenance/"+sessionID+"/"+c)
	}
	// One native update-ref transaction prevents partial protection writes.
	if len(refs) == 0 {
		return refs, nil
	}
	var input strings.Builder
	input.WriteString("start\n")
	for _, r := range refs {
		expected := filepath.Base(r)
		current, e := maintenanceGit(context.Background(), filepath.Join(app, ".git"), app, "show-ref", "--verify", "--hash", r)
		if e == nil {
			if current != expected {
				return nil, fmt.Errorf("protected ref %s was altered", r)
			}
			fmt.Fprintf(&input, "verify %s %s\n", r, expected)
		} else {
			fmt.Fprintf(&input, "create %s %s\n", r, expected)
		}
	}
	input.WriteString("prepare\ncommit\n")
	cmd := exec.Command("git", "-c", "core.hooksPath=/dev/null", "--git-dir="+filepath.Join(app, ".git"), "update-ref", "--stdin")
	cmd.Env = maintenanceGitEnv()
	cmd.Stdin = strings.NewReader(input.String())
	if err = cmd.Run(); err != nil {
		return nil, fmt.Errorf("protect commits: %w", err)
	}
	if err = syncMaintenanceTree(filepath.Join(app, ".git")); err != nil {
		return nil, err
	}
	return refs, nil
}

func SetPreparedMaintenanceOwner(preparedPath, sessionID string) error {
	if !maintenanceComponent(sessionID) {
		return errors.New("invalid session id")
	}
	p := filepath.Join(preparedPath, "kb-maintenance-session")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	data, marshalErr := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		SessionID     string `json:"session_id"`
	}{1, sessionID})
	if marshalErr != nil {
		f.Close()
		return marshalErr
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	e := f.Close()
	if err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	return fsutil.SyncDir(preparedPath)
}

func VerifyAttachedMaintenanceOwner(root, directory, sessionID string) error {
	app, err := maintenanceAppPath(root, directory)
	if err != nil {
		return err
	}
	if err = ownMaintenanceGit(app); err != nil {
		return err
	}
	p := filepath.Join(app, ".git", "kb-maintenance-session")
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return errors.New("Git ownership marker is not a regular file")
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	var owner struct {
		SchemaVersion int    `json:"schema_version"`
		SessionID     string `json:"session_id"`
	}
	if err = json.Unmarshal(raw, &owner); err != nil {
		return errors.New("malformed maintenance Git ownership marker")
	}
	if owner.SchemaVersion != 1 {
		return errors.New("unsupported maintenance Git ownership marker schema")
	}
	if owner.SessionID != sessionID {
		return errors.New("Git repository belongs to another maintenance session")
	}
	return nil
}

func MaintenanceCommits(root, directory, base string) ([]string, error) {
	if !ident.CommitRE.MatchString(base) {
		return nil, errors.New("invalid base commit")
	}
	app, err := maintenanceAppPath(root, directory)
	if err != nil {
		return nil, err
	}
	if err = ownMaintenanceGit(app); err != nil {
		return nil, err
	}
	g := func(a ...string) (string, error) {
		return maintenanceGit(context.Background(), filepath.Join(app, ".git"), app, a...)
	}
	if _, err = g("merge-base", "--is-ancestor", base, "HEAD"); err != nil {
		return nil, errors.New("maintenance HEAD no longer contains its recorded base")
	}
	out, err := g("rev-list", "--reverse", "--max-count=65", base+"..HEAD")
	if err != nil {
		return nil, err
	}
	commits := []string{}
	if out != "" {
		commits = strings.Split(out, "\n")
	}
	head, err := g("rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	if len(commits) == 0 {
		commits = append(commits, head)
	}
	if len(commits) > 64 {
		return nil, errors.New("maintenance commits exceed 64; reconcile before reporting")
	}
	return commits, nil
}
