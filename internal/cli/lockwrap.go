package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/KB-Developpement/kb_pro_cli/internal/bench"
	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
	"github.com/KB-Developpement/kb_pro_cli/internal/ui"
)

// runLocked runs one complete bench mutation under the bench lock. It is called
// from inside the run* functions (not the cobra wrappers), so the interactive
// menu takes the lock too.
//
// In order: the effective user must own the bench root; the lock .kb/lock is
// taken without blocking (a second holder fails at once and is named by PID);
// no .kb/*.json may come from a newer kb; and an earlier interrupted
// transaction is recovered or replayed before fn runs. A journal that cannot be
// resolved refuses here, naming the journal path.
func runLocked(ctx context.Context, fn func() error) error {
	root := bench.Root()
	if err := kbstate.CheckBenchOwner(root); err != nil {
		return err
	}
	lock, err := kbstate.AcquireLock(root)
	if err != nil {
		return err
	}
	defer lock.Release()
	if err := kbstate.CheckSchemas(root); err != nil {
		return err
	}
	kbstate.CleanTempFiles(root)
	notes, err := bench.RecoverJournal(ctx, bench.RecoverMutate)
	for _, n := range notes {
		fmt.Fprintln(os.Stderr, ui.Dim.Render(n))
	}
	if err != nil {
		return err
	}
	return fn()
}
