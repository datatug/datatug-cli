package commands

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2http"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/queryresults"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resultsHome points HOME at a fresh folder and answers the result root.
func resultsHome(t *testing.T) string {
	t.Helper()
	home := tempDatatugHome(t)
	return filepath.Join(home, ".datatug", "query-results")
}

func chinookFixture(t *testing.T) string {
	t.Helper()
	fixture, err := filepath.Abs("../../../pkg/dbcopy/testdata/chinook.db")
	require.NoError(t, err)
	return fixture
}

// runResults runs `datatug query results <argv>`.
func runResults(t *testing.T, argv ...string) (stdout, stderr string, code int) {
	t.Helper()
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	root := &cobra.Command{Use: "datatug", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(queryCommand())
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs(append([]string{"query", "results"}, argv...))
	if err := root.ExecuteContext(context.Background()); err != nil {
		var coder ExitCoder
		code = 1
		if errors.As(err, &coder) {
			code = coder.ExitCode()
		}
		errOut.WriteString(err.Error() + "\n")
	}
	return out.String(), errOut.String(), code
}

type metaDoc struct {
	ID        string `json:"id"`
	DataBytes int64  `json:"dataBytes"`
	DB        struct {
		Ref     *string `json:"ref"`
		Project *string `json:"project"`
		Env     *string `json:"env"`
		Source  *string `json:"source"`
		Driver  string  `json:"driver"`
	} `json:"db"`
	Query struct {
		Kind           string                     `json:"kind"`
		DTQL           string                     `json:"dtql"`
		From           string                     `json:"from"`
		SavedQueryID   string                     `json:"savedQueryId"`
		SavedQueryType string                     `json:"savedQueryType"`
		Parameters     map[string]json.RawMessage `json:"parameters"`
		Policy         string                     `json:"policy"`
	} `json:"query"`
	Rows       queryresults.Rows        `json:"rows"`
	Provenance *queryresults.Provenance `json:"provenance"`
	Columns    []queryresults.Column    `json:"columns"`
}

func readResult(t *testing.T, root, id string) (metaDoc, apicontract.Recordset, string) {
	t.Helper()
	metaBytes, err := os.ReadFile(filepath.Join(root, id, "meta.json"))
	require.NoError(t, err)
	var meta metaDoc
	require.NoError(t, json.Unmarshal(metaBytes, &meta))
	dataBytes, err := os.ReadFile(filepath.Join(root, id, "data.json"))
	require.NoError(t, err)
	assert.Equal(t, int64(len(dataBytes)), meta.DataBytes)
	var data struct {
		Recordset apicontract.Recordset `json:"recordset"`
	}
	require.NoError(t, json.Unmarshal(dataBytes, &data))
	return meta, data.Recordset, string(metaBytes)
}

func TestQueryRunResultFilesFromChinook(t *testing.T) {
	root := resultsHome(t)
	stdout, stderr, code := runQuery(t, "", "--db", "sqlite://"+chinookFixture(t), "--from", "Invoice", "--no-policies", "--result-id", "t1")
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, `{"resultFiles":{"version":1},"rowsKept":412,"hasMore":false,"columns":10}`+"\n", stdout)
	assert.Contains(t, stderr, "access: running without access policies")

	for path, mode := range map[string]os.FileMode{root: 0o700, filepath.Join(root, "t1"): 0o700, filepath.Join(root, "t1", "data.json"): 0o600, filepath.Join(root, "t1", "meta.json"): 0o600} {
		info, err := os.Lstat(path)
		require.NoError(t, err)
		assert.Equal(t, mode, info.Mode().Perm(), path)
	}
	meta, recordset, metaText := readResult(t, root, "t1")
	assert.Len(t, recordset.Rows, 412)
	assert.Equal(t, queryresults.Rows{Kept: 412, TruncatedBy: "none"}, meta.Rows)
	assert.Equal(t, "from", meta.Query.Kind)
	assert.Equal(t, "Invoice", meta.Query.From)
	assert.Equal(t, "from:\n  name: Invoice\n", meta.Query.DTQL)
	assert.Equal(t, "none", meta.Query.Policy)
	assert.Nil(t, meta.DB.Ref)
	assert.Equal(t, "sqlite", meta.DB.Driver)
	assert.Equal(t, "$key", recordset.Columns[0].Name)
	assert.NotContains(t, metaText, "chinook.db", "meta.json names no path")

	// the same id again is refused, and the result is not touched
	stdout, stderr, code = runQuery(t, "", "--db", "sqlite://"+chinookFixture(t), "--from", "Invoice", "--no-policies", "--result-id", "t1")
	assert.Equal(t, exitCodeUsage, code)
	assert.Equal(t, `{"resultFiles":null,"reason":"result-id-in-use"}`+"\n", stdout)
	assert.Contains(t, stderr, "result-id-in-use")

	out, _, code := runResults(t, "locate", "--id", "t1", "--format", "json")
	require.Equal(t, 0, code)
	var located struct {
		ResultFiles queryresults.Locator `json:"resultFiles"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &located))
	assert.Equal(t, "t1", located.ResultFiles.ID)
	assert.Equal(t, filepath.Join(located.ResultFiles.Root, "t1", "data.json"), located.ResultFiles.Data)
	out, _, code = runResults(t, "check", "--format", "json")
	assert.Equal(t, 0, code)
	assert.Equal(t, `{"usable":true}`+"\n", out)
}

func TestQueryRunResultIDRefusals(t *testing.T) {
	db := "sqlite://" + chinookFixture(t)
	cases := map[string]struct {
		argv   []string
		code   int
		stdout string
	}{
		"bad id":                    {[]string{"--db", db, "--from", "Invoice", "--no-policies", "--result-id", "../x"}, exitCodeUsage, `{"resultFiles":null,"reason":"bad-result-id"}` + "\n"},
		"csv":                       {[]string{"--db", db, "--from", "Invoice", "--no-policies", "--result-id", "t2", "--format", "csv"}, exitCodeUsage, `{"resultFiles":null,"reason":"query-failed"}` + "\n"},
		"no rows":                   {[]string{"--db", db, "--from", "Invoice", "--no-policies", "--result-id", "t2", "--result-rows", "0"}, exitCodeUsage, `{"resultFiles":null,"reason":"query-failed"}` + "\n"},
		"too many rows":             {[]string{"--db", db, "--from", "Invoice", "--no-policies", "--result-id", "t2", "--result-rows", "10001"}, exitCodeUsage, `{"resultFiles":null,"reason":"query-failed"}` + "\n"},
		"result-rows without an id": {[]string{"--db", db, "--from", "Invoice", "--no-policies", "--result-rows", "5"}, exitCodeUsage, ""},
		"a stray word":              {[]string{"--db", db, "--from", "Invoice", "stray"}, 1, ""},
		"another usage error":       {[]string{"--from", "Invoice", "--result-id", "t2"}, exitCodeUsage, `{"resultFiles":null,"reason":"query-failed"}` + "\n"},
		"database cannot be opened": {[]string{"--db", "sqlite://" + filepath.Join(t.TempDir(), "missing", "x.db"), "--from", "Invoice", "--no-policies", "--result-id", "t3"}, exitCodeDatabase, `{"resultFiles":null,"reason":"query-failed"}` + "\n"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := resultsHome(t)
			stdout, stderr, code := runQuery(t, "", tc.argv...)
			assert.Equal(t, tc.code, code, stderr)
			assert.Equal(t, tc.stdout, stdout)
			entries, _ := os.ReadDir(root)
			assert.Empty(t, entries, "nothing is written")
		})
	}
	// --format json is the format the files take
	resultsHome(t)
	_, stderr, code := runQuery(t, "", "--db", db, "--from", "Invoice", "--no-policies", "--result-id", "t4", "--format", "json")
	assert.Equal(t, 0, code, stderr)
}

func TestQueryRunResultStoreRefusedBeforeTheDatabaseOpens(t *testing.T) {
	home := tempDatatugHome(t)
	require.NoError(t, os.Mkdir(filepath.Join(home, ".git"), 0o700))
	missing := "sqlite://" + filepath.Join(t.TempDir(), "never-opened.db")
	stdout, stderr, code := runQuery(t, "", "--db", missing, "--from", "Invoice", "--no-policies", "--result-id", "t1")
	assert.Equal(t, exitCodeUsage, code)
	assert.Equal(t, `{"resultFiles":null,"reason":"inside-git"}`+"\n", stdout)
	assert.NotContains(t, stderr, home)
	out, _, code := runResults(t, "check")
	assert.Equal(t, 0, code)
	assert.Equal(t, `{"usable":false,"reason":"inside-git"}`+"\n", out)
	_, err := os.Stat(filepath.Join(home, ".datatug"))
	assert.True(t, errors.Is(err, os.ErrNotExist), "nothing is written")

	// a symlinked root
	home = tempDatatugHome(t)
	require.NoError(t, os.Mkdir(filepath.Join(home, ".datatug"), 0o700))
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(home, ".datatug", "query-results")))
	stdout, _, code = runQuery(t, "", "--db", missing, "--from", "Invoice", "--no-policies", "--result-id", "t1")
	assert.Equal(t, exitCodeUsage, code)
	assert.Equal(t, `{"resultFiles":null,"reason":"symlink"}`+"\n", stdout)
}

// writeWideDB is a SQLite file with a table of n rows, one of them holding a
// 70 KiB cell.
func writeWideDB(t *testing.T, n int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wide.db")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`CREATE TABLE Big (id INTEGER PRIMARY KEY, note TEXT)`)
	require.NoError(t, err)
	for i := 1; i <= n; i++ {
		note := fmt.Sprintf("row %d", i)
		if i == 1 {
			note = strings.Repeat("n", 70<<10)
		}
		_, err = db.Exec(`INSERT INTO Big VALUES (?, ?)`, i, note)
		require.NoError(t, err)
	}
	return path
}

func TestQueryRunResultRowsCapAndCellCut(t *testing.T) {
	root := resultsHome(t)
	db := "sqlite://" + writeWideDB(t, 30)
	stdout, stderr, code := runQuery(t, "", "--db", db, "--from", "Big", "--no-policies", "--result-id", "t3", "--result-rows", "5")
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, `{"resultFiles":{"version":1},"rowsKept":5,"hasMore":true,"columns":3}`+"\n", stdout)
	meta, recordset, _ := readResult(t, root, "t3")
	assert.Equal(t, queryresults.Rows{Kept: 5, HasMore: true, TruncatedBy: "rows"}, meta.Rows)
	assert.Len(t, recordset.Rows, 5)
	assert.Equal(t, 1, meta.Columns[2].CutCells)
	assert.Len(t, recordset.Rows[0][2].Str, queryresults.MaxCellBytes)
}

// countingReader serves n records and counts the reads.
type countingReader struct {
	n, reads int
	failAt   int
	data     any
	missing  bool
}

func (r *countingReader) Next() (record.Record, error) {
	r.reads++
	if r.failAt > 0 && r.reads == r.failAt {
		return nil, errors.New("read failed")
	}
	if r.reads > r.n {
		return nil, dal.ErrNoMoreRecords
	}
	data := r.data
	if data == nil {
		data = map[string]any{"v": r.reads}
	}
	rec := record.NewRecordWithData(record.NewKeyWithID("Big", r.reads), data)
	if r.missing {
		rec.SetError(record.ErrRecordNotFound)
	}
	return rec, nil
}
func (r *countingReader) Cursor() (string, error) { return "", nil }
func (r *countingReader) Close() error            { return nil }

func TestCollectResultRowsReadsAtMostKeptPlusOne(t *testing.T) {
	reader := &countingReader{n: 1_000_000}
	collector := queryresults.NewCollector(queryresults.DefaultRows)
	require.NoError(t, collectResultRows(reader, collector))
	assert.Equal(t, queryresults.DefaultRows+1, reader.reads)
	data := collector.Build("t3", []string{"v"})
	assert.Equal(t, queryresults.Rows{Kept: 2000, HasMore: true, TruncatedBy: "rows"}, data.Rows)

	assert.NoError(t, collectResultRows(nil, collector))
	assert.Error(t, collectResultRows(&countingReader{n: 5, failAt: 2}, queryresults.NewCollector(5)))
	assert.Error(t, collectResultRows(&countingReader{n: 5, data: "not an object"}, queryresults.NewCollector(5)))
	missing := queryresults.NewCollector(5)
	require.NoError(t, collectResultRows(&countingReader{n: 2, missing: true}, missing))
	assert.Equal(t, 2, missing.Build("m", nil).Rows.Kept)
}

func TestQueryRunResultFromDTQLFileWithVariablesAndPolicies(t *testing.T) {
	root := resultsHome(t)
	url := setupQueryDB(t)
	dtqlText := "from:\n  name: products\nwhere:\n  op: '>='\n  left: { field: price }\n  right: { param: minPrice }\n"
	file := filepath.Join(t.TempDir(), "q.yaml")
	require.NoError(t, os.WriteFile(file, []byte(dtqlText), 0o600))
	dir := policiesDir(t, map[string]string{"pricing.yaml": minPricePolicy})

	stdout, stderr, code := runQuery(t, "", "--db", url, "-f", file, "--policies-dir", dir, "--var", "minPrice=10", "--var", "ids=[1,2]", "--result-id", "q1")
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, `"rowsKept":2`)
	meta, _, _ := readResult(t, root, "q1")
	assert.Equal(t, "dtql", meta.Query.Kind)
	assert.Equal(t, dtqlText, meta.Query.DTQL)
	assert.Equal(t, "dir", meta.Query.Policy)
	assert.JSONEq(t, `{"type":"integer","value":"10"}`, string(meta.Query.Parameters["minPrice"]))
	assert.JSONEq(t, `{"type":"set","values":[{"type":"integer","value":"1"},{"type":"integer","value":"2"}]}`, string(meta.Query.Parameters["ids"]))
	assert.Equal(t, "ingitdb", meta.DB.Driver)

	_, stderr, code = runQuery(t, "", "--db", url, "--from", "products", "--policies-dir", dir, "--policy", filepath.Join(dir, "pricing.yaml"), "--var", "minPrice=10", "--result-id", "q2")
	require.Equal(t, 0, code, stderr)
	meta, _, metaText := readResult(t, root, "q2")
	assert.Equal(t, "explicit", meta.Query.Policy)
	assert.NotContains(t, metaText, dir, "no policy path")
}

func TestAdHocPolicyAndParameters(t *testing.T) {
	t.Setenv(accesspolicies.DirEnv, "")
	assert.Equal(t, "discovered", adHocPolicy(queryOptions{}))
	t.Setenv(accesspolicies.DirEnv, "/somewhere")
	assert.Equal(t, "dir", adHocPolicy(queryOptions{}))
	assert.Equal(t, "none", adHocPolicy(queryOptions{noPolicies: true}))
	assert.Nil(t, typedParameters(nil))
	mixed := typedParameters(map[string]any{"m": []any{1, "a"}, "s": "x"})
	encoded, _ := json.Marshal(mixed)
	assert.JSONEq(t, `{"m":{"type":"string","value":"[1,\"a\"]"},"s":{"type":"string","value":"x"}}`, string(encoded))
}

func TestResultProvenance(t *testing.T) {
	at := time.Date(2026, 10, 15, 1, 2, 3, 0, time.UTC)
	assert.Equal(t, &queryresults.Provenance{Mode: "live", ObservedAt: "2026-10-15T01:02:03Z"}, resultProvenance(dalgo2http.Provenance{Source: dalgo2http.SourceLive, FetchedAt: at, Collection: "c"}))
	assert.Equal(t, "snapshot", resultProvenance(dalgo2http.Provenance{Source: dalgo2http.SourceSnapshot, FetchedAt: at}).Mode)
}

func TestQueryRunResultFromHTTPSourceRecordsProvenance(t *testing.T) {
	root := resultsHome(t)
	useInsecureLoopbackBackend(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"Gadget","color":"blue"}`))
	}))
	defer up.Close()
	project := writeHTTPSourceProject(t, up.URL+"/widgets?name={name}")
	stdout, stderr, code := runQuery(t, widgetsQuery, "--db", "http://"+project, "-f", "-", "--no-policies", "--var", "name=Gadget", "--result-id", "h1")
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, `"rowsKept":1`)
	assert.Contains(t, stderr, "source: live widgets")
	meta, recordset, metaText := readResult(t, root, "h1")
	require.NotNil(t, meta.Provenance)
	assert.Equal(t, "live", meta.Provenance.Mode)
	assert.NotContains(t, metaText, "127.0.0.1", "no URL")
	for _, column := range recordset.Columns {
		assert.NotEqual(t, "$provenance", column.Name)
	}
}

