package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/huh/spinner"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/KB-Developpement/kb_pro_cli/internal/apps"
	"github.com/KB-Developpement/kb_pro_cli/internal/bench"
	"github.com/KB-Developpement/kb_pro_cli/internal/config"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
	"github.com/KB-Developpement/kb_pro_cli/internal/ui"
)

// newInstallCmd returns the "install" subcommand which downloads and installs
// selected KB apps on the active Frappe site.
func newInstallCmd() *cobra.Command {
	var appsFlag, branchFlag string

	cmd := &cobra.Command{
		Use:     "install",
		Aliases: []string{"i"},
		Short:   "Download and install KB apps on this site",
		Long: `Download and install selected KB-Developpement apps on the active Frappe site.

Examples:
  kb install                           # Interactive — pick apps from a menu
  kb install --apps kb_app,other_app   # Non-interactive install
  kb install --no-input --apps kb_app  # Scripted / CI usage
  kb install --branch develop --apps kb_pro   # Non-interactive branch
`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireInitializedForCLI(); err != nil {
				return err
			}
			if !bench.InBenchContainer() {
				return fmt.Errorf("kb must be run inside a Frappe bench container — use: ffm shell <bench-name>")
			}
			site, err := bench.DetectSiteName()
			if err != nil {
				return fmt.Errorf("could not detect site name: %w\nSet the active site with: bench use <site>", err)
			}
			if !globalFlags.Quiet {
				fmt.Fprintln(os.Stderr, ui.Dim.Render("Site: "+site))
			}
			preselected := parseAppsFlag(appsFlag)
			return runInstall(cmd.Context(), site, preselected, branchFlag)
		},
	}

	cmd.Flags().StringVar(&appsFlag, "apps", "", "Comma-separated list of app names (required with --no-input)")
	cmd.Flags().StringVar(&branchFlag, "branch", "", "Git branch passed to bench get-app for each selected app (optional)")
	return cmd
}

// newAddCmd returns the "add" subcommand which downloads KB apps into the bench
// without installing them on any site.
func newAddCmd() *cobra.Command {
	var appsFlag, branchFlag string

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Download KB apps into the bench without site installation",
		Long: `Download selected KB-Developpement apps into the bench apps folder.
Apps downloaded this way can later be installed via "kb manage".

Examples:
  kb add                           # Interactive — pick apps from a menu
  kb add --apps kb_app,other_app   # Non-interactive
  kb add --branch develop --apps kb_pro
`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireInitializedForCLI(); err != nil {
				return err
			}
			if !bench.InBenchContainer() {
				return fmt.Errorf("kb must be run inside a Frappe bench container — use: ffm shell <bench-name>")
			}
			preselected := parseAppsFlag(appsFlag)
			return runAddToBench(cmd.Context(), preselected, branchFlag)
		},
	}

	cmd.Flags().StringVar(&appsFlag, "apps", "", "Comma-separated list of app names (required with --no-input)")
	cmd.Flags().StringVar(&branchFlag, "branch", "", "Git branch passed to bench get-app for each selected app (optional)")
	return cmd
}

// parseAppsFlag splits a comma-separated app list into trimmed, non-empty names.
// Returns nil when flag is empty.
func parseAppsFlag(flag string) []string {
	if flag == "" {
		return nil
	}
	parts := strings.Split(flag, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if name := strings.TrimSpace(p); name != "" {
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// resolveToken returns a GitHub token from env/config, prompting the user if none is found.
func resolveToken() string {
	token := config.LoadToken()
	if token != "" {
		return token
	}

	if globalFlags.NoInput {
		if !globalFlags.Quiet {
			fmt.Fprintln(os.Stderr, ui.Dim.Render("No GitHub token found. Set KB_GITHUB_TOKEN or use --no-input with a configured token."))
		}
		return ""
	}

	var saveToken bool
	_ = huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("GitHub Personal Access Token").
				Description("Required for private repos. Set KB_GITHUB_TOKEN env var to skip this prompt. Tab to next field · Esc to skip").
				EchoMode(huh.EchoModePassword).
				Value(&token),
			huh.NewConfirm().
				Title("Save token to ~/.config/kb/config.json?").
				Value(&saveToken),
		),
	).WithKeyMap(formKeyMap()).Run()

	if token == "" {
		if !globalFlags.Quiet {
			fmt.Fprintln(os.Stderr, ui.Dim.Render("No token provided — private repos may fail."))
		}
		return ""
	}

	// Warn if the token doesn't look like a known GitHub PAT format.
	if !strings.HasPrefix(token, "ghp_") && !strings.HasPrefix(token, "github_pat_") {
		fmt.Fprintln(os.Stderr, ui.Dim.Render("Warning: token does not start with 'ghp_' or 'github_pat_' — verify it is a valid GitHub PAT."))
	}

	if saveToken {
		if err := config.SaveToken(token); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not save token: %v\n", err)
		}
	}
	return token
}

