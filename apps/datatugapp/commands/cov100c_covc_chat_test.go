package commands

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
)

// covCPutFailStore is a real project store whose PutQuery always fails.
type covCPutFailStore struct {
	datatug.ProjectStore
	revisioned datatug.RevisionedQueriesStore
}

func (s covCPutFailStore) PutQuery(context.Context, *datatug.QueryDefWithFolderPath, datatug.QueryWriteCondition) (*datatug.StoredQuery, error) {
	return nil, errors.New("covC put failed")
}
func (s covCPutFailStore) LoadQueryRevision(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.StoredQuery, error) {
	return s.revisioned.LoadQueryRevision(ctx, id, o...)
}
func (s covCPutFailStore) DeleteQueryRevision(ctx context.Context, id string, r datatug.QueryRevision) error {
	return s.revisioned.DeleteQueryRevision(ctx, id, r)
}

func covCChatService(t *testing.T, dir string, unrestricted bool) chatSavedQueries {
	t.Helper()
	store := covCFileStore(dir)
	return chatSavedQueries{
		projectDir: dir, store: store, projectID: "covc", env: "local",
		executor: secureread.NewExecutor(secureread.Session{Unrestricted: true}),
		session:  secureread.Session{Unrestricted: unrestricted},
	}
}

func TestCovCChatSaveRejections(t *testing.T) {
	ctx := context.Background()
	dir := covCWriteProject(t, nil)
	service := covCChatService(t, dir, true)

	plain := service
	plain.store = &covCStore{}
	if _, err := plain.Save(ctx, chat.SavedQuerySaveRequest{Title: "t", Type: "DTQL", Text: "x"}); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("non-revisioned store: %v", err)
	}
	if _, err := service.Save(ctx, chat.SavedQuerySaveRequest{Title: "t", Type: "SQL", Text: "select 1"}); err == nil || !strings.Contains(err.Error(), "only DTQL and HTTP") {
		t.Fatalf("SQL save: %v", err)
	}
	if _, err := service.Save(ctx, chat.SavedQuerySaveRequest{Title: "  ", Type: "DTQL", Text: "x"}); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("blank title: %v", err)
	}
	restricted := covCChatService(t, dir, false)
	if _, err := restricted.Save(ctx, chat.SavedQuerySaveRequest{Title: "t", Type: "DTQL", Text: covCItemDTQL}); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("restricted session: %v", err)
	}
	failing := service
	failing.store = covCPutFailStore{ProjectStore: service.store, revisioned: service.store.(datatug.RevisionedQueriesStore)}
	if _, err := failing.Save(ctx, chat.SavedQuerySaveRequest{Title: "t", Type: "DTQL", Text: covCItemDTQL}); err == nil || !strings.Contains(err.Error(), "put failed") {
		t.Fatalf("put failure: %v", err)
	}
}

func TestCovCChatListErrorsAndMeta(t *testing.T) {
	ctx := context.Background()
	if _, err := (chatSavedQueries{projectDir: "bad\x00dir"}).List(ctx); err == nil {
		t.Fatal("unlistable project directory accepted")
	}
	dir := covCWriteProject(t, covCQueryFiles("f", "q", "DTQL", "dtql", covCItemDTQL, ""))
	boom := errors.New("covC load failed")
	if _, err := (chatSavedQueries{projectDir: dir, store: &covCStore{queryErr: boom}}).List(ctx); !errors.Is(err, boom) {
		t.Fatalf("load failure: %v", err)
	}
	unencodable := &datatug.QueryDef{Parameters: datatug.Parameters{{ID: "p", Type: "string", DefaultValue: func() {}}}}
	if _, err := (chatSavedQueries{projectDir: dir, store: &covCStore{queryDef: unencodable}}).List(ctx); err == nil || !strings.Contains(err.Error(), "encode default") {
		t.Fatalf("unencodable default: %v", err)
	}
	withMeta := &datatug.QueryDef{Parameters: datatug.Parameters{{ID: "p", Type: "string", Meta: &datatug.EntityFieldRef{Entity: "Customer", Field: "Id"}}}}
	listed, err := (chatSavedQueries{projectDir: dir, store: &covCStore{queryDef: withMeta}}).List(ctx)
	if err != nil || len(listed) != 1 || listed[0].Parameters[0].Entity != "Customer" || listed[0].Parameters[0].Field != "Id" {
		t.Fatalf("meta not listed: %+v %v", listed, err)
	}
}

