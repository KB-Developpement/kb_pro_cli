// Package license manages license validation for the kb CLI.
// It follows the same startup-check pattern as update_check.go:
// read cached state synchronously, then optionally refresh in the background.
package license

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

const checkInterval = 7 * 24 * time.Hour

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
// then starts a background goroutine to refresh if the cache is stale (>7 days).
// It is called in PersistentPreRunE and completes in under a millisecond normally.
func RunCheck() {
	entry, err := loadCache()
	if err != nil {
		// Corrupted cache — delete and treat as unlicensed.
		deleteCache()
		return
	}
	if entry == nil {
		// No license file — unlicensed.
		return
	}

	c, err := verifyToken(entry.Token)
	if err != nil {
		// Invalid token (wrong key, malformed) — delete corrupt cache.
		deleteCache()
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
		runHeartbeat(token)
	}()
}

func runHeartbeat(token string) {
	fp, err := Fingerprint()
	if err != nil {
		// Can't compute fingerprint — leave cache intact (grace period).
		return
	}

	result := Heartbeat("", token, fp)
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

func handleHeartbeatError(errCode string) {
	switch errCode {
	case "contract_expired":
		deleteCache()
		fmt.Fprintln(os.Stderr, "warning: support contract has expired — contact KB-Developpement to renew")
	case "license_revoked":
		deleteCache()
		fmt.Fprintln(os.Stderr, "warning: license has been revoked — contact KB-Developpement")
	case "fingerprint_mismatch":
		deleteCache()
		fmt.Fprintln(os.Stderr, "warning: machine fingerprint changed — run: kb activate")
	case "activation_not_found":
		deleteCache()
		fmt.Fprintln(os.Stderr, "warning: activation not found on server — run: kb activate")
	case "machine_banned":
		deleteCache()
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
