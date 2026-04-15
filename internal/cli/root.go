package cli

import (
	"fmt"
	"os"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/KB-Developpement/kb_pro_cli/internal/license"
	"github.com/KB-Developpement/kb_pro_cli/internal/ui"
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

	// Persistent flags available on every subcommand.
	root.PersistentFlags().BoolVar(&globalFlags.NoInput, "no-input", false, "Disable interactive prompts (requires explicit flags for inputs)")
	root.PersistentFlags().BoolVarP(&globalFlags.Quiet, "quiet", "q", false, "Suppress informational output")
	root.PersistentFlags().BoolVarP(&globalFlags.Verbose, "verbose", "v", false, "Print verbose output including raw bench output on success")
	root.PersistentFlags().BoolVar(&globalFlags.NoColor, "no-color", false, "Disable colours in output (also honoured via NO_COLOR env var)")

	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if globalFlags.NoColor {
			ui.DisableColors()
		}
		if cmd.Annotations["skipChecks"] != "true" {
			runUpdateCheck()
		}
		if cmd.Annotations["skipChecks"] != "true" && cmd.Annotations["skipLicenseCheck"] != "true" {
			license.RunCheck()
		}
		return nil
	}

	root.AddCommand(newInstallCmd())
	root.AddCommand(newAddCmd())
	root.AddCommand(newUpdateCmd())
	root.AddCommand(newManageCmd())
	root.AddCommand(newActivateCmd())
	root.AddCommand(newLicenseCmd())
	root.AddCommand(newCompletionCmd())

	return root
}

// newCompletionCmd returns a subcommand that generates shell completion scripts.
func newCompletionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:       "completion [bash|zsh|fish|powershell]",
		Short:     "Generate shell completion scripts",
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		Args:      cobra.ExactArgs(1),
		Annotations: map[string]string{
			"skipChecks": "true",
		},
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return cmd.Root().GenBashCompletionV2(os.Stdout, true)
			case "zsh":
				return cmd.Root().GenZshCompletion(os.Stdout)
			case "fish":
				return cmd.Root().GenFishCompletion(os.Stdout, true)
			case "powershell":
				return cmd.Root().GenPowerShellCompletionWithDesc(os.Stdout)
			}
			return fmt.Errorf("unsupported shell: %s", args[0])
		},
	}
	return cmd
}

// Execute runs the root command and waits for any background goroutines to
// finish writing their state files before the process exits.
func Execute() error {
	err := newRootCmd().Execute()
	waitForUpdateCheck()
	license.WaitForCheck()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	return nil
}
