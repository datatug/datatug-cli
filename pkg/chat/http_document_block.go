package chat

import (
	tea "charm.land/bubbletea/v2"
	"github.com/strongo/aichat/tui/mdrender"
	"github.com/strongo/aichat/tui/transcript"
)

// httpDocumentBlock is a transcript.Block port of the legacy UI's
// markdown_ui.go httpDocumentView + the "1"/"2"/"3"/"m"/"h" messageFocused
// key handling in ui.go's Update: a saved HTTP response's chat message
// (markdown or plain text) keeps its Rendered/Raw/Headers toggle once it's
// a real Block instead of a static AppendAssistant(Markdown) string.
type httpDocumentBlock struct {
	ui           *ChatUI
	text         string
	markdown     bool
	response     *HTTPResponse
	versionBadge string

	showRaw     bool
	showHeaders bool
}

var _ transcript.Block = (*httpDocumentBlock)(nil)

func (b *httpDocumentBlock) Focusable() bool { return true }

func (b *httpDocumentBlock) View(width int, focused bool) string {
	mode := "Rendered"
	body := sanitizeMultilineText(b.text)
	if b.showHeaders {
		mode = "Headers"
		if b.response != nil {
			body = b.response.headersContent(max(1, width-2))
		}
	} else if b.showRaw {
		mode = "Raw"
		if b.response != nil {
			body = sanitizeMultilineText(boundedText(b.response.Body))
		}
	} else if b.markdown {
		body = mdrender.Render(body, width)
	}
	// The card frame (background, padding, focus highlight) is now the
	// shared theme.Card transcript wraps every Block's View in
	// (strongo/aichat#chat-shared-look) -- this Block supplies content
	// only: the mode-toggle header line plus the body itself.
	summary := ""
	if b.response != nil {
		summary = b.response.summary()
	}
	if b.versionBadge != "" {
		summary = b.versionBadge + " · " + summary
	}
	header := agentStyle().Render(summary) + " " + statusStyle().Render("[1 Rendered · 2 Raw · 3 Headers] · "+mode)
	return header + "\n" + body
}

// Title satisfies transcript.Titled: the card transcript wraps this
// Block's View in shows the HTTP response's own summary as its header,
// instead of an untitled card.
func (b *httpDocumentBlock) Title() string {
	if b.response != nil {
		return "HTTP " + b.response.summary()
	}
	return "HTTP response"
}

func (b *httpDocumentBlock) Update(msg tea.Msg) (transcript.Block, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return b, nil
	}
	switch key.String() {
	case "1":
		b.showRaw, b.showHeaders = false, false
	case "2", "m", "enter":
		b.showRaw, b.showHeaders = !b.showRaw, false
	case "3", "h":
		b.showHeaders = true
	case "q":
		// M5 (r1 adversarial review of #289): save-as-query for a
		// non-table HTTP response -- one with no RecordSet at all, so
		// ChatUI's grid-focused "q" (openSaveQueryDialog, via
		// activeRecordSetID) never applies to it.
		if b.ui != nil {
			return b, b.ui.openSaveQueryDialogForHTTPResponse(b.response)
		}
	}
	return b, nil
}
