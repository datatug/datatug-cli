package endpoints

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/datarolesfile"
	"github.com/datatug/datatug-cli/pkg/demofixtures/factories"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/reconcileapi"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/dataroles"
	"github.com/datatug/datatug-core/pkg/datatug"
	core "github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/reconcile"
	"github.com/stretchr/testify/require"
)

func reconcileCode(t *testing.T, err error) apicontract.ErrorCode {
	t.Helper()
	var ce *contractError
	require.True(t, errors.As(err, &ce), "want a contract error, got %v", err)
	return ce.Code
}

func TestReconcileHandlerHTTP(t *testing.T) {
	projectID, _ := serveFactoriesProject(t)
	post := func(method, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		reconcileHandler(w, httptest.NewRequest(method, "/datatug/reconcile", strings.NewReader(body)))
		return w
	}
	require.Equal(t, http.StatusBadRequest, post(http.MethodGet, "").Code)
	require.Equal(t, http.StatusBadRequest, post(http.MethodPost, `{"unknown":1}`).Code)
	require.Equal(t, http.StatusBadRequest, post(http.MethodPost, strings.Repeat(" ", maxCompareBodyBytes+1)).Code)

	body, _ := json.Marshal(reconcileapi.Request{SecurityContextID: api.SecurityContextID(), Project: projectID, ProfileID: factories.ProfileCountries})
	w := post(http.MethodPost, string(body))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var response reconcileapi.Response
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, reconcile.StatusDiscrepancies, response.Result.Status)
	require.Len(t, response.Receipts, 5)
	for env, receipt := range response.Receipts {
		require.Equal(t, projectID, receipt.Execution.ProjectID, env)
	}
}

