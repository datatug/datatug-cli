package endpoints

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/incidentstore"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/investigation"
	"github.com/stretchr/testify/require"
)

const compareFactsCustomerDTQL = `from:
  name: Customer
where:
  op: In
  left:
    field: CustomerId
  right:
    param: CustomerId
`

func TestCompareFactsUsesOneNativeSQLiteSetExecutionPerSide(t *testing.T) {
	projectDir, projectID := writeRunQueryTestProject(t)
	queriesDir := filepath.Join(projectDir, "queries", "customers")
	mustWriteFile(t, filepath.Join(queriesDir, "customer-invoices.query.json"), `{
        "id": "customer-invoices",
        "title": "Customers by cohort",
        "type": "DTQL",
        "parameters": [
			{"id":"CustomerId","type":"integer","isRequired":true,"isMultiValue":true,"meta":{"entity":"Customer","field":"CustomerId"}}
        ]
    }`)
	mustWriteFile(t, filepath.Join(queriesDir, "customer-invoices.query.dtql"), compareFactsCustomerDTQL)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	repository := t.TempDir()
	require.NoError(t, api.ConfigureExecutionEvidence(map[string]string{projectID: projectDir}, []incidentstore.ConfiguredStore{{
		StoreID: "ops", Kind: incidents.StoreLocationDedicatedRepository, Repository: repository,
	}}, executionstore.Options{PrivateDir: t.TempDir()}))
	t.Cleanup(func() { require.NoError(t, api.CloseExecutionEvidence()) })

	projectScope := investigation.ProjectScope{StoreID: api.LocalStoreID, ProjectID: projectID, Environment: scope.Environment}
	fact := func(id, value, role string) investigation.Fact {
		return investigation.Fact{
			ID: id, Entity: "Customer", Field: "CustomerId", Value: investigation.NewIntegerValue(value),
			Origin: investigation.FactOriginManual, Enabled: true, Role: role,
			Layer: investigation.FactLayerCanonical, Scope: &projectScope,
		}
	}
	incidentStore, err := api.IncidentStoreByID(projectID, "ops")
	require.NoError(t, err)
	created, err := incidentStore.Create(context.Background(), incidents.CreateMutation{
		MutationID: "create-compare-facts", StoreID: "ops", UID: "compare-facts", Title: "Compare cohorts", At: time.Now().UTC(),
		Reporter:       incidents.Actor{Kind: incidents.ActorHuman, ID: "alice", Via: incidents.ActorViaAPI},
		PrimaryProject: projectScope,
		CanonicalContext: incidents.CanonicalContext{Facts: []investigation.Fact{
			fact("affected-5", "5", investigation.FactRoleAffected),
			fact("control-6", "6", investigation.FactRoleHealthyControl),
		}},
	})
	require.NoError(t, err)
	req := apicontract.CompareRequest{
		SecurityContextID: scope.SecurityContextID, QueryID: "customers/customer-invoices",
		Incident: &created.Projection.Ref, MutationID: "compare-facts-run", Key: []string{"CustomerId"},
		Left: apicontract.CompareSideSpec{
			Kind: apicontract.CompareSideFacts, StoreID: api.LocalStoreID, Project: projectID,
			Environment: scope.Environment, CohortRole: apicontract.CompareCohortAffected,
		},
		Right: apicontract.CompareSideSpec{
			Kind: apicontract.CompareSideFacts, StoreID: api.LocalStoreID, Project: projectID,
			Environment: scope.Environment, CohortRole: apicontract.CompareCohortControl,
		},
	}
	result, err := computeCompareWith(context.Background(), req, compareDependencies{
		executeSide: executeCompareSide,
		preflight:   preflightCompareIncident,
		appendRun:   appendCompareRun,
	})
	require.NoError(t, err)
	require.Equal(t, 1, result.Summary.Added)
	require.Equal(t, 1, result.Summary.Removed)
	require.NotEqual(t, result.Left.Execution.ExecutionID, result.Right.Execution.ExecutionID)
	require.Equal(t, "ops", result.Left.Execution.StoreID)
	require.Equal(t, "ops", result.Right.Execution.StoreID)

	leftStore, err := api.ExecutionEvidenceStoreByID(projectID, result.Left.Execution.StoreID)
	require.NoError(t, err)
	leftRecord, err := leftStore.Execution(context.Background(), result.Left.Execution)
	require.NoError(t, err)
	require.Len(t, leftRecord.BindingsApplied, 1)
	require.Equal(t, "CustomerId", leftRecord.BindingsApplied[0].ParameterID)
	require.True(t, leftRecord.BindingsApplied[0].Value.IsSet())
	require.Equal(t, [][]string{{"affected-5"}}, leftRecord.BindingsApplied[0].ValueFactIDs)
	events, err := incidentStore.Events(context.Background(), created.Projection.Ref, 0)
	require.NoError(t, err)
	compareEvents := 0
	for _, event := range events {
		if event.Type == incidents.EventCompareRun {
			compareEvents++
			comparison, ok := comparisonFromEvent(event)
			require.True(t, ok)
			require.Equal(t, incidents.ExecutionRef(result.Left.Execution), comparison.Left)
			require.Equal(t, incidents.ExecutionRef(result.Right.Execution), comparison.Right)
		}
	}
	require.Equal(t, 1, compareEvents)
	executedAgain := 0
	_, err = computeCompareWith(context.Background(), req, compareDependencies{
		preflight: preflightCompareIncident,
		executeSide: func(context.Context, apicontract.CompareRequest, apicontract.CompareSideSpec) (compareSideData, error) {
			executedAgain++
			return compareSideData{}, nil
		},
		appendRun: appendCompareRun,
	})
	require.Error(t, err)
	require.Zero(t, executedAgain)
	var duplicate *compareComputationError
	require.ErrorAs(t, err, &duplicate)
	require.NotNil(t, duplicate.comparison)
	require.Equal(t, incidents.ExecutionRef(result.Left.Execution), duplicate.comparison.Left)
	require.Equal(t, incidents.ExecutionRef(result.Right.Execution), duplicate.comparison.Right)

	recordRequest := apicontract.CompareRequest{
		SecurityContextID: scope.SecurityContextID, QueryID: req.QueryID, Key: []string{"CustomerId"},
		Left:  apicontract.CompareSideSpec{Kind: apicontract.CompareSideRecord, Execution: &result.Left.Execution},
		Right: apicontract.CompareSideSpec{Kind: apicontract.CompareSideRecord, Execution: &result.Right.Execution},
	}
	replayed, err := loadCompareRecordSide(context.Background(), recordRequest, recordRequest.Left)
	require.NoError(t, err)
	require.Equal(t, result.Left.Execution, replayed.receipt.Execution)
	require.True(t, replayed.receipt.Reproducible)
	require.Len(t, replayed.recordset.Rows, 1)

	recordRequest.QueryID = "another-query"
	_, err = loadCompareRecordSide(context.Background(), recordRequest, recordRequest.Left)
	var mismatch *contractError
	require.ErrorAs(t, err, &mismatch)
	require.Equal(t, apicontract.ErrCodeInvalidRequest, mismatch.Code)

	projectEvidence, err := api.ExecutionEvidenceStoreByID(projectID, projectID)
	require.NoError(t, err)
	collisionRecord := leftRecord
	collisionRecord.Ref.StoreID = projectID
	collisionRecord.Ref.ExecutionID = result.Left.Execution.ExecutionID
	collisionRows := replayed.recordset
	collisionRows.Rows = append([][]apicontract.TypedValue(nil), collisionRows.Rows...)
	collisionRows.Rows[0] = append([]apicontract.TypedValue(nil), collisionRows.Rows[0]...)
	for index, column := range collisionRows.Columns {
		if column.Name == "CustomerId" {
			collisionRows.Rows[0][index] = apicontract.NewNumberValue(7)
		}
	}
	snapshotRef, stored, err := projectEvidence.PutSnapshot(context.Background(), collisionRecord.Ref, collisionRows, time.Now().UTC())
	require.NoError(t, err)
	require.True(t, stored)
	collisionRecord.SnapshotRef = snapshotRef
	require.NoError(t, projectEvidence.PutExecution(context.Background(), collisionRecord))
	recordRequest.QueryID = req.QueryID
	recordRequest.Left.Execution = &collisionRecord.Ref
	projectReplay, err := loadCompareRecordSide(context.Background(), recordRequest, recordRequest.Left)
	require.NoError(t, err)
	require.Equal(t, projectID, projectReplay.receipt.Execution.StoreID)
	require.NotEqual(t, replayed.recordset.Rows, projectReplay.recordset.Rows)

	missing := collisionRecord
	missing.Ref.ExecutionID = "missing-snapshot"
	missing.SnapshotRef = ""
	require.NoError(t, projectEvidence.PutExecution(context.Background(), missing))
	recordRequest.Left.Execution = &missing.Ref
	_, err = loadCompareRecordSide(context.Background(), recordRequest, recordRequest.Left)
	var expired *contractError
	require.ErrorAs(t, err, &expired)
	require.Equal(t, apicontract.ErrCodeSnapshotExpired, expired.Code)

	policyDir := t.TempDir()
	mustWriteFile(t, filepath.Join(policyDir, "cohort.yaml"), `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata: {name: current-cohort-policy}
default: deny
scopes:
  - path: /Customer
    rules:
      - id: current-cohort-row
        effect: allow
        operations: [query]
        where:
          op: "=="
          left: {field: CustomerId}
          right: {value: 5}
        fields: [CustomerId, FirstName]
`)
	policySession, err := secureread.NewSession(secureread.SessionOptions{As: "support", PoliciesDir: policyDir})
	require.NoError(t, err)
	require.NotNil(t, policySession.Principal)
	require.Equal(t, "support", policySession.Principal.ID)
	api.ConfigureSecureSession(policySession, map[string]string{projectID: projectDir}, api.Capabilities{})
	recordRequest.SecurityContextID = api.SecurityContextID()
	recordRequest.Left.Execution = &result.Left.Execution
	policyReplay, err := loadCompareRecordSide(context.Background(), recordRequest, recordRequest.Left)
	require.NoError(t, err)
	for _, column := range policyReplay.recordset.Columns {
		require.NotEqual(t, "Email", column.Name)
	}
	require.Len(t, policyReplay.receipt.Limitations, 1)
	require.Equal(t, "current-cohort-policy", policyReplay.receipt.Limitations[0].Policy)
	require.True(t, policyReplay.receipt.Limitations[0].RowsFiltered)
	require.Contains(t, policyReplay.receipt.Limitations[0].HiddenColumns, "Email")
	policyReq := apicontract.CompareRequest{
		SecurityContextID: recordRequest.SecurityContextID, QueryID: req.QueryID, Key: []string{"CustomerId"},
		Left:  apicontract.CompareSideSpec{Kind: apicontract.CompareSideRecord, Execution: &result.Left.Execution},
		Right: apicontract.CompareSideSpec{Kind: apicontract.CompareSideRecord, Execution: &result.Left.Execution},
	}
	policyResult, err := computeCompareWith(context.Background(), policyReq, compareDependencies{
		executeSide: func(context.Context, apicontract.CompareRequest, apicontract.CompareSideSpec) (compareSideData, error) {
			return policyReplay, nil
		},
	})
	require.NoError(t, err)
	require.True(t, policyResult.PolicyLimited)
	for _, column := range policyResult.Columns {
		require.NotEqual(t, "Email", column.Name)
	}
}

