package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/huh/spinner"
	"github.com/spf13/cobra"

	"github.com/KB-Developpement/kb_pro_cli/internal/apps"
	"github.com/KB-Developpement/kb_pro_cli/internal/bench"
	"github.com/KB-Developpement/kb_pro_cli/internal/errlog"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
	"github.com/KB-Developpement/kb_pro_cli/internal/ui"
)

const (
	manageInstall   = "install"
	manageUninstall = "uninstall"
	manageRemove    = "remove"
)

func newManageCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:     "manage",
		Aliases: []string{"m"},
		Short:   "Manage KB Frappe apps (install downloaded / uninstall / remove)",
		Long: `Interactively manage KB apps in the bench.

  Install    — bench --site <site> install-app <app>  (for already-downloaded apps)
  Uninstall  — bench --site <site> uninstall-app <app> -y  (removes from site, keeps source)
  Remove     — uninstall if needed, then bench remove-app <app>  (deletes source)`,
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
			return runManage(cmd.Context(), site, force)
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Pass --force to bench uninstall-app")
	return cmd
}

// runManage shows a looping submenu for managing KB apps.
// Esc / Ctrl+C exits the loop and returns nil.
func runManage(ctx context.Context, site string, force bool) error {
	for {
		clearScreen()

		installed, err := bench.DetectInstalledApps(site)
		if err != nil {
			return fmt.Errorf("could not detect installed apps: %w", err)
		}
		inBench := bench.DetectAppsInBench()

		var action string
		if err := huh.NewForm(
			huh.NewGroup(
				huh.NewSelect[string]().
					Title("Manage KB apps").
					Description("↑/↓ to navigate · Enter to confirm · Esc/Ctrl+C to go back").
					Options(
						huh.NewOption("Install downloaded apps  — install on this site", manageInstall),
						huh.NewOption("Uninstall from site      — keep source in bench", manageUninstall),
						huh.NewOption("Remove from bench        — uninstall + delete source", manageRemove),
					).
					Value(&action),
			),
		).WithKeyMap(formKeyMap()).Run(); err != nil {
			return nil // Esc / Ctrl+C — return to caller
		}

		var actionErr error
		switch action {
		case manageInstall:
			actionErr = runManageInstall(ctx, site, installed, inBench)
		case manageUninstall:
			actionErr = runManageUninstall(ctx, site, installed, force)
		case manageRemove:
			actionErr = runManageRemove(ctx, site, installed, inBench, force)
		}
		if actionErr != nil {
			errlog.Log(actionErr)
			fmt.Fprintf(os.Stderr, "\n%s %v\n", ui.Failure.Render("Error:"), actionErr)
		}
		pause()
	}
}

// runManageInstall installs already-downloaded apps onto the site.
func runManageInstall(ctx context.Context, site string, installed, inBench map[string]bool) error {
	allowedSet := license.AllowedSet()
	if allowedSet == nil {
		return fmt.Errorf("license required to install apps — run: kb activate")
	}

	var selectable []apps.App
	for _, app := range apps.All {
		if inBench[app.Name] && !installed[app.Name] && allowedSet[app.Name] {
			selectable = append(selectable, app)
		}
	}
	if len(selectable) == 0 {
		fmt.Fprintln(os.Stdout, ui.Dim.Render("No downloaded apps waiting to be installed on site."))
		return nil
	}

	selected, err := selectApps(selectable, "Select downloaded apps to install on site")
	if err != nil || len(selected) == 0 {
		return nil
	}

	fmt.Fprintln(os.Stdout)
	results := make([]installResult, 0, len(selected))
	for _, name := range selected {
		var opOut string
		var opErr error
		opCtx, opCancel := context.WithTimeout(ctx, 10*time.Minute)
		if spinErr := spinner.New().
			Title(fmt.Sprintf("Installing %s on %s…", ui.AppName.Render(name), site)).
			Action(func() { opOut, opErr = bench.InstallApp(opCtx, site, name) }).
			Run(); spinErr != nil {
			opErr = spinErr
		}
		opCancel()
		if opErr != nil {
			errlog.Logf("manage install %s: %v", name, opErr)
			fmt.Fprintf(os.Stdout, "%s %s: %v\n", ui.Failure.Render("✗"), ui.AppName.Render(name), opErr)
		} else {
			fmt.Fprintf(os.Stdout, "%s %s\n", ui.Success.Render("✓"), ui.AppName.Render(name))
			if globalFlags.Verbose && opOut != "" {
				fmt.Fprintln(os.Stdout, ui.Dim.Render(opOut))
			}
		}
		results = append(results, installResult{name, opErr})
	}
	printSummary(results)
	return nil
}

