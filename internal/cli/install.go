package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/huh/spinner"

	"github.com/KB-Developpement/kb_pro_cli/internal/apps"
	"github.com/KB-Developpement/kb_pro_cli/internal/bench"
	"github.com/KB-Developpement/kb_pro_cli/internal/ui"
)

func runInstall() error {
	// 1. Verify we're inside a bench container.
	if !bench.InBenchContainer() {
		return fmt.Errorf("kb must be run inside a Frappe bench container — use: ffm shell <bench-name>")
	}

	// 2. Detect site name.
	site, err := bench.DetectSiteName()
	if err != nil {
		return fmt.Errorf("could not detect site name: %w\nSet the active site with: bench use <site>", err)
	}
	fmt.Fprintln(os.Stderr, ui.Dim.Render("Site: "+site))

	// 3. Detect installed apps (non-fatal on error).
	installed, detectErr := bench.DetectInstalledApps(site)
	if detectErr != nil {
		fmt.Fprintln(os.Stderr, ui.Dim.Render("Warning: could not detect installed apps — all apps will be shown"))
	}

	// 4. Split registry into already-installed and selectable.
	var alreadyInstalled []string
	var selectable []apps.App
	for _, app := range apps.All {
		if installed[app.Name] {
			alreadyInstalled = append(alreadyInstalled, app.Name)
		} else {
			selectable = append(selectable, app)
		}
	}

	if len(alreadyInstalled) > 0 {
		fmt.Fprintln(os.Stderr, ui.Dim.Render("Already installed: "+strings.Join(alreadyInstalled, ", ")))
	}

	if len(selectable) == 0 {
		fmt.Fprintln(os.Stdout, ui.Success.Render("All KB apps are already installed."))
		return nil
	}

	// 5. Build multi-select options.
	options := make([]huh.Option[string], len(selectable))
	for i, app := range selectable {
		options[i] = huh.NewOption(app.Name, app.Name)
	}

	var selected []string
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("Select KB apps to install").
				Description("Space to toggle · Enter to confirm · Esc/Ctrl+C to cancel").
				Options(options...).
				Value(&selected),
		),
	)

	if err := form.Run(); err != nil {
		// ErrUserAborted or context cancel — exit cleanly.
		return nil
	}

	if len(selected) == 0 {
		fmt.Fprintln(os.Stdout, ui.Dim.Render("No apps selected."))
		return nil
	}

	// Build a map from name → App for quick URL lookup.
	appByName := make(map[string]apps.App, len(selectable))
	for _, app := range selectable {
		appByName[app.Name] = app
	}

	// 6. Install selected apps sequentially.
	fmt.Fprintln(os.Stdout)

	type result struct {
		name string
		err  error
	}
	results := make([]result, 0, len(selected))

	for _, name := range selected {
		app := appByName[name]

		// get-app
		var getErr error
		spinErr := spinner.New().
			Title(fmt.Sprintf("Downloading %s…", ui.AppName.Render(name))).
			Action(func() { getErr = bench.GetApp(app.URL) }).
			Run()
		if spinErr != nil {
			getErr = spinErr
		}
		if getErr != nil {
			fmt.Fprintf(os.Stdout, "%s %s: %v\n",
				ui.Failure.Render("✗"),
				ui.AppName.Render(name),
				getErr,
			)
			results = append(results, result{name, getErr})
			continue
		}

		// install-app
		var installErr error
		spinErr = spinner.New().
			Title(fmt.Sprintf("Installing %s on %s…", ui.AppName.Render(name), site)).
			Action(func() { installErr = bench.InstallApp(site, name) }).
			Run()
		if spinErr != nil {
			installErr = spinErr
		}
		if installErr != nil {
			fmt.Fprintf(os.Stdout, "%s %s: %v\n",
				ui.Failure.Render("✗"),
				ui.AppName.Render(name),
				installErr,
			)
			results = append(results, result{name, installErr})
			continue
		}

		fmt.Fprintf(os.Stdout, "%s %s\n",
			ui.Success.Render("✓"),
			ui.AppName.Render(name),
		)
		results = append(results, result{name, nil})
	}

	// 7. Summary.
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
		fmt.Fprintln(os.Stdout, ui.Success.Render(fmt.Sprintf("Done — %d app(s) installed successfully.", successes)))
	} else {
		fmt.Fprintln(os.Stdout, ui.Bold.Render(fmt.Sprintf("%d installed, %d failed:", successes, failures)))
		for _, r := range results {
			if r.err != nil {
				fmt.Fprintf(os.Stdout, "  %s %s\n", ui.Failure.Render("✗"), r.name)
			}
		}
	}

	return nil
}
