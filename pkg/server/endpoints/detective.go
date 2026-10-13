package endpoints

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
)

func detectiveRoutes(path string, router router, wrap wrapper, writeOnly bool, caps Capabilities) {
	if !writeOnly {
		route(router, wrap, http.MethodGet, path+"/detective/state", detectiveStateHandler)
		route(router, wrap, http.MethodGet, path+"/detective/case-events", detectiveCaseEventsHandler)
		route(router, wrap, http.MethodGet, path+"/detective/incidents", detectiveIncidentsHandler)
		route(router, wrap, http.MethodGet, path+"/detective/knowledge_proposals", detectiveKnowledgeProposalsHandler)
		route(router, wrap, http.MethodGet, path+"/detective/ai_proposals", detectiveAIProposalsHandler)
	}
	route(router, wrap, http.MethodPost, path+"/detective/action", requireWriteCapability(caps, detectiveActionHandler))
	route(router, wrap, http.MethodPost, path+"/detective/incident-action", requireWriteCapability(caps, detectiveIncidentActionHandler))
	route(router, wrap, http.MethodPost, path+"/detective/knowledge_proposal_action", requireWriteCapability(caps, detectiveKnowledgeActionHandler))
	route(router, wrap, http.MethodPost, path+"/detective/ai_proposals", requireWriteCapability(caps, detectiveAIProposalsHandler))
	route(router, wrap, http.MethodPost, path+"/detective/ai_review", requireWriteCapability(caps, detectiveAIReviewHandler))
	route(router, wrap, http.MethodPost, path+"/detective/ai_confirm", requireWriteCapability(caps, detectiveAIConfirmHandler))
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
	if err == nil {
		state.Environments, err = detectiveEnvironments(r.Context(), r.URL.Query().Get("project"))
	}
	state.Capabilities.CanWrite = api.GetCapabilities().AllowWrites
	state.Capabilities.CanRun = api.GetCapabilities().AllowWrites
	writeContractResponse(w, r, detectiveError(err), state)
}

func detectiveEnvironments(ctx context.Context, project string) ([]string, error) {
	projectStore, err := api.ProjectStoreFor(project)
	if err != nil {
		return nil, newNotFound("project not found")
	}
	items, err := projectStore.LoadEnvironments(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if item != nil {
			result = append(result, item.ID)
		}
	}
	return result, nil
}

func detectiveActionHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !api.GetCapabilities().AllowWrites {
		writeContractError(w, r, newAccessDenied("this agent was started without --allow-writes"))
		return
	}
	var body struct {
		OperationID string `json:"operationId"`
		detective.ActionRequest
	}
	if err := readIncidentBody(w, r, &body); err != nil {
		writeContractError(w, r, err)
		return
	}
	req := body.ActionRequest
	if req.Project != r.URL.Query().Get("project") {
		writeContractError(w, r, newInvalidRequest("project", "URL and body project differ"))
		return
	}
	if body.OperationID == "" {
		writeContractError(w, r, newInvalidRequest("operationId", "operation ID is required for replay-safe Detective actions"))
		return
	}
	store, actor, err := detectiveStore(req.Project, req.SecurityContextID)
	if err != nil {
		writeContractError(w, r, err)
		return
	}
	var result *apicontract.Result
	var proof *detective.ScenarioQueryProof
	switch req.Type {
	case "save-query-validators":
		if req.QueryValidators != nil {
			projectDir, _ := api.ProjectDir(req.Project)
			canonical, resolveErr := api.ResolveQueryID(projectDir, req.QueryValidators.QueryID)
			if resolveErr != nil {
				err = newInvalidRequest("queryId", "saved query is unavailable")
			} else {
				req.QueryValidators.QueryID = canonical
			}
		}
	case "run-check":
		if req.Environment == "" || api.ValidateIdentifier("environment", req.Environment) != nil {
			err = detective.ErrInvalid
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
		req.QueryID = canonical
		if req.Execution != nil {
			var retained apicontract.Result
			retained, req.Inputs, err = loadDetectiveExecutionResult(r.Context(), req.Project, actor, canonical, req.Environment, req.Execution, req.Inputs)
			result = &retained
		}
	case "record-scenario-step":
		if req.Execution != nil {
			if req.StepStatus != "completed" || req.RunID != "" || len(req.Inputs) != 0 {
				err = detective.ErrInvalid
			} else {
				var verified detective.ScenarioQueryProof
				verified, req.Inputs, err = loadDetectiveScenarioQueryProof(r.Context(), req.Project, actor, req.Execution)
				if err == nil && (req.QueryID != "" && req.QueryID != verified.QueryID || req.Environment != "" && req.Environment != verified.Environment) {
					err = newInvalidRequest("execution", "execution does not match the requested query and environment")
				}
				proof = &verified
			}
		}
	}
	if err != nil {
		writeContractResponse(w, r, detectiveError(err), detective.ActionResponse{})
		return
	}
	response, err := store.ExecuteAction(r.Context(), body.OperationID, actor, req, detectiveQueryRunner(req.Project, req.SecurityContextID), detectiveExecutionMetadata(req.Project), result, proof, detectiveQueryRevisionLookup(req.Project))
	linkStatus := ""
	if err == nil && (req.Type == "run-check" || req.Type == "run-query-validators") {
		linkStatus, err = store.ActionLinkStatus(r.Context(), body.OperationID, actor, req)
	}
	writeContractResponse(w, r, detectiveError(err), struct {
		detective.ActionResponse
		Receipt        string `json:"receipt,omitempty"`
		CaseLinkStatus string `json:"caseLinkStatus,omitempty"`
	}{response, body.OperationID, linkStatus})
}

