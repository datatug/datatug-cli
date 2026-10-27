package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/schemers/dalgoschema"
	"github.com/datatug/datatug-core/pkg/schemer"
	"github.com/spf13/cobra"
)

// couchDBRealFixture is an owned, externally seeded CouchDB 3.5.3 database with
// a least-privilege reader. The shell script scripts/check-journey-couchdb.sh
// starts the pinned container, creates the database and the reader, and exports
// these variables; the test itself seeds native documents and Mango indexes over
// the admin connection, never through the adapter under test.
type couchDBRealFixture struct {
	source        string
	baseURL       string
	database      string
	adminUser     string
	adminPassword string
	readerUser    string
	readerPass    string
}

func requireCouchDBRealFixture(t *testing.T) couchDBRealFixture {
	t.Helper()
	source := os.Getenv("DATATUG_TEST_COUCHDB_URL")
	if source == "" {
		t.Skip("set DATATUG_TEST_COUCHDB_URL (and the admin/reader variables) to an owned disposable CouchDB database")
	}
	parsed, err := url.Parse(source)
	if err != nil || (parsed.Scheme != "couchdb+http" && parsed.Scheme != "couchdb") || parsed.Host == "" {
		t.Fatalf("DATATUG_TEST_COUCHDB_URL must be a CouchDB source URL, not %q", dbcopy.SourceDisplay(source))
	}
	database := strings.TrimPrefix(parsed.Path, "/")
	if database == "" || strings.Contains(database, "/") {
		t.Fatalf("DATATUG_TEST_COUCHDB_URL must name one database")
	}
	scheme := "https"
	if parsed.Scheme == "couchdb+http" {
		scheme = "http"
	}
	fixture := couchDBRealFixture{
		source:        source,
		baseURL:       scheme + "://" + parsed.Host,
		database:      database,
		adminUser:     os.Getenv("DATATUG_TEST_COUCHDB_ADMIN_USER"),
		adminPassword: os.Getenv("DATATUG_TEST_COUCHDB_ADMIN_PASSWORD"),
		readerUser:    os.Getenv("DATATUG_TEST_COUCHDB_READER_USER"),
		readerPass:    os.Getenv("DATATUG_TEST_COUCHDB_READER_PASSWORD"),
	}
	for name, value := range map[string]string{
		"DATATUG_TEST_COUCHDB_ADMIN_USER":      fixture.adminUser,
		"DATATUG_TEST_COUCHDB_ADMIN_PASSWORD":  fixture.adminPassword,
		"DATATUG_TEST_COUCHDB_READER_USER":     fixture.readerUser,
		"DATATUG_TEST_COUCHDB_READER_PASSWORD": fixture.readerPass,
	} {
		if value == "" {
			t.Fatalf("%s is required with DATATUG_TEST_COUCHDB_URL", name)
		}
	}
	return fixture
}

func (f couchDBRealFixture) useReader(t *testing.T) {
	t.Helper()
	t.Setenv(dbcopy.CouchDBUsernameEnv, f.readerUser)
	t.Setenv(dbcopy.CouchDBPasswordEnv, f.readerPass)
}

func (f couchDBRealFixture) adminRequest(t *testing.T, method, path string, body any) map[string]any {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequest(method, f.baseURL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(f.adminUser, f.adminPassword)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode >= 300 {
		t.Fatalf("%s %s: HTTP %d", method, path, response.StatusCode)
	}
	var decoded map[string]any
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &decoded)
	}
	return decoded
}

