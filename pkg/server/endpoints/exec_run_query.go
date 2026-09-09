package endpoints

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2http"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
)

// runQueryHandler is POST exec/run_query, rewritten (Task 12 / S64 item 3)
// to the appendix's exact ExecutionRequest -> Result envelope
// (api-contract.md "Endpoint table" / "Shared JSON types"). It replaces the
// previous api.RunQueryRequest/RunQueryResponse shape entirely — every
// caller (datatug-apps' query page, the journey harness, this repo's own
// security-matrix tests) must send the new envelope; see the PR body's
// inventory for what a legacy caller sees instead (nothing: this route's
// name/method is unchanged, only its body/response shape is, so an old
// caller gets a 400 INVALID_REQUEST from decodeContractBody's
// DisallowUnknownFields rather than a silently wrong response).
func runQueryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeContractError(w, r, newInvalidRequest("", "only POST is supported for exec/run_query"))
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeContractError(w, r, newInvalidRequest("", "failed to read request body: "+err.Error()))
		return
	}
	var req apicontract.ExecutionRequest
	if err := decodeContractBody(body, &req); err != nil {
		writeContractError(w, r, newInvalidRequest("", err.Error()))
		return
	}
	result, err := computeRunQuery(r.Context(), req)
	writeContractResponse(w, r, err, result)
}

