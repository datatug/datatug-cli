package commands

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/schemers/dalgoschema"
	"github.com/datatug/datatug-core/pkg/schemer"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

// postgRESTRealFixture is an owned, externally seeded PostgreSQL 17 + PostgREST
// 16.5 project with least-privilege roles. scripts/check-journey-postgrest.sh
// starts the pinned Compose project, seeds every row through PostgreSQL's init
// hook and mints short-lived read-only JWTs; the test never seeds through the
// adapter under test.
type postgRESTRealFixture struct {
	source         string
	bearer         string
	noaccessBearer string
}

func requirePostgRESTRealFixture(t *testing.T) postgRESTRealFixture {
	t.Helper()
	source := os.Getenv("DATATUG_TEST_POSTGREST_URL")
	if source == "" {
		t.Skip("set DATATUG_TEST_POSTGREST_URL (and the reader tokens) to an owned disposable PostgREST project")
	}
	parsed, err := url.Parse(source)
	if err != nil || !dbcopy.IsPostgRESTScheme(parsed.Scheme) || parsed.Host == "" {
		t.Fatalf("DATATUG_TEST_POSTGREST_URL must be a PostgREST source URL, not %q", dbcopy.SourceDisplay(source))
	}
	fixture := postgRESTRealFixture{
		source:         source,
		bearer:         os.Getenv("DATATUG_TEST_POSTGREST_BEARER"),
		noaccessBearer: os.Getenv("DATATUG_TEST_POSTGREST_NOACCESS_BEARER"),
	}
	if fixture.bearer == "" {
		t.Fatal("DATATUG_TEST_POSTGREST_BEARER is required with DATATUG_TEST_POSTGREST_URL")
	}
	return fixture
}

func (f postgRESTRealFixture) useReader(t *testing.T) {
	t.Helper()
	t.Setenv(dbcopy.PostgRESTBearerEnv, f.bearer)
}

const postgRESTJourneyQuery = `from: {name: items}
columns:
  - {field: title}
  - {field: qty}
  - {field: owner}
where:
  op: '>='
  left: {field: qty}
  right: {value: 10}
orderBy:
  - {field: qty}
limit: 5
`

