package commands

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/spf13/cobra"
)

// covCStore is a datatug.ProjectStore whose loaders return scripted values.
type covCStore struct {
	datatug.ProjectStore
	envs         datatug.Environments
	envsErr      error
	env          *datatug.Environment
	envErr       error
	catalogs     datatug.DbCatalogs
	catalogsErr  error
	catalog      datatug.DbCatalog
	catalogErr   error
	queryDef     *datatug.QueryDef
	queryErr     error
	putQueryErr  error
	putQueryCall int
}

func (s *covCStore) LoadEnvironments(context.Context, ...datatug.StoreOption) (datatug.Environments, error) {
	return s.envs, s.envsErr
}
func (s *covCStore) LoadEnvironment(context.Context, string, ...datatug.StoreOption) (*datatug.Environment, error) {
	return s.env, s.envErr
}
func (s *covCStore) LoadEnvDbCatalogs(context.Context, string, ...datatug.StoreOption) (datatug.DbCatalogs, error) {
	return s.catalogs, s.catalogsErr
}
func (s *covCStore) LoadEnvDbCatalog(context.Context, string, string, string, ...datatug.StoreOption) (datatug.DbCatalog, error) {
	return s.catalog, s.catalogErr
}
func (s *covCStore) LoadQuery(context.Context, string, ...datatug.StoreOption) (*datatug.QueryDef, error) {
	return s.queryDef, s.queryErr
}

