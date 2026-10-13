package endpoints

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-core/pkg/apicontract"
)

func detectiveRoutes(path string, router router, wrap wrapper, writeOnly bool, caps Capabilities) {
	if !writeOnly {
		route(router, wrap, http.MethodGet, path+"/detective/state", detectiveStateHandler)
	}
	route(router, wrap, http.MethodPost, path+"/detective/action", requireWriteCapability(caps, detectiveActionHandler))
}

func detectiveStore(project, securityContextID string) (*detective.LocalStore, string, error) {
	if project == "" || api.ValidateProjectIdentifier("project", project) != nil {
		return nil, "", newInvalidRequest("project", "invalid project")
	}
	if securityContextID == "" || !api.ValidateSecurityContext(securityContextID) {
		return nil, "", newStaleContext("securityContextId does not match the current agent session")
	}
	projectDir, ok := api.ProjectDir(project)
	if !ok {
		return nil, "", newNotFound("project not found")
	}
	// The current policy engine has a query-scoped asset grant, not a project
	// Case/Check resource grant. A query grant cannot authorize Case context.
	// Until that grant exists, secured profiles fail closed; explicit local
	// --no-policies owner mode alone can access this local private store.
	if !api.SecureSessionUnrestricted() {
		return nil, "", newAccessDenied("Detective project assets require a project asset grant")
	}
	actor := api.SecurePrincipalID()
	if actor == "" {
		actor = "local-owner"
	}
	base, err := executionstore.ResolvePrivateDir(api.GetCapabilities().EvidencePrivateDir)
	if err != nil {
		return nil, "", err
	}
	store, err := detective.NewLocalStore(project, projectDir, base)
	return store, actor, err
}

func detectiveStateHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	store, _, err := detectiveStore(r.URL.Query().Get("project"), r.URL.Query().Get("securityContextId"))
	if err != nil {
		writeContractError(w, r, err)
		return
	}
	state, err := store.StatePage(r.Context(), r.URL.Query().Get("runsBefore"))
	state.Capabilities.CanWrite = api.GetCapabilities().AllowWrites
	state.Capabilities.CanRun = api.GetCapabilities().AllowWrites
	writeContractResponse(w, r, detectiveError(err), state)
}

func detectiveActionHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !api.GetCapabilities().AllowWrites {
		writeContractError(w, r, newAccessDenied("this agent was started without --allow-writes"))
		return
	}
	var req detective.ActionRequest
	if err := readIncidentBody(w, r, &req); err != nil {
		writeContractError(w, r, err)
		return
	}
	if req.Project != r.URL.Query().Get("project") {
		writeContractError(w, r, newInvalidRequest("project", "URL and body project differ"))
		return
	}
	store, actor, err := detectiveStore(req.Project, req.SecurityContextID)
	if err != nil {
		writeContractError(w, r, err)
		return
	}
	var response detective.ActionResponse
	switch req.Type {
	case "save-check":
		if req.Check == nil {
			err = detective.ErrInvalid
			break
		}
		var check detective.Check
		check, err = store.SaveCheck(r.Context(), *req.Check, actor)
		if err == nil {
			response.Check = &check
		}
	case "save-validator":
		if req.Validator == nil {
			err = detective.ErrInvalid
			break
		}
		var validator detective.PublishedValidator
		validator, err = store.SaveValidator(r.Context(), *req.Validator, actor)
		if err == nil {
			response.Validator = &validator
		}
	case "save-query-validators":
		if req.QueryValidators == nil {
			err = detective.ErrInvalid
			break
		}
		projectDir, _ := api.ProjectDir(req.Project)
		canonical, resolveErr := api.ResolveQueryID(projectDir, req.QueryValidators.QueryID)
		if resolveErr != nil {
			err = newInvalidRequest("queryId", "saved query is unavailable")
			break
		}
		item := *req.QueryValidators
		item.QueryID = canonical
		var saved detective.QueryValidators
		saved, err = store.SaveQueryValidators(r.Context(), item, actor)
		if err == nil {
			response.QueryValidators = &saved
		}
	case "save-scenario":
		if req.Scenario == nil {
			err = detective.ErrInvalid
			break
		}
		var scenario detective.Scenario
		scenario, err = store.SaveScenario(r.Context(), *req.Scenario, actor)
		if err == nil {
			response.Scenario = &scenario
		}
	case "create-case", "update-case":
		if req.Case == nil {
			err = detective.ErrInvalid
			break
		}
		var c detective.Case
		c, err = store.SaveCase(r.Context(), *req.Case, actor, req.Type)
		if err == nil {
			response.Case = &c
		}
	case "append-event":
		if req.Event == nil {
			err = detective.ErrInvalid
			break
		}
		var c detective.Case
		c, err = store.AppendCaseEvent(r.Context(), req.CaseID, *req.Event, actor)
		if err == nil {
			response.Case = &c
		}
	case "run-check":
		if req.Environment == "" || api.ValidateIdentifier("environment", req.Environment) != nil {
			err = detective.ErrInvalid
			break
		}
		var run detective.CheckRun
		run, err = store.Run(r.Context(), req.CheckID, req.Environment, req.CaseID, actor, req.Inputs, detectiveQueryRunner(req.Project, req.SecurityContextID), detectiveExecutionMetadata(req.Project))
		if err == nil {
			response.Run = &run
		}
	case "run-query-validators":
		if req.Environment == "" || api.ValidateIdentifier("environment", req.Environment) != nil {
			err = detective.ErrInvalid
			break
		}
		projectDir, _ := api.ProjectDir(req.Project)
		canonical, resolveErr := api.ResolveQueryID(projectDir, req.QueryID)
		if resolveErr != nil {
			err = newInvalidRequest("queryId", "saved query is unavailable")
			break
		}
		var run detective.CheckRun
		run, err = store.RunQueryValidators(r.Context(), canonical, req.Environment, req.CaseID, actor, req.Inputs, detectiveQueryRunner(req.Project, req.SecurityContextID), detectiveExecutionMetadata(req.Project))
		if err == nil {
			response.Run = &run
		}
	default:
		err = detective.ErrInvalid
	}
	writeContractResponse(w, r, detectiveError(err), response)
}

