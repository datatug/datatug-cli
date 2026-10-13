package endpoints

import (
	"encoding/json"
	"errors"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"github.com/julienschmidt/httprouter"
)

func sessionContextRoutes(path string, router router, wrap wrapper, writeOnly bool, caps Capabilities) {
	if !writeOnly {
		for _, suffix := range []string{"capabilities", "sessions", "sessions/:id", "operations/:id", "inputs/:id", "runs/:id", "sessions/:id/runs", "sessions/:id/messages", "sessions/:id/entries/:entryId/revisions", "sessions/:id/entries/:entryId/revisions/:revisionId"} {
			route(router, wrap, http.MethodGet, path+"/session-context/"+suffix, sessionContextHandler)
		}
	}
	route(router, wrap, http.MethodPost, path+"/session-context/commands", requireWriteCapability(caps, sessionContextHandler))
	route(router, wrap, http.MethodPost, path+"/session-context/query", requireWriteCapability(caps, sessionContextQueryHandler))
}

func sessionContextHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	q, parseErr := url.ParseQuery(r.URL.RawQuery)
	if parseErr != nil {
		writeSessionContextError(w, r, sessioncontext.ErrInvalid)
		return
	}
	for key, values := range q {
		if len(values) != 1 || !sessionContextQueryKey(key) {
			writeSessionContextError(w, r, sessioncontext.ErrInvalid)
			return
		}
	}

	if _, limitErr := sessionContextPageLimit(q); limitErr != nil {
		writeSessionContextError(w, r, sessioncontext.ErrInvalid)
		return
	}
	service, principal, caps, err := api.ResolveSessionContext(r.Context(), q.Get("storage"), q.Get("project"))
	if err != nil {
		writeSessionContextError(w, r, err)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/capabilities") {
		writeContractResponseStatus(w, r, http.StatusOK, caps)
		return
	}
	if q.Get("databaseId") != principal.DatabaseID || q.Get("projectId") != principal.ProjectID {
		writeSessionContextError(w, r, sessioncontext.ErrForbidden)
		return
	}
	var result any
	if r.Method == http.MethodPost {
		var command sessioncontext.Command
		body, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
		if readErr != nil || apicontract.DecodeStrict(body, &command) != nil {
			writeSessionContextError(w, r, sessioncontext.ErrInvalid)
			return
		}
		result, err = service.Execute(r.Context(), principal, command)
	} else {
		id := httprouter.ParamsFromContext(r.Context()).ByName("id")
		switch {
		case strings.Contains(r.URL.Path, "/revisions/"):
			params := httprouter.ParamsFromContext(r.Context())
			result, err = service.ReadEntryRevision(r.Context(), principal, id, params.ByName("entryId"), params.ByName("revisionId"))
		case strings.HasSuffix(r.URL.Path, "/revisions"):
			entryID := httprouter.ParamsFromContext(r.Context()).ByName("entryId")
			limit, _ := sessionContextPageLimit(q)
			result, err = service.ListEntryRevisions(r.Context(), principal, id, entryID, q.Get("cursor"), limit)
		case strings.HasSuffix(r.URL.Path, "/runs") && id != "" || strings.HasSuffix(r.URL.Path, "/messages") && id != "":
			limit, _ := sessionContextPageLimit(q)
			if strings.HasSuffix(r.URL.Path, "/runs") {
				result, err = service.ListRuns(r.Context(), principal, id, q.Get("cursor"), limit)
			} else {
				result, err = service.ListMessages(r.Context(), principal, id, q.Get("cursor"), limit)
			}
		case strings.Contains(r.URL.Path, "/runs/"):
			result, err = service.ReadRun(r.Context(), principal, id)
		case strings.Contains(r.URL.Path, "/inputs/"):
			input, revisions, inputErr := service.ReadInput(r.Context(), principal, id)
			err = inputErr
			result = struct {
				Input     sessioncontext.InputSnapshot   `json:"input"`
				Revisions []sessioncontext.EntryRevision `json:"revisions"`
			}{input, revisions}
		case id == "":
			limit, parseErr := sessionContextPageLimit(q)
			if q.Get("limit") != "" && parseErr != nil {
				writeSessionContextError(w, r, sessioncontext.ErrInvalid)
				return
			}
			result, err = service.ListSessions(r.Context(), principal, q.Get("cursor"), limit)
		case strings.Contains(r.URL.Path, "/operations/"):
			result, err = service.ReadOperation(r.Context(), principal, id)
		default:
			result, err = service.GetSnapshot(r.Context(), principal, id)
		}
	}
	if err != nil {
		writeSessionContextError(w, r, err)
		return
	}
	writeContractResponseStatus(w, r, http.StatusOK, result)
}

func writeSessionContextError(w http.ResponseWriter, r *http.Request, err error) {
	writeCORSOrigin(w, r)
	status, code := http.StatusServiceUnavailable, "session_context_unavailable"
	switch {
	case errors.Is(err, sessioncontext.ErrInvalid):
		status, code = http.StatusBadRequest, "invalid_request"
	case errors.Is(err, sessioncontext.ErrForbidden):
		status, code = http.StatusForbidden, "access_denied"
	case errors.Is(err, sessioncontext.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, sessioncontext.ErrConflict):
		status, code = http.StatusConflict, "version_conflict"
	case errors.Is(err, sessioncontext.ErrReadOnly):
		status, code = http.StatusConflict, "session_read_only"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": code}})
}

func sessionContextQueryKey(key string) bool {
	switch key {
	case "storage", "project", "spaceID", "branch", "databaseId", "projectId", "cursor", "limit":
		return true
	}
	return false
}

func sessionContextPageLimit(q url.Values) (int, error) {
	if q.Get("limit") == "" {
		return 100, nil
	}
	limit, err := strconv.Atoi(q.Get("limit"))
	if err != nil || limit < 1 || limit > 100 {
		return 0, sessioncontext.ErrInvalid
	}
	return limit, nil
}
