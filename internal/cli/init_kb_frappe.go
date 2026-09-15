package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/KB-Developpement/kb_pro_cli/internal/bench"
	"github.com/KB-Developpement/kb_pro_cli/internal/errlog"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
	"github.com/KB-Developpement/kb_pro_cli/internal/ui"
)

// runInitKBFrappe downloads the kb_frappe tarball from the license server and
// replaces apps/frappe in-place. The on-disk directory remains named "frappe"
// since the tarball's top-level folder is already named "frappe".
func runInitKBFrappe(ctx context.Context) error {
	// Sampled before anything touches apps/frappe — see devServerAction.
	devWasRunning := bench.IsDevServerRunning()

	token, serverURL, err := licenseTokenAndServer(ctx)
	if err != nil {
		return err
	}
	if !license.AllowedSet()["kb_frappe"] {
		return fmt.Errorf("your license does not allow kb_frappe — contact KB to update your license")
	}

	var archivePath string
	if spinErr := runWithSpinner("Downloading KB Frappe fork…", func() {
		archivePath, err = license.DownloadApp(ctx, serverURL, token, "kb_frappe", "")
	}); spinErr != nil {
		return spinErr
	}
	if err != nil {
		return fmt.Errorf("download kb_frappe: %w", err)
	}
	defer os.Remove(archivePath)

	var benchOut string
	if spinErr := runWithSpinner(fmt.Sprintf("Replacing %s with KB Frappe fork…", ui.AppName.Render("frappe")), func() {
		benchOut, err = bench.UpdateFromArchive(ctx, archivePath, "frappe")
	}); spinErr != nil {
		return spinErr
	}
	// From here on apps/frappe has been swapped (or swapped and rolled back),
	// either of which takes a running dev server down with it.
	defer maybeRestartDevServer(ctx, devWasRunning)
	if err != nil {
		if globalFlags.Verbose && benchOut != "" {
			fmt.Fprintln(os.Stdout, ui.Dim.Render(benchOut))
		}
		errlog.Logf("init-kb-frappe: %v", err)
		return fmt.Errorf("replacing frappe: %w", err)
	}
	if globalFlags.Verbose && benchOut != "" {
		fmt.Fprintln(os.Stdout, ui.Dim.Render(benchOut))
	}

	if syncErr := bench.SyncAppState("frappe"); syncErr != nil && !globalFlags.Quiet {
		fmt.Fprintf(os.Stderr, "  warning: could not update apps.json for frappe: %v\n", syncErr)
	}

	fmt.Fprintf(os.Stdout, "%s KB Frappe fork installed successfully\n", ui.Success.Render("✓"))
	return nil
}
