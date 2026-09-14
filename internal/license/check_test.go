package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"testing"
	"time"
)

// setupSignedCache installs a test signing key and writes a cache entry holding
// a token with the given fingerprint claim.
// The temp HOME must already be in place when the fingerprint is computed:
// without /etc/machine-id the fallback id lives under ~/.config/kb.
func setupSignedCache(t *testing.T, fingerprintFor func(t *testing.T) string) {
	t.Helper()
	withTempConfigDir(t)
	fingerprint := fingerprintFor(t)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	overrideEmbeddedKey(t, pub)

	token := signTestToken(t, priv, claims{
		ClientID:    "acme",
		AllowedApps: []string{"kb_pro"},
		Fingerprint: fingerprint,
		Tier:        "standard",
	}, time.Now())

	if err := saveCache(&cacheEntry{Token: token, ActivatedAt: time.Now(), LastCheck: time.Now()}); err != nil {
		t.Fatalf("saveCache: %v", err)
	}
	cachedState.Store(nil)
	t.Cleanup(func() { cachedState.Store(nil) })
}

func TestRunCheck_ForeignFingerprintInvalidatesCache(t *testing.T) {
	setupSignedCache(t, func(*testing.T) string {
		return "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	})

	RunCheck()

	if s := CurrentState(); s != nil {
		t.Errorf("CurrentState = %+v, want nil for a token bound to another machine", s)
	}
	if _, err := os.Stat(cachePath()); !os.IsNotExist(err) {
		t.Errorf("license cache should have been deleted, stat err = %v", err)
	}
}

func TestRunCheck_LocalFingerprintStaysValid(t *testing.T) {
	setupSignedCache(t, func(t *testing.T) string {
		fp, err := Fingerprint()
		if err != nil {
			t.Skipf("Fingerprint unavailable: %v", err)
		}
		return fp
	})

	RunCheck()

	s := CurrentState()
	if s == nil || !s.Valid {
		t.Fatalf("CurrentState = %+v, want a valid state for the local fingerprint", s)
	}
	if _, err := os.Stat(cachePath()); err != nil {
		t.Errorf("license cache should still exist: %v", err)
	}
}

func TestRunCheck_EmptyFingerprintClaimStaysValid(t *testing.T) {
	setupSignedCache(t, func(*testing.T) string { return "" })

	RunCheck()

	if s := CurrentState(); s == nil || !s.Valid {
		t.Fatalf("CurrentState = %+v, want a valid state when the token carries no fingerprint", s)
	}
}
