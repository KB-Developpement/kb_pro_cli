package ui

import "charm.land/lipgloss/v2"

var (
	Title   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	Success = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	Failure = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	Dim     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	AppName = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	Bold    = lipgloss.NewStyle().Bold(true)
)
