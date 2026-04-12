package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

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
		Use:   "update",
		Short: "Update kb to the latest version",
		Long: `Check GitHub for the latest kb release and replace the binary in place.

Examples:
  kb update           # Check and update (asks for confirmation)
  kb update --check   # Only check, do not install
  kb update --yes     # Update without asking for confirmation
`,
		// Still receives update check, but skips license check.
		Annotations: map[string]string{"skipLicenseCheck": "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUpdate(checkOnly, yes)
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "Only check for updates, do not install")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation prompt")
	return cmd
}

func githubClient() *resty.Client {
	return resty.New().SetHeader("Accept", "application/vnd.github+json")
}

func runUpdate(checkOnly, yes bool) error {
	current := version.Version

	// License gate: downloading a new binary requires an active license.
	// --check is always allowed (it's just a read operation).
	if !checkOnly && !license.IsValid() {
		return fmt.Errorf("active license required to update — run: kb activate")
	}

	var release githubRelease
	var fetchErr error
	_ = spinner.New().
		Title("Checking for updates…").
		Action(func() {
			resp, err := githubClient().R().
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
		fmt.Fprintf(os.Stderr, "Already up to date (%s)\n", current)
		return nil
	}

	if isDev {
		fmt.Fprintf(os.Stderr, "Running a dev build. Latest release: %s\n", latest)
	} else {
		fmt.Fprintf(os.Stderr, "Update available: %s → %s\n", current, latest)
	}

	if checkOnly {
		return nil
	}

	target := releaseAssetName(latest)
	var downloadURL string
	for _, a := range release.Assets {
		if a.Name == target {
			downloadURL = a.BrowserDownloadURL
			break
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
			installErr = downloadAndInstall(downloadURL)
		}).
		Run()
	if installErr != nil {
		return installErr
	}

	fmt.Fprintf(os.Stderr, "Updated to kb %s\n", latest)
	return nil
}

func downloadAndInstall(downloadURL string) error {
	resp, err := resty.New().R().Get(downloadURL)
	if err != nil {
		return fmt.Errorf("downloading: %w", err)
	}
	if resp.StatusCode() != 200 {
		return fmt.Errorf("download failed: HTTP %d", resp.StatusCode())
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
