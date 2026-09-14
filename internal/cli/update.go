package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/huh/spinner"
	"github.com/go-resty/resty/v2"
	"github.com/spf13/cobra"

	"github.com/KB-Developpement/kb_pro_cli/internal/license"
	"github.com/KB-Developpement/kb_pro_cli/internal/version"
)

const githubReleasesAPI = "https://api.github.com/repos/KB-Developpement/kb_pro_cli/releases/latest"

type githubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func newUpdateCmd() *cobra.Command {
	var checkOnly, yes bool

	cmd := &cobra.Command{
		Use:     "update",
		Aliases: []string{"u"},
		Short:   "Update kb to the latest version",
		Long: `Check GitHub for the latest kb release and replace the binary in place.

Examples:
  kb update           # Check and update (asks for confirmation)
  kb update --check   # Only check, do not install
  kb update --yes     # Update without asking for confirmation
`,
		// Still receives update check, but skips license check.
		Annotations: map[string]string{"skipLicenseCheck": "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			// --no-input implies --yes (non-interactive confirmation).
			if globalFlags.NoInput {
				yes = true
			}
			return runUpdate(cmd.Context(), checkOnly, yes)
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "Only check for updates, do not install")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation prompt")
	return cmd
}

// githubHTTPClient is a shared resty client for GitHub API calls. resty has no
// default timeout, so set one explicitly — without it `kb update` and the
// background release check can hang indefinitely.
var githubHTTPClient = resty.New().
	SetHeader("Accept", "application/vnd.github+json").
	SetTimeout(30 * time.Second)

// githubDownloadClient fetches release assets, which are much larger than an
// API response and need a longer ceiling.
var githubDownloadClient = resty.New().SetTimeout(5 * time.Minute)

func runUpdate(ctx context.Context, checkOnly, yes bool) error {
	current := version.Version

	// License gate: downloading a new binary requires an active license.
	// --check is always allowed (it's just a read operation).
	// RunCheck populates cachedState from disk since kb update skips the
	// license check in PersistentPreRunE (skipLicenseCheck annotation).
	if !checkOnly {
		license.RunCheck()
		if err := license.RunSyncCheck(ctx); err != nil {
			return err
		}
		if !license.IsValid() {
			return fmt.Errorf("active license required to update — run: kb activate")
		}
	}

	var release githubRelease
	var fetchErr error
	_ = spinner.New().
		Title("Checking for updates…").
		Action(func() {
			resp, err := githubHTTPClient.R().
				SetResult(&release).
				Get(githubReleasesAPI)
			if err != nil {
				fetchErr = fmt.Errorf("fetching release info: %w", err)
				return
			}
			if resp.StatusCode() != 200 {
				fetchErr = fmt.Errorf("GitHub API returned HTTP %d", resp.StatusCode())
			}
		}).
		Run()
	if fetchErr != nil {
		return fetchErr
	}

	latest := release.TagName
	if latest == "" {
		return fmt.Errorf("no releases found on GitHub")
	}

	isDev := current == "dev" || current == ""
	upToDate := !isDev && !newerThan(current, latest)

	if upToDate {
		if !globalFlags.Quiet {
			fmt.Fprintf(os.Stderr, "Already up to date (%s)\n", current)
		}
		return nil
	}

	if !globalFlags.Quiet {
		if isDev {
			fmt.Fprintf(os.Stderr, "Running a dev build. Latest release: %s\n", latest)
		} else {
			fmt.Fprintf(os.Stderr, "Update available: %s → %s\n", current, latest)
		}
	}

	if checkOnly {
		return nil
	}

	target := releaseAssetName(latest)
	var downloadURL, checksumsURL string
	for _, a := range release.Assets {
		switch a.Name {
		case target:
			downloadURL = a.BrowserDownloadURL
		case checksumsAssetName:
			checksumsURL = a.BrowserDownloadURL
		}
	}
	if downloadURL == "" {
		return fmt.Errorf("no asset found for %s/%s (expected %q)", runtime.GOOS, runtime.GOARCH, target)
	}

	if !yes {
		var confirmed bool
		err := huh.NewForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title(fmt.Sprintf("Install kb %s?", latest)).
					Description("←/→ or Y/N · Enter to confirm · Esc/Ctrl+C to cancel").
					Value(&confirmed),
			),
		).WithKeyMap(formKeyMap()).Run()
		if err != nil || !confirmed {
			fmt.Fprintln(os.Stderr, "Update cancelled.")
			return nil
		}
	}

	var installErr error
	_ = spinner.New().
		Title(fmt.Sprintf("Downloading kb %s…", latest)).
		Action(func() {
			installErr = downloadAndInstall(downloadURL, checksumsURL, target)
		}).
		Run()
	if installErr != nil {
		return installErr
	}

	if !globalFlags.Quiet {
		fmt.Fprintf(os.Stderr, "Updated to kb %s\n", latest)
	}
	return nil
}

