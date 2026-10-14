package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/huh"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/KB-Developpement/kb_pro_cli/internal/license"
	"github.com/KB-Developpement/kb_pro_cli/internal/ui"
)

func newActivateCmd() *cobra.Command {
	var keyFile string
	var prompt bool
	cmd := &cobra.Command{
		Use:     "activate",
		Aliases: []string{"a"},
		Short:   "Activate this machine with a KB Pro license key",
		Long: `Activate this machine using your KB Pro license key.

The license key is provided by KB-Developpement when you purchase a support contract.
Your machine fingerprint is computed locally and sent to the license server to
register this machine. A signed token is stored at ~/.config/kb/license.json.

If a license key is already saved locally, it will be used automatically without
prompting. Use --prompt to enter a different key at the masked prompt, or
--key-file <path> to read it from a private file. --key-file - reads redirected
stdin. Positional license keys are refused to avoid shell-history exposure.`,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 0 {
				return errors.New("license keys are not accepted as arguments; use kb activate --prompt or --key-file <path>")
			}
			return nil
		},
		SilenceUsage: true,
		// Skip both update check and license check for this command.
		Annotations: map[string]string{"skipChecks": "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Flags().Changed("key-file") && keyFile == "" {
				return errors.New("--key-file requires a path or - for redirected stdin")
			}
			return runActivate(keyFile, prompt, cmd.InOrStdin())
		},
	}
	cmd.Flags().StringVar(&keyFile, "key-file", "", "Read the license key from a private file (- for redirected stdin)")
	cmd.Flags().BoolVar(&prompt, "prompt", false, "Enter a key at the masked prompt instead of using the saved key")
	cmd.MarkFlagsMutuallyExclusive("key-file", "prompt")
	return cmd
}

func runActivate(keyFile string, prompt bool, input io.Reader) error {
	licenseKey, err := readActivationKey(keyFile, prompt, input)
	if errors.Is(err, huh.ErrUserAborted) {
		return nil
	}
	if err != nil {
		return err
	}

	// Captured before activation succeeds and overwrites the cache: it names the
	// seat an administrator has to release when the key is already at its limit.
	previousFingerprint := license.PreviousFingerprint()

	var token string
	var activateErr error
	if spinErr := runWithSpinner("Activating license…", func() {
		fp, err := license.Fingerprint()
		if err != nil {
			activateErr = fmt.Errorf("compute machine fingerprint: %w", err)
			return
		}
		token, activateErr = license.Activate("", licenseKey, fp)
	}); spinErr != nil {
		return spinErr
	}
	if activateErr != nil {
		// The seat is taken by an activation this installation made before its
		// identifier changed — say how to free it instead of stopping at
		// "contact KB-Developpement".
		if errors.Is(activateErr, license.ErrActivationLimitReached) &&
			(previousFingerprint != "" || license.LoadLicenseKey() != "") {
			return fmt.Errorf("%w\n%s", activateErr, license.ActivationLimitHelp(previousFingerprint))
		}
		return activateErr
	}

	// Save the key separately, replacing an earlier key only after successful activation.
	if err := license.SaveLicenseKey(licenseKey); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not save license key: %v\n", err)
	}

	// Save the JWT to the cache file.
	if err := license.SaveTokenCache(token, time.Now().UTC()); err != nil {
		return fmt.Errorf("save license token: %w", err)
	}
	// This installation now holds a live activation; the stashed one is history.
	license.ClearPreviousFingerprint()

	if !globalFlags.Quiet {
		fmt.Fprintln(os.Stdout, ui.Success.Render("License activated successfully."))
		fmt.Fprintln(os.Stdout, ui.Dim.Render("Run: kb license — to view license details."))
	}
	return nil
}

// readActivationKey keeps key material out of arguments and gives an explicit
// source precedence over the saved key. A failed explicit source never falls back.
func readActivationKey(keyFile string, prompt bool, input io.Reader) (string, error) {
	if keyFile != "" {
		if keyFile == "-" {
			if isActivationTerminal(input) {
				return "", errors.New("--key-file - requires redirected stdin; use --prompt for masked terminal input")
			}
			return readActivationKeyInput(input)
		}
		info, err := os.Lstat(keyFile)
		if err != nil {
			return "", fmt.Errorf("inspect license key file: %w", err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return "", errors.New("license key file must be a regular private file (chmod 600)")
		}
		f, err := os.Open(keyFile)
		if err != nil {
			return "", fmt.Errorf("open license key file: %w", err)
		}
		defer f.Close()
		opened, err := f.Stat()
		if err != nil {
			return "", fmt.Errorf("inspect opened license key file: %w", err)
		}
		if !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm()&0o077 != 0 {
			return "", errors.New("license key file changed while opening")
		}
		return readActivationKeyInput(f)
	}
	if !prompt {
		if key := license.LoadLicenseKey(); key != "" {
			return validateActivationKey(key)
		}
	}
	if globalFlags.NoInput || !isActivationTerminal(input) {
		return "", errors.New("no interactive license key input; use --key-file <path> or --key-file - with redirected stdin")
	}
	var key string
	if err := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("KB Pro License Key").
				Description("Enter the license key provided by KB-Developpement.").
				EchoMode(huh.EchoModePassword).
				CharLimit(license.MaxLicenseKeyBytes).
				Value(&key),
		),
	).WithInput(input).WithKeyMap(formKeyMap()).Run(); err != nil {
		return "", err
	}
	return validateActivationKey(key)
}

func isActivationTerminal(input io.Reader) bool {
	f, ok := input.(*os.File)
	return ok && isatty.IsTerminal(f.Fd())
}

func readActivationKeyInput(input io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(input, license.MaxLicenseKeyBytes+3))
	if err != nil {
		return "", errors.New("could not read license key input")
	}
	if len(data) > license.MaxLicenseKeyBytes+2 {
		return "", errors.New("license key input is too large (maximum key length: 4096 bytes)")
	}
	return validateActivationKey(string(data))
}

func validateActivationKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", errors.New("no license key provided")
	}
	if len(key) > license.MaxLicenseKeyBytes {
		return "", errors.New("license key exceeds 4096 bytes")
	}
	if strings.ContainsFunc(key, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) {
		return "", errors.New("license key input must contain one key without whitespace or control characters")
	}
	return key, nil
}
