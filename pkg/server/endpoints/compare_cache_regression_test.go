package endpoints

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/comparecache"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/recordsetcompare"
	"github.com/stretchr/testify/require"
)

func TestComputeCompareOptionalCacheFailureDoesNotChangeResult(t *testing.T) {
	for _, failure := range []string{"insert", "byte cap", "begin"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			options := comparecache.Options{PrivateDir: dir}
			if failure == "byte cap" {
				options.ByteCap = 1
			}
			require.NoError(t, api.ConfigureCompareCache(options))
			t.Cleanup(func() { require.NoError(t, api.CloseCompareCache()) })
			if failure == "insert" {
				db, err := sql.Open("sqlite", filepath.Join(dir, "comparisons.sqlite"))
				require.NoError(t, err)
				_, err = db.Exec(`CREATE TRIGGER fail_cache_insert BEFORE INSERT ON comparison_rows BEGIN SELECT RAISE(FAIL, 'injected cache insert failure'); END`)
				require.NoError(t, err)
				require.NoError(t, db.Close())
			}
			set := apicontract.Recordset{Columns: []apicontract.Column{{Name: "id", Type: "string"}}, Rows: [][]apicontract.TypedValue{{apicontract.NewStringValue("1")}, {apicontract.NewStringValue("2")}}}
			now := time.Now().UTC().Format(time.RFC3339Nano)
			req := apicontract.CompareRequest{SecurityContextID: "ctx", QueryID: "orders", Key: []string{"id"}, Left: apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, StoreID: "ops", Project: "project", Environment: "before"}, Right: apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, StoreID: "ops", Project: "project", Environment: "after"}}
			deps := compareDependencies{executeSide: func(_ context.Context, _ apicontract.CompareRequest, side apicontract.CompareSideSpec) (compareSideData, error) {
				receipt := apicontract.CompareSideReceipt{Execution: apicontract.ExecutionRef{StoreID: "ops", ProjectID: "project", ExecutionID: side.Environment}, ExecutedAt: now, RowCount: 2}
				return compareSideData{recordset: set, receipt: receipt}, nil
			}}
			ctx := context.Background()
			if failure == "begin" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			result, err := computeCompareWith(ctx, req, deps)
			require.NoError(t, err)
			require.Equal(t, 2, result.Summary.Unchanged)
			require.NoError(t, result.Validate())
			_, err = api.LookupCompareCache(context.Background(), api.ComparisonCacheID(result.Left.Execution, result.Right.Execution))
			require.ErrorIs(t, err, comparecache.ErrNotFound)
		})
	}
}

