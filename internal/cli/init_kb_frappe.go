package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/charmbracelet/huh/spinner"

	"github.com/KB-Developpement/kb_pro_cli/internal/bench"
	"github.com/KB-Developpement/kb_pro_cli/internal/errlog"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
	"github.com/KB-Developpement/kb_pro_cli/internal/ui"
)

// runInitKBFrappe downloads the kb_frappe tarball from the license server and
// replaces apps/frappe in-place. The on-disk directory remains named "frappe"
// since the tarball's top-level folder is already named "frappe".
func runInitKBFrappe(ctx context.Context) error {
	token, serverURL, err := licenseTokenAndServer(ctx)
	if err != nil {
		return err
	}

	var archivePath string
	if spinErr := spinner.New().
		Title("Downloading KB Frappe fork…").
		Action(func() {
			archivePath, err = license.DownloadApp(ctx, serverURL, token, "kb_frappe", "")
		}).
		Run(); spinErr != nil {
		return spinErr
	}
	if err != nil {
		return fmt.Errorf("download kb_frappe: %w", err)
	}
	defer os.Remove(archivePath)

	var benchOut string
	if spinErr := spinner.New().
		Title(fmt.Sprintf("Replacing %s with KB Frappe fork…", ui.AppName.Render("frappe"))).
		Action(func() {
			benchOut, err = bench.UpdateFromArchive(ctx, archivePath, "frappe")
		}).
		Run(); spinErr != nil {
		return spinErr
	}
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
