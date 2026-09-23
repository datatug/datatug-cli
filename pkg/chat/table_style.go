package chat

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// tableStyle is UI presentation only. RecordSets and their saved rows are not
// rewritten when the user changes the style.
type tableStyle uint8

const (
	tableStyleLines tableStyle = iota
	tableStyleSoft
	tableStyleMinimal
	tableStyleCount
)

func (s tableStyle) name() string {
	switch s {
	case tableStyleMinimal:
		return "Minimal"
	case tableStyleLines:
		return "Lines"
	default:
		return "Soft"
	}
}

func parseTableStyle(name string) tableStyle {
	switch name {
	case "Soft":
		return tableStyleSoft
	case "Minimal":
		return tableStyleMinimal
	case "Lines":
		return tableStyleLines
	default:
		return tableStyleLines
	}
}

func (s tableStyle) borderColor() color.Color {
	switch s {
	case tableStyleMinimal:
		return lipgloss.Color("232")
	case tableStyleLines:
		return lipgloss.Color("241")
	default:
		return lipgloss.Color("235")
	}
}

func (s tableStyle) headerStyle() lipgloss.Style {
	switch s {
	case tableStyleMinimal:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Bold(true)
	case tableStyleLines:
		return lipgloss.NewStyle().Background(lipgloss.Color("237")).Foreground(lipgloss.Color("255")).Bold(true)
	default:
		return lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("250")).Bold(true)
	}
}

func (s tableStyle) dividerStyle() lipgloss.Style {
	return lipgloss.NewStyle().BorderForeground(s.borderColor())
}
