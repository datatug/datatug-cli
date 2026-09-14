package commands

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/server/endpoints"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/julienschmidt/httprouter"
	"github.com/sneat-co/sneat-go-core/apicore"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
	"github.com/stretchr/testify/require"
)

func TestParseCompareSideMixedKinds(t *testing.T) {
	environment, err := parseCompareSide("env=before", "demo", "local", "")
	require.NoError(t, err)
	require.Equal(t, apicontract.CompareSideScope, environment.Kind)
	require.Equal(t, "before", environment.Environment)

	record, err := parseCompareSide("execution=evidence/demo/run-1", "ignored", "local", "ignored")
	require.NoError(t, err)
	require.Equal(t, apicontract.CompareSideRecord, record.Kind)
	require.Equal(t, "evidence", record.Execution.StoreID)
	require.Equal(t, "demo", record.Execution.ProjectID)
	require.Equal(t, "run-1", record.Execution.ExecutionID)
}

func TestParseCompareSideFactsUsesExplicitScope(t *testing.T) {
	side, err := parseCompareSide("facts=control", "demo", "local", "prod")
	require.NoError(t, err)
	require.Equal(t, apicontract.CompareSideFacts, side.Kind)
	require.Equal(t, apicontract.CompareCohortControl, side.CohortRole)
	require.Equal(t, "prod", side.Environment)

	_, err = parseCompareSide("facts=affected", "demo", "local", "")
	require.ErrorContains(t, err, "--environment")
}

func TestParseCompareSideRejectsAmbiguousSyntax(t *testing.T) {
	for _, value := range []string{"env=prod,facts=affected", "execution=one/two", "facts=suspected", "check=x", "env="} {
		t.Run(value, func(t *testing.T) {
			_, err := parseCompareSide(value, "demo", "local", "prod")
			require.Error(t, err)
		})
	}
}

func TestCompareCommandJSONAndHumanUseRealServer(t *testing.T) {
	const projectID = "payments"
	projectDir := t.TempDir()
	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	api.ConfigureSecureSession(session, map[string]string{projectID: projectDir}, api.Capabilities{})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	require.NoError(t, api.ConfigureExecutionEvidence(map[string]string{projectID: projectDir}, nil, executionstore.Options{PrivateDir: t.TempDir()}))
	t.Cleanup(func() { require.NoError(t, api.CloseExecutionEvidence()) })
	store, err := api.ExecutionEvidenceStoreByID(projectID, projectID)
	require.NoError(t, err)
	put := func(executionID, status string) {
		t.Helper()
		ref := apicontract.ExecutionRef{StoreID: projectID, ProjectID: projectID, ExecutionID: executionID}
		recordset := apicontract.Recordset{
			Columns: []apicontract.Column{{Name: "id", Type: "integer"}, {Name: "status", Type: "string"}},
			Rows:    [][]apicontract.TypedValue{{apicontract.NewIntegerValue("7"), apicontract.NewStringValue(status)}},
		}
		executedAt := time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)
		snapshotRef, stored, err := store.PutSnapshot(context.Background(), ref, recordset, executedAt)
		require.NoError(t, err)
		require.True(t, stored)
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
			AuthorizedFields: []apicontract.FieldAccessRef{
				{StoreID: projectID, Project: projectID, Environment: "prod", Source: "sales", Collection: "orders", Column: "id"},
				{StoreID: projectID, Project: projectID, Environment: "prod", Source: "sales", Collection: "orders", Column: "status"},
			},
			RowCount: 1, ResultFingerprint: fingerprint, SnapshotRef: snapshotRef, Measurements: []apicontract.ScalarMeasurement{},
		}))
	}
	put("left-run", "pending")
	put("right-run", "paid")

	router := httprouter.New()
	endpoints.RegisterDatatugHandlers("", router, endpoints.RegisterAllHandlers, nil, func(r *http.Request) (context.Context, error) {
		return r.Context(), nil
	}, func(http.ResponseWriter, *http.Request, apicore.RequestDTO, verify.RequestOptions, int, apicore.ContextProvider, apicore.Worker) {
		panic("legacy handler unexpectedly invoked")
	})
	agent := httptest.NewServer(router)
	t.Cleanup(agent.Close)
	baseArgs := []string{
		"--agent", agent.URL, "--project", projectID, "--query", "orders",
		"--left", "execution=payments/payments/left-run", "--right", "execution=payments/payments/right-run", "--key", "id",
	}

	t.Run("json", func(t *testing.T) {
		var output bytes.Buffer
		command := compareCommand()
		command.SetOut(&output)
		command.SetErr(&output)
		command.SetArgs(append(append([]string(nil), baseArgs...), "--json"))
		require.NoError(t, command.ExecuteContext(context.Background()), output.String())
		var result apicontract.CompareResult
		require.NoError(t, apicontract.DecodeStrict(output.Bytes(), &result))
		require.Equal(t, 1, result.Summary.Changed)
		require.Equal(t, "left-run", result.Left.Execution.ExecutionID)
		require.Equal(t, "right-run", result.Right.Execution.ExecutionID)
	})

	t.Run("human", func(t *testing.T) {
		var output bytes.Buffer
		command := compareCommand()
		command.SetOut(&output)
		command.SetErr(&output)
		command.SetArgs(baseArgs)
		require.NoError(t, command.ExecuteContext(context.Background()), output.String())
		require.True(t, strings.Contains(output.String(), "left-run -> right-run: +0 -0 ~1, 0 unchanged"), output.String())
	})
}
