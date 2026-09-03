package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/ddl"
	"github.com/dal-go/record"
	"github.com/ingitdb/dalgo2ingitdb"
	"github.com/ingitdb/ingitdb-go/ingitdb/validator"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// globalPolicyExample is the documented policy the feature ships; the tests
// use it verbatim so the example is proven on every run.
const globalPolicyExample = "../../../docs/policies/global.yaml"

const minPricePolicy = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: pricing
default: deny
scopes:
  - path: /products
    rules:
      - id: priced
        effect: allow
        operations: [query]
        where:
          op: ">="
          left: { field: price }
          right: { param: minPrice }
`

const permissivePolicy = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: extra
default: deny
scopes:
  - path: /customers
    rules:
      - id: all-customers
        effect: allow
        operations: [query]
  - path: /products
    rules:
      - id: all-products
        effect: allow
        operations: [query]
`

// setupQueryDB builds a temporary inGitDB database with customers owned by
// alice and bob, and three priced products. It returns the database URL.
func setupQueryDB(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	db, err := dalgo2ingitdb.NewDatabase(root, validator.NewCollectionsReader())
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if closer, ok := db.(io.Closer); ok {
		t.Cleanup(func() { _ = closer.Close() })
	}
	modifier, ok := dal.As[ddl.SchemaModifier](db)
	if !ok {
		t.Fatal("inGitDB must support schema modification")
	}
	ctx := context.Background()
	stringField := func(name string) dbschema.FieldDef {
		return dbschema.FieldDef{Name: dal.FieldName(name), Type: dbschema.String}
	}
	collections := []dbschema.CollectionDef{
		{Name: "customers", Fields: []dbschema.FieldDef{stringField("id"), stringField("name"), stringField("email"), stringField("passwordHash"), stringField("ownerID")}},
		{Name: "products", Fields: []dbschema.FieldDef{stringField("name"), {Name: "price", Type: dbschema.Int}}},
	}
	for _, collection := range collections {
		if err := modifier.CreateCollection(ctx, collection); err != nil {
			t.Fatalf("CreateCollection %s: %v", collection.Name, err)
		}
	}
	records := []record.Record{
		record.NewRecordWithData(record.NewKeyWithID("customers", "c1"), map[string]any{"id": "c1", "name": "Ann", "email": "ann@example.com", "passwordHash": "h1", "ownerID": "alice"}),
		record.NewRecordWithData(record.NewKeyWithID("customers", "c2"), map[string]any{"id": "c2", "name": "Ben", "email": "ben@example.com", "passwordHash": "h2", "ownerID": "alice"}),
		record.NewRecordWithData(record.NewKeyWithID("customers", "c3"), map[string]any{"id": "c3", "name": "Cid", "email": "cid@example.com", "passwordHash": "h3", "ownerID": "bob"}),
		record.NewRecordWithData(record.NewKeyWithID("products", "p1"), map[string]any{"name": "pen", "price": 5}),
		record.NewRecordWithData(record.NewKeyWithID("products", "p2"), map[string]any{"name": "book", "price": 10}),
		record.NewRecordWithData(record.NewKeyWithID("products", "p3"), map[string]any{"name": "lamp", "price": 1000000}),
	}
	if err := db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.InsertMulti(ctx, records)
	}); err != nil {
		t.Fatalf("insert fixture: %v", err)
	}
	return "ingitdb://" + root
}

func policiesDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func globalPolicyDir(t *testing.T) string {
	t.Helper()
	content, err := os.ReadFile(globalPolicyExample)
	if err != nil {
		t.Fatalf("read documented policy: %v", err)
	}
	return policiesDir(t, map[string]string{"global.yaml": string(content)})
}

func runQuery(t *testing.T, stdin string, argv ...string) (stdout, stderr string, code int) {
	t.Helper()
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	root := &cobra.Command{Use: "datatug", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(queryCommand())
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{"query", "run"}, argv...))
	err := root.ExecuteContext(context.Background())
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

func decodeObjects(t *testing.T, stdout string) []map[string]any {
	t.Helper()
	var objects []map[string]any
	if err := json.Unmarshal([]byte(stdout), &objects); err != nil {
		t.Fatalf("stdout is not a JSON array: %v\n%s", err, stdout)
	}
	return objects
}

