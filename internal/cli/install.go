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
	var appsFlag, versionFlag string

	cmd := &cobra.Command{
		Use:     "install",
		Aliases: []string{"i"},
		Short:   "Download and install KB apps on this site",
		Long: `Download and install selected KB-Developpement apps on the active Frappe site.

Apps are fetched from the license server using your active license — no GitHub
token is required on the client.

Examples:
  kb install                           # Interactive — pick apps from a menu
  kb install --apps kb_app,other_app   # Non-interactive install
  kb install --apps kb_pro --version v1.2.0   # Pin a tag/branch/commit (one app only)
  kb install --no-input --apps kb_app  # Scripted / CI usage
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
			return runInstall(cmd.Context(), site, preselected, versionFlag)
		},
	}

	cmd.Flags().StringVar(&appsFlag, "apps", "", "Comma-separated list of app names (required with --no-input)")
	cmd.Flags().StringVar(&versionFlag, "version", "", "Git tag, branch, or commit for the download when exactly one app is selected (default: latest release)")
	return cmd
}

// newAddCmd returns the "add" subcommand which downloads KB apps into the bench
// without installing them on any site.
func newAddCmd() *cobra.Command {
	var appsFlag, versionFlag string

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Download KB apps into the bench without site installation",
		Long: `Download selected KB-Developpement apps into the bench apps folder.
Apps downloaded this way can later be installed via "kb manage".

Apps are fetched from the license server using your active license — no GitHub
token is required on the client.

Examples:
  kb add                           # Interactive — pick apps from a menu
  kb add --apps kb_app,other_app   # Non-interactive
  kb add --apps kb_pro --version v1.2.0   # Pin a tag/branch/commit (one app only)
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
			return runAddToBench(cmd.Context(), preselected, versionFlag)
		},
	}

	cmd.Flags().StringVar(&appsFlag, "apps", "", "Comma-separated list of app names (required with --no-input)")
	cmd.Flags().StringVar(&versionFlag, "version", "", "Git tag, branch, or commit for the download when exactly one app is selected (default: latest release)")
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

