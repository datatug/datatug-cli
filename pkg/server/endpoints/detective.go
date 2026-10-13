package endpoints

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-core/pkg/apicontract"
)

func detectiveRoutes(path string, router router, wrap wrapper, writeOnly bool, caps Capabilities) {
	if !writeOnly {
		route(router, wrap, http.MethodGet, path+"/detective/state", detectiveStateHandler)
		route(router, wrap, http.MethodGet, path+"/detective/case-events", detectiveCaseEventsHandler)
	}
	route(router, wrap, http.MethodPost, path+"/detective/action", requireWriteCapability(caps, detectiveActionHandler))
}

func detectiveCaseEventsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	store, _, err := detectiveStore(r.URL.Query().Get("project"), r.URL.Query().Get("securityContextId"))
	if err != nil {
		writeContractError(w, r, err)
		return
	}
	before := 0
	if value := r.URL.Query().Get("before"); value != "" {
		before, err = strconv.Atoi(value)
		if err != nil {
			writeContractError(w, r, newInvalidRequest("before", "invalid timeline cursor"))
			return
		}
	}
	page, err := store.CaseEventsPage(r.Context(), r.URL.Query().Get("caseId"), before)
	writeContractResponse(w, r, detectiveError(err), page)
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
	case "apply-scenario":
		var c detective.Case
		c, err = store.ApplyScenario(r.Context(), req.CaseID, req.ScenarioID, actor)
		if err == nil {
			response.Case = &c
		}
	case "record-scenario-step":
		var c detective.Case
		if req.Execution == nil {
			c, err = store.RecordScenarioStep(r.Context(), req.CaseID, req.ApplicationID, req.StepID, req.StepStatus, req.RunID, actor)
		} else if req.StepStatus != "completed" || req.RunID != "" {
			err = detective.ErrInvalid
		} else {
			var proof detective.ScenarioQueryProof
			proof, err = loadDetectiveScenarioQueryProof(r.Context(), req.Project, actor, req.Execution)
			if err == nil {
				c, err = store.RecordScenarioQueryStep(r.Context(), req.CaseID, req.ApplicationID, req.StepID, proof, actor)
			}
		}
		if err == nil {
			response.Case = &c
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
		if req.Execution == nil {
			run, err = store.RunQueryValidators(r.Context(), canonical, req.Environment, req.CaseID, actor, req.Inputs, detectiveQueryRunner(req.Project, req.SecurityContextID), detectiveExecutionMetadata(req.Project))
		} else {
			var result apicontract.Result
			var inputs map[string]detective.InputValue
			result, inputs, err = loadDetectiveExecutionResult(r.Context(), req.Project, canonical, req.Environment, req.Execution, req.Inputs)
			if err == nil {
				run, err = store.RunQueryValidatorsForResult(r.Context(), canonical, req.Environment, req.CaseID, actor, inputs, result, detectiveQueryRunner(req.Project, req.SecurityContextID), detectiveExecutionMetadata(req.Project))
			}
		}
		if err == nil {
			response.Run = &run
		}
	default:
		err = detective.ErrInvalid
	}
	writeContractResponse(w, r, detectiveError(err), response)
}

func loadDetectiveScenarioQueryProof(ctx context.Context, project, actor string, ref *apicontract.ExecutionRef) (detective.ScenarioQueryProof, error) {
	if ref == nil || ref.Validate() != nil || ref.ProjectID != project {
		return detective.ScenarioQueryProof{}, newInvalidRequest("execution", "invalid execution reference")
	}
	store, err := api.ExecutionEvidenceStoreByID(project, ref.StoreID)
	if err != nil {
		return detective.ScenarioQueryProof{}, newNotFound("execution store not found")
	}
	record, err := store.Execution(ctx, *ref)
	if err != nil {
		return detective.ScenarioQueryProof{}, newNotFound("execution not found")
	}
	if record.Ref != *ref || record.Scope.Project != project || record.QueryID == "" || record.Scope.Environment == "" || record.Principal.ID != actor || record.ResultComplete == nil {
		return detective.ScenarioQueryProof{}, newAccessDenied("execution receipt does not match the current project and principal")
	}
	return detective.ScenarioQueryProof{Execution: *ref, QueryID: record.QueryID, Environment: record.Scope.Environment, ExecutionStatus: "completed"}, nil
}

