package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/huh/spinner"
	"github.com/spf13/cobra"

	"github.com/KB-Developpement/kb_pro_cli/internal/apps"
	"github.com/KB-Developpement/kb_pro_cli/internal/bench"
	"github.com/KB-Developpement/kb_pro_cli/internal/ui"
)

const (
	actionUninstall       = "uninstall"
	actionRemove          = "remove"
	actionUninstallRemove = "both"
)

func newManageCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "manage",
		Short: "Manage installed KB Frappe apps (uninstall / remove)",
		Long: `Interactively manage KB apps that are currently installed on the bench.

You can uninstall an app from the site, remove its source from the bench, or do both.

  Uninstall  — bench --site <site> uninstall-app <app>  (removes from site, keeps source)
  Remove     — bench remove-app <app>                   (deletes source from apps folder)`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !bench.InBenchContainer() {
				return fmt.Errorf("kb must be run inside a Frappe bench container — use: ffm shell <bench-name>")
			}
			site, err := bench.DetectSiteName()
			if err != nil {
				return fmt.Errorf("could not detect site name: %w\nSet the active site with: bench use <site>", err)
			}
			fmt.Fprintln(os.Stderr, ui.Dim.Render("Site: "+site))
			return runManage(site)
		},
	}
}

func runManage(site string) error {
	// 1. Detect installed apps.
	installed, err := bench.DetectInstalledApps(site)
	if err != nil {
		return fmt.Errorf("could not detect installed apps: %w", err)
	}

	// 2. Filter registry to only installed KB apps.
	var manageable []apps.App
	for _, app := range apps.All {
		if installed[app.Name] {
			manageable = append(manageable, app)
		}
	}

	if len(manageable) == 0 {
		fmt.Fprintln(os.Stdout, ui.Dim.Render("No KB apps are currently installed on this site."))
		return nil
	}

	// 3. Multi-select: which apps to manage.
	options := make([]huh.Option[string], len(manageable))
	for i, app := range manageable {
		options[i] = huh.NewOption(app.Name, app.Name)
	}

	var selected []string
	var action string

	if err := huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("Select KB apps to manage").
				Description("Space to toggle · Enter to confirm · Esc/Ctrl+C to cancel").
				Options(options...).
				Value(&selected),
		),
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Choose action").
				Description("What do you want to do with the selected app(s)?").
				Options(
					huh.NewOption("Uninstall from site  (keeps source in bench)", actionUninstall),
					huh.NewOption("Remove from bench    (deletes source folder)", actionRemove),
					huh.NewOption("Uninstall + Remove   (full cleanup)", actionUninstallRemove),
				).
				Value(&action),
		),
	).Run(); err != nil {
		return nil
	}

	if len(selected) == 0 {
		fmt.Fprintln(os.Stdout, ui.Dim.Render("No apps selected."))
		return nil
	}

	// 4. Confirm destructive action.
	actionLabel := map[string]string{
		actionUninstall:       "uninstall from site",
		actionRemove:          "remove from bench",
		actionUninstallRemove: "uninstall + remove",
	}[action]

	var confirmed bool
	if err := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title(fmt.Sprintf("About to %s: %s", actionLabel, strings.Join(selected, ", "))).
				Description("This cannot be undone. Continue?").
				Value(&confirmed),
		),
	).Run(); err != nil || !confirmed {
		fmt.Fprintln(os.Stdout, ui.Dim.Render("Cancelled."))
		return nil
	}

	// 5. Execute actions sequentially.
	fmt.Fprintln(os.Stdout)

	results := make([]installResult, 0, len(selected))

	for _, name := range selected {
		var opErr error

		switch action {
		case actionUninstall:
			if spinErr := spinner.New().
				Title(fmt.Sprintf("Uninstalling %s from %s…", ui.AppName.Render(name), site)).
				Action(func() { opErr = bench.UninstallApp(site, name) }).
				Run(); spinErr != nil {
				opErr = spinErr
			}

		case actionRemove:
			if spinErr := spinner.New().
				Title(fmt.Sprintf("Removing %s from bench…", ui.AppName.Render(name))).
				Action(func() { opErr = bench.RemoveApp(name) }).
				Run(); spinErr != nil {
				opErr = spinErr
			}

		case actionUninstallRemove:
			if spinErr := spinner.New().
				Title(fmt.Sprintf("Uninstalling %s from %s…", ui.AppName.Render(name), site)).
				Action(func() { opErr = bench.UninstallApp(site, name) }).
				Run(); spinErr != nil {
				opErr = spinErr
			}
			if opErr == nil {
				if spinErr := spinner.New().
					Title(fmt.Sprintf("Removing %s from bench…", ui.AppName.Render(name))).
					Action(func() { opErr = bench.RemoveApp(name) }).
					Run(); spinErr != nil {
					opErr = spinErr
				}
			}
		}

		if opErr != nil {
			fmt.Fprintf(os.Stdout, "%s %s: %v\n", ui.Failure.Render("✗"), ui.AppName.Render(name), opErr)
		} else {
			fmt.Fprintf(os.Stdout, "%s %s\n", ui.Success.Render("✓"), ui.AppName.Render(name))
		}
		results = append(results, installResult{name, opErr})
	}

	// 6. Summary.
	printSummary(results)
	return nil
}
