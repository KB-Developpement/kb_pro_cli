package bench

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/KB-Developpement/kb_pro_cli/internal/apps"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
)

func maintenanceTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = maintenanceGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestMaintenanceGitUsesRegistryCredentialPath(t *testing.T) {
	for _, name := range []string{"kb_compta", "kb_stock"} {
		app, ok := apps.ByName(name)
		if !ok {
			t.Fatal("READY app missing from registry")
		}
		if got := maintenanceRepositoryURL("KB-Developpement/" + app.Repository()); got != app.URL {
			t.Fatalf("Git URL changes repository-scoped credential path: %s != %s", got, app.URL)
		}
	}
}
func maintenanceTestWrite(t *testing.T, p, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func maintenanceFixture(t *testing.T) (root, remote, base string) {
	t.Helper()
	root = t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0755); err != nil {
		t.Fatal(err)
	}
	maintenanceTestGit(t, source, "init", "--initial-branch=erpnext")
	maintenanceTestGit(t, source, "config", "user.name", "Fixture")
	maintenanceTestGit(t, source, "config", "user.email", "fixture@example.test")
	maintenanceTestWrite(t, filepath.Join(source, "kb_compta", "__init__.py"), "__version__ = '1.0.0'\n", 0644)
	maintenanceTestWrite(t, filepath.Join(source, "kb_compta", "hooks.py"), "app_name = 'kb_compta'\n", 0644)
	maintenanceTestWrite(t, filepath.Join(source, "script"), "#!/bin/sh\necho base\n", 0755)
	maintenanceTestWrite(t, filepath.Join(source, "tracked"), "base\n", 0644)
	maintenanceTestWrite(t, filepath.Join(source, ".gitignore"), "__pycache__/\n", 0644)
	if err := os.Symlink("tracked", filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	maintenanceTestGit(t, source, "add", ".")
	maintenanceTestGit(t, source, "commit", "-m", "base")
	base = maintenanceTestGit(t, source, "rev-parse", "HEAD")
	remote = filepath.Join(root, "remote.git")
	maintenanceTestGit(t, root, "clone", "--bare", source, remote)
	app := filepath.Join(root, "apps", "kb_compta")
	if err := os.MkdirAll(app, 0755); err != nil {
		t.Fatal(err)
	}
	entries, _, err := maintenanceManifest(source)
	if err != nil {
		t.Fatal(err)
	}
	filtered := []MaintenanceManifestEntry{}
	for _, e := range entries {
		if e.Path == ".git" || strings.HasPrefix(e.Path, ".git/") {
			continue
		}
		filtered = append(filtered, e)
	}
	if err = copyMaintenanceTree(source, app, filtered); err != nil {
		t.Fatal(err)
	}
	prior := maintenanceRepositoryURL
	maintenanceRepositoryURL = func(string) string { return remote }
	t.Cleanup(func() { maintenanceRepositoryURL = prior })
	return root, remote, base
}
func maintenanceAttach(t *testing.T, root, base string) {
	t.Helper()
	p, err := PrepareMaintenanceGit(context.Background(), root, "kb_compta", "KB-Developpement/kb_compta", base, "support/test")
	if err != nil {
		t.Fatal(err)
	}
	if err = SetPreparedMaintenanceOwner(p, "session-1"); err != nil {
		t.Fatal(err)
	}
	if err = PublishMaintenanceGit(root, "kb_compta", p); err != nil {
		t.Fatal(err)
	}
}
func maintenanceAppManifest(t *testing.T, root string) []MaintenanceManifestEntry {
	t.Helper()
	entries, _, err := maintenanceManifest(filepath.Join(root, "apps", "kb_compta"))
	if err != nil {
		t.Fatal(err)
	}
	filtered := []MaintenanceManifestEntry{}
	for _, e := range entries {
		if e.Path == ".git" || strings.HasPrefix(e.Path, ".git/") {
			continue
		}
		filtered = append(filtered, e)
	}
	return filtered
}

func TestMaintenanceAttachPreservesEverything(t *testing.T) {
	root, _, base := maintenanceFixture(t)
	app := filepath.Join(root, "apps", "kb_compta")
	maintenanceTestWrite(t, filepath.Join(app, "tracked"), "edited\n", 0644)
	maintenanceTestWrite(t, filepath.Join(app, "untracked"), "private\n", 0644)
	maintenanceTestWrite(t, filepath.Join(app, "__pycache__", "ignored.pyc"), "ignored\n", 0600)
	before := maintenanceAppManifest(t, root)
	maintenanceAttach(t, root, base)
	after := maintenanceAppManifest(t, root)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("entry changed working files, modes or symlinks")
	}
	o, err := ObserveMaintenanceGit(root, "kb_compta")
	if err != nil {
		t.Fatal(err)
	}
	if o.Head != base || o.Branch != "support/test" || !o.Dirty || o.UntrackedFiles != 1 {
		t.Fatalf("observation %+v", o)
	}
	branches := maintenanceTestGit(t, app, "for-each-ref", "--format=%(refname)", "refs/heads")
	if branches != "refs/heads/support/test" {
		t.Fatal(branches)
	}
	if _, err = os.Lstat(filepath.Join(app, ".git", "objects", "info", "alternates")); !os.IsNotExist(err) {
		t.Fatal("attached alternates")
	}
	if err = VerifyAttachedMaintenanceOwner(root, "kb_compta", "session-1"); err != nil {
		t.Fatal(err)
	}
}

