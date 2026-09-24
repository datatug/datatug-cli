package chat

import (
	"sort"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// headersContent renders the saved HTTP response's request/response headers
// for gridState's Headers ExtraView (see ui.go's headersExtraView).
func (response *HTTPResponse) headersContent(width int) string {
	request := []string{"Request", nonempty(response.Method, "GET") + " " + sanitizeTerminalText(response.URL), ""}
	request = append(request, formatHTTPHeaderLines(response.RequestHeaders)...)
	if len(response.RequestHeaders) == 0 {
		request = append(request, "(Request headers were not captured for this older result.)")
	}
	result := []string{
		"Response",
		"Status: " + strconv.Itoa(response.StatusCode),
		"Time to response: " + response.TimeToResponse.Round(1).String(),
		"Download: " + response.DownloadTime.Round(1).String(),
	}
	if len(response.Redirects) > 0 {
		result = append(result, "", "Redirects:")
		for i, hop := range response.Redirects {
			result = append(result, strconv.Itoa(i+1)+". "+strconv.Itoa(hop.StatusCode)+" "+sanitizeTerminalText(hop.URL)+" ("+hop.Elapsed.Round(1).String()+")")
		}
		result = append(result, "Final: "+sanitizeTerminalText(response.FinalURL))
	}
	result = append(result, "")
	result = append(result, formatHTTPHeaderLines(response.Headers)...)
	if width < 72 {
		return strings.Join(append(append(request, ""), result...), "\n")
	}
	leftWidth := (width - 3) / 2
	rightWidth := width - leftWidth - 3
	left, right := make([]string, 0, max(len(request), len(result))), make([]string, 0, max(len(request), len(result)))
	for i := 0; i < max(len(request), len(result)); i++ {
		leftLine, rightLine := "", ""
		if i < len(request) {
			leftLine = request[i]
		}
		if i < len(result) {
			rightLine = result[i]
		}
		left = append(left, lipgloss.NewStyle().Width(leftWidth).Render(ansi.Truncate(leftLine, leftWidth, "…")))
		right = append(right, ansi.Truncate(rightLine, rightWidth, "…"))
	}
	separator := strings.TrimSuffix(strings.Repeat(" │ \n", len(left)), "\n")
	return lipgloss.JoinHorizontal(lipgloss.Top, strings.Join(left, "\n"), separator, strings.Join(right, "\n"))
}

func formatHTTPHeaderLines(headers map[string][]string) []string {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	lines := make([]string, 0, len(names))
	for _, name := range names {
		for _, value := range headers[name] {
			lines = append(lines, sanitizeTerminalText(name)+": "+sanitizeTerminalText(value))
		}
	}
	return lines
}