func TestProveNativeFactsBindingFailsClosedForNonDTQLBeforeExecution(t *testing.T) {
	projectDir, projectID := writeRunQueryTestProject(t)
	queryPath := filepath.Join(projectDir, "queries", "customers", "customer-invoices.query.json")
	mustWriteFile(t, queryPath, `{
        "id":"customer-invoices","type":"SQL",
		"parameters":[{"id":"CustomerId","type":"integer","isRequired":true,"isMultiValue":true,"meta":{"entity":"Customer","field":"CustomerId"}}]
    }`)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })

	_, err := proveNativeFactsBinding(context.Background(), "customers/customer-invoices", apicontract.CompareSideSpec{
		Kind: apicontract.CompareSideFacts, StoreID: api.LocalStoreID, Project: projectID,
		Environment: scope.Environment, CohortRole: apicontract.CompareCohortAffected,
	}, "Customer", "CustomerId")
	var contractErr *contractError
	require.ErrorAs(t, err, &contractErr)
	require.Equal(t, apicontract.ErrCodeSourceUnavailable, contractErr.Code)
}

func TestProveNativeFactsBindingRejectsINBelowOR(t *testing.T) {
	projectDir, projectID := writeRunQueryTestProject(t)
	queryDir := filepath.Join(projectDir, "queries", "customers")
	mustWriteFile(t, filepath.Join(queryDir, "customer-invoices.query.json"), `{
        "id":"customer-invoices","type":"DTQL",
        "parameters":[{"id":"CustomerId","type":"integer","isRequired":true,"isMultiValue":true,"meta":{"entity":"Customer","field":"CustomerId"}}]
    }`)
	mustWriteFile(t, filepath.Join(queryDir, "customer-invoices.query.dtql"), `from:
  name: Customer
where:
  or:
    - op: In
      left: {field: CustomerId}
      right: {param: CustomerId}
    - op: "=="
      left: {field: Country}
      right: {value: Brazil}
`)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })

	_, err := proveNativeFactsBinding(context.Background(), "customers/customer-invoices", apicontract.CompareSideSpec{
		Kind: apicontract.CompareSideFacts, StoreID: api.LocalStoreID, Project: projectID,
		Environment: scope.Environment, CohortRole: apicontract.CompareCohortAffected,
	}, "Customer", "CustomerId")
	var contractErr *contractError
	require.ErrorAs(t, err, &contractErr)
	require.Equal(t, apicontract.ErrCodeSourceUnavailable, contractErr.Code)
}
