package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/demofixtures"
	"github.com/datatug/datatug-cli/pkg/policyexceptions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runCLI runs the root command with stdin and returns what it wrote and its exit code.
func runCLI(t *testing.T, stdin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	root := DatatugCommand()
	var out, errOut bytes.Buffer
	root.SetArgs(args)
	root.SetIn(strings.NewReader(stdin))
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SilenceUsage, root.SilenceErrors = true, true
	err := root.Execute()
	if err != nil {
		code = 1
		var coder ExitCoder
		if errors.As(err, &coder) {
			code = coder.ExitCode()
		}
		if !IsReported(err) {
			errOut.WriteString(err.Error())
		}
	}
	return out.String(), errOut.String(), code
}

// demoForTest installs the demo project in a fresh home and returns its folder.
func demoForTest(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	// The harness the tests run under is not a caller of these commands.
	restoreVar(t, &callerLookup)
	callerLookup = func(string) (string, bool) { return "", false }
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".datatug", "policies"), 0o700), "an empty ~/.datatug/policies")
	stdout, stderr, code := runCLI(t, "", "demo", "install", "--format", "json")
	require.Equal(t, 0, code, stderr)
	var result demoInstallResult
	require.NoError(t, json.Unmarshal([]byte(stdout), &result))
	require.Equal(t, "installed", result.Status)
	return result.ProjectDir
}

// asPerson makes stdin an interactive terminal for apply, reject and revoke.
func asPerson(t *testing.T) {
	t.Helper()
	restoreVar(t, &exceptionStdinIsTerminal)
	exceptionStdinIsTerminal = func(io.Reader) bool { return true }
}

func restoreVar[T any](t *testing.T, target *T) {
	original := *target
	t.Cleanup(func() { *target = original })
}

func readAs(t *testing.T, stdin string, extra ...string) (rows []map[string]any, stderr string, code int) {
	t.Helper()
	args := append([]string{"query", "run", "--project", "datatug-demo", "--as", "agent", "--role", "agent", "--format", "json"}, extra...)
	stdout, stderr, code := runCLI(t, stdin, args...)
	if code == 0 {
		require.NoError(t, json.Unmarshal([]byte(stdout), &rows), stdout)
	}
	return rows, stderr, code
}

func propose(t *testing.T, args ...string) exceptionView {
	t.Helper()
	stdout, stderr, code := runCLI(t, "", append([]string{"policy", "exception", "propose", "--project", "datatug-demo", "--format", "json", "--reason", "support escalation list"}, args...)...)
	require.Equal(t, 0, code, stderr)
	var view exceptionView
	require.NoError(t, json.Unmarshal([]byte(stdout), &view))
	return view
}