// runInstall downloads and installs selected apps on the given site.
// preselected, when non-nil, bypasses the interactive selector.
// Download ref: empty means latest on the license server. For exactly one app, the ref comes from
// --version (non-interactive / --apps) or from the optional version form (interactive); for multiple apps, ref is always empty.
func runInstall(ctx context.Context, site string, preselected []string, versionFromFlag string) error {
	if err := license.RunSyncCheck(ctx); err != nil {
		return err
	}
	allowedSet := license.AllowedSet()
	if allowedSet == nil {
		return fmt.Errorf("license required to install apps — run: kb activate")
	}

	token, err := license.GetCachedToken()
	if err != nil {
		return fmt.Errorf("could not read license token: %w", err)
	}
	serverURL := config.ResolveLicenseServerURL()

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

	downloadRef, err := resolveDownloadRef(selected, versionFromFlag, preselected == nil)
	if err != nil {
		return nil
	}

	fmt.Fprintln(os.Stdout)

	// --- Phase 1: Download all apps in parallel (up to 3 concurrent) ---
	type dlResult struct {
		name string
		out  string
		err  error
	}
	dlResults := make([]dlResult, len(selected))
	var mu sync.Mutex

	fmt.Fprintf(os.Stdout, "Downloading %d app(s) from license server…\n", len(selected))

	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(3)

	for i, name := range selected {
		g.Go(func() error {
			dlCtx, dlCancel := context.WithTimeout(gCtx, 10*time.Minute)
			defer dlCancel()

			tmpPath, dlErr := license.DownloadApp(dlCtx, serverURL, token, name, downloadRef)
			var out string
			if dlErr == nil {
				out, dlErr = bench.GetAppFromArchive(dlCtx, tmpPath, name)
				os.Remove(tmpPath)
			}

			mu.Lock()
			dlResults[i] = dlResult{name: name, out: out, err: dlErr}
			if dlErr != nil {
				fmt.Fprintf(os.Stdout, "  %s %s — %v\n", ui.Failure.Render("✗"), ui.AppName.Render(name), dlErr)
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
			fmt.Fprintf(os.Stdout, "%s %s: %v\n", ui.Failure.Render("✗"), ui.AppName.Render(dr.name), dr.err)
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
// versionFromFlag and interactive version prompt behave like runInstall.
func runAddToBench(ctx context.Context, preselected []string, versionFromFlag string) error {
	if err := license.RunSyncCheck(ctx); err != nil {
		return err
	}
	allowedSet := license.AllowedSet()
	if allowedSet == nil {
		return fmt.Errorf("license required to download apps — run: kb activate")
	}

	token, err := license.GetCachedToken()
	if err != nil {
		return fmt.Errorf("could not read license token: %w", err)
	}
	serverURL := config.ResolveLicenseServerURL()

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

	downloadRef, err := resolveDownloadRef(selected, versionFromFlag, preselected == nil)
	if err != nil {
		return nil
	}

	fmt.Fprintln(os.Stdout)

	// Download all apps in parallel (up to 3 concurrent).
	type dlResult struct {
		name string
		out  string
		err  error
	}
	dlResults := make([]dlResult, len(selected))
	var mu sync.Mutex

	fmt.Fprintf(os.Stdout, "Downloading %d app(s) from license server…\n", len(selected))

	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(3)

	for i, name := range selected {
		g.Go(func() error {
			dlCtx, dlCancel := context.WithTimeout(gCtx, 10*time.Minute)
			defer dlCancel()

			tmpPath, dlErr := license.DownloadApp(dlCtx, serverURL, token, name, downloadRef)
			var out string
			if dlErr == nil {
				out, dlErr = bench.GetAppFromArchive(dlCtx, tmpPath, name)
				os.Remove(tmpPath)
			}

			mu.Lock()
			dlResults[i] = dlResult{name: name, out: out, err: dlErr}
			if dlErr != nil {
				fmt.Fprintf(os.Stdout, "  %s %s — %v\n", ui.Failure.Render("✗"), ui.AppName.Render(name), dlErr)
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

// resolveDownloadRef returns the Git ref to pass as ?v= to the license server (empty = latest).
// When more than one app is selected, ref is always empty and versionFromFlag is ignored (with a dim warning if set).
// When exactly one app: if usedInteractiveMenu, the user is prompted (defaultRef pre-fills from --version if set); otherwise versionFromFlag is used.
func resolveDownloadRef(selected []string, versionFromFlag string, usedInteractiveMenu bool) (string, error) {
	if len(selected) != 1 {
		if len(selected) > 1 && strings.TrimSpace(versionFromFlag) != "" && !globalFlags.Quiet {
			fmt.Fprintln(os.Stderr, ui.Dim.Render("Ignoring --version: more than one app selected; using latest release for each."))
		}
		return "", nil
	}
	if usedInteractiveMenu {
		return promptOptionalDownloadRef(selected[0], strings.TrimSpace(versionFromFlag))
	}
	return strings.TrimSpace(versionFromFlag), nil
}

// promptOptionalDownloadRef asks for an optional tag/branch/commit when a single app was chosen interactively.
// defaultRef is shown in the field first (e.g. from kb install --version … without --apps).
func promptOptionalDownloadRef(appName, defaultRef string) (string, error) {
	ref := defaultRef
	err := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Version or tag (optional)").
				Description(fmt.Sprintf("Git ref for %s — leave blank for latest release (license server ?v=)", appName)).
				Value(&ref),
		),
	).WithKeyMap(formKeyMap()).Run()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(ref), nil
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
				fmt.Fprintf(os.Stdout, "  %s %s — %v\n", ui.Failure.Render("✗"), r.name, r.err)
			}
		}
	}
}
