package commands

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/callercontext"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/demofixtures"
	"github.com/datatug/datatug-cli/pkg/policyexceptions"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/datatug/datatug-core/pkg/meaninglabels"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type opaqueExpression struct{}

func (opaqueExpression) String() string { return "opaque()" }

func TestPrepareMaskingRefusesShapesItCannotMask(t *testing.T) {
	restoreVar(t, &loadLabels)
	loadLabels = func(context.Context, string) (meaninglabels.Document, error) { return meaninglabels.NewDocument(), nil }
	r := &projectRead{source: "chinook"}
	base := dal.From(dal.NewRootCollectionRef("Customer", ""))
	join := dal.From(dal.NewRootCollectionRef("Customer", "c")).Join(dal.NewJoinedSource(dal.NewRootCollectionRef("Invoice", "i"), dal.JoinInner, dal.WhereField("CustomerId", dal.Equal, 1)))
	cases := map[string]dal.Query{
		"opaque":     dal.NewTextQuery("select 1", nil),
		"join":       dal.NewQueryBuilder(join).SelectColumns(),
		"expression": dal.NewQueryBuilder(base).SelectColumns(dal.Column{Expression: opaqueExpression{}, Alias: "x"}),
	}
	for name, query := range cases {
		t.Run(name, func(t *testing.T) {
			protected := r.prepareMasking(context.Background(), nil, query)
			require.NotNil(t, protected)
			assert.NotEmpty(t, protected.Error())
		})
	}
}

func TestBaseCollectionRef(t *testing.T) {
	ref := dal.NewRootCollectionRef("Customer", "")
	got, ok := baseCollectionRef(dal.NewQueryBuilder(dal.From(&ref)).SelectColumns())
	assert.True(t, ok)
	assert.Equal(t, "Customer", got.Name())
	_, ok = baseCollectionRef(dal.NewQueryBuilder(dal.From(dal.NewCollectionGroupRef("notes", ""))).SelectColumns())
	assert.False(t, ok)
}

type fakeReader struct {
	records []record.Record
	err     error
}

func (f *fakeReader) Cursor() (string, error) { return "", nil }
func (f *fakeReader) Close() error            { return nil }
func (f *fakeReader) Next() (record.Record, error) {
	if f.err != nil {
		return nil, f.err
	}
	if len(f.records) == 0 {
		return nil, dal.ErrNoMoreRecords
	}
	next := f.records[0]
	f.records = f.records[1:]
	return next, nil
}

func TestMaskingReader(t *testing.T) {
	r := &projectRead{source: "chinook", table: "Customer", labels: meaninglabels.NewDocument(), masked: map[string]meaninglabels.ColumnLabel{}}
	assert.Nil(t, r.mask(nil))
	reader := r.mask(&fakeReader{records: []record.Record{
		record.NewRecordWithoutKey(map[string]any{"Email": "a@b", "City": "Halifax"}),
		record.NewRecordWithData(record.NewKeyWithID("Customer", 1), 42),
	}})
	rec, err := reader.Next()
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"Email": meaninglabels.MaskedValue, "City": "Halifax"}, rec.Data())
	assert.Equal(t, meaninglabels.MaskedValue, rec.Key().ID, "a record without a key column gets a withheld key")
	_, err = reader.Next()
	assert.ErrorContains(t, err, "masking")
	_, err = r.mask(&fakeReader{err: errors.New("read failed")}).Next()
	assert.ErrorContains(t, err, "read failed")
	assert.Contains(t, r.maskedNote(), "Customer.Email (pii)")
	assert.Empty(t, (&projectRead{}).maskedNote())
}

func TestShellWord(t *testing.T) {
	assert.Equal(t, "datatug-demo", shellWord("datatug-demo"))
	assert.Equal(t, "'<role>'", shellWord("<role>"))
	assert.Equal(t, `'it'\''s'`, shellWord("it's"))
	assert.Equal(t, "''", shellWord(""))
}

func TestReportedError(t *testing.T) {
	err := Reported("denied", 5)
	assert.True(t, IsReported(err))
	assert.False(t, IsReported(Exit("x", 5)))
	var coder ExitCoder
	require.ErrorAs(t, err, &coder)
	assert.Equal(t, 5, coder.ExitCode())
	assert.Equal(t, "denied", err.Error())
}

func TestResolveProjectReadUnknownProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := resolveProjectRead(context.Background(), queryOptions{project: "nope", source: "chinook"})
	assert.Error(t, err)
}

func TestSourceColumnsFailures(t *testing.T) {
	dir := demoForTest(t)
	url := "sqlite://" + filepath.Join(dir, filepath.FromSlash(demofixtures.ChinookPath))
	ctx := context.Background()
	_, _, err := sourceColumns(ctx, "bogus://x", "Customer")
	assert.Error(t, err)
	_, _, err = sourceColumns(ctx, "sqlite://"+filepath.Join(t.TempDir(), "missing", "x.db"), "Customer")
	assert.Error(t, err)

	restoreVar(t, &listCollections)
	restoreVar(t, &describeCollection)
	listCollections = func(context.Context, dal.DB, *record.Key) ([]dal.CollectionRef, error) {
		return nil, errors.New("no list")
	}
	_, _, err = sourceColumns(ctx, url, "Customer")
	assert.ErrorContains(t, err, "cannot be listed")
	listCollections = func(context.Context, dal.DB, *record.Key) ([]dal.CollectionRef, error) {
		return []dal.CollectionRef{dal.NewRootCollectionRef("Customer", ""), dal.NewRootCollectionRef("customer", "")}, nil
	}
	_, _, err = sourceColumns(ctx, url, "Customer")
	assert.ErrorContains(t, err, "ambiguous")
	listCollections = func(context.Context, dal.DB, *record.Key) ([]dal.CollectionRef, error) {
		return []dal.CollectionRef{dal.NewRootCollectionRef("Customer", "")}, nil
	}
	describeCollection = func(context.Context, dal.DB, *dal.CollectionRef) (*dbschema.CollectionDef, error) {
		return nil, errors.New("no describe")
	}
	_, _, err = sourceColumns(ctx, url, "Customer")
	assert.ErrorContains(t, err, "cannot be read")
}

