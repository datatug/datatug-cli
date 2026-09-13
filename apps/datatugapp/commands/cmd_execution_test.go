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
)

func TestExecutionCommandUsesRealAgentEndpoints(t *testing.T) {
	const projectID = "payments"
	projectDir := t.TempDir()
	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	if err != nil {
		t.Fatal(err)
	}
	api.ConfigureSecureSession(session, map[string]string{projectID: projectDir}, api.Capabilities{})
	if err := api.ConfigureExecutionEvidence(map[string]string{projectID: projectDir}, nil, executionstore.Options{PrivateDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = api.CloseExecutionEvidence() })
	store, err := api.ExecutionEvidenceStoreByID(projectID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	ref := apicontract.ExecutionRef{StoreID: projectID, ProjectID: projectID, ExecutionID: "exec-cli-1"}
	recordset := apicontract.Recordset{
		Columns: []apicontract.Column{{Name: "id", Type: "integer"}},
		Rows:    [][]apicontract.TypedValue{{apicontract.NewIntegerValue("7")}},
	}
	snapshotRef, stored, err := store.PutSnapshot(context.Background(), ref, recordset, time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC))
	if err != nil || !stored {
		t.Fatalf("PutSnapshot: stored=%v err=%v", stored, err)
	}
	fingerprint, err := apicontract.FingerprintRecordset(recordset)
	if err != nil {
		t.Fatal(err)
	}
	record := apicontract.ExecutionRecord{
		Ref: ref, Scope: apicontract.ExecutionRecordScope{StoreID: projectID, Project: projectID, Environment: "prod"},
		DTQLHash: strings.Repeat("1", 64), Parameters: map[string]apicontract.TypedValueOrSet{}, BindingsApplied: []apicontract.Binding{},
		Principal: apicontract.ExecutionPrincipal{ID: "alice", Roles: []string{}, Groups: []string{}}, PolicyFingerprint: api.SecurePolicyFingerprint(),
		ExecutedAt: "2026-09-13T10:00:00Z", Limitations: []apicontract.Limitation{},
		Provenance:       apicontract.Provenance{Source: "sales", Collection: "orders", Mode: apicontract.ProvenanceModeLive, ObservedAt: "2026-09-13T10:00:00Z", ExecutionProfile: apicontract.ExecutionProfileProtected},
		AuthorizedFields: []apicontract.FieldAccessRef{{StoreID: projectID, Project: projectID, Environment: "prod", Source: "sales", Collection: "orders", Column: "id"}},
		RowCount:         1, ResultFingerprint: fingerprint, SnapshotRef: snapshotRef, Measurements: []apicontract.ScalarMeasurement{},
	}
	if err := store.PutExecution(context.Background(), record); err != nil {
		t.Fatal(err)
	}

	router := httprouter.New()
	endpoints.RegisterDatatugHandlers("", router, endpoints.RegisterAllHandlers, nil, func(r *http.Request) (context.Context, error) {
		return r.Context(), nil
	}, func(http.ResponseWriter, *http.Request, apicore.RequestDTO, verify.RequestOptions, int, apicore.ContextProvider, apicore.Worker) {
		panic("legacy handler unexpectedly invoked")
	})
	agent := httptest.NewServer(router)
	t.Cleanup(agent.Close)

	t.Run("list human output", func(t *testing.T) {
		var out bytes.Buffer
		cmd := executionCommand()
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"--agent", agent.URL, "--project", projectID, "--environment", "prod", "list"})
		if err := cmd.ExecuteContext(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), ref.ExecutionID) || !strings.Contains(out.String(), "rows=1") {
			t.Fatalf("output = %q", out.String())
		}
	})

	t.Run("show snapshot JSON", func(t *testing.T) {
		var out bytes.Buffer
		cmd := executionCommand()
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"--agent", agent.URL, "--project", projectID, "--environment", "prod", "--json", "show", ref.ExecutionID, "--snapshot"})
		if err := cmd.ExecuteContext(context.Background()); err != nil {
			t.Fatal(err)
		}
		var response apicontract.SnapshotReadResponse
		if err := apicontract.DecodeStrict(out.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Execution != ref || response.Recordset == nil || len(response.Recordset.Rows) != 1 {
			t.Fatalf("snapshot = %+v", response)
		}
	})
}
