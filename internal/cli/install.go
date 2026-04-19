package cli

import (
	"context"
	"errors"
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
	"github.com/KB-Developpement/kb_pro_cli/internal/errlog"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
	"github.com/KB-Developpement/kb_pro_cli/internal/ui"
)

// newAddCmd returns the "add" subcommand: download KB apps into the bench
// (extract, pip install -e, build assets) without installing on any site.
// Equivalent to "bench get-app".
func newAddCmd() *cobra.Command {
	var appsFlag, versionFlag string

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Download KB apps into the bench (no site install)",
		Long: `Download selected KB apps into the bench apps folder.

Performs the full bench get-app equivalent: extracts the archive, installs
Python/Node dependencies, registers the editable package, and builds JS/CSS assets.
Apps can be installed on a site afterwards with: kb site-install

Examples:
  kb add                                # Interactive — pick apps from a menu
  kb add --apps kb_app,other_app        # Non-interactive
  kb add --apps kb_pro --version v1.2.0 # Pin a specific tag/branch/commit
`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireInitializedForCLI(); err != nil {
				return err
			}
			if !bench.InBenchContainer() {
				return fmt.Errorf("kb must be run inside a Frappe bench container — use: ffm shell <bench-name>")
			}
			return runAdd(cmd.Context(), parseAppsFlag(appsFlag), versionFlag)
		},
	}

	cmd.Flags().StringVar(&appsFlag, "apps", "", "Comma-separated list of app names (required with --no-input)")
	cmd.Flags().StringVar(&versionFlag, "version", "", "Git tag, branch, or commit — one app only (default: latest release)")
	return cmd
}

// newSiteInstallCmd returns the "site-install" subcommand: install already-downloaded
// KB apps on the active Frappe site. Equivalent to "bench install-app".
func newSiteInstallCmd() *cobra.Command {
	var appsFlag string

	cmd := &cobra.Command{
		Use:   "site-install",
		Short: "Install already-downloaded KB apps on this site",
		Long: `Install KB apps that are already present in the bench onto the active Frappe site.

Runs bench install-app for each selected app, which performs DocType sync,
fixture imports, hook execution, and all other Frappe site-level setup.
Use "kb add" first to download apps that are not yet in the bench.

Examples:
  kb site-install                       # Interactive — pick from downloaded apps
  kb site-install --apps kb_app         # Non-interactive
  kb site-install --no-input --apps kb_app  # CI usage
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
			return runSiteInstall(cmd.Context(), site, parseAppsFlag(appsFlag))
		},
	}

	cmd.Flags().StringVar(&appsFlag, "apps", "", "Comma-separated list of app names (required with --no-input)")
	return cmd
}

// newInstallCmd returns the "install" subcommand: download KB apps and install
// them on the active Frappe site in one step. Equivalent to
// "bench get-app <url> && bench install-app <app>".
func newInstallCmd() *cobra.Command {
	var appsFlag, versionFlag string

	cmd := &cobra.Command{
		Use:     "install",
		Aliases: []string{"i"},
		Short:   "Download and install KB apps on this site",
		Long: `Download selected KB apps and install them on the active Frappe site.

Combines "kb add" (download + assets) and "kb site-install" (bench install-app)
in a single command.

Examples:
  kb install                            # Interactive — pick apps from a menu
  kb install --apps kb_app,other_app    # Non-interactive install
  kb install --apps kb_pro --version v1.2.0  # Pin a tag/branch/commit (one app only)
  kb install --no-input --apps kb_app   # CI usage
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
			return runInstall(cmd.Context(), site, parseAppsFlag(appsFlag), versionFlag)
		},
	}

	cmd.Flags().StringVar(&appsFlag, "apps", "", "Comma-separated list of app names (required with --no-input)")
	cmd.Flags().StringVar(&versionFlag, "version", "", "Git tag, branch, or commit — one app only (default: latest release)")
	return cmd
}

// ── Core runners ─────────────────────────────────────────────────────────────

