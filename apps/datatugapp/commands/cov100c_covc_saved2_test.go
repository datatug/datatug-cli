package commands

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
)

func TestCovCSavedRunLoadQueryFailure(t *testing.T) {
	dir := covCWriteProject(t, map[string]string{"queries/f/broken.query.json": "{not json"})
	_, _, err := covCRunSaved(t, queryOptions{project: dir, query: "f/broken", format: "json"})
	if covCExitCode(t, err) != exitCodeDatabase || !strings.Contains(err.Error(), "load query") {
		t.Fatalf("load failure: %v", err)
	}
}

func TestCovCSavedRunEnvironmentAndCatalogResolution(t *testing.T) {
	db := covCSQLiteFile(t, "main.sqlite", `CREATE TABLE Item(id INTEGER PRIMARY KEY, name TEXT)`, `INSERT INTO Item VALUES (1,'a')`)
	dtqlFile := covCQueryFiles("f", "q", "DTQL", "dtql", covCItemDTQL, "")
	// single environment is picked without --env
	dir := covCWriteProject(t, covCMerge(covCEnvFiles(map[string]string{"main": db}), dtqlFile))
	stdout, stderr, err := covCRunSaved(t, queryOptions{project: dir, query: "f/q", format: "json", quiet: true})
	if err != nil || !strings.Contains(stdout, "a") {
		t.Fatalf("single env: %v %s %s", err, stdout, stderr)
	}
	// no environments at all
	dir = covCWriteProject(t, dtqlFile)
	if _, _, err = covCRunSaved(t, queryOptions{project: dir, query: "f/q", format: "json"}); err == nil || !strings.Contains(err.Error(), "no environments") {
		t.Fatalf("no envs: %v", err)
	}
	// the query names its catalog through targets
	targeted := covCQueryFiles("f", "q", "DTQL", "dtql", covCItemDTQL, `,"targets":[{"catalog":"main"}]`)
	dir = covCWriteProject(t, covCMerge(covCEnvFiles(map[string]string{"main": db, "spare": db}), targeted))
	if _, stderr, err = covCRunSaved(t, queryOptions{project: dir, query: "f/q", env: "local", format: "json", quiet: true}); err != nil {
		t.Fatalf("targets: %v %s", err, stderr)
	}
	// ... and a target that no server declares is reported as not found
	missing := covCQueryFiles("f", "q", "DTQL", "dtql", covCItemDTQL, `,"targets":[{"catalog":"absent"}]`)
	dir = covCWriteProject(t, covCMerge(covCEnvFiles(map[string]string{"main": db}), missing))
	if _, _, err = covCRunSaved(t, queryOptions{project: dir, query: "f/q", env: "local", format: "json"}); err == nil || !strings.Contains(err.Error(), "not found in environment") {
		t.Fatalf("absent catalog: %v", err)
	}
	// an environment without catalogs
	dir = covCWriteProject(t, covCMerge(map[string]string{"environments/local/local.env.json": `{"id":"local","dbServers":[{"driver":"sqlite3"}]}`}, dtqlFile))
	if _, _, err = covCRunSaved(t, queryOptions{project: dir, query: "f/q", env: "local", format: "json"}); err == nil || !strings.Contains(err.Error(), "no database catalogs") {
		t.Fatalf("no catalogs: %v", err)
	}
}

func TestCovCQuerySourceURLFromCatalog(t *testing.T) {
	mk := func(driver, path string) datatug.DbCatalog {
		var catalog datatug.DbCatalog
		catalog.ID = "c"
		catalog.Driver = driver
		catalog.Path = path
		return catalog
	}
	if _, err := querySourceURLFromCatalog(mk("sqlite3", ""), "/p"); err == nil || !strings.Contains(err.Error(), "no path") {
		t.Fatalf("sqlite no path: %v", err)
	}
	if _, err := querySourceURLFromCatalog(mk("ingitdb", ""), "/p"); err == nil || !strings.Contains(err.Error(), "no path") {
		t.Fatalf("ingitdb no path: %v", err)
	}
	if got, err := querySourceURLFromCatalog(mk("ingitdb", "data"), "/p"); err != nil || got != "ingitdb:///p/data" {
		t.Fatalf("ingitdb: %q %v", got, err)
	}
	if _, err := querySourceURLFromCatalog(mk("oracle", "x"), "/p"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("unsupported driver: %v", err)
	}
}

func TestCovCSavedFailureAndLimitationReporting(t *testing.T) {
	if covCExitCode(t, savedQueryFailure(secureread.ErrAccessDenied)) != exitCodeAccessDenied {
		t.Fatal("access denied code")
	}
	if covCExitCode(t, savedQueryFailure(secureread.ErrNoPrincipal)) != exitCodeUsage {
		t.Fatal("no principal code")
	}
	var buf bytes.Buffer
	writeSavedQueryLimitations(&buf, []secureread.Limitation{
		{Kind: secureread.LimitationRowsFiltered},
		{Kind: secureread.LimitationHiddenColumns, Columns: []string{"a", "b"}},
	})
	if !strings.Contains(buf.String(), "rows filtered by policy") || !strings.Contains(buf.String(), "hidden columns: a, b") {
		t.Fatalf("limitations: %q", buf.String())
	}
}

