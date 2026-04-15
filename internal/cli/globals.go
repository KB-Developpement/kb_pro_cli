package cli

// globalFlags holds the values of persistent flags registered on the root command.
// All fields are read-only after cobra.Command.Execute() has parsed arguments.
var globalFlags struct {
	NoInput bool // --no-input: disable interactive prompts; require flags instead
	Quiet   bool // --quiet / -q: suppress informational stderr output
	Verbose bool // --verbose / -v: print raw bench output on success
	NoColor bool // --no-color: strip ANSI colours from all output
}

// inMenuMode is true while runMainMenu is running.
// pause() uses this to decide whether to wait for Enter.
var inMenuMode bool
