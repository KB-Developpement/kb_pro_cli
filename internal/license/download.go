package license

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// downloadError is decoded from the license server's JSON error body.
type downloadError struct {
	Error string `json:"error"`
}

// DownloadApp downloads a KB app archive from the license server and saves it
// to a temporary file. The caller is responsible for removing the file when done.
//
// If version is empty the server will resolve the latest release.
// Returns the path to the saved .tar.gz file.
func DownloadApp(ctx context.Context, serverURL, token, app, version string) (string, error) {
	// Build endpoint URL.
	endpoint := strings.TrimRight(serverURL, "/") + "/download/" + url.PathEscape(app)
	if version != "" {
		endpoint += "?v=" + url.QueryEscape(version)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("build download request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", app, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Read a small portion of the body to surface the server error code.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var apiErr downloadError
		if jsonErr := json.Unmarshal(body, &apiErr); jsonErr == nil && apiErr.Error != "" {
			return "", fmt.Errorf("license server error for %s: %s (HTTP %d)", app, apiErr.Error, resp.StatusCode)
		}
		return "", fmt.Errorf("license server returned HTTP %d for %s", resp.StatusCode, app)
	}

	// Stream body to a temp file. os.CreateTemp sets mode 0600 on Unix.
	f, err := os.CreateTemp("", "kb-app-*.tar.gz")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := f.Name()

	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("write archive for %s: %w", app, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("close archive for %s: %w", app, err)
	}

	return tmpPath, nil
}
