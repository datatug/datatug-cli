package endpoints

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
)

type sessionQueryBinding struct {
	EntryID     string `json:"entryId"`
	ParameterID string `json:"parameterId"`
}
type sessionQueryRequest struct {
	Command  sessioncontext.Command `json:"command"`
	QueryID  string                 `json:"queryId"`
	Source   string                 `json:"source,omitempty"`
	Bindings []sessionQueryBinding  `json:"bindings"`
	Limit    *int                   `json:"limit,omitempty"`
}

// sessionContextQueryHandler pins before dispatch and uses the existing source
// executor. An already accepted operation is returned for reconciliation; it
// never dispatches a second source query after an uncertain acknowledgement.
func sessionContextQueryHandler(w http.ResponseWriter, r *http.Request) {
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
	service, p, caps, err := api.ResolveSessionContext(r.Context(), q.Get("storage"), q.Get("project"))
	if err != nil {
		writeSessionContextError(w, r, err)
		return
	}
	if q.Get("databaseId") != p.DatabaseID || q.Get("projectId") != p.ProjectID {
		writeSessionContextError(w, r, sessioncontext.ErrForbidden)
		return
	}
	var request sessionQueryRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil || apicontract.DecodeStrict(body, &request) != nil || request.Command.Action != "pinInput" || request.Command.Kind != "query" || request.QueryID == "" || request.Command.Scope == nil || len(request.Bindings) == 0 {
		writeSessionContextError(w, r, sessioncontext.ErrInvalid)
		return
	}
	admitted := false
	for _, env := range caps.Environments {
		if env.EnvironmentID == request.Command.Scope.EnvironmentID && env.IdentityDomain == request.Command.Scope.IdentityDomain {
			admitted = true
		}
	}
	if !admitted {
		writeSessionContextError(w, r, sessioncontext.ErrForbidden)
		return
	}
	if request.Command.DatabaseID != "" && request.Command.DatabaseID != p.DatabaseID || request.Command.ProjectID != "" && request.Command.ProjectID != p.ProjectID {
		writeSessionContextError(w, r, sessioncontext.ErrForbidden)
		return
	}
	request.Command.DatabaseID, request.Command.ProjectID = p.DatabaseID, p.ProjectID
	frozen, _ := json.Marshal(struct {
		QueryID  string
		Source   string
		Bindings []sessionQueryBinding
		Limit    *int
	}{request.QueryID, request.Source, request.Bindings, request.Limit})
	digest := sha256.Sum256(frozen)
	request.Command.Text = hex.EncodeToString(digest[:])
	if receipt, lookupErr := service.ReadOperation(r.Context(), p, request.Command.OpID); lookupErr == nil {
		run, runErr := service.ReadRun(r.Context(), p, receipt.ResourceID)
		if runErr != nil {
			writeSessionContextError(w, r, runErr)
			return
		}
		request.Command.DefinitionID, request.Command.DefinitionRevision = run.DefinitionID, run.DefinitionRevision
		input, _, inputErr := service.ReadInput(r.Context(), p, run.InputSnapshotID)
		if inputErr != nil {
			writeSessionContextError(w, r, inputErr)
			return
		}
		request.Command.ResolvedParameters = input.ResolvedParameters
		request.Command.DefinitionDocument = input.DefinitionDocument
		request.Command.ExpectedContextSetID = input.ContextSetID
		executor := p
		executor.Executor = true
		if _, replayErr := service.Execute(r.Context(), executor, request.Command); replayErr != nil {
			writeSessionContextError(w, r, replayErr)
			return
		}
		writeContractResponseStatus(w, r, http.StatusOK, struct {
			Receipt  sessioncontext.Receipt `json:"receipt"`
			Run      sessioncontext.Run     `json:"run"`
			Replayed bool                   `json:"replayed"`
		}{receipt, run, true})
		return
	} else if !errors.Is(lookupErr, sessioncontext.ErrNotFound) {
		writeSessionContextError(w, r, lookupErr)
		return
	}
	store, err := api.ProjectStoreFor(p.ProjectID)
	if err != nil {
		writeSessionContextError(w, r, sessioncontext.ErrUnavailable)
		return
	}
	revisioned, ok := store.(datatug.RevisionedQueriesStore)
	if !ok {
		writeSessionContextError(w, r, sessioncontext.ErrUnavailable)
		return
	}
	dir, _ := api.ProjectDir(p.ProjectID)
	canonical, err := api.ResolveQueryID(dir, request.QueryID)
	if err != nil {
		writeSessionContextError(w, r, sessioncontext.ErrNotFound)
		return
	}
	query, err := revisioned.LoadQueryRevision(r.Context(), canonical)
	if err != nil {
		writeSessionContextError(w, r, sessioncontext.ErrNotFound)
		return
	}
	request.Command.DefinitionID, request.Command.DefinitionRevision = canonical, string(query.Revision)
	parameters := []map[string]any{}
	for _, parameter := range query.Query.Parameters {
		parameters = append(parameters, map[string]any{"id": parameter.ID, "type": parameter.Type, "isRequired": parameter.IsRequired})
	}
	request.Command.DefinitionDocument, _ = json.Marshal(map[string]any{"schemaVersion": 1, "kind": "query", "queries": []any{map[string]any{"id": "source-query", "queryType": query.Query.Type, "text": query.Query.Text, "connectionId": query.Query.ConnectionID, "parameters": parameters}}})
	snapshot, snapshotErr := service.GetSnapshot(r.Context(), p, request.Command.SessionID)
	if snapshotErr != nil {
		writeSessionContextError(w, r, snapshotErr)
		return
	}
	if snapshot.Session.Version != request.Command.ExpectedVersion {
		writeSessionContextError(w, r, sessioncontext.ErrConflict)
		return
	}
	request.Command.ExpectedContextSetID = snapshot.ContextSet.ID
	refs := map[string]string{}
	addRef := func(ref sessioncontext.EntryRef) bool {
		if prior, exists := refs[ref.EntryID]; exists && prior != ref.RevisionID {
			return false
		}
		refs[ref.EntryID] = ref.RevisionID
		return true
	}
	if snapshot.ContextSet.Enabled {
		for _, slot := range snapshot.ContextSet.Slots {
			if slot.Scope == *request.Command.Scope && slot.BagID != "" {
				for _, ref := range slot.Entries {
					if !addRef(ref) {
						writeSessionContextError(w, r, sessioncontext.ErrInvalid)
						return
					}
				}
			}
		}
	}
	for _, ref := range request.Command.Attachments {
		if !addRef(ref) {
			writeSessionContextError(w, r, sessioncontext.ErrInvalid)
			return
		}
	}
	request.Command.ResolvedParameters = map[string]apicontract.TypedValue{}
	declared := map[string]bool{}
	for _, parameter := range query.Query.Parameters {
		declared[parameter.ID] = true
	}
	for _, binding := range request.Bindings {
		revisionID, exists := refs[binding.EntryID]
		if !exists || !declared[binding.ParameterID] {
			writeSessionContextError(w, r, sessioncontext.ErrInvalid)
			return
		}
		if _, exists := request.Command.ResolvedParameters[binding.ParameterID]; exists {
			writeSessionContextError(w, r, sessioncontext.ErrInvalid)
			return
		}
		revision, revisionErr := service.ReadEntryRevision(r.Context(), p, request.Command.SessionID, binding.EntryID, revisionID)
		if revisionErr != nil {
			writeSessionContextError(w, r, revisionErr)
			return
		}
		if revision.Value.Kind != "scalar" || revision.Value.Scalar == nil {
			writeSessionContextError(w, r, sessioncontext.ErrInvalid)
			return
		}
		request.Command.ResolvedParameters[binding.ParameterID] = *revision.Value.Scalar
	}
	executor := p
	executor.Executor = true
	pinned, err := service.Execute(r.Context(), executor, request.Command)
	if err != nil {
		writeSessionContextError(w, r, err)
		return
	}
	execution := apicontract.ExecutionRequest{StoreID: api.LocalStoreID, Project: p.ProjectID, Environment: request.Command.Scope.EnvironmentID, SecurityContextID: api.SecurityContextID(), Source: request.Source, QueryID: canonical, Mode: "live", Record: true, Limit: request.Limit, Parameters: map[string]apicontract.TypedValueOrSet{}, BindingOrigins: []apicontract.BindingOriginEntry{}}
	for _, binding := range request.Bindings {
		execution.Parameters[binding.ParameterID] = apicontract.ScalarValue(request.Command.ResolvedParameters[binding.ParameterID])
		execution.BindingOrigins = append(execution.BindingOrigins, apicontract.BindingOriginEntry{ParameterID: binding.ParameterID, Origin: apicontract.BindingOriginContext, FactID: binding.EntryID})
	}
	result, executionErr := computeRunQueryWithOptions(r.Context(), execution, runQueryOptions{expectedQueryRevision: string(query.Revision)})
	completionDigest := sha256.Sum256([]byte(request.Command.OpID))
	completion := sessioncontext.Command{OpID: "complete-" + hex.EncodeToString(completionDigest[:]), Action: "completeRun", SessionID: request.Command.SessionID, RunID: request.Command.RunID, ExpectedRunVersion: 1, RunState: "completed"}
	if executionErr != nil {
		completion.RunState = "failed"
		completion.ErrorCode = "SOURCE_EXECUTION_FAILED"
	} else if result.Execution != nil {
		completion.ResultRefs = []string{result.Execution.ExecutionID}
	}
	trusted := p
	trusted.Executor = true
	if _, err = service.Execute(r.Context(), trusted, completion); err != nil {
		writeSessionContextError(w, r, err)
		return
	}
	if executionErr != nil {
		writeContractError(w, r, executionErr)
		return
	}
	run, err := service.ReadRun(r.Context(), p, request.Command.RunID)
	if err != nil {
		writeSessionContextError(w, r, err)
		return
	}
	writeContractResponseStatus(w, r, http.StatusOK, struct {
		Receipt sessioncontext.Receipt `json:"receipt"`
		Run     sessioncontext.Run     `json:"run"`
		Result  apicontract.Result     `json:"result"`
	}{pinned.Receipt, run, result})
}
