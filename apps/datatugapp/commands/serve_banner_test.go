package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestServeBanner_Render(t *testing.T) {
	var out bytes.Buffer
	writeServeBanner(&out, serveBanner{
		version:  "v1.2.3",
		webUI:    "http://datatug-app.localhost:8989/store/x#code=a",
		fallback: "http://127.0.0.1:8989/store/y#code=b",
		api:      "http://127.0.0.1:8989/datatug",
		localUI:  &serveLocalUI{version: "0.1.0", dir: "/ui/0.1.0", connect: []string{"https://api.example.com"}},
		notices:  []serveNotice{{text: "info line"}, {warn: true, text: "warn line"}},
	})
	s := out.String()
	assert.NotContains(t, s, "\x1b[", "no ANSI when the writer is not a terminal")
	for _, want := range []string{
		"◆ DataTug v1.2.3  localhost   agent ready",
		"➜  Web UI    http://datatug-app.localhost:8989/store/x#code=a\n",
		"➜  Fallback  http://127.0.0.1:8989/store/y#code=b\n",
		"➜  API       http://127.0.0.1:8989/datatug\n",
		"Local web UI 0.1.0 · offline · no telemetry · reaches this agent and https://api.example.com",
		"/ui/0.1.0",
		"●  info line",
		"▲  warn line",
		"Press Ctrl+C to stop",
	} {
		assert.Contains(t, s, want)
	}

	// hosted UI, no notices, daemon: no badge, no fallback, no detail block, no key hint
	out.Reset()
	writeServeBanner(&out, serveBanner{version: "dev", webUI: "https://datatug.app/store/z", api: "http://127.0.0.1:1/datatug", daemon: true})
	s = out.String()
	assert.NotContains(t, s, "localhost")
	assert.NotContains(t, s, "Fallback")
	assert.NotContains(t, s, "Ctrl+C")
	assert.False(t, strings.Contains(s, "\n\n\n"), "no empty detail block: %q", s)
}

func TestInlineHandlersNotice(t *testing.T) {
	n := inlineHandlersNotice(2)
	assert.True(t, n.warn)
	assert.Contains(t, n.text, "2 inline event handler(s)")
}