func auditEvents(t *testing.T) []string {
	t.Helper()
	stdout, stderr, code := runCLI(t, "", "policy", "exception", "audit", "--project", "datatug-demo", "--format", "json")
	require.Equal(t, 0, code, stderr)
	var audit struct {
		Events []policyexceptions.Event `json:"events"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &audit))
	var kinds []string
	for _, e := range audit.Events {
		kinds = append(kinds, e.Event+":"+e.Table)
		assert.NotContains(t, stdout, "Andrew", "no row value is audited")
	}
	return kinds
}

func keys(row map[string]any) []string {
	var names []string
	for name := range row {
		if name != "$key" {
			names = append(names, name)
		}
	}
	return names
}

// AC:demo-idempotent, AC:permitted-read, AC:masked, AC:denied-read,
// AC:no-tty-no-approval, AC:narrow-only, AC:approve-in-scope, AC:audit.
func TestPerimeterDemoJourney(t *testing.T) {
	dir := demoForTest(t)
	stdout, _, code := runCLI(t, "", "demo", "install", "--format", "json")
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, `"status":"unchanged"`)

	rows, stderr, code := readAs(t, "", "--source", "chinook", "--from", "Customer")
	require.Equal(t, 0, code, stderr)
	require.Len(t, rows, 8)
	for _, row := range rows {
		assert.Equal(t, "Canada", row["Country"])
		assert.Equal(t, "••••", row["Email"])
		assert.NotContains(t, row, "Phone")
		assert.NotContains(t, row, "Address")
	}
	assert.Contains(t, stderr, "masked: Customer.Email (pii)")

	notes, stderr, code := readAs(t, "", "--source", "support", "--from", "support-notes")
	require.Equal(t, 0, code, stderr)
	require.Len(t, notes, 1)

	// A paged read (--limit, as the onboarding skill runs it) keeps the inGitDB
	// key order instead of sorting on $key, which the field allow-list hides.
	paged, stderr, code := readAs(t, "", "--source", "support", "--from", "support-notes", "--limit", "3")
	require.Equal(t, 0, code, stderr)
	assert.LessOrEqual(t, len(paged), 3)
	for _, row := range paged {
		assert.Equal(t, "Canada", row["Country"])
	}

	_, stderr, code = readAs(t, "", "--source", "chinook", "--from", "Employee")
	require.Equal(t, exitCodeAccessDenied, code)
	assert.Equal(t, 1, strings.Count(stderr, "\n"), "one JSON object on stderr: %s", stderr)
	var denial denialJSON
	require.NoError(t, json.Unmarshal([]byte(stderr), &denial))
	assert.Equal(t, "ACCESS_DENIED", denial.Error.Code)
	assert.Equal(t, "agent", denial.Error.Policy)
	assert.Equal(t, "/Employee", denial.Error.Resource)
	assert.Equal(t, "query", denial.Error.Operation)
	assert.Contains(t, strings.ToLower(denial.Error.Message), "access denied", "the text refusal stays in the JSON for consumers that match on it")
	assert.Contains(t, denial.Error.Request.Command, "datatug policy exception propose --project datatug-demo --source chinook --role agent --table Employee --fields <field,...>")

	view := propose(t, "--source", "chinook", "--role", "agent", "--table", "employee", "--fields", "employeeid,FirstName,LastName,Title,Email")
	assert.Equal(t, policyexceptions.StatePending, view.State)
	assert.Equal(t, "Employee", view.Table, "the canonical table name")
	assert.Equal(t, []string{"EmployeeId", "FirstName", "LastName", "Title", "Email"}, view.Fields)
	assert.Equal(t, "policies/agent.yaml", view.Policy.Path)
	assert.Equal(t, "datatug policy exception apply "+view.ID+" --project datatug-demo", view.Next["apply"])
	assert.Len(t, view.ID, 16)

	store := filepath.Join(dir, ".datatug", "policy-exceptions", view.ID+".json")
	before, err := os.ReadFile(store)
	require.NoError(t, err)
	for _, verb := range []string{"apply", "reject", "revoke"} {
		_, stderr, code = runCLI(t, "Employee\n", "policy", "exception", verb, view.ID, "--project", "datatug-demo", "--format", "json")
		assert.Equal(t, exitCodeHumanTerminal, code, verb)
		assert.Contains(t, stderr, `"code":"HUMAN_TERMINAL_REQUIRED"`, verb)
		_, stderr, code = runCLI(t, "Employee\n", "policy", "exception", verb, view.ID, "--project", "datatug-demo")
		assert.Equal(t, exitCodeHumanTerminal, code, verb)
		assert.Contains(t, stderr, "interactive terminal")
	}
	after, _ := os.ReadFile(store)
	assert.Equal(t, before, after, "nothing changed without a terminal")

	asPerson(t)
	_, stderr, code = runCLI(t, "Employee\n", "policy", "exception", "apply", view.ID, "--project", "datatug-demo", "--fields", "EmployeeId,BirthDate")
	assert.Equal(t, exitCodeUsage, code)
	assert.Contains(t, stderr, "may only narrow")
	stdout, stderr, code = runCLI(t, "employee\n", "policy", "exception", "apply", view.ID, "--project", "datatug-demo", "--fields", "EmployeeId,FirstName,LastName,Title")
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "cancelled")
	assert.Contains(t, stdout, "all rows of Employee")
	stdout, stderr, code = runCLI(t, "Employee\n", "policy", "exception", "apply", view.ID, "--project", "datatug-demo", "--fields", "EmployeeId,FirstName,LastName,Title")
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "Applied exception "+view.ID)
	_, stderr, code = runCLI(t, "Employee\n", "policy", "exception", "apply", view.ID, "--project", "datatug-demo")
	assert.Equal(t, exitCodeUsage, code)
	assert.Contains(t, stderr, "is applied")

	rows, stderr, code = readAs(t, "", "--source", "chinook", "--from", "Employee")
	require.Equal(t, 0, code, stderr)
	require.Len(t, rows, 8)
	assert.ElementsMatch(t, []string{"EmployeeId", "FirstName", "LastName", "Title"}, keys(rows[0]))

	hidden := filepath.Join(t.TempDir(), "q.yaml")
	require.NoError(t, os.WriteFile(hidden, []byte("from: {name: Employee}\ncolumns:\n  - field: BirthDate\n"), 0o600))
	_, _, code = readAs(t, "", "--source", "chinook", "-f", hidden)
	assert.Equal(t, exitCodeAccessDenied, code, "other fields stay refused")

	statusOut, _, code := runCLI(t, "", "policy", "exception", "status", view.ID, "--project", "datatug-demo", "--format", "json")
	require.Equal(t, 0, code)
	var status exceptionView
	require.NoError(t, json.Unmarshal([]byte(statusOut), &status))
	assert.Equal(t, policyexceptions.StateApplied, status.State)
	require.NotNil(t, status.ExpiresAt)

	restoreVar(t, &projectReadNow)
	projectReadNow = func() time.Time { return status.ExpiresAt.Add(time.Second) }
	_, _, code = readAs(t, "", "--source", "chinook", "--from", "Employee")
	assert.Equal(t, exitCodeAccessDenied, code, "refused again after expiresAt")

	assert.Equal(t, []string{"proposed:Employee", "narrowed:Employee", "used:Employee", "expired:Employee"}, auditEvents(t))
}

// AC:reject-stops; revoke ends an applied exception.
func TestPerimeterRejectAndRevoke(t *testing.T) {
	demoForTest(t)
	asPerson(t)
	rejected := propose(t, "--source", "chinook", "--role", "agent", "--table", "Track", "--fields", "Name")
	_, _, code := runCLI(t, "", "policy", "exception", "reject", rejected.ID, "--project", "datatug-demo", "--reason", "bad\nline")
	assert.Equal(t, exitCodeUsage, code)
	stdout, stderr, code := runCLI(t, "", "policy", "exception", "reject", rejected.ID, "--project", "datatug-demo", "--reason", "not needed")
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "nothing was added")
	_, _, code = runCLI(t, "", "policy", "exception", "reject", rejected.ID, "--project", "datatug-demo")
	assert.Equal(t, exitCodeUsage, code, "decided once")
	_, _, code = runCLI(t, "", "policy", "exception", "revoke", rejected.ID, "--project", "datatug-demo")
	assert.Equal(t, exitCodeUsage, code, "only an applied one is revoked")
	_, _, code = readAs(t, "", "--source", "chinook", "--from", "Track")
	assert.Equal(t, exitCodeAccessDenied, code)

	applied := propose(t, "--source", "chinook", "--role", "agent", "--table", "Genre", "--fields", "GenreId,Name")
	_, stderr, code = runCLI(t, "Genre\n", "policy", "exception", "apply", applied.ID, "--project", "datatug-demo")
	require.Equal(t, 0, code, stderr)
	rows, _, code := readAs(t, "", "--source", "chinook", "--from", "Genre", "--limit", "2")
	require.Equal(t, 0, code)
	assert.Len(t, rows, 2)
	stdout, stderr, code = runCLI(t, "", "policy", "exception", "revoke", applied.ID, "--project", "datatug-demo")
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "Revoked")
	_, _, code = readAs(t, "", "--source", "chinook", "--from", "Genre")
	assert.Equal(t, exitCodeAccessDenied, code)

	stdout, _, code = runCLI(t, "", "policy", "exception", "list", "--project", "datatug-demo", "--format", "json", "--state", "revoked")
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, applied.ID)
	assert.NotContains(t, stdout, rejected.ID)
	stdout, _, code = runCLI(t, "", "policy", "exception", "list", "--project", "datatug-demo")
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, rejected.ID+" rejected")
	assert.Contains(t, stdout, "applied fields: GenreId, Name")
	stdout, _, code = runCLI(t, "", "policy", "exception", "audit", "--project", "datatug-demo")
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, rejected.ID+" rejected: role agent on chinook.Track")
	assert.Contains(t, stdout, applied.ID+" revoked")
}

func TestPerimeterProposeRefusals(t *testing.T) {
	demoForTest(t)
	base := []string{"policy", "exception", "propose", "--project", "datatug-demo", "--source", "chinook", "--role", "agent", "--reason", "x"}
	cases := map[string]struct {
		args []string
		want string
	}{
		"glob table":       {[]string{"--table", "Emp*", "--fields", "EmployeeId"}, "one literal name"},
		"glob field":       {[]string{"--table", "Employee", "--fields", "*"}, "one literal name"},
		"no fields":        {[]string{"--table", "Employee"}, "--fields is required"},
		"long duration":    {[]string{"--table", "Employee", "--fields", "Title", "--for", "9h"}, "at most 8h"},
		"unknown field":    {[]string{"--table", "Employee", "--fields", "Salary"}, `no column "Salary"`},
		"unknown table":    {[]string{"--table", "Payroll", "--fields", "Salary"}, `no table "Payroll"`},
		"existing rule":    {[]string{"--table", "Customer", "--fields", "Phone"}, "already has a rule on Customer"},
		"unknown policy":   {[]string{"--table", "Employee", "--fields", "Title", "--policy", "other"}, `named "other"`},
		"unknown source":   {[]string{"--table", "Employee", "--fields", "Title", "--source", "nope"}, "nope"},
		"unknown env":      {[]string{"--table", "Employee", "--fields", "Title", "--env", "prod"}, "prod"},
		"bad format":       {[]string{"--table", "Employee", "--fields", "Title", "--format", "xml"}, "unsupported --format"},
		"reason too short": {[]string{"--table", "Employee", "--fields", "Title", "--reason", ""}, "--reason is required"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, stderr, code := runCLI(t, "", append(append([]string{}, base...), c.args...)...)
			assert.NotEqual(t, 0, code)
			assert.Contains(t, stderr, c.want)
		})
	}
	_, stderr, code := runCLI(t, "", "policy", "exception", "status", "0000000000000000", "--project", "datatug-demo")
	assert.Equal(t, exitCodeNotFound, code, stderr)
	_, _, code = runCLI(t, "", "policy", "exception", "status", "0000000000000000", "--project", "datatug-demo", "--wait", "1h")
	assert.Equal(t, exitCodeUsage, code)
	_, _, code = runCLI(t, "", "policy", "exception", "list")
	assert.Equal(t, exitCodeUsage, code, "--project is required")
	_, _, code = runCLI(t, "", "policy", "exception", "audit", "--project", "no-such-project")
	assert.Equal(t, exitCodeUsage, code)
}

func TestPerimeterStatusWaits(t *testing.T) {
	demoForTest(t)
	view := propose(t, "--source", "chinook", "--role", "agent", "--table", "Artist", "--fields", "Name")
	restoreVar(t, &exceptionSleep)
	polls := 0
	exceptionSleep = func(time.Duration) {
		polls++
		if polls == 2 {
			asPerson(t)
			_, stderr, code := runCLI(t, "Artist\n", "policy", "exception", "apply", view.ID, "--project", "datatug-demo")
			require.Equal(t, 0, code, stderr)
		}
	}
	stdout, stderr, code := runCLI(t, "", "policy", "exception", "status", view.ID, "--project", "datatug-demo", "--wait", "10m")
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, 2, polls)
	assert.Contains(t, stdout, view.ID+" applied")
	assert.Contains(t, stdout, "expires at:")

	other := propose(t, "--source", "chinook", "--role", "agent", "--table", "Album", "--fields", "Title")
	restoreVar(t, &exceptionNow)
	start := time.Now()
	ticks := 0
	exceptionNow = func() time.Time { ticks++; return start.Add(time.Duration(ticks) * time.Minute) }
	exceptionSleep = func(time.Duration) {}
	stdout, _, code = runCLI(t, "", "policy", "exception", "status", other.ID, "--project", "datatug-demo", "--wait", "3m")
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, "pending")
	assert.Contains(t, stdout, "waits for a decision until")
}

// A policy edited after an exception was applied makes it contribute
// nothing, and an exception cannot be applied once the policy changed.
func TestPerimeterPolicyFingerprintBinding(t *testing.T) {
	dir := demoForTest(t)
	asPerson(t)
	first := propose(t, "--source", "chinook", "--role", "agent", "--table", "Employee", "--fields", "Title")
	second := propose(t, "--source", "chinook", "--role", "agent", "--table", "Track", "--fields", "Name")
	_, stderr, code := runCLI(t, "Employee\n", "policy", "exception", "apply", first.ID, "--project", "datatug-demo")
	require.Equal(t, 0, code, stderr)
	policy := filepath.Join(dir, filepath.FromSlash(demofixtures.PolicyPath))
	data, _ := os.ReadFile(policy)
	require.NoError(t, os.WriteFile(policy, append(data, []byte("# edited\n")...), 0o644))
	_, _, code = readAs(t, "", "--source", "chinook", "--from", "Employee")
	assert.Equal(t, exitCodeAccessDenied, code)
	_, stderr, code = runCLI(t, "Track\n", "policy", "exception", "apply", second.ID, "--project", "datatug-demo")
	assert.Equal(t, exitCodeUsage, code)
	assert.Contains(t, stderr, "the policy changed")
}

// An exception store that cannot be read applies no exception, and a use
// that cannot be audited is refused.
func TestPerimeterFailClosed(t *testing.T) {
	dir := demoForTest(t)
	asPerson(t)
	view := propose(t, "--source", "chinook", "--role", "agent", "--table", "Employee", "--fields", "Title")
	_, stderr, code := runCLI(t, "Employee\n", "policy", "exception", "apply", view.ID, "--project", "datatug-demo")
	require.Equal(t, 0, code, stderr)
	storeDir := filepath.Join(dir, ".datatug", "policy-exceptions")

	require.NoError(t, os.Chmod(filepath.Join(storeDir, policyexceptions.AuditFile), 0o400))
	_, stderr, code = readAs(t, "", "--source", "chinook", "--from", "Employee")
	assert.Equal(t, exitCodeAccessDenied, code)
	assert.Contains(t, stderr, "cannot be written to the audit log")
	require.NoError(t, os.Chmod(filepath.Join(storeDir, policyexceptions.AuditFile), 0o600))

	// An unreadable file contributes nothing; the readable exceptions still apply.
	require.NoError(t, os.WriteFile(filepath.Join(storeDir, "ffffffffffffffff.json"), []byte("{"), 0o600))
	_, stderr, code = readAs(t, "", "--source", "chinook", "--from", "Employee")
	assert.Equal(t, 0, code, stderr)
	assert.Contains(t, stderr, "warning: local policy exception ffffffffffffffff.json is unreadable and is not applied")

	// A tampered exception file (a field the store does not know, as an
	// older or hand-edited one has) grants nothing.
	tampered := propose(t, "--source", "chinook", "--role", "agent", "--table", "Track", "--fields", "TrackId,Name")
	_, stderr, code = runCLI(t, "Track\n", "policy", "exception", "apply", tampered.ID, "--project", "datatug-demo")
	require.Equal(t, 0, code, stderr)
	path := filepath.Join(storeDir, tampered.ID+".json")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(strings.Replace(string(data), "{", `{"scopeHash":"x",`, 1)), 0o600))
	_, stderr, code = readAs(t, "", "--source", "chinook", "--from", "Track")
	assert.Equal(t, exitCodeAccessDenied, code)
	assert.Contains(t, stderr, tampered.ID+".json is unreadable")

	// The store folder cannot be listed: no exception applies.
	require.NoError(t, os.Chmod(storeDir, 0o300))
	t.Cleanup(func() { _ = os.Chmod(storeDir, 0o700) })
	_, stderr, code = readAs(t, "", "--source", "chinook", "--from", "Employee")
	assert.Equal(t, exitCodeAccessDenied, code)
	assert.Contains(t, stderr, "warning: local policy exceptions are not applied")
}

func TestPerimeterMaskingRefusals(t *testing.T) {
	dir := demoForTest(t)
	write := func(doc string) string {
		path := filepath.Join(t.TempDir(), "q.yaml")
		require.NoError(t, os.WriteFile(path, []byte(doc), 0o600))
		return path
	}
	filter := write("from: {name: Customer}\nwhere: {op: '==', left: {field: Email}, right: {value: x}}\n")
	_, stderr, code := readAs(t, "", "--source", "chinook", "-f", filter)
	assert.Equal(t, exitCodeAccessDenied, code)
	assert.Equal(t, 1, strings.Count(stderr, "\n"), stderr)
	assert.Contains(t, stderr, `"code":"FIELD_PROTECTED"`)
	assert.Contains(t, stderr, "Customer.Email is protected (pii)")
	_, stderr, code = runCLI(t, "", "query", "run", "--project", "datatug-demo", "--source", "chinook", "-f", filter, "--as", "agent", "--role", "agent")
	assert.Equal(t, exitCodeAccessDenied, code)
	assert.Contains(t, stderr, "access denied: Customer.Email is protected")

	join := write("from:\n  name: Customer\n  alias: c\n  joins:\n    - from: {name: Invoice, alias: i}\n      on: [{left: {field: CustomerId, source: c}, op: '==', right: {field: CustomerId, source: i}}]\ncolumns: [{field: City, source: c}]\n")
	_, stderr, code = readAs(t, "", "--source", "chinook", "-f", join)
	assert.Equal(t, exitCodeAccessDenied, code, stderr)

	require.NoError(t, os.WriteFile(filepath.Join(dir, demofixtures.LabelsPath), []byte("{"), 0o644))
	_, stderr, code = readAs(t, "", "--source", "chinook", "--from", "Customer")
	assert.Equal(t, exitCodeAccessDenied, code)
	assert.Contains(t, stderr, "cannot be read")
}

func TestPerimeterProjectReadFlags(t *testing.T) {
	dir := demoForTest(t)
	cases := map[string][]string{
		"no project":  {"--source", "chinook", "--from", "Customer"},
		"with db":     {"--project", "datatug-demo", "--source", "chinook", "--db", "sqlite:///x.db", "--from", "Customer"},
		"with policy": {"--project", "datatug-demo", "--source", "chinook", "--no-policies", "--from", "Customer"},
		"with sql":    {"--project", "datatug-demo", "--source", "chinook", "--sql", "select 1"},
		"no input":    {"--project", "datatug-demo", "--source", "chinook"},
		"two inputs":  {"--project", "datatug-demo", "--source", "chinook", "--from", "Customer", "-f", "q.yaml"},
		"bad project": {"--project", "nope", "--source", "chinook", "--from", "Customer"},
		"bad env":     {"--project", "datatug-demo", "--env", "prod", "--source", "chinook", "--from", "Customer"},
		"bad source":  {"--project", "datatug-demo", "--source", "nope", "--from", "Customer"},
		"with query":  {"--project", "datatug-demo", "--source", "chinook", "--query", "x"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			_, stderr, code := runCLI(t, "", append([]string{"query", "run"}, args...)...)
			assert.Equal(t, exitCodeUsage, code, stderr)
		})
	}
	require.NoError(t, os.RemoveAll(filepath.Join(dir, "policies")))
	_, stderr, code := readAs(t, "", "--source", "chinook", "--from", "Customer")
	assert.Equal(t, exitCodeAccessDenied, code)
	assert.Contains(t, stderr, "denied by default")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "policies"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "policies", "bad.yaml"), []byte("kind: nope\n"), 0o644))
	_, _, code = readAs(t, "", "--source", "chinook", "--from", "Customer")
	assert.Equal(t, exitCodeUsage, code)
}

// TestPerimeterProjectReadKeepsAResultCardMasked covers the Claude Code
// plugin's result card: it adds --result-id to every read, so a project read
// must accept it, and the stored rows are the masked ones.
func TestPerimeterProjectReadKeepsAResultCardMasked(t *testing.T) {
	home := filepath.Dir(filepath.Dir(demoForTest(t)))
	stdout, stderr, code := runCLI(t, "", "query", "run", "--project", "datatug-demo", "--source", "chinook", "--from", "Customer",
		"--as", "agent", "--role", "agent", "--limit", "3", "--format", "json", "--result-id", "onboarding-card")
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, `"resultFiles"`)
	stored := ""
	require.NoError(t, filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.Contains(path, "onboarding-card") {
			data, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			stored += string(data)
		}
		return nil
	}))
	require.NotEmpty(t, stored, "the result card's files are written")
	assert.Contains(t, stored, "••••")
	assert.NotContains(t, stored, "@", "no email address reaches the stored result")
}