// runAdd downloads selected apps into the bench and performs all post-download
// steps: pip install -e (inside GetAppFromArchive), bench build, apps.json sync.
func runAdd(ctx context.Context, preselected []string, versionFromFlag string) error {
	token, serverURL, err := licenseTokenAndServer(ctx)
	if err != nil {
		return err
	}

	inBench := bench.DetectAppsInBench()

	var alreadyPresent, notLicensed []string
	var selectable []apps.App
	allowedSet := license.AllowedSet()
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

	selected, err := selectAppsInteractiveOrFlag(selectable, preselected, "Select KB apps to add to bench")
	if err != nil || len(selected) == 0 {
		return err
	}

	downloadRef, err := resolveDownloadRef(selected, versionFromFlag, preselected == nil)
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil
		}
		return err
	}

	fmt.Fprintln(os.Stdout)
	dlResults := downloadApps(ctx, selected, downloadRef, serverURL, token)

	results := postDownloadSteps(ctx, dlResults, true)
	printSummary(results)
	pause()
	return nil
}

// runSiteInstall installs already-downloaded apps onto the given site.
func runSiteInstall(ctx context.Context, site string, preselected []string) error {
	allowedSet := license.AllowedSet()
	if allowedSet == nil {
		return fmt.Errorf("license required to install apps — run: kb activate")
	}

	installed, detectErr := bench.DetectInstalledApps(site)
	if detectErr != nil && !globalFlags.Quiet {
		fmt.Fprintln(os.Stderr, ui.Dim.Render("Warning: could not detect installed apps — all downloaded apps will be shown"))
	}
	inBench := bench.DetectAppsInBench()

	var selectable []apps.App
	for _, app := range apps.All {
		if inBench[app.Name] && !installed[app.Name] && allowedSet[app.Name] {
			selectable = append(selectable, app)
		}
	}
	if len(selectable) == 0 {
		fmt.Fprintln(os.Stdout, ui.Dim.Render("No downloaded apps waiting to be installed on this site."))
		return nil
	}

	selected, err := selectAppsInteractiveOrFlag(selectable, preselected, "Select downloaded apps to install on site")
	if err != nil || len(selected) == 0 {
		return err
	}

	fmt.Fprintln(os.Stdout)
	results := siteInstallApps(ctx, site, selected)
	printSummary(results)
	pause()
	return nil
}

// runInstall downloads selected apps and installs them on the site in one step.
func runInstall(ctx context.Context, site string, preselected []string, versionFromFlag string) error {
	token, serverURL, err := licenseTokenAndServer(ctx)
	if err != nil {
		return err
	}

	installed, detectErr := bench.DetectInstalledApps(site)
	if detectErr != nil && !globalFlags.Quiet {
		fmt.Fprintln(os.Stderr, ui.Dim.Render("Warning: could not detect installed apps — all apps will be shown"))
	}
	inBench := bench.DetectAppsInBench()

	allowedSet := license.AllowedSet()
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
			fmt.Fprintln(os.Stderr, ui.Dim.Render("Already downloaded (use kb site-install): "+strings.Join(alreadyDownloaded, ", ")))
		}
	}
	if len(selectable) == 0 {
		fmt.Fprintln(os.Stdout, ui.Success.Render("All KB apps are already installed or downloaded."))
		return nil
	}

	selected, err := selectAppsInteractiveOrFlag(selectable, preselected, "Select KB apps to install")
	if err != nil || len(selected) == 0 {
		return err
	}

	downloadRef, err := resolveDownloadRef(selected, versionFromFlag, preselected == nil)
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil
		}
		return err
	}

	fmt.Fprintln(os.Stdout)

	// Phase 1: parallel download + extraction + pip install.
	dlResults := downloadApps(ctx, selected, downloadRef, serverURL, token)

	// Phase 2: sequential bench build + apps.json sync.
	addResults := postDownloadSteps(ctx, dlResults, false)

	// Phase 3: sequential bench install-app for apps that passed phase 2.
	fmt.Fprintln(os.Stdout)
	var installResults []installResult
	for _, r := range addResults {
		if r.err != nil {
			installResults = append(installResults, r)
			continue
		}
		opCtx, opCancel := context.WithTimeout(ctx, 10*time.Minute)
		var installOut string
		var installErr error
		if spinErr := spinner.New().
			Title(fmt.Sprintf("Installing %s on %s…", ui.AppName.Render(r.name), site)).
			Action(func() { installOut, installErr = bench.InstallApp(opCtx, site, r.name) }).
			Run(); spinErr != nil {
			installErr = spinErr
		}
		opCancel()
		if installErr != nil {
			errlog.Logf("install-app %s on %s: %v", r.name, site, installErr)
			fmt.Fprintf(os.Stdout, "%s %s: %v\n", ui.Failure.Render("✗"), ui.AppName.Render(r.name), installErr)
		} else {
			fmt.Fprintf(os.Stdout, "%s %s\n", ui.Success.Render("✓"), ui.AppName.Render(r.name))
			if globalFlags.Verbose && installOut != "" {
				fmt.Fprintln(os.Stdout, ui.Dim.Render(installOut))
			}
		}
		installResults = append(installResults, installResult{r.name, installErr})
	}

	printSummary(installResults)
	pause()
	return nil
}