func TestCovCRunHTTPSavedQueryParameters(t *testing.T) {
	orig := runStructuredHTTPQuery
	t.Cleanup(func() { runStructuredHTTPQuery = orig })
	var seen dal.Query
	runStructuredHTTPQuery = func(_ context.Context, _ *secureread.Executor, _ string, query dal.Query, _ map[string]any) (secureread.Result, error) {
		seen = query
		return secureread.Result{}, nil
	}
	def := &datatug.QueryDef{Parameters: datatug.Parameters{
		{ID: "given", Type: "string"},
		{ID: "optional", Type: "string"},
		{ID: "needed", Type: "string", IsRequired: true},
	}}
	def.ID = "web"
	executor := secureread.NewExecutor(secureread.Session{Unrestricted: true})
	if _, err := runHTTPSavedQuery(context.Background(), executor, "/p", def, map[string]any{"given": "x"}); err == nil || !strings.Contains(err.Error(), "needed") {
		t.Fatalf("missing required: %v", err)
	}
	if _, err := runHTTPSavedQuery(context.Background(), executor, "/p", def, map[string]any{"given": "x", "needed": "y"}); err != nil {
		t.Fatal(err)
	}
	if structured, ok := seen.(dal.StructuredQuery); !ok || structured.Where() == nil {
		t.Fatalf("bound parameters missing from query: %#v", seen)
	}
}

func TestCovCDefaultStructuredHTTPQueryRefusesPlainHTTP(t *testing.T) {
	dir := covCWriteProject(t, covCQueryFiles("f", "web", "HTTP", "http", "http://127.0.0.1:1/x", ""))
	def := &datatug.QueryDef{}
	def.ID = "f/web"
	executor := secureread.NewExecutor(secureread.Session{Unrestricted: true})
	// The default runStructuredHTTPQuery enforces https-only project descriptors,
	// so this fails before any connection is attempted.
	if _, err := runHTTPSavedQuery(context.Background(), executor, dir, def, nil); err == nil {
		t.Fatal("plain http project query was run")
	}
}

// covCStreamProject builds a project over the streamFixture databases.
func covCStreamProject(t *testing.T, queries map[string]string) string {
	t.Helper()
	urls := streamFixture(t, 3)
	return covCWriteProject(t, covCMerge(covCEnvFiles(map[string]string{
		"orders":    strings.TrimPrefix(urls["orders"], "sqlite://"),
		"countries": strings.TrimPrefix(urls["countries"], "sqlite://"),
	}), queries))
}

func TestCovCSavedStreamedErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"region":"Europe"}}`)
	}))
	defer server.Close()
	lookups := func(base string) string {
		return fmt.Sprintf(`,"federation":{"ovdbBaseUrl":%q,"lookups":[{"database":"extra","collection":"Country","fromColumn":"id","concurrency":2,"fields":[{"source":"region","target":"region"}]}]}`, base)
	}
	parameterised := strings.Replace(streamingJoinDTQL, "columns:", "where: {op: '==', left: {field: id, source: o}, right: {param: Wanted}}\ncolumns:", 1)
	dir := covCStreamProject(t, covCMerge(
		covCQueryFiles("f", "nocols", "DTQL", "dtql", streamingLookupDTQL, ""),
		covCQueryFiles("f", "lookup", "DTQL", "dtql", streamingLookupDTQL, lookups(server.URL)),
		covCQueryFiles("f", "badlookup", "DTQL", "dtql", streamingLookupDTQL, lookups("ftp://nope")),
		covCQueryFiles("f", "param", "DTQL", "dtql", parameterised, ""),
	))
	if _, _, err := covCRunSaved(t, queryOptions{project: dir, query: "f/nocols", env: "local", format: "csv", quiet: true}); err == nil || !strings.Contains(err.Error(), "explicit DTQL columns") {
		t.Fatalf("csv without columns: %v", err)
	}
	stdout, stderr, err := covCRunSaved(t, queryOptions{project: dir, query: "f/lookup", env: "local", format: "jsonl"})
	if err != nil || strings.Count(stdout, "Europe") != 3 {
		t.Fatalf("streamed lookups: %v %s %s", err, stdout, stderr)
	}
	if _, _, err = covCRunSaved(t, queryOptions{project: dir, query: "f/badlookup", env: "local", format: "jsonl", quiet: true}); err == nil || !strings.Contains(err.Error(), "OVDB base URL") {
		t.Fatalf("bad lookup base: %v", err)
	}
	if _, _, err = covCRunSaved(t, queryOptions{project: dir, query: "f/param", env: "local", format: "jsonl", quiet: true}); err == nil {
		t.Fatal("unresolved parameter accepted by streaming path")
	}
	// federated databases with no environment to resolve them
	bare := covCWriteProject(t, covCQueryFiles("f", "fed", "DTQL", "dtql", streamingLookupDTQL, ""))
	if _, _, err = covCRunSaved(t, queryOptions{project: bare, query: "f/fed", format: "jsonl"}); err == nil || !strings.Contains(err.Error(), "no environments") {
		t.Fatalf("federated without environment: %v", err)
	}
}

func TestCovCSavedStreamedSourceOpenFailure(t *testing.T) {
	dir := covCWriteProject(t, covCMerge(
		covCEnvFiles(map[string]string{
			"orders":    "/nonexistent-covc/orders.sqlite",
			"countries": "/nonexistent-covc/countries.sqlite",
		}),
		covCQueryFiles("f", "rows", "DTQL", "dtql", streamingJoinDTQL, ""),
	))
	if _, _, err := covCRunSaved(t, queryOptions{project: dir, query: "f/rows", env: "local", format: "jsonl", quiet: true}); err == nil {
		t.Fatal("unopenable federated sources accepted")
	}
}
