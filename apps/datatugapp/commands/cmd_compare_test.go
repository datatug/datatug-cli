package commands

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/server/endpoints"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/julienschmidt/httprouter"
	_ "github.com/mattn/go-sqlite3"
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
	otherProjectDir := t.TempDir()
	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	api.ConfigureSecureSession(session, map[string]string{projectID: projectDir, "other-project": otherProjectDir}, api.Capabilities{})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	require.NoError(t, api.ConfigureExecutionEvidence(map[string]string{projectID: projectDir, "other-project": otherProjectDir}, nil, executionstore.Options{PrivateDir: t.TempDir()}))
	t.Cleanup(func() { require.NoError(t, api.CloseExecutionEvidence()) })
	store, err := api.ExecutionEvidenceStoreByID(projectID, projectID)
	require.NoError(t, err)
	put := func(executionID string, rows [][]apicontract.TypedValue) {
		t.Helper()
		ref := apicontract.ExecutionRef{StoreID: projectID, ProjectID: projectID, ExecutionID: executionID}
		recordset := apicontract.Recordset{
			Columns: []apicontract.Column{{Name: "id", Type: "integer"}, {Name: "status", Type: "string"}},
			Rows:    rows,
		}
		executedAt := time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)
		snapshotRef, stored, err := store.PutSnapshot(context.Background(), ref, recordset, executedAt)
		require.NoError(t, err)
		require.True(t, stored)
		fingerprint, err := apicontract.FingerprintRecordset(recordset)
		require.NoError(t, err)
		resultComplete := true
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
			RowCount: len(rows), ResultFingerprint: fingerprint, ResultComplete: &resultComplete,
			SnapshotRef: snapshotRef, Measurements: []apicontract.ScalarMeasurement{},
		}))
	}
	put("left-run", [][]apicontract.TypedValue{
		{apicontract.NewIntegerValue("7"), apicontract.NewStringValue("pending")},
		{apicontract.NewIntegerValue("8"), apicontract.NewStringValue("stuck")},
	})
	put("right-run", [][]apicontract.TypedValue{
		{apicontract.NewIntegerValue("7"), apicontract.NewStringValue("paid")},
		{apicontract.NewIntegerValue("9"), apicontract.NewStringValue("shipped")},
	})

	router := httprouter.New()
	endpoints.RegisterDatatugHandlers("", router, endpoints.RegisterAllHandlers, nil, func(r *http.Request) (context.Context, error) {
		return r.Context(), nil
	}, func(http.ResponseWriter, *http.Request, apicore.RequestDTO, verify.RequestOptions, int, apicore.ContextProvider, apicore.Worker) {
		panic("legacy handler unexpectedly invoked")
	})
	agent := httptest.NewServer(router)
	t.Cleanup(agent.Close)
	baseArgs := []string{
		"--agent", agent.URL, "--query", "orders",
		"--left", "execution=payments/payments/left-run", "--right", "execution=payments/payments/right-run", "--key", "id", "--distribution", "status",
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
		require.Equal(t, 1, result.Summary.Added)
		require.Equal(t, 1, result.Summary.Removed)
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
		require.Contains(t, output.String(), "left-run -> right-run: +1 -1 ~1, 0 unchanged")
		require.Contains(t, output.String(), "added [id=9]: id=9 status=\"shipped\"")
		require.Contains(t, output.String(), "removed [id=8]: id=8 status=\"stuck\"")
		require.Contains(t, output.String(), "changed [id=7]: status \"pending\" -> \"paid\"")
		require.Contains(t, output.String(), "distribution status:")
		require.Contains(t, output.String(), "\"pending\": left 1 (50.00%), right 0 (0.00%), ratio null")
	})
}

