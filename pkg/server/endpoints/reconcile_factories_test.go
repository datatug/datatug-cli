package endpoints

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/demofixtures/factories"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/reconcileapi"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/reconcile"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/stretchr/testify/require"
)

// factoriesProjectRuns numbers the generated projects: filestore keeps the
// folder of a project id for the life of the process.
var factoriesProjectRuns atomic.Int64

// serveFactoriesProject generates the five-factory demo into a temp folder
// and configures this process as an unrestricted agent serving it.
func serveFactoriesProject(t *testing.T) (projectID, dir string) {
	t.Helper()
	return serveFactoriesProjectAs(t, func(string) secureread.SessionOptions {
		return secureread.SessionOptions{As: "alice", NoPolicies: true}
	})
}

// serveFactoriesProjectAs is serveFactoriesProject with the session options
// options returns for the generated project folder.
func serveFactoriesProjectAs(t *testing.T, options func(dir string) secureread.SessionOptions) (projectID, dir string) {
	t.Helper()
	projectID = fmt.Sprintf("factories-%d", factoriesProjectRuns.Add(1))
	dir = t.TempDir()
	filestore.SetProjectPath(projectID, dir)
	require.NoError(t, factories.Generate(context.Background(), dir, filestore.NewProjectStore(projectID, dir)))
	session, err := secureread.NewSession(options(dir))
	require.NoError(t, err)
	api.ConfigureSecureSession(session, map[string]string{projectID: dir}, api.Capabilities{EvidencePrivateDir: t.TempDir(), SnapshotPolicies: map[string]api.SnapshotProjectPolicy{
		projectID: {Sources: map[string]api.SnapshotSourcePolicy{factories.Catalog: {Allow: true}}},
	}})
	previousStore := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return filestore.NewStore("files", map[string]string{projectID: dir})
	}
	t.Cleanup(func() {
		storage.NewDatatugStore = previousStore
		api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	})
	require.NoError(t, api.ConfigureExecutionEvidence(map[string]string{projectID: dir}, nil, executionstore.Options{PrivateDir: t.TempDir()}))
	t.Cleanup(func() { require.NoError(t, api.CloseExecutionEvidence()) })
	return projectID, dir
}

func instanceResult(t *testing.T, result reconcile.Result, id string) reconcile.InstanceResult {
	t.Helper()
	for _, instance := range result.Instances {
		if instance.Instance.ID == id {
			return instance
		}
	}
	t.Fatalf("instance %s not in result", id)
	return reconcile.InstanceResult{}
}

func TestReconcileFactoriesFiveFactoriesOutlier(t *testing.T) {
	projectID, _ := serveFactoriesProject(t)
	response, err := computeReconcile(context.Background(), reconcileapi.Request{
		SecurityContextID: api.SecurityContextID(), Project: projectID, QueryID: "countries",
		Instances: &reconcile.InstanceSelector{Family: factories.Family, Kind: "production"},
		Rules:     &reconcile.Rules{Key: []string{"code"}},
	})
	require.NoError(t, err)
	result := response.Result
	require.Equal(t, reconcile.StatusDiscrepancies, result.Status)
	require.Len(t, result.Instances, 5)
	require.Len(t, response.Receipts, 5)
	require.Len(t, result.Unexpected, 1)
	ie := result.Unexpected[0]
	require.Equal(t, apicontract.NewStringValue("IE"), ie.Key[0])
	require.Len(t, ie.Fields, 1)
	name := ie.Fields[0]
	require.Equal(t, "name", name.Column)
	require.Equal(t, []string{"factory-e-prod"}, name.Deviating)
	require.Len(t, name.Groups, 2)
	require.Equal(t, []string{"factory-a-prod", "factory-b-prod", "factory-c-prod", "factory-d-prod"}, name.Groups[0].Instances)
	require.Equal(t, "Ireland", name.Groups[0].Value.Str)
	require.Equal(t, []string{"factory-e-prod"}, name.Groups[1].Instances)
	require.Equal(t, "Eire", name.Groups[1].Value.Str)
	for _, group := range name.Groups {
		require.False(t, group.Authoritative)
	}
	require.Equal(t, 6, result.Summary.Consistent)
	require.Equal(t, reconcile.StatusDiscrepancies, instanceResult(t, result, "factory-e-prod").Status)
	require.Equal(t, reconcile.StatusConsistent, instanceResult(t, result, "factory-a-prod").Status)
	require.Contains(t, result.Caveats, reconcile.SnapshotCaveat)
}

func profileRun(t *testing.T, projectID, profile string) reconcileapi.Response {
	t.Helper()
	response, err := computeReconcile(context.Background(), reconcileapi.Request{SecurityContextID: api.SecurityContextID(), Project: projectID, ProfileID: profile})
	require.NoError(t, err)
	return response
}

