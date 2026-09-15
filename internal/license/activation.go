package license

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/KB-Developpement/kb_pro_cli/internal/config"
	"github.com/KB-Developpement/kb_pro_cli/internal/fsutil"
)

// previousFingerprintFile remembers the fingerprint a cached token was bound to
// when that token was thrown away because this installation's identity changed.
// An administrator needs that value to release the seat the old identity still
// holds on the license server, and it is gone from the cache by then.
const previousFingerprintFile = "previous-fingerprint"

// ErrActivationLimitReached reports that the license server refused the
// activation because the key has no free activation slot left.
var ErrActivationLimitReached = errors.New("activation limit reached")

func previousFingerprintPath() string {
	return filepath.Join(config.ConfigDir(), previousFingerprintFile)
}

// savePreviousFingerprint records the fingerprint of an activation that is
// being superseded. Best effort: it only feeds a help message.
func savePreviousFingerprint(fingerprint string) {
	if fingerprint == "" {
		return
	}
	if err := os.MkdirAll(config.ConfigDir(), 0700); err != nil {
		return
	}
	_ = fsutil.WriteFileAtomic(previousFingerprintPath(), []byte(fingerprint+"\n"), 0600)
}

// PreviousFingerprint returns the fingerprint this installation last activated
// with, or "" when it is unknown. It prefers the still-cached token's claim and
// falls back to the value stashed when that cache was invalidated.
func PreviousFingerprint() string {
	if entry, err := loadCache(); err == nil && entry != nil && entry.Token != "" {
		if c, err := verifyToken(entry.Token); err == nil && c.Fingerprint != "" {
			return c.Fingerprint
		}
	}
	return readTrimmedFile(previousFingerprintPath())
}

// ClearPreviousFingerprint drops the stashed fingerprint once it is obsolete —
// after a successful activation, or when the local license is wiped.
func ClearPreviousFingerprint() {
	_ = os.Remove(previousFingerprintPath())
}

// ActivationLimitHelp returns the operator remedy appended to an
// activation_limit_reached failure. Both arguments may be empty; placeholders
// are used instead so the shape of the command is still clear.
func ActivationLimitHelp(licenseKey, previousFingerprint string) string {
	key := licenseKey
	if key == "" {
		key = "<license-key>"
	}
	old := previousFingerprint
	if old == "" {
		old = "<old-fingerprint>"
	}

	var b strings.Builder
	b.WriteString("upgrading kb changes this installation's identifier once, so the previous identifier may still hold a seat on this key.\n")
	b.WriteString("A KB-Developpement administrator can release it:\n")
	fmt.Fprintf(&b, "  kbls remove-activation --key %s --fingerprint %s\n", key, old)
	b.WriteString("or raise this key's --max-activations.")
	return b.String()
}
