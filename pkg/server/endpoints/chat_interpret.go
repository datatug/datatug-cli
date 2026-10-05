package endpoints

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"

	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

const maxChatInterpretBody = 20 << 10

// chatInterpretHandler is a stateless model proxy. No key or generated query
// is saved on the agent. The browser validates and executes DTQL separately.
func chatInterpretHandler(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin != "" {
		if !IsSupportedOrigin(origin) {
			writeChatError(w, http.StatusForbidden, "origin is not allowed")
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/json" {
		writeChatError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxChatInterpretBody))
	if err != nil {
		writeChatError(w, http.StatusBadRequest, "request body is too large or unreadable")
		return
	}
	var req chat.InterpretRequest
	if err := decodeContractBody(body, &req); err != nil {
		writeChatError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := req.Validate(); err != nil {
		writeChatError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := chat.InterpretDetailed(r.Context(), req)
	if err != nil {
		writeChatError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func writeChatError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}{Error: struct {
		Message string `json:"message"`
	}{Message: dbcopy.RedactText(message)}})
}