func TestComputeReconcileRefusals(t *testing.T) {
	projectID, dir := serveFactoriesProject(t)
	ctx := context.Background()
	sc := api.SecurityContextID()
	production := &reconcile.InstanceSelector{Family: factories.Family, Kind: "production"}
	cases := []struct {
		name string
		req  reconcileapi.Request
		code apicontract.ErrorCode
		text string
	}{
		{"no security context", reconcileapi.Request{Project: projectID, ProfileID: "x"}, apicontract.ErrCodeInvalidRequest, "securityContextId"},
		{"no project", reconcileapi.Request{SecurityContextID: sc, ProfileID: "x"}, apicontract.ErrCodeInvalidRequest, "project"},
		{"no query", reconcileapi.Request{SecurityContextID: sc, Project: projectID}, apicontract.ErrCodeInvalidRequest, "queryId"},
		{"no instances", reconcileapi.Request{SecurityContextID: sc, Project: projectID, QueryID: "countries"}, apicontract.ErrCodeInvalidRequest, "instances"},
		{"stale", reconcileapi.Request{SecurityContextID: "other", Project: projectID, ProfileID: "x"}, apicontract.ErrCodeStaleContext, ""},
		{"unknown project", reconcileapi.Request{SecurityContextID: sc, Project: "nope", ProfileID: "x"}, apicontract.ErrCodeNotFound, "nope"},
		{"unknown profile", reconcileapi.Request{SecurityContextID: sc, Project: projectID, ProfileID: "nope"}, apicontract.ErrCodeNotFound, "nope"},
		{"no match", reconcileapi.Request{SecurityContextID: sc, Project: projectID, QueryID: "countries", Instances: &reconcile.InstanceSelector{Family: "other"}}, apicontract.ErrCodeInvalidRequest, "at least two"},
		{"no key for an unknown query", reconcileapi.Request{SecurityContextID: sc, Project: projectID, QueryID: "nope", Instances: production}, apicontract.ErrCodeInvalidRequest, "no key"},
		{"no key", reconcileapi.Request{SecurityContextID: sc, Project: projectID, QueryID: "countries", Instances: production}, apicontract.ErrCodeInvalidRequest, "no key"},
		{"bad rules", reconcileapi.Request{SecurityContextID: sc, Project: projectID, QueryID: "countries", Instances: production, Rules: &reconcile.Rules{Key: []string{"code"}, Presence: "subset"}}, apicontract.ErrCodeInvalidRequest, "baseline mode"},
		{"baseline elsewhere", reconcileapi.Request{SecurityContextID: sc, Project: projectID, QueryID: "countries", Instances: production, Rules: &reconcile.Rules{Key: []string{"code"}, Baseline: "factory-a-test"}}, apicontract.ErrCodeInvalidRequest, "not among"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := computeReconcile(ctx, c.req)
			require.Equal(t, c.code, reconcileCode(t, err))
			require.Contains(t, err.Error(), c.text)
		})
	}

	t.Run("unreadable profile", func(t *testing.T) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, reconcile.ProfilesFolder, "broken.json"), []byte("{"), 0o644))
		_, err := computeReconcile(ctx, reconcileapi.Request{SecurityContextID: sc, Project: projectID, ProfileID: "broken"})
		require.Equal(t, apicontract.ErrCodeInvalidRequest, reconcileCode(t, err))
		require.Contains(t, err.Error(), `reconcile profile "broken" cannot be read`)
		_, err = computeReconcile(ctx, reconcileapi.Request{SecurityContextID: sc, Project: projectID, ProfileID: "evil\x1b[31m\nid"})
		require.Equal(t, apicontract.ErrCodeNotFound, reconcileCode(t, err))
		require.NotContains(t, err.Error(), "\x1b", "a raw profile id is never echoed")
	})
	t.Run("store unavailable", func(t *testing.T) {
		previous := execRunProjectStoreFor
		t.Cleanup(func() { execRunProjectStoreFor = previous })
		execRunProjectStoreFor = func(string) (datatug.ProjectStore, error) { return nil, errors.New("driver: secret") }
		_, err := computeReconcile(ctx, reconcileapi.Request{SecurityContextID: sc, Project: projectID, ProfileID: factories.ProfileCountries})
		require.Equal(t, apicontract.ErrCodeInvalidRequest, reconcileCode(t, err))
		require.NotContains(t, err.Error(), "secret")
	})
	t.Run("environments unreadable", func(t *testing.T) {
		previous := execRunProjectStoreFor
		t.Cleanup(func() { execRunProjectStoreFor = previous })
		real, err := previous(projectID)
		require.NoError(t, err)
		execRunProjectStoreFor = func(string) (datatug.ProjectStore, error) { return failingEnvStore{ProjectStore: real}, nil }
		_, err = computeReconcile(ctx, reconcileapi.Request{SecurityContextID: sc, Project: projectID, ProfileID: factories.ProfileCountries})
		require.Equal(t, codeInternal, reconcileCode(t, err))
		execRunProjectStoreFor = func(string) (datatug.ProjectStore, error) {
			return failingEnvStore{ProjectStore: real, catalogs: true}, nil
		}
		_, err = computeReconcile(ctx, reconcileapi.Request{SecurityContextID: sc, Project: projectID, ProfileID: factories.ProfileCountries})
		require.Equal(t, codeInternal, reconcileCode(t, err))
	})
	t.Run("cancelled", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		_, err := runReconciliation(cancelled, reconcileRun{selected: []reconcile.Candidate{{Environment: "a"}, {Environment: "b"}}, rules: reconcile.Rules{Key: []string{"code"}}})
		require.Equal(t, apicontract.ErrCodeTimeout, reconcileCode(t, err))
	})
}

type failingEnvStore struct {
	datatug.ProjectStore
	catalogs bool
}

func (s failingEnvStore) LoadEnvironments(ctx context.Context, o ...datatug.StoreOption) (datatug.Environments, error) {
	if s.catalogs {
		return s.ProjectStore.LoadEnvironments(ctx, o...)
	}
	return nil, errors.New("disk")
}

func (s failingEnvStore) LoadEnvDbCatalogs(context.Context, string, ...datatug.StoreOption) (datatug.DbCatalogs, error) {
	return nil, errors.New("disk")
}