func loadDetectiveScenarioQueryProof(ctx context.Context, project, actor string, ref *apicontract.ExecutionRef) (detective.ScenarioQueryProof, map[string]detective.InputValue, error) {
	if ref == nil || ref.Validate() != nil || ref.ProjectID != project {
		return detective.ScenarioQueryProof{}, nil, newInvalidRequest("execution", "invalid execution reference")
	}
	store, err := api.ExecutionEvidenceStoreByID(project, ref.StoreID)
	if err != nil {
		return detective.ScenarioQueryProof{}, nil, newNotFound("execution store not found")
	}
	record, err := store.Execution(ctx, *ref)
	if err != nil {
		return detective.ScenarioQueryProof{}, nil, newNotFound("execution not found")
	}
	if record.Ref != *ref || record.Scope.Project != project || record.QueryID == "" || record.Scope.Environment == "" || record.Principal.ID != actor || record.ResultComplete == nil || !*record.ResultComplete {
		return detective.ScenarioQueryProof{}, nil, newAccessDenied("execution receipt does not match the current project and principal")
	}
	inputs, err := recordedDetectiveInputs(record)
	if err != nil {
		return detective.ScenarioQueryProof{}, nil, err
	}
	return detective.ScenarioQueryProof{Execution: *ref, QueryID: record.QueryID, Environment: record.Scope.Environment, ExecutionStatus: "completed"}, inputs, nil
}

