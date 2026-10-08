package cli

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KB-Developpement/kb_pro_cli/internal/apps"
	"github.com/KB-Developpement/kb_pro_cli/internal/bench"
	"github.com/KB-Developpement/kb_pro_cli/internal/errlog"
	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
	"github.com/KB-Developpement/kb_pro_cli/internal/testutil"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "kb-errlog-")
	if err != nil {
		panic(err)
	}
	errlog.SetDir(dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// ── license-server stub ─────────────────────────────────────────────────────

type reqRecord struct {
	Method     string
	Path       string
	Query      url.Values
	CLIVersion string
	Auth       string
}

// dlServer is an httptest stand-in for the license server. It serves release
// archives built on the fly and records every request it receives.
type dlServer struct {
	t   *testing.T
	srv *httptest.Server

	mu   sync.Mutex
	reqs []reqRecord

	Delay    map[string]time.Duration // per app, before answering
	Status   map[string]int           // per app: answer with this status instead
	StatusK  map[string]string        // per app: the {"error": key} body for Status
	Omit     map[string]bool          // response headers to leave out
	Set      map[string]string        // response headers to override
	Cut      int                      // >0: cut the archive body after this many bytes, ending cleanly
	Files    map[string][]testutil.File
	Released map[string]bool   // "app@tag" -> a published release; default: v1.0.0, v1.1.0, v1.2.0
	CommitOf map[string]string // "app@ref" -> commit to report instead of the derived one
	LineTag  string            // tag served for major=N (default v1.2.0)
	Stall    map[string]bool   // never answer (until the test ends)
	done     chan struct{}

	inflight, MaxInflight int
}

func newDLServer(t *testing.T) *dlServer {
	t.Helper()
	s := &dlServer{
		t: t, Delay: map[string]time.Duration{}, Status: map[string]int{}, StatusK: map[string]string{},
		Omit: map[string]bool{}, Set: map[string]string{}, Files: map[string][]testutil.File{},
		Released: map[string]bool{}, Stall: map[string]bool{}, CommitOf: map[string]string{}, LineTag: "v1.2.0", done: make(chan struct{}),
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(func() { close(s.done); s.srv.Close() })
	return s
}

func commitFor(app, ref string) string {
	sum := sha1.Sum([]byte(app + "@" + ref))
	return hex.EncodeToString(sum[:])
}

// releaseFiles is the content of app at ref.
func releaseFiles(app apps.App, ref string) []testutil.File {
	pkg := app.PackageName()
	ver := strings.TrimPrefix(ref, "v")
	return []testutil.File{
		{Name: pkg + "/__init__.py", Body: fmt.Sprintf("__version__ = %q\n", ver)},
		{Name: pkg + "/hooks.py", Body: fmt.Sprintf("app_name = %q\napp_version = %q\n", pkg, ver)},
		{Name: "README.md", Body: "release " + ref + " of " + app.Name + "\n"},
		{Name: "requirements.txt", Body: "\n"},
		{Name: "scripts/run.sh", Body: "#!/bin/sh\necho " + ref + "\n", Mode: 0o755},
		{Name: "data/sub/file.txt", Body: "data of " + ref + "\n"},
	}
}

func (s *dlServer) archive(app, ref string) []byte {
	a, ok := apps.ByName(app)
	if !ok {
		s.t.Fatalf("stub: unknown app %s", app)
	}
	files, ok := s.Files[app+"@"+ref]
	if !ok {
		files = releaseFiles(a, ref)
	}
	_, data := testutil.TarGz(s.t, a.Repository()+"-"+commitFor(app, ref)[:7], files)
	return data
}

func (s *dlServer) published(app, tag string) bool {
	if len(s.Released) > 0 {
		return s.Released[app+"@"+tag]
	}
	return tag == "v1.0.0" || tag == "v1.1.0" || tag == "v1.2.0"
}

func (s *dlServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.reqs = append(s.reqs, reqRecord{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), CLIVersion: r.Header.Get(license.CLIVersionHeader), Auth: r.Header.Get("Authorization")})
	s.mu.Unlock()

	if r.URL.Path == "/releases/latest" {
		_, _ = w.Write([]byte(`{"tag_name":"v0.0.1","assets":[]}`))
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/download/") {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"t","expires_at":"2030-01-01T00:00:00Z"}`))
		return
	}
	app := strings.TrimPrefix(r.URL.Path, "/download/")
	q := r.URL.Query()
	s.mu.Lock()
	s.inflight++
	if s.inflight > s.MaxInflight {
		s.MaxInflight = s.inflight
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.inflight--
		s.mu.Unlock()
	}()
	if s.Stall[app] {
		select {
		case <-s.done:
		case <-r.Context().Done():
		}
		return
	}
	if d := s.Delay[app]; d > 0 {
		time.Sleep(d)
	}
	if st := s.Status[app]; st != 0 {
		w.WriteHeader(st)
		_, _ = w.Write([]byte(`{"error":"` + s.StatusK[app] + `"}`))
		return
	}
	var ref string
	switch {
	case q.Get("major") != "" && q.Get("v") == "":
		ref = s.LineTag
	case q.Get("v") != "":
		ref = q.Get("v")
	default:
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"conflicting_selectors"}`))
		return
	}
	strict := q.Get("release") == "1"
	if strict {
		switch {
		case strings.HasPrefix(ref, "draft"):
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"error":"release_not_published"}`))
			return
		case !s.published(app, ref):
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"version_not_found"}`))
			return
		}
	}
	a, _ := apps.ByName(app)
	h := map[string]string{
		"X-KB-Repository": "KB-Developpement/" + a.Repository(),
		"X-KB-Ref":        ref,
		"X-KB-Commit":     commitFor(app, ref),
	}
	if c, ok := s.CommitOf[app+"@"+ref]; ok {
		h["X-KB-Commit"] = c
	}
	if strict {
		h["X-KB-Release-Tag"] = ref
	}
	for k, v := range s.Set {
		h[k] = v
	}
	for k, v := range h {
		if !s.Omit[k] {
			w.Header().Set(k, v)
		}
	}
	body := s.archive(app, ref)
	if s.Cut > 0 && s.Cut < len(body) {
		body = body[:s.Cut]
	}
	_, _ = w.Write(body)
}

// downloads returns the recorded /download requests.
func (s *dlServer) downloads() []reqRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []reqRecord
	for _, r := range s.reqs {
		if strings.HasPrefix(r.Path, "/download/") {
			out = append(out, r)
		}
	}
	return out
}

func (s *dlServer) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

func (s *dlServer) all() []reqRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]reqRecord(nil), s.reqs...)
}

// ── bench fixture ───────────────────────────────────────────────────────────

const fakeBenchScript = `#!/bin/sh
log="${KB_BENCH_LOG:-/dev/null}"
echo "start $$ $*" >> "$log"
case "$*" in
  *list-apps*) echo "{\"site1\": [${KB_FAKE_INSTALLED:-\"frappe\"}]}" ;;
