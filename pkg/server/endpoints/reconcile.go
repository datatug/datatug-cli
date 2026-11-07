package endpoints

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/dal-go/dalgo/recordops"
	"github.com/dal-go/dalgo/tugql"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/datarolesfile"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/reconcileapi"
	"github.com/datatug/datatug-cli/pkg/reconcileprofiles"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/dataroles"
	"github.com/datatug/datatug-core/pkg/datatug"
	core "github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/reconcile"
	"github.com/datatug/datatug-core/pkg/recordsetcompare"
)

// RowLimitPolicy names the limitation a truncated instance read carries: its
// rows are incomplete, so its absences are inconclusive, never "missing".
const RowLimitPolicy = "row-limit"

// reconcileExecuteInstanceHook executes one instance. It is the same
// policy-enforced live read as a compare side; tests replace it to simulate
// a refused or failing instance.
var reconcileExecuteInstanceHook = executeLiveInstance

func reconcileHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeContractResponse(w, r, newInvalidRequest("", "only POST is supported for reconcile"), nil)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCompareBodyBytes))
	if err != nil {
		writeContractResponse(w, r, newInvalidRequest("body", "request body is too large or unreadable"), nil)
		return
	}
	var req reconcileapi.Request
	if err := decodeContractBody(body, &req); err != nil {
		writeContractResponse(w, r, newInvalidRequest("", err.Error()), nil)
		return
	}
	response, err := computeReconcile(withAgentContext(r.Context(), isAgentRequest(r)), req)
	writeContractResponse(w, r, err, response)
}

// computeReconcile resolves the profile, the instances and the rules of req
// and runs the reconciliation. Every error is a contract error whose message
// names only project objects, never driver text.
func computeReconcile(ctx context.Context, req reconcileapi.Request) (reconcileapi.Response, error) {
	if err := req.Validate(); err != nil {
		return reconcileapi.Response{}, newInvalidRequest("", err.Error())
	}
	if !api.ValidateSecurityContext(req.SecurityContextID) {
		return reconcileapi.Response{}, newStaleContext("securityContextId does not match the agent's current session; call agent-info again")
	}
	projDir, ok := api.ProjectDir(req.Project)
	if !ok {
		return reconcileapi.Response{}, newNotFound(fmt.Sprintf("unknown project %q", dbcopy.SourceIDDisplay(req.Project)))
	}
	projStore, err := execRunProjectStoreFor(req.Project)
	if err != nil {
		return reconcileapi.Response{}, newInvalidRequest("project", "the project store cannot be opened")
	}
	queryID := req.QueryID
	var selector reconcile.InstanceSelector
	var base reconcile.Rules
	if req.ProfileID != "" {
		profile, err := reconcileprofiles.Store{Dir: projDir}.Load(req.ProfileID)
		if errors.Is(err, reconcileprofiles.ErrNotFound) {
			return reconcileapi.Response{}, newNotFound(fmt.Sprintf("reconcile profile %q not found", dbcopy.SourceIDDisplay(req.ProfileID)))
		}
		if err != nil {
			return reconcileapi.Response{}, newInvalidRequest("profileId", fmt.Sprintf("reconcile profile %q cannot be read", dbcopy.SourceIDDisplay(req.ProfileID)))
		}
		if queryID == "" {
			queryID = profile.QueryID
		}
		selector, base = profile.Instances, profile.Rules
	}
	selector = reconcileapi.OverlaySelector(selector, req.Instances)
	candidates, err := reconcileCandidates(ctx, projStore)
	if err != nil {
		return reconcileapi.Response{}, err
	}
	selected, err := selector.Select(candidates)
	if err != nil {
		return reconcileapi.Response{}, newInvalidRequest("instances", err.Error())
	}
	if len(base.Key) == 0 && (req.Rules == nil || len(req.Rules.Key) == 0) {
		classified, err := confirmedConsistency(ctx, req.Project, projDir, projStore, queryID, selected)
		if err != nil {
			return reconcileapi.Response{}, err
		}
		if classified != nil {
			base = *classified
		}
	}
	rules := reconcileapi.Overlay(base, req.Rules)
	if agentFromContext(ctx) {
		// An AI agent sees which instances agree, never the values.
		rules.Disclosure = reconcile.DiscloseGroupsOnly
	}
	if len(rules.Key) == 0 {
		return reconcileapi.Response{}, newInvalidRequest("rules.key", "no key: pass the semantic key columns, or confirm a data-role classification with a consistency rule for the query's collection (datatug db roles confirm)")
	}
	check := reconcile.Profile{ID: "request", QueryID: queryID, Instances: selector, Rules: rules}
	if err := check.Validate(); err != nil {
		return reconcileapi.Response{}, newInvalidRequest("rules", err.Error())
	}
	if rules.Baseline != "" && !hasEnvironment(selected, rules.Baseline) {
		return reconcileapi.Response{}, newInvalidRequest("rules.baseline", fmt.Sprintf("baseline %q is not among the selected instances", dbcopy.SourceIDDisplay(rules.Baseline)))
	}
	return runReconciliation(ctx, reconcileRun{
		securityContextID: req.SecurityContextID, storeID: req.StoreID, project: req.Project, queryID: queryID,
		selected: selected, rules: rules,
	})
}

