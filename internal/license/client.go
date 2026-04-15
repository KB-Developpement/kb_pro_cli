package license

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-resty/resty/v2"

	"github.com/KB-Developpement/kb_pro_cli/internal/config"
)

// resolveServerURL returns the license server base URL (see config.ResolveLicenseServerURL).
func resolveServerURL() string {
	return config.ResolveLicenseServerURL()
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

// httpClient is a shared resty client for all license-server calls.
// Resty clients are safe for concurrent use and pool TCP connections.
var httpClient = newClient()

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

	r, err := httpClient.R().
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
// Pass ctx to enforce a deadline (e.g. 5s for synchronous checks); use context.Background()
// for background goroutines where the existing client timeout (15s) is sufficient.
func Heartbeat(ctx context.Context, serverBaseURL, token, fingerprint string) HeartbeatResult {
	if serverBaseURL == "" {
		serverBaseURL = resolveServerURL()
	}

	var resp activateResponse
	var apiErr apiError

	r, err := httpClient.R().
		SetContext(ctx).
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