// runManageUninstall removes selected apps from the site, keeping their source in the bench.
func runManageUninstall(ctx context.Context, site string, installed map[string]bool, force bool) error {
	var selectable []apps.App
	for _, app := range apps.All {
		if installed[app.Name] {
			selectable = append(selectable, app)
		}
	}
	if len(selectable) == 0 {
		fmt.Fprintln(os.Stdout, ui.Dim.Render("No KB apps are currently installed on site."))
		return nil
	}

	selected, err := selectApps(selectable, "Select apps to uninstall from site")
	if err != nil || len(selected) == 0 {
		return nil
	}

	var confirmed bool
	if err := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title(fmt.Sprintf("Uninstall from site: %s", strings.Join(selected, ", "))).
				Description("This cannot be undone. Continue? (←/→ or Y/N · Enter to confirm · Esc/Ctrl+C to cancel)").
				Value(&confirmed),
		),
	).WithKeyMap(formKeyMap()).Run(); err != nil || !confirmed {
		fmt.Fprintln(os.Stdout, ui.Dim.Render("Cancelled."))
		return nil
	}

	fmt.Fprintln(os.Stdout)
	results := make([]installResult, 0, len(selected))
	for _, name := range selected {
		var opOut string
		var opErr error
		opCtx, opCancel := context.WithTimeout(ctx, 10*time.Minute)
		if spinErr := spinner.New().
			Title(fmt.Sprintf("Uninstalling %s from %s…", ui.AppName.Render(name), site)).
			Action(func() { opOut, opErr = bench.UninstallApp(opCtx, site, name, force) }).
			Run(); spinErr != nil {
			opErr = spinErr
		}
		opCancel()
		if opErr != nil {
			errlog.Logf("manage uninstall %s: %v", name, opErr)
			fmt.Fprintf(os.Stdout, "%s %s: %v\n", ui.Failure.Render("✗"), ui.AppName.Render(name), opErr)
		} else {
			fmt.Fprintf(os.Stdout, "%s %s\n", ui.Success.Render("✓"), ui.AppName.Render(name))
			if globalFlags.Verbose && opOut != "" {
				fmt.Fprintln(os.Stdout, ui.Dim.Render(opOut))
			}
		}
		results = append(results, installResult{name, opErr})
	}
	printSummary(results)
	return nil
}

// runManageRemove removes selected apps from the bench entirely.
// Apps installed on the site are uninstalled first.
func runManageRemove(ctx context.Context, site string, installed, inBench map[string]bool, force bool) error {
	var selectable []apps.App
	for _, app := range apps.All {
		if inBench[app.Name] {
			selectable = append(selectable, app)
		}
	}
	if len(selectable) == 0 {
		fmt.Fprintln(os.Stdout, ui.Dim.Render("No KB apps found in bench."))
		return nil
	}

	// Annotate apps that are also installed so the user knows uninstall will happen.
	options := make([]huh.Option[string], len(selectable))
	for i, app := range selectable {
		label := app.Name
		if installed[app.Name] {
			label += "  (will also uninstall from site)"
		}
		options[i] = huh.NewOption(label, app.Name)
	}

	var selected []string
	if err := huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("Select apps to remove from bench").
				Description("Space to toggle · Enter to confirm · Esc/Ctrl+C to cancel").
				Options(options...).
				Value(&selected),
		),
	).WithKeyMap(formKeyMap()).Run(); err != nil {
		return nil
	}
	if len(selected) == 0 {
		fmt.Fprintln(os.Stdout, ui.Dim.Render("No apps selected."))
		return nil
	}

	var confirmed bool
	if err := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title(fmt.Sprintf("Remove from bench: %s", strings.Join(selected, ", "))).
				Description("This cannot be undone. Continue? (←/→ or Y/N · Enter to confirm · Esc/Ctrl+C to cancel)").
				Value(&confirmed),
		),
	).WithKeyMap(formKeyMap()).Run(); err != nil || !confirmed {
		fmt.Fprintln(os.Stdout, ui.Dim.Render("Cancelled."))
		return nil
	}

	fmt.Fprintln(os.Stdout)
	results := make([]installResult, 0, len(selected))
	for _, name := range selected {
		var opErr error

		if installed[name] {
			var opOut string
			opCtx, opCancel := context.WithTimeout(ctx, 10*time.Minute)
			if spinErr := spinner.New().
				Title(fmt.Sprintf("Uninstalling %s from %s…", ui.AppName.Render(name), site)).
				Action(func() { opOut, opErr = bench.UninstallApp(opCtx, site, name, force) }).
				Run(); spinErr != nil {
				opErr = spinErr
			}
			opCancel()
			if opErr == nil && globalFlags.Verbose && opOut != "" {
				fmt.Fprintln(os.Stdout, ui.Dim.Render(opOut))
			}
		}
		if opErr == nil {
			var opOut string
			opCtx, opCancel := context.WithTimeout(ctx, 10*time.Minute)
			if spinErr := spinner.New().
				Title(fmt.Sprintf("Removing %s from bench…", ui.AppName.Render(name))).
				Action(func() { opOut, opErr = bench.RemoveApp(opCtx, name) }).
				Run(); spinErr != nil {
				opErr = spinErr
			}
			opCancel()
			if opErr == nil && globalFlags.Verbose && opOut != "" {
				fmt.Fprintln(os.Stdout, ui.Dim.Render(opOut))
			}
		}
		if opErr != nil {
			errlog.Logf("manage remove %s: %v", name, opErr)
			fmt.Fprintf(os.Stdout, "%s %s: %v\n", ui.Failure.Render("✗"), ui.AppName.Render(name), opErr)
		} else {
			fmt.Fprintf(os.Stdout, "%s %s\n", ui.Success.Render("✓"), ui.AppName.Render(name))
		}
		results = append(results, installResult{name, opErr})
	}
	printSummary(results)
	return nil
}