func TestPolicyExceptionCommandEdges(t *testing.T) {
	dir := demoForTest(t)
	proposeArgs := []string{"policy", "exception", "propose", "--project", "datatug-demo", "--source", "chinook", "--role", "agent", "--table", "Employee", "--fields", "Title", "--reason", "why"}

	stdout, stderr, code := runCLI(t, "", proposeArgs...)
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "apply: datatug policy exception apply ")
	assert.Contains(t, stdout, "waits for a decision until")

	for _, verb := range []string{"status", "list", "audit"} {
		args := []string{"policy", "exception", verb, "--project", "datatug-demo", "--format", "xml"}
		if verb == "status" {
			args = append(args, "0000000000000000")
		}
		_, _, code = runCLI(t, "", args...)
		assert.Equal(t, exitCodeUsage, code, verb)
	}
	asPerson(t)
	_, _, code = runCLI(t, "", "policy", "exception", "apply", "0000000000000000", "--project", "datatug-demo", "--format", "xml")
	assert.Equal(t, exitCodeUsage, code)
	_, _, code = runCLI(t, "", "policy", "exception", "apply", "0000000000000000", "--project", "datatug-demo")
	assert.Equal(t, exitCodeNotFound, code)

	storeDir := filepath.Join(dir, ".datatug", "policy-exceptions")
	require.NoError(t, os.WriteFile(filepath.Join(storeDir, "ffffffffffffffff.json"), []byte("{"), 0o600))
	_, stderr, code = runCLI(t, "", "policy", "exception", "status", "ffffffffffffffff", "--project", "datatug-demo")
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "unreadable")
	_, stderr, code = runCLI(t, "", "policy", "exception", "list", "--project", "datatug-demo")
	assert.Equal(t, 0, code)
	assert.Contains(t, stderr, "ffffffffffffffff.json is unreadable")
	require.NoError(t, os.Chmod(storeDir, 0o300))
	_, _, code = runCLI(t, "", "policy", "exception", "list", "--project", "datatug-demo")
	assert.Equal(t, 1, code)
	require.NoError(t, os.Chmod(storeDir, 0o700))
	require.NoError(t, os.Remove(filepath.Join(storeDir, "ffffffffffffffff.json")))
	audit := filepath.Join(storeDir, "audit.jsonl")
	good, _ := os.ReadFile(audit)
	require.NoError(t, os.WriteFile(audit, append(good, []byte("not json\n")...), 0o600))
	_, _, code = runCLI(t, "", "policy", "exception", "audit", "--project", "datatug-demo")
	assert.Equal(t, 1, code)
	require.NoError(t, os.WriteFile(audit, good, 0o600))

	restoreVar(t, &newExceptionID)
	newExceptionID = func() (string, error) { return "", errors.New("no entropy") }
	_, stderr, code = runCLI(t, "", proposeArgs...)
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "no entropy")
	newExceptionID = func() (string, error) { return "0000000000000001", nil }
	_, _, code = runCLI(t, "", proposeArgs...)
	require.Equal(t, 0, code)
	_, stderr, code = runCLI(t, "", proposeArgs...)
	assert.Equal(t, 1, code, "a taken id is never overwritten")
	assert.Contains(t, stderr, "taken")

	// The proposal expires while the person is typing: nothing is applied.
	restoreVar(t, &exceptionNow)
	calls := 0
	exceptionNow = func() time.Time {
		calls++
		if calls > 1 {
			return time.Now().Add(time.Hour)
		}
		return time.Now()
	}
	_, stderr, code = runCLI(t, "Employee\n", "policy", "exception", "apply", "0000000000000001", "--project", "datatug-demo")
	assert.Equal(t, exitCodeUsage, code)
	assert.Contains(t, stderr, "is expired")
	exceptionNow = time.Now
}

// changingReader rewrites the exception file while the person types, so the
// decision commits against bytes it did not show.
type changingReader struct {
	path string
	done bool
}

func (c *changingReader) Read(p []byte) (int, error) {
	if c.done {
		return 0, io.EOF
	}
	c.done = true
	data, _ := os.ReadFile(c.path)
	_ = os.WriteFile(c.path, []byte(strings.Replace(string(data), `"reason": "why"`, `"reason": "changed"`, 1)), 0o600)
	return copy(p, "Employee\n"), nil
}

func TestPolicyExceptionDecisionsFailClosed(t *testing.T) {
	dir := demoForTest(t)
	asPerson(t)
	storeDir := filepath.Join(dir, ".datatug", "policy-exceptions")
	newOne := func(table, field string) string {
		view := propose(t, "--source", "chinook", "--role", "agent", "--table", table, "--fields", field, "--reason", "why")
		return view.ID
	}

	changed := newOne("Employee", "Title")
	root := DatatugCommand()
	root.SetArgs([]string{"policy", "exception", "apply", changed, "--project", "datatug-demo"})
	root.SetIn(&changingReader{path: filepath.Join(storeDir, changed+".json")})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SilenceUsage, root.SilenceErrors = true, true
	assert.ErrorContains(t, root.Execute(), "changed while it was being decided")

	rejected := newOne("Track", "Name")
	applied := newOne("Genre", "Name")
	_, stderr, code := runCLI(t, "Genre\n", "policy", "exception", "apply", applied, "--project", "datatug-demo")
	require.Equal(t, 0, code, stderr)
	require.NoError(t, os.Chmod(storeDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(storeDir, 0o700) })
	_, _, code = runCLI(t, "", "policy", "exception", "reject", rejected, "--project", "datatug-demo")
	assert.Equal(t, 1, code)
	_, _, code = runCLI(t, "", "policy", "exception", "revoke", applied, "--project", "datatug-demo")
	assert.Equal(t, 1, code)
	_, _, code = runCLI(t, "Track\n", "policy", "exception", "apply", rejected, "--project", "datatug-demo")
	assert.Equal(t, 1, code)
	_, stderr, code = runCLI(t, "", "policy", "exception", "propose", "--project", "datatug-demo", "--source", "chinook", "--role", "agent", "--table", "Album", "--fields", "Title", "--reason", "why")
	assert.Equal(t, 1, code, stderr)
}

