package commands

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/demofixtures/factories"
	"github.com/datatug/datatug-cli/pkg/reconcileapi"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/dataroles"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/reconcile"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// fakeAgent answers agent-info for projects and hands every other request
// to handle.
func fakeReconcileAgent(t *testing.T, projects []string, handle http.HandlerFunc) string {
	t.Helper()
	refs := make([]apicontract.AgentProjectRef, 0, len(projects))
	for _, id := range projects {
		refs = append(refs, apicontract.AgentProjectRef{ID: id})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/datatug/agent-info" {
			_ = json.NewEncoder(w).Encode(apicontract.AgentInfo{Version: "test", Principal: apicontract.AgentPrincipal{ID: "alice", Roles: []string{}, Groups: []string{}},
				SecurityContextID: "sc", Projects: refs})
			return
		}
		handle(w, r)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func answerJSON(v any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(v) }
}

func refuse(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write([]byte(`{"error":{"code":"INTERNAL","message":"nope","requestId":"r"}}`))
}

func requireReconcileExit(t *testing.T, code int, command *cobra.Command, args ...string) string {
	t.Helper()
	output, err := runReconcileTestCommand(t, command, args...)
	require.Equal(t, code, reconcileExitCodeOf(err), "%v: %s", err, output)
	return output
}

