package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/demofixtures/factories"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/server/endpoints"
	"github.com/datatug/datatug-core/pkg/dataroles"
	"github.com/datatug/datatug-core/pkg/reconcile"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/julienschmidt/httprouter"
	"github.com/sneat-co/sneat-go-core/apicore"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

var factoriesDemoRuns atomic.Int64

// serveFactoriesDemo installs the five-factory demo with `demo install
// factories` into a temp folder and serves it to a real agent router.
func serveFactoriesDemo(t *testing.T) (agentURL, projectID, dir string) {
	t.Helper()
	projectID = fmt.Sprintf("factories-cli-%d", factoriesDemoRuns.Add(1))
	dir = filepath.Join(t.TempDir(), "demo")
	output, err := runReconcileTestCommand(t, demoInstallCommand(), "factories", dir, "--project-id", projectID)
	require.NoError(t, err, output)
	require.Contains(t, output, "Five-factory demo "+projectID+" installed")
	filestore.SetProjectPath(projectID, dir)
	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	api.ConfigureSecureSession(session, map[string]string{projectID: dir}, api.Capabilities{AllowWrites: true, EvidencePrivateDir: t.TempDir(), SnapshotPolicies: map[string]api.SnapshotProjectPolicy{
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
	router := httprouter.New()
	endpoints.RegisterDatatugHandlersWithCapabilities("", router, endpoints.RegisterAllHandlers, nil, func(r *http.Request) (context.Context, error) {
		return r.Context(), nil
	}, func(http.ResponseWriter, *http.Request, apicore.RequestDTO, verify.RequestOptions, int, apicore.ContextProvider, apicore.Worker) {
		panic("legacy handler unexpectedly invoked")
	}, endpoints.Capabilities{AllowWrites: true})
	agent := httptest.NewServer(router)
	t.Cleanup(agent.Close)
	return agent.URL, projectID, dir
}

func runReconcileTestCommand(t *testing.T, command *cobra.Command, args ...string) (string, error) {
	t.Helper()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(io.Discard) // main prints errors; Reported ones never
	command.SetArgs(args)
	err := command.ExecuteContext(context.Background())
	return output.String(), err
}

func reconcileExitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var coder ExitCoder
	if errors.As(err, &coder) {
		return coder.ExitCode()
	}
	return -1
}

func TestReconcileCommandFactoriesDemo(t *testing.T) {
	agent, projectID, dir := serveFactoriesDemo(t)
	reconcileArgs := func(args ...string) []string {
		return append([]string{"--agent", agent, "--project", projectID}, args...)
	}

	t.Run("profile text output", func(t *testing.T) {
		output, err := runReconcileTestCommand(t, reconcileCommand(), reconcileArgs("--profile", factories.ProfileCountries)...)
		require.Equal(t, reconcileExitDiscrepancies, reconcileExitCodeOf(err), output)
		t.Logf("datatug reconcile --profile %s\n%s", factories.ProfileCountries, output)
		require.Contains(t, output, "reference-countries: discrepancies (consensus, key code, presence required)")
		require.Contains(t, output, "unexpected (1):\n  [code=\"IE\"]\n    name (identical): \"Ireland\" {factory-a-prod, factory-b-prod, factory-c-prod, factory-d-prod} | \"Eire\" {factory-e-prod}; deviating: factory-e-prod")
		require.Contains(t, output, reconcile.SnapshotCaveat)
	})

	t.Run("ad hoc json", func(t *testing.T) {
		output, err := runReconcileTestCommand(t, reconcileCommand(), reconcileArgs("--query", "countries", "--key", "code", "--family", "factory", "--kind", "production", "--json")...)
		require.Equal(t, reconcileExitDiscrepancies, reconcileExitCodeOf(err), output)
		var result reconcile.Result
		require.NoError(t, json.Unmarshal([]byte(output), &result), output)
		require.Equal(t, reconcile.StatusDiscrepancies, result.Status)
		require.Equal(t, "IE", result.Unexpected[0].Key[0].Str)
	})

	t.Run("consistent exits 0 and partial or missing exits nonzero", func(t *testing.T) {
		output, err := runReconcileTestCommand(t, reconcileCommand(), reconcileArgs("--profile", factories.ProfileSettings)...)
		require.NoError(t, err, output)
		require.Contains(t, output, "factory-settings: consistent (baseline factory-a-prod")
		require.Contains(t, output, "(baseline)")
		require.Contains(t, output, "expected (1):")

		output, err = runReconcileTestCommand(t, reconcileCommand(), reconcileArgs("--profile", factories.ProfileProducts, "--groups-only")...)
		require.Equal(t, reconcileExitDiscrepancies, reconcileExitCodeOf(err), output)
		require.Contains(t, output, "<group> {factory-a-prod, factory-b-prod, factory-d-prod, factory-e-prod} | <group> {factory-c-prod}; deviating: factory-c-prod")
		require.Contains(t, output, "columns:\n  local_notes: missing in factory-a-prod, factory-b-prod, factory-c-prod, factory-d-prod")

		output, err = runReconcileTestCommand(t, reconcileCommand(), reconcileArgs("--profile", factories.ProfileCurrencies, "--env", "factory-a-prod,factory-b-prod", "--limit", "5")...)
		require.NoError(t, err, "an explicit list narrows the profile's instances: %s", output)
		require.Contains(t, output, "reference-currencies: consistent")

		output, err = runReconcileTestCommand(t, reconcileCommand(), reconcileArgs("--query", "currencies", "--key", "code", "--family", "factory", "--presence", "subset")...)
		require.Equal(t, reconcileExitError, reconcileExitCodeOf(err), output)
		require.Contains(t, err.Error(), "baseline mode")

		output, err = runReconcileTestCommand(t, reconcileCommand(), reconcileArgs("--query", "currencies", "--key", "code", "--env", "factory-d-prod,factory-e-prod", "--baseline", "factory-e-prod", "--presence", "subset", "--field", "name=normalized", "--default-field", "identical")...)
		require.NoError(t, err, output)
		require.Contains(t, output, "missing in factory-d-prod (expected)")
	})

	t.Run("confirmed classification supplies the key", func(t *testing.T) {
		args := reconcileArgs("--query", "countries", "--family", "factory", "--kind", "production")
		output, err := runReconcileTestCommand(t, reconcileCommand(), args...)
		require.Equal(t, reconcileExitError, reconcileExitCodeOf(err), output)
		require.Contains(t, err.Error(), "no key")

		output, err = runReconcileTestCommand(t, dbRolesCommand(), "suggest", "factory", "--project", dir)
		require.NoError(t, err, output)
		require.Contains(t, output, "7 new suggestion(s) for factory")
		require.Contains(t, output, "factory/countries")
		output, err = runReconcileTestCommand(t, reconcileCommand(), args...)
		require.Equal(t, reconcileExitError, reconcileExitCodeOf(err), "a suggestion never takes effect: %s", output)

		output, err = runReconcileTestCommand(t, dbRolesCommand(), "confirm", "factory/countries", "--project", dir, "--reason", "ISO 3166 list")
		require.NoError(t, err, output)
		require.Contains(t, output, "confirmed")
		output, err = runReconcileTestCommand(t, reconcileCommand(), args...)
		require.Equal(t, reconcileExitDiscrepancies, reconcileExitCodeOf(err), output)
		require.Contains(t, output, "countries: discrepancies (consensus, key code, presence required)")

		output, err = runReconcileTestCommand(t, dbRolesCommand(), "list", "factory", "--project", dir)
		require.NoError(t, err, output)
		require.Contains(t, output, "factory/products")
		output, err = runReconcileTestCommand(t, dbRolesCommand(), "list", "--project", dir, "--json")
		require.NoError(t, err, output)
		var doc dataroles.Document
		require.NoError(t, json.Unmarshal([]byte(output), &doc))
		roles := map[string]dataroles.Role{}
		for _, c := range doc.Classifications {
			roles[c.Target.Collection] = c.Role
		}
		require.Equal(t, map[string]dataroles.Role{
			"audit_events": dataroles.RoleEvent, "countries": dataroles.RoleReference, "currencies": dataroles.RoleReference,
			"factory_settings": dataroles.RoleConfiguration, "inventory_movements": dataroles.RoleTransactional,
			"orders": dataroles.RoleTransactional, "products": dataroles.RoleMaster,
		}, roles)

		output, err = runReconcileTestCommand(t, dbRolesCommand(), "dismiss", "factory/audit_events", "--project", dir)
		require.NoError(t, err, output)
		require.Contains(t, output, "dismissed")
		output, err = runReconcileTestCommand(t, dbRolesCommand(), "confirm", "factory/products", "--project", dir, "--role", "master", "--key", "sku")
		require.NoError(t, err, output)
		require.Contains(t, output, "key sku")
	})

	t.Run("save and list", func(t *testing.T) {
		output, err := runReconcileTestCommand(t, reconcileCommand(), "save", "currencies-a-b", "--project", dir, "--query", "currencies", "--key", "code", "--env", "factory-a-prod,factory-b-prod", "--title", "A against B")
		require.NoError(t, err, output)
		require.FileExists(t, filepath.Join(dir, reconcile.ProfilesFolder, "currencies-a-b.json"))
		output, err = runReconcileTestCommand(t, reconcileCommand(), "list", "--project", dir)
		require.NoError(t, err, output)
		require.Contains(t, output, "currencies-a-b\tquery currencies\tenvs factory-a-prod,factory-b-prod\tA against B")
		require.Contains(t, output, "reference-countries\tquery countries\tfamily factory kind production")
		output, err = runReconcileTestCommand(t, reconcileCommand(), reconcileArgs("--profile", "currencies-a-b")...)
		require.NoError(t, err, output)
		output, err = runReconcileTestCommand(t, reconcileCommand(), "list", "--project", dir, "--json")
		require.NoError(t, err, output)
		var profiles []reconcile.Profile
		require.NoError(t, json.Unmarshal([]byte(output), &profiles))
		require.Len(t, profiles, 5)
	})

	t.Run("checks run", func(t *testing.T) {
		output, err := runReconcileTestCommand(t, detectiveUICommand("checks"), "run", factories.CheckID, "--agent", agent, "--project", projectID)
		require.Equal(t, checksRunExitFail, reconcileExitCodeOf(err), output)
		t.Logf("datatug checks run %s\n%s", factories.CheckID, output)
		require.Contains(t, output, "check reference-data-agrees: fail (execution completed)")
		require.Contains(t, output, "  countries (reconcile): fail")
		require.Contains(t, output, "    reference-countries: discrepancies;")
		require.Contains(t, output, "    factory-e-prod discrepancies")
		require.Contains(t, output, "    factory-d-prod discrepancies")

		output, err = runReconcileTestCommand(t, detectiveUICommand("checks"), "run", factories.CheckID, "--agent", agent, "--project", projectID, "--env", "factory-b-prod", "--json")
		require.Equal(t, checksRunExitFail, reconcileExitCodeOf(err), output)
		var run struct {
			Outcome string `json:"outcome"`
			Steps   []struct {
				Reconciliation *reconcile.Result `json:"reconciliation"`
			} `json:"steps"`
		}
		require.NoError(t, json.Unmarshal([]byte(output), &run), output)
		require.Equal(t, "fail", run.Outcome)
		require.NotNil(t, run.Steps[0].Reconciliation)
	})

	t.Run("demo install regenerates in place", func(t *testing.T) {
		output, err := runReconcileTestCommand(t, demoInstallCommand(), "factories", dir, "--project-id", projectID)
		require.NoError(t, err, output)
		require.Contains(t, output, "regenerated")
		readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
		require.NoError(t, err)
		require.Contains(t, string(readme), "factory-d-prod | currency `CHF` is missing")
	})
}
