package kbstate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func goodReceipt() *Receipt {
	tag := "v1.2.0"
	return &Receipt{
		SchemaVersion: 1,
		App:           "kb_pro",
		Directory:     "kb_pro",
		Package:       "kb_pro",
		Repository:    "KB-Developpement/kb_pro",
		Line:          "1",
		Requested:     "major=1",
		Ref:           "v1.2.0",
		Commit:        strings.Repeat("a", 40),
		ReleaseTag:    &tag,
		ArchiveSHA256: strings.Repeat("b", 64),
		InstalledAt:   "2026-10-08T10:00:00Z",
		State:         StateArchive,
		Provenance:    ProvInstalled,
	}
}

func TestReceiptRoundTripModes(t *testing.T) {
	root := t.TempDir()
	if err := WriteReceipt(root, goodReceipt()); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{Dir(root): 0o700, ReceiptsDir(root): 0o700, ReceiptPath(root, "kb_pro"): 0o600} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode = %v, want %v", path, fi.Mode().Perm(), want)
		}
	}
	st := ReadReceipt(root, "kb_pro")
	if !st.Valid() || st.Provenance() != ProvInstalled || st.Receipt.Tag() != "v1.2.0" {
		t.Fatalf("ReadReceipt = %+v", st)
	}
	var raw map[string]any
	data, _ := os.ReadFile(ReceiptPath(root, "kb_pro"))
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"schema_version", "app", "directory", "package", "repository", "line", "requested", "ref", "commit", "release_tag", "archive_sha256", "installed_at", "state", "session_id", "provenance"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("receipt has no %q field", k)
		}
	}
}

// Truncated, non-JSON, schema-invalid or tampered receipts are provenance
// unknown with a reason, never an error; unknown fields are ignored.
func TestReceiptUnknownCases(t *testing.T) {
	good, _ := json.Marshal(goodReceipt())
	mutate := func(f func(m map[string]any)) []byte {
		var m map[string]any
		_ = json.Unmarshal(good, &m)
		f(m)
		b, _ := json.Marshal(m)
		return b
	}
	cases := map[string][]byte{
		"truncated":        good[:len(good)/2],
		"not json":         []byte("hello"),
		"empty":            nil,
		"commit 39 hex":    mutate(func(m map[string]any) { m["commit"] = strings.Repeat("a", 39) }),
		"commit uppercase": mutate(func(m map[string]any) { m["commit"] = strings.Repeat("A", 40) }),
		"sha not 64":       mutate(func(m map[string]any) { m["archive_sha256"] = strings.Repeat("b", 63) }),
		"bad provenance":   mutate(func(m map[string]any) { m["provenance"] = "magic" }),
		"wrong directory":  mutate(func(m map[string]any) { m["directory"] = "other" }),
		"no schema":        mutate(func(m map[string]any) { delete(m, "schema_version") }),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			_ = os.MkdirAll(ReceiptsDir(root), 0o700)
			if err := os.WriteFile(ReceiptPath(root, "kb_pro"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			st := ReadReceipt(root, "kb_pro")
			if st.Valid() || st.Newer != nil || st.Provenance() != ProvUnknown || st.Reason == "" {
				t.Fatalf("ReadReceipt = %+v, want unknown with a reason", st)
			}
		})
	}

	t.Run("unknown fields ignored", func(t *testing.T) {
		root := t.TempDir()
		_ = os.MkdirAll(ReceiptsDir(root), 0o700)
		data := mutate(func(m map[string]any) { m["from_the_future"] = []int{1, 2} })
		_ = os.WriteFile(ReceiptPath(root, "kb_pro"), data, 0o600)
		if st := ReadReceipt(root, "kb_pro"); !st.Valid() {
			t.Fatalf("receipt with an unknown field was rejected: %s", st.Reason)
		}
	})
	t.Run("missing", func(t *testing.T) {
		if st := ReadReceipt(t.TempDir(), "kb_pro"); st.Valid() || st.Reason != "no receipt" {
			t.Fatalf("missing receipt: %+v", st)
		}
	})
}

