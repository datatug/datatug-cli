package commands

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

// libSQLRealFixture is an owned, externally seeded libSQL server. The published
// adapter's scripts/libsql-fixture.mjs starts one pinned container, mints an
// ephemeral Ed25519 read-only token, seeds every row and native type through the
// writer token, and proves the reader token cannot write (403).
// scripts/check-journey-libsql.sh runs this file and the built CLI against it.
// The test never seeds through the adapter under test and never sees a writer
// token.
// libSQLTestTransport gives the libSQL adapter a plain *http.Transport. The
// adapter clones http.DefaultTransport, and this package's test binary wraps that
// transport to refuse the live demo site (never_dial_demodb_test.go); the wrapper
// is an artifact of the test binary, since a real CLI process has a plain
// transport. Nothing is dialled by this substitution itself.
func libSQLTestTransport(t *testing.T) {
	t.Helper()
	original := http.DefaultTransport
	http.DefaultTransport = &http.Transport{}
	t.Cleanup(func() { http.DefaultTransport = original })
}

type libSQLRealFixture struct {
	source string
	bearer string
}

func requireLibSQLRealFixture(t *testing.T) libSQLRealFixture {
	t.Helper()
	libSQLTestTransport(t)
	source := os.Getenv("DATATUG_TEST_LIBSQL_URL")
	if source == "" {
		t.Skip("set DATATUG_TEST_LIBSQL_URL (and DATATUG_LIBSQL_BEARER) to an owned disposable libSQL server")
	}
	parsed, err := url.Parse(source)
	if err != nil || !dbcopy.IsLibSQLScheme(parsed.Scheme) || parsed.Host == "" {
		t.Fatalf("DATATUG_TEST_LIBSQL_URL must be a libSQL source URL, not %q", dbcopy.SourceDisplay(source))
	}
	fixture := libSQLRealFixture{source: source, bearer: os.Getenv(dbcopy.LibSQLBearerEnv)}
	if fixture.bearer == "" {
		t.Fatal(dbcopy.LibSQLBearerEnv + " is required with DATATUG_TEST_LIBSQL_URL")
	}
	return fixture
}

