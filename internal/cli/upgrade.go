package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/KB-Developpement/kb_pro_cli/internal/apps"
	"github.com/KB-Developpement/kb_pro_cli/internal/bench"
	"github.com/KB-Developpement/kb_pro_cli/internal/config"
	"github.com/KB-Developpement/kb_pro_cli/internal/errlog"
	"github.com/KB-Developpement/kb_pro_cli/internal/ident"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
	"github.com/KB-Developpement/kb_pro_cli/internal/ui"
)

// upgradePin is one `--to <app>=<tag>`: deploy exactly that published release.
type upgradePin struct {
	App string
	Tag string
}

// parseUpgradePins parses repeated --to values. Each must be <app>=<tag> for a
// registry app, with a tag that is a valid ref, and no app may appear twice.
func parseUpgradePins(values []string) ([]upgradePin, error) {
	var pins []upgradePin
	seen := map[string]bool{}
	for _, v := range values {
		app, tag, ok := strings.Cut(v, "=")
		app, tag = strings.TrimSpace(app), strings.TrimSpace(tag)
		if !ok || app == "" || tag == "" {
			return nil, fmt.Errorf("--to %q: expected <app>=<tag>, for example --to kb_pro=v1.2.0", v)
		}
		if _, known := indexByName(apps.All)[app]; !known {
			return nil, fmt.Errorf("--to %q: %q is not a KB app that kb upgrade manages", v, app)
		}
		if !ident.ValidRef(tag) {
			return nil, fmt.Errorf("--to %q: %q is not a valid tag name", v, tag)
		}
		if seen[app] {
			return nil, fmt.Errorf("--to names %s more than once", app)
		}
		seen[app] = true
		pins = append(pins, upgradePin{App: app, Tag: tag})
	}
	return pins, nil
}

func newUpgradeCmd() *cobra.Command {
	var appsFlag string
	var toFlags []string
	var skipFrappeCheck bool

	cmd := &cobra.Command{
		Use:     "upgrade",
		Aliases: []string{"up"},
		Short:   "Update KB apps already present in the bench",
		Long: `Download the latest release on each app's line for selected KB apps and migrate all sites.

Each app is fetched from the license server using your active license and then
extracted over the existing app directory. "bench migrate" is run afterwards
to apply any schema changes. Apps are upgraded sequentially.

--to <app>=<tag> (repeatable) deploys exactly that published release instead of
the line's newest. The server must confirm the tag is a published release (not a
draft, prerelease, branch or bare commit). With --to and no --apps, only the
--to apps are upgraded. An app directory that holds a .git is refused.

Examples:
  kb upgrade                          # Interactive — pick from apps in bench
  kb upgrade --apps kb_pro,kb_compta  # Non-interactive upgrade
  kb upgrade --no-input --apps kb_pro # Scripted / CI usage
  kb upgrade --to kb_pro=v1.2.0       # Deploy exactly the published release v1.2.0
`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireInitializedForCLI(); err != nil {
				return err
			}
			if !bench.InBenchContainer() {
				return fmt.Errorf("kb must be run inside a Frappe bench container — use: ffm shell <bench-name>")
			}
			pins, err := parseUpgradePins(toFlags)
			if err != nil {
				return err
			}
			if err := requireKBFrappe(skipFrappeCheck); err != nil {
				return err
			}
			preselected := parseAppsFlag(appsFlag)
			return runUpgrade(cmd.Context(), preselected, pins)
		},
	}

	cmd.Flags().StringVar(&appsFlag, "apps", "", "Comma-separated list of app names (required with --no-input)")
	cmd.Flags().StringArrayVar(&toFlags, "to", nil, "Deploy a published release: <app>=<tag> (repeatable)")
	cmd.Flags().BoolVar(&skipFrappeCheck, "skip-frappe-check", false, "Run even if apps/frappe is still stock Frappe")
	return cmd
}

// runUpgrade updates the selected KB apps that are present in the bench.
func runUpgrade(ctx context.Context, preselected []string, pins []upgradePin) error {
	return runLocked(ctx, func() error { return doUpgrade(ctx, preselected, pins) })
}

