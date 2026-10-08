package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/KB-Developpement/kb_pro_cli/internal/apps"
	"github.com/KB-Developpement/kb_pro_cli/internal/bench"
	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
)

// newStatusCmd returns "kb status": a read-only report on the bench's source
// provenance. It takes no lock, makes no network call (skipChecks also skips
// the update and license startup hooks) and writes nothing.
func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the bench root, per-app provenance, journal state and retained copies",
		Long: `Report, read-only, where each KB app's source came from.

For every app in the bench: provenance (installed, adopted or unknown), the
release tag and commit in its receipt, any unfinished transaction in the
journal, copies kb keeps under .kb/recovery (with sizes), and the PID named in
.kb/lock. It takes no lock, makes no network call and changes nothing.`,
		Annotations:  map[string]string{"skipChecks": "true", "skipLicenseCheck": "true"},
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runStatus(cmd.OutOrStdout())
		},
	}
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func runStatus(w io.Writer) error {
	root := bench.Root()
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return fmt.Errorf("bench directory %q is not accessible (set KB_BENCH_ROOT to your Frappe bench root)", root)
	}
	fmt.Fprintf(w, "Bench: %s\n", root)

	// Lock: read the PID line only; probing the lock would take it.
	if pid, ok := kbstate.HolderPID(root); ok {
		state := "not running"
		if kbstate.ProcessAlive(pid) {
			state = "running"
		}
		fmt.Fprintf(w, "Lock: .kb/lock names PID %d (%s)\n", pid, state)
	} else {
		fmt.Fprintln(w, "Lock: no holder recorded")
	}

	js := kbstate.ReadJournal(root)
	switch {
	case js.Missing:
		fmt.Fprintln(w, "Journal: none")
	case js.Newer != nil:
		fmt.Fprintf(w, "Journal: %v\n", js.Newer)
	case js.Malformed != "":
		fmt.Fprintf(w, "Journal: %s is unusable (%s)\n", kbstate.JournalPath(root), js.Malformed)
	case js.Journal.Pending():
		j := js.Journal
		fmt.Fprintf(w, "Journal: PENDING %s of %s at step %s (%s)\n", j.Command, j.App, j.Step, kbstate.JournalPath(root))
	default:
		fmt.Fprintf(w, "Journal: step %s (nothing pending)\n", js.Journal.Step)
	}

	fmt.Fprintln(w, "Apps:")
	listed := 0
	for _, a := range append(append([]apps.App{}, apps.All...), apps.Framework) {
		dir := a.Dir()
		rs := kbstate.ReadReceipt(root, dir)
		inBench := false
		if fi, err := os.Stat(filepath.Join(root, "apps", dir)); err == nil && fi.IsDir() {
			inBench = true
		}
		if !inBench && rs.Raw == nil {
			continue
		}
		listed++
		note := ""
		if bench.GitGuard(root, dir) != nil {
			note = " [Git checkout]"
		}
		if !inBench {
			note += " [receipt without an apps/ directory]"
		}
		switch {
		case rs.Valid():
			r := rs.Receipt
			fmt.Fprintf(w, "  %-14s %-10s tag %s  commit %s%s\n", dir, r.Provenance, r.Tag(), r.Commit, note)
		case rs.Newer != nil:
			fmt.Fprintf(w, "  %-14s %-10s %v%s\n", dir, "newer", rs.Newer, note)
		default:
			fmt.Fprintf(w, "  %-14s %-10s (%s)%s\n", dir, kbstate.ProvUnknown, rs.Reason, note)
		}
	}
	if listed == 0 {
		fmt.Fprintln(w, "  none")
	}

	fmt.Fprintln(w, "Retained copies:")
	retained := kbstate.ListRetained(root)
	if len(retained) == 0 {
		fmt.Fprintln(w, "  none")
	}
	for _, r := range retained {
		fmt.Fprintf(w, "  %s  %s  (%s)\n", r.Path, humanSize(r.Size), r.Kind)
	}
	if len(retained) > 0 {
		fmt.Fprintln(w, "kb never deletes these; removing one is your decision.")
	}
	return nil
}

// appsJSONVersion reads the "version" of one app from sites/apps.json.
func appsJSONVersion(root, appName string) string {
	data, err := os.ReadFile(filepath.Join(root, "sites", "apps.json"))
	if err != nil {
		return ""
	}
	var state map[string]struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &state) != nil {
		return ""
	}
	return strings.TrimSpace(state[appName].Version)
}
