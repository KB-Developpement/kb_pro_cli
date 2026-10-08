package bench

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// GitRefusal is returned when apps/<dir> holds a .git entry of its own.
type GitRefusal struct {
	Dir  string
	Path string
	Kind string // "directory", "file" (linked worktree) or "symlink"
}

func (e *GitRefusal) Error() string {
	return fmt.Sprintf("apps/%s holds a Git checkout (%s is a %s) — kb replaces and deletes archive installs only, never a directory under Git. "+
		"Nothing was changed. Manage this app with git, or move the checkout away yourself first", e.Dir, e.Path, e.Kind)
}

// GitGuard refuses when apps/<dir>/.git exists, whatever it is: a directory
// (clone), a file (linked worktree) or a symlink. Only the app's own .git
// counts: a .git in a parent directory (a bench kept under Git) is ignored, and
// no git command is run, so nothing can resolve to a parent repository.
func GitGuard(root, dir string) error {
	p := filepath.Join(root, "apps", dir, ".git")
	fi, err := os.Lstat(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("cannot inspect %s: %w", p, err) // fail closed
	}
	kind := "directory"
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		kind = "symlink"
	case !fi.IsDir():
		kind = "file"
	}
	return &GitRefusal{Dir: dir, Path: p, Kind: kind}
}

// GitGuardAll preflights a whole selection and names every refused app.
func GitGuardAll(root string, dirs []string) error {
	var msgs []string
	for _, d := range dirs {
		if err := GitGuard(root, d); err != nil {
			msgs = append(msgs, err.Error())
		}
	}
	switch len(msgs) {
	case 0:
		return nil
	case 1:
		return errors.New(msgs[0])
	default:
		return fmt.Errorf("%d apps hold a Git checkout, nothing was changed:\n  %s", len(msgs), strings.Join(msgs, "\n  "))
	}
}

// gitIn runs git against apps/<dir>'s own repository only. --git-dir and
// --work-tree are explicit and the working directory is the app, so git can
// never discover and use a parent repository. GIT_OPTIONAL_LOCKS=0 keeps
// `git status` from rewriting .git/index.
func gitIn(appDir string, args ...string) (string, error) {
	full := []string{
		"-c", "core.fsmonitor=false",
		"--git-dir=" + filepath.Join(appDir, ".git"),
		"--work-tree=" + appDir,
	}
	full = append(full, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = appDir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w (%s)", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

var scpRemoteRE = regexp.MustCompile(`^([A-Za-z0-9._-]+)@([A-Za-z0-9.-]+):(.+)$`)

// remote is a parsed git remote URL.
type remote struct {
	scheme string // "https", "http", "ssh", "git" or "scp"
	user   string
	host   string // lower case
	path   string // no leading slash, no trailing slash, no .git suffix
	extra  bool   // password, port, query or fragment present
}

func normaliseRemotePath(p string) string {
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimSuffix(p, "/")
	p = strings.TrimSuffix(p, ".git")
	return strings.TrimSuffix(p, "/")
}

func parseRemote(raw string) (remote, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return remote{}, false
	}
	if !strings.Contains(raw, "://") {
		m := scpRemoteRE.FindStringSubmatch(raw)
		if m == nil {
			return remote{}, false
		}
		return remote{scheme: "scp", user: m[1], host: strings.ToLower(m[2]), path: normaliseRemotePath(m[3])}, true
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return remote{}, false
	}
	r := remote{scheme: strings.ToLower(u.Scheme), host: strings.ToLower(u.Hostname()), path: normaliseRemotePath(u.Path)}
	if u.User != nil {
		r.user = u.User.Username()
		if _, hasPw := u.User.Password(); hasPw {
			r.extra = true
		}
	}
	if u.Port() != "" || u.RawQuery != "" || u.Fragment != "" {
		r.extra = true
	}
	return r, true
}

// remoteIs reports whether raw is GitHub <path>, whatever the transport. The
// host and the whole path must match; a substring never does.
func remoteIs(raw, path string) bool {
	r, ok := parseRemote(raw)
	return ok && !r.extra && r.host == "github.com" && r.path == path
}

// isStockRemoteForm is the strict stock-Frappe predicate for one URL: exactly
// https://github.com/frappe/frappe, git@github.com:frappe/frappe or
// ssh://git@github.com/frappe/frappe; host case-insensitive, .git suffix and
// trailing slash optional. http://, other hosts, other users, ports and any
// other path fail.
func isStockRemoteForm(raw string) bool {
	r, ok := parseRemote(raw)
	if !ok || r.extra || r.host != "github.com" || r.path != "frappe/frappe" {
		return false
	}
	switch r.scheme {
	case "https":
		return r.user == ""
	case "ssh", "scp":
		return r.user == "git"
	}
	return false
}

// remoteURLs returns every distinct fetch and push URL from `git remote -v`.
func remoteURLs(appDir string) ([]string, error) {
	out, err := gitIn(appDir, "remote", "-v")
	if err != nil {
		return nil, fmt.Errorf("could not read frappe git remotes: %w", err)
	}
	seen := map[string]bool{}
	var urls []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || seen[f[1]] {
			continue
		}
		seen[f[1]] = true
		urls = append(urls, f[1])
	}
	return urls, nil
}

