package license

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/KB-Developpement/kb_pro_cli/internal/ident"
	"github.com/KB-Developpement/kb_pro_cli/internal/version"
)

// ErrAppsUnsupported means the license server answered 404 for GET /apps: it
// predates the version check. The CLI keeps working without it.
var ErrAppsUnsupported = errors.New("license server does not support version check")

// ErrorMalformed is the line or app error FetchApps substitutes for an entry
// whose fields fail validation. It is never sent by the server.
const ErrorMalformed = "malformed_entry"

const (
	// appsTimeout bounds the whole GET /apps exchange.
	appsTimeout = 60 * time.Second
	// maxAppsBody is the largest /apps answer the client reads.
	maxAppsBody = 1 << 20
)

// errKeyRE is the shape of a server error key (snake_case).
var errKeyRE = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// AppsResponse is the GET /apps answer: the newest release on each line of
// every app the license covers.
type AppsResponse struct {
	CheckedAt string     `json:"checked_at"`
	Apps      []AppEntry `json:"apps"`
}

// AppEntry is one app of the answer. Error is "line_not_found" when the server
// has no app_lines entry for it.
type AppEntry struct {
	App         string    `json:"app"`
	DefaultLine int       `json:"default_line"`
	Error       string    `json:"error"`
	Lines       []AppLine `json:"lines"`
}

// AppLine is the newest published release of one line. When Error is set
// ("line_not_found", "upstream_error") Tag and Commit are empty.
type AppLine struct {
	Line        int    `json:"line"`
	Tag         string `json:"tag"`
	Commit      string `json:"commit"`
	PublishedAt string `json:"published_at"`
	Error       string `json:"error"`
	Stale       bool   `json:"stale"`
}

// Line returns the entry for a line number.
func (e AppEntry) Line(n int) (AppLine, bool) {
	for _, l := range e.Lines {
		if l.Line == n {
			return l, true
		}
	}
	return AppLine{}, false
}

// App returns the entry for an app name.
func (r *AppsResponse) App(name string) (AppEntry, bool) {
	for _, e := range r.Apps {
		if e.App == name {
			return e, true
		}
	}
	return AppEntry{}, false
}

// FetchApps asks the license server for the newest release on each line of
// every licensed app (GET /apps). The answer is validated entry by entry: an
// entry that fails validation is kept but marked with ErrorMalformed and its
// tag and commit are cleared, so a caller can never act on a value that was
// not checked. A 404 is ErrAppsUnsupported; any other non-200 is an *HTTPError.
func FetchApps(ctx context.Context, serverURL, token string) (*AppsResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, appsTimeout)
	defer cancel()

	endpoint := strings.TrimRight(serverURL, "/") + "/apps"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build version check request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set(CLIVersionHeader, version.Version)

	client := newDownloadClient()
	defer client.CloseIdleConnections()
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("version check: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrAppsUnsupported
	case resp.StatusCode != http.StatusOK:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var apiErr downloadError
		key := ""
		if jsonErr := json.Unmarshal(body, &apiErr); jsonErr == nil {
			key = apiErr.Error
		}
		return nil, &HTTPError{App: "/apps", Status: resp.StatusCode, Key: key}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAppsBody+1))
	if err != nil {
		return nil, fmt.Errorf("read version check answer: %w", err)
	}
	if len(body) > maxAppsBody {
		return nil, fmt.Errorf("the license server's version check answer exceeds %d bytes", maxAppsBody)
	}
	var out AppsResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("the license server's version check answer is not valid JSON: %w", err)
	}
	validateApps(&out)
	return &out, nil
}

// validateApps checks every entry in place. An app entry without a usable name
// is dropped (nothing can be matched to it); every other failure turns the
// entry into an error state.
func validateApps(r *AppsResponse) {
	seenApp := map[string]bool{}
	kept := r.Apps[:0]
	for _, e := range r.Apps {
		if !ident.ValidRef(e.App) {
			continue
		}
		if seenApp[e.App] {
			// Two answers for one app: neither can be trusted.
			for i := range kept {
				if kept[i].App == e.App {
					kept[i] = AppEntry{App: e.App, Error: ErrorMalformed}
				}
			}
			continue
		}
		seenApp[e.App] = true

		if e.Error != "" && !errKeyRE.MatchString(e.Error) {
			e = AppEntry{App: e.App, Error: ErrorMalformed}
		}
		seenLine := map[int]bool{}
		for i := range e.Lines {
			l := &e.Lines[i]
			if seenLine[l.Line] {
				// A repeated line number is ambiguous: no copy is trusted.
				for j := range e.Lines {
					if e.Lines[j].Line == l.Line {
						e.Lines[j] = AppLine{Line: l.Line, Error: ErrorMalformed}
					}
				}
				continue
			}
			seenLine[l.Line] = true
			validateLine(l)
		}
		kept = append(kept, e)
	}
	r.Apps = kept
}

// validateLine clears a line that fails validation and marks it malformed.
func validateLine(l *AppLine) {
	bad := l.Line < 1
	switch {
	case l.Error != "":
		bad = bad || !errKeyRE.MatchString(l.Error) || l.Tag != "" || l.Commit != ""
	default:
		bad = bad || !ident.ValidRef(l.Tag) || !ident.CommitRE.MatchString(l.Commit)
	}
	if bad {
		*l = AppLine{Line: l.Line, Error: ErrorMalformed}
	}
}
