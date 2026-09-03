package accesspolicies

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
)

const testPolicy = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: %s
default: deny
scopes:
  - path: /customers
    rules:
      - id: list-own
        effect: allow
        operations: [query]
        where:
          op: "=="
          left: { field: ownerID }
          right: { param: currentUser }
        fields: [id, name, address.*, public_*]
  - path: /products
    rules:
      - id: list-all
        effect: allow
        operations: [query]
`

const boundPolicy = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: bound
default: deny
ruleSets:
  readers:
    - path: /products
      rules:
        - id: list
          effect: allow
          operations: [query]
bindings:
  roles:
    reader: [readers]
`

func writePolicy(t *testing.T, dir, name, policyName string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(strings.Replace(testPolicy, "%s", policyName, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveDir(t *testing.T) {
	if dir, explicit, err := ResolveDir("/explicit"); err != nil || dir != "/explicit" || !explicit {
		t.Errorf("explicit = %q, %v, %v", dir, explicit, err)
	}
	t.Setenv(DirEnv, "/from-env")
	if dir, explicit, err := ResolveDir(""); err != nil || dir != "/from-env" || !explicit {
		t.Errorf("env = %q, %v, %v", dir, explicit, err)
	}
	t.Setenv(DirEnv, "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	if dir, explicit, err := ResolveDir(""); err != nil || dir != filepath.Join(home, ".datatug", "policies") || explicit {
		t.Errorf("default = %q, %v, %v", dir, explicit, err)
	}
}

func TestLoadDirOrderAndFiltering(t *testing.T) {
	dir := t.TempDir()
	writePolicy(t, dir, "20-extra.yml", "extra")
	writePolicy(t, dir, "10-global.yaml", "global")
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a policy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.yaml"), 0o700); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, item := range loaded {
		names = append(names, item.Policy.Name())
		if !strings.HasPrefix(item.Source, dir) {
			t.Errorf("source = %q", item.Source)
		}
	}
	if want := []string{"global", "extra"}; !reflect.DeepEqual(names, want) {
		t.Errorf("loaded %v, want %v", names, want)
	}
	if len(Policies(loaded)) != 2 {
		t.Errorf("Policies = %d", len(Policies(loaded)))
	}
	if _, err := LoadDir(filepath.Join(dir, "notes.txt")); err == nil {
		t.Error("a file is not a directory")
	}
}

func TestLoadRules(t *testing.T) {
	t.Setenv(DirEnv, "")
	dir := t.TempDir()
	global := writePolicy(t, dir, "global.yaml", "global")
	extra := writePolicy(t, t.TempDir(), "extra.yaml", "extra")
	// Directory then --policy files, in order.
	loaded, err := Load(LoadOptions{Dir: dir, Files: []string{extra}})
	if err != nil || len(loaded) != 2 || loaded[0].Source != global || loaded[1].Source != extra {
		t.Errorf("dir + file = %v, %v", loaded, err)
	}
	// --no-policies skips discovery; with --policy it is an error.
	if loaded, err := Load(LoadOptions{Dir: dir, None: true}); err != nil || loaded != nil {
		t.Errorf("none = %v, %v", loaded, err)
	}
	if _, err := Load(LoadOptions{None: true, Files: []string{extra}}); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("none + file = %v", err)
	}
	// An explicit missing directory is an error; the default missing one is empty.
	missing := filepath.Join(dir, "missing")
	if _, err := Load(LoadOptions{Dir: missing}); err == nil || !strings.Contains(err.Error(), missing) {
		t.Errorf("explicit missing = %v", err)
	}
	t.Setenv(DirEnv, missing)
	if _, err := Load(LoadOptions{}); err == nil || !strings.Contains(err.Error(), missing) {
		t.Errorf("env missing = %v", err)
	}
	t.Setenv(DirEnv, "")
	t.Setenv("HOME", t.TempDir())
	if _, err := Load(LoadOptions{}); !errors.Is(err, ErrNoPolicies) {
		t.Errorf("default missing without --no-policies = %v", err)
	}
	if loaded, err := Load(LoadOptions{Files: []string{extra}}); err != nil || len(loaded) != 1 {
		t.Errorf("default missing + file = %v, %v", loaded, err)
	}
	if _, err := Load(LoadOptions{Dir: t.TempDir()}); !errors.Is(err, ErrNoPolicies) {
		t.Errorf("empty explicit dir = %v", err)
	}
}

func TestLoadFileErrors(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("kind: Nonsense\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(bad); err == nil || !strings.Contains(err.Error(), bad) {
		t.Errorf("bad document error = %v", err)
	}
	if _, err := LoadFile(filepath.Join(dir, "absent.json")); err == nil {
		t.Error("absent file must fail")
	}
	if _, err := LoadFile(filepath.Join(dir, "policy.txt")); err == nil || !strings.Contains(err.Error(), "unsupported extension") {
		t.Errorf("extension error = %v", err)
	}
	if _, err := LoadDir(dir); err == nil {
		t.Error("a directory with a bad document must fail")
	}
}

func TestParseVariables(t *testing.T) {
	variables, err := ParseVariables([]string{"minPrice=10", "open=true", "name=alice", "empty=", "list=[eu, uk]", "text=[unclosed", "id='007'", "now=2026-01-01"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"minPrice": 10, "open": true, "name": "alice", "empty": "", "list": []any{"eu", "uk"}, "text": "[unclosed", "id": "007", "now": "2026-01-01"}
	if !reflect.DeepEqual(variables, want) {
		t.Errorf("variables = %#v, want %#v", variables, want)
	}
	for _, bad := range []string{"novalue", "=x", " =x", "my-var=1", "currentUser=bob", "principal.roles=[a]", "path.spaceID=s1", "m={a: 1}"} {
		if _, err := ParseVariables([]string{bad}); err == nil || !strings.Contains(err.Error(), strings.SplitN(bad, "=", 2)[0]) {
			t.Errorf("%q must be rejected naming the variable: %v", bad, err)
		}
	}
}

func customersQuery() dal.StructuredQuery {
	return dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("customers", ""))).SelectColumns()
}