esac
if [ -n "$KB_FAKE_FAIL" ]; then
  case "$*" in
    *"$KB_FAKE_FAIL"*) echo "fake bench: injected failure" >&2; echo "end $$ $*" >> "$log"; exit 1 ;;
  esac
fi
/bin/sleep "${KB_FAKE_SLEEP:-0}"
echo "end $$ $*" >> "$log"
exit 0
`

// gitShimScript records its arguments and then runs the real git.
const gitShimScript = `#!/bin/sh
echo "$PWD :: $*" >> "$KB_GIT_LOG"
exec "$KB_REAL_GIT" "$@"
`

// benchH is a throw-away bench: a temp bench root, a fake `bench`, a git shim
// that logs argv, a license-server stub, and the environment that points the
// CLI at them. PATH holds only those fakes, so neither the real bench nor the
// developer's dev-server processes can be reached.
type benchH struct {
	t        *testing.T
	root     string
	home     string
	srv      *dlServer
	binDir   string
	benchLog string
	gitLog   string
}

func newBenchH(t *testing.T) *benchH {
	t.Helper()
	h := &benchH{t: t, root: t.TempDir(), home: t.TempDir(), srv: newDLServer(t), binDir: t.TempDir()}
	h.benchLog = filepath.Join(t.TempDir(), "bench.log")
	h.gitLog = filepath.Join(t.TempDir(), "git.log")

	for _, d := range []string{"apps", "sites/site1", "env/bin", "logs"} {
		must(t, os.MkdirAll(filepath.Join(h.root, d), 0o755))
	}
	write(t, filepath.Join(h.root, "sites", "apps.txt"), "frappe\n")
	write(t, filepath.Join(h.root, "sites", "apps.json"), "{}")
	write(t, filepath.Join(h.root, "sites", "site1", "site_config.json"), "{}")
	write(t, filepath.Join(h.root, "sites", "common_site_config.json"), `{"default_site":"site1"}`)
	writeExec(t, filepath.Join(h.root, "env", "bin", "python"), fakeBenchScript)
	writeExec(t, filepath.Join(h.binDir, "bench"), fakeBenchScript)
	writeExec(t, filepath.Join(h.binDir, "git"), gitShimScript)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}

	t.Setenv("KB_BENCH_ROOT", h.root)
	t.Setenv("HOME", h.home)
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("PATH", h.binDir)
	t.Setenv("KB_LICENSE_SERVER", h.srv.srv.URL)
	t.Setenv("KB_BENCH_LOG", h.benchLog)
	t.Setenv("KB_GIT_LOG", h.gitLog)
	t.Setenv("KB_REAL_GIT", realGit)
	t.Setenv("KB_FAKE_INSTALLED", `"frappe"`)
	testutil.IsolateGit(t)

	// No update check over the network: a fresh cache file means none is started.
	cache, _ := json.Marshal(updateCheckState{CheckedAt: time.Now().UTC(), Latest: "v0.0.1"})
	must(t, os.MkdirAll(filepath.Join(h.home, ".config", "kb"), 0o700))
	write(t, filepath.Join(h.home, ".config", "kb", ".update_check.json"), string(cache))
	oldAPI := githubReleasesAPI
	githubReleasesAPI = h.srv.srv.URL + "/releases/latest"
	t.Cleanup(func() { githubReleasesAPI = oldAPI })

	// License: everything is allowed; the token is a placeholder the stub accepts.
	oldSync, oldAllowed, oldToken, oldConc := syncLicenseFn, allowedSetFn, cachedTokenFn, downloadConcurrency
	syncLicenseFn = func(context.Context) error { return nil }
	allowedSetFn = func() map[string]bool {
		m := map[string]bool{apps.Framework.Name: true}
		for _, a := range apps.All {
			m[a.Name] = true
		}
		return m
	}
	cachedTokenFn = func() (string, error) { return "tok", nil }
	t.Cleanup(func() {
		syncLicenseFn, allowedSetFn, cachedTokenFn, downloadConcurrency = oldSync, oldAllowed, oldToken, oldConc
		bench.FinalizeHook = nil
	})

	orig := globalFlags
	globalFlags.NoInput = true
	t.Cleanup(func() { globalFlags = orig })
	h.seedFrappe("v1.0.5", true)
	return h
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(path), 0o755))
	must(t, os.WriteFile(path, []byte(body), 0o644))
}

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	must(t, os.WriteFile(path, []byte(body), 0o755))
}

func (h *benchH) appDir(dir string) string { return filepath.Join(h.root, "apps", dir) }

func (h *benchH) digest(dir string) string { return testutil.TreeDigest(h.t, h.appDir(dir)) }

func (h *benchH) benchDigest() string { return testutil.BenchDigest(h.t, h.root) }

func (h *benchH) app(name string) apps.App {
	a, ok := apps.ByName(name)
	if !ok {
		h.t.Fatalf("unknown app %s", name)
	}
	return a
}

// seed installs app at ref the way a previous kb run would have: files under
// apps/<dir>, an apps.txt line, an apps.json entry and, when receipt is true, a
// valid receipt.
func (h *benchH) seed(name, ref string, receipt bool) {
	h.t.Helper()
	a := h.app(name)
	for _, f := range releaseFiles(a, ref) {
		p := filepath.Join(h.appDir(a.Dir()), f.Name)
		must(h.t, os.MkdirAll(filepath.Dir(p), 0o755))
		mode := os.FileMode(0o644)
		if f.Mode != 0 {
			mode = os.FileMode(f.Mode)
		}
		must(h.t, os.WriteFile(p, []byte(f.Body), mode))
	}
	if name != apps.Framework.Name {
		txt := filepath.Join(h.root, "sites", "apps.txt")
		cur, _ := os.ReadFile(txt)
		if !strings.Contains(string(cur), a.PackageName()+"\n") {
			write(h.t, txt, string(cur)+a.PackageName()+"\n")
		}
	}
	must(h.t, bench.SyncAppState(a.Dir(), a.PackageName()))
	if receipt {
		h.writeReceipt(name, ref, kbstate.ProvInstalled)
	}
}

func (h *benchH) seedFrappe(ref string, receipt bool) { h.seed(apps.Framework.Name, ref, receipt) }

func (h *benchH) writeReceipt(name, ref, prov string) {
	h.t.Helper()
	a := h.app(name)
	tag := ref
	r := &kbstate.Receipt{
		SchemaVersion: 1, App: a.Name, Directory: a.Dir(), Package: a.PackageName(),
		Repository: "KB-Developpement/" + a.Repository(), Line: "1", Requested: "major=1", Ref: ref,
		Commit: commitFor(name, ref), ReleaseTag: &tag,
		ArchiveSHA256: testutil.Sha256Hex(h.srv.archive(name, ref)), InstalledAt: "2026-01-01T00:00:00Z",
		State: kbstate.StateArchive, Provenance: prov,
	}
	must(h.t, kbstate.WriteReceipt(h.root, r))
}

func (h *benchH) receipt(dir string) kbstate.ReceiptState { return kbstate.ReadReceipt(h.root, dir) }

func (h *benchH) journal() *kbstate.Journal {
	h.t.Helper()
	js := kbstate.ReadJournal(h.root)
	if js.Journal == nil {
		h.t.Fatalf("no readable journal: %+v", js)
	}
	return js.Journal
}

func (h *benchH) appsTxt() []string {
	data, err := os.ReadFile(filepath.Join(h.root, "sites", "apps.txt"))
	must(h.t, err)
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// benchLogLines returns the fake bench's log.
func (h *benchH) benchLogLines() []string {
	data, _ := os.ReadFile(h.benchLog)
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (h *benchH) gitCalls() []string {
	data, _ := os.ReadFile(h.gitLog)
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// exec runs the real command tree (cobra parsing, startup hooks and all) and
// returns everything written to stdout and stderr, and the error.
func (h *benchH) exec(args ...string) (string, error) {
	h.t.Helper()
	var err error
	out := capture(h.t, func() {
		root := newRootCmd()
		root.SetArgs(append(args, "--no-input"))
		root.SetOut(os.Stdout)
		root.SetErr(os.Stderr)
		err = root.Execute()
		waitForUpdateCheck()
	})
	return out, err
}

// capture redirects os.Stdout and os.Stderr to a pipe while fn runs.
func capture(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	must(t, err)
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() {
		os.Stdout, os.Stderr = oldOut, oldErr
	}()
	fn()
	_ = w.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	return <-done
}

// holdLock starts a subprocess that holds .kb/lock and returns its PID.
func (h *benchH) holdLock() int {
	return startLockHolder(h.t, h.root)
}
