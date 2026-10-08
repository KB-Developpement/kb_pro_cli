package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/KB-Developpement/kb_pro_cli/internal/bench"
)

// newInitKBFrappeCmd exposes the menu's "Init KB Frappe" action as a
// subcommand so scripted and CI runs can replace stock Frappe without a TTY.
func newInitKBFrappeCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "init-kb-frappe",
		Short: "Replace stock Frappe in apps/frappe with the licensed KB Frappe fork",
		Long: `Download the licensed kb_frappe archive from the license server and replace
apps/frappe with it in place (the directory keeps its name). Runs bench setup
requirements, pip install -e, bench build and bench migrate, then updates
sites/apps.json. This is the same action as "Init KB Frappe" in the menu.

An apps/frappe that holds a .git is replaced only when it is a clean stock
frappe/frappe checkout: remotes exactly frappe/frappe on GitHub, no changes, no
stash, one worktree, one branch with an upstream and no local commits. Any
other Git checkout is refused, with or without --force; the stock tree is not
kept afterwards. An apps/frappe without a .git needs --force (kb cannot tell what
it is).

Examples:
  kb init-kb-frappe
  kb init-kb-frappe --force --no-input
`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireInitializedForCLI(); err != nil {
				return err
			}
			if !bench.InBenchContainer() {
				return fmt.Errorf("kb must be run inside a Frappe bench container — use: ffm shell <bench-name>")
			}
			return runInitKBFrappe(cmd.Context(), force)
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Replace an apps/frappe that has no .git (never bypasses the Git checks)")
	return cmd
}
