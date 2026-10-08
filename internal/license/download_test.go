package license

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KB-Developpement/kb_pro_cli/internal/version"
)

const goodCommit = "0123456789abcdef0123456789abcdef01234567"

type recorded struct {
	URL     *url.URL
	Version string
	Auth    string
}

// stub is a license-server download stub that records every request.
type stub struct {
	mu      sync.Mutex
	reqs    []recorded
	headers map[string]string // response identity headers (nil entry = omitted)
	body    []byte
	status  int
	errKey  string
	srv     *httptest.Server
}

func newStub(t *testing.T) *stub {
	t.Helper()
	s := &stub{
		headers: map[string]string{
			"X-KB-Repository":  "KB-Developpement/kb_pro",
			"X-KB-Ref":         "v1.2.0",
			"X-KB-Commit":      goodCommit,
			"X-KB-Release-Tag": "v1.2.0",
		},
		body:   []byte("archive-bytes"),
		status: 200,
	}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.reqs = append(s.reqs, recorded{URL: r.URL, Version: r.Header.Get(CLIVersionHeader), Auth: r.Header.Get("Authorization")})
		s.mu.Unlock()
		if s.status != 200 {
			w.WriteHeader(s.status)
			_, _ = w.Write([]byte(`{"error":"` + s.errKey + `"}`))
			return
		}
		for k, v := range s.headers {
			if v != "" {
				w.Header().Set(k, v)
			}
		}
		_, _ = w.Write(s.body)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

func req() DownloadRequest {
	return DownloadRequest{App: "kb_pro", Repository: "kb_pro", Line: 1}
}

func TestDownloadSelectorsOnTheWire(t *testing.T) {
	cases := []struct {
		name  string
		req   DownloadRequest
		query url.Values
	}{
		{"line", DownloadRequest{App: "kb_pro", Repository: "kb_pro", Line: 1}, url.Values{"major": {"1"}}},
		{"ref", DownloadRequest{App: "kb_pro", Repository: "kb_pro", Line: 1, Ref: "v1.2.0"}, url.Values{"v": {"v1.2.0"}}},
		{"ref with slash", DownloadRequest{App: "kb_pro", Repository: "kb_pro", Line: 1, Ref: "release/1.0"}, url.Values{"v": {"release/1.0"}}},
		{"strict", DownloadRequest{App: "kb_pro", Repository: "kb_pro", Line: 1, Ref: "v1.2.0", Release: true}, url.Values{"v": {"v1.2.0"}, "release": {"1"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newStub(t)
			s.headers["X-KB-Ref"] = firstNonEmpty(c.req.Ref, "v1.2.0")
			s.headers["X-KB-Release-Tag"] = firstNonEmpty(c.req.Ref, "v1.2.0")
			dl, err := DownloadApp(context.Background(), s.srv.URL, "tok", c.req)
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(dl.Path)
			if s.count() != 1 {
				t.Fatalf("%d requests", s.count())
			}
			got := s.reqs[0].URL.Query()
			if len(got) != len(c.query) {
				t.Fatalf("query = %v, want exactly %v", got, c.query)
			}
			for k, v := range c.query {
				if got.Get(k) != v[0] {
					t.Errorf("query %s = %q, want %q", k, got.Get(k), v[0])
				}
			}
			if s.reqs[0].URL.Path != "/download/kb_pro" || s.reqs[0].Auth != "Bearer tok" {
				t.Errorf("path/auth = %s %s", s.reqs[0].URL.Path, s.reqs[0].Auth)
			}
		})
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func TestDownloadRefusesBeforeAnyRequest(t *testing.T) {
	s := newStub(t)
	cases := map[string]DownloadRequest{
		"line 0":               {App: "kb_pro", Repository: "kb_pro", Line: 0},
		"line 0 with a ref":    {App: "kb_pro", Repository: "kb_pro", Line: 0, Ref: "v1.0.0"},
		"line 0 strict":        {App: "kb_pro", Repository: "kb_pro", Line: 0, Ref: "v1.0.0", Release: true},
		"negative line":        {App: "kb_pro", Repository: "kb_pro", Line: -1},
		"strict without a tag": {App: "kb_pro", Repository: "kb_pro", Line: 1, Release: true},
		"invalid ref":          {App: "kb_pro", Repository: "kb_pro", Line: 1, Ref: "../../x"},
		"no repository":        {App: "kb_pro", Line: 1},
	}
	for name, r := range cases {
		if _, err := DownloadApp(context.Background(), s.srv.URL, "tok", r); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if n := s.count(); n != 0 {
		t.Fatalf("%d HTTP requests were sent, want 0", n)
	}
}

func TestDownloadVerifiesAndReturnsIdentity(t *testing.T) {
	s := newStub(t)
	s.body = []byte(strings.Repeat("x", 100000))
	dl, err := DownloadApp(context.Background(), s.srv.URL, "tok", req())
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(dl.Path)
	sum := sha256.Sum256(s.body)
	if dl.SHA256 != hex.EncodeToString(sum[:]) || dl.Size != int64(len(s.body)) {
		t.Errorf("sha/size = %s/%d", dl.SHA256, dl.Size)
	}
	if dl.Repository != "KB-Developpement/kb_pro" || dl.Ref != "v1.2.0" || dl.Commit != goodCommit || dl.Requested != "major=1" || dl.ReleaseTag != "" {
		t.Errorf("identity = %+v", dl)
	}
	data, _ := os.ReadFile(dl.Path)
	if len(data) != len(s.body) {
		t.Errorf("file has %d bytes", len(data))
	}
	fi, _ := os.Stat(dl.Path)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("archive mode = %v", fi.Mode().Perm())
	}
}

func TestDownloadRejectsBadIdentityHeaders(t *testing.T) {
	cases := map[string]func(h map[string]string){
		"missing commit":     func(h map[string]string) { delete(h, "X-KB-Commit") },
		"missing ref":        func(h map[string]string) { delete(h, "X-KB-Ref") },
		"missing repository": func(h map[string]string) { delete(h, "X-KB-Repository") },
		"39 hex commit":      func(h map[string]string) { h["X-KB-Commit"] = goodCommit[:39] },
		"41 hex commit":      func(h map[string]string) { h["X-KB-Commit"] = goodCommit + "0" },
		"uppercase commit":   func(h map[string]string) { h["X-KB-Commit"] = strings.ToUpper(goodCommit) },
		"newline in commit":  func(h map[string]string) { h["X-KB-Commit"] = goodCommit[:20] + "\n" + goodCommit[21:] },
		"64 hex commit":      func(h map[string]string) { h["X-KB-Commit"] = goodCommit + goodCommit[:24] },
		"bad repository":     func(h map[string]string) { h["X-KB-Repository"] = "other-org/kb_pro" },
		"other repository":   func(h map[string]string) { h["X-KB-Repository"] = "KB-Developpement/kb_compta" },
		"repository suffix":  func(h map[string]string) { h["X-KB-Repository"] = "KB-Developpement/kb_pro/x" },
		"bad ref":            func(h map[string]string) { h["X-KB-Ref"] = "../x" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := newStub(t)
			mutate(s.headers)
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			_, err := DownloadApp(context.Background(), s.srv.URL, "tok", req())
			var ce *CompatError
			if !errors.As(err, &ce) {
				t.Fatalf("err = %v, want *CompatError", err)
			}
			if !strings.Contains(err.Error(), "server-compatibility") {
				t.Errorf("message lacks the server-compatibility requirement: %v", err)
			}
			if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
				t.Errorf("a temp file was written before the headers were validated: %v", entries)
			}
		})
	}
}

func TestDownloadStrictNeedsReleaseTag(t *testing.T) {
	strict := DownloadRequest{App: "kb_pro", Repository: "kb_pro", Line: 1, Ref: "v1.2.0", Release: true}
	for name, tag := range map[string]string{"missing": "", "different": "v9.9.9", "malformed": "../x"} {
		t.Run(name, func(t *testing.T) {
			s := newStub(t)
			s.headers["X-KB-Release-Tag"] = tag
			_, err := DownloadApp(context.Background(), s.srv.URL, "tok", strict)
			var ce *CompatError
			if !errors.As(err, &ce) {
				t.Fatalf("err = %v, want *CompatError", err)
			}
		})
	}
	// A non-strict request ignores a release tag header.
	s := newStub(t)
	dl, err := DownloadApp(context.Background(), s.srv.URL, "tok", DownloadRequest{App: "kb_pro", Repository: "kb_pro", Line: 1, Ref: "v1.2.0"})
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(dl.Path)
	if dl.ReleaseTag != "" {
		t.Errorf("non-strict download claims release tag %q", dl.ReleaseTag)
	}
}

func TestDownloadHTTPErrors(t *testing.T) {
	s := newStub(t)
	s.status, s.errKey = 409, "release_not_published"
	_, err := DownloadApp(context.Background(), s.srv.URL, "tok", req())
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 409 || he.Key != "release_not_published" {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "release_not_published") || !strings.Contains(err.Error(), "409") {
		t.Errorf("message = %v", err)
	}
}

func TestDownloadAbortedBodyIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-KB-Repository", "KB-Developpement/kb_pro")
		w.Header().Set("X-KB-Ref", "v1.2.0")
		w.Header().Set("X-KB-Commit", goodCommit)
		w.Header().Set("Content-Length", "100000")
		_, _ = w.Write([]byte("short"))
		// returning with fewer bytes than promised: the client must notice
	}))
	defer srv.Close()
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	if _, err := DownloadApp(context.Background(), srv.URL, "tok", req()); err == nil {
		t.Fatal("a short body was accepted")
	}
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		t.Errorf("partial archive left behind: %v", entries)
	}
}

func TestDownloadResponseHeaderTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	old := ResponseHeaderTimeout
	ResponseHeaderTimeout = 150 * time.Millisecond
	defer func() { ResponseHeaderTimeout = old }()

	start := time.Now()
	_, err := DownloadApp(context.Background(), srv.URL, "tok", req())
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("stalled server: err=%v after %v", err, time.Since(start))
	}
}

func TestCLIVersionHeaderOnEveryRequest(t *testing.T) {
	old := version.Version
	defer func() { version.Version = old }()

	var mu sync.Mutex
	seen := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path] = r.Header.Get(CLIVersionHeader)
		mu.Unlock()
		switch r.URL.Path {
		case "/download/kb_pro":
			w.Header().Set("X-KB-Repository", "KB-Developpement/kb_pro")
			w.Header().Set("X-KB-Ref", "v1.2.0")
			w.Header().Set("X-KB-Commit", goodCommit)
			_, _ = w.Write([]byte("x"))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"t","expires_at":"2030-01-01T00:00:00Z"}`))
		}
	}))
	defer srv.Close()

	for _, v := range []string{"dev", "0.9.1"} {
		version.Version = v
		dl, err := DownloadApp(context.Background(), srv.URL, "tok", req())
		if err != nil {
			t.Fatal(err)
		}
		os.Remove(dl.Path)
		if _, err := Activate(srv.URL, "KEY", "fp"); err != nil {
			t.Fatal(err)
		}
		if res := Heartbeat(context.Background(), srv.URL, "tok", "fp"); res.Err != nil {
			t.Fatal(res.Err)
		}
		mu.Lock()
		for _, p := range []string{"/download/kb_pro", "/activate", "/heartbeat"} {
			if seen[p] != v {
				t.Errorf("version %q: %s carried %s=%q", v, p, CLIVersionHeader, seen[p])
			}
		}
		mu.Unlock()
	}
}
