package license

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/KB-Developpement/kb_pro_cli/internal/config"
	"github.com/KB-Developpement/kb_pro_cli/internal/fsutil"
)

// installIDFile holds the random per-installation id under the kb config dir.
const installIDFile = "machine-id"

// Fingerprint returns a stable SHA256 hex string identifying this kb
// installation. The result is always 64 lowercase hex characters — the license
// server validates that shape.
//
// The identity is per installation, not per host. Host signals alone are not
// discriminating enough: a container image bakes /etc/machine-id into the
// image, so every container started from it reports the same id, and arm64
// /proc/cpuinfo carries no "model name" line, so the CPU field is empty there.
// Together they collapse to a single constant shared by every deployment of an
// image, which makes a per-key activation limit unenforceable.
//
// A random 128-bit id stored at <config dir>/machine-id therefore leads the
// mix. The host signals are still folded in so the fingerprint also moves when
// the machine genuinely changes:
//
//	sha256(installID + "|" + machineID + "|" + cpuModel)
func Fingerprint() (string, error) {
	id, err := installID()
	if err != nil {
		return "", fmt.Errorf("fingerprint: %w", err)
	}
	raw := id + "|" + hostMachineID() + "|" + cpuModel()
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:]), nil
}

// installID returns the random 128-bit identity of this kb installation,
// creating it on first use. An existing id is never overwritten.
func installID() (string, error) {
	path := installIDPath()
	if id := readTrimmedFile(path); id != "" {
		return id, nil
	}

	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate install id: %w", err)
	}
	id := hex.EncodeToString(buf)

	if err := os.MkdirAll(config.ConfigDir(), 0700); err != nil {
		return "", fmt.Errorf("create config dir: %w", err)
	}
	if err := fsutil.WriteFileAtomic(path, []byte(id+"\n"), 0600); err != nil {
		return "", fmt.Errorf("save install id: %w", err)
	}
	// Re-read so that if a concurrent first run won the rename, this process
	// reports the id that future runs will read rather than its own discarded one.
	if written := readTrimmedFile(path); written != "" {
		return written, nil
	}
	return id, nil
}

// installIDPath is <config dir>/machine-id.
func installIDPath() string {
	return filepath.Join(config.ConfigDir(), installIDFile)
}

// hostMachineID reads /etc/machine-id. A missing or empty file is not an error:
// it is a secondary signal that simply drops out of the mix.
func hostMachineID() string {
	return readTrimmedFile("/etc/machine-id")
}

// cpuModel reads the first "model name" field from /proc/cpuinfo.
// Returns empty string if unavailable — arm64 hosts have no such field.
func cpuModel() string {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "model name") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

// readTrimmedFile returns the whitespace-trimmed contents of path,
// or "" when it cannot be read.
func readTrimmedFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