// checksumsAssetName is the checksum manifest GoReleaser publishes with every
// release (see .goreleaser.yaml `checksum.name_template`).
const checksumsAssetName = "checksums.txt"

func downloadAndInstall(downloadURL, checksumsURL, assetName string) error {
	if checksumsURL == "" {
		return fmt.Errorf("release has no %s asset — refusing to install an unverified binary", checksumsAssetName)
	}

	sumsResp, err := githubDownloadClient.R().Get(checksumsURL)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", checksumsAssetName, err)
	}
	if sumsResp.StatusCode() != 200 {
		return fmt.Errorf("downloading %s failed: HTTP %d", checksumsAssetName, sumsResp.StatusCode())
	}

	resp, err := githubDownloadClient.R().Get(downloadURL)
	if err != nil {
		return fmt.Errorf("downloading: %w", err)
	}
	if resp.StatusCode() != 200 {
		return fmt.Errorf("download failed: HTTP %d", resp.StatusCode())
	}

	if err := verifyChecksum(resp.Body(), sumsResp.Body(), assetName); err != nil {
		return err
	}

	binData, err := extractFromTarGz(resp.Body(), "kb")
	if err != nil {
		return fmt.Errorf("extracting binary: %w", err)
	}
	return replaceBinary(binData)
}

func replaceBinary(newData []byte) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding executable path: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return fmt.Errorf("resolving symlinks: %w", err)
	}

	dir := filepath.Dir(exePath)
	tmp, err := os.CreateTemp(dir, "kb-update-*")
	if err != nil {
		if os.IsPermission(err) {
			return fmt.Errorf("no write permission to %s — try running with sudo", dir)
		}
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, writeErr := tmp.Write(newData); writeErr != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("writing update: %w", writeErr)
	}
	tmp.Close()

	if err := os.Chmod(tmpPath, 0755); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("setting permissions: %w", err)
	}

	if err := os.Rename(tmpPath, exePath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("replacing binary: %w", err)
	}
	return nil
}

// verifyChecksum compares the SHA-256 of data against the entry for assetName
// in a GoReleaser checksums.txt body. Any missing entry is a hard failure —
// an unverifiable download is never installed.
func verifyChecksum(data, checksums []byte, assetName string) error {
	want, err := checksumFor(checksums, assetName)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s — refusing to install", assetName, want, got)
	}
	return nil
}

// checksumFor returns the hex SHA-256 recorded for assetName in a
// "<hex>  <filename>" checksum manifest.
func checksumFor(checksums []byte, assetName string) (string, error) {
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") != assetName {
			continue
		}
		sum := strings.ToLower(fields[0])
		raw, err := hex.DecodeString(sum)
		if err != nil || len(raw) != sha256.Size {
			return "", fmt.Errorf("malformed checksum entry for %s in %s", assetName, checksumsAssetName)
		}
		return sum, nil
	}
	return "", fmt.Errorf("no checksum entry for %s in %s — refusing to install an unverified binary", assetName, checksumsAssetName)
}

// releaseAssetName returns the GoReleaser archive filename for the current platform.
// Example: "v0.2.0" → "kb_0.2.0_linux_amd64.tar.gz"
func releaseAssetName(tagVersion string) string {
	ver := strings.TrimPrefix(tagVersion, "v")
	return fmt.Sprintf("kb_%s_%s_%s.tar.gz", ver, runtime.GOOS, runtime.GOARCH)
}

func extractFromTarGz(data []byte, name string) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decompressing gzip: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading tar: %w", err)
		}
		if filepath.Base(hdr.Name) == name {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("%q not found in archive", name)
}

// newerThan reports whether latest is a higher semver than current.
func newerThan(current, latest string) bool {
	cur := parseSemver(strings.TrimPrefix(current, "v"))
	lat := parseSemver(strings.TrimPrefix(latest, "v"))
	for i := range lat {
		if i >= len(cur) {
			return lat[i] > 0
		}
		if lat[i] > cur[i] {
			return true
		}
		if lat[i] < cur[i] {
			return false
		}
	}
	return false
}

func parseSemver(s string) []int {
	parts := strings.SplitN(s, ".", 3)
	out := make([]int, 3)
	for i, p := range parts {
		if i >= 3 {
			break
		}
		if idx := strings.IndexAny(p, "-+"); idx >= 0 {
			p = p[:idx]
		}
		out[i], _ = strconv.Atoi(p)
	}
	return out
}