// computeRunQuery is runQueryHandler's testable core.
func computeRunQuery(ctx context.Context, req apicontract.ExecutionRequest) (apicontract.Result, error) {
	if err := validateScope(apicontract.Scope{Project: req.Project, Environment: req.Environment, SecurityContextID: req.SecurityContextID}); err != nil {
		return apicontract.Result{}, err
	}
	if req.QueryID == "" && req.DTQL == "" {
		return apicontract.Result{}, newInvalidRequest("queryId", "exactly one of queryId or dtql is required")
	}
	if req.QueryID != "" && req.DTQL != "" {
		return apicontract.Result{}, newInvalidRequest("dtql", "dtql cannot be supplied together with queryId")
	}
	switch req.Mode {
	case "", apicontract.ProvenanceModeLive:
		req.Mode = apicontract.ProvenanceModeLive
	case apicontract.ProvenanceModeSnapshot:
	default:
		return apicontract.Result{}, newTypeMismatch("mode", fmt.Sprintf("must be %q or %q", apicontract.ProvenanceModeLive, apicontract.ProvenanceModeSnapshot))
	}
	if err := validateBindingOrigins(req); err != nil {
		return apicontract.Result{}, err
	}

	projDir, ok := api.ProjectDir(req.Project)
	if !ok {
		return apicontract.Result{}, newNotFound(fmt.Sprintf("unknown project %q", req.Project))
	}
	projStore, err := api.ProjectStoreFor(req.Project)
	if err != nil {
		return apicontract.Result{}, newInvalidRequest("project", err.Error())
	}
	executor, ok := api.SecureExecutor()
	if !ok {
		return apicontract.Result{}, newContractError(codeInternal, "server has no policy-enforced session configured", "")
	}

	var queryDef *datatug.QueryDef
	if req.QueryID != "" {
		queryDef, err = projStore.LoadQuery(ctx, req.QueryID)
		if err != nil {
			return apicontract.Result{}, newNotFound(fmt.Sprintf("query %q not found", req.QueryID))
		}
	}

	resolved, targetErr := resolveExecutionSource(ctx, projStore, projDir, req, queryDef)
	if targetErr != nil {
		return apicontract.Result{}, targetErr
	}

	variables, missing, mismatched := typedParametersToVariables(req.Parameters, queryDef)
	if len(missing) > 0 {
		sort.Strings(missing)
		return apicontract.Result{}, newMissingParameter(missing[0])
	}
	if len(mismatched) > 0 {
		sort.Strings(mismatched)
		return apicontract.Result{}, newTypeMismatch(mismatched[0], fmt.Sprintf("parameter %q does not match its declared type", mismatched[0]))
	}

	runCtx, cancel := context.WithTimeout(ctx, defaultExecTimeout)
	defer cancel()

	var result secureread.Result
	var profile string
	var collection string

	switch {
	case req.DTQL != "":
		profile = apicontract.ExecutionProfileProtected
		result, err = executor.RunDTQL(runCtx, resolved.URL, []byte(req.DTQL), variables)
	case queryDef.Type == datatug.QueryTypeDTQL:
		var doc string
		doc, err = api.LoadQueryDocument(req.Project, req.QueryID, queryDef.Type)
		if err == nil {
			profile = apicontract.ExecutionProfileProtected
			result, err = executor.RunDTQL(runCtx, resolved.URL, []byte(doc), variables)
		}
	case queryDef.Type == datatug.QueryTypeSQL:
		// The opaque-query-grant gate itself lives centrally in
		// secureread.Executor.RunNativeSQL (session.AllowOpaqueSQL), not
		// here: that is what makes it apply uniformly to exec/run_query AND
		// the legacy exec/select / exec/execute_commands routes, which
		// share the same Executor (api-contract.md "Security and errors":
		// "All legacy routes obey the same rule"). A missing grant surfaces
		// below as secureread.ErrOpaqueSQLNotGranted, mapped to
		// UNSUPPORTED_PROTECTED_EXECUTION.
		var text string
		text, err = api.LoadQueryDocument(req.Project, req.QueryID, queryDef.Type)
		if err == nil {
			profile = apicontract.ExecutionProfileOpaquePrivileged
			result, err = executor.RunNativeSQL(runCtx, resolved.URL, text, sqlQueryArgs(queryDef, variables)...)
		}
	case queryDef.Type == datatug.QueryTypeHTTP:
		profile = apicontract.ExecutionProfileProtected
		collection = queryDef.ID
		result, err = runHTTPQuery(runCtx, executor, resolved.URL, queryDef, variables)
	default:
		return apicontract.Result{}, newInvalidRequest("queryId", fmt.Sprintf("query %q has type %q, which exec/run_query does not support yet", req.QueryID, queryDef.Type))
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return apicontract.Result{}, newTimeout(fmt.Sprintf("execution exceeded the %s timeout", defaultExecTimeout))
		}
		if errors.Is(err, secureread.ErrOpaqueSQLNotGranted) {
			return apicontract.Result{}, newUnsupportedProtectedExecution(err.Error())
		}
		if errors.Is(err, secureread.ErrAccessDenied) {
			return apicontract.Result{}, contractErrAccessDenied(err.Error())
		}
		return apicontract.Result{}, newInvalidRequest("", err.Error())
	}
	if collection == "" {
		collection = req.Source
		if req.Source == "" {
			collection = resolved.Collection
		}
	}

	requestedLimit := 0
	if req.Limit != nil {
		requestedLimit = *req.Limit
	}
	limit := boundLimit(requestedLimit)
	truncated := false
	if len(result.Rows) > limit {
		result.Rows = result.Rows[:limit]
		truncated = true
	}

	recordset, err := toContractRecordset(result)
	if err != nil {
		return apicontract.Result{}, newContractError(codeInternal, err.Error(), "")
	}

	mode := apicontract.ProvenanceModeLive
	if result.Provenance != nil && result.Provenance.Source == dalgo2http.SourceSnapshot {
		mode = apicontract.ProvenanceModeSnapshot
	}

	return apicontract.Result{
		Recordset:       recordset,
		Limitations:     toContractLimitations(result.Limitations),
		BindingsApplied: bindingsApplied(req, queryDef),
		Provenance: apicontract.Provenance{
			Source: resolved.ID, Collection: collection, QueryID: req.QueryID,
			Mode: mode, SnapshotID: req.SnapshotID, ObservedAt: nowRFC3339UTC(), ExecutionProfile: profile,
		},
		Truncated: truncated,
	}, nil
}

// resolveExecutionSource applies api-contract.md's target-resolution rule
// (see resolver.go's EligibleTargets doc) to one ExecutionRequest: ad-hoc
// DTQL requires an explicit req.Source; a saved query obeys target
// resolution (one eligible target auto-selected and reported; several need
// an explicit, authorized req.Source or TARGET_REQUIRED; zero is
// SOURCE_UNAVAILABLE).
func resolveExecutionSource(ctx context.Context, projStore datatug.ProjectStore, projDir string, req apicontract.ExecutionRequest, queryDef *datatug.QueryDef) (api.ResolvedSource, error) {
	if req.DTQL != "" {
		if req.Source == "" {
			return api.ResolvedSource{}, newMissingParameter("source")
		}
		resolved, err := api.ResolveSource(ctx, projStore, projDir, req.Environment, req.Source)
		if err != nil {
			return api.ResolvedSource{}, newSourceUnavailable(err.Error())
		}
		return resolved, nil
	}

	eligible, err := api.EligibleTargets(ctx, projStore, projDir, req.Environment, queryDef)
	if err != nil {
		return api.ResolvedSource{}, newSourceUnavailable(err.Error())
	}
	if req.Source != "" {
		for _, s := range eligible {
			if s.ID == req.Source {
				return s, nil
			}
		}
		return api.ResolvedSource{}, newTargetRequired(
			fmt.Sprintf("source %q is not an authorized target for query %q", req.Source, req.QueryID), targetOptions(candidateTargets(eligible)))
	}
	switch len(eligible) {
	case 0:
		return api.ResolvedSource{}, newSourceUnavailable(fmt.Sprintf("query %q has no eligible source in environment %q", req.QueryID, req.Environment))
	case 1:
		return eligible[0], nil
	default:
		return api.ResolvedSource{}, newTargetRequired(
			fmt.Sprintf("query %q has more than one eligible target; select one explicitly", req.QueryID), targetOptions(candidateTargets(eligible)))
	}
}

