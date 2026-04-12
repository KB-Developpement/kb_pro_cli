package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/KB-Developpement/kb_pro_cli/internal/license"
	"github.com/KB-Developpement/kb_pro_cli/internal/ui"
)

func newLicenseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "license",
		Short:        "Show current license status",
		SilenceUsage: true,
		// Skip both update check and license check — this is meta-information about the license itself.
		Annotations: map[string]string{"skipChecks": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLicenseStatus()
		},
	}
	return cmd
}

func runLicenseStatus() error {
	// Run a synchronous check so we have fresh state.
	license.RunCheck()

	state := license.CurrentState()
	if state == nil {
		fmt.Fprintln(os.Stdout, ui.Failure.Render("No license found."))
		fmt.Fprintln(os.Stdout, ui.Dim.Render("Run: kb activate — to activate your license."))
		return nil
	}

	if !state.Valid {
		fmt.Fprintln(os.Stdout, ui.Failure.Render("License expired."))
		fmt.Fprintf(os.Stdout, "  Expired:  %s\n", state.ExpiresAt.Format(time.RFC3339))
		fmt.Fprintln(os.Stdout, ui.Dim.Render("Run: kb activate — to reactivate."))
		return nil
	}

	fmt.Fprintln(os.Stdout, ui.Success.Render("License active"))
	fmt.Fprintf(os.Stdout, "  Client:   %s\n", state.ClientID)
	fmt.Fprintf(os.Stdout, "  Tier:     %s\n", state.Tier)
	fmt.Fprintf(os.Stdout, "  Expires:  %s\n", state.ExpiresAt.Format("2006-01-02"))
	fmt.Fprintf(os.Stdout, "  Apps:     %s\n", strings.Join(state.AllowedApps, ", "))
	return nil
}
