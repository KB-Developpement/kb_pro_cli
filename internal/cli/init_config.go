package cli

import (
	"fmt"
	"os"

	"github.com/charmbracelet/huh"
	"github.com/mattn/go-isatty"

	"github.com/KB-Developpement/kb_pro_cli/internal/config"
)

// runInit shows a first-time setup banner then runs the settings form.
func runInit() {
	fmt.Println("KB Pro CLI — first-time setup")
	fmt.Println("Configure the license server URL and GitHub token used for private repos.")
	fmt.Println()
	runSettingsForm()
}

// runConfigEdit opens the settings form so the user can change stored values.
func runConfigEdit() {
	runSettingsForm()
}

// runSettingsForm presents an interactive form for persistent settings and
// writes the result to disk on success.
func runSettingsForm() {
	s, err := config.LoadStoredSettings()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not load existing settings: %v\n", err)
		s = config.StoredSettings{}
	}

	if s.LicenseServerURL == "" {
		s.LicenseServerURL = config.DefaultLicenseServerURL
	}

	formErr := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("License server URL").
				Description("Base URL for activation and heartbeat (no trailing path). KB_LICENSE_SERVER overrides at runtime.").
				Value(&s.LicenseServerURL),
			huh.NewInput().
				Title("GitHub Personal Access Token").
				Description("For private KB repos. Leave empty if not needed. KB_GITHUB_TOKEN overrides at runtime.").
				EchoMode(huh.EchoModePassword).
				Value(&s.GitHubToken),
		),
	).WithKeyMap(formKeyMap()).Run()

	if formErr != nil {
		return
	}

	if err := config.SaveStoredSettings(s); err != nil {
		fmt.Fprintf(os.Stderr, "error: save settings: %v\n", err)
		return
	}

	fmt.Printf("Settings saved to %s\n", config.SettingsPath())
}

// ensureFirstRunSetup runs the init wizard when nothing is stored yet.
// Skipped with --no-input or non-interactive stdin.
func ensureFirstRunSetup() {
	if globalFlags.NoInput || !isatty.IsTerminal(os.Stdin.Fd()) {
		return
	}
	if config.IsInitialized() {
		return
	}
	runInit()
}

// requireInitializedForCLI runs the first-time wizard when appropriate, then
// errors if the user still has no stored settings (e.g. cancelled the form).
func requireInitializedForCLI() error {
	ensureFirstRunSetup()
	if !config.IsInitialized() {
		return fmt.Errorf("configuration required — run kb init or kb (interactive menu) to set the license server URL and optional GitHub token")
	}
	return nil
}
