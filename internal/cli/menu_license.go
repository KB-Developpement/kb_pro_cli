package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/KB-Developpement/kb_pro_cli/internal/errlog"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
	"github.com/KB-Developpement/kb_pro_cli/internal/ui"
)

const (
	licMenuStatus     = "status"
	licMenuActivate   = "activate"
	licMenuDeactivate = "deactivate"
)

// runLicenseMenu is a looping submenu for license status, activation, and
// clearing local license material. Esc / Ctrl+C exits back to the main menu.
func runLicenseMenu() {
	for {
		clearScreen()
		if !globalFlags.Quiet {
			fmt.Fprintln(os.Stderr, ui.Dim.Render("License"))
		}

		var action string
		if err := huh.NewForm(
			huh.NewGroup(
				huh.NewSelect[string]().
					Title("KB Pro license").
					Description("↑/↓ to navigate · Enter to confirm · Esc/Ctrl+C to go back").
					Options(
						huh.NewOption("View status            — tier, expiry, allowed apps", licMenuStatus),
						huh.NewOption("Activate / reactivate  — use saved key or enter a new one", licMenuActivate),
						huh.NewOption("Deactivate locally     — remove JWT cache and stored key on this machine", licMenuDeactivate),
					).
					Value(&action),
			),
		).WithKeyMap(formKeyMap()).Run(); err != nil {
			return
		}

		switch action {
		case licMenuStatus:
			_ = runLicenseStatus(context.Background())
			pause()

		case licMenuActivate:
			if err := runActivate(nil); err != nil {
				errlog.Log(err)
				fmt.Fprintf(os.Stderr, "\n%s %v\n", ui.Failure.Render("Error:"), err)
			}
			pause()

		case licMenuDeactivate:
			runLicenseDeactivate()
			pause()
		}
	}
}

func runLicenseDeactivate() {
	state := license.CurrentState()
	hasKey := license.LoadLicenseKey() != ""
	if state == nil && !hasKey {
		fmt.Fprintln(os.Stdout, ui.Dim.Render("No local license cache or stored key to remove."))
		return
	}

	var confirmed bool
	if err := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Remove local license files?").
				Description(strings.Join([]string{
					"This deletes ~/.config/kb/license.json, license.jwt, and license_key on this machine.",
					"The license server is not contacted; your activation may still count on the server.",
				}, " ")).
				Value(&confirmed),
		),
	).WithKeyMap(formKeyMap()).Run(); err != nil || !confirmed {
		fmt.Fprintln(os.Stdout, ui.Dim.Render("Cancelled."))
		return
	}

	license.ClearLocalLicense()
	license.RunCheck()
	fmt.Fprintln(os.Stdout, ui.Success.Render("Local license data removed."))
	fmt.Fprintln(os.Stdout, ui.Dim.Render("Run Activate / reactivate when you are ready to use a license again."))
}
