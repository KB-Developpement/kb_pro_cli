package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/huh/spinner"
	"github.com/spf13/cobra"

	"github.com/KB-Developpement/kb_pro_cli/internal/license"
	"github.com/KB-Developpement/kb_pro_cli/internal/ui"
)

func newActivateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "activate [license-key]",
		Short: "Activate this machine with a KB Pro license key",
		Long: `Activate this machine using your KB Pro license key.

The license key is provided by KB-Developpement when you purchase a support contract.
Your machine fingerprint is computed locally and sent to the license server to
register this machine. A signed token is stored at ~/.config/kb/license.json.

If a license key is already saved locally, it will be used automatically without
prompting. To re-activate with a different key, pass it as an argument.`,
		SilenceUsage: true,
		// Skip both update check and license check for this command.
		Annotations: map[string]string{"skipChecks": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runActivate(args)
		},
	}
	return cmd
}

func runActivate(args []string) error {
	// Resolve license key: argument > saved file > prompt.
	licenseKey := ""
	if len(args) > 0 {
		licenseKey = args[0]
	}
	if licenseKey == "" {
		licenseKey = license.LoadLicenseKey()
	}
	if licenseKey == "" {
		if err := huh.NewForm(
			huh.NewGroup(
				huh.NewInput().
					Title("KB Pro License Key").
					Description("Enter the license key provided by KB-Developpement.").
					EchoMode(huh.EchoModePassword).
					Value(&licenseKey),
			),
		).WithKeyMap(formKeyMap()).Run(); err != nil {
			return nil
		}
	}
	if licenseKey == "" {
		return fmt.Errorf("no license key provided")
	}

	var token string
	var activateErr error
	if spinErr := spinner.New().
		Title("Activating license…").
		Action(func() {
			fp, err := license.Fingerprint()
			if err != nil {
				activateErr = fmt.Errorf("compute machine fingerprint: %w", err)
				return
			}
			token, activateErr = license.Activate("", licenseKey, fp)
		}).
		Run(); spinErr != nil {
		return spinErr
	}
	if activateErr != nil {
		return activateErr
	}

	// Save the license key separately (write-once pattern).
	if err := license.SaveLicenseKey(licenseKey); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not save license key: %v\n", err)
	}

	// Save the JWT to the cache file.
	if err := license.SaveTokenCache(token, time.Now().UTC()); err != nil {
		return fmt.Errorf("save license token: %w", err)
	}

	fmt.Fprintln(os.Stdout, ui.Success.Render("License activated successfully."))
	fmt.Fprintln(os.Stdout, ui.Dim.Render("Run: kb license — to view license details."))
	return nil
}