func TestMaintenancePublishRefusesDestinationRace(t *testing.T) {
	root, _, base := maintenanceFixture(t)
	p, err := PrepareMaintenanceGit(context.Background(), root, "kb_compta", "KB-Developpement/kb_compta", base, "support/test")
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "apps", "kb_compta", ".git")
	if err = os.Mkdir(dest, 0755); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(dest)
	if err = PublishMaintenanceGit(root, "kb_compta", p); err == nil {
		t.Fatal("replaced foreign empty directory")
	}
	after, _ := os.Stat(dest)
	if !os.SameFile(fi, after) {
		t.Fatal("foreign Git inode changed")
	}
	if _, err = os.Stat(p); err != nil {
		t.Fatal("prepared metadata lost on refusal")
	}
}

func TestMaintenanceParentRepositoryIgnored(t *testing.T) {
	root, _, _ := maintenanceFixture(t)
	maintenanceTestGit(t, root, "init")
	if _, err := ObserveMaintenanceGit(root, "kb_compta"); err == nil {
		t.Fatal("used parent repository")
	}
	if err := VerifyMaintenanceRelease(context.Background(), root, "kb_compta", "KB-Developpement/kb_compta", "v1.0.0", strings.Repeat("a", 40), nil); err == nil {
		t.Fatal("release used parent repository")
	}
}

func TestMaintenanceGitFailuresPreserveFiles(t *testing.T) {
	root, _, base := maintenanceFixture(t)
	before := maintenanceAppManifest(t, root)
	for _, branch := range []string{"bad..branch", "-bad"} {
		if _, err := PrepareMaintenanceGit(context.Background(), root, "kb_compta", "KB-Developpement/kb_compta", base, branch); err == nil {
			t.Fatal("invalid branch accepted")
		}
	}
	if _, err := PrepareMaintenanceGit(context.Background(), root, "kb_compta", "KB-Developpement/kb_compta", strings.Repeat("a", 40), "support/test"); err == nil {
		t.Fatal("missing base accepted")
	}
	if !reflect.DeepEqual(before, maintenanceAppManifest(t, root)) {
		t.Fatal("failed entry changed files")
	}
	if _, err := os.Lstat(filepath.Join(root, "apps", "kb_compta", ".git")); !os.IsNotExist(err) {
		t.Fatal("failed entry attached Git")
	}
}

