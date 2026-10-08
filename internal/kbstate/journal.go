package kbstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/KB-Developpement/kb_pro_cli/internal/fault"
	"github.com/KB-Developpement/kb_pro_cli/internal/fsutil"
)

// Journal steps, in order (contracts 6.3).
const (
	StepPlanned    = "planned"
	StepDownloaded = "downloaded"
	StepStaged     = "staged"
	StepSwapped    = "swapped"
	StepMigrated   = "migrated"
	StepFinalized  = "finalized"
)

// Journal ops.
const (
	OpSwap     = "swap"
	OpRegister = "register"
	OpAdopt    = "adopt"
)

// LegacyMove records a leftover .kb-old or .kb-new that was moved out of apps/.
type LegacyMove struct {
	Original    string `json:"original"`
	Destination string `json:"destination"`
	MovedAt     string `json:"moved_at"`
}

// Journal is .kb/journal.json: the one in-flight (or last completed)
// transaction. A finished transaction leaves the file in place at step
// "finalized", which means nothing is pending; the next transaction overwrites it.
type Journal struct {
	SchemaVersion  int          `json:"schema_version"`
	Op             string       `json:"op"`
	Command        string       `json:"command"`
	App            string       `json:"app"`
	Directory      string       `json:"directory"`
	Step           string       `json:"step"`
	StartedAt      string       `json:"started_at"`
	StagedFiles    int          `json:"staged_files"`
	Paths          []string     `json:"paths"`
	PendingReceipt *Receipt     `json:"pending_receipt"`
	Legacy         []LegacyMove `json:"legacy"`

	// OldPath and NewPath are the backup of the replaced tree and the live
	// tree, so an operator reading a refusal knows what to move where.
	OldPath string `json:"old_path,omitempty"`
	NewPath string `json:"new_path,omitempty"`
	// HadReceipt says the replaced tree had a valid receipt when the
	// transaction started; it decides whether the old tree is deleted or kept.
	HadReceipt bool `json:"had_receipt"`
	// StockConversion marks the one case where a Git checkout is replaced.
	StockConversion bool `json:"stock_conversion,omitempty"`
	// Retained is where the replaced tree was kept (pre-receipt copy).
	Retained string `json:"retained,omitempty"`
	// Aborted marks a transaction that was rolled back cleanly; Step is then
	// "finalized" because nothing is pending.
	Aborted bool   `json:"aborted,omitempty"`
	Error   string `json:"error,omitempty"`

	root string
}

// NewJournal starts a journal for a bench.
func NewJournal(root string) *Journal {
	return &Journal{
		SchemaVersion: SupportedSchema,
		Step:          StepPlanned,
		StartedAt:     time.Now().UTC().Format(time.RFC3339),
		Paths:         []string{},
		Legacy:        []LegacyMove{},
		root:          root,
	}
}

// Pending reports whether the journal describes an unfinished transaction.
func (j *Journal) Pending() bool { return j != nil && j.Step != "" && j.Step != StepFinalized }

// Save writes the journal durably (0600, atomic rename, directory fsync).
func (j *Journal) Save() error {
	if err := EnsureKB(j.root); err != nil {
		return err
	}
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := fsutil.WriteFileDurable(JournalPath(j.root), data, 0o600); err != nil {
		return fmt.Errorf("write journal: %w", err)
	}
	return nil
}

// Advance records the next step and then offers the matching kill point.
func (j *Journal) Advance(step string) error {
	j.Step = step
	if err := j.Save(); err != nil {
		return err
	}
	fault.Hit("after-" + step)
	return nil
}

// AddPath records a staging or temp path before it is created. Recovery may
// discard only recorded paths.
func (j *Journal) AddPath(p string) error {
	for _, have := range j.Paths {
		if have == p {
			return nil
		}
	}
	j.Paths = append(j.Paths, p)
	return j.Save()
}

// Abort closes a transaction that was rolled back cleanly. Nothing is pending.
func (j *Journal) Abort(cause error) error {
	j.Step = StepFinalized
	j.Aborted = true
	if cause != nil {
		j.Error = cause.Error()
	}
	return j.Save()
}

// JournalState is the outcome of reading the journal.
type JournalState struct {
	Journal   *Journal
	Missing   bool
	Malformed string       // non-empty: why the file cannot be used
	Newer     *SchemaError // non-nil: written by a newer kb
}

// ReadJournal reads .kb/journal.json without changing anything.
func ReadJournal(root string) JournalState {
	path := JournalPath(root)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return JournalState{Missing: true}
		}
		return JournalState{Malformed: "unreadable: " + err.Error()}
	}
	if v, ok := peekSchema(data); ok && v > SupportedSchema {
		return JournalState{Newer: &SchemaError{Path: path, Found: v, Supported: SupportedSchema}}
	}
	var j Journal
	if err := json.Unmarshal(data, &j); err != nil {
		return JournalState{Malformed: "not valid JSON: " + err.Error()}
	}
	if j.SchemaVersion < 1 {
		return JournalState{Malformed: "schema_version is missing"}
	}
	switch j.Step {
	case StepPlanned, StepDownloaded, StepStaged, StepSwapped, StepMigrated, StepFinalized:
	default:
		return JournalState{Malformed: fmt.Sprintf("unknown step %q", j.Step)}
	}
	j.root = root
	return JournalState{Journal: &j}
}

// Attach binds a journal read from disk to a bench root so it can be saved.
func (j *Journal) Attach(root string) { j.root = root }