func TestQueryRunResultByProjectReference(t *testing.T) {
	registeredSQLiteSource(t)
	root := filepath.Join(os.Getenv("HOME"), ".datatug", "query-results")
	for i, ref := range []string{"datatug://shop/shop", "datatug://shop/shop?env=local", "datatug:///shop"} {
		id := fmt.Sprintf("r%d", i)
		_, stderr, code := runQuery(t, "", "--db", ref, "--from", "Customer", "--no-policies", "--result-id", id)
		require.Equal(t, 0, code, stderr)
		meta, _, _ := readResult(t, root, id)
		require.NotNil(t, meta.DB.Ref, ref)
		assert.Equal(t, "datatug://shop/shop?env=local", *meta.DB.Ref, ref)
		assert.Equal(t, "shop", *meta.DB.Project)
		assert.Equal(t, "local", *meta.DB.Env)
		assert.Equal(t, "shop", *meta.DB.Source)
	}
}

func TestFullDatatugRef(t *testing.T) {
	ctx := context.Background()
	_, ok := fullDatatugRef(ctx, "sqlite:///x.db")
	assert.False(t, ok)
	_, ok = fullDatatugRef(ctx, "datatug://bad name/x")
	assert.False(t, ok)
	tempDatatugHome(t)
	_, ok = fullDatatugRef(ctx, "datatug:///shop") // no project to resolve
	assert.False(t, ok)
	_, ok = fullDatatugRef(ctx, "datatug://nope/shop")
	assert.False(t, ok)

	dir := addProject(t)
	_, ok = fullDatatugRef(ctx, "datatug://shop/missing") // no environment lists it
	assert.False(t, ok)
	// a project folder that is the current folder but is not registered
	unregistered := filepath.Join(t.TempDir(), "p")
	require.NoError(t, os.MkdirAll(unregistered, 0o755))
	data, _ := os.ReadFile(filepath.Join(dir, "datatug-project.json"))
	require.NoError(t, os.WriteFile(filepath.Join(unregistered, "datatug-project.json"), data, 0o644))
	t.Chdir(unregistered)
	_, ok = fullDatatugRef(ctx, "datatug:///shop?env=local")
	assert.False(t, ok)
	assert.Equal(t, "", registeredProjectID(unregistered))
	assert.Equal(t, "shop", registeredProjectID(dir))

	require.NoError(t, os.WriteFile(filepath.Join(os.Getenv("HOME"), ".datatug.yaml"), []byte("projects: ["), 0o600))
	assert.Equal(t, "", registeredProjectID(dir))
}

