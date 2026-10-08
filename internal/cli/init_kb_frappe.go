package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/KB-Developpement/kb_pro_cli/internal/apps"
	"github.com/KB-Developpement/kb_pro_cli/internal/bench"
	"github.com/KB-Developpement/kb_pro_cli/internal/errlog"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
	"github.com/KB-Developpement/kb_pro_cli/internal/ui"
)

// initKBFrappeTimeout bounds the whole operation. bench build and bench migrate
// on a first run can take a long time, but not forever.
var initKBFrappeTimeout = 90 * time.Minute

// frappeLine is the release line init-kb-frappe pins. It is a constant until
// the framework gets a registry row of its own in Phase 3.
const frappeLine = 1

// runInitKBFrappe downloads the kb_frappe tarball from the license server and
// replaces apps/frappe in-place. The on-disk directory remains named "frappe"
// since the tarball's top-level folder is already named "frappe".
//
// An apps/frappe that holds a .git is replaced only when it is a clean stock
// frappe/frappe checkout (the stock-Frappe predicate); --force bypasses none of
// that. Any other Git checkout is refused before anything is touched.
func runInitKBFrappe(ctx context.Context, force bool) error {
	ctx, cancel := context.WithTimeout(ctx, initKBFrappeTimeout)
	defer cancel()
	return runLocked(ctx, func() error { return doInitKBFrappe(ctx, force) })
}

func doInitKBFrappe(ctx context.Context, force bool) error {
	// Sampled before anything touches apps/frappe — see devServerAction.
	devWasRunning := bench.IsDevServerRunning()

	stock := false
	if _, err := os.Lstat(filepath.Join(bench.Root(), "apps", "frappe", ".git")); err == nil {
		// A Git checkout: only the stock predicate may replace it.
		if err := bench.CheckStockFrappe(); err != nil {
			return err
		}
		stock = true
	} else if !force {
		return fmt.Errorf("apps/frappe has no .git of its own, so kb cannot tell whether it is stock Frappe — pass --force to replace apps/frappe anyway")
	}

	token, serverURL, err := licenseTokenAndServer(ctx)
	if err != nil {
		return err
	}
	if !allowedSetFn()["kb_frappe"] {
		return fmt.Errorf("your license does not allow kb_frappe — contact KB to update your license")
	}

	fw := apps.Framework
	fw.Line = frappeLine

	var dl *license.Download
	if spinErr := runWithSpinner("Downloading KB Frappe fork…", func() {
		dlCtx, dlCancel := context.WithTimeout(ctx, 10*time.Minute)
		defer dlCancel()
		dl, err = license.DownloadApp(dlCtx, serverURL, token, license.DownloadRequest{
			App:        fw.LicenseID(),
			Repository: fw.Repository(),
			Line:       fw.Line,
		})
	}); spinErr != nil {
		return spinErr
	}
	if err != nil {
		return fmt.Errorf("download kb_frappe: %w", err)
	}
	defer os.Remove(dl.Path)

	var res bench.ApplyResult
	if spinErr := runWithSpinner(fmt.Sprintf("Replacing %s with KB Frappe fork…", ui.AppName.Render("frappe")), func() {
		res, err = bench.ApplyArchive(ctx, bench.ApplyPlan{Command: bench.CmdInitFrappe, App: fw, Download: dl, StockConversion: stock})
	}); spinErr != nil {
		return spinErr
	}
	// From here on apps/frappe has been swapped (or swapped and rolled back),
	// either of which takes a running dev server down with it.
	defer maybeRestartDevServer(ctx, devWasRunning)
	if err != nil {
		if globalFlags.Verbose && res.Output != "" {
			fmt.Fprintln(os.Stdout, ui.Dim.Render(res.Output))
		}
		errlog.Logf("init-kb-frappe: %v", err)
		return fmt.Errorf("replacing frappe: %w", err)
	}
	if globalFlags.Verbose && res.Output != "" {
		fmt.Fprintln(os.Stdout, ui.Dim.Render(res.Output))
	}
	printTransactionNotes("frappe", res)

	fmt.Fprintf(os.Stdout, "%s KB Frappe fork installed successfully\n", ui.Success.Render("✓"))
	return nil
}