// A row overlay is safe only when the retained receipt still has the same
// ordered rows and columns after current policy is reapplied. The client can
// select the receipt but cannot supply either its rows or its parameters.
func loadDetectiveExecutionResult(ctx context.Context, project, actor, queryID, environment string, ref *apicontract.ExecutionRef, supplied map[string]detective.InputValue) (apicontract.Result, map[string]detective.InputValue, error) {
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
	if record.QueryID != queryID || record.Scope.Project != project || record.Scope.Environment != environment || record.Ref != *ref || record.Principal.ID != actor {
		return apicontract.Result{}, nil, newInvalidRequest("execution", "execution does not match the requested query and environment")
	}
	if record.ResultComplete == nil || !*record.ResultComplete {
		return apicontract.Result{}, nil, newSourceUnavailable("the execution cannot prove complete retained rows")
	}
	retained, transient := detectiveTransientRows.get(*ref, actor, time.Now())
	var snapshot apicontract.SnapshotReadResponse
	if !transient {
		if record.SnapshotRef == "" {
			return apicontract.Result{}, nil, newSnapshotExpired("the execution has no current transient rows or retained row snapshot")
		}
		var err error
		snapshot, err = store.Snapshot(ctx, *ref, record.SnapshotRef)
		retained, err = readableCompareSnapshot(snapshot, err)
		if err != nil {
			return apicontract.Result{}, nil, err
		}
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
	if !transient {
		limitations = append(limitations, snapshot.Limitations...)
		limitations = append(limitations, recordedSnapshotRetentionLimitations(record, retained)...)
	}
	limitations = append(limitations, toContractLimitations(filtered.Limitations)...)
	inputs, err := recordedDetectiveInputs(record)
	if err != nil {
		return apicontract.Result{}, nil, err
	}
	result := apicontract.Result{Recordset: retained, Limitations: normalizeLimitations(limitations), BindingsApplied: record.BindingsApplied, Provenance: record.Provenance, Truncated: !*record.ResultComplete, Execution: ref}
	if result.Recordset.Validate() == nil {
		return result, inputs, nil
	}
	return apicontract.Result{}, nil, newSourceUnavailable("recorded query result is invalid")
}

func recordedDetectiveInputs(record apicontract.ExecutionRecord) (map[string]detective.InputValue, error) {
	inputs := make(map[string]detective.InputValue, len(record.Parameters))
	for name, value := range record.Parameters {
		if value.Validate() != nil {
			return nil, newSourceUnavailable("recorded query inputs are invalid")
		}
		inputs[name] = detective.InputValue{Scalar: value.Scalar, Set: value.Set}
	}
	return inputs, nil
}

func detectiveQueryRunner(project, securityContextID string) detective.QueryRunner {
	return func(ctx context.Context, queryID, environment string, params map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		revision, admitted := detective.AdmittedQueryRevision(ctx, queryID)
		if !admitted {
			return apicontract.Result{}, &detective.QueryFailure{Code: "SOURCE_UNAVAILABLE"}
		}
		origins := make([]apicontract.BindingOriginEntry, 0, len(params))
		for name := range params {
			origins = append(origins, apicontract.BindingOriginEntry{ParameterID: name, Origin: apicontract.BindingOriginManual})
		}
		limit := 500
		result, runErr := computeRunQueryWithOptions(ctx, apicontract.ExecutionRequest{Project: project, Environment: environment, SecurityContextID: securityContextID, QueryID: queryID, Parameters: params, BindingOrigins: origins, Mode: apicontract.ProvenanceModeLive, Limit: &limit, Record: true}, runQueryOptions{expectedQueryRevision: revision})
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

func detectiveQueryRevisionLookup(project string) detective.QueryRevisionLookup {
	return func(ctx context.Context, queryID string) (string, error) {
		projectDir, ok := api.ProjectDir(project)
		if !ok {
			return "", newNotFound("project not found")
		}
		canonicalID, err := api.ResolveQueryID(projectDir, queryID)
		if err != nil {
			return "", newNotFound("saved query not found")
		}
		store, err := api.ProjectStoreFor(project)
		if err != nil {
			return "", newNotFound("project not found")
		}
		revisioned, ok := store.(datatug.RevisionedQueriesStore)
		if !ok {
			return "", newContractError(codeInternal, "project store cannot attest query revisions", "")
		}
		stored, err := revisioned.LoadQueryRevision(ctx, canonicalID)
		if err != nil {
			return "", newNotFound("saved query not found")
		}
		return string(stored.Revision), nil
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
		return newContractError("OUTCOME_UNCERTAIN", "operation outcome is uncertain; review state and use a new operation ID for another attempt", "operationId")
	case errors.Is(err, context.DeadlineExceeded):
		return newInvalidRequest("body", "Detective store is busy; retry")
	}
	if strings.Contains(err.Error(), "limit") {
		return newInvalidRequest("body", "Detective storage or execution limit reached")
	}
	return newContractError(codeInternal, "Detective operation failed", "")
}
