package license

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/KB-Developpement/kb_pro_cli/internal/ident"
	"github.com/KB-Developpement/kb_pro_cli/internal/version"
)

// CLIVersionHeader carries the kb build version on every license-server
// request. It is informational: a dev build sends "dev" and the server never
// refuses on it in Phase 0.
const CLIVersionHeader = "X-KB-CLI-Version"

// Connection and response-header ceilings for license-server downloads
// (contracts section 5). The body is bounded only by the caller's context.
// They are variables so tests can shorten them.
var (
	DialTimeout           = 15 * time.Second
	ResponseHeaderTimeout = 15 * time.Second
)

// downloadError is decoded from the license server's JSON error body.
type downloadError struct {
	Error string `json:"error"`
}

// HTTPError is a non-200 answer from the license server.
type HTTPError struct {
	App    string
	Status int
	Key    string // the server's {"error": key}, empty when the body had none
}

func (e *HTTPError) Error() string {
	if e.Key != "" {
		return fmt.Sprintf("license server error for %s: %s (HTTP %d)", e.App, e.Key, e.Status)
	}
	return fmt.Sprintf("license server returned HTTP %d for %s", e.Status, e.App)
}

// CompatError means the server answered 200 but without valid source-identity
// headers: it predates the provenance contract (or is broken). Nothing has
// been written to the bench when this is returned.
type CompatError struct {
	App    string
	Reason string
}

func (e *CompatError) Error() string {
	return fmt.Sprintf("the license server's answer for %s lacks valid source-identity headers (%s) — "+
		"this kb needs a license server that sends X-KB-Repository, X-KB-Ref and X-KB-Commit (server-compatibility requirement); "+
		"nothing was changed — contact KB-Developpement", e.App, e.Reason)
}

// DownloadRequest names one archive. Exactly one selector goes on the wire:
// v=<Ref> (plus release=1 when Release) if Ref is set, otherwise
// major=<Line>.
type DownloadRequest struct {
	App        string // license identifier, the /download/{app} path segment
	Repository string // registry repository name, e.g. "kb_pro"
	Line       int    // registry line; must be >= 1 unless Ref is set
	Ref        string // explicit tag/branch/commit (--version, --to, adopt candidates)
	Release    bool   // strict: require a published release; only with Ref
}

// Download is a verified archive on disk plus the identity the server stated.
type Download struct {
	Path       string // temp file; the caller removes it
	Repository string // X-KB-Repository, e.g. "KB-Developpement/kb_pro"
	Ref        string // X-KB-Ref
	Commit     string // X-KB-Commit, 40 lowercase hex
	ReleaseTag string // X-KB-Release-Tag; set only for a strict request
	SHA256     string // hex SHA-256 of the bytes received
	Size       int64
	Requested  string // "major=N" or the v value as sent
}

// query builds the one-selector query string, or refuses.
func (r DownloadRequest) query() (query, requested string, err error) {
	// A registry row without a line is refused whatever the selector: the
	// receipt records the line, and a row that lacks one is a build defect.
	if r.Line < 1 {
		return "", "", fmt.Errorf("the registry row for %s has no release line — refusing to download", r.App)
	}
	switch {
	case r.Ref != "":
		if !ident.ValidRef(r.Ref) {
			return "", "", fmt.Errorf("%q is not a valid tag, branch or commit name", r.Ref)
		}
		q := "v=" + url.QueryEscape(r.Ref)
		if r.Release {
			q += "&release=1"
		}
		return q, r.Ref, nil
	case r.Release:
		return "", "", errors.New("a strict (release=1) download needs an explicit tag")
	default:
		q := "major=" + strconv.Itoa(r.Line)
		return q, q, nil
	}
}

// newDownloadClient returns an http.Client with explicit connect and
// response-header timeouts and no overall timeout: the archive body is bounded
// by the request context.
func newDownloadClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{Timeout: DialTimeout, KeepAlive: 30 * time.Second}).DialContext
	tr.ResponseHeaderTimeout = ResponseHeaderTimeout
	tr.TLSHandshakeTimeout = DialTimeout
	tr.DisableKeepAlives = true
	return &http.Client{Transport: tr}
}