// covCWriteProject writes files (slash paths) under a fresh project dir that
// also gets a datatug-project.json, and returns the dir.
func covCWriteProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	all := map[string]string{"datatug-project.json": `{"id":"covc","title":"covC"}`}
	for name, content := range files {
		all[name] = content
	}
	for name, content := range all {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func covCFileStore(dir string) datatug.ProjectStore {
	store, id := filestore.NewSingleProjectStore(dir, "covc")
	return store.GetProjectStore(id)
}

// covCEnvFiles declares env "local" with one sqlite3 catalog per name/path.
func covCEnvFiles(catalogs map[string]string) map[string]string {
	files := map[string]string{}
	names := make([]string, 0, len(catalogs))
	for name, path := range catalogs {
		names = append(names, fmt.Sprintf("%q", name))
		files["environments/local/catalogs/"+name+"/"+name+".db.json"] = fmt.Sprintf(`{"driver":"sqlite3","path":%q}`, path)
	}
	files["environments/local/local.env.json"] = `{"id":"local","dbServers":[{"driver":"sqlite3","catalogs":[` + strings.Join(names, ",") + `]}]}`
	return files
}

func covCMerge(maps ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func covCQueryFiles(folder, id, typ, sidecarExt, text string, extra string) map[string]string {
	files := map[string]string{
		"queries/" + folder + "/" + id + ".query.json": `{"id":"` + id + `","type":"` + typ + `"` + extra + `}`,
	}
	if sidecarExt != "" {
		files["queries/"+folder+"/"+id+".query."+sidecarExt] = text
	}
	return files
}

func covCExitCode(t *testing.T, err error) int {
	t.Helper()
	var coder ExitCoder
	if !errors.As(err, &coder) {
		t.Fatalf("want an ExitCoder error, got %v", err)
	}
	return coder.ExitCode()
}

func covCRunSaved(t *testing.T, o queryOptions) (stdout, stderr string, err error) {
	t.Helper()
	cmd := &cobra.Command{} // never executed, so cmd.Context() is nil
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	err = runSavedQueryCommand(cmd, o)
	return out.String(), errOut.String(), err
}

const covCItemDTQL = "from: {name: Item}\n"

// covCFullProject is a project with a sqlite catalog and one query of each type.
func covCFullProject(t *testing.T) string {
	t.Helper()
	db := covCSQLiteFile(t, "main.sqlite",
		`CREATE TABLE Item(id INTEGER PRIMARY KEY, name TEXT)`,
		`INSERT INTO Item VALUES (1,'a'),(2,'b')`)
	return covCWriteProject(t, covCMerge(
		covCEnvFiles(map[string]string{"main": db}),
		covCQueryFiles("f", "sqlq", "SQL", "sql", "SELECT id, name FROM Item WHERE id = @Id",
			`,"parameters":[{"id":"Id","type":"int","isRequired":true}]`),
		covCQueryFiles("f", "dtqlq", "DTQL", "dtql", covCItemDTQL, ""),
		covCQueryFiles("f", "other", "GraphQL", "", "", ""),
	))
}

func TestCovCSavedRunSQLNilContextUnrestricted(t *testing.T) {
	dir := covCFullProject(t)
	tempDatatugHome(t)
	stdout, stderr, err := covCRunSaved(t, queryOptions{project: dir, query: "f/sqlq", env: "local", format: "json", vars: []string{"Id=2"}})
	if err != nil {
		t.Fatalf("run: %v (stderr %s)", err, stderr)
	}
	if !strings.Contains(stderr, "access: running without access policies") {
		t.Fatalf("stderr: %q", stderr)
	}
	if !strings.Contains(stdout, `"name": "b"`) && !strings.Contains(stdout, `"name":"b"`) {
		t.Fatalf("stdout: %s", stdout)
	}
	// Missing required parameter surfaces as a database exit code.
	_, _, err = covCRunSaved(t, queryOptions{project: dir, query: "f/sqlq", env: "local", format: "json"})
	if err == nil || !strings.Contains(err.Error(), "missing --var") {
		t.Fatalf("missing param: %v", err)
	}
}

func TestCovCSavedRunDTQLPlainAndStreamFallthrough(t *testing.T) {
	dir := covCFullProject(t)
	// jsonl on a non-federated DTQL query falls through the streaming attempt
	// (no named databases) to the ordinary path.
	stdout, stderr, err := covCRunSaved(t, queryOptions{project: dir, query: "f/dtqlq", env: "local", format: "jsonl", quiet: true})
	if err != nil {
		t.Fatalf("run: %v (%s)", err, stderr)
	}
	if !strings.Contains(stdout, `"name":"a"`) {
		t.Fatalf("stdout: %s", stdout)
	}
}

func TestCovCSavedRunErrors(t *testing.T) {
	dir := covCFullProject(t)
	// unknown project -> usage error (also covers the nil-context branch)
	home := tempDatatugHome(t)
	if err := os.WriteFile(filepath.Join(home, ".datatug.yaml"), []byte("projects: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := covCRunSaved(t, queryOptions{project: "covc-not-registered", query: "f/sqlq", format: "json"})
	if covCExitCode(t, err) != exitCodeUsage {
		t.Fatalf("unknown project: %v", err)
	}
	// bad --var
	_, _, err = covCRunSaved(t, queryOptions{project: dir, query: "f/sqlq", format: "json", vars: []string{"novalue"}})
	if covCExitCode(t, err) != exitCodeUsage {
		t.Fatalf("bad var: %v", err)
	}
	// unsupported query type
	_, _, err = covCRunSaved(t, queryOptions{project: dir, query: "f/other", format: "json"})
	if err == nil || !strings.Contains(err.Error(), "not yet runnable") {
		t.Fatalf("other type: %v", err)
	}
	// writer failure while rendering rows
	cmd := &cobra.Command{}
	cmd.SetOut(covCFailWriter{})
	cmd.SetErr(io.Discard)
	err = runSavedQueryCommand(cmd, queryOptions{project: dir, query: "f/dtqlq", env: "local", format: "json", quiet: true})
	if covCExitCode(t, err) != 1 {
		t.Fatalf("write failure: %v", err)
	}
}

func TestCovCSavedRunInvalidPoliciesDir(t *testing.T) {
	db := covCSQLiteFile(t, "main.sqlite", `CREATE TABLE Item(id INTEGER PRIMARY KEY, name TEXT)`)
	dir := covCWriteProject(t, covCMerge(
		covCEnvFiles(map[string]string{"main": db}),
		covCQueryFiles("f", "dtqlq", "DTQL", "dtql", covCItemDTQL, ""),
		map[string]string{"policies/broken.yaml": "ruleSets: [unclosed"},
	))
	_, _, err := covCRunSaved(t, queryOptions{project: dir, query: "f/dtqlq", env: "local", format: "json", as: "x"})
	if covCExitCode(t, err) != exitCodeUsage || !strings.Contains(err.Error(), "load access policies") {
		t.Fatalf("policies dir: %v", err)
	}
}

func TestCovCSavedRunHTTPProvenanceAndLookups(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"region":"Europe"}}`)
	}))
	defer server.Close()
	federation := fmt.Sprintf(`,"federation":{"ovdbBaseUrl":%q,"lookups":[{"database":"extra","collection":"Country","fromColumn":"id","concurrency":2,"fields":[{"source":"region","target":"region"}]}]}`, server.URL)
	dir := covCWriteProject(t, covCMerge(
		covCQueryFiles("f", "web", "HTTP", "http", "https://example.test/x", ""),
		covCQueryFiles("f", "webfed", "HTTP", "http", "https://example.test/x", federation),
		covCQueryFiles("f", "webbad", "HTTP", "http", "https://example.test/x", `,"federation":{"ovdbBaseUrl":"ftp://nope","lookups":[{"database":"extra","collection":"Country","fromColumn":"id","fields":[{"source":"region","target":"region"}]}]}`),
	))
	orig := runStructuredHTTPQuery
	t.Cleanup(func() { runStructuredHTTPQuery = orig })
	runStructuredHTTPQuery = func(context.Context, *secureread.Executor, string, dal.Query, map[string]any) (secureread.Result, error) {
		return secureread.Result{
			Columns: []string{"id"},
			Rows:    []secureread.Row{{Key: "1", Data: map[string]any{"id": 1}}},
			Limitations: []secureread.Limitation{
				{Kind: secureread.LimitationPolicy, Note: "covC policy note"},
			},
			Provenance: &dalgo2http.Provenance{Collection: "web", Source: dalgo2http.SourceLive, FetchedAt: time.Unix(0, 0)},
		}, nil
	}
	stdout, stderr, err := covCRunSaved(t, queryOptions{project: dir, query: "f/web", format: "json"})
	if err != nil {
		t.Fatalf("http run: %v", err)
	}
	if !strings.Contains(stderr, "source: live web") || !strings.Contains(stderr, "covC policy note") || !strings.Contains(stdout, "$provenance") {
		t.Fatalf("stderr=%q stdout=%q", stderr, stdout)
	}
	stdout, _, err = covCRunSaved(t, queryOptions{project: dir, query: "f/webfed", format: "json", quiet: true})
	if err != nil || !strings.Contains(stdout, "Europe") {
		t.Fatalf("lookup run: %v %s", err, stdout)
	}
	_, _, err = covCRunSaved(t, queryOptions{project: dir, query: "f/webbad", format: "json", quiet: true})
	if err == nil || !strings.Contains(err.Error(), "OVDB base URL") {
		t.Fatalf("bad lookup base: %v", err)
	}
}

func TestCovCResolveQueryProject(t *testing.T) {
	home := tempDatatugHome(t)
	cfg := filepath.Join(home, ".datatug.yaml")
	if err := os.WriteFile(cfg, []byte("projects: [unclosed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveQueryProject("covc-any"); err == nil || !strings.Contains(err.Error(), "settings") {
		t.Fatalf("broken settings: %v", err)
	}
	if err := os.WriteFile(cfg, []byte("projects:\n  - id: covc-empty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveQueryProject("covc-empty"); err == nil || !strings.Contains(err.Error(), "resolved to no directory") {
		t.Fatalf("empty project: %v", err)
	}
}

func TestCovCResolveEnvironmentAndDatabase(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("covC boom")
	if _, err := resolveQueryEnvironment(ctx, &covCStore{envsErr: boom}, ""); !errors.Is(err, boom) {
		t.Fatalf("envs error: %v", err)
	}
	two := covCFileStore(covCWriteProject(t, map[string]string{
		"environments/a/a.env.json": `{"id":"a"}`,
		"environments/b/b.env.json": `{"id":"b"}`,
	}))
	if _, err := resolveQueryEnvironment(ctx, two, ""); err == nil || !strings.Contains(err.Error(), "pass --env") {
		t.Fatalf("many envs: %v", err)
	}
	def := &datatug.QueryDef{}
	if _, err := resolveQueryDatabase(ctx, &covCStore{catalogsErr: boom}, "local", def); !errors.Is(err, boom) {
		t.Fatalf("catalogs error: %v", err)
	}
	dir := covCWriteProject(t, covCEnvFiles(map[string]string{"one": "/x/one.sqlite", "two": "/x/two.sqlite"}))
	if _, err := resolveQueryDatabase(ctx, covCFileStore(dir), "local", def); err == nil || !strings.Contains(err.Error(), "does not declare which one") {
		t.Fatalf("many catalogs: %v", err)
	}
}

func TestCovCResolveQuerySourceURL(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("covC boom")
	if _, err := resolveQuerySourceURL(ctx, &covCStore{envErr: boom}, "", "local", "db"); !errors.Is(err, boom) {
		t.Fatalf("env error: %v", err)
	}
	noServers := &covCStore{env: &datatug.Environment{DbServers: datatug.EnvDbServers{nil}}}
	if _, err := resolveQuerySourceURL(ctx, noServers, "", "local", "db"); err == nil || !strings.Contains(err.Error(), "no DB servers") {
		t.Fatalf("no servers: %v", err)
	}
	for _, driver := range []string{"sqlite3", "ingitdb"} {
		catalog := datatug.DbCatalog{}
		catalog.Driver = driver
		catalog.Path = "relative/path"
		if _, err := querySourceURLFromCatalog(catalog, ""); err == nil || !strings.Contains(err.Error(), "no project directory") {
			t.Fatalf("%s relative path without project dir: %v", driver, err)
		}
	}
}

func TestCovCSQLAndDTQLRunnersFailEarly(t *testing.T) {
	ctx := context.Background()
	executor := secureread.NewExecutor(secureread.Session{Unrestricted: true})
	boom := errors.New("covC boom")
	broken := &covCStore{envsErr: boom}
	if _, err := runSQLSavedQuery(ctx, executor, broken, "", "", &datatug.QueryDef{}, nil); !errors.Is(err, boom) {
		t.Fatalf("sql source error: %v", err)
	}
	if _, err := runDTQLSavedQuery(ctx, executor, broken, "", "", &datatug.QueryDef{Text: covCItemDTQL}, nil); !errors.Is(err, boom) {
		t.Fatalf("dtql source error: %v", err)
	}
	if _, err := runDTQLSavedQuery(ctx, executor, broken, "", "", &datatug.QueryDef{Text: "from: [unclosed"}, nil); err == nil {
		t.Fatal("bad dtql text accepted")
	}
	federated := &datatug.QueryDef{Text: "from: {database: orders, name: Invoice}\n"}
	if _, err := federatedDTQLURLs(ctx, broken, "", "", federated); !errors.Is(err, boom) {
		t.Fatalf("federated env error: %v", err)
	}
	missingDB := &covCStore{envs: datatug.Environments{}, env: &datatug.Environment{DbServers: datatug.EnvDbServers{nil}}}
	if _, err := federatedDTQLURLs(ctx, missingDB, "", "local", federated); err == nil {
		t.Fatal("unresolvable database accepted")
	}
	if _, err := resolveSQLOrDTQLSourceURL(ctx, &covCStore{catalogsErr: boom}, "", "local", &datatug.QueryDef{}); !errors.Is(err, boom) {
		t.Fatalf("database error: %v", err)
	}
	if got, _, err := parseFederatedDTQL(ctx, broken, "", "", &datatug.QueryDef{Text: covCItemDTQL}); err != nil || got == nil {
		t.Fatalf("non-federated parse: %v %v", got, err)
	}
	collected := map[string]bool{}
	from := dal.From(dal.NewRootCollectionRef("Root", "")).Join(dal.NewJoinedSource(dal.NewDatabaseCollectionRef("other", "", "Dim", ""), dal.JoinInner))
	collectFederatedDatabases(from, collected)
	if !collected["other"] || len(collected) != 1 {
		t.Fatalf("collected: %v", collected)
	}
}

func covCJoinQuery(rootAlias, dimAlias string, limit int, order dal.OrderExpression, on ...dal.Condition) dal.StructuredQuery {
	root := dal.NewDatabaseCollectionRef("a", "", "Root", rootAlias)
	dim := dal.NewDatabaseCollectionRef("b", "", "Dim", dimAlias)
	if order != nil {
		dim = dim.WithScan(limit, order)
	} else {
		dim = dim.WithScan(limit)
	}
	from := dal.From(root).Join(dal.NewJoinedSource(dim, dal.JoinInner, on...))
	return dal.NewQueryBuilder(from).SelectColumns()
}

func TestCovCBoundedShapeBranches(t *testing.T) {
	id := dal.AscendingField("id")
	eq := dal.NewComparison(dal.NewFieldRef("Root", "x"), dal.Equal, dal.NewFieldRef("Dim", "id"))
	if !isBoundedFederatedRowShape(covCJoinQuery("", "", 10, id, eq)) {
		t.Fatal("names as aliases: want bounded")
	}
	if isBoundedFederatedRowShape(covCJoinQuery("", "", 10, id, dal.NewComparison(dal.NewFieldRef("Root", "x"), dal.GreaterThen, dal.NewFieldRef("Dim", "id")))) {
		t.Fatal("non-equality join accepted")
	}
	if isBoundedFederatedRowShape(covCJoinQuery("", "", 10, id, dal.WhereField("x", dal.Equal, 1))) {
		t.Fatal("non-comparison join condition accepted")
	}
	if isBoundedFederatedRowShape(covCJoinQuery("", "", 10, id, dal.NewComparison(dal.NewFieldRef("Root", "x"), dal.Equal, dal.NewFieldRef("Other", "id")))) {
		t.Fatal("unrelated source accepted")
	}
	two := dal.From(dal.NewDatabaseCollectionRef("a", "", "Root", "")).
		Join(dal.NewJoinedSource(dal.NewDatabaseCollectionRef("b", "", "D1", "").WithScan(1, id), dal.JoinInner)).
		Join(dal.NewJoinedSource(dal.NewDatabaseCollectionRef("c", "", "D2", "").WithScan(1, id), dal.JoinInner))
	if isBoundedFederatedRowShape(dal.NewQueryBuilder(two).SelectColumns()) {
		t.Fatal("two joins accepted")
	}
	nested := dal.From(dal.NewDatabaseCollectionRef("b", "", "Dim", "").WithScan(1, id)).
		Join(dal.NewJoinedSource(dal.NewDatabaseCollectionRef("c", "", "D2", ""), dal.JoinInner))
	withNested := dal.From(dal.NewDatabaseCollectionRef("a", "", "Root", "")).Join(dal.NewJoinedFrom(nested, dal.JoinInner, eq))
	if isBoundedFederatedRowShape(dal.NewQueryBuilder(withNested).SelectColumns()) {
		t.Fatal("nested join child accepted")
	}
	withAlgo := dal.From(dal.NewDatabaseCollectionRef("a", "", "Root", "")).
		Join(dal.NewJoinedSource(dal.NewDatabaseCollectionRef("b", "", "Dim", "").WithScan(1, id), dal.JoinInner, eq).WithAlgorithms(dal.JoinAlgorithmHash))
	if isBoundedFederatedRowShape(dal.NewQueryBuilder(withAlgo).SelectColumns()) {
		t.Fatal("join with algorithms accepted")
	}
	noDatabase := dal.From(dal.NewRootCollectionRef("Root", "")).
		Join(dal.NewJoinedSource(dal.NewDatabaseCollectionRef("b", "", "Dim", "").WithScan(1, id), dal.JoinInner, eq))
	if isBoundedFederatedRowShape(dal.NewQueryBuilder(noDatabase).SelectColumns()) {
		t.Fatal("root without database accepted")
	}
	groupRef := dal.NewCollectionGroupRef("Dim", "")
	nonCollection := dal.From(dal.NewDatabaseCollectionRef("a", "", "Root", "")).Join(dal.NewJoinedSource(groupRef, dal.JoinInner, eq))
	if isBoundedFederatedRowShape(dal.NewQueryBuilder(nonCollection).SelectColumns()) {
		t.Fatal("non-collection dimension accepted")
	}
	if isBoundedFederatedRowShape(covCJoinQuery("r", "d", 0, id, eq)) {
		t.Fatal("unbounded scan accepted")
	}
}

func TestCovCExplicitStreamColumns(t *testing.T) {
	from := dal.From(dal.NewDatabaseCollectionRef("a", "", "Root", ""))
	if got := explicitStreamColumns(dal.NewQueryBuilder(from).SelectColumns()); got != nil {
		t.Fatalf("no columns: %v", got)
	}
	field := dal.Column{Expression: dal.Field("id")}
	if got := explicitStreamColumns(dal.NewQueryBuilder(from).SelectColumns(field)); len(got) != 1 || got[0] != "id" {
		t.Fatalf("field column: %v", got)
	}
	anonymous := dal.Column{Expression: dal.NewAggregate("count", false, dal.Field("id"))}
	if got := explicitStreamColumns(dal.NewQueryBuilder(from).SelectColumns(anonymous)); got != nil {
		t.Fatalf("anonymous column: %v", got)
	}
}

// covCFailAfterWriter accepts `after` writes and then fails.
type covCFailAfterWriter struct{ after, calls int }

func (w *covCFailAfterWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls > w.after {
		return 0, errors.New("covC write failed")
	}
	return len(p), nil
}

func TestCovCWriteStreamedRowsErrors(t *testing.T) {
	ctx := context.Background()
	rec := func(data any) record.Record { return record.NewRecordWithData(record.NewKeyWithID("c", "1"), data) }
	if err := writeStreamedRows(ctx, covCFailWriter{}, "csv", []string{"a"}, &covCReader{}); err == nil {
		t.Fatal("csv header failure ignored")
	}
	if err := writeStreamedRows(ctx, io.Discard, "jsonl", nil, &covCReader{err: errors.New("covC next")}); err == nil {
		t.Fatal("reader error ignored")
	}
	if err := writeStreamedRows(ctx, io.Discard, "jsonl", nil, &covCReader{recs: []record.Record{rec(map[string]any{"bad": make(chan int)})}}); err == nil {
		t.Fatal("unserialisable data ignored")
	}
	if err := writeStreamedRows(ctx, io.Discard, "jsonl", nil, &covCReader{recs: []record.Record{rec(map[string]any{keyColumn: 1})}}); !errors.Is(err, errReservedKeyField) {
		t.Fatalf("reserved key: %v", err)
	}
	if err := writeStreamedRows(ctx, &covCFailAfterWriter{after: 1}, "csv", []string{"a"}, &covCReader{recs: []record.Record{rec(map[string]any{"a": 1})}}); err == nil {
		t.Fatal("csv row failure ignored")
	}
}
