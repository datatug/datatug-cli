package chat

import (
	"net/http"
)

// httpFormMethods and httpHeaderField are ui.go's original
// http_request_ui.go declarations, kept unchanged: chatui_http_overlay.go's
// httpRequestOverlay (the ChatUI port of httpRequestDialog) still uses both.
var httpFormMethods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions}

type httpHeaderField struct {
	name       string
	value      string
	configured bool
}