func TestQueryRunSavedResultFiles(t *testing.T) {
	dir := covCFullProject(t)
	root := resultsHome(t)
	for _, tc := range []struct {
		id, query, kind string
		vars            []string
	}{
		{"s1", "f/dtqlq", "dtql", nil},
		{"s2", "f/sqlq", "sql", []string{"Id=2"}},
	} {
		stdout, stderr, code := runQuery(t, "", append([]string{"--project", dir, "--query", tc.query, "--env", "local", "--result-id", tc.id}, varsArgs(tc.vars)...)...)
		require.Equal(t, 0, code, stderr)
		assert.True(t, strings.HasPrefix(stdout, `{"resultFiles":{"version":1}`), stdout)
		meta, recordset, _ := readResult(t, root, tc.id)
		assert.Equal(t, "saved", meta.Query.Kind)
		assert.Equal(t, tc.query, meta.Query.SavedQueryID)
		assert.Equal(t, tc.kind, meta.Query.SavedQueryType)
		assert.Equal(t, "project", meta.Query.Policy)
		assert.Empty(t, meta.Query.DTQL, "the saved text is not copied")
		assert.Nil(t, meta.DB.Ref, "the project is not registered")
		assert.Equal(t, "sqlite", meta.DB.Driver)
		assert.NotEmpty(t, recordset.Rows)
	}

	// a registered project gets its reference
	require.NoError(t, os.WriteFile(filepath.Join(os.Getenv("HOME"), ".datatug.yaml"), []byte("projects:\n  - id: covc\n    path: "+dir+"\n"), 0o600))
	_, stderr, code := runQuery(t, "", "--project", dir, "--query", "f/dtqlq", "--env", "local", "--result-id", "s3", "--result-rows", "1")
	require.Equal(t, 0, code, stderr)
	meta, _, _ := readResult(t, root, "s3")
	require.NotNil(t, meta.DB.Ref)
	assert.Equal(t, "datatug://covc/main?env=local", *meta.DB.Ref)
	assert.Equal(t, queryresults.Rows{Kept: 1, HasMore: true, TruncatedBy: "rows"}, meta.Rows)

	// a failing saved query writes nothing
	stdout, _, code := runQuery(t, "", "--project", dir, "--query", "f/sqlq", "--env", "local", "--result-id", "s4")
	assert.NotEqual(t, 0, code)
	assert.Equal(t, `{"resultFiles":null,"reason":"query-failed"}`+"\n", stdout)
	_, err := os.Lstat(filepath.Join(root, "s4"))
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

func varsArgs(vars []string) []string {
	var argv []string
	for _, v := range vars {
		argv = append(argv, "--var", v)
	}
	return argv
}

func TestQueryRunSavedHTTPResultFiles(t *testing.T) {
	dir := covCWriteProject(t, covCQueryFiles("f", "web", "HTTP", "http", "https://example.test/x", ""))
	root := resultsHome(t)
	orig := runStructuredHTTPQuery
	t.Cleanup(func() { runStructuredHTTPQuery = orig })
	rows := []secureread.Row{{Key: "1", Data: map[string]any{"id": 1}}}
	runStructuredHTTPQuery = func(context.Context, *secureread.Executor, string, dal.Query, map[string]any) (secureread.Result, error) {
		return secureread.Result{Columns: []string{"id"}, Rows: rows,
			Provenance: &dalgo2http.Provenance{Collection: "web", Source: dalgo2http.SourceSnapshot, FetchedAt: time.Unix(0, 0)}}, nil
	}
	_, stderr, code := runQuery(t, "", "--project", dir, "--query", "f/web", "--result-id", "w1")
	require.Equal(t, 0, code, stderr)
	meta, _, _ := readResult(t, root, "w1")
	assert.Equal(t, "http", meta.Query.SavedQueryType)
	assert.Equal(t, "http", meta.DB.Driver)
	assert.Nil(t, meta.DB.Ref)
	require.NotNil(t, meta.Provenance)
	assert.Equal(t, "snapshot", meta.Provenance.Mode)

	rows = []secureread.Row{{Key: "1", Data: map[string]any{"$key": 1}}}
	_, _, code = runQuery(t, "", "--project", dir, "--query", "f/web", "--result-id", "w2")
	assert.Equal(t, 1, code)
}

func TestSavedQueryDB(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	sqlDef := &datatug.QueryDef{Type: datatug.QueryTypeSQL}
	assert.Equal(t, queryresults.DB{}, savedQueryDB(ctx, &covCStore{envsErr: boom}, "", "", "", sqlDef))
	assert.Equal(t, queryresults.DB{}, savedQueryDB(ctx, &covCStore{catalogsErr: boom}, "", "", "local", sqlDef))
	assert.Equal(t, queryresults.DB{}, savedQueryDB(ctx, &covCStore{catalogs: datatug.DbCatalogs{{}}, envErr: boom}, "", "", "local", sqlDef))
	env := &datatug.Environment{DbServers: datatug.EnvDbServers{{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}}}
	ingit := &covCStore{catalogs: datatug.DbCatalogs{{}}, env: env, catalog: datatug.DbCatalog{Driver: "ingitdb", Path: "x"}}
	got := savedQueryDB(ctx, ingit, t.TempDir(), "", "local", sqlDef)
	assert.Equal(t, "ingitdb", got.Driver)
	assert.Nil(t, got.Ref)
	unsupported := &covCStore{catalogs: datatug.DbCatalogs{{}}, env: env, catalog: datatug.DbCatalog{Driver: "mysql"}}
	assert.Equal(t, queryresults.DB{}, savedQueryDB(ctx, unsupported, t.TempDir(), "", "local", sqlDef))

	// a federated DTQL query is described by its root's database
	federated := &datatug.QueryDef{Type: datatug.QueryTypeDTQL, Text: "from: {name: Item, database: main}\n"}
	database, err := savedQueryDatabase(ctx, &covCStore{catalogsErr: boom}, "local", federated)
	require.NoError(t, err)
	assert.Equal(t, "main", database)
	_, err = savedQueryDatabase(ctx, &covCStore{catalogsErr: boom}, "local", &datatug.QueryDef{Type: datatug.QueryTypeDTQL, Text: ":"})
	assert.ErrorIs(t, err, boom)
}

func TestResultSinkFailures(t *testing.T) {
	resultsHome(t)
	o := queryOptions{resultID: "f1", resultRows: 5}
	sink, err := openResultSink(o)
	require.NoError(t, err)
	// a result directory that cannot be written to
	require.NoError(t, os.Chmod(filepath.Join(sink.store.Root(), "f1"), 0o500))
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(sink.store.Root(), "f1"), 0o700) })
	err = sink.write(&bytes.Buffer{}, queryresults.NewCollector(5), nil)
	assert.Equal(t, queryresults.ReasonUnwritable, resultFailureReason(err))
	assert.Equal(t, exitCodeUsage, covCExitCode(t, err))

	// stdout that cannot be written to
	sink, err = openResultSink(queryOptions{resultID: "f2", resultRows: 5})
	require.NoError(t, err)
	err = sink.write(failingWriter{}, queryresults.NewCollector(5), nil)
	assert.Equal(t, 1, covCExitCode(t, err))
	sink.discardUnlessWritten()
	_, statErr := os.Lstat(filepath.Join(sink.store.Root(), "f2", "meta.json"))
	assert.NoError(t, statErr, "a written result is kept")

	// a store that cannot be opened
	orig := openResultsStore
	t.Cleanup(func() { openResultsStore = orig })
	openResultsStore = func() (*queryresults.Store, error) { return nil, &queryresults.Error{Reason: queryresults.ReasonOwner} }
	_, err = openResultSink(o)
	assert.Equal(t, "owner", resultFailureReason(err))
	assert.Equal(t, "query-failed", resultFailureReason(errors.New("x")))

	// a reader that fails, and a record with a $key field
	sink, err = func() (*resultSink, error) {
		openResultsStore = orig
		return openResultSink(queryOptions{resultID: "f3", resultRows: 5})
	}()
	require.NoError(t, err)
	err = sink.collectAndWrite(&bytes.Buffer{}, &countingReader{n: 3, failAt: 1}, nil)
	assert.Equal(t, exitCodeDatabase, covCExitCode(t, err))
	err = sink.collectAndWrite(&bytes.Buffer{}, &countingReader{n: 1, data: map[string]any{"$key": 1}}, nil)
	assert.Equal(t, 1, covCExitCode(t, err))
}

