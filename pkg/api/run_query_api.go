package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/datatug-core/storage"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/strongo/validation"
)

// RunQueryRequest is the body of POST /datatug/exec/run_query: run a saved
// query (QueryID, resolved via the project store) or an ad-hoc DTQL document
// (DTQL, raw DTQL-YAML text) against Environment/Database, with Parameters
// bound as the query's own `param` nodes (including $currentUser, bound
// automatically from the session's principal — see
// pkg/accesspolicies.Run/secureread.Executor.RunDTQL).
type RunQueryRequest struct {
	StoreID     string         `json:"storage"`
	ProjectID   string         `json:"project"`
	Environment string         `json:"environment"`
	Database    string         `json:"database"`
	QueryID     string         `json:"queryId,omitempty"`
	DTQL        string         `json:"dtql,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// Validate returns an error if the request is not well-formed.
func (v RunQueryRequest) Validate() error {
	if v.ProjectID == "" {
		return validation.NewErrRequestIsMissingRequiredField("project")
	}
	if v.Environment == "" {
		return validation.NewErrRequestIsMissingRequiredField("environment")
	}
	if v.Database == "" {
		return validation.NewErrRequestIsMissingRequiredField("database")
	}
	if v.QueryID == "" && v.DTQL == "" {
		return validation.NewErrRequestIsMissingRequiredField("queryId or dtql")
	}
	if v.QueryID != "" && v.DTQL != "" {
		return validation.NewErrBadRequestFieldValue("dtql", "can't be supplied together with queryId")
	}
	return nil
}

// RunQueryResponse is POST /datatug/exec/run_query's response: the
// recordset and limitations every policy-enforced read carries
// (QueryResultResponse), plus the parameter values that were actually bound
// into the run.
type RunQueryResponse struct {
	QueryResultResponse
	BindingsApplied map[string]any `json:"bindingsApplied,omitempty"`
}

// RunQuery dispatches a saved or ad-hoc query through the policy-enforced
// secureread.Executor (REQ:server-acl-all-reads, REQ:dtql-query-type): a
// saved SQL query runs through RunNativeSQL, a saved or ad-hoc DTQL document
// through RunDTQL. It is the one implementation POST /datatug/exec/run_query
// and (indirectly, via the same dispatch shape) saved-query runs elsewhere
// must funnel through.
func RunQuery(ctx context.Context, request RunQueryRequest) (RunQueryResponse, error) {
	if err := request.Validate(); err != nil {
		return RunQueryResponse{}, err
	}
	executor, ok := SecureExecutor()
	if !ok {
		return RunQueryResponse{}, errors.New("run_query: server has no policy-enforced session configured")
	}
	store, err := storage.NewDatatugStore(request.StoreID)
	if err != nil {
		return RunQueryResponse{}, err
	}
	projStore := store.GetProjectStore(request.ProjectID)
	sourceURL, err := resolveSourceURL(ctx, projStore, request.Environment, request.Database)
	if err != nil {
		return RunQueryResponse{}, err
	}

	var result secureread.Result
	if request.DTQL != "" {
		result, err = executor.RunDTQL(ctx, sourceURL, []byte(request.DTQL), request.Parameters)
	} else {
		result, err = runSavedQuery(ctx, executor, projStore, request, sourceURL)
	}
	if err != nil {
		return RunQueryResponse{}, err
	}
	return RunQueryResponse{QueryResultResponse: resultToResponse(result), BindingsApplied: request.Parameters}, nil
}

// runSavedQuery loads request.QueryID from the project store and dispatches
// on its declared Type: SQL text through RunNativeSQL, DTQL through RunDTQL.
// Both read the query's document/text sidecar file directly (loadQueryDocument)
// since LoadQuery does not hydrate QueryDef.Text from it yet.
func runSavedQuery(ctx context.Context, executor *secureread.Executor, projStore datatug.ProjectStore, request RunQueryRequest, sourceURL string) (secureread.Result, error) {
	queryDef, err := projStore.LoadQuery(ctx, request.QueryID)
	if err != nil {
		return secureread.Result{}, fmt.Errorf("run_query: load query %q: %w", request.QueryID, err)
	}
	switch queryDef.Type {
	case datatug.QueryTypeSQL:
		text, err := loadQueryDocument(request.ProjectID, request.QueryID, queryDef.Type)
		if err != nil {
			return secureread.Result{}, err
		}
		return executor.RunNativeSQL(ctx, sourceURL, text)
	case datatug.QueryTypeDTQL:
		doc, err := loadQueryDocument(request.ProjectID, request.QueryID, queryDef.Type)
		if err != nil {
			return secureread.Result{}, err
		}
		return executor.RunDTQL(ctx, sourceURL, []byte(doc), request.Parameters)
	default:
		return secureread.Result{}, fmt.Errorf("run_query: query %q has type %q, which is not yet runnable through the policy-enforced path", request.QueryID, queryDef.Type)
	}
}