func TestExplainAndLine(t *testing.T) {
	dir := t.TempDir()
	global := writePolicy(t, dir, "global.yaml", "global")
	loaded, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := access.WithCurrentUser(context.Background(), "alice")
	lines := Explain(ctx, loaded, customersQuery(), map[string]any{"currentUser": "alice", "unused": 1})
	if len(lines) != 1 {
		t.Fatalf("lines = %v", lines)
	}
	want := `access: policy "global" (` + global + `) rule "list-own" allows query on /customers: where ownerID = $currentUser [currentUser=alice]; fields [id, name, address.*, public_*]`
	if got := lines[0].String(); got != want {
		t.Errorf("line =\n%s\nwant\n%s", got, want)
	}
	products := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("products", ""))).SelectColumns()
	if lines := Explain(ctx, loaded, products, nil); len(lines) != 1 || !strings.HasSuffix(lines[0].String(), `rule "list-all" allows query on /products: no limitations`) {
		t.Errorf("products = %v", lines)
	}
	orders := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("orders", ""))).SelectColumns()
	if lines := Explain(ctx, loaded, orders, nil); len(lines) != 1 || lines[0].Allowed || !strings.Contains(lines[0].String(), "denies query on /orders: ") {
		t.Errorf("orders = %v", lines)
	}
	// A principal-bound document attributes the binding and prefixes the rule.
	bound := filepath.Join(dir, "bound.yaml")
	if err := os.WriteFile(bound, []byte(boundPolicy), 0o600); err != nil {
		t.Fatal(err)
	}
	boundLoaded, err := LoadFile(bound)
	if err != nil {
		t.Fatal(err)
	}
	reader := access.WithPrincipal(context.Background(), access.Principal{Roles: []string{"reader"}})
	if lines := Explain(reader, []Loaded{boundLoaded}, products, nil); len(lines) != 1 || !strings.HasSuffix(lines[0].String(), `rule "readers/list" allows query on /products: no limitations via role:reader`) {
		t.Errorf("bound = %v", lines)
	}
	group := dal.NewQueryBuilder(dal.From(dal.NewCollectionGroupRef("customers", ""))).SelectColumns()
	if BaseResource(group).String() != "collection-group:customers" || BaseResource(opaqueQuery{}).String() != "opaque-query:SELECT 1" {
		t.Errorf("resources = %v / %v", BaseResource(group), BaseResource(opaqueQuery{}))
	}
	multi := Line{Policy: "p", Source: "s", Resource: "/c", Rule: "r", Allowed: true, FieldLists: [][]string{{"a"}, {"b", "c"}}}
	if got := multi.String(); got != `access: policy "p" (s) rule "r" allows query on /c: fields [a] or [b, c]` {
		t.Errorf("multi = %s", got)
	}
}