// A row overlay is safe only when the retained receipt still has the same
// ordered rows and columns after current policy is reapplied. The client can
// select the receipt but cannot supply either its rows or its parameters.
func loadDetectiveExecutionResult(ctx context.Context, project, queryID, environment string, ref *apicontract.ExecutionRef, supplied map[string]detective.InputValue) (apicontract.Result, map[string]detective.InputValue, error) {
	if ref == nil || ref.Validate() != nil || ref.ProjectID != project || len(supplied) != 0 {
		return apicontract.Result{}, nil, newInvalidRequest("execution", "invalid execution reference or inputs")
	}
	store, err := api.ExecutionEvidenceStoreByID(project, ref.StoreID)
	if err != nil {
		return apicontract.Result{}, nil, newNotFound("execution store not found")
	}
	record, err := store.Execution(ctx, *ref)
	if err != nil {
		return apicontract.Result{}, nil, newNotFound("execution not found")
	}
	if record.QueryID != queryID || record.Scope.Project != project || record.Scope.Environment != environment || record.Ref != *ref {
		return apicontract.Result{}, nil, newInvalidRequest("execution", "execution does not match the requested query and environment")
	}
	if record.SnapshotRef == "" {
		return apicontract.Result{}, nil, newSnapshotExpired("the execution has no retained row snapshot")
	}
	snapshot, err := store.Snapshot(ctx, *ref, record.SnapshotRef)
	retained, err := readableCompareSnapshot(snapshot, err)
	if err != nil {
		return apicontract.Result{}, nil, err
	}
	if record.ResultComplete == nil || !*record.ResultComplete {
		return apicontract.Result{}, nil, newSourceUnavailable("the execution cannot prove complete retained rows")
	}
	if len(retained.Rows) != record.RowCount {
		return apicontract.Result{}, nil, newSourceUnavailable("retained rows do not match the execution receipt")
	}
	fingerprint, err := apicontract.FingerprintRecordset(retained)
	if err != nil || fingerprint != record.ResultFingerprint {
		return apicontract.Result{}, nil, newSourceUnavailable("retained rows do not match the execution fingerprint")
	}
	executor, ok := compareSecureExecutorHook()
	if !ok {
		return apicontract.Result{}, nil, newContractError(codeInternal, "server has no policy-enforced session configured", "")
	}
	filtered, err := compareRunSnapshotHook(executor, ctx, record.Provenance.Collection, retained)
	if err != nil {
		if isCurrentPolicyDenial(err) {
			return apicontract.Result{}, nil, newAccessDenied("current access policy does not permit this recorded evidence")
		}
		return apicontract.Result{}, nil, newContractError(codeInternal, "apply current policy to recorded Detective evidence", "")
	}
	if filtered.SnapshotRecordset == nil {
		return apicontract.Result{}, nil, newContractError(codeInternal, "shape filtered Detective snapshot", "")
	}
	if !reflect.DeepEqual(retained, *filtered.SnapshotRecordset) {
		return apicontract.Result{}, nil, newSourceUnavailable("current policy changed the recorded rows or columns")
	}
	limitations := append([]apicontract.Limitation(nil), record.Limitations...)
	limitations = append(limitations, snapshot.Limitations...)
	limitations = append(limitations, recordedSnapshotRetentionLimitations(record, retained)...)
	limitations = append(limitations, toContractLimitations(filtered.Limitations)...)
	inputs := make(map[string]detective.InputValue, len(record.Parameters))
	for name, value := range record.Parameters {
		if value.Validate() != nil {
			return apicontract.Result{}, nil, newSourceUnavailable("recorded query inputs are invalid")
		}
		inputs[name] = detective.InputValue{Scalar: value.Scalar, Set: value.Set}
	}
	result := apicontract.Result{Recordset: retained, Limitations: normalizeLimitations(limitations), BindingsApplied: record.BindingsApplied, Provenance: record.Provenance, Truncated: snapshot.Truncated, Execution: ref}
	if result.Recordset.Validate() == nil {
		return result, inputs, nil
	}
	return apicontract.Result{}, nil, newSourceUnavailable("recorded query result is invalid")
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
	case errors.Is(err, detective.ErrCapacity):
		return newInvalidRequest("body", "Detective catalog capacity reached")
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