func TestReconcileCommandRefusals(t *testing.T) {
	const closed = "http://127.0.0.1:1"
	requireReconcileExit(t, reconcileExitError, reconcileCommand(), "--query", "q", "--family", "f", "--kind", "prod")
	requireReconcileExit(t, reconcileExitError, reconcileCommand(), "--query", "q", "--family", "f", "--field", "nopolicy")
	requireReconcileExit(t, reconcileExitError, reconcileCommand(), "--query", "q", "--family", "f", "--limit", "0")
	requireReconcileExit(t, reconcileExitError, reconcileCommand(), "--query", "q")
	requireReconcileExit(t, reconcileExitError, reconcileCommand(), "--agent", closed, "--profile", "p")
	two := fakeReconcileAgent(t, []string{"a", "b"}, refuse)
	requireReconcileExit(t, reconcileExitError, reconcileCommand(), "--agent", two, "--profile", "p")
	requireReconcileExit(t, reconcileExitError, reconcileCommand(), "--agent", two, "--project", "a", "--profile", "p")
	garbage := fakeReconcileAgent(t, []string{"a"}, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not json")) })
	requireReconcileExit(t, reconcileExitError, reconcileCommand(), "--agent", garbage, "--profile", "p")
}

func TestReconcileCommandPrintsEveryKindOfFinding(t *testing.T) {
	value := apicontract.NewStringValue("x")
	result := reconcile.Result{
		Mode: reconcile.ModeBaseline, Baseline: "a", Key: []string{"code"}, Presence: reconcile.PresenceSubset, Status: reconcile.StatusPartial,
		Instances: []reconcile.InstanceResult{
			{Instance: reconcile.Instance{ID: "a"}, Status: reconcile.StatusPartial, Authoritative: true, DriftColumns: []string{"amount", "secret"}},
			{Instance: reconcile.Instance{ID: "b"}, Status: reconcile.StatusInconclusive, Unavailable: &reconcile.Unavailable{Reason: reconcile.UnavailablePermissionDenied, Message: "refused"}},
		},
		Columns: []reconcile.ColumnStatus{{Column: "secret", HiddenIn: []string{"c"}, Compared: true}, {Column: "amount", Types: map[string]string{"a": "number", "c": "string"}}},
		Unexpected: []reconcile.RecordDiscrepancy{{Key: []apicontract.TypedValue{value}, Extra: []string{"c"},
			Fields: []reconcile.FieldDifference{{Column: "name", Policy: reconcile.FieldIdentical, NoConsensus: true, Groups: []reconcile.ValueGroup{{Value: &value, Instances: []string{"a"}, Authoritative: true}, {Instances: []string{"c"}}}}}}},
		Inconclusive: []reconcile.RecordDiscrepancy{{Key: []apicontract.TypedValue{value}, Inconclusive: []string{"c"}}, {Ordinal: 3, Missing: []string{"a"}, Extra: []string{"c"}, PresenceNoConsensus: true}},
		Expected:     []reconcile.RecordDiscrepancy{{Key: []apicontract.TypedValue{value}, Extra: []string{"d"}, ExtraExpected: true}},
		Truncated:    true,
	}
	agent := fakeReconcileAgent(t, []string{"a"}, answerJSON(reconcileapi.Response{Result: result}))
	output := requireReconcileExit(t, reconcileExitInconclusive, reconcileCommand(), "--agent", agent, "--profile", "p")
	for _, want := range []string{
		"p: partial (baseline a, key code, presence subset)",
		"(baseline)",
		"permission_denied: refused",
		"extra in c\n",
		"extra in d (expected)",
		"cannot judge absence in c",
		`"x" (baseline) {a} | <group> {c}; no consensus`,
		"secret: hidden in c",
		"amount: conflicting types, not compared",
		"detail lists are truncated",
		"drift: amount, secret",
		"  #3\n    presence: no consensus\n    missing in a\n    extra in c\n",
	} {
		require.Contains(t, output, want)
	}
}

func TestReconcileSaveAndListRefusals(t *testing.T) {
	_, _, dir := serveFactoriesDemo(t)
	requireReconcileExit(t, reconcileExitError, reconcileCommand(), "save", "x", "--query", "q", "--family", "f", "--limit", "0")
	requireReconcileExit(t, reconcileExitError, reconcileCommand(), "save", "x", "--query", "q", "--family", "f")
	requireReconcileExit(t, reconcileExitError, reconcileCommand(), "save", "x", "--project", filepath.Join(dir, "missing"), "--query", "q", "--key", "k", "--family", "f")
	requireReconcileExit(t, reconcileExitError, reconcileCommand(), "save", "x", "--project", dir, "--query", "q", "--key", "k", "--family", "f", "--presence", "subset")
	requireReconcileExit(t, 0, reconcileCommand(), "save", "untitled", "--project", dir, "--query", "q", "--key", "k", "--family", "f")
	body, err := os.ReadFile(filepath.Join(dir, reconcile.ProfilesFolder, "untitled.json"))
	require.NoError(t, err)
	require.Contains(t, string(body), `"title": "untitled"`)

	requireReconcileExit(t, reconcileExitError, reconcileCommand(), "list", "--project", filepath.Join(dir, "missing"))
	require.NoError(t, os.WriteFile(filepath.Join(dir, reconcile.ProfilesFolder, "broken.json"), []byte("{"), 0o644))
	requireReconcileExit(t, reconcileExitError, reconcileCommand(), "list", "--project", dir)
	require.NoError(t, os.Remove(filepath.Join(dir, reconcile.ProfilesFolder, "broken.json")))

	t.Chdir(dir)
	output := requireReconcileExit(t, 0, reconcileCommand(), "list")
	require.Contains(t, output, factories.ProfileCountries)

	// Several registered projects and no project folder: --project is required.
	_, _, _ = serveFactoriesDemo(t)
	t.Chdir(t.TempDir())
	_, err = runReconcileTestCommand(t, reconcileCommand(), "list")
	require.ErrorContains(t, err, "--project is required")
}

func TestChecksRunAgainstAFakeAgent(t *testing.T) {
	requireReconcileExit(t, checksRunExitError, detectiveUICommand("checks"), "run", "c", "--agent", "http://127.0.0.1:1")
	requireReconcileExit(t, checksRunExitError, detectiveUICommand("checks"), "run", "c", "--agent", fakeReconcileAgent(t, []string{"a", "b"}, refuse))
	requireReconcileExit(t, checksRunExitError, detectiveUICommand("checks"), "run", "c", "--agent", fakeReconcileAgent(t, []string{"a"}, refuse))
	requireReconcileExit(t, checksRunExitError, detectiveUICommand("checks"), "run", "c", "--env", "e", "--agent", fakeReconcileAgent(t, []string{"a"}, refuse))
	requireReconcileExit(t, checksRunExitError, detectiveUICommand("checks"), "run", "c", "--env", "e", "--agent", fakeReconcileAgent(t, []string{"a"}, answerJSON(detective.ActionResponse{})))

	state := detective.State{Capabilities: detective.Capabilities{CanRead: true}, Environments: []string{"e1", "e2"}, Checks: []detective.Check{
		{ID: "other"},
		{ID: "mixed", Steps: []detective.Step{{Kind: "query"}, {Kind: "reconcile"}}},
		{ID: "c", Steps: []detective.Step{{ID: "r", Kind: "reconcile", Profile: "p"}}},
	}}
	run := detective.CheckRun{CheckID: "c", Outcome: "unknown", ExecutionStatus: "completed", Steps: []detective.StepResult{
		{StepID: "r", Kind: "reconcile", Profile: "p", Outcome: "unknown", ExecutionStatus: "error", ErrorCode: "TIMEOUT", Reconciliation: &reconcile.Result{
			Status: reconcile.StatusInconclusive, Instances: []reconcile.InstanceResult{
				{Instance: reconcile.Instance{ID: "e1"}, Status: reconcile.StatusConsistent},
				{Instance: reconcile.Instance{ID: "e2"}, Status: reconcile.StatusInconclusive, Unavailable: &reconcile.Unavailable{Reason: reconcile.UnavailablePermissionDenied}},
			}}},
		{StepID: "q", Kind: "query", Outcome: "unknown", ExecutionStatus: "skipped"},
	}}
	var environment string
	agent := fakeReconcileAgent(t, []string{"a"}, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/datatug/detective/state") {
			answerJSON(state)(w, r)
			return
		}
		var body detective.ActionRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		environment = body.Environment
		answerJSON(detective.ActionResponse{Run: &run})(w, r)
	})
	output := requireReconcileExit(t, checksRunExitUnknown, detectiveUICommand("checks"), "run", "c", "--agent", agent)
	require.Equal(t, "e1", environment, "a Check of reconcile steps runs in any environment")
	require.Contains(t, output, "r (reconcile): unknown [error TIMEOUT]")
	require.Contains(t, output, "q (query): unknown [skipped]")
	require.Contains(t, output, "e2 inconclusive: permission_denied")
	require.NotContains(t, output, "e1 consistent")
	requireReconcileExit(t, checksRunExitError, detectiveUICommand("checks"), "run", "mixed", "--agent", agent)
	run.Outcome = "pass"
	requireReconcileExit(t, 0, detectiveUICommand("checks"), "run", "c", "--agent", agent)

	env, err := checkRunEnvironment(detective.State{Environments: []string{"only"}}, "x")
	require.NoError(t, err)
	require.Equal(t, "only", env)
}