// promptOptionalGitBranch asks for a single git ref to pass to every bench get-app
// in this run. Esc / Ctrl+C returns a non-nil error (caller should abort quietly).
func promptOptionalGitBranch() (string, error) {
	var branch string
	if err := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Git branch (optional)").
				Description("Applied to all selected apps for bench get-app — leave empty for each repo’s default branch · Esc/Ctrl+C to cancel").
				Value(&branch),
		),
	).WithKeyMap(formKeyMap()).Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(branch), nil
}

// runInstall downloads and installs selected apps on the given site.
// preselected, when non-nil, bypasses the interactive selector (used by cobra command / --no-input).
// branchFlag is used as-is when non-empty; otherwise an interactive user is prompted once.
func runInstall(ctx context.Context, site string, preselected []string, branchFlag string) error {
	allowedSet := license.AllowedSet()
	if allowedSet == nil {
		return fmt.Errorf("license required to install apps — run: kb activate")
	}

	installed, detectErr := bench.DetectInstalledApps(site)
	if detectErr != nil && !globalFlags.Quiet {
		fmt.Fprintln(os.Stderr, ui.Dim.Render("Warning: could not detect installed apps — all apps will be shown"))
	}
	inBench := bench.DetectAppsInBench()

	var alreadyInstalled, alreadyDownloaded, notLicensed []string
	var selectable []apps.App
	for _, app := range apps.All {
		switch {
		case !allowedSet[app.Name]:
			notLicensed = append(notLicensed, app.Name)
		case installed[app.Name]:
			alreadyInstalled = append(alreadyInstalled, app.Name)
		case inBench[app.Name]:
			alreadyDownloaded = append(alreadyDownloaded, app.Name)
		default:
			selectable = append(selectable, app)
		}
	}

	if !globalFlags.Quiet {
		if len(notLicensed) > 0 {
			fmt.Fprintln(os.Stderr, ui.Dim.Render("Not in your license: "+strings.Join(notLicensed, ", ")))
		}
		if len(alreadyInstalled) > 0 {
			fmt.Fprintln(os.Stderr, ui.Dim.Render("Already installed: "+strings.Join(alreadyInstalled, ", ")))
		}
		if len(alreadyDownloaded) > 0 {
			fmt.Fprintln(os.Stderr, ui.Dim.Render("Already downloaded (use Manage to install): "+strings.Join(alreadyDownloaded, ", ")))
		}
	}
	if len(selectable) == 0 {
		fmt.Fprintln(os.Stdout, ui.Success.Render("All KB apps are already installed or downloaded."))
		return nil
	}

	var selected []string
	if preselected != nil {
		// Validate preselected names are installable.
		selectableByName := indexByName(selectable)
		for _, name := range preselected {
			if _, ok := selectableByName[name]; !ok {
				return fmt.Errorf("app %q is not available for installation (not licensed, already installed, or unknown)", name)
			}
		}
		selected = preselected
	} else {
		if globalFlags.NoInput {
			return fmt.Errorf("specify apps with --apps when using --no-input")
		}
		var err error
		selected, err = selectApps(selectable, "Select KB apps to install")
		if err != nil || len(selected) == 0 {
			return nil
		}
	}

	branch := strings.TrimSpace(branchFlag)
	if branch == "" && !globalFlags.NoInput {
		var err error
		branch, err = promptOptionalGitBranch()
		if err != nil {
			return nil
		}
	}

	token := resolveToken()
	appByName := indexByName(selectable)
	fmt.Fprintln(os.Stdout)

	// --- Phase 1: Download all apps in parallel (up to 3 concurrent) ---
	type dlResult struct {
		name string
		out  string
		err  error
	}
	dlResults := make([]dlResult, len(selected))
	var mu sync.Mutex

	if branch != "" {
		fmt.Fprintf(os.Stdout, "Downloading %d app(s) on branch %s…\n", len(selected), branch)
	} else {
		fmt.Fprintf(os.Stdout, "Downloading %d app(s)…\n", len(selected))
	}

	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(3)

	for i, name := range selected {
		app := appByName[name]
		g.Go(func() error {
			dlCtx, dlCancel := context.WithTimeout(gCtx, 10*time.Minute)
			out, err := bench.GetApp(dlCtx, app.URL, token, branch)
			dlCancel()

			mu.Lock()
			dlResults[i] = dlResult{name: name, out: out, err: err}
			if err != nil {
				fmt.Fprintf(os.Stdout, "  %s %s\n", ui.Failure.Render("✗"), ui.AppName.Render(name))
			} else {
				fmt.Fprintf(os.Stdout, "  %s %s\n", ui.Success.Render("↓"), ui.AppName.Render(name))
				if globalFlags.Verbose && out != "" {
					fmt.Fprintln(os.Stdout, ui.Dim.Render(out))
				}
			}
			mu.Unlock()
			return nil // Never abort sibling downloads on a single failure.
		})
	}
	_ = g.Wait()
	fmt.Fprintln(os.Stdout)

	// --- Phase 2: Install downloaded apps sequentially ---
	results := make([]installResult, 0, len(selected))
	for _, dr := range dlResults {
		if dr.err != nil {
			fmt.Fprintf(os.Stdout, "%s %s: download failed: %v\n", ui.Failure.Render("✗"), ui.AppName.Render(dr.name), dr.err)
			results = append(results, installResult{dr.name, dr.err})
			continue
		}
		var installOut string
		var installErr error
		opCtx, opCancel := context.WithTimeout(ctx, 10*time.Minute)
		if spinErr := spinner.New().
			Title(fmt.Sprintf("Installing %s on %s…", ui.AppName.Render(dr.name), site)).
			Action(func() { installOut, installErr = bench.InstallApp(opCtx, site, dr.name) }).
			Run(); spinErr != nil {
			installErr = spinErr
		}
		opCancel()
		if installErr != nil {
			fmt.Fprintf(os.Stdout, "%s %s: %v\n", ui.Failure.Render("✗"), ui.AppName.Render(dr.name), installErr)
		} else {
			fmt.Fprintf(os.Stdout, "%s %s\n", ui.Success.Render("✓"), ui.AppName.Render(dr.name))
			if globalFlags.Verbose && installOut != "" {
				fmt.Fprintln(os.Stdout, ui.Dim.Render(installOut))
			}
		}
		results = append(results, installResult{dr.name, installErr})
	}

	printSummary(results)
	pause()
	return nil
}