func candidateTargets(sources []api.ResolvedSource) []apicontract.CandidateTarget {
	out := make([]apicontract.CandidateTarget, len(sources))
	for i, s := range sources {
		out[i] = apicontract.CandidateTarget{Source: s.ID, Label: s.Label}
	}
	return out
}

// validateBindingOrigins checks api-contract.md's "bindingOrigins supplies
// display provenance, is checked for exactly the submitted keys": every
// Parameters key has exactly one BindingOrigins entry, and vice versa.
func validateBindingOrigins(req apicontract.ExecutionRequest) error {
	origins := make(map[string]bool, len(req.BindingOrigins))
	for _, o := range req.BindingOrigins {
		if origins[o.ParameterID] {
			return newInvalidRequest("bindingOrigins", fmt.Sprintf("duplicate bindingOrigins entry for parameter %q", o.ParameterID))
		}
		origins[o.ParameterID] = true
		switch o.Origin {
		case apicontract.BindingOriginSelection, apicontract.BindingOriginContext, apicontract.BindingOriginManual, apicontract.BindingOriginDefault:
		default:
			return newInvalidRequest("bindingOrigins", fmt.Sprintf("parameter %q has unknown origin %q", o.ParameterID, o.Origin))
		}
	}
	for paramID := range req.Parameters {
		if !origins[paramID] {
			return newInvalidRequest("bindingOrigins", fmt.Sprintf("parameter %q has no matching bindingOrigins entry", paramID))
		}
	}
	if len(origins) != len(req.Parameters) {
		return newInvalidRequest("bindingOrigins", "bindingOrigins must name exactly the submitted parameters, no more")
	}
	return nil
}

// typedParametersToVariables converts req.Parameters (TypedValue) into the
// plain map[string]any accesspolicies.Run/dtql substitution expects
// (Executor.RunDTQL/RunStructured's variables parameter), validating each
// value converts cleanly and, when queryDef is known, that its declared
// type matches. missing lists required declared parameters with no
// supplied value; mismatched lists parameters whose TypedValue.Type
// disagrees with the query's own declared ParameterDef.Type.
func typedParametersToVariables(params map[string]apicontract.TypedValue, queryDef *datatug.QueryDef) (variables map[string]any, missing, mismatched []string) {
	variables = make(map[string]any, len(params))
	for name, tv := range params {
		native, err := nativeValue(tv)
		if err != nil {
			mismatched = append(mismatched, name)
			continue
		}
		variables[name] = native
	}
	if queryDef == nil {
		return variables, missing, mismatched
	}
	for _, p := range queryDef.Parameters {
		tv, supplied := params[p.ID]
		if !supplied {
			if p.IsRequired {
				missing = append(missing, p.ID)
			}
			continue
		}
		if expected, ok := declaredValueType(p.Type); ok && tv.Type != expected {
			mismatched = append(mismatched, p.ID)
		}
	}
	return variables, missing, mismatched
}

// declaredValueType maps a ParameterDef.Type string ("integer", "string",
// "number", "boolean", "date", "datetime", "decimal") onto the matching
// apicontract.ValueType, when this codebase declares one it recognizes. An
// unrecognized type string (ok == false) is not checked — api-contract.md:
// "Type names for parameters/columns map explicitly to existing core
// definitions; unsupported types fail validation rather than being
// converted silently" — but that mapping/validation lives in datatug-core's
// own type registry, not duplicated here; this resolver only checks the
// types it already understands.
func declaredValueType(t string) (apicontract.ValueType, bool) {
	switch t {
	case "integer":
		return apicontract.ValueTypeInteger, true
	case "string":
		return apicontract.ValueTypeString, true
	case "number":
		return apicontract.ValueTypeNumber, true
	case "boolean", "bit":
		return apicontract.ValueTypeBoolean, true
	case "date":
		return apicontract.ValueTypeDate, true
	case "datetime":
		return apicontract.ValueTypeDatetime, true
	case "decimal":
		return apicontract.ValueTypeDecimal, true
	default:
		return "", false
	}
}