func TestCompareCommandInfersLiveMappedKeyThroughRealServer(t *testing.T) {
	const projectID = "shipping"
	projectDir := t.TempDir()
	filestore.SetProjectPath(projectID, projectDir)
	queryDir := filepath.Join(projectDir, "queries")
	require.NoError(t, os.MkdirAll(queryDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(queryDir, "shipping-rate-config.query.json"), []byte(`{
      "id":"shipping-rate-config","title":"Shipping rates","type":"DTQL",
      "recordsets":[{
        "id":"rates","title":"Rates","type":"recordset",
        "primaryKey":{"name":"PK_Rate","columns":["id"]},
        "columns":[
          {"name":"id","type":"number","meta":{"entity":"ShippingRate","field":"ID"}},
          {"name":"rate","type":"string","meta":{"entity":"ShippingRate","field":"Rate"}},
          {"name":"provider","type":"string","meta":{"entity":"ShippingRate","field":"Provider"}}
        ]
      }]
    }`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(queryDir, "shipping-rate-config.query.dtql"), []byte("from:\n  name: Rate\n"), 0o600))
	projectStore := filestore.NewProjectStore(projectID, projectDir)
	registerEnvironment := func(environment string, rows [][3]any) {
		t.Helper()
		dbPath := filepath.Join(projectDir, environment+".sqlite")
		db, err := sql.Open("sqlite3", dbPath)
		require.NoError(t, err)
		_, err = db.Exec(`CREATE TABLE Rate (id INTEGER PRIMARY KEY, rate TEXT NOT NULL, provider TEXT NOT NULL)`)
		require.NoError(t, err)
		tx, err := db.Begin()
		require.NoError(t, err)
		for _, row := range rows {
			_, err = tx.Exec(`INSERT INTO Rate(id,rate,provider) VALUES(?,?,?)`, row[0], row[1], row[2])
			require.NoError(t, err)
		}
		require.NoError(t, tx.Commit())
		require.NoError(t, db.Close())
		env := &datatug.Environment{DbServers: datatug.EnvDbServers{{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}}}
		env.ID = environment
		require.NoError(t, projectStore.SaveEnvironment(context.Background(), env))
		serverID := (&datatug.EnvDbServer{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}).GetID()
		catalog := &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: "sqlite3", Path: dbPath, DbModel: "shipping-rates"}}
		catalog.ID = "shipping-rates-" + environment
		require.NoError(t, projectStore.SaveEnvDbCatalog(context.Background(), environment, serverID, catalog.ID, catalog))
	}
	prodRows := make([][3]any, 501)
	uatRows := make([][3]any, 500)
	for i := 1; i <= len(prodRows); i++ {
		prodRows[i-1] = [3]any{i, fmt.Sprintf("%d.00", i), "FedEx"}
		if i <= len(uatRows) {
			uatRows[i-1] = prodRows[i-1]
		}
	}
	uatRows[0][1] = "999.00"
	registerEnvironment("prod", prodRows)
	registerEnvironment("uat", uatRows)
	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	api.ConfigureSecureSession(session, map[string]string{projectID: projectDir}, api.Capabilities{SnapshotPolicies: map[string]api.SnapshotProjectPolicy{
		projectID: {Sources: map[string]api.SnapshotSourcePolicy{"shipping-rates": {Allow: true}}},
	}})
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return filestore.NewStore("files", map[string]string{projectID: projectDir})
	}
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	require.NoError(t, api.ConfigureExecutionEvidence(map[string]string{projectID: projectDir}, nil, executionstore.Options{PrivateDir: t.TempDir()}))
	t.Cleanup(func() { require.NoError(t, api.CloseExecutionEvidence()) })

	router := httprouter.New()
	endpoints.RegisterDatatugHandlers("", router, endpoints.RegisterAllHandlers, nil, func(r *http.Request) (context.Context, error) {
		return r.Context(), nil
	}, func(http.ResponseWriter, *http.Request, apicore.RequestDTO, verify.RequestOptions, int, apicore.ContextProvider, apicore.Worker) {
		panic("legacy handler unexpectedly invoked")
	})
	agent := httptest.NewServer(router)
	t.Cleanup(agent.Close)
	var output bytes.Buffer
	command := compareCommand()
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{
		"--agent", agent.URL, "--project", projectID, "--query", "shipping-rate-config",
		"--left", "env=prod", "--right", "env=uat", "--distribution", "provider",
	})

	require.NoError(t, command.ExecuteContext(context.Background()), output.String())
	require.Contains(t, output.String(), "key: id")
	require.Contains(t, output.String(), "+0 -1 ~1, 499 unchanged")
	require.Contains(t, output.String(), "removed [id=501]: id=501 provider=\"FedEx\" rate=\"501.00\"")
	require.Contains(t, output.String(), "changed [id=1]: rate \"1.00\" -> \"999.00\"")
	require.Contains(t, output.String(), "distribution provider:")
}

func TestCompareAgentRequestTimeoutCoversTwoMaximumSideBudgets(t *testing.T) {
	require.Greater(t, compareAgentRequestTimeout, 2*30*time.Second)
}