func TestSchemaVersionAbove1(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(ReceiptsDir(root), 0o700)
	data := []byte(`{"schema_version": 2, "anything": "else"}`)
	path := ReceiptPath(root, "kb_pro")
	_ = os.WriteFile(path, data, 0o600)

	st := ReadReceipt(root, "kb_pro")
	if st.Newer == nil || st.Newer.Found != 2 || st.Newer.Supported != 1 {
		t.Fatalf("ReadReceipt.Newer = %+v", st.Newer)
	}
	err := CheckSchemas(root)
	var se *SchemaError
	if !errors.As(err, &se) || se.Path != path {
		t.Fatalf("CheckSchemas = %v, want a SchemaError naming %s", err, path)
	}
	if !strings.Contains(err.Error(), "2") || !strings.Contains(err.Error(), "1") {
		t.Errorf("message does not name both versions: %v", err)
	}
	// Writing over it is refused too.
	r := goodReceipt()
	r.SchemaVersion = 2
	if err := WriteReceipt(root, r); err == nil {
		t.Error("WriteReceipt wrote a schema_version 2 receipt")
	}
	// the journal and bench-id are covered
	for _, name := range []string{"journal.json", "bench-id"} {
		root := t.TempDir()
		_ = os.MkdirAll(Dir(root), 0o700)
		_ = os.WriteFile(filepath.Join(Dir(root), name), []byte(`{"schema_version": 3}`), 0o600)
		if CheckSchemas(root) == nil {
			t.Errorf("CheckSchemas ignored a version-3 %s", name)
		}
	}
	if err := CheckSchemas(t.TempDir()); err != nil {
		t.Errorf("CheckSchemas on an empty bench: %v", err)
	}
}

func TestExclusions(t *testing.T) {
	cases := []struct {
		rel   string
		isDir bool
		want  bool
	}{
		{"kb_pro.egg-info", true, true},
		{"kb_pro.egg-info/PKG-INFO", false, true},
		{"kb_pro/__pycache__", true, true},
		{"kb_pro/__pycache__/x.cpython-312.pyc", false, true},
		{"kb_pro/a.pyc", false, true},
		{"node_modules", true, true},
		{"kb_pro/public/node_modules/x/y.js", false, true},
		{"kb_pro/public/node_modules", false, true}, // bench build's symlink to ../../node_modules
		{"kb_pro/public/dist", true, true},
		{"kb_pro/public/dist/app.js", false, true},
		{"kb_pro/public/js/app.js", false, false},
		{"other/public/dist/app.js", false, false},
		{".DS_Store", false, true},
		{"kb_pro/.DS_Store", false, true},
		{"kb_pro/hooks.py", false, false},
		{"notes.egg-info", false, false}, // a file named like the pattern is not the directory pattern
		{".env", false, false},
		{"kb_pro/a.swp", false, false},
	}
	for _, c := range cases {
		if got := Excluded(c.rel, c.isDir, "kb_pro"); got != c.want {
			t.Errorf("Excluded(%q, dir=%v) = %v, want %v", c.rel, c.isDir, got, c.want)
		}
	}
	if got := Exclusions("kb_pro"); len(got) != 6 || got[4] != "kb_pro/public/dist/" {
		t.Errorf("Exclusions = %v", got)
	}
}

func TestCountEntriesAppliesExclusions(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"a.py", "kb_pro/hooks.py", "kb_pro/__pycache__/h.pyc", "kb_pro.egg-info/PKG-INFO", "node_modules/x/y.js", "kb_pro/public/dist/b.js", ".DS_Store"} {
		p := filepath.Join(root, f)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte("x"), 0o644)
	}
	got, err := CountEntries(root, "kb_pro")
	if err != nil {
		t.Fatal(err)
	}
	// a.py, kb_pro/, kb_pro/hooks.py, kb_pro/public/ -> 4 (dist is excluded)
	if got != 4 {
		t.Fatalf("CountEntries = %d, want 4", got)
	}
}