func TestQuery_FromCollectionWithoutPolicies(t *testing.T) {
	url := setupQueryDB(t)
	stdout, stderr, code := runQuery(t, "", "--db", url, "--from", "products", "--format", "json", "--no-policies")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	objects := decodeObjects(t, stdout)
	if len(objects) != 3 {
		t.Fatalf("products = %v", objects)
	}
	for _, object := range objects {
		if object["$key"] == "" || object["name"] == nil || object["price"] == nil {
			t.Errorf("object %v lacks $key/name/price", object)
		}
	}
	if !strings.Contains(stderr, "running without access policies") {
		t.Errorf("stderr must warn about running unrestricted: %q", stderr)
	}
}

func TestQuery_DTQLFromStdin(t *testing.T) {
	url := setupQueryDB(t)
	dtql := "from:\n  name: products\ncolumns:\n  - field: name\nwhere:\n  op: '>='\n  left: { field: price }\n  right: { value: 10 }\n"
	stdout, stderr, code := runQuery(t, dtql, "--db", url, "-f", "-", "--format", "json", "--no-policies")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	objects := decodeObjects(t, stdout)
	if len(objects) != 2 {
		t.Fatalf("priced products = %v", objects)
	}
	for _, object := range objects {
		if _, ok := object["price"]; ok || object["name"] == nil || object["$key"] == nil {
			t.Errorf("object %v must carry $key and name only", object)
		}
	}
}