func doUpgrade(ctx context.Context, preselected []string, pins []upgradePin) error {
	// Sampled before anything touches apps/ — see devServerAction.
	devWasRunning := bench.IsDevServerRunning()

	if err := syncLicenseFn(ctx); err != nil {
		return err
	}
	allowedSet := allowedSetFn()
	if allowedSet == nil {
		return fmt.Errorf("license required to upgrade apps — run: kb activate")
	}

	token, err := cachedTokenFn()
	if err != nil {
		return fmt.Errorf("could not read license token: %w", err)
	}
	serverURL := config.ResolveLicenseServerURL()

	inBench := bench.DetectAppsInBench()

	var notInBench, notLicensed []string
	var selectable []apps.App
	for _, app := range apps.All {
		switch {
		case !allowedSet[app.Name]:
			notLicensed = append(notLicensed, app.Name)
		case !inBench[app.Dir()]:
			notInBench = append(notInBench, app.Name)
		default:
			selectable = append(selectable, app)
		}
	}

	if !globalFlags.Quiet {
		if len(notLicensed) > 0 {
			fmt.Fprintln(os.Stderr, ui.Dim.Render("Not in your license: "+strings.Join(notLicensed, ", ")))
		}
		if len(notInBench) > 0 {
			fmt.Fprintln(os.Stderr, ui.Dim.Render("Not in bench (use install/add first): "+strings.Join(notInBench, ", ")))
		}
	}
	if len(selectable) == 0 {
		fmt.Fprintln(os.Stdout, ui.Dim.Render("No KB apps found in bench to upgrade."))
		return nil
	}

	selectableByName := indexByName(selectable)
	pinByApp := map[string]string{}
	for _, p := range pins {
		if _, ok := selectableByName[p.App]; !ok {
			return fmt.Errorf("--to %s=%s: %s is not available for upgrade (not licensed, not in bench, or unknown)", p.App, p.Tag, p.App)
		}
		pinByApp[p.App] = p.Tag
	}

	var selected []string
	switch {
	case preselected != nil:
		for _, name := range preselected {
			if _, ok := selectableByName[name]; !ok {
				return fmt.Errorf("app %q is not available for upgrade (not licensed, not in bench, or unknown)", name)
			}
		}
		selected = append(selected, preselected...)
	case len(pins) > 0:
		// --to alone: only the pinned apps, no picker.
	default:
		if globalFlags.NoInput {
			return fmt.Errorf("specify apps with --apps when using --no-input")
		}
		var err error
		selected, err = selectApps(selectable, "Select KB apps to upgrade")
		if err != nil || len(selected) == 0 {
			return nil
		}
	}
	for _, p := range pins {
		found := false
		for _, n := range selected {
			if n == p.App {
				found = true
			}
		}
		if !found {
			selected = append(selected, p.App)
		}
	}

	// Preflight the whole selection before the first download or swap.
	if err := guardApps(selected); err != nil {
		return err
	}

	fmt.Fprintln(os.Stdout)
	fmt.Fprintf(os.Stdout, "Upgrading %d app(s) sequentially…\n", len(selected))

	results := make([]installResult, 0, len(selected))
	stopped := false
	for _, name := range selected {
		if stopped {
			results = append(results, installResult{name, errNotAttempted})
			continue
		}
		app := selectableByName[name]
		req := license.DownloadRequest{App: app.LicenseID(), Repository: app.Repository(), Line: app.Line}
		if tag, pinned := pinByApp[name]; pinned {
			req.Ref, req.Release = tag, true
		}

		var res bench.ApplyResult
		var opErr error

		// Each upgrade has its own 15-minute budget covering download + extract + migrate.
		opCtx, opCancel := context.WithTimeout(ctx, 15*time.Minute)
		if spinErr := runWithSpinner(fmt.Sprintf("Upgrading %s…", ui.AppName.Render(name)), func() {
			var dl *license.Download
			dl, opErr = license.DownloadApp(opCtx, serverURL, token, req)
			if opErr != nil {
				return
			}
			defer os.Remove(dl.Path)
			res, opErr = bench.ApplyArchive(opCtx, bench.ApplyPlan{Command: bench.CmdUpgrade, App: app, Download: dl})
		}); spinErr != nil {
			opErr = spinErr
		}
		opCancel()

		if opErr != nil {
			errlog.Logf("upgrade %s: %v", name, opErr)
			fmt.Fprintf(os.Stdout, "%s %s: %v\n", ui.Failure.Render("✗"), ui.AppName.Render(name), opErr)
			if globalFlags.Verbose && res.Output != "" {
				fmt.Fprintln(os.Stdout, ui.Dim.Render(res.Output))
			}
			if errors.Is(opErr, bench.ErrJournalPending) {
				stopped = true
			}
		} else {
			fmt.Fprintf(os.Stdout, "%s %s\n", ui.Success.Render("✓"), ui.AppName.Render(name))
			printTransactionNotes(name, res)
			if globalFlags.Verbose && res.Output != "" {
				fmt.Fprintln(os.Stdout, ui.Dim.Render(res.Output))
			}
		}
		results = append(results, installResult{name, opErr})
	}

	if anySucceeded(results) {
		fmt.Fprintln(os.Stdout)
		maybeRestartDevServer(ctx, devWasRunning)
	}
	failures := printSummary(results)
	pause()
	return summaryError(failures, len(results))
}