func TestFindProjectPolicy(t *testing.T) {
	dir := demoForTest(t)
	policies := filepath.Join(dir, "policies")
	require.NoError(t, os.MkdirAll(filepath.Join(policies, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(policies, "README.md"), []byte("notes"), 0o644))
	found, err := findProjectPolicy(dir, "")
	require.NoError(t, err)
	assert.Equal(t, "policies/agent.yaml", found.rel)
	found, err = findProjectPolicy(dir, "agent")
	require.NoError(t, err)
	assert.Equal(t, "agent", found.name)

	scopes := "apiVersion: dalgo.io/access/v1\nkind: AccessPolicy\nmetadata:\n  name: global\ndefault: deny\nscopes:\n  - path: /Track\n    rules:\n      - {id: t, effect: allow, operations: [query]}\n"
	require.NoError(t, os.WriteFile(filepath.Join(policies, "global.yaml"), []byte(scopes), 0o644))
	_, err = findProjectPolicy(dir, "")
	assert.ErrorContains(t, err, "several policies")
	global, err := findProjectPolicy(dir, "global")
	require.NoError(t, err)
	assert.ErrorContains(t, checkRoleHasNoRule(context.Background(), global, "agent", "Employee"), "binds no roles")

	require.NoError(t, os.WriteFile(filepath.Join(policies, "bad.yaml"), []byte("kind: nope\n"), 0o644))
	_, err = findProjectPolicy(dir, "")
	assert.ErrorContains(t, err, "policy bad.yaml")
	require.NoError(t, os.Chmod(filepath.Join(policies, "bad.yaml"), 0o000))
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(policies, "bad.yaml"), 0o644) })
	_, err = findProjectPolicy(dir, "")
	assert.ErrorContains(t, err, "cannot be read")

	require.NoError(t, os.RemoveAll(policies))
	_, err = findProjectPolicy(dir, "")
	assert.ErrorContains(t, err, "no policies/ folder")
}

func TestProposeNeedsAnEnvironmentWhenThereAreSeveral(t *testing.T) {
	dir := demoForTest(t)
	other := filepath.Join(t.TempDir(), "other.db")
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(demofixtures.ChinookPath)))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(other, data, 0o644))
	_, stderr, code := runCLI(t, "", "db", "add", "--project", "datatug-demo", "--db", other, "--env", "prod")
	require.Equal(t, 0, code, stderr)
	_, stderr, code = runCLI(t, "", "policy", "exception", "propose", "--project", "datatug-demo", "--source", "chinook", "--role", "agent", "--table", "Employee", "--fields", "Title", "--reason", "why")
	assert.Equal(t, exitCodeUsage, code)
	assert.Contains(t, stderr, "pass --env")
	_, stderr, code = readAs(t, "", "--source", "chinook", "--from", "Customer")
	assert.Equal(t, exitCodeUsage, code)
	assert.Contains(t, stderr, "pass --env")
}

func TestDemoInstallEdges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, args := range [][]string{{"--format", "xml"}, {"--project-id", "Bad ID"}} {
		_, _, code := runCLI(t, "", append([]string{"demo", "install"}, args...)...)
		assert.Equal(t, exitCodeUsage, code, args)
	}
	restoreVar(t, &absolutePath)
	absolutePath = func(string) (string, error) { return "", errors.New("no cwd") }
	_, _, code := runCLI(t, "", "demo", "install", "--dir", "rel")
	assert.Equal(t, exitCodeUsage, code)
	absolutePath = filepath.Abs

	restoreVar(t, &demoInstallFiles)
	demoInstallFiles = func() ([]demofixtures.File, error) { return nil, errors.New("pinned sha256 mismatch") }
	_, stderr, code := runCLI(t, "", "demo", "install")
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "pinned sha256 mismatch")
	demoInstallFiles = demofixtures.Files

	require.NoError(t, os.WriteFile(filepath.Join(home, dtconfig.ConfigFileName), []byte("projects: [:"), 0o600))
	_, _, code = runCLI(t, "", "demo", "install")
	assert.Equal(t, 1, code)
	require.NoError(t, os.Remove(filepath.Join(home, dtconfig.ConfigFileName)))

	foreign := filepath.Join(t.TempDir(), "mine")
	require.NoError(t, os.MkdirAll(foreign, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(foreign, "datatug-project.json"), []byte(`{"id":"mine"}`), 0o644))
	_, stderr, code = runCLI(t, "", "demo", "install", "--dir", foreign)
	assert.Equal(t, exitCodeUsage, code)
	assert.Contains(t, stderr, "did not make")
	require.NoError(t, os.MkdirAll(filepath.Join(foreign, ".datatug"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(foreign, filepath.FromSlash(demoInstallMarker)), []byte(`{}`), 0o644))
	_, stderr, code = runCLI(t, "", "demo", "install", "--dir", foreign)
	assert.Equal(t, exitCodeUsage, code)
	assert.Contains(t, stderr, "another ID or version")

	notADir := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(notADir, nil, 0o644))
	_, stderr, code = runCLI(t, "", "demo", "install", "--dir", notADir)
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "cannot read")

	restoreVar(t, &initStat)
	initStat = func(string) (os.FileInfo, error) { return nil, errors.New("stat failed") }
	_, stderr, code = runCLI(t, "", "demo", "install", "--dir", filepath.Join(t.TempDir(), "a"))
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "failed to create the demo project")
	initStat = os.Stat

	restoreVar(t, &demoInstallMkdirAll)
	demoInstallMkdirAll = func(string, os.FileMode) error { return errors.New("read-only") }
	_, stderr, code = runCLI(t, "", "demo", "install", "--dir", filepath.Join(t.TempDir(), "b"), "--project-id", "b")
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "read-only")
	demoInstallMkdirAll = os.MkdirAll
	restoreVar(t, &demoInstallWriteFile)
	demoInstallWriteFile = func(string, []byte, os.FileMode) error { return errors.New("disk full") }
	_, stderr, code = runCLI(t, "", "demo", "install", "--dir", filepath.Join(t.TempDir(), "c"), "--project-id", "c")
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "disk full")
}

