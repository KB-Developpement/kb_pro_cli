package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KB-Developpement/kb_pro_cli/internal/testutil"
)

func TestGitGuard(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string, dir bool) {
		p := filepath.Join(root, "apps", rel)
		if dir {
			_ = os.MkdirAll(p, 0o755)
		} else {
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			_ = os.WriteFile(p, []byte("gitdir: x"), 0o644)
		}
	}
	mk("clone/.git", true)
	mk("worktree/.git", false)
	mk("archive/hooks.py", false)
	_ = os.Symlink("/nowhere", filepath.Join(root, "apps", "archive", ".git"))
	mk("plain/hooks.py", false)

	cases := map[string]string{"clone": "directory", "worktree": "file", "archive": "symlink", "plain": "", "missing": ""}
	for dir, kind := range cases {
		err := GitGuard(root, dir)
		if kind == "" {
			if err != nil {
				t.Errorf("GitGuard(%s) = %v", dir, err)
			}
			continue
		}
		gr, ok := err.(*GitRefusal)
		if !ok || gr.Kind != kind || !strings.Contains(err.Error(), "apps/"+dir) {
			t.Errorf("GitGuard(%s) = %v, want a %s refusal", dir, err, kind)
		}
	}
	err := GitGuardAll(root, []string{"plain", "clone", "worktree"})
	if err == nil || !strings.Contains(err.Error(), "apps/clone") || !strings.Contains(err.Error(), "apps/worktree") {
		t.Errorf("GitGuardAll = %v, want both refused apps named", err)
	}
	if err := GitGuardAll(root, []string{"plain"}); err != nil {
		t.Error(err)
	}
}

// A bench kept under Git: an app without a .git of its own is never refused,
// and no git command is ever pointed at the parent repository.
func TestGitGuardIgnoresParentRepository(t *testing.T) {
	root := t.TempDir()
	testutil.IsolateGit(t)
	testutil.Git(t, root, "init", "-q")
	_ = os.MkdirAll(filepath.Join(root, "apps", "kb_pro"), 0o755)
	if err := GitGuard(root, "kb_pro"); err != nil {
		t.Fatalf("parent .git made the app look like a checkout: %v", err)
	}
}

func TestParseRemoteForms(t *testing.T) {
	stock := []string{
		"https://github.com/frappe/frappe",
		"git@github.com:frappe/frappe",
		"ssh://git@github.com/frappe/frappe",
		"https://GitHub.com/frappe/frappe/",
		"https://github.com/frappe/frappe.git",
		"git@GITHUB.com:frappe/frappe.git",
		"ssh://git@github.com/frappe/frappe.git/",
	}
	for _, u := range stock {
		if !isStockRemoteForm(u) {
			t.Errorf("%q should be a stock remote form", u)
		}
	}
	notStock := []string{
		"http://github.com/frappe/frappe",
		"https://example.com/frappe/frappe",
		"https://github.com/frappe/frappe-fork",
		"https://github.com/frappe/frappe-fork.git",
		"https://github.com/someone/frappe",
		"https://github.com/Frappe/frappe",
		"https://github.com/frappe/frappe/extra",
		"https://user@github.com/frappe/frappe",
		"https://github.com:8443/frappe/frappe",
		"https://github.com/frappe/frappe?x=1",
		"git://github.com/frappe/frappe",
		"ssh://deploy@github.com/frappe/frappe",
		"deploy@github.com:frappe/frappe",
		"git@example.com:frappe/frappe",
		"https://notgithub.com/frappe/frappe",
		"https://github.com.evil.io/frappe/frappe",
		"/local/path/frappe",
		"",
	}
	for _, u := range notStock {
		if isStockRemoteForm(u) {
			t.Errorf("%q must not be a stock remote form", u)
		}
	}
	if !remoteIs("https://github.com/KB-Developpement/kb_frappe.git", "KB-Developpement/kb_frappe") {
		t.Error("kb_frappe remote not recognised")
	}
	if remoteIs("https://github.com/KB-Developpement/kb_frappe-old", "KB-Developpement/kb_frappe") {
		t.Error("a longer repository name matched by substring")
	}
}

func benchWithGit(t *testing.T, remote string) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("KB_BENCH_ROOT", root)
	testutil.StockClone(t, filepath.Join(root, "apps", "frappe"), remote)
	return filepath.Join(root, "apps", "frappe")
}

func TestDetectFrappeOrigin(t *testing.T) {
	cases := []struct {
		remote  string
		isStock bool
		wantErr bool
	}{
		{"https://github.com/frappe/frappe.git", true, false},
		{"git@github.com:frappe/frappe", true, false},
		{"https://github.com/KB-Developpement/kb_frappe.git", false, false},
		{"https://github.com/frappe/frappe-fork.git", false, true},
		{"https://github.com/KB-Developpement/kb_frappe-old", false, true},
		{"https://example.com/frappe/frappe", false, true},
	}
	for _, c := range cases {
		t.Run(c.remote, func(t *testing.T) {
			benchWithGit(t, c.remote)
			stock, err := DetectFrappeOrigin()
			if stock != c.isStock || (err != nil) != c.wantErr {
				t.Fatalf("DetectFrappeOrigin = %v, %v; want %v, err=%v", stock, err, c.isStock, c.wantErr)
			}
		})
	}
}

