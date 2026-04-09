package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/huh/spinner"

	"github.com/KB-Developpement/kb_pro_cli/internal/apps"
	"github.com/KB-Developpement/kb_pro_cli/internal/bench"
	"github.com/KB-Developpement/kb_pro_cli/internal/config"
	"github.com/KB-Developpement/kb_pro_cli/internal/ui"
)

// resolveToken returns a GitHub token from env/config, prompting the user if none is found.
func resolveToken() string {
	token := config.LoadToken()
	if token != "" {
		return token
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
				Title("Save token to ~/.config/kb/github_token?").
				Value(&saveToken),
		),
	).WithKeyMap(formKeyMap()).Run()

	if token == "" {
		fmt.Fprintln(os.Stderr, ui.Dim.Render("No token provided — private repos may fail."))
		return ""
	}
	if saveToken {
		if err := config.SaveToken(token); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not save token: %v\n", err)
		}
	}
	return token
}

// runInstall downloads and installs selected apps on the given site.
// Only shows apps that are not yet in the bench and not installed on site.
// Apps already downloaded (in bench) should be installed via "Manage".
func runInstall(site string) error {
	installed, detectErr := bench.DetectInstalledApps(site)
	if detectErr != nil {
		fmt.Fprintln(os.Stderr, ui.Dim.Render("Warning: could not detect installed apps — all apps will be shown"))
	}
	inBench := bench.DetectAppsInBench()

	var alreadyInstalled, alreadyDownloaded []string
	var selectable []apps.App
	for _, app := range apps.All {
		switch {
		case installed[app.Name]:
			alreadyInstalled = append(alreadyInstalled, app.Name)
		case inBench[app.Name]:
			alreadyDownloaded = append(alreadyDownloaded, app.Name)
		default:
			selectable = append(selectable, app)
		}
	}

	if len(alreadyInstalled) > 0 {
		fmt.Fprintln(os.Stderr, ui.Dim.Render("Already installed: "+strings.Join(alreadyInstalled, ", ")))
	}
	if len(alreadyDownloaded) > 0 {
		fmt.Fprintln(os.Stderr, ui.Dim.Render("Already downloaded (use Manage to install): "+strings.Join(alreadyDownloaded, ", ")))
	}
	if len(selectable) == 0 {
		fmt.Fprintln(os.Stdout, ui.Success.Render("All KB apps are already installed or downloaded."))
		return nil
	}

	selected, err := selectApps(selectable, "Select KB apps to install")
	if err != nil || len(selected) == 0 {
		return nil
	}

	token := resolveToken()

	appByName := indexByName(selectable)
	fmt.Fprintln(os.Stdout)

	results := make([]installResult, 0, len(selected))

	for _, name := range selected {
		app := appByName[name]

		var getErr error
		if spinErr := spinner.New().
			Title(fmt.Sprintf("Downloading %s…", ui.AppName.Render(name))).
			Action(func() { getErr = bench.GetApp(app.URL, token) }).
			Run(); spinErr != nil {
			getErr = spinErr
		}
		if getErr != nil {
			fmt.Fprintf(os.Stdout, "%s %s: %v\n", ui.Failure.Render("✗"), ui.AppName.Render(name), getErr)
			results = append(results, installResult{name, getErr})
			continue
		}

		var installErr error
		if spinErr := spinner.New().
			Title(fmt.Sprintf("Installing %s on %s…", ui.AppName.Render(name), site)).
			Action(func() { installErr = bench.InstallApp(site, name) }).
			Run(); spinErr != nil {
			installErr = spinErr
		}
		if installErr != nil {
			fmt.Fprintf(os.Stdout, "%s %s: %v\n", ui.Failure.Render("✗"), ui.AppName.Render(name), installErr)
			results = append(results, installResult{name, installErr})
			continue
		}

		fmt.Fprintf(os.Stdout, "%s %s\n", ui.Success.Render("✓"), ui.AppName.Render(name))
		results = append(results, installResult{name, nil})
	}

	printSummary(results)
	return nil
}

// runAddToBench downloads selected apps into the bench apps folder without installing them on any site.
// Skips apps whose source folder already exists in the bench.
func runAddToBench() error {
	inBench := bench.DetectAppsInBench()

	var alreadyPresent []string
	var selectable []apps.App
	for _, app := range apps.All {
		if inBench[app.Name] {
			alreadyPresent = append(alreadyPresent, app.Name)
		} else {
			selectable = append(selectable, app)
		}
	}

	if len(alreadyPresent) > 0 {
		fmt.Fprintln(os.Stderr, ui.Dim.Render("Already in bench: "+strings.Join(alreadyPresent, ", ")))
	}
	if len(selectable) == 0 {
		fmt.Fprintln(os.Stdout, ui.Success.Render("All KB apps are already present in the bench."))
		return nil
	}

	selected, err := selectApps(selectable, "Select KB apps to add to bench")
	if err != nil || len(selected) == 0 {
		return nil
	}

	token := resolveToken()

	appByName := indexByName(selectable)
	fmt.Fprintln(os.Stdout)

	results := make([]installResult, 0, len(selected))

	for _, name := range selected {
		app := appByName[name]

		var getErr error
		if spinErr := spinner.New().
			Title(fmt.Sprintf("Downloading %s…", ui.AppName.Render(name))).
			Action(func() { getErr = bench.GetApp(app.URL, token) }).
			Run(); spinErr != nil {
			getErr = spinErr
		}
		if getErr != nil {
			fmt.Fprintf(os.Stdout, "%s %s: %v\n", ui.Failure.Render("✗"), ui.AppName.Render(name), getErr)
		} else {
			fmt.Fprintf(os.Stdout, "%s %s\n", ui.Success.Render("✓"), ui.AppName.Render(name))
		}
		results = append(results, installResult{name, getErr})
	}

	printSummary(results)
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

	if len(selected) == 0 {
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