func TestDemoInstallRepairsAndPreserves(t *testing.T) {
	dir := demoForTest(t)
	home := os.Getenv("HOME")

	policy := filepath.Join(dir, filepath.FromSlash(demofixtures.PolicyPath))
	require.NoError(t, os.WriteFile(policy, []byte("# mine\n"), 0o644))
	require.NoError(t, os.Remove(filepath.Join(dir, filepath.FromSlash(demofixtures.NoticePath))))
	stdout, stderr, code := runCLI(t, "", "demo", "install")
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "Demo project datatug-demo repaired at "+dir)
	assert.Contains(t, stdout, "preserved (you changed it): policies/agent.yaml")
	assert.Contains(t, stdout, "source support (ingitdb)")
	mine, _ := os.ReadFile(policy)
	assert.Equal(t, "# mine\n", string(mine))

	require.NoError(t, os.RemoveAll(filepath.Join(dir, "environments")))
	stdout, _, code = runCLI(t, "", "demo", "install", "--format", "json")
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, `"status":"repaired"`)

	settings, err := dtconfig.GetSettings()
	require.NoError(t, err)
	settings.Projects = nil
	require.NoError(t, dtconfig.SaveSettings(settings))
	stdout, _, code = runCLI(t, "", "demo", "install", "--format", "json")
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, `"status":"repaired"`)

	_, stderr, code = runCLI(t, "", "demo", "install", "--dir", filepath.Join(t.TempDir(), "elsewhere"))
	assert.Equal(t, exitCodeUsage, code)
	assert.Contains(t, stderr, "already registered at another folder")

	chinook := filepath.Join(dir, filepath.FromSlash(demofixtures.ChinookPath))
	catalog := filepath.Join(dir, "environments", "local", "catalogs", "chinook", "chinook.db.json")
	var doc map[string]any
	data, err := os.ReadFile(catalog)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &doc))
	other := filepath.Join(t.TempDir(), "other.sqlite")
	fixture, _ := os.ReadFile(chinook)
	require.NoError(t, os.WriteFile(other, fixture, 0o644))
	doc["path"] = other
	data, _ = json.Marshal(doc)
	require.NoError(t, os.WriteFile(catalog, data, 0o644))
	_, stderr, code = runCLI(t, "", "demo", "install")
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "failed to add source chinook")

	require.NoError(t, os.WriteFile(chinook, append(fixture, 'x'), 0o644))
	_, stderr, code = runCLI(t, "", "demo", "install")
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "does not match its pinned sha256")

	_ = home
}

func TestDemoInstallRegistrationFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	settingsFile := filepath.Join(home, dtconfig.ConfigFileName)
	require.NoError(t, os.WriteFile(settingsFile, []byte("projects: []\n"), 0o400))
	_, stderr, code := runCLI(t, "", "demo", "install")
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "failed to register")
}

func TestProjectReadRefusesClickHouse(t *testing.T) {
	demoForTest(t)
	t.Setenv("DATATUG_TEST_CLICKHOUSE_SOURCE", "clickhouse+https://warehouse.example:8443/explore")
	_, stderr, code := runCLI(t, "", "db", "add", "--project", "datatug-demo", "--db", "env:DATATUG_TEST_CLICKHOUSE_SOURCE", "--env", "local", "--name", "warehouse")
	require.Equal(t, 0, code, stderr)
	_, stderr, code = readAs(t, "", "--source", "warehouse", "--from", "events")
	assert.Equal(t, exitCodeUsage, code)
	assert.Contains(t, stderr, "ClickHouse source is not supported")
}

// The record key is shown only when it is the value of the table's single key
// column, the policy lets the row carry that column, and it is not masked.
func TestMaskingReaderKey(t *testing.T) {
	doc, _, _ := meaninglabels.Apply(meaninglabels.NewDocument(), meaninglabels.Change{Action: meaninglabels.ActionSet,
		Target: meaninglabels.Target{Source: "chinook", Table: "Secret", Column: "Code"}, Class: meaninglabels.ClassPII, Reason: "r"}, meaninglabels.Actor{ID: "o"}, time.Now())
	read := func(table, key string, row map[string]any, id any) any {
		r := &projectRead{source: "chinook", table: table, key: key, labels: doc, masked: map[string]meaninglabels.ColumnLabel{}}
		rec, err := r.mask(&fakeReader{records: []record.Record{record.NewRecordWithData(record.NewKeyWithID(table, id), row)}}).Next()
		require.NoError(t, err)
		return rec.Key().ID
	}
	assert.Equal(t, 7, read("Employee", "EmployeeId", map[string]any{"EmployeeId": 7, "Title": "x"}, 7))
	assert.Equal(t, meaninglabels.MaskedValue, read("Employee", "EmployeeId", map[string]any{"Title": "x"}, 7), "key column not allowed")
	assert.Equal(t, meaninglabels.MaskedValue, read("Employee", "", map[string]any{"EmployeeId": 7}, 7), "no key column")
	assert.Equal(t, meaninglabels.MaskedValue, read("Employee", "EmployeeId", map[string]any{"EmployeeId": 8}, 7), "the key is another value")
	assert.Equal(t, meaninglabels.MaskedValue, read("Secret", "Code", map[string]any{"Code": "abc"}, "abc"), "a labelled key")
}