// ── Shared download + post-download helpers ───────────────────────────────────

type dlResult struct {
	name string
	out  string
	err  error
}

// downloadApps fetches and extracts all selected apps in parallel (max 3 concurrent).
// It never returns an error — per-app failures are captured in the returned slice.
func downloadApps(ctx context.Context, selected []string, ref, serverURL, token string) []dlResult {
	results := make([]dlResult, len(selected))
	var mu sync.Mutex

	fmt.Fprintf(os.Stdout, "Downloading %d app(s) from license server…\n", len(selected))

	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(3)

	for i, name := range selected {
		g.Go(func() error {
			dlCtx, dlCancel := context.WithTimeout(gCtx, 10*time.Minute)
			defer dlCancel()

			tmpPath, dlErr := license.DownloadApp(dlCtx, serverURL, token, name, ref)
			var out string
			if dlErr == nil {
				out, dlErr = bench.GetAppFromArchive(dlCtx, tmpPath, name)
				os.Remove(tmpPath)
			}

			mu.Lock()
			results[i] = dlResult{name: name, out: out, err: dlErr}
			if dlErr != nil {
				errlog.Logf("download %s: %v", name, dlErr)
				fmt.Fprintf(os.Stdout, "  %s %s — %v\n", ui.Failure.Render("✗"), ui.AppName.Render(name), dlErr)
			} else {
				fmt.Fprintf(os.Stdout, "  %s %s\n", ui.Success.Render("↓"), ui.AppName.Render(name))
				if globalFlags.Verbose && out != "" {
					fmt.Fprintln(os.Stdout, ui.Dim.Render(out))
				}
			}
			mu.Unlock()
			return nil // never abort sibling downloads on a single failure
		})
	}
	_ = g.Wait()
	fmt.Fprintln(os.Stdout)
	return results
}

// postDownloadSteps runs bench build and updates apps.json sequentially for each
// successfully downloaded app. Download failures are passed through silently —
// they were already printed by downloadApps. When printSuccess is true a ✓ line
// is printed for each app that passes (used by runAdd as its final output);
// when false only failures are printed (runInstall prints its own final ✓/✗).
func postDownloadSteps(ctx context.Context, dlResults []dlResult, printSuccess bool) []installResult {
	var results []installResult
	for _, dr := range dlResults {
		if dr.err != nil {
			// Already printed by downloadApps — just propagate the result.
			results = append(results, installResult{dr.name, dr.err})
			continue
		}

		opCtx, opCancel := context.WithTimeout(ctx, 10*time.Minute)
		var buildOut string
		var buildErr error
		if spinErr := spinner.New().
			Title(fmt.Sprintf("Building assets for %s…", ui.AppName.Render(dr.name))).
			Action(func() { buildOut, buildErr = bench.BuildApp(opCtx, dr.name) }).
			Run(); spinErr != nil {
			buildErr = spinErr
		}
		opCancel()

		if buildErr != nil {
			errlog.Logf("build %s: %v", dr.name, buildErr)
			fmt.Fprintf(os.Stdout, "%s %s: %v\n", ui.Failure.Render("✗"), ui.AppName.Render(dr.name), buildErr)
			results = append(results, installResult{dr.name, buildErr})
			continue
		}
		if globalFlags.Verbose && buildOut != "" {
			fmt.Fprintln(os.Stdout, ui.Dim.Render(buildOut))
		}

		if syncErr := bench.SyncAppState(dr.name); syncErr != nil && !globalFlags.Quiet {
			fmt.Fprintf(os.Stderr, "  warning: could not update apps.json for %s: %v\n", dr.name, syncErr)
		}

		if printSuccess {
			fmt.Fprintf(os.Stdout, "%s %s\n", ui.Success.Render("✓"), ui.AppName.Render(dr.name))
		}
		results = append(results, installResult{dr.name, nil})
	}
	return results
}

