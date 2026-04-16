package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/mattn/go-isatty"

	"github.com/KB-Developpement/kb_pro_cli/internal/bench"
	"github.com/KB-Developpement/kb_pro_cli/internal/config"
	"github.com/KB-Developpement/kb_pro_cli/internal/errlog"
	"github.com/KB-Developpement/kb_pro_cli/internal/ui"
)

const (
	menuInstall  = "install"
	menuAdd      = "add"
	menuManage   = "manage"
	menuUpgrade  = "upgrade"
	menuLicense  = "license"
	menuSettings = "settings"
)

// clearScreen writes the standard ANSI escape sequence to clear the terminal.
// It is a no-op when stdout is not connected to a terminal.
func clearScreen() {
	if isatty.IsTerminal(os.Stdout.Fd()) {
		fmt.Print("\033[H\033[2J")
	}
}

// pause waits for the user to press Enter before the menu loop clears the screen.
// It is skipped when not running in menu mode, --no-input is set, or stdin is
// not a terminal (e.g. piped / scripted invocations).
func pause() {
	if !inMenuMode || globalFlags.NoInput || !isatty.IsTerminal(os.Stdin.Fd()) {
		return
	}
	fmt.Println()
	fmt.Print("Press Enter to return to menu…")
	fmt.Scanln()
}

func runMainMenu() error {
	ensureFirstRunSetup()
	if !config.IsInitialized() {
		return nil
	}

	if !bench.InBenchContainer() {
		return fmt.Errorf("kb must be run inside a Frappe bench container — use: ffm shell <bench-name>")
	}

	site, err := bench.DetectSiteName()
	if err != nil {
		return fmt.Errorf("could not detect site name: %w\nSet the active site with: bench use <site>", err)
	}

	inMenuMode = true
	defer func() { inMenuMode = false }()

	for {
		clearScreen()
		if !globalFlags.Quiet {
			fmt.Fprintln(os.Stderr, ui.Dim.Render("Site: "+site))
		}

		var choice string
		if err := huh.NewForm(
			huh.NewGroup(
				huh.NewSelect[string]().
					Title("KB — What would you like to do?").
					Description("↑/↓ to navigate · Enter to confirm · Esc/Ctrl+C to cancel").
					Options(
						huh.NewOption("Install apps          — download and install on this site", menuInstall),
						huh.NewOption("Add apps to bench     — download only, skip site install", menuAdd),
						huh.NewOption("Manage apps           — install downloaded / uninstall / remove", menuManage),
						huh.NewOption("Upgrade apps          — pull latest changes and migrate", menuUpgrade),
						huh.NewOption("License               — status, activate, deactivate locally", menuLicense),
						huh.NewOption("Settings              — license server URL, GitHub token", menuSettings),
					).
					Value(&choice),
			),
		).WithKeyMap(formKeyMap()).Run(); err != nil {
			return nil // Esc / Ctrl+C — exit to shell
		}

		// Each action gets a fresh operation context.
		// Upgrade gets its own long-lived context since bench update can take many
		// minutes per app; per-app timeouts inside runUpgrade handle the granularity.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		var actionErr error
		switch choice {
		case menuInstall:
			actionErr = runInstall(ctx, site, nil, "")
		case menuAdd:
			actionErr = runAddToBench(ctx, nil, "")
		case menuManage:
			actionErr = runManage(ctx, site, false)
		case menuUpgrade:
			cancel() // release the 10-min context; upgrade manages its own per-app timeouts
			actionErr = runUpgrade(context.Background(), nil)
		case menuLicense:
			runLicenseMenu()
		case menuSettings:
			runConfigEdit()
		}
		cancel()

		if actionErr != nil {
			errlog.Log(actionErr)
			fmt.Fprintf(os.Stderr, "\n%s %v\n", ui.Failure.Render("Error:"), actionErr)
			pause()
		}
	}
}
