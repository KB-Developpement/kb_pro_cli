package license

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
)

const defaultServerURL = "https://license.kb-developpement.com"

// resolveServerURL returns the license server base URL using the following precedence:
//  1. KB_LICENSE_SERVER environment variable
//  2. ~/.config/kb/license_server file
//  3. defaultServerURL
func resolveServerURL() string {
	if v := os.Getenv("KB_LICENSE_SERVER"); v != "" {
		return strings.TrimRight(v, "/")
	}
	home, _ := os.UserHomeDir()
	if data, err := os.ReadFile(filepath.Join(home, ".config", "kb", "license_server")); err == nil {
		if s := strings.TrimSpace(string(data)); s != "" {
			return strings.TrimRight(s, "/")
		}
	}
	return defaultServerURL
}

// activateResponse is the JSON response from POST /activate.
type activateResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

// apiError is the JSON error body from the license server.
type apiError struct {
	Error string `json:"error"`
}

func newClient() *resty.Client {
	return resty.New().
		SetTimeout(15 * time.Second).
		SetHeader("Content-Type", "application/json").
		SetHeader("Accept", "application/json")
}

// Activate calls POST /activate on the license server and returns a fresh JWT.
// Returns a user-friendly error message on non-200 responses.
func Activate(serverBaseURL, licenseKey, fingerprint string) (string, error) {
	if serverBaseURL == "" {
		serverBaseURL = resolveServerURL()
	}

	var resp activateResponse
	var apiErr apiError

	r, err := newClient().R().
		SetBody(map[string]string{
			"license_key": licenseKey,
			"fingerprint": fingerprint,
		}).
		SetResult(&resp).
		SetError(&apiErr).
		Post(serverBaseURL + "/activate")
	if err != nil {
		return "", fmt.Errorf("connect to license server: %w", err)
	}

	switch r.StatusCode() {
	case http.StatusOK:
		return resp.Token, nil
	case http.StatusUnauthorized:
		return "", fmt.Errorf("invalid license key")
	case http.StatusForbidden:
		switch apiErr.Error {
		case "license_revoked":
			return "", fmt.Errorf("license key has been revoked — contact KB-Developpement")
		case "contract_expired":
			return "", fmt.Errorf("support contract has expired — contact KB-Developpement")
		case "machine_banned":
			return "", fmt.Errorf("this machine has been banned — contact KB-Developpement")
		}
		return "", fmt.Errorf("activation denied: %s", apiErr.Error)
	case http.StatusConflict:
		return "", fmt.Errorf("activation limit reached — contact KB-Developpement to add more machines")
	default:
		return "", fmt.Errorf("license server returned HTTP %d", r.StatusCode())
	}
}

// Heartbeat calls POST /heartbeat to refresh a JWT.
// Returns the new token string, or a specific error for each failure mode.
type HeartbeatResult struct {
	Token     string
	Err       error
	ErrCode   string // empty on success; "contract_expired", "license_revoked", etc.
}

// Heartbeat refreshes the JWT. Network errors return HeartbeatResult.Err without
// touching ErrCode — callers treat network errors as grace-period (leave cache intact).
func Heartbeat(serverBaseURL, token, fingerprint string) HeartbeatResult {
	if serverBaseURL == "" {
		serverBaseURL = resolveServerURL()
	}

	var resp activateResponse
	var apiErr apiError

	r, err := newClient().R().
		SetBody(map[string]string{
			"token":       token,
			"fingerprint": fingerprint,
		}).
		SetResult(&resp).
		SetError(&apiErr).
		Post(serverBaseURL + "/heartbeat")
	if err != nil {
		return HeartbeatResult{Err: fmt.Errorf("connect to license server: %w", err)}
	}

	if r.StatusCode() == http.StatusOK {
		return HeartbeatResult{Token: resp.Token}
	}

	return HeartbeatResult{
		Err:     fmt.Errorf("heartbeat failed: HTTP %d — %s", r.StatusCode(), apiErr.Error),
		ErrCode: apiErr.Error,
	}
}