func TestMaintenanceAppStatePreservesRawValues(t *testing.T) {
	root, _, base := maintenanceFixture(t)
	p := filepath.Join(root, "sites", "apps.json")
	maintenanceTestWrite(t, p, `{"kb_compta":{"is_repo":false,"resolution":"not a repo","required":["kb_pro"],"idx":7,"version":"1.9","custom":{"x":true}},"other":{"nested":[1,true,"x"]}}`, 0644)
	var before map[string]json.RawMessage
	raw, _ := os.ReadFile(p)
	_ = json.Unmarshal(raw, &before)
	if err := SyncMaintenanceAppState(root, "kb_compta", "kb_compta", base, "support/test"); err != nil {
		t.Fatal(err)
	}
	var after map[string]json.RawMessage
	raw, _ = os.ReadFile(p)
	_ = json.Unmarshal(raw, &after)
	var b, a any
	_ = json.Unmarshal(before["other"], &b)
	_ = json.Unmarshal(after["other"], &a)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("unrelated entry changed")
	}
	var entry map[string]any
	_ = json.Unmarshal(after["kb_compta"], &entry)
	if entry["idx"] != float64(7) || entry["version"] != "1.9" || entry["is_repo"] != true || !reflect.DeepEqual(entry["required"], []any{"kb_pro"}) {
		t.Fatalf("entry %v", entry)
	}
}

func TestMaintenancePushedUsesActualRemote(t *testing.T) {
	root, remote, base := maintenanceFixture(t)
	maintenanceAttach(t, root, base)
	app := filepath.Join(root, "apps", "kb_compta")
	maintenanceTestGit(t, app, "config", "user.name", "Fixture")
	maintenanceTestGit(t, app, "config", "user.email", "fixture@example.test")
	maintenanceTestWrite(t, filepath.Join(app, "tracked"), "fix\n", 0644)
	maintenanceTestGit(t, app, "add", "tracked")
	maintenanceTestGit(t, app, "commit", "-m", "fix")
	head := maintenanceTestGit(t, app, "rev-parse", "HEAD")
	maintenanceTestGit(t, app, "update-ref", "refs/remotes/origin/support/test", head)
	before, _, _ := maintenanceManifest(filepath.Join(app, ".git"))
	if err := VerifyMaintenancePushed(context.Background(), root, "kb_compta", []string{head}); err == nil {
		t.Fatal("stale remote-tracking ref proved push")
	}
	after, _, _ := maintenanceManifest(filepath.Join(app, ".git"))
	if !reflect.DeepEqual(before, after) {
		t.Fatal("push refusal mutated Git")
	}
	maintenanceTestGit(t, app, "push", remote, "HEAD:refs/heads/support/test")
	if err := VerifyMaintenancePushed(context.Background(), root, "kb_compta", []string{head}); err != nil {
		t.Fatal(err)
	}
	refs, err := ProtectMaintenanceCommits(root, "kb_compta", "session-1", []string{base, head})
	if err != nil || len(refs) != 2 {
		t.Fatalf("protect %v %v", refs, err)
	}
	if count := len(strings.Split(maintenanceTestGit(t, app, "for-each-ref", "--format=%(refname)", "refs/kb-maintenance/"), "\n")); count != 2 {
		t.Fatal(count)
	}
	commits, err := MaintenanceCommits(root, "kb_compta", base)
	if err != nil || !reflect.DeepEqual(commits, []string{head}) {
		t.Fatalf("commits %v %v", commits, err)
	}
}

