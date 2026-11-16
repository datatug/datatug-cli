package endpoints

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/strongo/validation"
)

const maxProjectQueryMutationBody = 2 << 20

var localProjectQueries dto.ProjectQueryAdapter = api.LocalProjectQueryAdapter{}

// POST /datatug/queries/save_query is additive to the legacy create/update
// routes. All identity and Git preconditions come from the typed body; actor
// identity comes exclusively from the configured serving session.
func saveProjectQueryHandler(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxProjectQueryMutationBody+1))
	if err != nil || len(body) > maxProjectQueryMutationBody {
		writeProjectMutationError(w, r, validation.NewBadRequestError(errors.New("query request body is too large or unreadable")))
		return
	}
	var request dto.SaveQueryRequest
	if err := decodeContractBody(body, &request); err != nil {
		writeProjectMutationError(w, r, validation.NewBadRequestError(err))
		return
	}
	scope, err := api.SecureProjectMutationScope(request.ProjectRef, request.Branch, "save-query")
	if err != nil {
		writeProjectMutationError(w, r, secureread.ErrAccessDenied)
		return
	}
	result, err := localProjectQueries.SaveQuery(r.Context(), scope, request)
	if err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	writeProjectMutationJSON(w, r, http.StatusOK, result)
}

func getProjectQueryRevisionHandler(w http.ResponseWriter, r *http.Request) {
	ref, err := newProjectRef(r.URL.Query())
	if err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	request := dto.GetQueryRequest{ProjectRef: ref, ID: r.URL.Query().Get("id"), Branch: r.URL.Query().Get("branch")}
	result, err := localProjectQueries.GetQuery(r.Context(), request)
	if err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	writeProjectMutationJSON(w, r, http.StatusOK, result)
}

func projectCapabilitiesHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ref, err := newProjectRef(r.URL.Query())
	if err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	result, err := localProjectQueries.Capabilities(r.Context(), ref)
	if err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	guardedBoardCreate := false
	query, queryErr := guardedBoardCreateSelector(r.URL.RawQuery)
	if queryErr == nil {
		guardedBoardCreate = api.LocalGuardedBoardCreateSupported(r.Context(), ref, query.Get("branch"))
	}
	writeProjectMutationJSON(w, r, http.StatusOK, projectCapabilitiesWithTags{ProjectCapabilities: result, BoardTags: true, DbServerTags: true, GuardedBoardCreate: guardedBoardCreate})
}

func projectConnectionCatalogHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := api.RequireLocalReadPrincipal(); err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeProjectMutationError(w, r, validation.NewBadRequestError(errors.New("invalid catalog selector")))
		return
	}
	for key, values := range q {
		if len(values) != 1 || (key != "project" && key != "storage" && key != "branch") {
			writeProjectMutationError(w, r, validation.NewBadRequestError(errors.New("invalid catalog selector")))
			return
		}
	}
	ref, err := newProjectRef(q)
	if err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	var result any
	if q.Has("branch") {
		result, err = api.LocalProjectConnectionCatalogForBranch(r.Context(), ref, q.Get("branch"))
	} else {
		result, err = api.LocalProjectConnectionCatalog(r.Context(), ref)
	}
	if err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	writeProjectMutationJSON(w, r, http.StatusOK, result)
}

func projectBranchesHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := api.RequireLocalReadPrincipal(); err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	ref, err := newProjectRef(r.URL.Query())
	if err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	result, err := api.LocalProjectBranches(r.Context(), ref)
	if err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	writeProjectMutationJSON(w, r, http.StatusOK, result)
}

func writeProjectMutationJSON(w http.ResponseWriter, r *http.Request, status int, value any) {
	writeCORSOrigin(w, r)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeProjectMutationError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := http.StatusInternalServerError, "INTERNAL", "project query operation failed"
	switch {
	case errors.Is(err, secureread.ErrAccessDenied):
		status, code, message = http.StatusForbidden, "ACCESS_DENIED", "project query access denied"
	case errors.Is(err, api.ErrItemTagsRevisionConflict):
		status, code, message = http.StatusConflict, "ITEM_REVISION_CONFLICT", "item changed; reload before saving tags"
	case errors.Is(err, dto.ErrQueryRevisionConflict):
		status, code, message = http.StatusConflict, "QUERY_REVISION_CONFLICT", "query changed; reload before saving"
	case errors.Is(err, dto.ErrBranchHeadConflict):
		status, code, message = http.StatusConflict, "BRANCH_HEAD_CONFLICT", "branch changed; reload before saving"
	case errors.Is(err, dto.ErrOperationConflict):
		status, code, message = http.StatusConflict, "OPERATION_CONFLICT", "operation ID was already used for another mutation"
	case errors.Is(err, api.ErrProjectMutationOutcomeUncertain):
		status, code, message = http.StatusConflict, "OUTCOME_UNCERTAIN", "query may have been saved; reload and review before another save"
	case errors.Is(err, dto.ErrUnsupportedCapability):
		status, code, message = http.StatusBadRequest, "UNSUPPORTED_CAPABILITY", "selected project store cannot perform this operation"
	case errors.Is(err, dto.ErrInitializationRequired):
		status, code, message = http.StatusConflict, "INITIALIZATION_REQUIRED", "initialize a Git branch before saving"
	case errors.Is(err, api.ErrUnknownStoreID), validation.IsBadRequestError(err), validation.IsBadRecordError(err):
		status, code, message = http.StatusBadRequest, "INVALID_REQUEST", "invalid project query request"
	case errors.Is(err, os.ErrNotExist), errors.Is(err, api.ErrQueryNotFound):
		status, code, message = http.StatusNotFound, "QUERY_NOT_FOUND", "query was not found"
	}
	writeProjectMutationJSON(w, r, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