const covCCustomerDTQL = "from: {name: Invoice}\nwhere: {op: '==', left: {field: CustomerId}, right: {param: CustomerId}}\n"

const covCCustomerParams = `,"parameters":[{"id":"CustomerId","type":"int","isRequired":true,"meta":{"entity":"Customer","field":"CustomerId"}}]`

func covCLookupDB(t *testing.T) string {
	t.Helper()
	return covCSQLiteFile(t, "lookup.sqlite",
		`CREATE TABLE Customer(CustomerId INTEGER PRIMARY KEY, Name TEXT)`,
		`CREATE TABLE Invoice(InvoiceId INTEGER PRIMARY KEY, CustomerId INTEGER REFERENCES Customer(CustomerId))`,
		`INSERT INTO Customer VALUES (1,'Ann'),(2,'Ben')`,
		`INSERT INTO Invoice VALUES (10,1),(11,1),(12,2)`)
}

func TestCovCChatLookupParameter(t *testing.T) {
	ctx := context.Background()
	db := covCLookupDB(t)
	dir := covCWriteProject(t, covCMerge(
		covCEnvFiles(map[string]string{"main": db}),
		covCQueryFiles("f", "inv", "DTQL", "dtql", covCCustomerDTQL, covCCustomerParams),
		covCQueryFiles("f", "nolookup", "DTQL", "dtql", "from: {name: Invoice}\nlimit: 5\n", covCCustomerParams),
		covCQueryFiles("f", "sqlq", "SQL", "sql", "select 1", ""),
	))
	service := covCChatService(t, dir, true)

	lookup, err := service.LookupParameter(ctx, "f/inv", "CustomerId")
	if err != nil || lookup == nil || lookup.Key != "CustomerId" || len(lookup.Result.Rows) != 2 {
		t.Fatalf("lookup: %+v %v", lookup, err)
	}
	if got, err := service.LookupParameter(ctx, "f/nolookup", "CustomerId"); got != nil || err != nil {
		t.Fatalf("query without a matching filter: %v %v", got, err)
	}
	if got, err := service.LookupParameter(ctx, "f/inv", "Unknown"); got != nil || err != nil {
		t.Fatalf("unknown parameter: %v %v", got, err)
	}
	if got, err := service.LookupParameter(ctx, "f/sqlq", "x"); got != nil || err != nil {
		t.Fatalf("non-DTQL query: %v %v", got, err)
	}
	if _, err := service.LookupParameter(ctx, "f/absent", "x"); err == nil {
		t.Fatal("unknown query accepted")
	}
	broken := covCWriteProject(t, map[string]string{"queries/f/b.query.json": "{not json"})
	if _, err := covCChatService(t, broken, true).LookupParameter(ctx, "f/b", "x"); err == nil {
		t.Fatal("unloadable query accepted")
	}

	// a restricted executor cannot run the lookup itself
	denied := service
	denied.executor = secureread.NewExecutor(secureread.Session{})
	if _, err := denied.LookupParameter(ctx, "f/inv", "CustomerId"); err == nil {
		t.Fatal("lookup ran without a principal")
	}

	// no environment to resolve the source from
	noEnv := covCWriteProject(t, covCQueryFiles("f", "inv", "DTQL", "dtql", covCCustomerDTQL, covCCustomerParams))
	if _, err := covCChatService(t, noEnv, true).LookupParameter(ctx, "f/inv", "CustomerId"); err == nil {
		t.Fatal("unresolvable source accepted")
	}
}

