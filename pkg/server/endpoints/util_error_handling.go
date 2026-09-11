package endpoints

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/personalqueries"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/strongo/validation"
)

func handleError(err error, w http.ResponseWriter, r *http.Request) bool {
	if err == nil {
		return false
	}
	_, _ = fmt.Fprintln(os.Stderr, err)
	//_, _ = fmt.Println("Error:", err)
	responseHeader := w.Header()
	origin := r.Header.Get("Origin")
	if origin != "" {
		responseHeader.Set("Access-Control-Allow-Origin", origin)
	}
	responseHeader.Set("Content-Type", "application/json")
	response := ErrorResponse{Error: err.Error()}
	switch {
	// A policy refusal is structured with code ACCESS_DENIED (hub Feature
	// core-investigation-loop, "Errors are structured (code, message,
	// field); access refusals use code: ACCESS_DENIED with the policy name
	// and never the hidden value" — REQ:server-acl-all-reads). The message
	// is whatever pkg/accesspolicies/secureread already produced; neither
	// package ever includes a hidden value in that text (see
	// pkg/secureread's own tests), so nothing here can leak one either.
	case errors.Is(err, secureread.ErrAccessDenied):
		response.Code = "ACCESS_DENIED"
		w.WriteHeader(http.StatusForbidden)
	// A legacy route (exec/select, exec/execute_commands) hit the same
	// centralized opaque-SQL grant gate exec/run_query uses
	// (secureread.Executor.RunNativeSQL / session.AllowOpaqueSQL) —
	// api-contract.md "Security and errors": "All legacy routes obey the
	// same rule." This keeps the legacy {error,code} envelope shape (not
	// this task's rewritten {error:{code,message,field,requestId}}
	// contract envelope — see contract_errors.go for the routes that DO
	// use it) but reports the appendix's own error code for this refusal
	// rather than reusing ACCESS_DENIED.
	case errors.Is(err, secureread.ErrOpaqueSQLNotGranted):
		response.Code = "UNSUPPORTED_PROTECTED_EXECUTION"
		w.WriteHeader(http.StatusForbidden)
	// A legacy route (exec/select, exec/execute_commands) hit the same
	// missing-source-file condition api-contract.md's SOURCE_UNAVAILABLE
	// covers (see pkg/dbcopy.CheckSourceFile) — surfaced with that code
	// rather than the driver's own raw "unable to open database file" text
	// reaching a bare 500.
	case errors.Is(err, dbcopy.ErrSourceFileMissing):
		response.Code = "SOURCE_UNAVAILABLE"
		w.WriteHeader(http.StatusServiceUnavailable)
	// api.ResolveStoreID (S87): an explicit ?storage= naming a store this
	// session did not configure for the request's project, or (today
	// unreachable — see ErrAmbiguousStore's own doc comment) a project
	// served by more than one configured store with no explicit ?storage=
	// to disambiguate. Both name the "storage" parameter, matching the
	// brief's "INVALID_REQUEST naming the parameter".
	case errors.Is(err, api.ErrUnknownStoreID), errors.Is(err, api.ErrAmbiguousStore):
		response.Code = "INVALID_REQUEST"
		response.Field = urlParamStoreID
		w.WriteHeader(http.StatusBadRequest)
	// api.ResolveQueryID (S97): get_query's legacy envelope maps an unknown
	// saved-query id to a clean 404 (never the store's own raw filesystem
	// error text) and an ambiguous bare id to 400 INVALID_REQUEST naming the
	// "query" parameter and every candidate.
	case errors.Is(err, api.ErrQueryNotFound):
		w.WriteHeader(http.StatusNotFound)
	// api.GetCatalogTables (S121, Task 17 item A.2): an unknown project or a
	// catalog with no <id>.db.json under the requested environment — same
	// clean-404 treatment as ErrQueryNotFound above, never a raw
	// filesystem error.
	case errors.Is(err, api.ErrCatalogNotFound):
		w.WriteHeader(http.StatusNotFound)
	case errors.Is(err, api.ErrAmbiguousQueryID):
		response.Code = "INVALID_REQUEST"
		response.Field = "query"
		w.WriteHeader(http.StatusBadRequest)
	// getQueriesHandler's ?root= (S169, GET all_queries?root=personal): an
	// unrecognized value — never "shared"/"personal" nor silently defaulted.
	case errors.Is(err, ErrInvalidQueriesRoot):
		response.Code = "INVALID_REQUEST"
		response.Field = urlParamRoot
		w.WriteHeader(http.StatusBadRequest)
	// getPersonalQueries -> personalqueries.ResolveProjectDir (S172): a
	// ref.ProjectID that is not a single safe directory-name path segment
	// (empty, a path separator, or a "."/".." traversal segment) — same
	// {code,field} shape as ErrInvalidQueriesRoot above, naming "project"
	// per this task's own brief. Reachable only defensively; ProjectID
	// always comes from a loaded project file or served-project config key
	// in practice, never raw request input.
	case errors.Is(err, personalqueries.ErrInvalidProjectID):
		response.Code = "INVALID_REQUEST"
		response.Field = "project"
		w.WriteHeader(http.StatusBadRequest)
	case validation.IsBadRequestError(err):
		w.WriteHeader(http.StatusBadRequest)
	default:
		w.WriteHeader(http.StatusInternalServerError)
	}
	encoder := json.NewEncoder(w)
	if err2 := encoder.Encode(response); err2 != nil {
		log.Printf("Failed to encode error to response stream: %v.\nOriginal error: %v", err2, err)
	}
	return true
}

// ErrorResponse defines format of error response body. Code is set for a
// structured refusal (currently only "ACCESS_DENIED"); Field is reserved for
// a future per-field validation error and is not populated yet.
type ErrorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
	Field string `json:"field,omitempty"`
}