func detectiveQueryRunner(project, securityContextID string) detective.QueryRunner {
	return func(ctx context.Context, queryID, environment string, params map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		origins := make([]apicontract.BindingOriginEntry, 0, len(params))
		for name := range params {
			origins = append(origins, apicontract.BindingOriginEntry{ParameterID: name, Origin: apicontract.BindingOriginManual})
		}
		limit := 500
		result, runErr := computeRunQuery(ctx, apicontract.ExecutionRequest{Project: project, Environment: environment, SecurityContextID: securityContextID, QueryID: queryID, Parameters: params, BindingOrigins: origins, Mode: apicontract.ProvenanceModeLive, Limit: &limit, Record: true})
		if runErr != nil {
			var contract *contractError
			if errors.As(runErr, &contract) {
				return apicontract.Result{}, &detective.QueryFailure{Code: string(contract.Code)}
			}
			return apicontract.Result{}, &detective.QueryFailure{Code: "INTERNAL"}
		}
		return result, nil
	}
}

func detectiveExecutionMetadata(project string) detective.ExecutionMetadata {
	return func(ctx context.Context, ref *apicontract.ExecutionRef) (string, string, error) {
		if ref == nil || ref.ProjectID != project {
			return "", "", detective.ErrInvalid
		}
		store, err := api.ExecutionEvidenceStoreByID(project, ref.StoreID)
		if err != nil {
			return "", "", err
		}
		record, err := store.Execution(ctx, *ref)
		if err != nil {
			return "", "", err
		}
		return record.QueryRevision, record.ResultFingerprint, nil
	}
}

func detectiveError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, detective.ErrConflict):
		return newContractError(apicontract.ErrCodeRevisionConflict, "item changed; reload before saving", "version")
	case errors.Is(err, detective.ErrNotFound):
		return newNotFound("Detective item not found")
	case errors.Is(err, detective.ErrInvalid):
		return newInvalidRequest("body", "invalid Detective item or action")
	case errors.Is(err, detective.ErrOutcomeUncertain):
		return newInvalidRequest("body", "operation may have been saved; reload and review")
	case errors.Is(err, context.DeadlineExceeded):
		return newInvalidRequest("body", "Detective store is busy; retry")
	}
	if strings.Contains(err.Error(), "limit") {
		return newInvalidRequest("body", "Detective storage or execution limit reached")
	}
	return newContractError(codeInternal, "Detective operation failed", "")
}
