package endpoints

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/demofixtures/factories"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/reconcile"
	"github.com/stretchr/testify/require"
)

// factoriesPolicy lets every factory read the reference tables except
// factory E, which a rule selecting its instance denies, and denies every
// production environment the orders table (backstage spec/features/perimeter,
// AC:instance-deny-reconcile-inconclusive and AC:environment-deny).
const factoriesPolicy = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: factories
default: deny
scopes:
  - path: /countries
    rules:
      - id: never-factory-e
        effect: deny
        operations: [query]
        instance: factory-e-prod/factory
      - id: read-countries
        effect: allow
        operations: [query]
  - path: /currencies
    rules:
      - id: read-currencies
        effect: allow
        operations: [query]
  - path: /orders
    rules:
      - id: no-production-orders
        effect: deny
        operations: [query]
        environment: production
      - id: read-orders
        effect: allow
        operations: [query]
`

// serveFactoriesUnderPolicy serves the five-factory demo as `datatug serve
// --project <dir> --as analyst` does once the project has a policies/ folder.
func serveFactoriesUnderPolicy(t *testing.T, policy string) (projectID, dir string) {
	t.Helper()
	return serveFactoriesProjectAs(t, func(dir string) secureread.SessionOptions {
		policies := filepath.Join(dir, "policies")
		require.NoError(t, os.MkdirAll(policies, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(policies, "factories.yaml"), []byte(policy), 0o600))
		return secureread.SessionOptions{As: "analyst", PoliciesDir: policies}
	})
}

func TestReconcileFactoriesInstanceRuleDeniesFactoryE(t *testing.T) {
	projectID, _ := serveFactoriesUnderPolicy(t, factoriesPolicy)
	response := profileRun(t, projectID, factories.ProfileCountries)
	result := response.Result
	e := instanceResult(t, result, "factory-e-prod")
	require.Equal(t, reconcile.StatusInconclusive, e.Status)
	require.NotNil(t, e.Unavailable)
	require.Equal(t, reconcile.UnavailablePermissionDenied, e.Unavailable.Reason)
	require.NotContains(t, response.Receipts, "factory-e-prod", "no row of factory E was read")
	for _, id := range []string{"factory-a-prod", "factory-b-prod", "factory-c-prod", "factory-d-prod"} {
		require.Equal(t, reconcile.StatusConsistent, instanceResult(t, result, id).Status, id)
		require.Contains(t, response.Receipts, id)
	}
	require.Equal(t, 4, result.Summary.ComparedInstances)
	require.NotEqual(t, reconcile.StatusConsistent, result.Status, "a refused factory is never consistent")
	require.Equal(t, reconcile.StatusInconclusive, result.Status)
	// The rule names factory E only: the currencies profile reads all five.
	currencies := profileRun(t, projectID, factories.ProfileCurrencies).Result
	require.Equal(t, 5, currencies.Summary.ComparedInstances)
	require.Equal(t, reconcile.StatusDiscrepancies, instanceResult(t, currencies, factories.MissingCurrencyIn).Status)
}

func TestReconcileFactoriesEnvironmentRuleDeniesProduction(t *testing.T) {
	projectID, _ := serveFactoriesUnderPolicy(t, factoriesPolicy)
	read := func(environment string) error {
		_, err := executeLiveInstance(context.Background(), liveInstanceRequest{
			SecurityContextID: api.SecurityContextID(), QueryID: "orders", Project: projectID,
			Environment: environment, Source: factories.Catalog,
		}, map[string]apicontract.TypedValueOrSet{}, []apicontract.BindingOriginEntry{})
		return err
	}
	require.NoError(t, read("factory-a-test"))
	err := read("factory-a-prod")
	require.Error(t, err)
	require.Equal(t, reconcile.UnavailablePermissionDenied, reconcileUnavailable(err).Reason)
}

func TestReconcileFactoriesEnvironmentRuleNeedsAKind(t *testing.T) {
	// "Production" is no kind of the closed set: an environment file is not
	// checked when it is loaded, so it must not count as "not production".
	for _, kind := range []string{"", "Production", "prod"} {
		projectID, dir := serveFactoriesUnderPolicy(t, factoriesPolicy)
		setKindOnDisk(t, filepath.Join(dir, "environments", "factory-a-prod"), kind)
		_, err := executeLiveInstance(context.Background(), liveInstanceRequest{
			SecurityContextID: api.SecurityContextID(), QueryID: "orders", Project: projectID,
			Environment: "factory-a-prod", Source: factories.Catalog,
		}, map[string]apicontract.TypedValueOrSet{}, []apicontract.BindingOriginEntry{})
		require.Error(t, err, "kind %q: a policy that selects environments denies it", kind)
		require.Equal(t, reconcile.UnavailablePermissionDenied, reconcileUnavailable(err).Reason, kind)
	}
}

// setKindOnDisk rewrites the kind in an environment's files as a hand edit
// would; saving through the store refuses a kind outside the closed set.
func setKindOnDisk(t *testing.T, envDir, kind string) {
	t.Helper()
	pattern := regexp.MustCompile(`"kind":\s*"production"`)
	replacement := ""
	if kind != "" {
		replacement = `"kind": "` + kind + `"`
	} else {
		replacement = `"kind": ""`
	}
	edited := 0
	require.NoError(t, filepath.WalkDir(envDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".json" {
			return err
		}
		data, err := os.ReadFile(path)
		if err == nil && pattern.Match(data) {
			edited++
			err = os.WriteFile(path, pattern.ReplaceAll(data, []byte(replacement)), 0o600)
		}
		return err
	}))
	require.Positive(t, edited, "the environment stores its kind")
}

// A case variant of an environment name opens the same files on a
// case-insensitive disk (macOS); it must not step around an instance rule.
func TestInstanceRuleHoldsForCaseVariants(t *testing.T) {
	projectID, _ := serveFactoriesUnderPolicy(t, factoriesPolicy)
	for _, environment := range []string{"Factory-E-Prod", "FACTORY-E-PROD"} {
		data, err := executeLiveInstance(context.Background(), liveInstanceRequest{
			SecurityContextID: api.SecurityContextID(), QueryID: "countries", Project: projectID,
			Environment: environment, Source: factories.Catalog,
		}, map[string]apicontract.TypedValueOrSet{}, []apicontract.BindingOriginEntry{})
		require.Error(t, err, "%s read %d rows", environment, len(data.recordset.Rows))
	}
}