func TestPostgRESTQueryJourney(t *testing.T) {
	fixture := requirePostgRESTRealFixture(t)
	fixture.useReader(t)

	t.Run("bounded-from-returns-native-rows", func(t *testing.T) {
		stdout, stderr, code := runQuery(t, "", "--db", fixture.source, "--from", "items", "--limit", "4", "--format", "json", "--no-policies")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		rows := decodeObjects(t, stdout)
		if len(rows) != 4 {
			t.Fatalf("rows = %d, want 4: %v", len(rows), rows)
		}
		for i, want := range []string{"1", "2", "3", "4"} {
			if rows[i]["$key"] != want {
				t.Fatalf("row %d key = %v, want %s in id order", i, rows[i]["$key"], want)
			}
		}
		if rows[0]["title"] != "alpha" || rows[0]["owner"] != "alice" || rows[0]["qty"] != float64(10) {
			t.Fatalf("first row = %v", rows[0])
		}
	})

	t.Run("from-without-limit-states-the-default-bound", func(t *testing.T) {
		stdout, stderr, code := runQuery(t, "", "--db", fixture.source, "--from", "items", "--format", "json", "--no-policies")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		rows := decodeObjects(t, stdout)
		if len(rows) != 6 {
			t.Fatalf("rows = %d, want the 6 seeded rows", len(rows))
		}
		if !strings.Contains(stderr, fmt.Sprintf("reading at most %d rows (PostgREST exploration default", postgRESTSourceDefaultLimit)) {
			t.Fatalf("stderr must state the default bound: %q", stderr)
		}
	})

	// The server caps one physical page at two rows and four rows share qty 20:
	// five matching rows can only come back if the adapter continues from
	// Content-Range with the id tie-breaker, never treating a short page as EOF.
	t.Run("structured-filter-order-and-multiple-physical-pages", func(t *testing.T) {
		stdout, stderr, code := runQuery(t, postgRESTJourneyQuery, "--db", fixture.source, "-f", "-", "--format", "json", "--no-policies")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		rows := decodeObjects(t, stdout)
		if len(rows) != 5 {
			t.Fatalf("rows = %d, want 5 across server-capped pages: %v", len(rows), rows)
		}
		wantTitles := []string{"alpha", "beta", "gamma", "delta", "epsilon"}
		for i, want := range wantTitles {
			if rows[i]["title"] != want {
				t.Fatalf("row %d title = %v, want %s", i, rows[i]["title"], want)
			}
			if rows[i]["qty"] != float64([]int{10, 20, 20, 20, 20}[i]) {
				t.Fatalf("row %d qty = %v", i, rows[i]["qty"])
			}
			if _, found := rows[i]["id"]; found {
				t.Fatalf("an undeclared field was returned: %v", rows[i])
			}
			if _, found := rows[i]["secret"]; found {
				t.Fatalf("an undeclared field was returned: %v", rows[i])
			}
		}
	})

	t.Run("explicit-projection-and-native-identity", func(t *testing.T) {
		query := `from: {name: items}
columns:
  - {field: title}
  - {field: owner}
where:
  op: '>='
  left: {field: qty}
  right: {value: 20}
orderBy:
  - {field: title}
limit: 2
`
		stdout, stderr, code := runQuery(t, query, "--db", fixture.source, "-f", "-", "--format", "json", "--no-policies")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		rows := decodeObjects(t, stdout)
		if len(rows) != 2 || rows[0]["title"] != "beta" || rows[0]["$key"] != "2" {
			t.Fatalf("rows = %v", rows)
		}
		for _, row := range rows {
			if _, found := row["id"]; found {
				t.Fatalf("the hidden id must stay the record key, not a column: %v", row)
			}
		}
	})

	t.Run("denied-role-is-safe", func(t *testing.T) {
		if fixture.noaccessBearer == "" {
			t.Skip("set DATATUG_TEST_POSTGREST_NOACCESS_BEARER to prove a role without grants is refused")
		}
		t.Setenv(dbcopy.PostgRESTBearerEnv, fixture.noaccessBearer)
		stdout, stderr, code := runQuery(t, "", "--db", fixture.source, "--from", "items", "--limit", "1", "--format", "json", "--no-policies")
		if code != exitCodeDatabase {
			t.Fatalf("exit %d, want %d: %s", code, exitCodeDatabase, stderr)
		}
		if strings.TrimSpace(stdout) != "" {
			t.Fatalf("a refused read printed rows: %q", stdout)
		}
		if !strings.Contains(stderr, "HTTP 401") && !strings.Contains(stderr, "HTTP 403") {
			t.Fatalf("stderr should report the server's refusal: %q", stderr)
		}
		if strings.Contains(stderr, fixture.noaccessBearer) || strings.Contains(stderr, fixture.source) {
			t.Fatalf("stderr leaks the token or the endpoint: %q", stderr)
		}
	})

	t.Run("bad-auth-is-safe", func(t *testing.T) {
		t.Setenv(dbcopy.PostgRESTBearerEnv, "not-a-valid-jwt")
		_, stderr, code := runQuery(t, postgRESTJourneyQuery, "--db", fixture.source, "-f", "-", "--format", "json", "--no-policies")
		if code != exitCodeDatabase {
			t.Fatalf("exit %d, want %d: %s", code, exitCodeDatabase, stderr)
		}
		if !strings.Contains(stderr, "HTTP 401") {
			t.Fatalf("stderr should report the server's refusal: %q", stderr)
		}
		if strings.Contains(stderr, "not-a-valid-jwt") || strings.Contains(stderr, fixture.source) {
			t.Fatalf("stderr leaks the token or the endpoint: %q", stderr)
		}
	})

	t.Run("unbounded-read-is-refused", func(t *testing.T) {
		stdout, stderr, code := runQuery(t, "", "--db", fixture.source, "--from", "items", "--limit", "0", "--format", "json", "--no-policies")
		if code != exitCodeDatabase {
			t.Fatalf("exit %d, want %d: %s", code, exitCodeDatabase, stderr)
		}
		if strings.TrimSpace(stdout) != "" {
			t.Fatalf("a refused read printed rows: %q", stdout)
		}
		if !strings.Contains(stderr, "positive limit") {
			t.Fatalf("stderr should explain that a positive limit is required: %q", stderr)
		}
	})

	// A local aggregation fallback would ask for unbounded source rows; the
	// adapter refuses it rather than returning a count from a hidden cap.
	t.Run("aggregate-is-refused-not-truncated", func(t *testing.T) {
		query := `from: {name: items}
columns:
  - {field: qty}
  - {aggregate: {function: COUNT, args: [{wildcard: true}]}, as: total}
groupBy: [{field: qty}]
limit: 3
`
		stdout, stderr, code := runQuery(t, query, "--db", fixture.source, "-f", "-", "--format", "json", "--no-policies")
		if code != exitCodeDatabase {
			t.Fatalf("exit %d, want %d: %s", code, exitCodeDatabase, stderr)
		}
		if strings.TrimSpace(stdout) != "" {
			t.Fatalf("a refused aggregate printed a partial result: %q", stdout)
		}
		if !strings.Contains(stderr, "not supported") {
			t.Fatalf("stderr should say the operation is unsupported: %q", stderr)
		}
	})

	t.Run("join-is-refused", func(t *testing.T) {
		query := `from: {name: items, alias: i, joins: [{from: {name: labels, alias: l}, on: [{left: {field: id, source: i}, op: '==', right: {field: id, source: l}}]}]}
columns:
  - {field: title, source: i}
  - {field: name, source: l}
limit: 2
`
		stdout, stderr, code := runQuery(t, query, "--db", fixture.source, "-f", "-", "--format", "json", "--no-policies")
		if code != exitCodeDatabase {
			t.Fatalf("exit %d, want %d: %s", code, exitCodeDatabase, stderr)
		}
		if strings.TrimSpace(stdout) != "" {
			t.Fatalf("a refused join printed rows: %q", stdout)
		}
		if !strings.Contains(stderr, "not supported") {
			t.Fatalf("stderr should say the operation is unsupported: %q", stderr)
		}
	})

	t.Run("cancellation-is-safe", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, stderr, code := runQueryContext(t, ctx, postgRESTJourneyQuery, "--db", fixture.source, "-f", "-", "--format", "json", "--no-policies")
		if code != exitCodeDatabase {
			t.Fatalf("exit %d, want %d: %s", code, exitCodeDatabase, stderr)
		}
		if !strings.Contains(stderr, "cancelled") && !strings.Contains(stderr, "canceled") {
			t.Fatalf("stderr should report cancellation: %q", stderr)
		}
	})

	t.Run("generic-schema-discovery", func(t *testing.T) {
		ref, err := dbcopy.Parse(fixture.source)
		if err != nil {
			t.Fatal(err)
		}
		scanDB, err := ref.OpenSchemaScan(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = scanDB.Close() }()
		provider := dalgoschema.NewSchemaProvider(scanDB, nil, "items", api.ServerDefaultSchema)
		catalog, err := schemer.NewScanner(provider).ScanCatalog(context.Background(), "items")
		if err != nil {
			t.Fatalf("PostgREST schema scan failed: %v", err)
		}
		if len(catalog.Schemas) != 1 {
			t.Fatalf("catalog = %#v", catalog)
		}
		tables := map[string]bool{}
		var itemsColumns []string
		for _, table := range catalog.Schemas[0].Tables {
			tables[table.Name()] = true
			if table.Name() == "items" {
				for _, column := range table.Columns {
					itemsColumns = append(itemsColumns, column.Name)
				}
				if table.PrimaryKey == nil || len(table.PrimaryKey.Columns) != 1 || table.PrimaryKey.Columns[0] != "id" {
					t.Fatalf("items primary key = %#v", table.PrimaryKey)
				}
				if len(table.Indexes) != 0 {
					t.Fatalf("PostgREST exposes no index metadata, got %v", table.Indexes)
				}
			}
		}
		if !tables["items"] || !tables["labels"] {
			t.Fatalf("tables = %v, want items and labels", tables)
		}
		for _, want := range []string{"id", "title", "qty", "owner"} {
			if !containsString(itemsColumns, want) {
				t.Fatalf("items columns = %v, want %s", itemsColumns, want)
			}
		}
	})
}