// seedCouchDB writes native documents and the Mango indexes the journey queries
// rely on. The seed is part of the fixture, not of the adapter: it uses the
// CouchDB HTTP API with the administrator, exactly as an operator would.
func seedCouchDB(t *testing.T, fixture couchDBRealFixture) {
	t.Helper()
	docs := make([]map[string]any, 0, 180)
	for i := 0; i < 150; i++ {
		owner := "alice"
		if i%3 == 0 {
			owner = "bob"
		}
		docs = append(docs, map[string]any{
			"_id": fmt.Sprintf("order-%03d", i), "kind": "order", "status": "active",
			"name": fmt.Sprintf("Order %03d", i), "score": i, "ownerID": owner, "secret": "hidden-" + owner,
		})
	}
	for i := 0; i < 30; i++ {
		docs = append(docs, map[string]any{
			"_id": fmt.Sprintf("archived-%03d", i), "kind": "order", "status": "archived",
			"name": fmt.Sprintf("Archived %03d", i), "score": 1000 + i, "ownerID": "alice", "secret": "hidden-archived",
		})
	}
	fixture.adminRequest(t, http.MethodPost, "/"+fixture.database+"/_bulk_docs", map[string]any{"docs": docs})
	for _, index := range []struct {
		name   string
		fields []string
	}{
		{"status-score-index", []string{"status", "score"}},
		{"score-index", []string{"score"}},
		{"owner-index", []string{"ownerID"}},
	} {
		fixture.adminRequest(t, http.MethodPost, "/"+fixture.database+"/_index", map[string]any{
			"index": map[string]any{"fields": index.fields},
			"name":  index.name,
		})
	}
}

const couchDBJourneyQuery = `from: {name: %s}
columns:
  - {field: _id}
  - {field: name}
  - {field: score}
  - {field: ownerID}
where:
  op: ==
  left: {field: status}
  right: {value: active}
orderBy:
  - {field: score, desc: true}
limit: 120
`

