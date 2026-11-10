package commands

import (
	"fmt"
	"io"
	"strings"

	"charm.land/lipgloss/v2"
)

// serveNotice is one line under the links of the serve banner: an info line, or a warning about
// something the operator turned on or should know (an unrestricted principal, an opt-in flag).
type serveNotice struct {
	warn bool
	text string
}

// serveLocalUI describes the installed web UI this agent serves itself (serve --web-ui).
type serveLocalUI struct {
	version string
	dir     string
	connect []string
}

// serveBanner is everything `datatug serve` tells the operator on start.
type serveBanner struct {
	version  string
	webUI    string // the link to open, with its one-time code for a hand-started server
	fallback string // the 127.0.0.1 link when the web UI is served on a *.localhost name
	api      string
	localUI  *serveLocalUI
	notices  []serveNotice
	daemon   bool // output goes to the daemon log: no "press Ctrl+C" hint
}

var (
	bannerBrand = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#A78BFA")) // violet
	bannerArrow = lipgloss.NewStyle().Foreground(lipgloss.Color("#34D399"))            // green
	bannerLabel = lipgloss.NewStyle().Bold(true).Width(10)
	bannerURL   = lipgloss.NewStyle().Foreground(lipgloss.Color("#22D3EE")) // cyan
	bannerDim   = lipgloss.NewStyle().Faint(true)
	bannerInfo  = lipgloss.NewStyle().Foreground(lipgloss.Color("#34D399"))
	bannerWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color("#FBBF24")) // amber
	bannerBadge = lipgloss.NewStyle().Bold(true).Padding(0, 1).
			Foreground(lipgloss.Color("#0B1120")).Background(lipgloss.Color("#34D399"))
)

// render lays the banner out the way modern dev servers do: a brand line, one arrow per link,
// then the details. URLs stay on one line each, so terminals and scripts can pick them up.
func (b serveBanner) render() string {
	var s strings.Builder
	s.WriteString("\n  " + bannerBrand.Render("◆ DataTug") + " " + bannerDim.Render(b.version))
	if b.localUI != nil {
		// the web UI comes from this machine, not from datatug.app
		s.WriteString(" " + bannerBadge.Render("localhost"))
	}
	s.WriteString("  agent ready\n\n")
	link := func(label, url string) {
		s.WriteString("  " + bannerArrow.Render("➜") + "  " + bannerLabel.Render(label) + bannerURL.Render(url) + "\n")
	}
	link("Web UI", b.webUI)
	if b.fallback != "" {
		link("Fallback", b.fallback)
	}
	link("API", b.api)
	s.WriteString("\n")
	if ui := b.localUI; ui != nil {
		reach := "only this agent"
		if len(ui.connect) > 0 {
			reach = "this agent and " + strings.Join(ui.connect, ", ")
		}
		s.WriteString("  " + bannerInfo.Render("●") + "  Local web UI " + ui.version + bannerDim.Render(" · offline · no telemetry · reaches "+reach) + "\n")
		s.WriteString("     " + bannerDim.Render(ui.dir) + "\n")
	}
	for _, n := range b.notices {
		if n.warn {
			s.WriteString("  " + bannerWarn.Render("▲") + "  " + bannerWarn.Render(n.text) + "\n")
		} else {
			s.WriteString("  " + bannerInfo.Render("●") + "  " + n.text + "\n")
		}
	}
	if b.localUI != nil || len(b.notices) > 0 {
		s.WriteString("\n")
	}
	if !b.daemon {
		s.WriteString("  " + bannerDim.Render("Press Ctrl+C to stop") + "\n\n")
	}
	return s.String()
}

// writeServeBanner prints the banner; lipgloss drops the colors when w is not a terminal.
func writeServeBanner(w io.Writer, b serveBanner) {
	_, _ = lipgloss.Fprint(w, b.render())
}

// inlineHandlersNotice warns about inline event handlers the web UI's CSP blocks.
func inlineHandlersNotice(n int) serveNotice {
	return serveNotice{warn: true, text: fmt.Sprintf("The web UI has %d inline event handler(s) that its Content-Security-Policy blocks", n)}
}