// runAddToBench downloads selected apps into the bench without installing them on any site.
// preselected, when non-nil, bypasses the interactive selector.
// branchFlag is used when non-empty; otherwise an interactive user is prompted once.
func runAddToBench(ctx context.Context, preselected []string, branchFlag string) error {
	allowedSet := license.AllowedSet()
	if allowedSet == nil {
		return fmt.Errorf("license required to download apps — run: kb activate")
	}

	inBench := bench.DetectAppsInBench()

	var alreadyPresent, notLicensed []string
	var selectable []apps.App
	for _, app := range apps.All {
		switch {
		case !allowedSet[app.Name]:
			notLicensed = append(notLicensed, app.Name)
		case inBench[app.Name]:
			alreadyPresent = append(alreadyPresent, app.Name)
		default:
			selectable = append(selectable, app)
		}
	}

	if !globalFlags.Quiet {
		if len(notLicensed) > 0 {
			fmt.Fprintln(os.Stderr, ui.Dim.Render("Not in your license: "+strings.Join(notLicensed, ", ")))
		}
		if len(alreadyPresent) > 0 {
			fmt.Fprintln(os.Stderr, ui.Dim.Render("Already in bench: "+strings.Join(alreadyPresent, ", ")))
		}
	}
	if len(selectable) == 0 {
		fmt.Fprintln(os.Stdout, ui.Success.Render("All KB apps are already present in the bench."))
		return nil
	}

	var selected []string
	if preselected != nil {
		selectableByName := indexByName(selectable)
		for _, name := range preselected {
			if _, ok := selectableByName[name]; !ok {
				return fmt.Errorf("app %q is not available for download (not licensed, already in bench, or unknown)", name)
			}
		}
		selected = preselected
	} else {
		if globalFlags.NoInput {
			return fmt.Errorf("specify apps with --apps when using --no-input")
		}
		var err error
		selected, err = selectApps(selectable, "Select KB apps to add to bench")
		if err != nil || len(selected) == 0 {
			return nil
		}
	}

	branch := strings.TrimSpace(branchFlag)
	if branch == "" && !globalFlags.NoInput {
		var err error
		branch, err = promptOptionalGitBranch()
		if err != nil {
			return nil
		}
	}

	token := resolveToken()
	appByName := indexByName(selectable)
	fmt.Fprintln(os.Stdout)

	// Download all apps in parallel (up to 3 concurrent).
	type dlResult struct {
		name string
		out  string
		err  error
	}
	dlResults := make([]dlResult, len(selected))
	var mu sync.Mutex

	if branch != "" {
		fmt.Fprintf(os.Stdout, "Downloading %d app(s) on branch %s…\n", len(selected), branch)
	} else {
		fmt.Fprintf(os.Stdout, "Downloading %d app(s)…\n", len(selected))
	}

	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(3)

	for i, name := range selected {
		app := appByName[name]
		g.Go(func() error {
			dlCtx, dlCancel := context.WithTimeout(gCtx, 10*time.Minute)
			out, err := bench.GetApp(dlCtx, app.URL, token, branch)
			dlCancel()

			mu.Lock()
			dlResults[i] = dlResult{name: name, out: out, err: err}
			if err != nil {
				fmt.Fprintf(os.Stdout, "  %s %s\n", ui.Failure.Render("✗"), ui.AppName.Render(name))
			} else {
				fmt.Fprintf(os.Stdout, "  %s %s\n", ui.Success.Render("↓"), ui.AppName.Render(name))
				if globalFlags.Verbose && out != "" {
					fmt.Fprintln(os.Stdout, ui.Dim.Render(out))
				}
			}
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()

	results := make([]installResult, len(dlResults))
	for i, dr := range dlResults {
		results[i] = installResult{dr.name, dr.err}
	}

	printSummary(results)
	pause()
	return nil
}

// selectApps shows a multi-select form and returns the chosen app names.
func selectApps(selectable []apps.App, title string) ([]string, error) {
	options := make([]huh.Option[string], len(selectable))
	for i, app := range selectable {
		options[i] = huh.NewOption(app.Name, app.Name)
	}

	var selected []string
	if err := huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title(title).
				Description("Space to toggle · Enter to confirm · Esc/Ctrl+C to cancel").
				Options(options...).
				Value(&selected),
		),
	).WithKeyMap(formKeyMap()).Run(); err != nil {
		return nil, err
	}

	if len(selected) == 0 && !globalFlags.Quiet {
		fmt.Fprintln(os.Stdout, ui.Dim.Render("No apps selected."))
	}
	return selected, nil
}

// indexByName builds a name → App map for quick lookup.
func indexByName(list []apps.App) map[string]apps.App {
	m := make(map[string]apps.App, len(list))
	for _, a := range list {
		m[a.Name] = a
	}
	return m
}

type installResult struct {
	name string
	err  error
}

// printSummary prints the success/failure counts and any failed app names.
func printSummary(results []installResult) {
	fmt.Fprintln(os.Stdout)
	successes, failures := 0, 0
	for _, r := range results {
		if r.err == nil {
			successes++
		} else {
			failures++
		}
	}
	if failures == 0 {
		fmt.Fprintln(os.Stdout, ui.Success.Render(fmt.Sprintf("Done — %d app(s) processed successfully.", successes)))
	} else {
		fmt.Fprintln(os.Stdout, ui.Bold.Render(fmt.Sprintf("%d succeeded, %d failed:", successes, failures)))
		for _, r := range results {
			if r.err != nil {
				fmt.Fprintf(os.Stdout, "  %s %s\n", ui.Failure.Render("✗"), r.name)
			}
		}
	}
}
