package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/demofixtures"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withRule adds rule (one YAML flow mapping) to the agent rule set's scope
// for table in the demo policy, at the same scope as the allow.
func withRule(t *testing.T, dir, table, rule string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(demofixtures.PolicyPath))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	scope := "    - path: /" + table + "\n      rules:\n"
	require.Contains(t, string(data), scope)
	data = []byte(strings.Replace(string(data), scope, scope+"        - "+rule+"\n", 1))
	require.NoError(t, os.WriteFile(path, data, 0o600))
}

// AC:instance-deny-reconcile-inconclusive at the CLI: a rule that selects
// the instance a project read opens takes part in it; one that selects
// another instance does not.
func TestPerimeterProjectReadInstanceRule(t *testing.T) {
	dir := demoForTest(t)
	withRule(t, dir, "Customer", "{id: not-here, effect: deny, operations: [query], instance: other/chinook}")
	rows, stderr, code := readAs(t, "", "--source", "chinook", "--from", "Customer")
	require.Equal(t, 0, code, stderr)
	require.Len(t, rows, 8, "a rule for another instance is absent")

	withRule(t, dir, "Customer", "{id: never-local-chinook, effect: deny, operations: [query], instance: local/chinook}")
	_, stderr, code = readAs(t, "", "--source", "chinook", "--from", "Customer")
	assert.Equal(t, exitCodeAccessDenied, code, stderr)
	assert.Contains(t, stderr, "never-local-chinook")
}

// AC:unknown-instance-denied: the demo environment has no kind, so a policy
// with an environment rule denies every read of it.
func TestPerimeterProjectReadEnvironmentRuleWithoutKind(t *testing.T) {
	dir := demoForTest(t)
	withRule(t, dir, "Invoice", "{id: no-production-invoices, effect: deny, operations: [query], environment: production}")
	_, stderr, code := readAs(t, "", "--source", "chinook", "--from", "Customer")
	assert.Equal(t, exitCodeAccessDenied, code, stderr)
}

func TestPerimeterProjectReadRefusesABadSelector(t *testing.T) {
	dir := demoForTest(t)
	withRule(t, dir, "Customer", "{id: bare, effect: deny, operations: [query], instance: chinook}")
	_, stderr, code := readAs(t, "", "--source", "chinook", "--from", "Customer")
	assert.Equal(t, exitCodeUsage, code, stderr)
	assert.Contains(t, stderr, "<environment>/<catalog>")
}

func TestPerimeterProposeUnderSelectorRules(t *testing.T) {
	dir := demoForTest(t)
	base := []string{"policy", "exception", "propose", "--project", "datatug-demo", "--source", "chinook", "--role", "agent", "--reason", "support escalation list"}

	// A rule for another instance is absent: the exception may open Employee.
	withRule(t, dir, "Customer", "{id: not-here, effect: deny, operations: [query], instance: other/chinook}")
	view := propose(t, "--source", "chinook", "--role", "agent", "--table", "Employee", "--fields", "Title")
	assert.Equal(t, "pending", string(view.State))

	// A deny for this instance is a rule of the role on the table.
	withRule(t, dir, "Customer", "{id: no-employees-here, effect: deny, operations: [query], instance: local/chinook}")
	policy := filepath.Join(dir, filepath.FromSlash(demofixtures.PolicyPath))
	data, err := os.ReadFile(policy)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(policy, []byte(strings.Replace(string(data), "path: /Customer", "path: /Employee", 1)), 0o600))
	_, stderr, code := runCLI(t, "", append(append([]string{}, base...), "--table", "Employee", "--fields", "Title")...)
	assert.Equal(t, exitCodeUsage, code)
	assert.Contains(t, stderr, "already has a rule on Employee")

	// An environment rule and an environment with no kind: the policy denies
	// every read here, so no exception can be added to it.
	withRule(t, dir, "Invoice", "{id: no-production-invoices, effect: deny, operations: [query], environment: production}")
	_, stderr, code = runCLI(t, "", append(append([]string{}, base...), "--table", "Track", "--fields", "Name")...)
	assert.Equal(t, exitCodeUsage, code)
	assert.Contains(t, stderr, "denies every read of local chinook")
}

func TestPerimeterProposeRefusesAScopedAccessPolicy(t *testing.T) {
	dir := demoForTest(t)
	policy := filepath.Join(dir, filepath.FromSlash(demofixtures.PolicyPath))
	require.NoError(t, os.WriteFile(policy, []byte(`apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata: {name: agent}
default: deny
scopes:
  - path: /Customer
    rules:
      - {id: read, effect: allow, operations: [query], instance: local/chinook}
`), 0o600))
	_, stderr, code := runCLI(t, "", "policy", "exception", "propose", "--project", "datatug-demo", "--source", "chinook", "--role", "agent",
		"--reason", "support escalation list", "--table", "Employee", "--fields", "Title")
	assert.Equal(t, exitCodeUsage, code)
	assert.Contains(t, stderr, "binds no roles to rule sets")
}

// Case variants of the environment or source name open the same database on
// a case-insensitive disk; they must not step around an instance rule.
func TestPerimeterProjectReadInstanceRuleHoldsForCaseVariants(t *testing.T) {
	dir := demoForTest(t)
	withRule(t, dir, "Customer", "{id: never-local-chinook, effect: deny, operations: [query], instance: local/chinook}")
	for _, args := range [][]string{
		{"--env", "LOCAL", "--source", "chinook", "--from", "Customer"},
		{"--source", "CHINOOK", "--from", "Customer"},
		{"--env", "Local", "--source", "Chinook", "--from", "Customer"},
	} {
		rows, stderr, code := readAs(t, "", args...)
		assert.NotEqual(t, 0, code, "%v read %d rows: %s", args, len(rows), stderr)
		assert.Empty(t, rows)
	}
}