func hasEnvironment(candidates []reconcile.Candidate, environment string) bool {
	for _, candidate := range candidates {
		if candidate.Environment == environment {
			return true
		}
	}
	return false
}

// reconcileCandidates lists every catalog of every active environment of the
// project as a candidate instance.
func reconcileCandidates(ctx context.Context, store datatug.ProjectStore) ([]reconcile.Candidate, error) {
	envs, err := store.LoadEnvironments(ctx, datatug.ActiveItemsOnly())
	if err != nil {
		return nil, newContractError(codeInternal, "the environments of the project cannot be read", "")
	}
	var candidates []reconcile.Candidate
	for _, env := range envs {
		catalogs, err := store.LoadEnvDbCatalogs(ctx, env.ID)
		if err != nil {
			return nil, newContractError(codeInternal, fmt.Sprintf("the catalogs of environment %q cannot be read", dbcopy.SourceIDDisplay(env.ID)), "")
		}
		for _, catalog := range catalogs {
			candidates = append(candidates, reconcile.Candidate{
				Environment: env.ID, Catalog: catalog.ID, Family: catalog.DbModel, Kind: env.Kind,
				Tenants: append([]string(nil), catalog.Tenants...),
			})
		}
	}
	reconcile.SortCandidates(candidates)
	return candidates, nil
}

// confirmedConsistency is the consistency rule of the confirmed data-role
// classification of the query's collection in the selected family, or nil.
// Suggested and dismissed classifications never take effect.
func confirmedConsistency(ctx context.Context, project, projDir string, store datatug.ProjectStore, queryID string, selected []reconcile.Candidate) (*reconcile.Rules, error) {
	family := selected[0].Family
	for _, candidate := range selected[1:] {
		if candidate.Family != family {
			return nil, nil
		}
	}
	collection := reconcileQueryCollection(ctx, project, projDir, store, queryID)
	if family == "" || collection == "" {
		return nil, nil
	}
	doc, err := datarolesfile.Store{Dir: projDir}.Load()
	if err != nil {
		return nil, newInvalidRequest("rules", fmt.Sprintf("%s cannot be read", dataroles.FileName))
	}
	// Precedence (REQ:rule-precedence): the caller applied the profile's own
	// rule already; Resolve takes the confirmed project classification, then a
	// Meaning Graph default. This agent binds no Meaning Graph defaults yet.
	classification, ok := dataroles.Resolve(doc, dataroles.Target{Family: family, Collection: collection}, nil)
	if !ok || classification.Consistency == nil || len(classification.Consistency.Key) == 0 {
		return nil, nil
	}
	rules := reconcileapi.Overlay(*classification.Consistency, nil)
	return &rules, nil
}

// reconcileQueryCollection is the base collection a saved TugQL query reads,
// or "" when it cannot be told without executing it.
func reconcileQueryCollection(ctx context.Context, project, projDir string, store datatug.ProjectStore, queryID string) string {
	canonical, err := api.ResolveQueryID(projDir, queryID)
	if err != nil {
		return ""
	}
	def, err := store.LoadQuery(ctx, canonical)
	if err != nil || def == nil || def.Type != datatug.QueryTypeTugQL {
		return ""
	}
	doc, err := executionQueryDocumentHook(project, canonical, def, "")
	if err != nil {
		return ""
	}
	query, err := tugql.Deserialize([]byte(doc))
	if err != nil {
		return ""
	}
	base, ok := baseCollectionOf(query)
	if !ok {
		return ""
	}
	return base.Name()
}

type reconcileRun struct {
	securityContextID string
	storeID           string
	project           string
	queryID           string
	selected          []reconcile.Candidate
	rules             reconcile.Rules
}

// runReconciliation reads every selected instance through its own
// policy-enforced execution and reconciles them in one pass. One instance
// that is refused or fails is reported, never fatal to the run.
func runReconciliation(ctx context.Context, run reconcileRun) (reconcileapi.Response, error) {
	sources := make([]reconcile.Source, 0, len(run.selected))
	receipts := make(map[string]apicontract.CompareSideReceipt, len(run.selected))
	for _, candidate := range run.selected {
		if ctx.Err() != nil {
			return reconcileapi.Response{}, newTimeout("the reconciliation was cancelled or ran out of time")
		}
		sources = append(sources, reconcileSource(ctx, run, candidate, receipts))
	}
	result, err := reconcile.Reconcile(sources, run.rules)
	if err != nil {
		if errors.Is(err, reconcile.ErrInvalidRules) {
			return reconcileapi.Response{}, newInvalidRequest("rules", err.Error())
		}
		if errors.Is(err, recordops.ErrDuplicateID) {
			// The engine's text can hold the duplicated key value: never echo it.
			return reconcileapi.Response{}, newInvalidRequest("rules.key", "the key is not unique in at least one instance: choose columns that identify one record")
		}
		log.Printf("reconcile: %v", dbcopy.RedactError(err))
		return reconcileapi.Response{}, newContractError(codeInternal, "the instances could not be reconciled", "")
	}
	return reconcileapi.Response{Result: result, Receipts: receipts}, nil
}