func findRecord(t *testing.T, records []reconcile.RecordDiscrepancy, key string) reconcile.RecordDiscrepancy {
	t.Helper()
	for _, record := range records {
		if record.Key[0].Str == key {
			return record
		}
	}
	t.Fatalf("record %s not listed", key)
	return reconcile.RecordDiscrepancy{}
}

func TestReconcileFactoriesExpectedLocalPriceAndSchemaDrift(t *testing.T) {
	projectID, _ := serveFactoriesProject(t)
	result := profileRun(t, projectID, factories.ProfileProducts).Result
	require.Equal(t, reconcile.StatusDiscrepancies, result.Status)
	require.Len(t, result.Unexpected, 1)
	bearing := result.Unexpected[0]
	require.Equal(t, factories.DescribedSKU, bearing.Key[0].Str)
	var description reconcile.FieldDifference
	for _, field := range bearing.Fields {
		if field.Column == "description" {
			description = field
		}
	}
	require.Equal(t, []string{factories.DescribedSKUIn}, description.Deviating)
	require.False(t, description.Expected)
	gear := findRecord(t, result.Expected, factories.PricedSKU)
	var price reconcile.FieldDifference
	for _, field := range gear.Fields {
		if field.Column == "price" {
			price = field
		}
	}
	require.True(t, price.Expected)
	require.Equal(t, reconcile.StatusConsistent, instanceResult(t, result, factories.PricedSKUIn).Status)
	require.Equal(t, reconcile.StatusDiscrepancies, instanceResult(t, result, factories.DescribedSKUIn).Status)
	var drift *reconcile.ColumnStatus
	for i := range result.Columns {
		if result.Columns[i].Column == factories.DriftColumn {
			drift = &result.Columns[i]
		}
	}
	require.NotNil(t, drift, "schema drift of products.%s is reported", factories.DriftColumn)
	require.Equal(t, []string{"factory-a-prod", "factory-b-prod", "factory-c-prod", "factory-d-prod"}, drift.MissingIn)
	for _, column := range result.Columns {
		require.NotEqual(t, "id", column.Column, "the surrogate id is ignored")
	}
}

func TestReconcileFactoriesMissingReferenceRecord(t *testing.T) {
	projectID, _ := serveFactoriesProject(t)
	result := profileRun(t, projectID, factories.ProfileCurrencies).Result
	require.Equal(t, reconcile.StatusDiscrepancies, result.Status)
	chf := findRecord(t, result.Unexpected, factories.MissingCurrency)
	require.Equal(t, []string{factories.MissingCurrencyIn}, chf.Missing)
	require.False(t, chf.MissingExpected)
	d := instanceResult(t, result, factories.MissingCurrencyIn)
	require.Equal(t, reconcile.StatusDiscrepancies, d.Status)
	require.Equal(t, 1, d.Missing)
}

func TestReconcileFactoriesSettingsDifferenceExpected(t *testing.T) {
	projectID, _ := serveFactoriesProject(t)
	result := profileRun(t, projectID, factories.ProfileSettings).Result
	require.Equal(t, reconcile.StatusConsistent, result.Status)
	require.Equal(t, reconcile.ModeBaseline, result.Mode)
	require.Len(t, result.Instances, 2)
	require.True(t, instanceResult(t, result, "factory-a-prod").Authoritative)
	smtp := findRecord(t, result.Expected, factories.TestSetting)
	require.True(t, smtp.Fields[0].Expected)
	require.Equal(t, []string{factories.TestSettingIn}, smtp.Fields[0].Deviating)
}

func TestReconcileFactoriesDeniedInstanceIsInconclusive(t *testing.T) {
	projectID, _ := serveFactoriesProject(t)
	previous := reconcileExecuteInstanceHook
	t.Cleanup(func() { reconcileExecuteInstanceHook = previous })
	read := map[string]bool{}
	reconcileExecuteInstanceHook = func(ctx context.Context, request liveInstanceRequest, parameters map[string]apicontract.TypedValueOrSet, origins []apicontract.BindingOriginEntry) (compareSideData, error) {
		if request.Environment == "factory-d-prod" {
			return compareSideData{}, newAccessDenied("access denied by policy")
		}
		read[request.Environment] = true
		return previous(ctx, request, parameters, origins)
	}
	response := profileRun(t, projectID, factories.ProfileCurrencies)
	result := response.Result
	require.False(t, read["factory-d-prod"], "no row of factory-d-prod was read")
	require.Len(t, read, 4)
	d := instanceResult(t, result, "factory-d-prod")
	require.Equal(t, reconcile.StatusInconclusive, d.Status)
	require.Equal(t, reconcile.UnavailablePermissionDenied, d.Unavailable.Reason)
	require.NotContains(t, response.Receipts, "factory-d-prod")
	require.NotEqual(t, reconcile.StatusConsistent, result.Status)
	require.Equal(t, reconcile.StatusInconclusive, result.Status)
	require.Equal(t, 4, result.Summary.ComparedInstances)
	require.Equal(t, reconcile.StatusConsistent, instanceResult(t, result, "factory-a-prod").Status)
}
