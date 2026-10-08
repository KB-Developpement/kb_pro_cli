package kbstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/KB-Developpement/kb_pro_cli/internal/fsutil"
	"github.com/KB-Developpement/kb_pro_cli/internal/ident"
)

// Provenance values (contracts 6.1). "unknown" is reported for a missing or
// unreadable receipt and is never written.
const (
	ProvInstalled   = "installed"
	ProvAdopted     = "adopted"
	ProvMaintenance = "maintenance"
	ProvUnknown     = "unknown"
)

// StateArchive is the only receipt state Phase 0 writes.
const StateArchive = "archive"

// AdoptInfo is the extra object on an adopted receipt.
type AdoptInfo struct {
	ComparedFiles   int      `json:"compared_files"`
	Exclusions      []string `json:"exclusions"`
	CandidatesTried []string `json:"candidates_tried"`
}

// Receipt is .kb/apps/<directory>.json: where an installed app's source came
// from. It is provenance, not a signature, and it lives outside the replaceable
// app directory.
type Receipt struct {
	SchemaVersion int        `json:"schema_version"`
	App           string     `json:"app"`
	Directory     string     `json:"directory"`
	Package       string     `json:"package"`
	Repository    string     `json:"repository"`
	Line          string     `json:"line"`
	Requested     string     `json:"requested"`
	Ref           string     `json:"ref"`
	Commit        string     `json:"commit"`
	ReleaseTag    *string    `json:"release_tag"`
	ArchiveSHA256 string     `json:"archive_sha256"`
	InstalledAt   string     `json:"installed_at"`
	State         string     `json:"state"`
	SessionID     *string    `json:"session_id"`
	Provenance    string     `json:"provenance"`
	Adopt         *AdoptInfo `json:"adopt,omitempty"`
}

// Tag is the release tag when verified, else the resolved ref.
func (r *Receipt) Tag() string {
	if r.ReleaseTag != nil && *r.ReleaseTag != "" {
		return *r.ReleaseTag
	}
	return r.Ref
}

// Validate checks every field a reader relies on. directory is the file the
// receipt was read from (apps/<directory>.json), which must agree with it.
func (r *Receipt) Validate(directory string) error {
	switch {
	case r.SchemaVersion < 1:
		return errors.New("schema_version is missing or below 1")
	case r.App == "":
		return errors.New("app is empty")
	case r.Directory == "" || (directory != "" && r.Directory != directory):
		return fmt.Errorf("directory %q does not match the receipt file %q", r.Directory, directory)
	case r.Package == "":
		return errors.New("package is empty")
	case !ident.RepositoryRE.MatchString(r.Repository):
		return errors.New("repository is not KB-Developpement/<repo>")
	case r.Line == "":
		return errors.New("line is empty")
	case r.Ref == "" || !ident.ValidRef(r.Ref):
		return errors.New("ref is empty or not a valid git ref")
	case !ident.CommitRE.MatchString(r.Commit):
		return errors.New("commit is not 40 lowercase hex")
	case !ident.SHA256RE.MatchString(r.ArchiveSHA256):
		return errors.New("archive_sha256 is not 64 lowercase hex")
	case r.ReleaseTag != nil && !ident.ValidRef(*r.ReleaseTag):
		return errors.New("release_tag is not a valid git ref")
	case r.State == "":
		return errors.New("state is empty")
	}
	switch r.Provenance {
	case ProvInstalled, ProvAdopted, ProvMaintenance:
	default:
		return fmt.Errorf("provenance %q is not installed, adopted or maintenance", r.Provenance)
	}
	if _, err := time.Parse(time.RFC3339, r.InstalledAt); err != nil {
		return errors.New("installed_at is not RFC3339")
	}
	return nil
}

// SchemaError reports a .kb/*.json file written by a newer kb.
type SchemaError struct {
	Path      string
	Found     int
	Supported int
}

func (e *SchemaError) Error() string {
	return fmt.Sprintf("%s has schema_version %d but this kb supports %d — it was written by a newer kb; update kb (kb update) before changing this bench", e.Path, e.Found, e.Supported)
}

// ReceiptState is the outcome of reading one receipt.
type ReceiptState struct {
	Path    string
	Receipt *Receipt     // non-nil only for a valid receipt
	Reason  string       // why the provenance is unknown
	Newer   *SchemaError // non-nil when the file has a higher schema_version
	Raw     []byte
}