func TestCovCChatLookupParameterSourceKinds(t *testing.T) {
	ctx := context.Background()
	query := covCQueryFiles("f", "inv", "DTQL", "dtql", covCCustomerDTQL, covCCustomerParams)
	envFor := func(driver, path string) map[string]string {
		return map[string]string{
			"environments/local/local.env.json":             `{"id":"local","dbServers":[{"driver":"` + driver + `","catalogs":["main"]}]}`,
			"environments/local/catalogs/main/main.db.json": `{"driver":"` + driver + `","path":"` + path + `"}`,
		}
	}
	// a non-sqlite source has no foreign-key lookup
	dir := covCWriteProject(t, covCMerge(envFor("ingitdb", "/tmp/covc-ingitdb"), query))
	if got, err := covCChatService(t, dir, true).LookupParameter(ctx, "f/inv", "CustomerId"); got != nil || err != nil {
		t.Fatalf("ingitdb source: %v %v", got, err)
	}
	// a missing sqlite file
	dir = covCWriteProject(t, covCMerge(envFor("sqlite3", filepath.Join(t.TempDir(), "absent.sqlite")), query))
	if _, err := covCChatService(t, dir, true).LookupParameter(ctx, "f/inv", "CustomerId"); err == nil {
		t.Fatal("missing sqlite file accepted")
	}
	// a "%" or a control character in a SQLite path is written into its URL escaped, so
	// that it parses (see api.LocalSQLiteSourceURL): the source of such a file is the file's
	dir = covCWriteProject(t, covCMerge(envFor("sqlite3", `/tmp/covc\u0001/100%/x.sqlite`), query))
	if _, err := covCChatService(t, dir, true).LookupParameter(ctx, "f/inv", "CustomerId"); !errors.Is(err, dbcopy.ErrSourceFileMissing) {
		t.Fatalf("a path with a control character and a percent sign: %v, want the answer for a file that is not there", err)
	}
	// an unparseable source URL: no source of a project is one, so the parse is a seam
	origParse := chatParseSource
	t.Cleanup(func() { chatParseSource = origParse })
	chatParseSource = func(string) (dbcopy.BackendRef, error) {
		return dbcopy.BackendRef{}, errors.New("covC unparseable source")
	}
	if _, err := covCChatService(t, dir, true).LookupParameter(ctx, "f/inv", "CustomerId"); err == nil || err.Error() != "covC unparseable source" {
		t.Fatalf("unparseable source: %v, want the parse error", err)
	}
	chatParseSource = origParse
	// a file that is not a database
	garbage := filepath.Join(t.TempDir(), "garbage.sqlite")
	if err := os.WriteFile(garbage, []byte(strings.Repeat("not a database ", 100)), 0o644); err != nil {
		t.Fatal(err)
	}
	dir = covCWriteProject(t, covCMerge(envFor("sqlite3", garbage), query))
	if _, err := covCChatService(t, dir, true).LookupParameter(ctx, "f/inv", "CustomerId"); err == nil {
		t.Fatal("garbage database accepted")
	}
}

func TestCovCChatLookupParameterSeams(t *testing.T) {
	ctx := context.Background()
	dir := covCWriteProject(t, covCMerge(
		covCEnvFiles(map[string]string{"main": covCLookupDB(t)}),
		covCQueryFiles("f", "inv", "DTQL", "dtql", covCCustomerDTQL, covCCustomerParams),
	))
	service := covCChatService(t, dir, true)

	origOpen, origLoad := chatOpenSQLite, chatLoadForeignKeySnapshot
	t.Cleanup(func() { chatOpenSQLite, chatLoadForeignKeySnapshot = origOpen, origLoad })

	chatOpenSQLite = func(string, string) (*sql.DB, error) { return nil, errors.New("covC open failed") }
	if _, err := service.LookupParameter(ctx, "f/inv", "CustomerId"); err == nil || !strings.Contains(err.Error(), "open failed") {
		t.Fatalf("open failure: %v", err)
	}
	chatOpenSQLite = origOpen

	chatLoadForeignKeySnapshot = func(ctx context.Context, source string, db *sql.DB) (chat.ForeignKeySnapshot, error) {
		return chat.ForeignKeySnapshot{Source: source + "-changed"}, nil
	}
	if _, err := service.LookupParameter(ctx, "f/inv", "CustomerId"); err == nil || !strings.Contains(err.Error(), "source changed") {
		t.Fatalf("changed source: %v", err)
	}
}