func TestDbRolesRefusals(t *testing.T) {
	_, _, dir := serveFactoriesDemo(t)
	_, err := runReconcileTestCommand(t, dbRolesCommand(), "suggest", "../x", "--project", dir)
	require.Equal(t, exitCodeUsage, reconcileExitCodeOf(err))
	_, err = runReconcileTestCommand(t, dbRolesCommand(), "suggest", "nope", "--project", dir)
	require.ErrorContains(t, err, "no stored tables")
	_, err = runReconcileTestCommand(t, dbRolesCommand(), "suggest", "factory", "--project", filepath.Join(dir, "missing"))
	require.Error(t, err)
	model := filepath.Join(dir, "dbmodels", factories.Family)
	corrupt := func(path string) func() {
		original, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		require.NoError(t, os.WriteFile(path, []byte("{"), 0o644))
		return func() { require.NoError(t, os.WriteFile(path, original, 0o644)) }
	}
	for _, path := range []string{
		filepath.Join(model, "main", "tables", "countries", "countries.columns.json"),
		filepath.Join(model, factories.Family+".refs.json"),
		filepath.Join(model, factories.Family+".indexes.json"),
	} {
		undo := corrupt(path)
		_, err = runReconcileTestCommand(t, dbRolesCommand(), "suggest", "factory", "--project", dir)
		require.Error(t, err, path)
		undo()
	}
	rolesFile := filepath.Join(dir, dataroles.FileName)
	require.NoError(t, os.WriteFile(rolesFile, []byte("{"), 0o644))
	_, err = runReconcileTestCommand(t, dbRolesCommand(), "suggest", "factory", "--project", dir)
	require.Error(t, err)
	_, err = runReconcileTestCommand(t, dbRolesCommand(), "list", "--project", dir)
	require.Error(t, err)
	_, err = runReconcileTestCommand(t, dbRolesCommand(), "list", "--project", filepath.Join(dir, "missing"))
	require.Error(t, err)
	require.NoError(t, os.Remove(rolesFile))

	_, err = runReconcileTestCommand(t, dbRolesCommand(), "confirm", "no-slash", "--project", dir)
	require.Equal(t, exitCodeUsage, reconcileExitCodeOf(err))
	_, err = runReconcileTestCommand(t, dbRolesCommand(), "confirm", "factory/countries", "--project", filepath.Join(dir, "missing"))
	require.Error(t, err)
	_, err = runReconcileTestCommand(t, dbRolesCommand(), "confirm", "factory/countries", "--project", dir)
	require.Equal(t, 1, reconcileExitCodeOf(err), "an unclassified collection cannot be confirmed")
	previous := rolesCurrentUser
	t.Cleanup(func() { rolesCurrentUser = previous })
	rolesCurrentUser = func() (*user.User, error) { return nil, errors.New("no user") }
	_, err = runReconcileTestCommand(t, dbRolesCommand(), "confirm", "factory/countries", "--project", dir)
	require.ErrorContains(t, err, "current user")
	rolesCurrentUser = previous

	output, err := runReconcileTestCommand(t, dbRolesCommand(), "suggest", "factory", "--project", dir)
	require.NoError(t, err, output)
	output, err = runReconcileTestCommand(t, dbRolesCommand(), "confirm", "factory/currencies", "--project", dir, "--key", "code,name")
	require.NoError(t, err, output)
	require.Contains(t, output, "key code,name")
	output, err = runReconcileTestCommand(t, dbRolesCommand(), "confirm", "factory/audit_events", "--project", dir, "--key", "id")
	require.NoError(t, err, output)
	require.Contains(t, output, "key id, consensus, presence required", "a key on a collection without a rule starts one")
	t.Chdir(dir)
	output, err = runReconcileTestCommand(t, dbRolesCommand(), "list")
	require.NoError(t, err, output)
	require.Contains(t, output, "factory/currencies")
}

