package ui

import (
	"os"

	"charm.land/lipgloss/v2"
)

var (
	Title   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	Success = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	Failure = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	Dim     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	AppName = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	Bold    = lipgloss.NewStyle().Bold(true)
)

func init() {
	if os.Getenv("NO_COLOR") != "" {
		DisableColors()
	}
}

// DisableColors strips colour from all styles.
// Called on --no-color flag or when NO_COLOR env var is set.
func DisableColors() {
	Title   = lipgloss.NewStyle().Bold(true)
	Success = lipgloss.NewStyle()
	Failure = lipgloss.NewStyle()
	Dim     = lipgloss.NewStyle()
	AppName = lipgloss.NewStyle()
	Bold    = lipgloss.NewStyle().Bold(true)
}
