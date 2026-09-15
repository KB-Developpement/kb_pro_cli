package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/KB-Developpement/kb_pro_cli/internal/bench"
	"github.com/KB-Developpement/kb_pro_cli/internal/ui"
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

By default it refuses to run unless apps/frappe is detected as stock
frappe/frappe; pass --force to run anyway (for example when the git remotes
are missing or unrecognised).

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
			isStock, err := bench.DetectFrappeOrigin()
			switch {
			case err != nil && !force:
				return fmt.Errorf("%v — pass --force to replace apps/frappe anyway", err)
			case err == nil && !isStock && !force:
				fmt.Fprintln(os.Stdout, ui.Dim.Render("apps/frappe is already the KB Frappe fork — nothing to do (use --force to reinstall)."))
				return nil
			}
			// bench build + migrate can exceed any sane timeout on a first run.
			return runInitKBFrappe(context.Background())
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Replace apps/frappe even if it is not detected as stock Frappe")
	return cmd
}