// Valid reports whether a trusted receipt was read.
func (s ReceiptState) Valid() bool { return s.Receipt != nil }

// Provenance is the receipt's provenance, or "unknown".
func (s ReceiptState) Provenance() string {
	if s.Receipt != nil {
		return s.Receipt.Provenance
	}
	return ProvUnknown
}

// ReadReceipt reads and validates .kb/apps/<directory>.json. A missing,
// truncated, non-JSON, schema-invalid or tampered receipt is provenance
// "unknown" with a reason; it is not an error and does not stop an ordinary
// command. A receipt with a higher schema_version is reported in Newer so the
// caller refuses to mutate the bench.
func ReadReceipt(root, directory string) ReceiptState {
	path := ReceiptPath(root, directory)
	st := ReceiptState{Path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			st.Reason = "no receipt"
		} else {
			st.Reason = "receipt unreadable: " + err.Error()
		}
		return st
	}
	st.Raw = data
	if v, ok := peekSchema(data); ok && v > SupportedSchema {
		st.Newer = &SchemaError{Path: path, Found: v, Supported: SupportedSchema}
		st.Reason = fmt.Sprintf("receipt has schema_version %d (newer than supported %d)", v, SupportedSchema)
		return st
	}
	var r Receipt
	if err := json.Unmarshal(data, &r); err != nil {
		st.Reason = "receipt is not valid JSON: " + err.Error()
		return st
	}
	if err := migrateReceipt(&r); err != nil {
		st.Reason = err.Error()
		return st
	}
	if err := r.Validate(directory); err != nil {
		st.Reason = "receipt is invalid: " + err.Error()
		return st
	}
	st.Receipt = &r
	return st
}

// migrateReceipt upgrades an older receipt in memory. Only version 1 exists;
// the hook is here so a later version adds its step instead of a new reader.
func migrateReceipt(r *Receipt) error {
	switch {
	case r.SchemaVersion == SupportedSchema:
		return nil
	case r.SchemaVersion < 1:
		return errors.New("receipt has no schema_version")
	}
	return nil
}

// WriteReceipt validates r and writes it durably to .kb/apps/<directory>.json
// (0600), creating .kb and .kb/apps at 0700.
func WriteReceipt(root string, r *Receipt) error {
	if err := r.Validate(r.Directory); err != nil {
		return fmt.Errorf("refusing to write an invalid receipt for %s: %w", r.Directory, err)
	}
	if r.SchemaVersion > SupportedSchema {
		return &SchemaError{Path: ReceiptPath(root, r.Directory), Found: r.SchemaVersion, Supported: SupportedSchema}
	}
	if err := EnsureTree(root, ReceiptsDir(root)); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return fsutil.WriteFileDurable(ReceiptPath(root, r.Directory), data, 0o600)
}

// RemoveReceipt deletes .kb/apps/<directory>.json. It is called when kb removes
// the app itself, so a stale receipt cannot vouch for a different tree later.
func RemoveReceipt(root, directory string) error {
	err := os.Remove(ReceiptPath(root, directory))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// peekSchema reads only the top-level schema_version of a JSON document.
func peekSchema(data []byte) (int, bool) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return 0, false
	}
	raw, ok := probe["schema_version"]
	if !ok {
		return 0, false
	}
	var n json.Number
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&n); err != nil {
		return 0, false
	}
	i, err := n.Int64()
	if err != nil {
		return 0, false
	}
	return int(i), true
}

// CheckSchemas refuses when any .kb/*.json file (journal, receipts and the
// files later phases add) was written by a newer kb. Every mutating command
// calls it under the lock; status and other readers do not.
func CheckSchemas(root string) error {
	kb := Dir(root)
	var files []string
	for _, name := range []string{"journal.json", "bench-id"} {
		files = append(files, filepath.Join(kb, name))
	}
	files = append(files, filepath.Join(kb, "maintenance", "session.json"))
	if matches, err := filepath.Glob(filepath.Join(ReceiptsDir(root), "*.json")); err == nil {
		files = append(files, matches...)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if v, ok := peekSchema(data); ok && v > SupportedSchema {
			return &SchemaError{Path: f, Found: v, Supported: SupportedSchema}
		}
	}
	return nil
}