type opaqueQuery struct{ dal.Query }

func (opaqueQuery) String() string { return "SELECT 1" }

func TestHiddenReferences(t *testing.T) {
	lines := []Line{{FieldLists: [][]string{{"id", "name", "address.*", "public_*", "*_at"}}}, {}}
	allowed := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("customers", ""))).
		Where(dal.WhereField("name", dal.Equal, dal.Constant{Value: "x"}), dal.NewGroupCondition(dal.Or, dal.WhereField("address.city", dal.Equal, dal.Constant{Value: "y"}), dal.WhereField("public_bio", dal.In, dal.Array{Value: []string{"z"}})), dal.WhereField("id", dal.Equal, dal.NewParam("id"))).
		OrderBy(dal.Ascending(dal.Field("created_at")), dal.Descending(dal.DocumentID())).
		SelectColumns(dal.Column{Expression: dal.Field("name")}, dal.Column{Expression: dal.Field("id"), Alias: "id"})
	if hidden, err := hiddenReferences(allowed, lines); err != nil || len(hidden) != 0 {
		t.Errorf("allowed references reported hidden: %v, %v", hidden, err)
	}
	probing := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("customers", ""))).
		Where(dal.WhereField("passwordHash", dal.Equal, dal.Constant{Value: "h1"})).
		OrderBy(dal.Descending(dal.Field("email"))).
		GroupBy(dal.Field("ownerID")).
		SelectColumns(dal.Column{Expression: dal.Field("phone"), Alias: "public_phone"})
	if hidden, err := hiddenReferences(probing, lines); err != nil || strings.Join(hidden, ",") != "email,ownerID,passwordHash,phone" {
		t.Errorf("hidden = %v, %v", hidden, err)
	}
	if aliases := aliasedColumns(probing); strings.Join(aliases, ",") != "public_phone" {
		t.Errorf("aliases = %v", aliases)
	}
	if hidden, err := hiddenReferences(probing, nil); err != nil || hidden != nil {
		t.Errorf("no field lists means nothing is hidden: %v, %v", hidden, err)
	}
	// Unknown node kinds fail closed.
	odd := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("customers", ""))).Where(fakeCondition{}).SelectColumns()
	if _, err := hiddenReferences(odd, lines); err == nil || !strings.Contains(err.Error(), "unsupported condition") {
		t.Errorf("unknown condition = %v", err)
	}
	oddExpr := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("customers", ""))).SelectColumns(dal.Column{Expression: fakeExpression{}})
	if _, err := hiddenReferences(oddExpr, lines); err == nil || !strings.Contains(err.Error(), "unsupported expression") {
		t.Errorf("unknown expression = %v", err)
	}
}

type fakeCondition struct{}

func (fakeCondition) String() string { return "fake" }

type fakeExpression struct{}

func (fakeExpression) String() string { return "fake()" }

type stubSession struct {
	dal.ReadSession
	query dal.Query
}

func (s *stubSession) ExecuteQueryToRecordsReader(_ context.Context, query dal.Query) (dal.RecordsReader, error) {
	s.query = query
	return nil, nil
}