// siteInstallApps runs bench install-app sequentially for each app name.
func siteInstallApps(ctx context.Context, site string, names []string) []installResult {
	results := make([]installResult, 0, len(names))
	for _, name := range names {
		opCtx, opCancel := context.WithTimeout(ctx, 10*time.Minute)
		var opOut string
		var opErr error
		if spinErr := spinner.New().
			Title(fmt.Sprintf("Installing %s on %s…", ui.AppName.Render(name), site)).
			Action(func() { opOut, opErr = bench.InstallApp(opCtx, site, name) }).
			Run(); spinErr != nil {
			opErr = spinErr
		}
		opCancel()
		if opErr != nil {
			errlog.Logf("site-install %s on %s: %v", name, site, opErr)
			fmt.Fprintf(os.Stdout, "%s %s: %v\n", ui.Failure.Render("✗"), ui.AppName.Render(name), opErr)
		} else {
			fmt.Fprintf(os.Stdout, "%s %s\n", ui.Success.Render("✓"), ui.AppName.Render(name))
			if globalFlags.Verbose && opOut != "" {
				fmt.Fprintln(os.Stdout, ui.Dim.Render(opOut))
			}
		}
		results = append(results, installResult{name, opErr})
	}
	return results
}

// ── Selection + misc helpers ──────────────────────────────────────────────────

// licenseTokenAndServer runs a license sync check and returns the cached token and server URL.
func licenseTokenAndServer(ctx context.Context) (token, serverURL string, err error) {
	if err = license.RunSyncCheck(ctx); err != nil {
		return
	}
	if license.AllowedSet() == nil {
		err = fmt.Errorf("license required to download apps — run: kb activate")
		return
	}
	token, err = license.GetCachedToken()
	if err != nil {
		err = fmt.Errorf("could not read license token: %w", err)
		return
	}
	serverURL = config.ResolveLicenseServerURL()
	return
}

// selectAppsInteractiveOrFlag validates preselected against the selectable list,
// or shows an interactive multi-select form when preselected is nil.
// Returns (nil, nil) when the user cancels the interactive form (Esc / Ctrl+C).
func selectAppsInteractiveOrFlag(selectable []apps.App, preselected []string, title string) ([]string, error) {
	if preselected != nil {
		byName := indexByName(selectable)
		for _, name := range preselected {
			if _, ok := byName[name]; !ok {
				return nil, fmt.Errorf("app %q is not available (not licensed, already in bench/installed, or unknown)", name)
			}
		}
		return preselected, nil
	}
	if globalFlags.NoInput {
		return nil, fmt.Errorf("specify apps with --apps when using --no-input")
	}
	selected, err := selectApps(selectable, title)
	if errors.Is(err, huh.ErrUserAborted) {
		return nil, nil // Esc / Ctrl+C — treat as no selection, not an error
	}
	return selected, err
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

// resolveDownloadRef returns the Git ref to pass to the license server (empty = latest).
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

// promptOptionalDownloadRef asks for an optional tag/branch/commit for a single app.
func promptOptionalDownloadRef(appName, defaultRef string) (string, error) {
	ref := defaultRef
	err := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Version or tag (optional)").
				Description(fmt.Sprintf("Git ref for %s — leave blank for latest release", appName)).
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

// indexByName builds a name → App lookup map.
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

// printSummary prints success/failure counts and any failed app names.
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