func TestCouchDBQueryJourney(t *testing.T) {
	fixture := requireCouchDBRealFixture(t)
	seedCouchDB(t, fixture)
	fixture.useReader(t)

	// A bounded --from read (--limit is explicit here, so the exploration default
	// is not involved) is still refused by the current adapter for lack of a
	// selector and order a Mango index can serve. This is the adapter's temporary
	// limitation, not a claim that a bounded primary read must fail; it stays
	// explicit rather than falling back to a full scan.
	t.Run("bounded-from-without-filter-or-order-is-refused", func(t *testing.T) {
		stdout, stderr, code := runQuery(t, "", "--db", fixture.source, "--from", fixture.database, "--limit", "5", "--format", "json", "--no-policies")
		if code != exitCodeDatabase {
			t.Fatalf("exit %d, want %d: %s", code, exitCodeDatabase, stderr)
		}
		if strings.TrimSpace(stdout) != "" {
			t.Fatalf("a refused read printed rows: %q", stdout)
		}
		if !strings.Contains(stderr, "CouchDB requires a Mango index") || !strings.Contains(stderr, "full scan") {
			t.Fatalf("stderr must explain the explicit refusal: %q", stderr)
		}
	})

	t.Run("structured-filter-order-and-two-physical-pages", func(t *testing.T) {
		stdout, stderr, code := runQuery(t, fmt.Sprintf(couchDBJourneyQuery, fixture.database), "--db", fixture.source, "-f", "-", "--format", "json", "--no-policies")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		rows := decodeObjects(t, stdout)
		if len(rows) != 120 {
			t.Fatalf("rows = %d, want 120 from two Mango pages", len(rows))
		}
		if rows[0]["score"] != float64(149) || rows[len(rows)-1]["score"] != float64(30) {
			t.Fatalf("unexpected page boundary: first=%v last=%v", rows[0]["score"], rows[len(rows)-1]["score"])
		}
		for i := 1; i < len(rows); i++ {
			if rows[i-1]["score"].(float64) < rows[i]["score"].(float64) {
				t.Fatalf("rows are not ordered: %v then %v", rows[i-1]["score"], rows[i]["score"])
			}
		}
	})

	t.Run("explicit-projection-and-native-id", func(t *testing.T) {
		query := `from: {name: ` + fixture.database + `}
columns:
  - {field: _id}
  - {field: name}
where:
  op: ==
  left: {field: status}
  right: {value: active}
orderBy:
  - {field: score, desc: true}
limit: 2
`
		stdout, stderr, code := runQuery(t, query, "--db", fixture.source, "-f", "-", "--format", "json", "--no-policies")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		rows := decodeObjects(t, stdout)
		if len(rows) != 2 || rows[0]["$key"] != "order-149" || rows[0]["name"] != "Order 149" {
			t.Fatalf("rows = %v", rows)
		}
		for _, row := range rows {
			if _, found := row["secret"]; found {
				t.Fatalf("an undeclared field was returned: %v", row)
			}
			if _, found := row["_rev"]; found {
				t.Fatalf("_rev must not be exposed unless declared: %v", row)
			}
		}
	})

	t.Run("bad-auth-is-safe", func(t *testing.T) {
		t.Setenv(dbcopy.CouchDBUsernameEnv, fixture.readerUser)
		t.Setenv(dbcopy.CouchDBPasswordEnv, "wrong-"+fixture.readerPass)
		_, stderr, code := runQuery(t, fmt.Sprintf(couchDBJourneyQuery, fixture.database), "--db", fixture.source, "-f", "-", "--format", "json", "--no-policies")
		if code != exitCodeDatabase {
			t.Fatalf("exit %d, want %d: %s", code, exitCodeDatabase, stderr)
		}
		if !strings.Contains(stderr, "HTTP 401") {
			t.Fatalf("stderr should report the server's refusal: %q", stderr)
		}
		if strings.Contains(stderr, "wrong-") || strings.Contains(stderr, fixture.readerPass) {
			t.Fatalf("stderr leaks the password: %q", stderr)
		}
	})

	t.Run("ambiguous-credentials-are-safe", func(t *testing.T) {
		t.Setenv(dbcopy.CouchDBUsernameEnv, fixture.readerUser)
		t.Setenv(dbcopy.CouchDBPasswordEnv, fixture.readerPass)
		t.Setenv(dbcopy.CouchDBBearerEnv, "bearer-"+fixture.readerPass)
		_, stderr, code := runQuery(t, fmt.Sprintf(couchDBJourneyQuery, fixture.database), "--db", fixture.source, "-f", "-", "--format", "json", "--no-policies")
		if code != exitCodeDatabase {
			t.Fatalf("exit %d, want %d: %s", code, exitCodeDatabase, stderr)
		}
		if !strings.Contains(stderr, "never both") {
			t.Fatalf("stderr should name the incompatible settings: %q", stderr)
		}
		if strings.Contains(stderr, "bearer-") || strings.Contains(stderr, fixture.readerPass) {
			t.Fatalf("stderr leaks a credential: %q", stderr)
		}
	})

	t.Run("cancellation-is-safe", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, stderr, code := runQueryContext(t, ctx, fmt.Sprintf(couchDBJourneyQuery, fixture.database), "--db", fixture.source, "-f", "-", "--format", "json", "--no-policies")
		if code != exitCodeDatabase {
			t.Fatalf("exit %d, want %d: %s", code, exitCodeDatabase, stderr)
		}
		if !strings.Contains(stderr, "cancelled") && !strings.Contains(stderr, "canceled") {
			t.Fatalf("stderr should report cancellation: %q", stderr)
		}
	})
}