func TestReconcileConfirmedClassificationSuppliesTheRule(t *testing.T) {
	projectID, dir := serveFactoriesProject(t)
	ctx := context.Background()
	req := reconcileapi.Request{SecurityContextID: api.SecurityContextID(), Project: projectID, QueryID: "currencies",
		Instances: &reconcile.InstanceSelector{Family: factories.Family, Kind: "production"}}
	store := datarolesfile.Store{Dir: dir}
	target := dataroles.Target{Family: factories.Family, Collection: "currencies"}
	at := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	_, err := store.Update(ctx, func(doc *dataroles.Document) error {
		doc.Merge([]dataroles.Classification{{Target: target, Role: dataroles.RoleReference, Origin: dataroles.OriginHeuristic, State: dataroles.StateSuggested, At: at,
			Consistency: &reconcile.Rules{Key: []string{"code"}, Mode: reconcile.ModeConsensus, Presence: reconcile.PresenceRequired}}})
		return nil
	})
	require.NoError(t, err)
	_, err = computeReconcile(ctx, req)
	require.ErrorContains(t, err, "no key", "a suggestion never takes effect")

	_, err = store.Update(ctx, func(doc *dataroles.Document) error {
		return doc.Decide(target, dataroles.StateConfirmed, "alice", at, "", nil, "")
	})
	require.NoError(t, err)
	response, err := computeReconcile(ctx, req)
	require.NoError(t, err)
	require.Equal(t, []string{"code"}, response.Result.Key)
	require.Equal(t, reconcile.StatusDiscrepancies, response.Result.Status)

	// Flags override the classification field by field.
	req.Rules = &reconcile.Rules{Presence: reconcile.PresenceAny}
	response, err = computeReconcile(ctx, req)
	require.NoError(t, err)
	require.Equal(t, reconcile.StatusConsistent, response.Result.Status)

	// A selection spanning families has no single classification.
	mixed := reconcileapi.Request{SecurityContextID: api.SecurityContextID(), Project: projectID, QueryID: "currencies",
		Instances: &reconcile.InstanceSelector{Environments: []string{"factory-a-prod", "factory-b-prod"}}}
	previous := execRunProjectStoreFor
	t.Cleanup(func() { execRunProjectStoreFor = previous })
	real, err := previous(projectID)
	require.NoError(t, err)
	execRunProjectStoreFor = func(string) (datatug.ProjectStore, error) { return mixedFamilyStore{ProjectStore: real}, nil }
	_, err = computeReconcile(ctx, mixed)
	require.ErrorContains(t, err, "no key")
	execRunProjectStoreFor = previous

	require.NoError(t, os.WriteFile(filepath.Join(dir, dataroles.FileName), []byte("{"), 0o644))
	_, err = computeReconcile(ctx, mixed)
	require.ErrorContains(t, err, dataroles.FileName)
}

type mixedFamilyStore struct{ datatug.ProjectStore }

func (s mixedFamilyStore) LoadEnvDbCatalogs(ctx context.Context, env string, o ...datatug.StoreOption) (datatug.DbCatalogs, error) {
	catalogs, err := s.ProjectStore.LoadEnvDbCatalogs(ctx, env, o...)
	if env == "factory-b-prod" {
		for _, c := range catalogs {
			c.DbModel = "other"
		}
	}
	return catalogs, err
}

