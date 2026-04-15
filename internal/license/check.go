// Package license manages license validation for the kb CLI.
// It follows the same startup-check pattern as update_check.go:
// read cached state synchronously, then optionally refresh in the background.
package license

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

const checkInterval = 24 * time.Hour

// licenseCheckDone is closed when the background heartbeat goroutine finishes.
// WaitForCheck() blocks on it (up to 2s) so the goroutine can write its state
// before the process exits.
var licenseCheckDone chan struct{}

// cachedState holds the current license state, populated synchronously from
// the cache file in RunCheck(). Gating functions read this without blocking.
var cachedState atomic.Pointer[State]

// State holds the resolved license state available to gating functions.
type State struct {
	Valid       bool
	AllowedApps []string
	Tier        string
	ClientID    string
	ExpiresAt   time.Time
}

// RunCheck reads the cached license state synchronously, populates cachedState,
// then starts a background goroutine to refresh if the cache is stale (>24h).
// It is called in PersistentPreRunE and completes in under a millisecond normally.
func RunCheck() {
	entry, err := loadCache()
	if err != nil {
		// Corrupted cache — delete and treat as unlicensed.
		deleteCache()
		cachedState.Store(nil)
		return
	}
	if entry == nil {
		// No license file — unlicensed.
		cachedState.Store(nil)
		return
	}

	c, err := verifyToken(entry.Token)
	if err != nil {
		// Invalid token (wrong key, malformed) — delete corrupt cache.
		deleteCache()
		cachedState.Store(nil)
		return
	}

	now := time.Now()
	state := &State{
		Valid:       !isExpired(c, now),
		AllowedApps: c.AllowedApps,
		Tier:        c.Tier,
		ClientID:    c.ClientID,
	}
	if c.ExpiresAt != nil {
		state.ExpiresAt = c.ExpiresAt.Time
	}
	cachedState.Store(state)

	if time.Since(entry.LastCheck) > checkInterval {
		startBackgroundHeartbeat(entry.Token)
	}
}

func startBackgroundHeartbeat(token string) {
	licenseCheckDone = make(chan struct{})
	go func() {
		defer close(licenseCheckDone)
		doHeartbeat(context.Background(), token)
	}()
}

// doHeartbeat performs a heartbeat with the given context and saves the result.
// Network errors leave the cache intact (grace period); server-side errors clear it.
func doHeartbeat(ctx context.Context, token string) {
	fp, err := Fingerprint()
	if err != nil {
		// Can't compute fingerprint — leave cache intact (grace period).
		return
	}

	result := Heartbeat(ctx, "", token, fp)
	if result.Err != nil && result.ErrCode == "" {
		// Network error — leave cache intact. Grace period: JWT exp covers 14 more days.
		return
	}

	if result.Err != nil {
		// Server rejected us with a specific error code — handle each case.
		handleHeartbeatError(result.ErrCode)
		return
	}

	// Success — save the refreshed token.
	entry := &cacheEntry{
		Token:     result.Token,
		LastCheck: time.Now().UTC(),
	}
	// Preserve ActivatedAt from existing cache.
	if existing, _ := loadCache(); existing != nil {
		entry.ActivatedAt = existing.ActivatedAt
	}
	if err := saveCache(entry); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not update license cache: %v\n", err)
	}
}

// RunSyncCheck performs a blocking heartbeat against the license server before
// a high-value operation (install, add). It uses a 5-second timeout so a slow
// server does not hang the CLI indefinitely. On network failure it falls through
// silently (grace period). On a server-side rejection it clears the cache and
// returns an error that the caller should surface to the user.
func RunSyncCheck(ctx context.Context) error {
	entry, err := loadCache()
	if err != nil || entry == nil {
		// No valid cache — RunCheck already handled this path.
		return nil
	}

	fp, err := Fingerprint()
	if err != nil {
		// Can't fingerprint — allow through (grace).
		return nil
	}

	syncCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	result := Heartbeat(syncCtx, "", entry.Token, fp)
	if result.Err != nil && result.ErrCode == "" {
		// Network/timeout error — fall back to cached state silently.
		return nil
	}
	if result.Err != nil {
		// Server explicitly rejected the license — clear cache and block.
		handleHeartbeatError(result.ErrCode)
		return fmt.Errorf("%s", errCodeMessage(result.ErrCode))
	}

	// Success — refresh cache with new token so the next background check
	// gets a fresh 21-day window.
	updated := &cacheEntry{
		Token:       result.Token,
		LastCheck:   time.Now().UTC(),
		ActivatedAt: entry.ActivatedAt,
	}
	if err := saveCache(updated); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not update license cache: %v\n", err)
	}
	return nil
}

// errCodeMessage returns a user-facing message for a server error code.
func errCodeMessage(code string) string {
	switch code {
	case "contract_expired":
		return "support contract has expired — contact KB-Developpement to renew"
	case "license_revoked":
		return "license has been revoked — contact KB-Developpement"
	case "machine_banned":
		return "this machine has been banned — contact KB-Developpement"
	case "fingerprint_mismatch":
		return "machine fingerprint changed — run: kb activate"
	case "activation_not_found":
		return "activation not found on server — run: kb activate"
	default:
		return fmt.Sprintf("license check failed: %s", code)
	}
}

func handleHeartbeatError(errCode string) {
	switch errCode {
	case "contract_expired":
		deleteCache()
		cachedState.Store(nil)
		fmt.Fprintln(os.Stderr, "warning: support contract has expired — contact KB-Developpement to renew")
	case "license_revoked":
		deleteCache()
		cachedState.Store(nil)
		fmt.Fprintln(os.Stderr, "warning: license has been revoked — contact KB-Developpement")
	case "fingerprint_mismatch":
		deleteCache()
		cachedState.Store(nil)
		fmt.Fprintln(os.Stderr, "warning: machine fingerprint changed — run: kb activate")
	case "activation_not_found":
		deleteCache()
		cachedState.Store(nil)
		fmt.Fprintln(os.Stderr, "warning: activation not found on server — run: kb activate")
	case "machine_banned":
		deleteCache()
		cachedState.Store(nil)
		fmt.Fprintln(os.Stderr, "warning: this machine has been banned — contact KB-Developpement")
	default:
		// Unknown error — leave cache intact for grace period.
	}
}

// WaitForCheck blocks until any in-flight background heartbeat completes
// or 2 seconds have elapsed. Called in Execute() before process exit.
func WaitForCheck() {
	if licenseCheckDone == nil {
		return
	}
	select {
	case <-licenseCheckDone:
	case <-time.After(2 * time.Second):
	}
}

// AllowedApps returns the allowed_apps list from the cached JWT,
// or nil if no valid license is cached.
func AllowedApps() []string {
	s := cachedState.Load()
	if s == nil || !s.Valid {
		return nil
	}
	return s.AllowedApps
}

// AllowedSet returns allowed_apps as a map[string]bool for O(1) lookup.
// Returns nil if no valid license is cached.
func AllowedSet() map[string]bool {
	apps := AllowedApps()
	if apps == nil {
		return nil
	}
	m := make(map[string]bool, len(apps))
	for _, a := range apps {
		m[a] = true
	}
	return m
}

// IsValid returns true if a valid, non-expired license is cached.
func IsValid() bool {
	s := cachedState.Load()
	return s != nil && s.Valid
}

// CurrentState returns the current license state for display purposes (kb license).
func CurrentState() *State {
	return cachedState.Load()
}
