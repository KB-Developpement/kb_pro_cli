package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/huh/spinner"
	"github.com/spf13/cobra"

	"github.com/KB-Developpement/kb_pro_cli/internal/apps"
	"github.com/KB-Developpement/kb_pro_cli/internal/bench"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
	"github.com/KB-Developpement/kb_pro_cli/internal/ui"
)

func newUpgradeCmd() *cobra.Command {
	var appsFlag string

	cmd := &cobra.Command{
		Use:     "upgrade",
		Aliases: []string{"up"},
		Short:   "Update KB apps already present in the bench",
		Long: `Pull the latest changes for selected KB apps and migrate all sites.

Runs "bench update --apps <app> --reset" sequentially for each selected app.
All apps are attempted even if one fails; a summary is printed at the end.

Examples:
  kb upgrade                          # Interactive — pick from apps in bench
  kb upgrade --apps kb_pro,kb_compta  # Non-interactive upgrade
  kb upgrade --no-input --apps kb_pro # Scripted / CI usage
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
			return runUpgrade(cmd.Context(), preselected)
		},
	}

	cmd.Flags().StringVar(&appsFlag, "apps", "", "Comma-separated list of app names (required with --no-input)")
	return cmd
}

// runUpgrade updates the selected KB apps that are present in the bench.
func runUpgrade(ctx context.Context, preselected []string) error {
	if err := license.RunSyncCheck(ctx); err != nil {
		return err
	}
	allowedSet := license.AllowedSet()
	if allowedSet == nil {
		return fmt.Errorf("license required to upgrade apps — run: kb activate")
	}

	inBench := bench.DetectAppsInBench()

	var notInBench, notLicensed []string
	var selectable []apps.App
	for _, app := range apps.All {
		switch {
		case !allowedSet[app.Name]:
			notLicensed = append(notLicensed, app.Name)
		case !inBench[app.Name]:
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

	var selected []string
	if preselected != nil {
		selectableByName := indexByName(selectable)
		for _, name := range preselected {
			if _, ok := selectableByName[name]; !ok {
				return fmt.Errorf("app %q is not available for upgrade (not licensed, not in bench, or unknown)", name)
			}
		}
		selected = preselected
	} else {
		if globalFlags.NoInput {
			return fmt.Errorf("specify apps with --apps when using --no-input")
		}
		var err error
		selected, err = selectApps(selectable, "Select KB apps to upgrade")
		if err != nil || len(selected) == 0 {
			return nil
		}
	}

	fmt.Fprintln(os.Stdout)
	fmt.Fprintf(os.Stdout, "Upgrading %d app(s) sequentially…\n", len(selected))

	results := make([]installResult, 0, len(selected))
	for _, name := range selected {
		var opOut string
		var opErr error
		opCtx, opCancel := context.WithTimeout(ctx, 15*time.Minute)
		if spinErr := spinner.New().
			Title(fmt.Sprintf("Upgrading %s…", ui.AppName.Render(name))).
			Action(func() { opOut, opErr = bench.UpdateApp(opCtx, name) }).
			Run(); spinErr != nil {
			opErr = spinErr
		}
		opCancel()
		if opErr != nil {
			fmt.Fprintf(os.Stdout, "%s %s: %v\n", ui.Failure.Render("✗"), ui.AppName.Render(name), opErr)
			if globalFlags.Verbose && opOut != "" {
				fmt.Fprintln(os.Stdout, ui.Dim.Render(opOut))
			}
		} else {
			fmt.Fprintf(os.Stdout, "%s %s\n", ui.Success.Render("✓"), ui.AppName.Render(name))
			if globalFlags.Verbose && opOut != "" {
				fmt.Fprintln(os.Stdout, ui.Dim.Render(opOut))
			}
		}
		results = append(results, installResult{name, opErr})
	}

	printSummary(results)
	pause()
	return nil
}