func TestQueryResultsCommands(t *testing.T) {
	root := resultsHome(t)
	_, stderr, code := runQuery(t, "", "--db", "sqlite://"+chinookFixture(t), "--from", "Genre", "--no-policies", "--result-id", "g1")
	require.Equal(t, 0, code, stderr)
	_, _, code = runQuery(t, "", "--db", "sqlite://"+chinookFixture(t), "--from", "MediaType", "--no-policies", "--result-id", "g2")
	require.Equal(t, 0, code)

	out, _, code := runResults(t, "locate", "--id", "nope")
	assert.Equal(t, 0, code)
	assert.Equal(t, `{"resultFiles":null,"reason":"not-found"}`+"\n", out)
	out, _, code = runResults(t, "locate", "--id", "../x")
	assert.Equal(t, exitCodeUsage, code)
	assert.Equal(t, `{"resultFiles":null,"reason":"bad-result-id"}`+"\n", out)
	_, _, code = runResults(t, "locate", "--id", "g1", "--format", "text")
	assert.Equal(t, exitCodeUsage, code)
	_, _, code = runResults(t, "locate")
	assert.Equal(t, 1, code, "--id is required")

	out, _, code = runResults(t, "rm", "--id", "g1")
	assert.Equal(t, 0, code)
	assert.Equal(t, `{"removed":1}`+"\n", out)
	_, err := os.Lstat(filepath.Join(root, "g1"))
	assert.True(t, errors.Is(err, os.ErrNotExist))
	_, err = os.Lstat(filepath.Join(root, "g2"))
	assert.NoError(t, err, "rm removes one result only")
	_, _, code = runResults(t, "rm", "--id", "g1")
	assert.Equal(t, exitCodeNotFound, code)
	_, _, code = runResults(t, "rm", "--id", "../x")
	assert.Equal(t, exitCodeUsage, code)
	_, _, code = runResults(t, "rm", "--id", "g2", "--format", "yaml")
	assert.Equal(t, exitCodeUsage, code)

	out, _, code = runResults(t, "prune")
	assert.Equal(t, 0, code)
	assert.Equal(t, `{"removed":0}`+"\n", out)
	out, _, code = runResults(t, "prune", "--all")
	assert.Equal(t, 0, code)
	assert.Equal(t, `{"removed":1}`+"\n", out)
	_, _, code = runResults(t, "prune", "--format", "csv")
	assert.Equal(t, exitCodeUsage, code)
	_, _, code = runResults(t, "check", "--format", "csv")
	assert.Equal(t, exitCodeUsage, code)

	// a store that cannot be opened
	orig := openResultsStore
	t.Cleanup(func() { openResultsStore = orig })
	openResultsStore = func() (*queryresults.Store, error) {
		return nil, &queryresults.Error{Reason: queryresults.ReasonInsideGit}
	}
	out, _, code = runResults(t, "locate", "--id", "g2")
	assert.Equal(t, exitCodeUsage, code)
	assert.Equal(t, `{"resultFiles":null,"reason":"inside-git"}`+"\n", out)
	_, _, code = runResults(t, "prune")
	assert.Equal(t, exitCodeUsage, code)
	_, _, code = runResults(t, "rm", "--id", "g2")
	assert.Equal(t, exitCodeUsage, code)

	// a store whose listing fails
	broken := filepath.Join(t.TempDir(), "results")
	require.NoError(t, os.WriteFile(broken, nil, 0o600))
	openResultsStore = func() (*queryresults.Store, error) {
		store, err := queryresults.Open(filepath.Dir(broken))
		if err != nil {
			return nil, err
		}
		return store, nil
	}
	require.NoError(t, os.Chmod(filepath.Dir(broken), 0o300))
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(broken), 0o700) })
	_, _, code = runResults(t, "prune")
	assert.Equal(t, exitCodeUsage, code)
}

func TestProjectDB(t *testing.T) {
	db := projectDB(dbcopy.DatatugRef{Project: "p", DB: "d", Env: "e"}, "sqlite")
	assert.Equal(t, "datatug://p/d?env=e", *db.Ref)
}