func TestCovCChatRunPaths(t *testing.T) {
	ctx := context.Background()
	db := covCLookupDB(t)
	httpQuery := covCQueryFiles("f", "web", "HTTP", "http", "https://example.test/x", "")
	dir := covCWriteProject(t, covCMerge(
		covCEnvFiles(map[string]string{"main": db}),
		covCQueryFiles("f", "inv", "DTQL", "dtql", covCCustomerDTQL, covCCustomerParams),
		covCQueryFiles("f", "sqlq", "SQL", "sql", "SELECT InvoiceId FROM Invoice WHERE CustomerId = @CustomerId", `,"parameters":[{"id":"CustomerId","type":"int","isRequired":true}]`),
		covCQueryFiles("f", "other", "GraphQL", "", "", ""),
		httpQuery,
		map[string]string{"queries/f/broken.query.json": "{not json"},
	))
	service := covCChatService(t, dir, true)
	vars := map[string]string{"CustomerId": "1"}

	dtqlResult, err := service.RunDTQLWithVariables(ctx, "f/inv", vars)
	if err != nil || len(dtqlResult.Result.Rows) != 2 || dtqlResult.DTQL == "" || dtqlResult.SourceID != "main" {
		t.Fatalf("dtql: %+v %v", dtqlResult, err)
	}
	if _, err := service.RunDTQLWithVariables(ctx, "f/sqlq", vars); err == nil || !strings.Contains(err.Error(), "unavailable for browser") {
		t.Fatalf("type gate: %v", err)
	}
	sqlResult, err := service.RunWithVariables(ctx, "f/sqlq", vars)
	if err != nil || len(sqlResult.Result.Rows) != 2 {
		t.Fatalf("sql: %+v %v", sqlResult, err)
	}
	if _, err := service.Run(ctx, "f/inv"); err == nil {
		t.Fatal("DTQL run without its required parameter accepted")
	}
	if _, err := service.Run(ctx, "f/other"); err == nil || !strings.Contains(err.Error(), "not runnable in chat") {
		t.Fatalf("other type: %v", err)
	}
	if _, err := service.Run(ctx, "f/absent"); err == nil {
		t.Fatal("unknown query accepted")
	}
	if _, err := service.Run(ctx, "f/broken"); err == nil {
		t.Fatal("unloadable query accepted")
	}
	if _, err := service.RunDTQLWithVariables(ctx, "f/inv", map[string]string{"bad name": "x"}); err == nil {
		t.Fatal("invalid DTQL parameter name accepted")
	}
	if _, err := service.RunHTTPWithVariables(ctx, "f/web", map[string]string{"bad name": "x"}); err == nil {
		t.Fatal("invalid HTTP parameter name accepted")
	}

	orig := runStructuredHTTPQuery
	t.Cleanup(func() { runStructuredHTTPQuery = orig })
	runStructuredHTTPQuery = func(context.Context, *secureread.Executor, string, dal.Query, map[string]any) (secureread.Result, error) {
		return secureread.Result{Columns: []string{"a"}}, nil
	}
	httpResult, err := service.RunHTTPWithVariables(ctx, "f/web", nil)
	if err != nil || !strings.HasPrefix(httpResult.Source, "http://") {
		t.Fatalf("http: %+v %v", httpResult, err)
	}

	// environment problems surface from both resolution steps
	noCatalogs := covCWriteProject(t, covCMerge(
		map[string]string{"environments/local/local.env.json": `{"id":"local","dbServers":[{"driver":"sqlite3"}]}`},
		covCQueryFiles("f", "inv", "DTQL", "dtql", covCCustomerDTQL, ""),
	))
	if _, err := covCChatService(t, noCatalogs, true).Run(ctx, "f/inv"); err == nil {
		t.Fatal("environment without catalogs accepted")
	}
	absent := covCWriteProject(t, covCMerge(
		covCEnvFiles(map[string]string{"main": db}),
		covCQueryFiles("f", "inv", "DTQL", "dtql", covCCustomerDTQL, `,"targets":[{"catalog":"absent"}]`),
	))
	if _, err := covCChatService(t, absent, true).Run(ctx, "f/inv"); err == nil {
		t.Fatal("unknown target catalog accepted")
	}
}
