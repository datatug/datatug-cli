package endpoints

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/stretchr/testify/require"
)

func TestLoadCompareRecordSideCompletenessAndInputBound(t *testing.T) {
	const projectID = "record-replay"
	projectDir := t.TempDir()
	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	api.ConfigureSecureSession(session, map[string]string{projectID: projectDir}, api.Capabilities{})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	require.NoError(t, api.ConfigureExecutionEvidence(map[string]string{projectID: projectDir}, nil, executionstore.Options{
		PrivateDir: t.TempDir(),
		ByteCap:    8 << 20,
	}))
	t.Cleanup(func() { require.NoError(t, api.CloseExecutionEvidence()) })
	store, err := api.ExecutionEvidenceStoreByID(projectID, projectID)
	require.NoError(t, err)
	executedAt := time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)
	rows := func(count int) apicontract.Recordset {
		recordset := apicontract.Recordset{
			Columns: []apicontract.Column{{Name: "id", Type: string(apicontract.ValueTypeInteger)}},
			Rows:    make([][]apicontract.TypedValue, count),
		}
		for index := range recordset.Rows {
			recordset.Rows[index] = []apicontract.TypedValue{apicontract.NewIntegerValue(fmt.Sprint(index + 1))}
		}
		return recordset
	}
	put := func(executionID string, recordset apicontract.Recordset, complete *bool, retain bool) apicontract.ExecutionRef {
		t.Helper()
		ref := apicontract.ExecutionRef{StoreID: projectID, ProjectID: projectID, ExecutionID: executionID}
		snapshotRef := ""
		if retain {
			var stored bool
			var err error
			snapshotRef, stored, err = store.PutSnapshot(context.Background(), ref, recordset, executedAt)
			require.NoError(t, err)
			require.True(t, stored)
		}
		fingerprint, err := apicontract.FingerprintRecordset(recordset)
		require.NoError(t, err)
		require.NoError(t, store.PutExecution(context.Background(), apicontract.ExecutionRecord{
			Ref: ref, Scope: apicontract.ExecutionRecordScope{StoreID: projectID, Project: projectID, Environment: "prod"},
			QueryID: "orders", Parameters: map[string]apicontract.TypedValueOrSet{}, BindingsApplied: []apicontract.Binding{},
			Principal:         apicontract.ExecutionPrincipal{ID: "alice", Roles: []string{}, Groups: []string{}},
			PolicyFingerprint: api.SecurePolicyFingerprint(), ExecutedAt: executedAt.Format(time.RFC3339Nano),
			Limitations: []apicontract.Limitation{}, Provenance: apicontract.Provenance{
				Source: "sales", Collection: "orders", QueryID: "orders", Mode: apicontract.ProvenanceModeLive,
				ObservedAt: executedAt.Format(time.RFC3339Nano), ExecutionProfile: apicontract.ExecutionProfileProtected,
			},
			AuthorizedFields: []apicontract.FieldAccessRef{{
				StoreID: projectID, Project: projectID, Environment: "prod", Source: "sales", Collection: "orders", Column: "id",
			}},
			RowCount: len(recordset.Rows), ResultFingerprint: fingerprint, ResultComplete: complete,
			SnapshotRef: snapshotRef, Measurements: []apicontract.ScalarMeasurement{},
		}))
		return ref
	}
	complete := true
	request := apicontract.CompareRequest{
		SecurityContextID: api.SecurityContextID(), QueryID: "orders", Key: []string{"id"},
	}

	t.Run("complete 847-row execution replays", func(t *testing.T) {
		ref := put("complete-847", rows(847), &complete, true)
		side := apicontract.CompareSideSpec{Kind: apicontract.CompareSideRecord, Execution: &ref}
		replayed, err := loadCompareRecordSide(context.Background(), request, side)
		require.NoError(t, err)
		require.Len(t, replayed.recordset.Rows, 847)
		require.True(t, replayed.receipt.Reproducible)
	})

	for _, test := range []struct {
		name      string
		execution string
		complete  *bool
		retain    bool
		wantCode  apicontract.ErrorCode
		rowCount  int
	}{
		{name: "legacy unknown completeness", execution: "legacy", complete: nil, retain: true, wantCode: apicontract.ErrCodeSourceUnavailable, rowCount: 2},
		{name: "known incomplete", execution: "incomplete", complete: boolPointer(false), retain: true, wantCode: apicontract.ErrCodeSourceUnavailable, rowCount: 100},
		{name: "missing snapshot takes lifecycle precedence", execution: "missing", complete: nil, retain: false, wantCode: apicontract.ErrCodeSnapshotExpired, rowCount: 2},
		{name: "complete snapshot exceeds input cap", execution: "over-cap", complete: &complete, retain: true, wantCode: apicontract.ErrCodeSourceUnavailable, rowCount: compareInputMaximumRows + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			ref := put(test.execution, rows(test.rowCount), test.complete, test.retain)
			side := apicontract.CompareSideSpec{Kind: apicontract.CompareSideRecord, Execution: &ref}
			_, err := loadCompareRecordSide(context.Background(), request, side)
			var contractErr *contractError
			require.ErrorAs(t, err, &contractErr)
			require.Equal(t, test.wantCode, contractErr.Code)
		})
	}
}

func boolPointer(value bool) *bool { return &value }