func reconcileSource(ctx context.Context, run reconcileRun, candidate reconcile.Candidate, receipts map[string]apicontract.CompareSideReceipt) reconcile.Source {
	source := reconcile.Source{Instance: candidate.Instance()}
	data, err := reconcileExecuteInstanceHook(ctx, liveInstanceRequest{
		SecurityContextID: run.securityContextID, QueryID: run.queryID,
		StoreID: run.storeID, Project: run.project, Environment: candidate.Environment, Source: candidate.Catalog,
	}, map[string]apicontract.TypedValueOrSet{}, []apicontract.BindingOriginEntry{})
	if err != nil {
		source.Unavailable = reconcileUnavailable(err)
		return source
	}
	receipts[candidate.Environment] = data.receipt
	source.ReadAt = data.receipt.ExecutedAt
	limitations := append([]apicontract.Limitation(nil), data.receipt.Limitations...)
	if data.truncated {
		limitations = append(limitations, apicontract.Limitation{Policy: RowLimitPolicy, RowsFiltered: true, HiddenColumns: []string{}})
	}
	rows, problem := orderReconcileRows(data.recordset, run.rules.Key)
	if problem != "" {
		source.Unavailable = &reconcile.Unavailable{Reason: reconcile.UnavailableNotExamined, Message: problem}
		return source
	}
	source.Columns = data.recordset.Columns
	source.Rows = rows
	source.Info = apicontract.CompareSideInfo{RowCount: len(data.recordset.Rows), Limitations: limitations}
	return source
}

// reconcileUnavailable classifies an instance's execution failure. A policy
// refusal is permission_denied; anything else is execution_failed. The
// message carries the contract code only, never the failure's own text.
func reconcileUnavailable(err error) *reconcile.Unavailable {
	code := string(codeInternal)
	var ce *contractError
	if errors.As(err, &ce) {
		code = string(ce.Code)
	} else if !errors.Is(err, secureread.ErrAccessDenied) {
		log.Printf("reconcile: instance execution failed: %v", dbcopy.RedactError(err))
	}
	switch {
	case errors.Is(err, secureread.ErrAccessDenied) || code == string(apicontract.ErrCodeAccessDenied) || code == string(apicontract.ErrCodeUnsupportedProtectedExecution):
		if code == string(codeInternal) {
			code = string(apicontract.ErrCodeAccessDenied)
		}
		return &reconcile.Unavailable{Reason: reconcile.UnavailablePermissionDenied, Message: "the access policy refused this read (" + code + ")"}
	default:
		return &reconcile.Unavailable{Reason: reconcile.UnavailableExecutionFailed, Message: "the read failed (" + code + ")"}
	}
}

// orderReconcileRows orders the recordset by key. A key column the instance
// does not return yields an empty stream, so the engine reports the instance
// not examined with its column drift; a null key is reported here. A
// duplicate key is left for the engine's merge to reject.
func orderReconcileRows(recordset apicontract.Recordset, key []string) (recordsetcompare.OrderedRows, string) {
	present := make(map[string]bool, len(recordset.Columns))
	for _, column := range recordset.Columns {
		present[column.Name] = true
	}
	for _, name := range key {
		if !present[name] {
			return func(func([]apicontract.TypedValue, error) bool) {}, ""
		}
	}
	ordered, err := recordsetcompare.OrderRecordset(recordset, key)
	if err != nil {
		return nil, "the rows cannot be aligned by the key: a key value is null or invalid"
	}
	return ordered, ""
}

// detectiveReconciler runs a Check's reconcile step through the same path as
// POST /datatug/reconcile, under the session that runs the Check.
//
// A Check run is persisted, shown to AI-dispatched runs, scheduled by Watch
// and returned to whoever triggered it, so its embedded result is always
// groups-only: it says which instances disagree, never the values or keys.
// People who need the values run `datatug reconcile --profile` directly.
func detectiveReconciler(project, securityContextID string) core.Reconciler {
	return func(ctx context.Context, profileID string) (reconcile.Result, error) {
		response, err := computeReconcile(ctx, reconcileapi.Request{SecurityContextID: securityContextID, Project: project, ProfileID: profileID,
			Rules: &reconcile.Rules{Disclosure: reconcile.DiscloseGroupsOnly}})
		if err != nil {
			code := string(codeInternal)
			var ce *contractError
			if errors.As(err, &ce) {
				code = string(ce.Code)
			}
			return reconcile.Result{}, &detective.QueryFailure{Code: code}
		}
		return response.Result, nil
	}
}

// withDetectiveReconciler lets the Checks evaluated under ctx run reconcile
// steps for project.
func withDetectiveReconciler(ctx context.Context, project, securityContextID string) context.Context {
	return detective.ContextWithReconciler(ctx, detectiveReconciler(project, securityContextID))
}