// DetectFrappeOrigin checks the git remotes of apps/frappe to determine whether
// it is the stock Frappe repo (frappe/frappe) or the KB fork
// (KB-Developpement/kb_frappe). Returns (true, nil) for stock Frappe,
// (false, nil) for the KB fork, and a non-nil error when apps/frappe has no .git
// of its own (a parent repository is never consulted), cannot be read, or has no
// recognisable remote. Remotes are parsed: host and exact path, never a
// substring, so frappe/frappe-fork is not stock Frappe.
func DetectFrappeOrigin() (isStock bool, err error) {
	frappeDir := filepath.Join(benchDir(), "apps", "frappe")
	if _, statErr := os.Lstat(filepath.Join(frappeDir, ".git")); statErr != nil {
		return false, fmt.Errorf("apps/frappe has no .git of its own, so its origin cannot be determined")
	}
	urls, err := remoteURLs(frappeDir)
	if err != nil {
		return false, err
	}
	for _, u := range urls {
		if remoteIs(u, "KB-Developpement/kb_frappe") {
			return false, nil
		}
	}
	for _, u := range urls {
		if remoteIs(u, "frappe/frappe") {
			return true, nil
		}
	}
	return false, fmt.Errorf("unrecognised frappe remotes — cannot determine Frappe origin")
}

// StockRefusal explains why apps/frappe is not a convertible stock checkout.
type StockRefusal struct{ Reason string }

func (e *StockRefusal) Error() string {
	return "apps/frappe is a Git checkout that is not a clean stock frappe/frappe clone (" + e.Reason +
		") — kb will not replace it, and --force does not bypass this. Nothing was changed"
}

// CheckStockFrappe is the stock-Frappe predicate (contracts 7.2): the only case
// where kb replaces a directory that holds a .git. It is evaluated offline and
// returns nil only when all of these hold: apps/frappe/.git is a directory;
// every remote URL is GitHub frappe/frappe in one of the three exact forms; no
// tracked change and no untracked non-ignored file; no stash; no linked
// worktree; no merge, rebase, cherry-pick or revert in progress; exactly one
// local branch, with an upstream, checked out; and no local commit ahead of it.
func CheckStockFrappe() error {
	frappeDir := filepath.Join(benchDir(), "apps", "frappe")
	gitDir := filepath.Join(frappeDir, ".git")
	refuse := func(format string, a ...any) error { return &StockRefusal{Reason: fmt.Sprintf(format, a...)} }

	fi, err := os.Lstat(gitDir)
	switch {
	case err != nil:
		return refuse("no .git: %v", err)
	case !fi.IsDir():
		return refuse(".git is not a directory (a linked worktree or a symlink)")
	}
	for _, marker := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply"} {
		if _, err := os.Lstat(filepath.Join(gitDir, marker)); err == nil {
			return refuse("a merge, rebase, cherry-pick or revert is in progress (%s)", marker)
		}
	}

	urls, err := remoteURLs(frappeDir)
	if err != nil {
		return refuse("%v", err)
	}
	if len(urls) == 0 {
		return refuse("no remote")
	}
	for _, u := range urls {
		if !isStockRemoteForm(u) {
			return refuse("remote %q is not one of the three GitHub frappe/frappe forms", u)
		}
	}

	out, err := gitIn(frappeDir, "status", "--porcelain")
	if err != nil {
		return refuse("%v", err)
	}
	if strings.TrimSpace(out) != "" {
		return refuse("there are tracked changes or untracked files")
	}
	if out, err = gitIn(frappeDir, "stash", "list"); err != nil {
		return refuse("%v", err)
	} else if strings.TrimSpace(out) != "" {
		return refuse("there is a stash")
	}
	if out, err = gitIn(frappeDir, "worktree", "list", "--porcelain"); err != nil {
		return refuse("%v", err)
	}
	if n := strings.Count(out, "worktree "); n != 1 {
		return refuse("it has %d worktrees", n)
	}

	out, err = gitIn(frappeDir, "for-each-ref", "--format=%(refname)%09%(upstream)", "refs/heads")
	if err != nil {
		return refuse("%v", err)
	}
	branches := strings.Split(strings.TrimSpace(out), "\n")
	if len(branches) != 1 || branches[0] == "" {
		return refuse("it does not have exactly one local branch")
	}
	parts := strings.SplitN(branches[0], "\t", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
		return refuse("its only local branch has no upstream")
	}
	head, err := gitIn(frappeDir, "symbolic-ref", "-q", "HEAD")
	if err != nil || strings.TrimSpace(head) != parts[0] {
		return refuse("HEAD is not on its only local branch")
	}
	ahead, err := gitIn(frappeDir, "rev-list", "@{u}..HEAD")
	if err != nil {
		return refuse("%v", err)
	}
	if strings.TrimSpace(ahead) != "" {
		return refuse("it has local commits not in its upstream")
	}
	return nil
}