// DownloadApp downloads one archive from the license server into a temporary
// file (outside the bench) while computing its SHA-256, and returns the path
// with the identity the server stated. The caller removes the file.
//
// Before the body is read, X-KB-Repository, X-KB-Ref and X-KB-Commit (and
// X-KB-Release-Tag for a strict request) are validated; a missing or malformed
// header is a *CompatError and no byte is written.
func DownloadApp(ctx context.Context, serverURL, token string, req DownloadRequest) (*Download, error) {
	q, requested, err := req.query()
	if err != nil {
		return nil, err
	}
	if req.Repository == "" {
		return nil, fmt.Errorf("the registry row for %s has no repository", req.App)
	}
	endpoint := strings.TrimRight(serverURL, "/") + "/download/" + url.PathEscape(req.App) + "?" + q

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build download request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set(CLIVersionHeader, version.Version)

	client := newDownloadClient()
	defer client.CloseIdleConnections()
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", req.App, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Read a small portion of the body to surface the server error code.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var apiErr downloadError
		key := ""
		if jsonErr := json.Unmarshal(body, &apiErr); jsonErr == nil {
			key = apiErr.Error
		}
		return nil, &HTTPError{App: req.App, Status: resp.StatusCode, Key: key}
	}

	dl := &Download{Requested: requested}
	if err := validateIdentity(resp.Header, req, dl); err != nil {
		return nil, err
	}

	// Stream body to a temp file. os.CreateTemp sets mode 0600 on Unix.
	f, err := os.CreateTemp("", "kb-app-*.tar.gz")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	dl.Path = f.Name()

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), resp.Body)
	if err != nil {
		f.Close()
		os.Remove(dl.Path)
		return nil, fmt.Errorf("write archive for %s: %w", req.App, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(dl.Path)
		return nil, fmt.Errorf("close archive for %s: %w", req.App, err)
	}
	dl.SHA256 = hex.EncodeToString(h.Sum(nil))
	dl.Size = n
	return dl, nil
}

// validateIdentity checks the X-KB-* response headers and fills dl.
func validateIdentity(h http.Header, req DownloadRequest, dl *Download) error {
	get := func(name string) (string, error) {
		vals := h.Values(name)
		if len(vals) != 1 || vals[0] == "" {
			return "", &CompatError{App: req.App, Reason: name + " is missing"}
		}
		return vals[0], nil
	}

	repo, err := get("X-KB-Repository")
	if err != nil {
		return err
	}
	if !ident.RepositoryRE.MatchString(repo) {
		return &CompatError{App: req.App, Reason: "X-KB-Repository is malformed"}
	}
	if want := ident.Repository(req.Repository); repo != want {
		return &CompatError{App: req.App, Reason: fmt.Sprintf("X-KB-Repository is %q, expected %q", repo, want)}
	}

	ref, err := get("X-KB-Ref")
	if err != nil {
		return err
	}
	if !ident.ValidRef(ref) {
		return &CompatError{App: req.App, Reason: "X-KB-Ref is malformed"}
	}

	commit, err := get("X-KB-Commit")
	if err != nil {
		return err
	}
	if !ident.CommitRE.MatchString(commit) {
		return &CompatError{App: req.App, Reason: "X-KB-Commit is not 40 lowercase hex"}
	}

	dl.Repository, dl.Ref, dl.Commit = repo, ref, commit

	if req.Release {
		tag, err := get("X-KB-Release-Tag")
		if err != nil {
			return &CompatError{App: req.App, Reason: "X-KB-Release-Tag is missing, so the server did not verify a published release (an older server ignores release=1)"}
		}
		if !ident.ValidRef(tag) {
			return &CompatError{App: req.App, Reason: "X-KB-Release-Tag is malformed"}
		}
		if tag != req.Ref {
			return &CompatError{App: req.App, Reason: fmt.Sprintf("X-KB-Release-Tag is %q, expected %q", tag, req.Ref)}
		}
		dl.ReleaseTag = tag
	}
	return nil
}