func TestReconcileQueryCollection(t *testing.T) {
	projectID, dir := serveFactoriesProject(t)
	ctx := context.Background()
	store, err := execRunProjectStoreFor(projectID)
	require.NoError(t, err)
	require.Equal(t, "countries", reconcileQueryCollection(ctx, projectID, dir, store, "countries"))
	require.Equal(t, "", reconcileQueryCollection(ctx, projectID, dir, store, "missing"))

	require.NoError(t, os.WriteFile(filepath.Join(dir, "queries", "raw.query.json"), []byte(`{"createdAt":"2026-11-07T00:00:00Z","id":"raw","title":"Raw","type":"SQL"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "queries", "raw.query.sql"), []byte("select 1"), 0o644))
	require.Equal(t, "", reconcileQueryCollection(ctx, projectID, dir, store, "raw"))

	previous := executionQueryDocumentHook
	t.Cleanup(func() { executionQueryDocumentHook = previous })
	for _, doc := range []string{"", "not: [tugql", "from:\n  query:\n    as: c\n    from: {name: countries, alias: x}\n    columns:\n      - {field: code, source: x}\n"} {
		executionQueryDocumentHook = func(string, string, *datatug.QueryDef, string) (string, error) {
			if doc == "" {
				return "", errors.New("unreadable")
			}
			return doc, nil
		}
		require.Equal(t, "", reconcileQueryCollection(ctx, projectID, dir, store, "countries"), doc)
	}
}

func TestReconcileInstanceFailuresNeverAbortTheRun(t *testing.T) {
	projectID, _ := serveFactoriesProject(t)
	previous := reconcileExecuteInstanceHook
	t.Cleanup(func() { reconcileExecuteInstanceHook = previous })
	failures := map[string]error{
		"factory-b-prod": errors.New("dial tcp: password=hunter2 refused"),
		"factory-c-prod": newContractError(apicontract.ErrCodeUnsupportedProtectedExecution, "x", ""),
		"factory-d-prod": secureread.ErrAccessDenied,
	}
	reconcileExecuteInstanceHook = func(ctx context.Context, request liveInstanceRequest, parameters map[string]apicontract.TypedValueOrSet, origins []apicontract.BindingOriginEntry) (compareSideData, error) {
		if err := failures[request.Environment]; err != nil {
			return compareSideData{}, err
		}
		data, err := previous(ctx, request, parameters, origins)
		if request.Environment == "factory-e-prod" {
			data.truncated = true
		}
		return data, err
	}
	response := profileRun(t, projectID, factories.ProfileCurrencies)
	result := response.Result
	b := instanceResult(t, result, "factory-b-prod")
	require.Equal(t, reconcile.UnavailableExecutionFailed, b.Unavailable.Reason)
	require.Equal(t, "the read failed (INTERNAL)", b.Unavailable.Message)
	require.Equal(t, reconcile.UnavailablePermissionDenied, instanceResult(t, result, "factory-c-prod").Unavailable.Reason)
	d := instanceResult(t, result, "factory-d-prod")
	require.Equal(t, reconcile.UnavailablePermissionDenied, d.Unavailable.Reason)
	require.Contains(t, d.Unavailable.Message, "ACCESS_DENIED")
	e := instanceResult(t, result, "factory-e-prod")
	require.True(t, e.RowsFiltered, "a truncated read makes absences inconclusive")
	require.Equal(t, reconcile.StatusInconclusive, result.Status)

	body, _ := json.Marshal(response)
	require.NotContains(t, string(body), "hunter2")
}

func TestReconcileUnavailableMapsContractCodes(t *testing.T) {
	require.Equal(t, reconcile.UnavailableExecutionFailed, reconcileUnavailable(newSourceUnavailable("gone")).Reason)
	require.Equal(t, "the read failed (SOURCE_UNAVAILABLE)", reconcileUnavailable(newSourceUnavailable("gone")).Message)
	require.Equal(t, reconcile.UnavailablePermissionDenied, reconcileUnavailable(newAccessDenied("no")).Reason)
}

func TestOrderReconcileRows(t *testing.T) {
	columns := []apicontract.Column{{Name: "code", Type: "string"}, {Name: "name", Type: "string"}}
	row := func(code apicontract.TypedValue) []apicontract.TypedValue {
		return []apicontract.TypedValue{code, apicontract.NewStringValue("n")}
	}
	rows, problem := orderReconcileRows(apicontract.Recordset{Columns: columns}, []string{"missing"})
	require.Empty(t, problem)
	for range rows {
		t.Fatal("a missing key column yields no rows")
	}
	_, problem = orderReconcileRows(apicontract.Recordset{Columns: columns, Rows: [][]apicontract.TypedValue{row(apicontract.NewNullValue())}}, []string{"code"})
	require.Contains(t, problem, "null")
	_, problem = orderReconcileRows(apicontract.Recordset{Columns: columns, Rows: [][]apicontract.TypedValue{row(apicontract.NewStringValue("A")), row(apicontract.NewStringValue("A"))}}, []string{"code"})
	require.Empty(t, problem, "a duplicate key is left for the engine's merge to reject")
	rows, problem = orderReconcileRows(apicontract.Recordset{Columns: columns, Rows: [][]apicontract.TypedValue{row(apicontract.NewStringValue("B")), row(apicontract.NewStringValue("A"))}}, []string{"code"})
	require.Empty(t, problem)
	var keys []string
	for r := range rows {
		keys = append(keys, r[0].Str)
		break
	}
	require.Equal(t, []string{"A"}, keys)
}

func TestReconcileSourceReportsUnalignableInstance(t *testing.T) {
	previous := reconcileExecuteInstanceHook
	t.Cleanup(func() { reconcileExecuteInstanceHook = previous })
	reconcileExecuteInstanceHook = func(context.Context, liveInstanceRequest, map[string]apicontract.TypedValueOrSet, []apicontract.BindingOriginEntry) (compareSideData, error) {
		return compareSideData{recordset: apicontract.Recordset{
			Columns: []apicontract.Column{{Name: "code", Type: "string"}},
			Rows:    [][]apicontract.TypedValue{{apicontract.NewNullValue()}},
		}}, nil
	}
	source := reconcileSource(context.Background(), reconcileRun{rules: reconcile.Rules{Key: []string{"code"}}}, reconcile.Candidate{Environment: "e"}, map[string]apicontract.CompareSideReceipt{})
	require.Equal(t, reconcile.UnavailableNotExamined, source.Unavailable.Reason)
}

func TestRunReconciliationHidesEngineFailures(t *testing.T) {
	previous := reconcileExecuteInstanceHook
	t.Cleanup(func() { reconcileExecuteInstanceHook = previous })
	reconcileExecuteInstanceHook = func(context.Context, liveInstanceRequest, map[string]apicontract.TypedValueOrSet, []apicontract.BindingOriginEntry) (compareSideData, error) {
		return compareSideData{recordset: apicontract.Recordset{Columns: []apicontract.Column{{Name: "code"}}}}, nil
	}
	_, err := runReconciliation(context.Background(), reconcileRun{selected: []reconcile.Candidate{{Environment: "a"}, {Environment: "b"}}, rules: reconcile.Rules{Key: []string{"code"}}})
	require.Equal(t, codeInternal, reconcileCode(t, err))
	require.Equal(t, "the instances could not be reconciled", err.(*contractError).Message)
}

func TestRunReconciliationRefusesADuplicateKeyWithoutEchoingIt(t *testing.T) {
	previous := reconcileExecuteInstanceHook
	t.Cleanup(func() { reconcileExecuteInstanceHook = previous })
	reconcileExecuteInstanceHook = func(context.Context, liveInstanceRequest, map[string]apicontract.TypedValueOrSet, []apicontract.BindingOriginEntry) (compareSideData, error) {
		secret := apicontract.NewStringValue("secret-key-value")
		return compareSideData{recordset: apicontract.Recordset{Columns: []apicontract.Column{{Name: "code", Type: "string"}}, Rows: [][]apicontract.TypedValue{{secret}, {secret}}}}, nil
	}
	_, err := runReconciliation(context.Background(), reconcileRun{selected: []reconcile.Candidate{{Environment: "a"}, {Environment: "b"}}, rules: reconcile.Rules{Key: []string{"code"}}})
	require.Equal(t, apicontract.ErrCodeInvalidRequest, reconcileCode(t, err))
	require.Contains(t, err.Error(), "not unique")
	require.NotContains(t, err.Error(), "secret-key-value")
}

// An agent request (X-DataTug-Agent) runs under the agent context, so the
// agent guards apply, and is always answered groups-only.
func TestReconcileHandlerAgentRequestsAreGroupsOnly(t *testing.T) {
	projectID, _ := serveFactoriesProject(t)
	var agent bool
	previous := reconcileExecuteInstanceHook
	t.Cleanup(func() { reconcileExecuteInstanceHook = previous })
	reconcileExecuteInstanceHook = func(ctx context.Context, request liveInstanceRequest, parameters map[string]apicontract.TypedValueOrSet, origins []apicontract.BindingOriginEntry) (compareSideData, error) {
		agent = agentFromContext(ctx)
		return previous(ctx, request, parameters, origins)
	}
	body, _ := json.Marshal(reconcileapi.Request{SecurityContextID: api.SecurityContextID(), Project: projectID, ProfileID: factories.ProfileCountries,
		Rules: &reconcile.Rules{Disclosure: reconcile.DiscloseValues}})
	request := httptest.NewRequest(http.MethodPost, "/datatug/reconcile", strings.NewReader(string(body)))
	request.Header.Set(agentHeader, "claude")
	w := httptest.NewRecorder()
	reconcileHandler(w, request)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.True(t, agent, "instances are read under the agent context")
	var response reconcileapi.Response
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, reconcile.DiscloseGroupsOnly, response.Result.Disclosure)
	require.NotContains(t, w.Body.String(), "Eire")
	require.NotContains(t, w.Body.String(), `"IE"`)
	require.Nil(t, response.Result.Unexpected[0].Key)
	require.Positive(t, response.Result.Unexpected[0].Ordinal)
}

func TestRunReconciliationRejectsInvalidRules(t *testing.T) {
	previous := reconcileExecuteInstanceHook
	t.Cleanup(func() { reconcileExecuteInstanceHook = previous })
	reconcileExecuteInstanceHook = func(context.Context, liveInstanceRequest, map[string]apicontract.TypedValueOrSet, []apicontract.BindingOriginEntry) (compareSideData, error) {
		return compareSideData{}, errors.New("down")
	}
	_, err := runReconciliation(context.Background(), reconcileRun{selected: []reconcile.Candidate{{Environment: "a"}, {Environment: "b"}}, rules: reconcile.Rules{}})
	require.Equal(t, apicontract.ErrCodeInvalidRequest, reconcileCode(t, err))
	_, err = runReconciliation(context.Background(), reconcileRun{selected: []reconcile.Candidate{{Environment: "a"}, {Environment: "a"}}, rules: reconcile.Rules{Key: []string{"k"}}})
	require.Equal(t, apicontract.ErrCodeInvalidRequest, reconcileCode(t, err))
}

func TestDetectiveReconcilerMapsErrors(t *testing.T) {
	projectID, _ := serveFactoriesProject(t)
	reconciler := detectiveReconciler(projectID, api.SecurityContextID())
	result, err := reconciler(context.Background(), factories.ProfileSettings)
	require.NoError(t, err)
	require.Equal(t, reconcile.StatusConsistent, result.Status)
	require.Equal(t, reconcile.DiscloseGroupsOnly, result.Disclosure, "Check runs never embed values")

	// A profile that discloses values is still reported groups-only in a Check.
	result, err = reconciler(context.Background(), factories.ProfileCountries)
	require.NoError(t, err)
	require.Equal(t, reconcile.StatusDiscrepancies, result.Status)
	require.Equal(t, reconcile.DiscloseGroupsOnly, result.Disclosure)
	require.Nil(t, result.Unexpected[0].Key)
	for _, group := range result.Unexpected[0].Fields[0].Groups {
		require.Nil(t, group.Value)
	}
	_, err = reconciler(context.Background(), "nope")
	var failure *core.QueryFailure
	require.ErrorAs(t, err, &failure)
	require.Equal(t, "NOT_FOUND", failure.Code)
}

// TestCheckReconcileStepIsInconclusiveWhenAFactoryIsUnreadable runs a Check
// whose only step reconciles a profile, through the agent's Detective action
// endpoint, while Perimeter refuses one factory (AC check-reconcile-step).
func TestCheckReconcileStepIsInconclusiveWhenAFactoryIsUnreadable(t *testing.T) {
	projectID, dir := serveFactoriesProject(t)
	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	api.ConfigureSecureSession(session, map[string]string{projectID: dir}, api.Capabilities{AllowWrites: true, EvidencePrivateDir: t.TempDir()})
	check := core.Check{ID: "currencies-agree", Title: "Currencies agree", Inputs: []core.Input{},
		Steps: []core.Step{{ID: "currencies", Kind: "reconcile", Profile: factories.ProfileCurrencies}}}

	previous := reconcileExecuteInstanceHook
	t.Cleanup(func() { reconcileExecuteInstanceHook = previous })
	reconcileExecuteInstanceHook = func(ctx context.Context, request liveInstanceRequest, parameters map[string]apicontract.TypedValueOrSet, origins []apicontract.BindingOriginEntry) (compareSideData, error) {
		if request.Environment == "factory-d-prod" {
			return compareSideData{}, newAccessDenied("access denied by policy")
		}
		return previous(ctx, request, parameters, origins)
	}
	action := func(fields map[string]any) detective.ActionResponse {
		t.Helper()
		fields["project"], fields["securityContextId"] = projectID, api.SecurityContextID()
		request, _ := json.Marshal(fields)
		w := httptest.NewRecorder()
		detectiveActionHandler(w, httptest.NewRequest(http.MethodPost, "/datatug/detective/action?project="+projectID, strings.NewReader(string(request))))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var response detective.ActionResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
		return response
	}
	// The project file store accepts a Check of reconcile steps and keeps the profile.
	saved := action(map[string]any{"operationId": "operation-save", "type": "save-check", "check": check})
	require.NotNil(t, saved.Check)
	require.Equal(t, factories.ProfileCurrencies, saved.Check.Steps[0].Profile)
	run := func(checkID string) core.CheckRun {
		t.Helper()
		response := action(map[string]any{"operationId": "operation-" + checkID, "type": "run-check", "checkId": checkID, "environment": "factory-a-prod"})
		require.NotNil(t, response.Run)
		return *response.Run
	}
	result := run(check.ID)
	require.Equal(t, "unknown", result.Outcome, "an unreadable factory never lets a reconcile step pass")
	step := result.Steps[0]
	require.Equal(t, "reconcile", step.Kind)
	require.NotNil(t, step.Reconciliation)
	require.Equal(t, reconcile.StatusInconclusive, step.Reconciliation.Status)
	require.Equal(t, reconcile.UnavailablePermissionDenied, instanceResult(t, *step.Reconciliation, "factory-d-prod").Unavailable.Reason)

	stored, err := os.ReadFile(filepath.Join(dir, "checks", check.ID+".json"))
	require.NoError(t, err)
	require.Contains(t, string(stored), `"profile": "`+factories.ProfileCurrencies+`"`)

	demo := run(factories.CheckID)
	require.Equal(t, "fail", demo.Outcome, "factory-e-prod's Eire is an unexpected difference")
}