// postgRESTProjectEndpointEnv is the operator variable a saved PostgREST project
// names through its connection descriptor. The API root itself is never written
// into the project.
const postgRESTProjectEndpointEnv = "DATATUG_TEST_POSTGREST_ITEMS"

func TestPostgRESTSavedQueryAndPolicyJourney(t *testing.T) {
	fixture := requirePostgRESTRealFixture(t)
	fixture.useReader(t)
	t.Setenv(postgRESTProjectEndpointEnv, fixture.source)
	projectDir := writePostgRESTProject(t)

	policy := `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: postgrest
default: deny
scopes:
  - path: /items
    rules:
      - id: own
        effect: allow
        operations: [query]
        where:
          op: "=="
          left: {field: owner}
          right: {param: currentUser}
        fields: [title, qty, owner]
`
	if err := os.MkdirAll(filepath.Join(projectDir, "policies"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "policies", "global.yaml"), []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("saved-project-roundtrips-through-the-core-validator", func(t *testing.T) {
		store, projectID := filestore.NewSingleProjectStore(projectDir, "postgrest-project")
		project, err := store.GetProjectStore(projectID).LoadProject(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err = project.Validate(); err != nil {
			t.Fatalf("the core validator refuses the saved PostgREST project: %v", err)
		}
		found := false
		for _, environment := range project.Environments {
			for _, server := range environment.DbServers {
				if server.Driver == api.DriverPostgREST {
					found = true
				}
			}
		}
		if !found {
			t.Fatal("the saved project records no postgrest server")
		}
	})

	t.Run("saved-query-resolves-the-catalog", func(t *testing.T) {
		stdout, stderr, code := runQuery(t, "", "--project", projectDir, "--query", "items/top", "--env", "local", "--as", "alice", "--format", "json")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		rows := decodeObjects(t, stdout)
		if len(rows) != 3 {
			t.Fatalf("rows = %v", rows)
		}
		for _, row := range rows {
			if row["owner"] != "alice" {
				t.Fatalf("a policy denied row was returned: %v", row)
			}
		}
		if rows[0]["qty"] != float64(20) || rows[2]["qty"] != float64(10) {
			t.Fatalf("rows are not ordered by qty descending: %v", rows)
		}
	})

	t.Run("policy-denied-column-is-refused", func(t *testing.T) {
		query := `from: {name: items}
columns:
  - {field: title}
  - {field: secret}
where:
  op: '>='
  left: {field: qty}
  right: {value: 10}
orderBy:
  - {field: qty, desc: true}
limit: 2
`
		stdout, stderr, code := runQuery(t, query, "--db", fixture.source, "-f", "-", "--policies-dir", filepath.Join(projectDir, "policies"), "--as", "alice", "--format", "json")
		if code != exitCodeAccessDenied {
			t.Fatalf("exit %d, want %d: %s", code, exitCodeAccessDenied, stderr)
		}
		if strings.TrimSpace(stdout) != "" {
			t.Fatalf("a denied query printed rows: %q", stdout)
		}
		if !strings.Contains(stderr, "secret") {
			t.Fatalf("stderr should name the hidden field: %q", stderr)
		}
	})

	t.Run("literal-endpoint-in-a-project-is-refused-before-io", func(t *testing.T) {
		unsafeProject := writePostgRESTProject(t)
		catalogPath := filepath.Join(unsafeProject, "environments", "local", "catalogs", "items", "items.db.json")
		if err := os.WriteFile(catalogPath, []byte(fmt.Sprintf(`{"driver":"postgrest","path":%q,"dbModel":"items"}`, fixture.source)), 0o600); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, code := runQuery(t, "", "--project", unsafeProject, "--query", "items/top", "--env", "local", "--as", "alice", "--format", "json")
		if code == 0 {
			t.Fatalf("a project that records a literal endpoint was accepted: %s", stdout)
		}
		if strings.TrimSpace(stdout) != "" {
			t.Fatalf("a refused project printed rows: %q", stdout)
		}
		if !strings.Contains(stderr, "catalog path is a URL") && !strings.Contains(stderr, "connection descriptor") {
			t.Fatalf("stderr should name the descriptor rule: %q", stderr)
		}
		if strings.Contains(stderr, fixture.bearer) || strings.Contains(stderr, "not-a-valid-jwt") {
			t.Fatalf("stderr leaks a credential: %q", stderr)
		}
	})
}

// writePostgRESTProject writes the minimal project files a saved query needs: the
// catalog records a connection descriptor that names an operator environment
// variable (never the API root or a credential), and the query names one relation
// of the exposed schema.
func writePostgRESTProject(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "postgrest-project")
	for _, folder := range []string{
		filepath.Join(dir, "environments", "local", "catalogs", "items"),
		filepath.Join(dir, "connections", "local"),
		filepath.Join(dir, "queries", "items"),
	} {
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(path)), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("datatug-project.json", `{"id":"postgrest-project","title":"PostgREST project"}`)
	write("environments/local/local.env.json", `{"id":"local","dbServers":[{"driver":"postgrest","catalogs":["items"]}]}`)
	write("connections/local/items.json", `{"dsnEnv":"`+postgRESTProjectEndpointEnv+`"}`)
	write("environments/local/catalogs/items/items.db.json", `{"driver":"postgrest","path":"connections/local/items.json","dbModel":"items"}`)
	write("queries/items/top.query.json", `{"id":"items/top","title":"Top items","type":"TugQL","createdAt":"2026-01-01T00:00:00Z","targets":[{"catalog":"items"}]}`)
	write("queries/items/top.query.tugql.yaml", `from: {name: items}
columns:
  - {field: title}
  - {field: qty}
  - {field: owner}
where:
  op: '>='
  left: {field: qty}
  right: {value: 10}
orderBy:
  - {field: qty, desc: true}
limit: 3
`)
	return dir
}