func TestCompareRowsRejectsOldCacheAfterPolicyNarrowsProjection(t *testing.T) {
	ctx := context.Background()
	const project = "cache-policy"
	dirs := map[string]string{project: t.TempDir()}
	unrestricted, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	api.ConfigureSecureSession(unrestricted, dirs, api.Capabilities{})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	require.NoError(t, api.ConfigureExecutionEvidence(dirs, nil, executionstore.Options{PrivateDir: t.TempDir()}))
	t.Cleanup(func() { require.NoError(t, api.CloseExecutionEvidence()) })
	require.NoError(t, api.ConfigureCompareCache(comparecache.Options{PrivateDir: t.TempDir()}))
	t.Cleanup(func() { require.NoError(t, api.CloseCompareCache()) })
	store, err := api.ExecutionEvidenceStoreByID(project, project)
	require.NoError(t, err)
	now := time.Now().UTC()
	set := apicontract.Recordset{Columns: []apicontract.Column{{Name: "id", Type: "string"}, {Name: "secret", Type: "string"}}, Rows: [][]apicontract.TypedValue{{apicontract.NewStringValue("one"), apicontract.NewStringValue("private-value")}}}
	fingerprint, err := apicontract.FingerprintRecordset(set)
	require.NoError(t, err)
	refs := make([]apicontract.ExecutionRef, 2)
	for i, id := range []string{"left", "right"} {
		ref := apicontract.ExecutionRef{StoreID: project, ProjectID: project, ExecutionID: id}
		refs[i] = ref
		snapshot, stored, err := store.PutSnapshot(ctx, ref, set, now)
		require.NoError(t, err)
		require.True(t, stored)
		require.NoError(t, store.PutExecution(ctx, apicontract.ExecutionRecord{
			Ref: ref, Scope: apicontract.ExecutionRecordScope{StoreID: project, Project: project, Environment: "prod"}, QueryID: "orders",
			Parameters: map[string]apicontract.TypedValueOrSet{}, BindingsApplied: []apicontract.Binding{}, Principal: apicontract.ExecutionPrincipal{ID: "alice", Roles: []string{}, Groups: []string{}}, PolicyFingerprint: api.SecurePolicyFingerprint(), ExecutedAt: now.Format(time.RFC3339Nano),
			Limitations: []apicontract.Limitation{}, Provenance: apicontract.Provenance{Source: "sales", Collection: "orders", QueryID: "orders", Mode: apicontract.ProvenanceModeLive, ObservedAt: now.Format(time.RFC3339Nano), ExecutionProfile: apicontract.ExecutionProfileProtected},
			AuthorizedFields: []apicontract.FieldAccessRef{{StoreID: project, Project: project, Environment: "prod", Source: "sales", Collection: "orders", Column: "id"}, {StoreID: project, Project: project, Environment: "prod", Source: "sales", Collection: "orders", Column: "secret"}}, RowCount: 1, ResultFingerprint: fingerprint, ResultComplete: boolPointer(true), SnapshotRef: snapshot, Measurements: []apicontract.ScalarMeasurement{},
		}))
	}
	writer, err := api.BeginCompareCache(ctx, comparecache.BeginRequest{QueryID: "orders", Left: refs[0], Right: refs[1]})
	require.NoError(t, err)
	defer writer.Abort()
	receipt := func(ref apicontract.ExecutionRef) apicontract.CompareSideReceipt {
		return apicontract.CompareSideReceipt{Execution: ref, ExecutedAt: now.Format(time.RFC3339Nano), RowCount: 1}
	}
	result, err := recordsetcompare.Compare(set, set, receipt(refs[0]), receipt(refs[1]), recordsetcompare.Options{Key: []string{"id"}, Observer: writer.Observer()})
	require.NoError(t, err)
	require.NoError(t, writer.Commit(result))
	id := api.ComparisonCacheID(refs[0], refs[1])
	oldMeta, err := api.LookupCompareCache(ctx, id)
	require.NoError(t, err)
	policyDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(policyDir, "restricted.yaml"), []byte(`apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: restricted
default: deny
scopes:
  - path: /orders
    rules:
      - id: visible-id
        effect: allow
        operations: [query]
        fields: [id]
`), 0600))
	restricted, err := secureread.NewSession(secureread.SessionOptions{As: "alice", PoliciesDir: policyDir})
	require.NoError(t, err)
	api.ConfigureSecureSession(restricted, dirs, api.Capabilities{})
	// Current snapshot reads succeed and remove the secret column. Such a
	// successful authorization alone must not release the older cached payload.
	replayed, err := loadCompareRecordSide(ctx, apicontract.CompareRequest{QueryID: "orders"}, apicontract.CompareSideSpec{Kind: apicontract.CompareSideRecord, Execution: &refs[0]})
	require.NoError(t, err)
	require.Len(t, replayed.recordset.Columns, 1)
	require.Equal(t, "id", replayed.recordset.Columns[0].Name)
	require.Error(t, authorizeCachedComparison(ctx, oldMeta))
	query := url.Values{"comparisonId": {id}, "state": {"matched"}, "securityContextId": {api.SecurityContextID()}}
	res := httptest.NewRecorder()
	compareRowsHandler(res, httptest.NewRequest(http.MethodGet, "/datatug/compare/rows?"+query.Encode(), nil))
	require.Equal(t, http.StatusServiceUnavailable, res.Code)
	require.NotContains(t, res.Body.String(), "private-value")
	// Recheck at Page, even when a caller has metadata obtained before a
	// policy change or a concurrent replacement of this comparison.
	_, err = api.PageCompareCache(ctx, comparecache.PageRequest{ComparisonID: id, State: recordsetcompare.RecordMatched})
	require.ErrorIs(t, err, comparecache.ErrNotFound)
}