func TestLibSQLQueryJourney(t *testing.T) {
	fixture := requireLibSQLRealFixture(t)
	t.Setenv(dbcopy.LibSQLBearerEnv, fixture.bearer)

	t.Run("bounded-from-returns-native-rows-in-key-order", func(t *testing.T) {
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
		if rows[0]["title"] != "Alpha" || rows[0]["score"] != 9.5 {
			t.Fatalf("first row = %v", rows[0])
		}
	})

	t.Run("from-without-limit-states-the-default-bound", func(t *testing.T) {
		stdout, stderr, code := runQuery(t, "", "--db", fixture.source, "--from", "items", "--format", "json", "--no-policies")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		if rows := decodeObjects(t, stdout); len(rows) != 12 {
			t.Fatalf("rows = %d, want the 12 seeded rows", len(rows))
		}
		if !strings.Contains(stderr, "libSQL exploration default") {
			t.Fatalf("stderr must state the default bound: %q", stderr)
		}
	})

	t.Run("explicit-key-order-and-offset-page", func(t *testing.T) {
		stdout, stderr, code := runQuery(t, "", "--db", fixture.source, "--from", "items", "--limit", "3", "--offset", "2", "--format", "json", "--no-policies")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		rows := decodeObjects(t, stdout)
		wantKeys := []string{"3", "4", "5"}
		if len(rows) != len(wantKeys) {
			t.Fatalf("rows = %d, want %d", len(rows), len(wantKeys))
		}
		for i, want := range wantKeys {
			if rows[i]["$key"] != want {
				t.Fatalf("row %d key = %v, want %s", i, rows[i]["$key"], want)
			}
		}
	})

	// A view has no declared primary key, so a scan never records it (the
	// adapter has no ViewLister and its strict DescribeCollection refuses a
	// view) and --from cannot page it in key order. A structured read that names
	// it and orders explicitly still works: the fixture's items_view is
	// SELECT id, title, rank FROM items, so LIMIT 2 in id order is the same two
	// first rows.
	t.Run("structured-read-of-a-view-orders-explicitly", func(t *testing.T) {
		query := `from: {name: items_view}
columns:
  - {field: id}
  - {field: title}
orderBy:
  - {field: id}
limit: 2
`
		stdout, stderr, code := runQuery(t, query, "--db", fixture.source, "-f", "-", "--format", "json", "--no-policies")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		var rows []map[string]any
		if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
			t.Fatalf("view output is not JSON: %v: %s", err, stdout)
		}
		if len(rows) != 2 {
			t.Fatalf("view rows = %d, want 2: %v", len(rows), rows)
		}
		for i, wantID := range []float64{1, 2} {
			if rows[i]["id"] != wantID {
				t.Fatalf("view row %d id = %v, want %v in id order: %v", i, rows[i]["id"], wantID, rows)
			}
			if _, found := rows[i]["rank"]; found {
				t.Fatalf("a column the view query did not select was returned: %v", rows[i])
			}
		}
		if rows[0]["title"] != "Alpha" {
			t.Fatalf("view first row = %v", rows[0])
		}
	})

	t.Run("structured-filter-projection-and-native-aggregate", func(t *testing.T) {
		query := `from: {name: items}
columns:
  - {field: title}
  - {field: rank}
  - {field: score}
where:
  op: '>='
  left: {field: rank}
  right: {value: 2}
orderBy:
  - {field: rank}
  - {field: id}
limit: 4
`
		stdout, stderr, code := runQuery(t, query, "--db", fixture.source, "-f", "-", "--format", "json", "--no-policies")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		rows := decodeObjects(t, stdout)
		if len(rows) != 4 {
			t.Fatalf("rows = %d, want 4: %v", len(rows), rows)
		}
		for _, row := range rows {
			if _, found := row["payload"]; found {
				t.Fatalf("an undeclared column was returned: %v", row)
			}
		}

		aggregate := `from: {name: items}
columns:
  - {field: rank}
  - {aggregate: {function: count, args: [{star: true}]}, as: total}
  - {aggregate: {function: max, args: [{field: id}]}, as: last_id}
where:
  op: '>='
  left: {field: rank}
  right: {value: 1}
groupBy: [{field: rank}]
orderBy: [{field: rank}]
limit: 10
`
		stdout, stderr, code = runQuery(t, aggregate, "--db", fixture.source, "-f", "-", "--format", "json", "--no-policies")
		if code != 0 {
			t.Fatalf("aggregate exit %d: %s", code, stderr)
		}
		rows = decodeObjects(t, stdout)
		if len(rows) != 3 {
			t.Fatalf("aggregate rows = %d, want 3 ranks: %v", len(rows), rows)
		}
		if rows[0]["total"] != float64(3) || rows[0]["last_id"] != float64(10) {
			t.Fatalf("rank 1 aggregate = %v", rows[0])
		}
	})

	// The whole point of the adapter's PreserveNativeValues: an int64 beyond
	// 2^53 keeps every digit, a blob arrives as its own value, and one column
	// holds several storage classes across rows.
	t.Run("native-values-keep-int64-fraction-blob-and-null", func(t *testing.T) {
		stdout, stderr, code := runQuery(t, "", "--db", fixture.source, "--from", "bigints", "--format", "json", "--no-policies")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		for _, want := range []string{"9223372036854775807", "-9223372036854775808", "9007199254740993"} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("output lost the exact int64 %s: %s", want, stdout)
			}
		}

		stdout, stderr, code = runQuery(t, "", "--db", fixture.source, "--from", "blobs", "--format", "json", "--no-policies")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		var blobs []map[string]any
		if err := json.Unmarshal([]byte(stdout), &blobs); err != nil {
			t.Fatalf("blobs output is not JSON: %v: %s", err, stdout)
		}
		if blobs[0]["payload"] != "AP8Q" || blobs[1]["payload"] != nil || blobs[2]["payload"] != "" {
			t.Fatalf("blob/null/empty-blob values = %v", blobs)
		}

		stdout, stderr, code = runQuery(t, "", "--db", fixture.source, "--from", "mixed", "--format", "json", "--no-policies")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		var mixed []map[string]any
		if err := json.Unmarshal([]byte(stdout), &mixed); err != nil {
			t.Fatalf("mixed output is not JSON: %v: %s", err, stdout)
		}
		classes := map[string]bool{}
		for _, row := range mixed {
			switch row["declared_integer"].(type) {
			case string:
				classes["text"] = true
			case float64:
				classes["number"] = true
			case nil:
				classes["null"] = true
			default:
				t.Fatalf("unexpected storage class for declared_integer: %T", row["declared_integer"])
			}
		}
		if !classes["text"] || !classes["number"] || !classes["null"] {
			t.Fatalf("one column did not keep its mixed classes: %v", classes)
		}
	})

	t.Run("bad-auth-is-refused-without-the-token-or-endpoint", func(t *testing.T) {
		t.Setenv(dbcopy.LibSQLBearerEnv, "not-a-valid-token")
		stdout, stderr, code := runQuery(t, "", "--db", fixture.source, "--from", "items", "--limit", "1", "--format", "json", "--no-policies")
		if code != exitCodeDatabase {
			t.Fatalf("exit %d, want %d: %s", code, exitCodeDatabase, stderr)
		}
		if strings.TrimSpace(stdout) != "" {
			t.Fatalf("a refused read printed rows: %q", stdout)
		}
		if !strings.Contains(stderr, "libSQL read failed") || !strings.Contains(stderr, "DATATUG_LIBSQL_BEARER") {
			t.Fatalf("stderr should classify the refusal with the safe auth hint: %q", stderr)
		}
		if strings.Contains(stderr, "not-a-valid-token") || strings.Contains(stderr, "127.0.0.1") {
			t.Fatalf("the refusal leaked the token or the endpoint: %q", stderr)
		}
	})

	t.Run("write-is-refused-before-io", func(t *testing.T) {
		source := filepath.Join(t.TempDir(), "empty.db")
		if err := os.WriteFile(source, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err := runCopy(t, "db", "copy", "--from", "sqlite://"+source, "--to", fixture.source)
		if err == nil {
			t.Fatal("a copy into libSQL was accepted")
		}
		if !strings.Contains(err.Error(), "read-only") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestLibSQLSavedQueryAndPolicyJourney(t *testing.T) {
	fixture := requireLibSQLRealFixture(t)
	t.Setenv(dbcopy.LibSQLBearerEnv, fixture.bearer)

	projectDir := t.TempDir()
	write := func(relative, content string) {
		path := filepath.Join(projectDir, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("datatug-project.json", `{"id":"libsql-project","title":"libSQL project","access":"private"}`)
	write("environments/local/local.env.json", `{"id":"local","dbServers":[{"driver":"libsql","catalogs":["main"]}]}`)
	write("connections/local/main.json", `{"dsnEnv":"DATATUG_TEST_LIBSQL_URL"}`)
	write("environments/local/catalogs/main/main.db.json", `{"driver":"libsql","path":"connections/local/main.json","dbModel":"main"}`)
	write("queries/items/by-rank.query.json", `{"id":"items/by-rank","title":"Items by rank","type":"TugQL","createdAt":"2026-01-01T00:00:00Z","targets":[{"catalog":"main"}]}`)
	write("queries/items/by-rank.query.tugql.yaml", `from: {name: items}
columns:
  - {field: title}
  - {field: rank}
  - {field: code}
where:
  op: '>='
  left: {field: rank}
  right: {value: 2}
orderBy:
  - {field: rank}
  - {field: code}
limit: 4
`)
	write("policies/global.yaml", `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: libsql
default: deny
scopes:
  - path: /items
    rules:
      - id: own
        effect: allow
        operations: [query]
        where:
          op: "=="
          left: {field: code}
          right: {param: currentUser}
        fields: [title, rank, code]
`)

	stdout, stderr, code := runQuery(t, "", "--project", projectDir, "--query", "items/by-rank", "--env", "local", "--as", "a", "--format", "json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	rows := decodeObjects(t, stdout)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want the one owned row: %v", len(rows), rows)
	}
	if rows[0]["code"] != "a" || rows[0]["title"] != "Alpha" {
		t.Fatalf("row = %v", rows[0])
	}
	for _, hidden := range []string{"score", "payload", "secret"} {
		if _, found := rows[0][hidden]; found {
			t.Fatalf("a denied column reached the result: %v", rows[0])
		}
	}
	// The saved project holds the descriptor and the variable name, never the
	// endpoint or the token.
	for _, file := range []string{"environments/local/catalogs/main/main.db.json", "connections/local/main.json"} {
		body, err := os.ReadFile(filepath.Join(projectDir, file))
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"127.0.0.1", fixture.bearer} {
			if strings.Contains(string(body), forbidden) {
				t.Fatalf("%s holds %q", file, forbidden)
			}
		}
	}

	// A scan must persist SQLite's own "main" schema, and the project it writes
	// must serve a saved query through the scanned descriptor with a bare
	// /items policy. This is the persisted-path assertion: default.items would
	// neither execute nor match the policy.
	t.Run("scan-writes-main-and-serves-a-policy-bound-query", func(t *testing.T) {
		t.Cleanup(api.SetOpenSchemaScanForTest(dbcopy.BackendRef.OpenSchemaScan))
		scanned := t.TempDir()
		if err := covDRunScan("-d", scanned, "-D", "libsql", "--dsn-env", "DATATUG_TEST_LIBSQL_URL", "--env", "local", "--db", "main"); err != nil {
			t.Fatalf("scan: %v", err)
		}
		columnsPath := filepath.Join(scanned, "dbmodels", "main", "main", "tables", "items", "main.items.columns.json")
		if _, err := os.Stat(columnsPath); err != nil {
			t.Fatalf("the scan did not persist main.items: %v", err)
		}
		if _, err := os.Stat(filepath.Join(scanned, "dbmodels", "main", "default")); err == nil {
			t.Fatal("the scan persisted the fixed default schema instead of SQLite's main")
		}
		writeScanned := func(relative, content string) {
			path := filepath.Join(scanned, relative)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		writeScanned("queries/items/scanned-items.query.json", `{"id":"items/scanned-items","title":"Scanned items","type":"TugQL","createdAt":"2026-01-01T00:00:00Z","targets":[{"catalog":"main"}]}`)
		// The scan persisted main.items. The query names the collection
		// unqualified, which libSQL resolves to that relation, because DALgo's
		// access layer treats every schema-qualified source as an opaque-query
		// resource that a /items path policy cannot match. `name: main.items`
		// would ask SQLite for one table literally named "main.items".
		writeScanned("queries/items/scanned-items.query.tugql.yaml", "from: {name: items}\ncolumns:\n  - {field: title}\n  - {field: rank}\n  - {field: code}\norderBy:\n  - {field: rank}\n  - {field: code}\nlimit: 10\n")
		writeScanned("policies/global.yaml", `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: libsql-scanned
default: deny
scopes:
  - path: /items
    rules:
      - id: own
        effect: allow
        operations: [query]
        where:
          op: "=="
          left: {field: code}
          right: {param: currentUser}
        fields: [title, rank, code]
`)
		stdout, stderr, code := runQuery(t, "", "--project", scanned, "--query", "items/scanned-items", "--env", "local", "--as", "a", "--format", "json")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		rows := decodeObjects(t, stdout)
		if len(rows) != 1 || rows[0]["code"] != "a" || rows[0]["title"] != "Alpha" {
			t.Fatalf("scanned-project rows = %v, want the one allowed row", rows)
		}
		for _, hidden := range []string{"score", "payload", "secret"} {
			if _, found := rows[0][hidden]; found {
				t.Fatalf("a denied column reached the scanned-project result: %v", rows[0])
			}
		}
	})
}
