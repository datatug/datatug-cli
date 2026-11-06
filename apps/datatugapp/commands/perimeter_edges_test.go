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
	"github.com/datatug/datatug-cli/pkg/demofixtures"
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
			protected := r.prepareMasking(context.Background(), query)
			require.NotNil(t, protected)
			assert.NotEmpty(t, protected.Error())
		})
	}
}

func TestBaseCollectionRef(t *testing.T) {
	ref := dal.NewRootCollectionRef("Customer", "")
	got, ok := baseCollectionRef(dal.NewQueryBuilder(dal.From(&ref)).SelectColumns().(dal.StructuredQuery))
	assert.True(t, ok)
	assert.Equal(t, "Customer", got.Name())
	_, ok = baseCollectionRef(dal.NewQueryBuilder(dal.From(dal.NewCollectionGroupRef("notes", ""))).SelectColumns().(dal.StructuredQuery))
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
	_, _, code = runCLI(t, "", "policy", "exception", "list", "--project", "datatug-demo")
	assert.Equal(t, 1, code)
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