// apps/frappe without a .git of its own must not pick up the parent repository,
// even when the parent's origin is frappe/frappe.
func TestDetectFrappeOriginIgnoresParentRepository(t *testing.T) {
	root := t.TempDir()
	t.Setenv("KB_BENCH_ROOT", root)
	testutil.StockClone(t, root, "https://github.com/frappe/frappe.git") // the BENCH is the repo
	_ = os.MkdirAll(filepath.Join(root, "apps", "frappe"), 0o755)
	stock, err := DetectFrappeOrigin()
	if stock || err == nil {
		t.Fatalf("DetectFrappeOrigin = %v, %v; want not stock and an error", stock, err)
	}
}

func TestCheckStockFrappe(t *testing.T) {
	good := "https://github.com/frappe/frappe.git"
	cases := []struct {
		name   string
		remote string
		setup  func(t *testing.T, dir string)
		want   string // "" = ok; else a substring of the refusal
	}{
		{name: "clean stock", remote: good},
		{name: "clean stock ssh form", remote: "ssh://git@github.com/frappe/frappe"},
		{name: "ignored file is fine", remote: good, setup: func(t *testing.T, dir string) {
			_ = os.WriteFile(filepath.Join(dir, "ignored.log"), []byte("x"), 0o644)
		}},
		{name: "tracked change", remote: good, want: "tracked changes", setup: func(t *testing.T, dir string) {
			_ = os.WriteFile(filepath.Join(dir, "README.md"), []byte("edited\n"), 0o644)
		}},
		{name: "untracked file", remote: good, want: "untracked", setup: func(t *testing.T, dir string) {
			_ = os.WriteFile(filepath.Join(dir, "new.py"), []byte("x"), 0o644)
		}},
		{name: "stash", remote: good, want: "stash", setup: func(t *testing.T, dir string) {
			_ = os.WriteFile(filepath.Join(dir, "README.md"), []byte("edited\n"), 0o644)
			testutil.Git(t, dir, "stash", "-q")
		}},
		{name: "http remote", remote: "http://github.com/frappe/frappe", want: "remote"},
		{name: "other host", remote: "https://example.com/frappe/frappe", want: "remote"},
		{name: "fork repo", remote: "https://github.com/frappe/frappe-fork.git", want: "remote"},
		{name: "second remote", remote: good, want: "remote", setup: func(t *testing.T, dir string) {
			testutil.Git(t, dir, "remote", "add", "mine", "https://github.com/me/frappe.git")
		}},
		{name: "extra local branch", remote: good, want: "exactly one local branch", setup: func(t *testing.T, dir string) {
			testutil.Git(t, dir, "branch", "other")
		}},
		{name: "extra linked worktree", remote: good, want: "worktrees", setup: func(t *testing.T, dir string) {
			testutil.Git(t, dir, "worktree", "add", "-q", "--detach", filepath.Join(t.TempDir(), "wt"))
		}},
		{name: "local commit ahead", remote: good, want: "local commits", setup: func(t *testing.T, dir string) {
			_ = os.WriteFile(filepath.Join(dir, "mine.py"), []byte("x"), 0o644)
			testutil.Git(t, dir, "add", ".")
			testutil.Git(t, dir, "commit", "-q", "-m", "local")
		}},
		{name: "no upstream", remote: good, want: "upstream", setup: func(t *testing.T, dir string) {
			testutil.Git(t, dir, "config", "--unset", "branch.main.merge")
		}},
		{name: "merge in progress", remote: good, want: "in progress", setup: func(t *testing.T, dir string) {
			_ = os.WriteFile(filepath.Join(dir, ".git", "MERGE_HEAD"), []byte(strings.Repeat("a", 40)+"\n"), 0o644)
		}},
		{name: "rebase in progress", remote: good, want: "in progress", setup: func(t *testing.T, dir string) {
			_ = os.MkdirAll(filepath.Join(dir, ".git", "rebase-merge"), 0o755)
		}},
		{name: "cherry-pick in progress", remote: good, want: "in progress", setup: func(t *testing.T, dir string) {
			_ = os.WriteFile(filepath.Join(dir, ".git", "CHERRY_PICK_HEAD"), []byte(strings.Repeat("a", 40)+"\n"), 0o644)
		}},
		{name: ".git is a file", remote: good, want: "not a directory", setup: func(t *testing.T, dir string) {
			_ = os.RemoveAll(filepath.Join(dir, ".git"))
			_ = os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /x"), 0o644)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := benchWithGit(t, c.remote)
			if c.setup != nil {
				c.setup(t, dir)
			}
			before := testutil.TreeDigest(t, dir)
			err := CheckStockFrappe()
			if after := testutil.TreeDigest(t, dir); after != before {
				t.Error("the predicate changed the checkout (it must be read-only)")
			}
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("predicate refused a clean stock checkout: %v", err)
			case c.want != "" && err == nil:
				t.Fatalf("predicate accepted %q", c.name)
			case c.want != "" && !strings.Contains(err.Error(), c.want):
				t.Fatalf("refusal = %v, want it to mention %q", err, c.want)
			}
		})
	}
}