func TestCouchDBSavedQueryAndPolicyJourney(t *testing.T) {
	fixture := requireCouchDBRealFixture(t)
	seedCouchDB(t, fixture)
	fixture.useReader(t)
	projectDir := writeCouchDBProject(t, fixture)

	policy := `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: couch
default: deny
scopes:
  - path: /` + fixture.database + `
    rules:
      - id: own
        effect: allow
        operations: [query]
        where:
          op: "=="
          left: {field: ownerID}
          right: {param: currentUser}
        fields: [name, score, ownerID, status]
`
	if err := os.MkdirAll(filepath.Join(projectDir, "policies"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "policies", "global.yaml"), []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("saved-query-resolves-the-catalog", func(t *testing.T) {
		stdout, stderr, code := runQuery(t, "", "--project", projectDir, "--query", "orders/top", "--env", "local", "--as", "alice", "--format", "json")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		rows := decodeObjects(t, stdout)
		if len(rows) != 3 {
			t.Fatalf("rows = %v", rows)
		}
		for _, row := range rows {
			if row["ownerID"] != "alice" {
				t.Fatalf("a policy denied row was returned: %v", row)
			}
		}
	})

	t.Run("policy-denied-column-is-refused", func(t *testing.T) {
		query := `from: {name: ` + fixture.database + `}
columns:
  - {field: name}
  - {field: secret}
where:
  op: ==
  left: {field: status}
  right: {value: active}
orderBy:
  - {field: score, desc: true}
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
		provider := dalgoschema.NewSchemaProvider(scanDB, nil, "orders", api.ServerDefaultSchema)
		catalog, err := schemer.NewScanner(provider).ScanCatalog(context.Background(), "orders")
		if err != nil {
			t.Fatalf("CouchDB schema scan failed: %v", err)
		}
		if len(catalog.Schemas) != 1 || len(catalog.Schemas[0].Tables) != 1 {
			t.Fatalf("catalog = %#v", catalog)
		}
		table := catalog.Schemas[0].Tables[0]
		if table.Name() != fixture.database {
			t.Fatalf("collection = %q, want %q", table.Name(), fixture.database)
		}
		if len(table.Columns) != 0 {
			t.Fatalf("native CouchDB documents declare no field schema, got %v", table.Columns)
		}
		names := make([]string, 0, len(table.Indexes))
		for _, index := range table.Indexes {
			names = append(names, index.Name)
		}
		if !containsString(names, "status-score-index") {
			t.Fatalf("Mango indexes = %v", names)
		}
	})
}

// writeCouchDBProject writes the minimal project files a saved query needs: the
// catalog records the endpoint (never a credential) and the query names the one
// logical collection.
func writeCouchDBProject(t *testing.T, fixture couchDBRealFixture) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "couch-project")
	for _, folder := range []string{
		filepath.Join(dir, "environments", "local", "catalogs", "orders"),
		filepath.Join(dir, "queries", "orders"),
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
	write("datatug-project.json", `{"id":"couch-project","title":"CouchDB project"}`)
	write("environments/local/local.env.json", `{"id":"local","dbServers":[{"driver":"couchdb","catalogs":["orders"]}]}`)
	write("environments/local/catalogs/orders/orders.db.json", fmt.Sprintf(`{"driver":"couchdb","path":%q,"dbModel":"orders"}`, fixture.source))
	write("queries/orders/top.query.json", `{"id":"orders/top","title":"Top orders","type":"TugQL","createdAt":"2026-01-01T00:00:00Z","targets":[{"catalog":"orders"}]}`)
	write("queries/orders/top.query.tugql.yaml", `from: {name: `+fixture.database+`}
columns:
  - {field: name}
  - {field: score}
  - {field: ownerID}
where:
  op: ==
  left: {field: status}
  right: {value: active}
orderBy:
  - {field: score, desc: true}
limit: 3
`)
	return dir
}

func runQueryContext(t *testing.T, ctx context.Context, stdin string, argv ...string) (stdout, stderr string, code int) {
	t.Helper()
	out, errOut := &strings.Builder{}, &strings.Builder{}
	root := &cobra.Command{Use: "datatug", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(queryCommand())
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{"query", "run"}, argv...))
	err := root.ExecuteContext(ctx)
	if err != nil {
		var coder ExitCoder
		if errors.As(err, &coder) {
			code = coder.ExitCode()
		} else {
			code = 1
		}
		errOut.WriteString(err.Error() + "\n")
	}
	return out.String(), errOut.String(), code
}
