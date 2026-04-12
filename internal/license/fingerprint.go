package license

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/KB-Developpement/kb_pro_cli/internal/config"
)

// Fingerprint returns a stable SHA256 hex string identifying this host.
// It reads /etc/machine-id (primary) and the CPU model from /proc/cpuinfo (secondary).
// If /etc/machine-id is missing, a random UUID is generated and cached at
// ~/.config/kb/machine-id so the identifier remains stable across runs.
func Fingerprint() (string, error) {
	machineID, err := machineID()
	if err != nil {
		return "", fmt.Errorf("fingerprint: %w", err)
	}
	cpuModel := cpuModel()

	raw := machineID + "|" + cpuModel
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:]), nil
}

// machineID reads /etc/machine-id, falling back to a cached generated UUID.
func machineID() (string, error) {
	data, err := os.ReadFile("/etc/machine-id")
	if err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" {
			return id, nil
		}
	}

	// Fallback: read or generate a persistent UUID.
	fallbackPath := filepath.Join(config.ConfigDir(), "machine-id")
	cached, err := os.ReadFile(fallbackPath)
	if err == nil {
		id := strings.TrimSpace(string(cached))
		if id != "" {
			return id, nil
		}
	}

	// Generate a new random ID.
	id, err := generateRandomID()
	if err != nil {
		return "", fmt.Errorf("generate machine-id fallback: %w", err)
	}
	dir := config.ConfigDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(fallbackPath, []byte(id+"\n"), 0600); err != nil {
		return "", fmt.Errorf("save machine-id fallback: %w", err)
	}
	return id, nil
}

// cpuModel reads the first "model name" field from /proc/cpuinfo.
// Returns empty string if unavailable — the machine-id is the primary identifier.
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

// generateRandomID generates a random 32-byte hex string.
func generateRandomID() (string, error) {
	b := make([]byte, 16)
	f, err := os.Open("/dev/urandom")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
