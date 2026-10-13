package endpoints

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/stretchr/testify/require"
)

func TestDetectiveExistingExecutionUsesReceiptAndSameRows(t *testing.T) {
	const project = "detective-replay"
	ctx := context.Background()
	projectDir := t.TempDir()
	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	api.ConfigureSecureSession(session, map[string]string{project: projectDir}, api.Capabilities{})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	require.NoError(t, api.ConfigureExecutionEvidence(map[string]string{project: projectDir}, nil, executionstore.Options{PrivateDir: t.TempDir(), ByteCap: 8 << 20}))
	t.Cleanup(func() { require.NoError(t, api.CloseExecutionEvidence()) })
	store, err := api.ExecutionEvidenceStoreByID(project, project)
	require.NoError(t, err)
	ref := apicontract.ExecutionRef{ProjectID: project, StoreID: project, ExecutionID: "exact-rows"}
	rows := apicontract.Recordset{Columns: []apicontract.Column{{Name: "id", Type: string(apicontract.ValueTypeInteger)}}, Rows: [][]apicontract.TypedValue{{apicontract.NewIntegerValue("1")}, {apicontract.NewIntegerValue("2")}}}
	at := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	snapshotRef, stored, err := store.PutSnapshot(ctx, ref, rows, at)
	require.NoError(t, err)
	require.True(t, stored)
	fingerprint, err := apicontract.FingerprintRecordset(rows)
	require.NoError(t, err)
	complete := true
	input := apicontract.NewIntegerValue("17")
	receipt := apicontract.ExecutionRecord{
		Ref: ref, Scope: apicontract.ExecutionRecordScope{StoreID: project, Project: project, Environment: "qa"},
		QueryID: "list", Parameters: map[string]apicontract.TypedValueOrSet{"invoice_id": apicontract.ScalarValue(input)}, BindingsApplied: []apicontract.Binding{},
		Principal:         apicontract.ExecutionPrincipal{ID: "alice", Roles: []string{}, Groups: []string{}},
		PolicyFingerprint: api.SecurePolicyFingerprint(), ExecutedAt: at.Format(time.RFC3339Nano),
		Limitations: []apicontract.Limitation{}, Provenance: apicontract.Provenance{Source: "sales", Collection: "invoices", QueryID: "list", Mode: apicontract.ProvenanceModeLive, ObservedAt: at.Format(time.RFC3339Nano), ExecutionProfile: apicontract.ExecutionProfileProtected},
		AuthorizedFields: []apicontract.FieldAccessRef{{StoreID: project, Project: project, Environment: "qa", Source: "sales", Collection: "invoices", Column: "id"}},
		RowCount:         2, ResultFingerprint: fingerprint, ResultComplete: &complete, SnapshotRef: snapshotRef, Measurements: []apicontract.ScalarMeasurement{},
	}
	require.NoError(t, store.PutExecution(ctx, receipt))
	result, inputs, err := loadDetectiveExecutionResult(ctx, project, "alice", "list", "qa", &ref, nil)
	require.NoError(t, err)
	require.Equal(t, ref, *result.Execution)
	require.Equal(t, rows, result.Recordset)
	require.Equal(t, input, *inputs["invoice_id"].Scalar)
	_, _, err = loadDetectiveExecutionResult(ctx, project, "alice", "list", "prod", &ref, nil)
	require.Error(t, err)
	_, _, err = loadDetectiveExecutionResult(ctx, project, "alice", "list", "qa", &ref, map[string]detective.InputValue{"invoice_id": {Scalar: &input}})
	require.Error(t, err)
	_, _, err = loadDetectiveExecutionResult(ctx, project, "alice", "other", "qa", &ref, nil)
	require.Error(t, err)
	_, _, err = loadDetectiveExecutionResult(ctx, project, "bob", "list", "qa", &ref, nil)
	require.Error(t, err)
	proof, proofInputs, err := loadDetectiveScenarioQueryProof(ctx, project, "alice", &ref)
	require.NoError(t, err)
	require.Equal(t, "list", proof.QueryID)
	require.Equal(t, "completed", proof.ExecutionStatus)
	require.Equal(t, input, *proofInputs["invoice_id"].Scalar)
	_, _, err = loadDetectiveScenarioQueryProof(ctx, project, "bob", &ref)
	require.Error(t, err)
	partial := receipt
	partial.Ref.ExecutionID = "partial-rows"
	partial.SnapshotRef = ""
	incomplete := false
	partial.ResultComplete = &incomplete
	require.NoError(t, store.PutExecution(ctx, partial))
	_, _, err = loadDetectiveScenarioQueryProof(ctx, project, "alice", &partial.Ref)
	require.Error(t, err)
	bad := receipt
	bad.Ref.ExecutionID = "bad-fingerprint"
	badFingerprintRef := bad.Ref
	bad.SnapshotRef, stored, err = store.PutSnapshot(ctx, badFingerprintRef, rows, at)
	require.NoError(t, err)
	require.True(t, stored)
	bad.ResultFingerprint = strings.Repeat("a", len(fingerprint))
	require.NoError(t, store.PutExecution(ctx, bad))
	_, _, err = loadDetectiveExecutionResult(ctx, project, "alice", "list", "qa", &badFingerprintRef, nil)
	require.Error(t, err)
}