func TestOwnerCheck(t *testing.T) {
	root := t.TempDir()
	if err := CheckBenchOwner(root); err != nil {
		t.Fatalf("owner refused its own bench: %v", err)
	}
	orig := Geteuid
	defer func() { Geteuid = orig }()
	Geteuid = func() int { return os.Geteuid() + 1 }
	err := CheckBenchOwner(root)
	if err == nil || !strings.Contains(err.Error(), "bench user") {
		t.Fatalf("CheckBenchOwner = %v, want a run-as-bench-user refusal", err)
	}
}

func TestJournalSaveReadAndStates(t *testing.T) {
	root := t.TempDir()
	if js := ReadJournal(root); !js.Missing {
		t.Fatalf("empty bench: %+v", js)
	}
	j := NewJournal(root)
	j.Command, j.App, j.Directory, j.Op = "add", "kb_pro", "kb_pro", OpRegister
	if err := j.Advance(StepStaged); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(JournalPath(root))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("journal mode: %v %v", fi, err)
	}
	js := ReadJournal(root)
	if js.Journal == nil || !js.Journal.Pending() || js.Journal.Step != StepStaged || js.Journal.SchemaVersion != 1 {
		t.Fatalf("ReadJournal = %+v", js)
	}
	if err := js.Journal.Abort(errors.New("x")); err != nil {
		t.Fatal(err)
	}
	if js := ReadJournal(root); js.Journal.Pending() || !js.Journal.Aborted {
		t.Fatalf("aborted journal still pending: %+v", js.Journal)
	}

	_ = os.WriteFile(JournalPath(root), []byte("{"), 0o600)
	if js := ReadJournal(root); js.Malformed == "" {
		t.Error("malformed journal not reported")
	}
	_ = os.WriteFile(JournalPath(root), []byte(`{"schema_version":2,"step":"swapped"}`), 0o600)
	if js := ReadJournal(root); js.Newer == nil {
		t.Error("newer journal not reported")
	}
	_ = os.WriteFile(JournalPath(root), []byte(`{"schema_version":1,"step":"bogus"}`), 0o600)
	if js := ReadJournal(root); js.Malformed == "" {
		t.Error("unknown step not reported")
	}
}

func TestListRetained(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(LegacyDir(root), "kb_pro-kb-old-20261008T100000Z")
	b := filepath.Join(RecoveryDir(root), "kb_pro-pre-receipt-20261008T100000Z")
	for _, d := range []string{a, b, filepath.Join(RecoveryDir(root), "unrelated")} {
		_ = os.MkdirAll(d, 0o700)
		_ = os.WriteFile(filepath.Join(d, "f"), []byte("12345"), 0o644)
	}
	got := ListRetained(root)
	if len(got) != 2 {
		t.Fatalf("ListRetained = %+v, want the legacy and the pre-receipt copy only", got)
	}
	for _, r := range got {
		if r.Size != 5 {
			t.Errorf("%s size = %d, want 5", r.Path, r.Size)
		}
	}
}

func TestCleanTempFilesRemovesOnlyCrashLeftovers(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(ReceiptsDir(root), 0o700)
	leftovers := []string{filepath.Join(Dir(root), ".journal.json.tmp-123"), filepath.Join(ReceiptsDir(root), ".kb_pro.json.tmp-9")}
	keep := []string{JournalPath(root), ReceiptPath(root, "kb_pro"), filepath.Join(Dir(root), "lock"), filepath.Join(Dir(root), "notes.tmp-1")}
	for _, p := range append(append([]string{}, leftovers...), keep...) {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	CleanTempFiles(root)
	for _, p := range leftovers {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s survived", p)
		}
	}
	for _, p := range keep {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed", p)
		}
	}
}
