package license

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KB-Developpement/kb_pro_cli/internal/version"
)

const otherCommit = "fedcba9876543210fedcba9876543210fedcba98"

// serveApps starts a stub /apps endpoint and returns its URL and the last
// request it saw.
func serveApps(t *testing.T, status int, body string) (string, *http.Request) {
	t.Helper()
	last := new(http.Request)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*last = *r.Clone(context.Background())
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, last
}

func TestFetchAppsOK(t *testing.T) {
	old := version.Version
	version.Version = "v0.10.0"
	defer func() { version.Version = old }()

	body := `{"checked_at":"2026-10-18T10:00:00Z","apps":[
	  {"app":"kb_pro","default_line":1,"error":"","lines":[
	    {"line":1,"tag":"v1.2.0","commit":"` + goodCommit + `","published_at":"2026-10-01T00:00:00Z","error":"","stale":true},
	    {"line":2,"tag":"","commit":"","error":"line_not_found"}]},
	  {"app":"kb_print","default_line":0,"error":"line_not_found","lines":[]}]}`
	url, last := serveApps(t, 200, body)

	got, err := FetchApps(context.Background(), url+"/", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if last.Method != http.MethodGet || last.URL.Path != "/apps" {
		t.Errorf("request = %s %s, want GET /apps", last.Method, last.URL.Path)
	}
	if last.Header.Get("Authorization") != "Bearer tok" {
		t.Errorf("Authorization = %q", last.Header.Get("Authorization"))
	}
	if last.Header.Get(CLIVersionHeader) != "v0.10.0" {
		t.Errorf("%s = %q", CLIVersionHeader, last.Header.Get(CLIVersionHeader))
	}
	if got.CheckedAt != "2026-10-18T10:00:00Z" || len(got.Apps) != 2 {
		t.Fatalf("response = %+v", got)
	}
	e, ok := got.App("kb_pro")
	if !ok || e.DefaultLine != 1 {
		t.Fatalf("kb_pro entry = %+v", e)
	}
	l, ok := e.Line(1)
	if !ok || l.Tag != "v1.2.0" || l.Commit != goodCommit || !l.Stale || l.Error != "" {
		t.Errorf("line 1 = %+v", l)
	}
	if l2, _ := e.Line(2); l2.Error != "line_not_found" || l2.Tag != "" {
		t.Errorf("line 2 = %+v", l2)
	}
	if pe, _ := got.App("kb_print"); pe.Error != "line_not_found" {
		t.Errorf("kb_print = %+v", pe)
	}
}

func TestFetchAppsNotFoundIsUnsupported(t *testing.T) {
	url, _ := serveApps(t, 404, "404 page not found")
	_, err := FetchApps(context.Background(), url, "tok")
	if !errors.Is(err, ErrAppsUnsupported) {
		t.Fatalf("err = %v, want ErrAppsUnsupported", err)
	}
	if !strings.Contains(err.Error(), "does not support version check") {
		t.Errorf("message = %q", err)
	}
}

func TestFetchAppsHTTPError(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		key    string
	}{
		{"401 key", 401, `{"error":"invalid_token"}`, "invalid_token"},
		{"402 key", 402, `{"error":"token_expired"}`, "token_expired"},
		{"429 key", 429, `{"error":"rate_limited"}`, "rate_limited"},
		{"500 no json", 500, `oops`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			url, _ := serveApps(t, tc.status, tc.body)
			_, err := FetchApps(context.Background(), url, "tok")
			var he *HTTPError
			if !errors.As(err, &he) {
				t.Fatalf("err = %v, want *HTTPError", err)
			}
			if he.Status != tc.status || he.Key != tc.key {
				t.Errorf("HTTPError = %+v, want status %d key %q", he, tc.status, tc.key)
			}
			if errors.Is(err, ErrAppsUnsupported) {
				t.Error("a non-404 must not be ErrAppsUnsupported")
			}
		})
	}
}

func TestFetchAppsMalformedEntries(t *testing.T) {
	body := `{"checked_at":"x","apps":[
	  {"app":"kb_pro","default_line":1,"lines":[
	    {"line":1,"tag":"v1.0.0","commit":"` + goodCommit + `"},
	    {"line":2,"tag":"bad tag","commit":"` + goodCommit + `"},
	    {"line":3,"tag":"v1.0.0","commit":"ABC"},
	    {"line":0,"tag":"v1.0.0","commit":"` + goodCommit + `"},
	    {"line":4,"tag":"v1.0.0","commit":"` + goodCommit + `","error":"upstream_error"},
	    {"line":5,"tag":"","commit":"","error":"Not A Key!"},
	    {"line":6,"tag":"v1.0.0","commit":"` + goodCommit + `"},
	    {"line":6,"tag":"v9.9.9","commit":"` + otherCommit + `"},
	    {"line":7,"tag":"","commit":""}]},
	  {"app":"kb_dup","lines":[]},
	  {"app":"kb_dup","lines":[]},
	  {"app":"../etc","lines":[]},
	  {"app":"","lines":[]},
	  {"app":"kb_bad","error":"BAD KEY","lines":[]}]}`
	url, _ := serveApps(t, 200, body)
	got, err := FetchApps(context.Background(), url, "tok")
	if err != nil {
		t.Fatal(err)
	}

	e, ok := got.App("kb_pro")
	if !ok {
		t.Fatal("kb_pro missing")
	}
	for _, n := range []int{2, 3, 4, 5, 7} {
		l, ok := e.Line(n)
		if !ok || l.Error != ErrorMalformed || l.Tag != "" || l.Commit != "" {
			t.Errorf("line %d = %+v, want malformed with no tag or commit", n, l)
		}
	}
	if l, ok := e.Line(6); !ok || l.Error != ErrorMalformed || l.Tag != "" {
		t.Errorf("duplicate line 6 = %+v, want malformed", l)
	}
	if l, ok := e.Line(0); !ok || l.Error != ErrorMalformed || l.Tag != "" {
		t.Errorf("line 0 = %+v, want malformed", l)
	}
	if l, _ := e.Line(1); l.Error != "" || l.Tag != "v1.0.0" {
		t.Errorf("valid line 1 was touched: %+v", l)
	}

	if d, ok := got.App("kb_dup"); !ok || d.Error != ErrorMalformed {
		t.Errorf("duplicate app = %+v, want malformed", d)
	}
	if _, ok := got.App("../etc"); ok {
		t.Error("an app name that is not a valid ref was kept")
	}
	if _, ok := got.App(""); ok {
		t.Error("an empty app name was kept")
	}
	if b, ok := got.App("kb_bad"); !ok || b.Error != ErrorMalformed {
		t.Errorf("kb_bad = %+v, want malformed app error", b)
	}
}

func TestFetchAppsOversizedBody(t *testing.T) {
	pad := strings.Repeat("a", maxAppsBody)
	url, _ := serveApps(t, 200, `{"checked_at":"`+pad+`","apps":[]}`)
	_, err := FetchApps(context.Background(), url, "tok")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v, want an oversize error", err)
	}
}

func TestFetchAppsInvalidJSON(t *testing.T) {
	url, _ := serveApps(t, 200, `{"apps":`)
	if _, err := FetchApps(context.Background(), url, "tok"); err == nil {
		t.Fatal("expected an error for a truncated body")
	}
}

func TestFetchAppsCancelledContext(t *testing.T) {
	url, _ := serveApps(t, 200, `{"apps":[]}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := FetchApps(ctx, url, "tok"); err == nil {
		t.Fatal("expected an error for a cancelled context")
	}
}