func TestRunSubstitutesParamsAndDenies(t *testing.T) {
	stub := &stubSession{}
	priced := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("products", ""))).Where(dal.WhereField("price", dal.GreaterOrEqual, dal.NewParam("minPrice"))).SelectColumns()
	unrestricted := Options{Unrestricted: true}
	result, err := Run(context.Background(), stub, priced, Options{Variables: map[string]any{"minPrice": 10}, Unrestricted: true})
	if err != nil || result.Lines != nil || !strings.Contains(stub.query.String(), "price >= 10") {
		t.Errorf("substituted run = %v, %v, %s", result, err, stub.query)
	}
	if _, err := Run(context.Background(), stub, priced, unrestricted); !errors.Is(err, ErrInvalidQuery) || !strings.Contains(err.Error(), "minPrice") {
		t.Errorf("unresolved = %v", err)
	}
	if _, err := Run(context.Background(), stub, priced, Options{}); !errors.Is(err, ErrNoPolicies) {
		t.Errorf("no policies without Unrestricted = %v", err)
	}
	misplaced := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("products", ""))).
		Where(dal.NewComparison(dal.NewParam("left"), dal.Equal, dal.Field("kind"))).
		GroupBy(dal.NewParam("grp")).Having(dal.WhereField("n", dal.Equal, dal.NewParam("n"))).
		OrderBy(dal.Ascending(dal.NewParam("ord"))).
		SelectColumns(dal.Column{Expression: dal.NewParam("col")})
	if _, err := Run(context.Background(), stub, misplaced, Options{Variables: map[string]any{"n": 1, "left": 1, "grp": 1, "ord": 1, "col": 1}, Unrestricted: true}); !errors.Is(err, ErrInvalidQuery) || !strings.Contains(err.Error(), "$col, $grp, $left, $n, $ord") {
		t.Errorf("misplaced params = %v", err)
	}
	plain := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("products", ""))).SelectColumns()
	if result, err := Run(context.Background(), stub, plain, unrestricted); err != nil || result.Query.String() != plain.String() {
		t.Errorf("plain run = %v, %v", result, err)
	}
	if result, err := Run(context.Background(), stub, opaqueQuery{}, unrestricted); err != nil || result.Query == nil {
		t.Errorf("opaque run = %v, %v", result, err)
	}
	// With policies: the hidden-field probe is denied before the session sees it.
	dir := t.TempDir()
	writePolicy(t, dir, "global.yaml", "global")
	loaded, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	probe := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("customers", ""))).Where(dal.WhereField("passwordHash", dal.Equal, dal.Constant{Value: "h1"})).SelectColumns()
	stub.query = nil
	result, err = Run(context.Background(), stub, probe, Options{Principal: &access.Principal{ID: "alice"}, Policies: loaded})
	if !errors.Is(err, access.ErrAccessDenied) || !strings.Contains(err.Error(), "passwordHash") || stub.query != nil || len(result.Lines) != 1 {
		t.Errorf("probe = %v, %v, %v", result.Lines, err, stub.query)
	}
	if result.Lines[0].Bindings[0] != "currentUser=alice" {
		t.Errorf("principal must bind currentUser: %v", result.Lines[0])
	}
	// Selecting a hidden field, or aliasing any column, is refused too.
	selecting := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("customers", ""))).SelectColumns(dal.Column{Expression: dal.Field("passwordHash")})
	if _, err := Run(context.Background(), stub, selecting, Options{Principal: &access.Principal{ID: "alice"}, Policies: loaded}); !errors.Is(err, access.ErrAccessDenied) || !strings.Contains(err.Error(), "passwordHash") || stub.query != nil {
		t.Errorf("select hidden = %v", err)
	}
	aliased := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("customers", ""))).SelectColumns(dal.Column{Expression: dal.Field("name"), Alias: "n"})
	if _, err := Run(context.Background(), stub, aliased, Options{Principal: &access.Principal{ID: "alice"}, Policies: loaded}); !errors.Is(err, ErrInvalidQuery) || !strings.Contains(err.Error(), "n") || stub.query != nil {
		t.Errorf("alias = %v", err)
	}
	unverifiable := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("customers", ""))).Where(fakeCondition{}).SelectColumns()
	if _, err := Run(context.Background(), stub, unverifiable, Options{Principal: &access.Principal{ID: "alice"}, Policies: loaded}); !errors.Is(err, access.ErrAccessDenied) || stub.query != nil {
		t.Errorf("unverifiable = %v", err)
	}
	// Without field lists the walker is not consulted, so odd nodes reach the session.
	open := writePolicy(t, t.TempDir(), "open.yaml", "open")
	openLoaded, _ := LoadFile(open)
	products := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("products", ""))).Where(fakeCondition{}).SelectColumns()
	if _, err := Run(context.Background(), stub, products, Options{Policies: []Loaded{openLoaded}}); err != nil || stub.query == nil {
		t.Errorf("unrestricted collection = %v", err)
	}
}