// sqlQueryArgs builds the []dal.QueryArg RunNativeSQL binds as real driver
// arguments (dal-go/dalgo2sql v0.11.7+, sql.Named — see native_sql.go's own
// doc comment), one named arg per queryDef.Parameters entry the caller
// supplied a value for. datatug-demo-projects/demo-project-1's own SQL
// queries already write "@ParamName" placeholders (e.g.
// "WHERE i.CustomerId = @CustomerId") — the exact named-placeholder form
// dalgo2sql converts a named QueryArg into — so no new query-authoring
// convention is introduced here, only real binding for the one that
// already existed textually.
func sqlQueryArgs(queryDef *datatug.QueryDef, variables map[string]any) []dal.QueryArg {
	var args []dal.QueryArg
	for _, p := range queryDef.Parameters {
		if value, ok := variables[p.ID]; ok {
			args = append(args, dal.QueryArg{Name: p.ID, Value: value})
		}
	}
	return args
}

// bindingsApplied builds the appendix's execution-confirmed
// bindingsApplied[] (api-contract.md: "returns only parameters actually
// applied by the executor"; "cannot echo supplied parameter values as
// applied before execution confirms them"): called only after Executor.Run*
// has already succeeded, so every parameter here really did reach that
// successful execution. For a saved query, "actually applied" means
// declared on the query itself (queryDef.Parameters); for ad-hoc DTQL,
// every supplied parameter is reported (no query definition exists to
// narrow against — see this file's Task-13-deferred note on deeper AST-level
// confirmation).
func bindingsApplied(req apicontract.ExecutionRequest, queryDef *datatug.QueryDef) []apicontract.Binding {
	origins := make(map[string]apicontract.BindingOriginEntry, len(req.BindingOrigins))
	for _, o := range req.BindingOrigins {
		origins[o.ParameterID] = o
	}
	var ids []string
	if queryDef != nil {
		declared := make(map[string]bool, len(queryDef.Parameters))
		for _, p := range queryDef.Parameters {
			declared[p.ID] = true
		}
		for id := range req.Parameters {
			if declared[id] {
				ids = append(ids, id)
			}
		}
	} else {
		for id := range req.Parameters {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	bindings := make([]apicontract.Binding, 0, len(ids))
	for _, id := range ids {
		origin := origins[id]
		bindings = append(bindings, apicontract.Binding{
			ParameterID: id, Value: req.Parameters[id], Origin: origin.Origin,
			OriginEvidence: apicontract.BindingOriginEvidenceClientReported, FactID: origin.FactID,
		})
	}
	return bindings
}

// runHTTPQuery is exec_run_query's HTTP-typed saved-query dispatch: one
// dal.WhereField equality per supplied declared parameter, run through the
// same policy-enforced Executor.RunStructured every other source uses —
// mirroring apps/datatugapp/commands/cmd_query_run_saved.go's
// runHTTPSavedQuery (the CLI's own working equivalent) rather than
// reinventing it. Full bounded/SSRF-hardened HTTP execution (address/
// redirect/size/time budgets beyond dalgo2http's own defaults, explicit
// snapshot dispatch) is plan task 14's scope ("Complete bounded HTTP
// browser execution"); this is the minimum needed so exec/run_query does
// not simply fail every HTTP-typed queryId, per AC:real-transport-and-
// parameter-effect's "HTTP targets resolve without a fake database".
func runHTTPQuery(ctx context.Context, executor *secureread.Executor, sourceURL string, queryDef *datatug.QueryDef, variables map[string]any) (secureread.Result, error) {
	var builder dal.IQueryBuilder = dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef(queryDef.ID, "")))
	var missing []string
	for _, p := range queryDef.Parameters {
		value, ok := variables[p.ID]
		if !ok {
			if p.IsRequired {
				missing = append(missing, p.ID)
			}
			continue
		}
		builder = builder.WhereField(p.ID, dal.Equal, value)
	}
	if len(missing) > 0 {
		return secureread.Result{}, fmt.Errorf("query %q: missing required parameter(s): %s", queryDef.ID, strings.Join(missing, ", "))
	}
	return executor.RunStructured(ctx, sourceURL, builder.SelectColumns(), nil)
}
