package bench

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/KB-Developpement/kb_pro_cli/internal/apps"
	"github.com/KB-Developpement/kb_pro_cli/internal/ident"
)

func maintenanceVerificationRepo(ctx context.Context, root, app string) (string, func(), error) {
	scratch, err := maintenanceScratch(root, "verify-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(scratch) }
	gitDir := filepath.Join(scratch, ".git")
	if err = os.Mkdir(gitDir, 0700); err != nil {
		cleanup()
		return "", nil, err
	}
	if _, err = maintenanceGit(ctx, gitDir, "", "init", "--bare"); err != nil {
		cleanup()
		return "", nil, err
	}
	alt := filepath.Join(gitDir, "objects", "info", "alternates")
	objects, err := filepath.Abs(filepath.Join(app, ".git", "objects"))
	if err != nil {
		cleanup()
		return "", nil, err
	}
	if strings.ContainsAny(objects, "\n\r") {
		cleanup()
		return "", nil, errors.New("Git object path contains a newline")
	}
	if err = os.WriteFile(alt, []byte(objects+"\n"), 0600); err != nil {
		cleanup()
		return "", nil, err
	}
	return gitDir, cleanup, nil
}

func maintenanceInProgress(app string) error {
	for _, marker := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "REBASE_HEAD", "rebase-merge", "rebase-apply", "sequencer", "BISECT_START", "index.lock", "shallow"} {
		_, err := os.Lstat(filepath.Join(app, ".git", marker))
		if err == nil {
			return fmt.Errorf("Git operation or incomplete history present (%s)", marker)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func maintenanceClean(root, directory string) (string, *GitObservation, error) {
	app, err := maintenanceAppPath(root, directory)
	if err != nil {
		return "", nil, err
	}
	o, err := ObserveMaintenanceGit(root, directory)
	if err != nil {
		return "", nil, err
	}
	if o.Dirty {
		return "", nil, fmt.Errorf("maintenance source has uncommitted changes: %s", strings.Join(o.Changes, ", "))
	}
	if o.Stashes != 0 {
		return "", nil, errors.New("maintenance repository has stashes")
	}
	if o.LinkedWorktrees != 0 {
		return "", nil, errors.New("maintenance repository has linked worktrees")
	}
	if err = maintenanceInProgress(app); err != nil {
		return "", nil, err
	}
	return app, o, nil
}

// VerifyMaintenancePushed fetches current remote refs into isolated scratch.
// Local remote-tracking refs are never accepted as proof of a push.
func VerifyMaintenancePushed(ctx context.Context, root, directory string, commits []string) error {
	app, _, err := maintenanceClean(root, directory)
	if err != nil {
		return err
	}
	url, err := maintenanceGit(ctx, filepath.Join(app, ".git"), app, "remote", "get-url", "--push", "origin")
	if err != nil {
		return err
	}
	if strings.ContainsAny(url, "\n\r") {
		return errors.New("multiple push URLs are unsupported")
	}
	identity, ok := apps.ByDirectory(directory)
	if !ok {
		return errors.New("maintenance app is absent from registry")
	}
	expectedRepository := "KB-Developpement/" + identity.Repository()
	if !maintenanceRemoteMatches(url, expectedRepository) && url != maintenanceRepositoryURL(expectedRepository) {
		return errors.New("origin push URL does not match the frozen app repository")
	}

	scratch, cleanup, err := maintenanceVerificationRepo(ctx, root, app)
	if err != nil {
		return err
	}
	defer cleanup()
	if _, err = maintenanceGit(ctx, scratch, "", "fetch", "--no-write-fetch-head", "--no-tags", url, "+refs/heads/*:refs/remotes/proof/*"); err != nil {
		return err
	}
	tips, err := maintenanceGit(ctx, scratch, "", "for-each-ref", "--format=%(objectname)", "refs/remotes/proof/")
	if err != nil {
		return err
	}
	for _, c := range commits {
		if !ident.CommitRE.MatchString(c) {
			return errors.New("invalid protected commit")
		}
		found := false
		for _, tip := range strings.Split(tips, "\n") {
			if tip == "" {
				continue
			}
			if _, err = maintenanceGit(ctx, scratch, "", "merge-base", "--is-ancestor", c, tip); err == nil {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("protected commit %s is not pushed to any current remote branch", c)
		}
	}
	return nil
}

// VerifyMaintenanceRelease refuses without changing the app's .git. Fetch and
// graph checks happen in isolated scratch; the app repository is never mutated.
func VerifyMaintenanceRelease(ctx context.Context, root, directory, repository, tag, commit string, protected []string) error {
	if err := maintenanceRepository(repository); err != nil {
		return err
	}
	if !ident.CommitRE.MatchString(commit) || !ident.ValidRef(tag) {
		return errors.New("invalid release identity")
	}
	app, o, err := maintenanceClean(root, directory)
	if err != nil {
		return err
	}
	gitDir := filepath.Join(app, ".git")
	remoteOutput, err := maintenanceGit(ctx, gitDir, app, "remote", "-v")
	urls := []string{}
	for _, line := range strings.Split(remoteOutput, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			urls = append(urls, fields[1])
		}
	}
	if err != nil {
		return err
	}
	if len(urls) == 0 {
		return errors.New("maintenance repository has no remote")
	}
	for _, u := range urls {
		if !maintenanceRemoteMatches(u, repository) && u != maintenanceRepositoryURL(repository) {
			return errors.New("maintenance repository remote does not match release repository")
		}
	}
	// Inspect every local ref before fetching. Only known Git and maintenance
	// namespaces are accepted, and every ref's peeled commit must be contained.
	raw, err := maintenanceGit(ctx, gitDir, app, "for-each-ref", "--format=%(refname)%09%(objectname)")
	if err != nil {
		return err
	}
	checks := map[string]string{"HEAD": o.Head}
	pins := map[string]bool{}
	for _, c := range protected {
		if !ident.CommitRE.MatchString(c) {
			return errors.New("invalid protected commit")
		}
		pins[c] = true
		checks["protected "+c] = c
	}
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			return errors.New("cannot parse local Git refs")
		}
		ref, obj := parts[0], parts[1]
		switch {
		case strings.HasPrefix(ref, "refs/heads/"), strings.HasPrefix(ref, "refs/tags/"), strings.HasPrefix(ref, "refs/remotes/origin/"):
		case strings.HasPrefix(ref, "refs/kb-maintenance/"):
			if !pins[obj] || filepath.Base(ref) != obj {
				return fmt.Errorf("unexpected maintenance ref %s", ref)
			}
		default:
			return fmt.Errorf("unexpected local ref %s", ref)
		}
		peeled, e := maintenanceGit(ctx, gitDir, app, "rev-parse", "--verify", ref+"^{commit}")
		if e != nil {
			return fmt.Errorf("local ref %s does not identify a commit", ref)
		}
		checks[ref] = peeled
	}
	scratch, cleanup, err := maintenanceVerificationRepo(ctx, root, app)
	if err != nil {
		return err
	}
	defer cleanup()
	if _, err = maintenanceGit(ctx, scratch, "", "fetch", "--no-write-fetch-head", "--no-tags", maintenanceRepositoryURL(repository), "refs/tags/"+tag+":refs/tags/kb-release-proof"); err != nil {
		return err
	}
	peeled, err := maintenanceGit(ctx, scratch, "", "rev-parse", "--verify", "refs/tags/kb-release-proof^{commit}")
	if err != nil || peeled != commit {
		return errors.New("release tag does not peel to strict download commit")
	}
	for name, c := range checks {
		if _, err = maintenanceGit(ctx, scratch, "", "merge-base", "--is-ancestor", c, commit); err != nil {
			return fmt.Errorf("%s (%s) is not contained in release %s", name, c, tag)
		}
	}
	// Recheck live state and refs after network I/O, before importing objects.
	after, err := ObserveMaintenanceGit(root, directory)
	if err != nil {
		return err
	}
	current, err := maintenanceGit(ctx, gitDir, app, "for-each-ref", "--format=%(refname)%09%(objectname)")
	if err != nil {
		return err
	}
	if after.Head != o.Head || after.Dirty || after.Stashes != o.Stashes || after.LinkedWorktrees != o.LinkedWorktrees || current != raw {
		return errors.New("maintenance repository changed during release verification")
	}
	if err = maintenanceInProgress(app); err != nil {
		return err
	}
	return nil
}

func maintenanceRemoteMatches(raw, repository string) bool {
	parsed, ok := parseRemote(raw)
	if !ok || parsed.extra || parsed.host != "github.com" || parsed.path != repository {
		return false
	}
	switch parsed.scheme {
	case "https":
		return parsed.user == ""
	case "ssh", "scp":
		return parsed.user == "git"
	}
	return false
}