func TestMaintenanceReleaseAllRefsAndTagPeelReadOnly(t *testing.T) {
	root, remote, base := maintenanceFixture(t)
	maintenanceAttach(t, root, base)
	app := filepath.Join(root, "apps", "kb_compta")
	maintenanceTestGit(t, remote, "tag", "v1.0.0", base)
	before, _, _ := maintenanceManifest(filepath.Join(app, ".git"))
	verify := func(tag, c string, pins []string) error {
		return VerifyMaintenanceRelease(context.Background(), root, "kb_compta", "KB-Developpement/kb_compta", tag, c, pins)
	}
	if err := verify("v1.0.0", strings.Repeat("a", 40), []string{base}); err == nil {
		t.Fatal("wrong peel accepted")
	}
	if err := verify("v1.0.0", base, []string{base}); err != nil {
		t.Fatal(err)
	}
	after, _, _ := maintenanceManifest(filepath.Join(app, ".git"))
	if !reflect.DeepEqual(before, after) {
		t.Fatal("release verification mutated Git")
	}
	maintenanceTestGit(t, app, "update-ref", "refs/unexpected/private", base)
	before, _, _ = maintenanceManifest(filepath.Join(app, ".git"))
	if err := verify("v1.0.0", base, []string{base}); err == nil || !strings.Contains(err.Error(), "unexpected local ref") {
		t.Fatalf("unexpected ref: %v", err)
	}
	after, _, _ = maintenanceManifest(filepath.Join(app, ".git"))
	if !reflect.DeepEqual(before, after) {
		t.Fatal("ref refusal mutated Git")
	}
	maintenanceTestGit(t, app, "update-ref", "-d", "refs/unexpected/private")
	maintenanceTestGit(t, app, "config", "user.name", "Fixture")
	maintenanceTestGit(t, app, "config", "user.email", "fixture@example.test")
	maintenanceTestWrite(t, filepath.Join(app, "tracked"), "unreleased\n", 0644)
	maintenanceTestGit(t, app, "add", "tracked")
	maintenanceTestGit(t, app, "commit", "-m", "unreleased")
	tip := maintenanceTestGit(t, app, "rev-parse", "HEAD")
	maintenanceTestGit(t, app, "branch", "unshipped", tip)
	maintenanceTestGit(t, app, "reset", "--hard", base)
	if err := verify("v1.0.0", base, []string{base}); err == nil || !strings.Contains(err.Error(), "not contained") {
		t.Fatalf("unshipped branch: %v", err)
	}
}

func TestMaintenanceReleaseStateRefusals(t *testing.T) {
	for _, kind := range []string{"dirty", "untracked", "stash", "merge", "worktree"} {
		t.Run(kind, func(t *testing.T) {
			root, remote, base := maintenanceFixture(t)
			maintenanceAttach(t, root, base)
			app := filepath.Join(root, "apps", "kb_compta")
			maintenanceTestGit(t, remote, "tag", "v1.0.0", base)
			switch kind {
			case "dirty":
				maintenanceTestWrite(t, filepath.Join(app, "tracked"), "edit", 0644)
			case "untracked":
				maintenanceTestWrite(t, filepath.Join(app, "new"), "edit", 0644)
			case "stash":
				maintenanceTestWrite(t, filepath.Join(app, "tracked"), "edit", 0644)
				maintenanceTestGit(t, app, "stash", "push")
			case "merge":
				maintenanceTestWrite(t, filepath.Join(app, ".git", "MERGE_HEAD"), base, 0644)
			case "worktree":
				maintenanceTestGit(t, app, "worktree", "add", "--detach", filepath.Join(root, "other-worktree"), base)
			}
			before, _, _ := maintenanceManifest(app)
			if err := VerifyMaintenanceRelease(context.Background(), root, "kb_compta", "KB-Developpement/kb_compta", "v1.0.0", base, []string{base}); err == nil {
				t.Fatal("unsafe state accepted")
			}
			after, _, _ := maintenanceManifest(app)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("refusal mutated source")
			}
		})
	}
}

