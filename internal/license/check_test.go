package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"os"
	"strings"
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

// A transient read error (EACCES) must not nuke the cache: deleting it forces a
// re-activation and burns an activation slot on the server.
func TestRunCheck_UnreadableCacheIsNotDeleted(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	withTempConfigDir(t)
	if err := os.WriteFile(cachePath(), []byte(`{"token":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cachePath(), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cachePath(), 0o600) })
	cachedState.Store(nil)

	RunCheck()

	if _, err := os.Stat(cachePath()); err != nil {
		t.Fatalf("license cache must survive a transient read error: %v", err)
	}
	if s := CurrentState(); s != nil {
		t.Errorf("CurrentState = %+v, want nil", s)
	}
}

func TestRunCheck_CorruptCacheIsDeleted(t *testing.T) {
	withTempConfigDir(t)
	if err := os.WriteFile(cachePath(), []byte("not json at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	cachedState.Store(nil)

	RunCheck()

	if _, err := os.Stat(cachePath()); !os.IsNotExist(err) {
		t.Errorf("corrupt license cache should be deleted, stat err = %v", err)
	}
}

// captureStderr swaps os.Stderr for a pipe while fn runs and returns what was
// written. Output here is a line or two, well under the pipe buffer.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	fn()
	os.Stderr = orig
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	_ = r.Close()
	return string(data)
}

const foreignFingerprint = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

// Upgrading kb changes this installation's identifier, so every token issued to
// an older kb mismatches exactly once. When a license key is stored locally the
// user can fix it themselves, so say what happened and what to run.
func TestRunCheck_IdentityChangeGuidance(t *testing.T) {
	setupSignedCache(t, func(*testing.T) string { return foreignFingerprint })
	if err := SaveLicenseKey("KB-ACME-123"); err != nil {
		t.Fatalf("SaveLicenseKey: %v", err)
	}

	out := captureStderr(t, RunCheck)

	if !strings.Contains(out, "identifies each installation separately") {
		t.Errorf("warning does not name the cause:\n%s", out)
	}
	if !strings.Contains(out, "kb activate") {
		t.Errorf("warning does not name the fix:\n%s", out)
	}
	if _, err := os.Stat(cachePath()); !os.IsNotExist(err) {
		t.Errorf("license cache should have been deleted, stat err = %v", err)
	}
	if s := CurrentState(); s != nil {
		t.Errorf("CurrentState = %+v, want nil", s)
	}
	if got := PreviousFingerprint(); got != foreignFingerprint {
		t.Errorf("PreviousFingerprint = %q, want the invalidated token's claim", got)
	}
}

// Without a stored key the guidance would be useless — keep the old wording.
func TestRunCheck_ForeignFingerprintWithoutKeyKeepsGenericWarning(t *testing.T) {
	setupSignedCache(t, func(*testing.T) string { return foreignFingerprint })

	out := captureStderr(t, RunCheck)

	if strings.Contains(out, "identifies each installation separately") {
		t.Errorf("upgrade guidance shown with no license key stored:\n%s", out)
	}
	if !strings.Contains(out, "machine fingerprint changed") {
		t.Errorf("expected the generic warning:\n%s", out)
	}
}

func TestAcceptRefreshedToken_RejectsForeignKey(t *testing.T) {
	withTempConfigDir(t)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	overrideEmbeddedKey(t, pub)

	fp, err := Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	good := signTestToken(t, priv, claims{ClientID: "acme", Fingerprint: fp}, time.Now())
	if err := saveCache(&cacheEntry{Token: good, ActivatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	// A token signed by a key this build does not trust must not replace it.
	_, foreignPriv, _ := ed25519.GenerateKey(rand.Reader)
	foreign := signTestToken(t, foreignPriv, claims{ClientID: "acme", Fingerprint: fp}, time.Now())

	stderr := captureStderr(t, func() { acceptRefreshedToken(foreign, time.Now().UTC()) })
	if !strings.Contains(stderr, "cannot verify") {
		t.Errorf("warning: got %q, want a cannot-verify warning", stderr)
	}
	kept, err := loadCache()
	if err != nil || kept == nil {
		t.Fatalf("cache must survive a foreign token: %v", err)
	}
	if kept.Token != good {
		t.Error("cache was overwritten with an unverifiable token")
	}

	// A token this build can verify is stored.
	next := signTestToken(t, priv, claims{ClientID: "acme", Fingerprint: fp}, time.Now().Add(time.Minute))
	acceptRefreshedToken(next, time.Now().UTC())
	after, _ := loadCache()
	if after == nil || after.Token != next {
		t.Error("a verifiable token should have been stored")
	}
}
