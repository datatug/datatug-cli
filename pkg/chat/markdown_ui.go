package chat

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/glamour"
)

func httpDocumentView(entry historyEntry, width int, selected bool) string {
	mode := "Rendered"
	body := sanitizeMultilineText(entry.text)
	if entry.showHeaders {
		mode = "Headers"
		body = entry.httpResponse.headersContent(max(1, width-2))
	} else if entry.showRaw {
		mode = "Raw"
		body = sanitizeMultilineText(boundedText(entry.httpResponse.Body))
	} else if entry.markdown {
		renderer, err := glamour.NewTermRenderer(glamour.WithStandardStyle("dark"), glamour.WithWordWrap(max(20, width-4)))
		if err == nil {
			if rendered, err := renderer.Render(body); err == nil {
				body = strings.TrimSpace(rendered)
			}
		}
	}
	background := messageSurfaceBackground
	if selected {
		background = selectedMessageBackground
	}
	summary := entry.httpResponse.summary()
	if entry.versionBadge != "" {
		summary = entry.versionBadge + " · " + summary
	}
	header := agentStyle.Render(summary) + " " + statusStyle.Render("[1 Rendered · 2 Raw · 3 Headers] · "+mode)
	return lipgloss.NewStyle().Width(max(1, width)).Padding(0, 1).Background(background).Render(header + "\n" + body)
}