func TestDescribeObjectKind(t *testing.T) {
	restoreVar(t, &listCollections)
	restoreVar(t, &describeCollection)
	restoreVar(t, &listSourceViews)
	tables := func(context.Context, dal.DB, *record.Key) ([]dal.CollectionRef, error) {
		return []dal.CollectionRef{dal.NewRootCollectionRef("Customer", ""), dal.NewRootCollectionRef("Recent", "")}, nil
	}
	listCollections = tables
	listSourceViews = func(context.Context, dal.DB) ([]dbschema.SourceViewDef, error) {
		return []dbschema.SourceViewDef{{Name: "Recent"}}, nil
	}
	describeCollection = func(context.Context, dal.DB, *dal.CollectionRef) (*dbschema.CollectionDef, error) {
		return &dbschema.CollectionDef{PrimaryKey: []dal.FieldName{"CustomerId"}}, nil
	}
	kind := func(scheme, table string) (bool, string) {
		r := &projectRead{table: table, backend: dbcopy.BackendRef{Scheme: scheme}}
		r.describe(context.Background(), nil)
		return r.view, r.key
	}
	view, key := kind("sqlite", "Customer")
	assert.False(t, view)
	assert.Equal(t, "CustomerId", key)
	view, _ = kind("sqlite", "Recent")
	assert.True(t, view, "a view")
	view, _ = kind("sqlite", "Missing")
	assert.True(t, view, "an object the source does not list")
	view, _ = kind("ingitdb", "support-notes")
	assert.True(t, view, "a document collection")
	listSourceViews = func(context.Context, dal.DB) ([]dbschema.SourceViewDef, error) { return nil, errors.New("no views") }
	view, _ = kind("sqlite", "Customer")
	assert.True(t, view, "kind unknown")
	listCollections = func(context.Context, dal.DB, *record.Key) ([]dal.CollectionRef, error) {
		return nil, errors.New("no list")
	}
	view, _ = kind("sqlite", "Customer")
	assert.True(t, view)
	listCollections = tables
	listSourceViews = func(context.Context, dal.DB) ([]dbschema.SourceViewDef, error) { return nil, nil }
	describeCollection = func(context.Context, dal.DB, *dal.CollectionRef) (*dbschema.CollectionDef, error) {
		return &dbschema.CollectionDef{PrimaryKey: []dal.FieldName{"A", "B"}}, nil
	}
	view, key = kind("sqlite", "Customer")
	assert.False(t, view)
	assert.Empty(t, key, "a composite key is not one column")
}

func TestListSourceViewsWithoutTheCapability(t *testing.T) {
	views, err := listSourceViews(context.Background(), nil)
	assert.NoError(t, err)
	assert.Nil(t, views)
}

// H1: an exception is bound to the source the project resolved when it was
// proposed: the same source id pointed at another database gets nothing.
func TestExceptionBoundToSourceIdentity(t *testing.T) {
	dir := demoForTest(t)
	asPerson(t)
	view := propose(t, "--source", "chinook", "--role", "agent", "--table", "Employee", "--fields", "EmployeeId,Title")
	assert.Equal(t, "local", view.Env)
	assert.Len(t, view.SourceIdentity, 64)
	stdout, stderr, code := runCLI(t, "Employee\n", "policy", "exception", "apply", view.ID, "--project", "datatug-demo")
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "Env:      local")
	assert.Contains(t, stdout, view.SourceIdentity)
	_, _, code = readAs(t, "", "--source", "chinook", "--from", "Employee")
	require.Equal(t, 0, code)

	catalog := filepath.Join(dir, "environments", "local", "catalogs", "chinook", "chinook.db.json")
	data, err := os.ReadFile(catalog)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	other := filepath.Join(dir, "data", "other.sqlite")
	fixture, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(demofixtures.ChinookPath)))
	require.NoError(t, os.WriteFile(other, fixture, 0o644))
	doc["path"] = other
	data, _ = json.Marshal(doc)
	require.NoError(t, os.WriteFile(catalog, data, 0o644))
	_, _, code = readAs(t, "", "--source", "chinook", "--from", "Employee")
	assert.Equal(t, exitCodeAccessDenied, code)
}

// M1: the expiry the screen shows is the one recorded; a confirmation after
// it applies nothing.
func TestApplyCommitsTheExpiryItShows(t *testing.T) {
	demoForTest(t)
	asPerson(t)
	view := propose(t, "--source", "chinook", "--role", "agent", "--table", "Employee", "--fields", "Title")
	stdout, stderr, code := runCLI(t, "Employee\n", "policy", "exception", "apply", view.ID, "--project", "datatug-demo")
	require.Equal(t, 0, code, stderr)
	statusOut, _, _ := runCLI(t, "", "policy", "exception", "status", view.ID, "--project", "datatug-demo", "--format", "json")
	var status exceptionView
	require.NoError(t, json.Unmarshal([]byte(statusOut), &status))
	assert.Contains(t, stdout, "until "+status.ExpiresAt.UTC().Format(time.RFC3339))

	late := propose(t, "--source", "chinook", "--role", "agent", "--table", "Track", "--fields", "Name")
	restoreVar(t, &exceptionNow)
	calls := 0
	exceptionNow = func() time.Time {
		calls++
		if calls > 1 {
			return time.Now().Add(20 * time.Minute)
		}
		return time.Now()
	}
	_, stderr, code = runCLI(t, "Track\n", "policy", "exception", "apply", late.ID, "--project", "datatug-demo")
	assert.Equal(t, exitCodeUsage, code)
	assert.Contains(t, stderr, "the time shown on the screen passed")
}

// M2: an applied field list without the key column withholds the key.
func TestProjectReadWithholdsAnUnapprovedKey(t *testing.T) {
	demoForTest(t)
	asPerson(t)
	view := propose(t, "--source", "chinook", "--role", "agent", "--table", "Employee", "--fields", "FirstName,LastName,Title")
	_, stderr, code := runCLI(t, "Employee\n", "policy", "exception", "apply", view.ID, "--project", "datatug-demo")
	require.Equal(t, 0, code, stderr)
	rows, stderr, code := readAs(t, "", "--source", "chinook", "--from", "Employee")
	require.Equal(t, 0, code, stderr)
	require.NotEmpty(t, rows)
	for _, row := range rows {
		assert.Equal(t, meaninglabels.MaskedValue, row["$key"])
		assert.NotContains(t, row, "EmployeeId")
	}
	customers, _, code := readAs(t, "", "--source", "chinook", "--from", "Customer", "--limit", "1")
	require.Equal(t, 0, code)
	assert.Equal(t, "3", customers[0]["$key"], "an allowed key column keeps its key")
}