func TestDemoInstallFactoriesRefusals(t *testing.T) {
	_, err := runReconcileTestCommand(t, demoInstallCommand(), "factories", "--project-id", "Not Plain")
	require.Equal(t, exitCodeUsage, reconcileExitCodeOf(err))

	previousAbs := absolutePath
	t.Cleanup(func() { absolutePath = previousAbs })
	absolutePath = func(string) (string, error) { return "", errors.New("no cwd") }
	_, err = runReconcileTestCommand(t, demoInstallCommand(), "factories")
	require.Equal(t, exitCodeUsage, reconcileExitCodeOf(err))
	absolutePath = previousAbs

	_, projectID, dir := serveFactoriesDemo(t)
	other := filepath.Join(t.TempDir(), "other")
	for _, args := range [][]string{
		{"factories", other, "--project-id", projectID},
		{"factories", dir, "--project-id", "factories-elsewhere"},
	} {
		_, err = runReconcileTestCommand(t, demoInstallCommand(), args...)
		require.Equal(t, exitCodeUsage, reconcileExitCodeOf(err), args)
	}
	foreign := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(foreign, "datatug-project.json"), []byte(`{"id":"x"}`), 0o644))
	_, err = runReconcileTestCommand(t, demoInstallCommand(), "factories", foreign, "--project-id", "factories-foreign")
	require.Equal(t, exitCodeUsage, reconcileExitCodeOf(err))

	boom := errors.New("boom")
	restore := func() {
		demoInstallMkdirAll, demoInstallWriteFile = os.MkdirAll, os.WriteFile
		demoFactoriesGenerate, demoFactoriesRegistration, demoFactoriesRegister, demoFactoriesInit = factories.Generate, demoRegistration, registerDemoProject, initCommandAction
	}
	t.Cleanup(restore)
	fresh := func() string { return filepath.Join(t.TempDir(), "demo") }
	demoFactoriesRegistration = func(string) (string, error) { return "", boom }
	_, err = runReconcileTestCommand(t, demoInstallCommand(), "factories", fresh(), "--project-id", "factories-fail")
	require.ErrorIs(t, err, boom)
	restore()
	demoInstallMkdirAll = func(string, os.FileMode) error { return boom }
	_, err = runReconcileTestCommand(t, demoInstallCommand(), "factories", fresh(), "--project-id", "factories-fail")
	require.ErrorIs(t, err, boom)
	restore()
	demoInstallWriteFile = func(string, []byte, os.FileMode) error { return boom }
	_, err = runReconcileTestCommand(t, demoInstallCommand(), "factories", fresh(), "--project-id", "factories-fail")
	require.ErrorIs(t, err, boom)
	restore()
	demoFactoriesInit = func(*cobra.Command, []string) error { return boom }
	_, err = runReconcileTestCommand(t, demoInstallCommand(), "factories", fresh(), "--project-id", "factories-fail")
	require.ErrorIs(t, err, boom)
	restore()
	demoFactoriesGenerate = func(context.Context, string, datatug.ProjectStore) error { return boom }
	_, err = runReconcileTestCommand(t, demoInstallCommand(), "factories", fresh(), "--project-id", "factories-fail")
	require.ErrorIs(t, err, boom)
	restore()
	demoFactoriesRegister = func(string, string) error { return boom }
	_, err = runReconcileTestCommand(t, demoInstallCommand(), "factories", fresh(), "--project-id", "factories-fail")
	require.ErrorIs(t, err, boom)
}

func TestWriteClassificationsFiltersByFamily(t *testing.T) {
	var out strings.Builder
	writeClassifications(&out, []dataroles.Classification{
		{Target: dataroles.Target{Family: "a", Collection: "x"}, Role: dataroles.RoleReference},
		{Target: dataroles.Target{Family: "b", Collection: "y"}, Role: dataroles.RoleMaster},
	}, "b")
	require.NotContains(t, out.String(), "a/x")
	require.Contains(t, out.String(), "b/y")
}