func TestQuery_UsageErrors(t *testing.T) {
	url := setupQueryDB(t)
	cases := map[string][]string{
		"neither input":          {"--db", url},
		"both inputs":            {"--db", url, "--from", "products", "-f", "q.yaml"},
		"missing db":             {"--from", "products"},
		"bad scheme":             {"--db", "mongo://x", "--from", "products"},
		"bad format":             {"--db", url, "--from", "products", "--format", "xml"},
		"bad var":                {"--db", url, "--from", "products", "--var", "novalue", "--no-policies"},
		"missing file":           {"--db", url, "-f", filepath.Join(t.TempDir(), "absent.yaml"), "--no-policies"},
		"empty stdin":            {"--db", url, "-f", "-", "--no-policies"},
		"bad policy file":        {"--db", url, "--from", "products", "--policies-dir", t.TempDir(), "--policy", filepath.Join(t.TempDir(), "absent.yaml")},
		"bad policy dir":         {"--db", url, "--from", "products", "--policies-dir", policiesDir(t, map[string]string{"broken.yaml": "kind: Nonsense\n"})},
		"unsupported var ":       {"--db", url, "--from", "products", "--var", "=x", "--no-policies"},
		"var bad name":           {"--db", url, "--from", "products", "--var", "my-var=1", "--no-policies"},
		"var reserved":           {"--db", url, "--from", "products", "--var", "currentUser=bob", "--no-policies"},
		"var mapping":            {"--db", url, "--from", "products", "--var", "m={a: 1}", "--no-policies"},
		"explicit dir missing":   {"--db", url, "--from", "products", "--policies-dir", filepath.Join(t.TempDir(), "missing")},
		"no policies + policy":   {"--db", url, "--from", "products", "--no-policies", "--policy", filepath.Join(t.TempDir(), "x.yaml")},
		"no policies loaded":     {"--db", url, "--from", "products", "--policies-dir", t.TempDir()},
		"unresolved query param": {"--db", url, "-f", "-", "--no-policies"},
	}
	paramQuery := "from:\n  name: products\nwhere:\n  op: '>='\n  left: { field: price }\n  right: { param: minPrice }\n"
	for name, argv := range cases {
		t.Run(name, func(t *testing.T) {
			stdin := ""
			if name == "unresolved query param" {
				stdin = paramQuery
			}
			stdout, stderr, code := runQuery(t, stdin, argv...)
			if code != 2 {
				t.Errorf("exit = %d, want 2 (%s)", code, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout must stay empty: %q", stdout)
			}
		})
	}
	stdout, stderr, code := runQuery(t, "", "--db", url, "--from", "products", "--policies-dir", policiesDir(t, map[string]string{"broken.yaml": "kind: Nonsense\n"}))
	if code != 2 || !strings.Contains(stderr, "broken.yaml") || stdout != "" {
		t.Errorf("invalid policy: exit %d, stderr %q, stdout %q", code, stderr, stdout)
	}
	_, stderr, code = runQuery(t, paramQuery, "--db", url, "-f", "-", "--no-policies")
	if code != 2 || !strings.Contains(stderr, "minPrice") {
		t.Errorf("unresolved query param: exit %d, stderr %q", code, stderr)
	}
	stdout, stderr, code = runQuery(t, paramQuery, "--db", url, "-f", "-", "--no-policies", "--var", "minPrice=10", "--format", "json")
	if code != 0 || len(decodeObjects(t, stdout)) != 2 {
		t.Errorf("substituted query param: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	_, stderr, code = runQuery(t, "", "--db", url, "--from", "products", "--policies-dir", t.TempDir())
	if code != 2 || !strings.Contains(stderr, "pass --no-policies") {
		t.Errorf("zero policies: exit %d, stderr %q", code, stderr)
	}
}

func TestQuery_UnopenableDatabaseExit4(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-db")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _, code := runQuery(t, "", "--db", "ingitdb://"+file, "--from", "products", "--no-policies")
	if code != 4 || stdout != "" {
		t.Errorf("exit %d stdout %q", code, stdout)
	}
}

func TestQuery_GlobalPolicyFiltersRowsAndFields(t *testing.T) {
	url := setupQueryDB(t)
	dir := globalPolicyDir(t)
	stdout, stderr, code := runQuery(t, "", "--db", url, "--from", "customers", "--policies-dir", dir, "--as", "alice", "--format", "json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	objects := decodeObjects(t, stdout)
	if len(objects) != 2 {
		t.Fatalf("alice must see two customers: %v", objects)
	}
	for _, object := range objects {
		if object["ownerID"] != "alice" {
			t.Errorf("bob's customer leaked: %v", object)
		}
		if _, leaked := object["passwordHash"]; leaked {
			t.Errorf("passwordHash leaked: %v", object)
		}
		for _, want := range []string{"id", "name", "email", "ownerID", "$key"} {
			if _, ok := object[want]; !ok {
				t.Errorf("object %v lacks %s", object, want)
			}
		}
	}
	for _, want := range []string{`policy "global"`, filepath.Join(dir, "global.yaml"), `rule "list-own"`, "allows query on /customers", "where ownerID = $currentUser [currentUser=alice]", "fields [id, name, email, ownerID]"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr %q lacks %q", stderr, want)
		}
	}
	if strings.Contains(stdout, "access:") {
		t.Errorf("report leaked into stdout: %q", stdout)
	}
	// Without policies every row and the hidden field come back, with a warning --quiet cannot hide.
	stdout, stderr, code = runQuery(t, "", "--db", url, "--from", "customers", "--no-policies", "--quiet", "--format", "json")
	if code != 0 || len(decodeObjects(t, stdout)) != 3 || !strings.Contains(stdout, "passwordHash") || !strings.Contains(stderr, "running without access policies") {
		t.Errorf("unrestricted run: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	// Filtering on the hidden field is refused before the database sees the query.
	probe := "from:\n  name: customers\nwhere:\n  op: '=='\n  left: { field: passwordHash }\n  right: { value: h1 }\n"
	stdout, stderr, code = runQuery(t, probe, "--db", url, "-f", "-", "--policies-dir", dir, "--as", "alice")
	if code != 5 || stdout != "" || !strings.Contains(stderr, "passwordHash") {
		t.Errorf("hidden field probe: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	// Selecting the hidden field is denied; aliasing any column is refused.
	selecting := "from:\n  name: customers\ncolumns:\n  - field: name\n  - field: passwordHash\n"
	stdout, stderr, code = runQuery(t, selecting, "--db", url, "-f", "-", "--policies-dir", dir, "--as", "alice", "--format", "csv")
	if code != 5 || stdout != "" || !strings.Contains(stderr, "passwordHash") {
		t.Errorf("select hidden: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	aliased := "from:\n  name: customers\ncolumns:\n  - field: passwordHash\n    as: public_hash\n"
	stdout, stderr, code = runQuery(t, aliased, "--db", url, "-f", "-", "--policies-dir", dir, "--as", "alice", "--format", "csv")
	if code != 2 || stdout != "" || !strings.Contains(stderr, "public_hash") {
		t.Errorf("alias under field list: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	// Requested columns keep their request order (the union would be sorted).
	ordered := "from:\n  name: customers\ncolumns:\n  - field: name\n  - field: email\n"
	stdout, stderr, code = runQuery(t, ordered, "--db", url, "-f", "-", "--policies-dir", dir, "--as", "alice", "--format", "csv")
	if code != 0 || !strings.HasPrefix(stdout, "$key,name,email\n") || strings.Contains(stdout, "passwordHash") {
		t.Errorf("request-order header: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	// A parameter outside a where right-hand side is a usage error.
	byParam := "from:\n  name: customers\norderBy:\n  - param: col\n"
	stdout, stderr, code = runQuery(t, byParam, "--db", url, "-f", "-", "--policies-dir", dir, "--as", "alice", "--var", "col=name")
	if code != 2 || stdout != "" || !strings.Contains(stderr, "$col") {
		t.Errorf("misplaced param: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	// The policies directory can come from the environment.
	t.Setenv("DATATUG_POLICIES_DIR", dir)
	_, stderr, code = runQuery(t, "", "--db", url, "--from", "customers", "--as", "alice", "--format", "json")
	if code != 0 || !strings.Contains(stderr, filepath.Join(dir, "global.yaml")) {
		t.Errorf("env dir: exit %d, stderr %q", code, stderr)
	}
}

func TestQuery_ReportAndQuiet(t *testing.T) {
	url := setupQueryDB(t)
	dir := globalPolicyDir(t)
	_, stderr, code := runQuery(t, "", "--db", url, "--from", "products", "--policies-dir", dir, "--format", "json")
	if code != 0 || !strings.Contains(stderr, `rule "list-all" allows query on /products: no limitations`) {
		t.Errorf("products report: exit %d, stderr %q", code, stderr)
	}
	stdout, stderr, code := runQuery(t, "", "--db", url, "--from", "customers", "--policies-dir", dir, "--as", "alice", "--format", "json", "--quiet")
	if code != 0 || stderr != "" || len(decodeObjects(t, stdout)) != 2 {
		t.Errorf("quiet run: exit %d, stderr %q, stdout %q", code, stderr, stdout)
	}
}

func TestQuery_DeniedExit5(t *testing.T) {
	url := setupQueryDB(t)
	dir := globalPolicyDir(t)
	stdout, stderr, code := runQuery(t, "", "--db", url, "--from", "customers", "--policies-dir", dir, "--format", "json")
	if code != 5 || stdout != "" || !strings.Contains(stderr, "currentUser") {
		t.Errorf("denied run: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestQuery_PoliciesDiscoveredInOrderAndAdded(t *testing.T) {
	url := setupQueryDB(t)
	global, err := os.ReadFile(globalPolicyExample)
	if err != nil {
		t.Fatal(err)
	}
	dir := policiesDir(t, map[string]string{"10-global.yaml": string(global), "20-extra.yml": permissivePolicy, "notes.txt": "ignored"})
	_, stderr, code := runQuery(t, "", "--db", url, "--from", "products", "--policies-dir", dir, "--format", "json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	first, second := strings.Index(stderr, filepath.Join(dir, "10-global.yaml")), strings.Index(stderr, filepath.Join(dir, "20-extra.yml"))
	if first < 0 || second < 0 || first > second || strings.Contains(stderr, "notes.txt") {
		t.Errorf("report order: %q", stderr)
	}
	// --policy adds a document after an empty directory; variables are typed.
	pricing := filepath.Join(t.TempDir(), "pricing.yaml")
	if err := os.WriteFile(pricing, []byte(minPricePolicy), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runQuery(t, "", "--db", url, "--from", "products", "--policies-dir", t.TempDir(), "--policy", pricing, "--var", "minPrice=10", "--format", "json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	objects := decodeObjects(t, stdout)
	if len(objects) != 2 {
		t.Errorf("priced products = %v", objects)
	}
	if !strings.Contains(stderr, "where price >= $minPrice [minPrice=10]") || !strings.Contains(stderr, pricing) {
		t.Errorf("report %q lacks the typed variable or the source", stderr)
	}
	// An absent default directory needs --no-policies.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DATATUG_POLICIES_DIR", "")
	stdout, stderr, code = runQuery(t, "", "--db", url, "--from", "products", "--format", "json")
	if code != 2 || stdout != "" || !strings.Contains(stderr, "pass --no-policies") {
		t.Errorf("absent default dir: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	stdout, stderr, code = runQuery(t, "", "--db", url, "--from", "products", "--format", "json", "--no-policies")
	if code != 0 || len(decodeObjects(t, stdout)) != 3 || !strings.Contains(stderr, "running without access policies") {
		t.Errorf("absent default dir + --no-policies: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestQuery_Formats(t *testing.T) {
	url := setupQueryDB(t)
	dir := globalPolicyDir(t)
	base := []string{"--db", url, "--from", "customers", "--policies-dir", dir, "--as", "alice", "--quiet", "--format"}
	stdout, stderr, code := runQuery(t, "", append(base, "csv")...)
	if code != 0 {
		t.Fatalf("csv exit %d: %s", code, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 3 || lines[0] != "$key,email,id,name,ownerID" {
		t.Errorf("csv = %q", stdout)
	}
	// Integers render plainly, never in exponent form.
	for _, format := range []string{"csv", "grid", "yaml", "json"} {
		stdout, _, code := runQuery(t, "", "--db", url, "--from", "products", "--policies-dir", dir, "--quiet", "--format", format)
		if code != 0 || !strings.Contains(stdout, "1000000") || strings.Contains(stdout, "e+06") {
			t.Errorf("%s numbers = %q", format, stdout)
		}
	}
	stdout, _, _ = runQuery(t, "", append(base, "jsonl")...)
	lines = strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 2 {
		t.Errorf("jsonl = %q", stdout)
	}
	for _, line := range lines {
		var object map[string]any
		if err := json.Unmarshal([]byte(line), &object); err != nil || object["$key"] == nil {
			t.Errorf("jsonl line %q: %v", line, err)
		}
	}
	stdout, _, _ = runQuery(t, "", append(base, "grid")...)
	lines = strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "$key") || !strings.Contains(lines[0], "ownerID") {
		t.Errorf("grid = %q", stdout)
	}
	stdout, _, _ = runQuery(t, "", append(base, "yaml")...)
	var documents []map[string]any
	if err := yaml.Unmarshal([]byte(stdout), &documents); err != nil || len(documents) != 2 || documents[0]["$key"] == nil || documents[0]["passwordHash"] != nil {
		t.Errorf("yaml = %q (%v)", stdout, err)
	}
}

func TestQueryOutputHelpers(t *testing.T) {
	rows := []queryRow{{key: "k1", data: map[string]any{"b": float64(1), "a": "x", "n": nil, "m": map[string]any{"z": float64(1000000)}, "l": []any{float64(1), 2.5}, "f": 1.5, "t": true}}}
	columns, err := rowColumns(dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("c", ""))).SelectColumns(), rows)
	if err != nil || strings.Join(columns, ",") != "a,b,f,l,m,n,t" {
		t.Errorf("columns = %v, %v", columns, err)
	}
	aliased := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("c", ""))).SelectColumns(dal.Column{Expression: dal.Field("x"), Alias: "b"}, dal.Column{Expression: dal.Field("a")}, dal.Column{Expression: dal.Field("gone")}, dal.Column{Expression: dal.Field("a")})
	if columns, err := rowColumns(aliased, rows); err != nil || strings.Join(columns, ",") != "b,a" {
		t.Errorf("aliased columns = %v, %v", columns, err)
	}
	onlyGone := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("c", ""))).SelectColumns(dal.Column{Expression: dal.Field("gone")})
	if columns, err := rowColumns(onlyGone, rows); err != nil || len(columns) != 0 {
		t.Errorf("no survivors must not widen: %v, %v", columns, err)
	}
	if rows, err := collectRows(nil); err != nil || rows != nil {
		t.Errorf("nil reader = %v, %v", rows, err)
	}
	if _, err := rowColumns(aliased, []queryRow{{key: "k", data: map[string]any{"$key": 1}}}); !errors.Is(err, errReservedKeyField) {
		t.Errorf("reserved key field = %v", err)
	}
	var out bytes.Buffer
	if err := writeQueryRows(&out, "csv", []string{"a", "b", "m", "l", "n", "f", "t"}, rows); err != nil || !strings.Contains(out.String(), `k1,x,1,"{""z"":1000000}","[1,2.5]",,1.5,true`) {
		t.Errorf("csv = %q, %v", out.String(), err)
	}
	out.Reset()
	if err := writeQueryRows(&out, "grid", []string{"a"}, nil); err != nil || out.String() != "$key  a\n" {
		t.Errorf("empty grid = %q, %v", out.String(), err)
	}
	out.Reset()
	if err := writeQueryRows(&out, "yaml", nil, nil); err != nil || out.String() != "[]\n" {
		t.Errorf("empty yaml = %q, %v", out.String(), err)
	}
	out.Reset()
	if err := writeQueryRows(&out, "yaml", []string{"a", "b", "m", "l"}, rows); err != nil || !strings.HasPrefix(out.String(), "- $key: k1\n  a: x\n  b: 1\n  m:\n    z: 1000000\n  l:\n    - 1\n    - 2.5\n") {
		t.Errorf("yaml = %q, %v", out.String(), err)
	}
	out.Reset()
	if err := writeQueryRows(&out, "json", nil, nil); err != nil || out.String() != "[\n]\n" {
		t.Errorf("empty json = %q, %v", out.String(), err)
	}
	if err := writeQueryRows(&out, "xml", nil, nil); err == nil {
		t.Error("unsupported format must fail")
	}
}
