package cli

import (
	"fmt"
	"os"

	"github.com/charmbracelet/huh"

	"github.com/KB-Developpement/kb_pro_cli/internal/bench"
	"github.com/KB-Developpement/kb_pro_cli/internal/ui"
)

const (
	menuInstall = "install"
	menuAdd     = "add"
	menuManage  = "manage"
	menuUpdate  = "update"
)

func runMainMenu() error {
	if !bench.InBenchContainer() {
		return fmt.Errorf("kb must be run inside a Frappe bench container — use: ffm shell <bench-name>")
	}

	site, err := bench.DetectSiteName()
	if err != nil {
		return fmt.Errorf("could not detect site name: %w\nSet the active site with: bench use <site>", err)
	}
	fmt.Fprintln(os.Stderr, ui.Dim.Render("Site: "+site))

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
					huh.NewOption("Update kb             — check for a newer version", menuUpdate),
				).
				Value(&choice),
		),
	).WithKeyMap(formKeyMap()).Run(); err != nil {
		return nil
	}

	switch choice {
	case menuInstall:
		return runInstall(site)
	case menuAdd:
		return runAddToBench()
	case menuManage:
		return runManage(site, false)
	case menuUpdate:
		return runUpdate(false, false)
	}
	return nil
}