// M3: the project is the same whether it is named by its ID or its folder.
func TestExceptionAppliesByProjectFolder(t *testing.T) {
	dir := demoForTest(t)
	asPerson(t)
	t.Chdir(filepath.Dir(dir))
	stdout, stderr, code := runCLI(t, "", "policy", "exception", "propose", "--project", "./datatug-demo", "--source", "chinook", "--role", "agent",
		"--table", "Employee", "--fields", "EmployeeId,Title", "--reason", "why", "--format", "json")
	require.Equal(t, 0, code, stderr)
	var view exceptionView
	require.NoError(t, json.Unmarshal([]byte(stdout), &view))
	assert.Equal(t, "datatug-demo", view.ProjectID)
	_, stderr, code = runCLI(t, "Employee\n", "policy", "exception", "apply", view.ID, "--project", "datatug-demo")
	require.Equal(t, 0, code, stderr)
	for _, project := range []string{"datatug-demo", "./datatug-demo", dir} {
		_, stderr, code = runCLI(t, "", "query", "run", "--project", project, "--source", "chinook", "--from", "Employee", "--as", "agent", "--role", "agent", "--format", "json")
		assert.Equal(t, 0, code, project+": "+stderr)
	}
	restoreVar(t, &absolutePath)
	absolutePath = func(string) (string, error) { return "", errors.New("no cwd") }
	_, _, code = runCLI(t, "", "policy", "exception", "list", "--project", "./datatug-demo")
	assert.Equal(t, exitCodeUsage, code)
}

func TestProjectSummaryID(t *testing.T) {
	dir := t.TempDir()
	assert.Equal(t, "fallback", projectSummaryID(dir, "fallback"))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "datatug-project.json"), []byte(`{"id":"shop"}`), 0o644))
	assert.Equal(t, "shop", projectSummaryID(dir, "fallback"))
}

// M4: a demo install that failed after its marker can be run again.
func TestDemoInstallRecoversAfterAFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), "demo")
	restoreVar(t, &initStat)
	initStat = func(string) (os.FileInfo, error) { return nil, errors.New("stat failed") }
	_, _, code := runCLI(t, "", "demo", "install", "--dir", dir)
	require.Equal(t, 1, code)
	initStat = os.Stat
	stdout, stderr, code := runCLI(t, "", "demo", "install", "--dir", dir, "--format", "json")
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, `"status":"installed"`)
}

func TestDemoInstallMarkerWriteFailures(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	restoreVar(t, &demoInstallWriteFile)
	demoInstallWriteFile = func(string, []byte, os.FileMode) error { return errors.New("disk full") }
	_, stderr, code := runCLI(t, "", "demo", "install", "--dir", filepath.Join(t.TempDir(), "d"))
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, demoInstallMarker)
}

// L1: an empty --fields does not mean every proposed field.
func TestApplyRefusesAnEmptyFieldList(t *testing.T) {
	demoForTest(t)
	asPerson(t)
	view := propose(t, "--source", "chinook", "--role", "agent", "--table", "Employee", "--fields", "Title")
	_, stderr, code := runCLI(t, "Employee\n", "policy", "exception", "apply", view.ID, "--project", "datatug-demo", "--fields", "")
	assert.Equal(t, exitCodeUsage, code)
	assert.Contains(t, stderr, "--fields is empty")
}

func TestDemoInstallRepairWriteFailures(t *testing.T) {
	dir := demoForTest(t)
	notice := filepath.Join(dir, filepath.FromSlash(demofixtures.NoticePath))
	require.NoError(t, os.Remove(notice))
	restoreVar(t, &demoInstallMkdirAll)
	demoInstallMkdirAll = func(string, os.FileMode) error { return errors.New("read-only") }
	_, stderr, code := runCLI(t, "", "demo", "install")
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "read-only")
	demoInstallMkdirAll = os.MkdirAll
	restoreVar(t, &demoInstallWriteFile)
	demoInstallWriteFile = func(string, []byte, os.FileMode) error { return errors.New("disk full") }
	_, stderr, code = runCLI(t, "", "demo", "install")
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "disk full")
}

func withCaller(t *testing.T, values map[string]string) {
	t.Helper()
	restoreVar(t, &callerLookup)
	callerLookup = func(name string) (string, bool) { v, ok := values[name]; return v, ok }
}

// Proposals and audit events name the harness they came from, and the apply
// and reject screens show it as self-reported.
func TestPerimeterCallerAttribution(t *testing.T) {
	demoForTest(t)
	asPerson(t)
	withCaller(t, map[string]string{"CLAUDECODE": "1", "CLAUDE_CODE_SESSION_ID": "0f88afc6-1234-4abc-9def-0123456789ab", "AI_AGENT": "claude-code_2-1-317_agent"})
	view := propose(t, "--source", "chinook", "--role", "agent", "--table", "Employee", "--fields", "EmployeeId,Title")
	require.NotNil(t, view.Caller)
	assert.Equal(t, callercontext.Caller{Harness: "claude-code", HarnessVersion: "2.1.317", Session: "0f88afc6-1234-4abc-9def-0123456789ab", Source: callercontext.SourceHarnessEnv}, *view.Caller)
	for _, verb := range []string{"status", "list"} {
		args := []string{"policy", "exception", verb, "--project", "datatug-demo", "--format", "json"}
		if verb == "status" {
			args = append(args, view.ID)
		}
		stdout, _, code := runCLI(t, "", args...)
		require.Equal(t, 0, code)
		assert.Contains(t, stdout, `"caller":{"harness":"claude-code","harnessVersion":"2.1.317"`, verb)
	}
	withCaller(t, nil) // the person's own terminal reports no harness
	stdout, stderr, code := runCLI(t, "Employee\n", "policy", "exception", "apply", view.ID, "--project", "datatug-demo")
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "Proposed by: claude-code 2.1.317 · session 0f88afc6… (self-reported)")
	withCaller(t, map[string]string{"CLAUDECODE": "1", "CLAUDE_CODE_SESSION_ID": "0f88afc6-1234-4abc-9def-0123456789ab"})
	_, _, code = readAs(t, "", "--source", "chinook", "--from", "Employee")
	require.Equal(t, 0, code)

	withCaller(t, map[string]string{"CLAUDECODE": "1", "CLAUDE_CODE_SESSION_ID": "bad session;", "AI_AGENT": "claude-code_x;_agent"})
	rejected := propose(t, "--source", "chinook", "--role", "agent", "--table", "Track", "--fields", "Name")
	assert.Equal(t, &callercontext.Caller{Harness: "claude-code", Source: callercontext.SourceHarnessEnv}, rejected.Caller, "invalid values are dropped")
	withCaller(t, nil)
	stdout, _, code = runCLI(t, "", "policy", "exception", "reject", rejected.ID, "--project", "datatug-demo")
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, "Proposed by: claude-code (self-reported)")
	unknown := propose(t, "--source", "chinook", "--role", "agent", "--table", "Genre", "--fields", "GenreId")
	assert.Nil(t, unknown.Caller)
	stdout, _, _ = runCLI(t, "", "policy", "exception", "reject", unknown.ID, "--project", "datatug-demo")
	assert.Contains(t, stdout, "Proposed by: not reported")

	auditOut, _, code := runCLI(t, "", "policy", "exception", "audit", "--project", "datatug-demo", "--format", "json")
	require.Equal(t, 0, code)
	var audit struct {
		Events []policyexceptions.Event `json:"events"`
	}
	require.NoError(t, json.Unmarshal([]byte(auditOut), &audit))
	byEvent := map[string]*callercontext.Caller{}
	for _, e := range audit.Events {
		if e.ID == view.ID {
			byEvent[e.Event] = e.Caller
		}
	}
	require.NotNil(t, byEvent["proposed"])
	assert.Equal(t, "2.1.317", byEvent["proposed"].HarnessVersion)
	assert.Nil(t, byEvent["applied"], "the person's terminal reported no harness")
	require.NotNil(t, byEvent["used"], "the agent's read is attributed")
	assert.Equal(t, "claude-code", byEvent["used"].Harness)
}

