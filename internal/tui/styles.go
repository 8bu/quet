package tui

import "github.com/charmbracelet/lipgloss"

// Styles used across the TUI. Colors use ANSI 256 hues; Lip Gloss degrades them
// automatically on terminals that cannot render them.
var (
	styleTitle         = lipgloss.NewStyle().Bold(true)
	styleAccent        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	styleBorderFocused = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
	styleBorderBlurred = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleSelected      = lipgloss.NewStyle().Reverse(true)
	styleMuted         = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	styleFooter        = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	styleStatus        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("114"))
	styleHelpGroup     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	styleHelpKey       = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	styleHelpDesc      = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
)

// borderSet holds the runes of one box-drawing border style. Focused panels use
// the heavy set, unfocused panels the light rounded set, so panel focus stays
// obvious even without color.
type borderSet struct {
	tl, tr, bl, br, h, v string
}

var (
	borderBlurred = borderSet{"╭", "╮", "╰", "╯", "─", "│"}
	borderFocused = borderSet{"┏", "┓", "┗", "┛", "━", "┃"}
)
