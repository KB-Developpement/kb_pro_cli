package cli

import (
	"fmt"
	"os"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/KB-Developpement/kb_pro_cli/internal/version"
)

// formKeyMap returns a huh KeyMap where Esc is bound to Quit, matching the
// behaviour users expect (Esc cancels / goes back to the shell).
func formKeyMap() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("ctrl+c", "esc"))
	return km
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "kb",
		Short: "KB-Developpement Frappe app manager",
		Long: `kb is an interactive manager for KB-Developpement custom Frappe apps.

Run inside a Frappe bench container (via ffm shell) to install, add,
or manage apps from the KB-Developpement GitHub organisation.`,
		SilenceUsage: true,
		Version: fmt.Sprintf("%s (commit %s, built %s)",
			version.Version, version.Commit, version.Date),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMainMenu()
		},
	}

	root.SetVersionTemplate("kb {{.Version}}\n")

	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if cmd.Name() != "update" {
			runUpdateCheck()
		}
		return nil
	}

	root.AddCommand(newUpdateCmd())
	root.AddCommand(newManageCmd())

	return root
}

// Execute runs the root command and waits for any background update-check
// goroutine to finish writing its state file before the process exits.
func Execute() error {
	err := newRootCmd().Execute()
	waitForUpdateCheck()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	return nil
}