const testSession = "0f88afc6-1234-4abc-9def-0123456789ab"

func inSession(t *testing.T, session string) {
	t.Helper()
	withCaller(t, map[string]string{"CLAUDECODE": "1", "CLAUDE_CODE_SESSION_ID": session, "AI_AGENT": "claude-code_2-1-317_agent"})
}

// A session exception applies only to the harness session that proposed it.
func TestSessionExceptions(t *testing.T) {
	demoForTest(t)
	asPerson(t)
	inSession(t, testSession)
	view := propose(t, "--source", "chinook", "--role", "agent", "--table", "Employee", "--fields", "EmployeeId,Title")
	assert.Equal(t, policyexceptions.LifetimeSession, view.Lifetime, "the default with a detected session")
	assert.Equal(t, &policyexceptions.SessionBinding{Harness: "claude-code", Session: testSession}, view.Session)

	withCaller(t, nil)
	stdout, stderr, code := runCLI(t, "Employee\n", "policy", "exception", "apply", view.ID, "--project", "datatug-demo")
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "Lifetime: this session only (claude-code session 0f88afc6…, self-reported), at most until ")

	inSession(t, testSession)
	_, stderr, code = readAs(t, "", "--source", "chinook", "--from", "Employee")
	assert.Equal(t, 0, code, stderr)
	inSession(t, "another-session")
	_, _, code = readAs(t, "", "--source", "chinook", "--from", "Employee")
	assert.Equal(t, exitCodeAccessDenied, code, "another session")
	withCaller(t, nil)
	_, _, code = readAs(t, "", "--source", "chinook", "--from", "Employee")
	assert.Equal(t, exitCodeAccessDenied, code, "no session")
	withCaller(t, map[string]string{"DATATUG_CALLER_HARNESS": "codex", "DATATUG_CALLER_SESSION": testSession})
	_, _, code = readAs(t, "", "--source", "chinook", "--from", "Employee")
	assert.Equal(t, exitCodeAccessDenied, code, "another harness")

	// The audit alone says which session got the permission and used it.
	auditOut, _, code := runCLI(t, "", "policy", "exception", "audit", "--project", "datatug-demo", "--format", "json", "--session", testSession)
	require.Equal(t, 0, code)
	var audit struct {
		Events []policyexceptions.Event `json:"events"`
	}
	require.NoError(t, json.Unmarshal([]byte(auditOut), &audit))
	kinds := []string{}
	for _, e := range audit.Events {
		kinds = append(kinds, e.Event)
		assert.Equal(t, policyexceptions.LifetimeSession, e.Lifetime)
		require.NotNil(t, e.BoundSession)
		assert.Equal(t, testSession, e.BoundSession.Session)
	}
	assert.Equal(t, []string{"proposed", "applied", "used"}, kinds)
	used := audit.Events[2]
	assert.Equal(t, view.ID, used.ID)
	assert.Equal(t, testSession, used.Caller.Session)
	none, _, _ := runCLI(t, "", "policy", "exception", "audit", "--project", "datatug-demo", "--format", "json", "--session", "nobody")
	assert.Contains(t, none, `"events":[]`)

	// end-session narrows without a terminal.
	withCaller(t, nil)
	restoreVar(t, &exceptionStdinIsTerminal)
	exceptionStdinIsTerminal = func(io.Reader) bool { return false }
	_, _, code = runCLI(t, "", "policy", "exception", "end-session", "--project", "datatug-demo", "--harness", "claude-code")
	assert.Equal(t, exitCodeUsage, code)
	stdout, stderr, code = runCLI(t, "", "policy", "exception", "end-session", "--project", "datatug-demo", "--harness", "claude-code", "--session", testSession, "--format", "json")
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, `{"ended":["`+view.ID+`"],"kind":"local-exception"}`+"\n", stdout)
	stdout, _, code = runCLI(t, "", "policy", "exception", "end-session", "--project", "datatug-demo", "--harness", "claude-code", "--session", testSession)
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, "Ended 0 session exception(s) of claude-code session 0f88afc6…")
	inSession(t, testSession)
	_, _, code = readAs(t, "", "--source", "chinook", "--from", "Employee")
	assert.Equal(t, exitCodeAccessDenied, code, "an ended session exception grants nothing")
	statusOut, _, _ := runCLI(t, "", "policy", "exception", "status", view.ID, "--project", "datatug-demo", "--format", "json")
	assert.Contains(t, statusOut, `"state":"ended"`)
	_, _, code = runCLI(t, "", "policy", "exception", "end-session", "--project", "nope", "--harness", "claude-code", "--session", testSession)
	assert.Equal(t, exitCodeUsage, code)
	require.NoError(t, os.Chmod(filepath.Join(os.Getenv("HOME"), "datatug", "datatug-demo", ".datatug", "policy-exceptions"), 0o300))
	t.Cleanup(func() {
		_ = os.Chmod(filepath.Join(os.Getenv("HOME"), "datatug", "datatug-demo", ".datatug", "policy-exceptions"), 0o700)
	})
	_, _, code = runCLI(t, "", "policy", "exception", "end-session", "--project", "datatug-demo", "--harness", "claude-code", "--session", testSession)
	assert.Equal(t, 1, code)
}