func TestMaintenanceRecoveryCompleteAndTamper(t *testing.T) {
	root, _, base := maintenanceFixture(t)
	maintenanceAttach(t, root, base)
	app := filepath.Join(root, "apps", "kb_compta")
	maintenanceTestWrite(t, filepath.Join(app, "__pycache__", "private.pyc"), "ignored", 0600)
	maintenanceTestGit(t, app, "hash-object", "-w", "--stdin")
	maintenanceTestWrite(t, filepath.Join(app, ".git", "unreachable-note"), "retained", 0600)
	copy, manifest, err := RecoveryCopyMaintenance(root, "kb_compta", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if err = VerifyMaintenanceRecovery(app, copy, manifest); err != nil {
		t.Fatal(err)
	}
	maintenanceTestWrite(t, filepath.Join(copy, "tracked"), "tampered", 0644)
	if err = VerifyMaintenanceRecovery(app, copy, manifest); err == nil {
		t.Fatal("tampered copy accepted")
	}
	prior := maintenanceAvailableSpace
	maintenanceAvailableSpace = func(string) (uint64, error) { return 0, nil }
	defer func() { maintenanceAvailableSpace = prior }()
	if _, _, err = RecoveryCopyMaintenance(root, "kb_compta", "session-2"); err == nil {
		t.Fatal("insufficient free space accepted")
	}
}

func maintenanceFakeTools(t *testing.T, root string) {
	t.Helper()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	maintenanceTestWrite(t, filepath.Join(bin, "bench"), `#!/bin/sh
printf '%s\n' "$*" >> "$KB_TEST_COMMANDS"
case "$*" in
  --version) echo 5.31.0;;
  *list-apps*) printf '{"%s":["kb_pro","kb_compta"]}\n' "$2";;
  *execute*) echo '["patch.b","patch.a","patch.b"]';;
  *backup*)
    while [ "$#" -gt 0 ]; do case "$1" in --backup-path-db|--backup-path-files|--backup-path-private-files|--backup-path-conf) shift; [ "$KB_TEST_EMPTY_BACKUP" = 1 ] || printf 'fresh' > "$1";; esac; shift; done;;
  *requirements*) [ "$KB_TEST_FAIL" != setup ];;
  *migrate*) [ "$KB_TEST_FAIL" != "$2" ];;
esac
`, 0755)
	maintenanceTestWrite(t, filepath.Join(root, "env", "bin", "python"), "#!/bin/sh\nexit 0\n", 0755)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("KB_TEST_COMMANDS", filepath.Join(root, "commands.log"))
}

func TestMaintenanceSitesBackupAndPatchLogs(t *testing.T) {
	root, _, _ := maintenanceFixture(t)
	maintenanceFakeTools(t, root)
	for _, s := range []string{"first.local", "second.local"} {
		maintenanceTestWrite(t, filepath.Join(root, "sites", s, "site_config.json"), "{}", 0600)
	}
	sites, err := MaintenanceSites(root)
	if err != nil || len(sites) != 2 {
		t.Fatalf("sites %v %v", sites, err)
	}
	if err = CheckMaintenanceBench(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	affected, err := InstalledMaintenanceSites(context.Background(), root, "kb_compta")
	if err != nil || !reflect.DeepEqual(affected, sites) {
		t.Fatalf("affected %v %v", affected, err)
	}
	backups, err := BackupMaintenanceSites(context.Background(), root, sites)
	if err != nil || len(backups) != 6 {
		t.Fatalf("backups %v %v", backups, err)
	}
	for _, b := range backups {
		fi, err := os.Stat(b.Path)
		if err != nil || fi.Size() == 0 || b.TakenAt == "" {
			t.Fatalf("backup %v", b)
		}
	}
	patches, err := PatchLogs(context.Background(), root, sites)
	if err != nil || len(patches) != 2 || !reflect.DeepEqual(patches[sites[0]], []string{"patch.a", "patch.b", "patch.b"}) {
		t.Fatalf("patches %v %v", patches, err)
	}
	commands, err := os.ReadFile(os.Getenv("KB_TEST_COMMANDS"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(commands), `"filters":{"skipped":0}`) {
		t.Fatal("Patch Log snapshot included skipped rows")
	}
	t.Setenv("KB_TEST_EMPTY_BACKUP", "1")
	if _, err = BackupMaintenanceSites(context.Background(), root, sites); err == nil {
		t.Fatal("printed backup success accepted without artifacts")
	}
}

func maintenanceReleaseArchive(t *testing.T, root, base string) *license.Download {
	t.Helper()
	p := filepath.Join(root, "release.tar.gz")
	cmd := exec.Command("git", "--git-dir="+filepath.Join(root, "remote.git"), "archive", "--format=tar.gz", "--prefix=release/", base)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = f
	err = cmd.Run()
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	sum, err := fileSHA256(p)
	if err != nil {
		t.Fatal(err)
	}
	return &license.Download{Path: p, SHA256: sum, Commit: base, Repository: "KB-Developpement/kb_compta", ReleaseTag: "v1.0.0"}
}

func TestMaintenanceReturnMigrationBoundary(t *testing.T) {
	for _, scenario := range []string{"setup", "second.local", "schema-changed", "ok"} {
		t.Run(scenario, func(t *testing.T) {
			root, _, base := maintenanceFixture(t)
			maintenanceAttach(t, root, base)
			maintenanceFakeTools(t, root)
			app := filepath.Join(root, "apps", "kb_compta")
			before, _, _ := maintenanceManifest(app)
			recovery, _, err := RecoveryCopyMaintenance(root, "kb_compta", "session-1")
			if err != nil {
				t.Fatal(err)
			}
			dl := maintenanceReleaseArchive(t, root, base)
			fail := scenario
			if scenario == "schema-changed" {
				fail = "setup"
			}
			t.Setenv("KB_TEST_FAIL", fail)
			events := []string{}
			record := func(step, site, outcome string) error {
				events = append(events, step+":"+site+":"+outcome)
				if step == "migrate" && outcome == "unknown" {
					raw, _ := os.ReadFile(filepath.Join(root, "commands.log"))
					if strings.Contains(string(raw), "--site "+site+" migrate") {
						t.Fatal("unknown recorded after migration command")
					}
				}
				return nil
			}
			a, _ := apps.ByName("kb_compta")
			err = ReturnMaintenanceArchive(context.Background(), root, a, dl, recovery, scenario == "schema-changed", []string{"first.local", "second.local"}, record)
			old, _ := MaintenanceReturnPaths(recovery)
			if scenario == "ok" {
				if err != nil {
					t.Fatal(err)
				}
				if !exists(old) || !exists(recovery) {
					t.Fatal("recovery sources lost")
				}
				if exists(filepath.Join(app, ".git")) {
					t.Fatal("release retains Git")
				}
				return
			}
			if err == nil {
				t.Fatal("failure expected")
			}
			if scenario == "setup" {
				after, _, _ := maintenanceManifest(app)
				if !reflect.DeepEqual(before, after) {
					t.Fatal("pre-migration rollback failed source proof")
				}
				if !strings.Contains(strings.Join(events, "\n"), "rollback::ok") {
					t.Fatal(events)
				}
			} else {
				if !exists(old) || !exists(recovery) || exists(filepath.Join(app, ".git")) {
					t.Fatal("post-boundary sources not retained")
				}
				if strings.Contains(strings.Join(events, "\n"), "rollback") {
					t.Fatal("unsafe automatic rollback")
				}
				if scenario == "second.local" {
					joined := strings.Join(events, "\n")
					if !strings.Contains(joined, "migrate:first.local:ok") || !strings.Contains(joined, "migrate:second.local:failed") {
						t.Fatal(joined)
					}
				}
			}
		})
	}
}

func TestMaintenanceRecoveryManifestSchema(t *testing.T) {
	root, _, base := maintenanceFixture(t)
	maintenanceAttach(t, root, base)
	recovery, manifest, err := RecoveryCopyMaintenance(root, "kb_compta", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(root, "apps", "kb_compta")
	raw, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var saved MaintenanceManifest
	if err = json.Unmarshal(raw, &saved); err != nil || saved.SchemaVersion != 1 || len(saved.Entries) == 0 {
		t.Fatalf("manifest %+v %v", saved, err)
	}
	for _, body := range []string{`{"schema_version":2,"entries":[]}`, `{"entries":[]}`, `{"schema_version":"1","entries":[]}`} {
		maintenanceTestWrite(t, manifest, body, 0600)
		if err = VerifyMaintenanceRecovery(app, recovery, manifest); err == nil {
			t.Fatal("invalid recovery schema accepted")
		}
	}
}

func TestMaintenanceArchiveAttributesRefuseBeforeAttach(t *testing.T) {
	root, remote, _ := maintenanceFixture(t)
	source := filepath.Join(root, "source")
	maintenanceTestWrite(t, filepath.Join(source, ".gitattributes"), "tracked export-ignore\n", 0644)
	maintenanceTestGit(t, source, "add", ".gitattributes")
	maintenanceTestGit(t, source, "commit", "-m", "archive attribute")
	base := maintenanceTestGit(t, source, "rev-parse", "HEAD")
	maintenanceTestGit(t, source, "push", remote, "HEAD:refs/heads/erpnext")
	before := maintenanceAppManifest(t, root)
	if _, err := PrepareMaintenanceGit(context.Background(), root, "kb_compta", "KB-Developpement/kb_compta", base, "support/test"); err == nil || !strings.Contains(err.Error(), "archive transformations") {
		t.Fatalf("attribute entry %v", err)
	}
	if !reflect.DeepEqual(before, maintenanceAppManifest(t, root)) {
		t.Fatal("attribute refusal changed files")
	}
}

func TestMaintenanceGitEnvironmentCannotRedirectApp(t *testing.T) {
	root, _, base := maintenanceFixture(t)
	maintenanceAttach(t, root, base)
	other := filepath.Join(root, "source", ".git")
	t.Setenv("GIT_DIR", other)
	t.Setenv("GIT_WORK_TREE", filepath.Join(root, "source"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(other, "foreign-index"))
	observation, err := ObserveMaintenanceGit(root, "kb_compta")
	if err != nil || observation.Branch != "support/test" {
		t.Fatalf("observation %+v %v", observation, err)
	}
	if exists(filepath.Join(other, "foreign-index")) {
		t.Fatal("foreign index modified")
	}
}

func TestMaintenancePushProofRefusesOtherProductRemote(t *testing.T) {
	root, _, base := maintenanceFixture(t)
	maintenanceAttach(t, root, base)
	app := filepath.Join(root, "apps", "kb_compta")
	maintenanceTestGit(t, app, "remote", "set-url", "--push", "origin", "https://github.com/KB-Developpement/kb_pro.git")
	before, _, _ := maintenanceManifest(filepath.Join(app, ".git"))
	err := VerifyMaintenancePushed(context.Background(), root, "kb_compta", []string{base})
	if err == nil || !strings.Contains(err.Error(), "frozen app repository") {
		t.Fatalf("wrong repository proof: %v", err)
	}
	after, _, _ := maintenanceManifest(filepath.Join(app, ".git"))
	if !reflect.DeepEqual(before, after) {
		t.Fatal("wrong repository refusal mutated Git")
	}
}

func TestMaintenanceProtectionNeverOverwritesAlteredPin(t *testing.T) {
	root, _, base := maintenanceFixture(t)
	maintenanceAttach(t, root, base)
	app := filepath.Join(root, "apps", "kb_compta")
	maintenanceTestGit(t, app, "config", "user.name", "Fixture")
	maintenanceTestGit(t, app, "config", "user.email", "fixture@example.test")
	maintenanceTestWrite(t, filepath.Join(app, "tracked"), "fix", 0644)
	maintenanceTestGit(t, app, "add", "tracked")
	maintenanceTestGit(t, app, "commit", "-m", "fix")
	head := maintenanceTestGit(t, app, "rev-parse", "HEAD")
	ref := "refs/kb-maintenance/session-1/" + base
	maintenanceTestGit(t, app, "update-ref", ref, head)
	if _, err := ProtectMaintenanceCommits(root, "kb_compta", "session-1", []string{base}); err == nil {
		t.Fatal("altered protected ref overwritten")
	}
	if got := maintenanceTestGit(t, app, "rev-parse", ref); got != head {
		t.Fatal("altered protected ref changed on refusal")
	}
	if _, err := ProtectMaintenanceCommits(root, "kb_compta", "session-1\ncreate refs/heads/evil", []string{base}); err == nil {
		t.Fatal("control characters accepted in protection namespace")
	}
}