func TestSessionLifetimeSelection(t *testing.T) {
	demoForTest(t)
	asPerson(t)
	base := []string{"policy", "exception", "propose", "--project", "datatug-demo", "--source", "chinook", "--role", "agent", "--table", "Employee", "--fields", "Title", "--reason", "why"}
	_, stderr, code := runCLI(t, "", append(base, "--lifetime", "session")...)
	assert.Equal(t, exitCodeUsage, code)
	assert.Contains(t, stderr, "no harness session detected; use --lifetime deadline")
	_, stderr, code = runCLI(t, "", append(base, "--lifetime", "forever")...)
	assert.Equal(t, exitCodeUsage, code)
	assert.Contains(t, stderr, "--lifetime is session or deadline")
	deadline := propose(t, "--source", "chinook", "--role", "agent", "--table", "Employee", "--fields", "Title")
	assert.Equal(t, policyexceptions.LifetimeDeadline, deadline.Lifetime)
	assert.Nil(t, deadline.Session)
	inSession(t, testSession)
	explicit := propose(t, "--source", "chinook", "--role", "agent", "--table", "Track", "--fields", "Name", "--lifetime", "deadline")
	assert.Equal(t, policyexceptions.LifetimeDeadline, explicit.Lifetime)
	assert.Nil(t, explicit.Session)
	assert.NotNil(t, explicit.Caller, "the proposer is still recorded")
	session := propose(t, "--source", "chinook", "--role", "agent", "--table", "Album", "--fields", "Title", "--lifetime", "session")
	stdout, _, code := runCLI(t, "", "policy", "exception", "reject", session.ID, "--project", "datatug-demo")
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, "Lifetime: this session only (claude-code session 0f88afc6…, self-reported), at most 15m0s once applied")
	stdout, _, _ = runCLI(t, "", "policy", "exception", "list", "--project", "datatug-demo", "--format", "json")
	assert.Contains(t, stdout, `"lifetime":"deadline"`)
	assert.Contains(t, stdout, `"lifetime":"session","session":{"harness":"claude-code","session":"`+testSession+`"}`)
}

// The result card of a project read records the project source and policies.
func TestProjectReadResultCardMetadata(t *testing.T) {
	home := filepath.Dir(filepath.Dir(demoForTest(t)))
	_, stderr, code := runCLI(t, "", "query", "run", "--project", "datatug-demo", "--source", "chinook", "--from", "Customer",
		"--as", "agent", "--role", "agent", "--limit", "2", "--format", "json", "--result-id", "card-meta")
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stderr, "masked: Customer.Email (pii)")
	meta := ""
	require.NoError(t, filepath.WalkDir(home, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.Contains(path, "card-meta") && strings.HasSuffix(path, "meta.json") {
			data, _ := os.ReadFile(path)
			meta = string(data)
		}
		return nil
	}))
	require.NotEmpty(t, meta)
	assert.Contains(t, meta, `"policy":"project"`)
	assert.Contains(t, meta, `"project":"datatug-demo"`)
	assert.Contains(t, meta, `"source":"chinook"`)
}

func TestAuditSessionFilterAndExpiryWarning(t *testing.T) {
	dir := demoForTest(t)
	asPerson(t)
	inSession(t, testSession)
	view := propose(t, "--source", "chinook", "--role", "agent", "--table", "Employee", "--fields", "EmployeeId,Title")
	withCaller(t, map[string]string{"DATATUG_CALLER_HARNESS": "codex", "DATATUG_CALLER_SESSION": testSession})
	propose(t, "--source", "chinook", "--role", "agent", "--table", "Track", "--fields", "TrackId")
	withCaller(t, nil)
	count := func(args ...string) int {
		stdout, _, code := runCLI(t, "", append([]string{"policy", "exception", "audit", "--project", "datatug-demo", "--format", "json", "--session", testSession}, args...)...)
		require.Equal(t, 0, code)
		var audit struct {
			Events []policyexceptions.Event `json:"events"`
		}
		require.NoError(t, json.Unmarshal([]byte(stdout), &audit))
		return len(audit.Events)
	}
	assert.Equal(t, 2, count(), "the session id alone matches both harnesses")
	assert.Equal(t, 1, count("--harness", "claude-code"))
	assert.Equal(t, 1, count("--harness", "codex"))
	stdout, _, _ := runCLI(t, "", "policy", "exception", "audit", "--project", "datatug-demo")
	assert.Contains(t, stdout, "; for claude-code session 0f88afc6…; by claude-code 2.1.317 · session 0f88afc6… (self-reported)")

	_, stderr, code := runCLI(t, "Employee\n", "policy", "exception", "apply", view.ID, "--project", "datatug-demo")
	require.Equal(t, 0, code, stderr)
	restoreVar(t, &projectReadNow)
	projectReadNow = func() time.Time { return time.Now().Add(time.Hour) }
	audit := filepath.Join(dir, ".datatug", "policy-exceptions", policyexceptions.AuditFile)
	require.NoError(t, os.Chmod(audit, 0o400))
	t.Cleanup(func() { _ = os.Chmod(audit, 0o600) })
	_, stderr, _ = readAs(t, "", "--source", "chinook", "--from", "Customer")
	assert.Contains(t, stderr, "could not be recorded")
}
