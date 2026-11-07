package secureread

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/tugql"
	"github.com/datatug/datatug-cli/pkg/queryops"
)

// federatedGuardFixture is two SQLite databases a federated query joins.
func federatedGuardFixture(t *testing.T) map[string]string {
	t.Helper()
	root := t.TempDir()
	makeDB := func(name, schema string, rows ...string) string {
		path := filepath.Join(root, name+".sqlite")
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		for _, statement := range append([]string{schema}, rows...) {
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
		return "sqlite://" + path
	}
	return map[string]string{
		"orders": makeDB("orders", `CREATE TABLE Invoice (id INTEGER PRIMARY KEY, country_id INTEGER NOT NULL, amount NUMERIC NOT NULL)`,
			`INSERT INTO Invoice VALUES (1, 1, 10)`, `INSERT INTO Invoice VALUES (2, 2, 20)`),
		"countries": makeDB("countries", `CREATE TABLE Country (id INTEGER PRIMARY KEY, name TEXT NOT NULL, population INTEGER NOT NULL)`,
			`INSERT INTO Country VALUES (1, 'Alpha', 100)`, `INSERT INTO Country VALUES (2, 'Beta', 200)`),
	}
}

// labelGuard refuses a leaf that touches a labelled column ("db.table.column"),
// or projects every column of a table with one, and records each leaf it saw.
type labelGuard struct {
	labelled map[string]bool
	seen     []FederatedLeaf
	dbs      []dal.DB
}

func (g *labelGuard) check(ctx context.Context, leaf FederatedLeaf) error {
	db, err := leaf.Open(ctx)
	if err != nil {
		return err
	}
	if leaf.Scheme != "sqlite" {
		return errors.New("the guard was not given the source's scheme")
	}
	g.seen = append(g.seen, leaf)
	g.dbs = append(g.dbs, db)
	for key := range g.labelled {
		prefix := leaf.Database + "." + leaf.Collection + "."
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if leaf.AllColumns {
			return &FieldProtectedError{Message: "wildcard over " + key}
		}
		for _, column := range leaf.Columns {
			if prefix+column == key {
				return &FieldProtectedError{Message: "protected " + key}
			}
		}
	}
	return nil
}

func guardedExecutor(guard FederatedColumnGuard) *Executor {
	return NewExecutor(Session{Unrestricted: true, FederatedColumns: guard})
}

// noLeafRead counts federated progress events: a refused read reports none.
func noLeafRead(t *testing.T) (context.Context, *int) {
	t.Helper()
	events := 0
	return WithFederatedProgress(context.Background(), func(dal.FederatedProgress) { events++ }), &events
}

const guardJoinTugQL = `from:
  database: orders
  name: Invoice
  alias: o
  joins:
    - from: {database: countries, name: Country, alias: c, scan: {orderBy: [{field: id}], limit: 100}}
      on: [{left: {field: country_id, source: o}, op: '==', right: {field: id, source: c}}]
columns:
  - {field: amount, source: o}
  - {field: name, source: c}
`

func TestFederatedGuardRefusesAProjectedLabelledColumn(t *testing.T) {
	urls := federatedGuardFixture(t)
	guard := &labelGuard{labelled: map[string]bool{"countries.Country.name": true}}
	ctx, events := noLeafRead(t)
	_, err := guardedExecutor(guard.check).RunFederatedTugQL(ctx, []byte(guardJoinTugQL), urls, nil)
	var protected *FieldProtectedError
	if !errors.As(err, &protected) || protected.Message != "protected countries.Country.name" {
		t.Fatalf("err = %v", err)
	}
	if *events != 0 {
		t.Fatalf("%d leaf reads happened before the refusal", *events)
	}
}

func TestFederatedGuardRefusesALabelledColumnOnlyInOn(t *testing.T) {
	urls := federatedGuardFixture(t)
	guard := &labelGuard{labelled: map[string]bool{"orders.Invoice.country_id": true}}
	ctx, events := noLeafRead(t)
	_, err := guardedExecutor(guard.check).RunFederatedTugQL(ctx, []byte(guardJoinTugQL), urls, nil)
	var protected *FieldProtectedError
	if !errors.As(err, &protected) || protected.Message != "protected orders.Invoice.country_id" {
		t.Fatalf("err = %v", err)
	}
	if *events != 0 {
		t.Fatalf("%d leaf reads happened before the refusal", *events)
	}
}

func TestFederatedGuardRefusesAWildcardOverALabelledLeaf(t *testing.T) {
	urls := federatedGuardFixture(t)
	wildcard := strings.Split(guardJoinTugQL, "columns:")[0]
	guard := &labelGuard{labelled: map[string]bool{"countries.Country.population": true}}
	_, err := guardedExecutor(guard.check).StreamFederatedTugQL(context.Background(), []byte(wildcard), urls, nil)
	var protected *FieldProtectedError
	if !errors.As(err, &protected) || protected.Message != "wildcard over countries.Country.population" {
		t.Fatalf("err = %v", err)
	}
	// A wildcard of one source names only that source.
	scoped := wildcard + "columns:\n  - {wildcard: {source: o, exclude: [nothing]}}\n  - {field: name, source: c}\n"
	guard = &labelGuard{labelled: map[string]bool{"orders.Invoice.amount": true}}
	if _, err = guardedExecutor(guard.check).StreamFederatedTugQL(context.Background(), []byte(scoped), urls, nil); !errors.As(err, &protected) {
		t.Fatalf("err = %v", err)
	}
	guard = &labelGuard{labelled: map[string]bool{"countries.Country.population": true}}
	stream, err := guardedExecutor(guard.check).StreamFederatedTugQL(context.Background(), []byte(scoped), urls, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.Close()
}

func TestFederatedGuardAllowsUnlabelledColumns(t *testing.T) {
	urls := federatedGuardFixture(t)
	guard := &labelGuard{labelled: map[string]bool{"countries.Country.population": true, "orders.Invoice.secret": true}}
	result, err := guardedExecutor(guard.check).RunFederatedTugQL(context.Background(), []byte(guardJoinTugQL), urls, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("rows: %+v", result.Rows)
	}
	got := map[string][]string{}
	for _, leaf := range guard.seen {
		if leaf.AllColumns {
			t.Fatalf("explicit columns reported as a wildcard: %+v", leaf)
		}
		got[leaf.Database+"."+leaf.Collection] = leaf.Columns
	}
	if strings.Join(got["orders.Invoice"], ",") != "amount,country_id" || strings.Join(got["countries.Country"], ",") != "id,name" {
		t.Fatalf("leaves: %+v", got)
	}
}

func TestFederatedGuardErrorPropagatesBeforeAnyLeafRead(t *testing.T) {
	urls := federatedGuardFixture(t)
	sentinel := errors.New("labels unreadable")
	calls := 0
	ctx, events := noLeafRead(t)
	_, err := guardedExecutor(func(context.Context, FederatedLeaf) error {
		calls++
		return sentinel
	}).StreamFederatedTugQL(ctx, []byte(guardJoinTugQL), urls, nil)
	if !errors.Is(err, sentinel) || calls != 1 || *events != 0 {
		t.Fatalf("err=%v calls=%d events=%d", err, calls, *events)
	}
	// A leaf whose source cannot be opened fails the guard that opens it.
	delete(urls, "countries")
	guard := &labelGuard{}
	_, err = guardedExecutor(guard.check).StreamFederatedTugQL(context.Background(), []byte(guardJoinTugQL), urls, nil)
	if err == nil || !strings.Contains(err.Error(), `database "countries" is not configured`) || len(guard.seen) != 1 {
		t.Fatalf("err=%v seen=%d", err, len(guard.seen))
	}
	// A guard that opens nothing leaves the missing source to the read.
	_, err = guardedExecutor(func(context.Context, FederatedLeaf) error { return nil }).
		StreamFederatedTugQL(context.Background(), []byte(guardJoinTugQL), urls, nil)
	if err == nil || !strings.Contains(err.Error(), `database "countries" is not configured`) {
		t.Fatalf("err=%v", err)
	}
}

func TestFederatedReadWithoutAGuard(t *testing.T) {
	urls := federatedGuardFixture(t)
	// A policy-governed session never runs a federated read unchecked.
	governed := NewExecutor(Session{DatabaseProfile: DatabasePolicyGoverned})
	_, err := governed.StreamFederatedTugQL(context.Background(), []byte(guardJoinTugQL), urls, nil)
	var protected *FieldProtectedError
	if !errors.As(err, &protected) || !strings.Contains(protected.Error(), "sensitivity labels") {
		t.Fatalf("err = %v", err)
	}
	// A policy-free session with no labels to apply keeps reading.
	result, err := NewExecutor(Session{Unrestricted: true}).RunFederatedTugQL(context.Background(), []byte(guardJoinTugQL), urls, nil)
	if err != nil || len(result.Rows) != 2 {
		t.Fatalf("rows=%+v err=%v", result.Rows, err)
	}
}

func TestFederatedGuardChecksNestedQueries(t *testing.T) {
	urls := federatedGuardFixture(t)
	subquery := `from:
  database: orders
  name: Invoice
  alias: o
where: {exists: {query: {from: {database: countries, name: Country, alias: c}}}}
`
	if _, err := tugql.Deserialize([]byte(subquery)); err != nil {
		t.Fatal(err)
	}
	// The nested query's leaf is checked: EXISTS reads every column of it.
	guard := &labelGuard{labelled: map[string]bool{"countries.Country.population": true}}
	_, err := guardedExecutor(guard.check).StreamFederatedTugQL(context.Background(), []byte(subquery), urls, nil)
	var protected *FieldProtectedError
	if !errors.As(err, &protected) || protected.Message != "wildcard over countries.Country.population" {
		t.Fatalf("err = %v", err)
	}
}

type opaqueFederatedExpression struct{}

func (opaqueFederatedExpression) String() string { return "opaque()" }

type opaqueFederatedCondition struct{}

func (opaqueFederatedCondition) String() string { return "opaque()" }

func TestFederatedLeavesAttributesEveryClause(t *testing.T) {
	orders := dal.NewDatabaseCollectionRef("orders", "", "Invoice", "o").WithScan(10, dal.AscendingField("created"))
	countries := dal.NewDatabaseCollectionRef("countries", "", "Country", "")
	from := dal.From(&orders).Join(dal.NewJoinedSource(countries, dal.JoinInner,
		dal.NewComparison(dal.NewFieldRef("o", "country_id"), dal.Equal, dal.NewFieldRef("Country", "id"))))
	amount := dal.NewFieldRef("o", "amount")
	query := dal.NewQueryBuilder(from).
		Where(dal.NewGroupCondition(dal.And,
			&dal.Comparison{Left: &amount, Operator: dal.GreaterThen, Right: dal.NewConstant(1)},
			dal.NewIsNotNullCondition(dal.NewFieldRef("", "region")),
			&dal.GroupCondition{},
		)).
		GroupBy(dal.NewFieldRef("Country", "name")).
		Having(dal.NewComparison(dal.NewAggregate(dal.SUM, false, dal.Binary(dal.NewFieldRef("o", "amount"), dal.Multiply, dal.NewParam("rate"))), dal.GreaterThen, dal.NewConstant(0))).
		OrderBy(dal.Descending(dal.NewOrderedAggregate(dal.LAST, []dal.OrderExpression{dal.AscendingField("ts")}, dal.NewFieldRef("x", "total")))).
		SelectColumns(
			dal.Column{Expression: dal.NewFieldRef("Country", "name")},
			dal.Column{Expression: dal.DocumentID()},
			dal.Count(),
			dal.Column{Expression: &dal.BinaryExpression{Left: dal.NewFieldRef("o", "amount"), Operator: dal.Add, Right: dal.NewConstant(1)}},
			dal.Column{Expression: &dal.IsNullCondition{}},
		)
	_, err := federatedLeaves(query)
	if err == nil || !strings.Contains(err.Error(), "expression") {
		t.Fatalf("an IS NULL used as an expression is not inspected: %v", err)
	}
	columns := query.Columns()
	query = dal.WithColumns(query, columns[:4])
	leaves, err := federatedLeaves(query)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, leaf := range leaves {
		got[leaf.Database+"."+leaf.Collection] = strings.Join(leaf.Columns, ",")
		if leaf.AllColumns {
			t.Fatalf("wildcard: %+v", leaf)
		}
	}
	// Unqualified and unknown sources fall to every leaf; scan orders stay on their own.
	if got["orders.Invoice"] != "amount,country_id,created,region,total,ts" || got["countries.Country"] != "id,name,region,total,ts" {
		t.Fatalf("leaves: %+v", got)
	}
	// No explicit column projects every column of every leaf.
	leaves, err = federatedLeaves(dal.NewQueryBuilder(from).SelectColumns())
	if err != nil || len(leaves) != 2 || !leaves[0].AllColumns || !leaves[1].AllColumns {
		t.Fatalf("leaves=%+v err=%v", leaves, err)
	}
	// An unscoped wildcard projects every leaf too.
	leaves, err = federatedLeaves(dal.NewQueryBuilder(from).SelectColumns(dal.AllColumnsExcept("x"), dal.Column{Expression: dal.NewFieldRef("o", "id")}))
	if err != nil || !leaves[0].AllColumns || !leaves[1].AllColumns {
		t.Fatalf("leaves=%+v err=%v", leaves, err)
	}
	// A nested join tree is walked.
	nested := dal.From(orders).Join(dal.NewJoinedFrom(dal.From(countries).Join(dal.NewJoinedSource(dal.NewDatabaseCollectionRef("geo", "", "Region", "r"), dal.JoinInner)), dal.JoinInner))
	leaves, err = federatedLeaves(dal.NewQueryBuilder(nested).SelectColumns(dal.Column{Expression: dal.NewFieldRef("r", "code")}))
	if err != nil || len(leaves) != 3 || strings.Join(leaves[2].Columns, ",") != "code" || len(leaves[0].Columns) != 1 {
		t.Fatalf("leaves=%+v err=%v", leaves, err)
	}
}

func TestFederatedLeavesRefusesWhatItCannotInspect(t *testing.T) {
	ref := dal.NewDatabaseCollectionRef("orders", "", "Invoice", "o")
	for name, query := range map[string]dal.StructuredQuery{
		"expression": dal.NewQueryBuilder(dal.From(ref)).SelectColumns(dal.Column{Expression: opaqueFederatedExpression{}}),
		"condition":  dal.NewQueryBuilder(dal.From(ref)).Where(opaqueFederatedCondition{}).SelectColumns(),
		"source":     dal.NewQueryBuilder(dal.From(dal.NewCollectionGroupRef("Invoice", "o"))).SelectColumns(),
		"join source": dal.NewQueryBuilder(dal.From(ref).Join(dal.NewJoinedSource(dal.NewCollectionGroupRef("Country", "c"), dal.JoinInner))).
			SelectColumns(),
		"on":              dal.NewQueryBuilder(dal.From(ref).Join(dal.NewJoinedSource(ref, dal.JoinInner, opaqueFederatedCondition{}))).SelectColumns(),
		"scan order":      dal.NewQueryBuilder(dal.From(ref.WithScan(1, dal.Ascending(opaqueFederatedExpression{})))).SelectColumns(),
		"having":          dal.NewQueryBuilder(dal.From(ref)).Having(opaqueFederatedCondition{}).SelectColumns(),
		"order":           dal.NewQueryBuilder(dal.From(ref)).OrderBy(dal.Ascending(opaqueFederatedExpression{})).SelectColumns(),
		"group":           dal.NewQueryBuilder(dal.From(ref)).GroupBy(opaqueFederatedExpression{}).SelectColumns(),
		"binary":          dal.NewQueryBuilder(dal.From(ref)).SelectColumns(dal.Column{Expression: dal.Binary(opaqueFederatedExpression{}, dal.Add, dal.NewConstant(1))}),
		"binary right":    dal.NewQueryBuilder(dal.From(ref)).SelectColumns(dal.Column{Expression: dal.Binary(dal.NewConstant(1), dal.Add, opaqueFederatedExpression{})}),
		"aggregate":       dal.NewQueryBuilder(dal.From(ref)).SelectColumns(dal.Column{Expression: dal.NewAggregate(dal.SUM, false, opaqueFederatedExpression{})}),
		"aggregate order": dal.NewQueryBuilder(dal.From(ref)).SelectColumns(dal.Column{Expression: dal.NewOrderedAggregate(dal.LAST, []dal.OrderExpression{dal.Ascending(opaqueFederatedExpression{})}, dal.NewFieldRef("", "a"))}),
		"comparison":      dal.NewQueryBuilder(dal.From(ref)).Where(dal.NewComparison(opaqueFederatedExpression{}, dal.Equal, dal.NewConstant(1))).SelectColumns(),
		"group member":    dal.NewQueryBuilder(dal.From(ref)).Where(dal.NewGroupCondition(dal.And, opaqueFederatedCondition{})).SelectColumns(),
		"is null":         dal.NewQueryBuilder(dal.From(ref)).Where(dal.NewIsNullCondition(opaqueFederatedExpression{})).SelectColumns(),
		"nil derived":     dal.NewQueryBuilder(dal.From(dal.NewQuerySource(nil, "d"))).SelectColumns(),
		"exists inner":    dal.NewQueryBuilder(dal.From(ref)).Where(dal.NewExistsCondition(dal.NewQueryBuilder(dal.From(ref)).Where(opaqueFederatedCondition{}).SelectColumns())).SelectColumns(),
		"derived joins":   dal.NewQueryBuilder(dal.From(dal.NewQuerySource(dal.NewQueryBuilder(dal.From(ref)).SelectColumns(), "d")).Join(dal.NewJoinedSource(dal.NewCollectionGroupRef("C", "c"), dal.JoinInner))).SelectColumns(),
		"derived inner":   dal.NewQueryBuilder(dal.From(dal.NewQuerySource(dal.NewQueryBuilder(dal.From(ref)).Where(opaqueFederatedCondition{}).SelectColumns(), "d"))).SelectColumns(),
		"too deep":        deepQuery(ref, maxQueryDepth+1),
	} {
		if _, err := federatedLeaves(query); err == nil {
			t.Errorf("%s: an uninspectable query was accepted", name)
		}
	}
}

func TestGuardTugQLColumnsChecksASingleSourceRead(t *testing.T) {
	urls := federatedGuardFixture(t)
	selfJoin := `from:
  name: Invoice
  alias: a
  joins:
    - from: {name: Invoice, alias: b}
      on: [{left: {field: id, source: a}, op: '==', right: {field: id, source: b}}]
columns: [{field: amount, source: b}]
`
	ctx := context.Background()
	guard := &labelGuard{labelled: map[string]bool{"orders.Invoice.amount": true}}
	err := guardedExecutor(guard.check).GuardTugQLColumns(ctx, []byte(selfJoin), "orders", urls["orders"])
	var protected *FieldProtectedError
	if !errors.As(err, &protected) || protected.Message != "protected orders.Invoice.amount" {
		t.Fatalf("err = %v", err)
	}
	// Both leaves are of the one source, opened once, named by the catalog id.
	guard = &labelGuard{labelled: map[string]bool{"orders.Invoice.secret": true}}
	if err = guardedExecutor(guard.check).GuardTugQLColumns(ctx, []byte(selfJoin), "orders", urls["orders"]); err != nil {
		t.Fatal(err)
	}
	if len(guard.seen) != 2 || guard.seen[0].Database != "orders" || g0(guard) != g1(guard) {
		t.Fatalf("leaves: %+v", guard.seen)
	}
	// A policy-free session without a guard is unchecked, a governed one refused.
	if err = NewExecutor(Session{Unrestricted: true}).GuardTugQLColumns(ctx, []byte("not: [tugql"), "orders", urls["orders"]); err != nil {
		t.Fatal(err)
	}
	governed := NewExecutor(Session{DatabaseProfile: DatabasePolicyGoverned})
	if err = governed.GuardTugQLColumns(ctx, []byte(selfJoin), "orders", urls["orders"]); !errors.As(err, &protected) {
		t.Fatalf("err = %v", err)
	}
	if err = governed.GuardTugQLColumns(ctx, []byte("not: [tugql"), "orders", urls["orders"]); err == nil || !strings.Contains(err.Error(), "secureread: parse TugQL") {
		t.Fatalf("err = %v", err)
	}
	if err = governed.GuardTugQLColumns(ctx, []byte(selfJoin), "orders", "sqlite://"+filepath.Join(t.TempDir(), "missing.sqlite")); err == nil || !strings.Contains(err.Error(), `open database "orders"`) {
		t.Fatalf("err = %v", err)
	}
}

func g0(g *labelGuard) dal.DB { return g.dbs[0] }
func g1(g *labelGuard) dal.DB { return g.dbs[1] }

func TestRunTugQLAndNativeSQLUseTheSessionGuards(t *testing.T) {
	urls := federatedGuardFixture(t)
	ctx := context.Background()
	doc := []byte("from: {name: Country}\ncolumns: [{field: population}]\n")
	guard := &labelGuard{labelled: map[string]bool{"countries.Country.population": true}}
	session := Session{Unrestricted: true, FederatedColumns: guard.check, SourceCatalogs: map[string][]string{urls["countries"]: {"countries"}}}
	var protected *FieldProtectedError
	if _, err := NewExecutor(session).RunTugQL(ctx, urls["countries"], doc, nil); !errors.As(err, &protected) || protected.Message != "protected countries.Country.population" {
		t.Fatalf("err = %v", err)
	}
	if _, err := NewExecutor(session).RunTugQL(ctx, urls["orders"], doc, nil); !errors.Is(err, errUnknownCatalog) {
		t.Fatalf("err = %v", err)
	}
	result, err := NewExecutor(session).RunTugQL(ctx, urls["countries"], []byte("from: {name: Country}\ncolumns: [{field: name}]\n"), nil)
	if err != nil || len(result.Rows) != 2 {
		t.Fatalf("rows=%+v err=%v", result.Rows, err)
	}
	sentinel := errors.New("labelled source")
	var checked string
	session.NativeSQL = func(_ context.Context, catalog, sourceURL string) error {
		checked = catalog + " " + sourceURL
		return sentinel
	}
	if _, err := NewExecutor(session).RunNativeSQL(ctx, urls["countries"], "SELECT name FROM Country"); !errors.Is(err, sentinel) || checked != "countries "+urls["countries"] {
		t.Fatalf("err=%v checked=%q", err, checked)
	}
	if _, err := NewExecutor(session).RunNativeSQL(ctx, urls["orders"], "SELECT id FROM Invoice"); !errors.Is(err, errUnknownCatalog) {
		t.Fatalf("err = %v", err)
	}
	session.NativeSQL = func(context.Context, string, string) error { return nil }
	if result, err = NewExecutor(session).RunNativeSQL(ctx, urls["countries"], "SELECT name FROM Country"); err != nil || len(result.Rows) != 2 {
		t.Fatalf("rows=%+v err=%v", result.Rows, err)
	}
}

// deepQuery nests depth scalar subqueries.
func deepQuery(ref dal.CollectionRef, depth int) dal.StructuredQuery {
	query := dal.NewQueryBuilder(dal.From(ref)).SelectColumns()
	for i := 0; i < depth; i++ {
		query = dal.NewQueryBuilder(dal.From(ref)).SelectColumns(dal.Column{Expression: dal.NewQueryExpression(query, "x")})
	}
	return query
}

func TestFederatedLeavesWalksNestedQueries(t *testing.T) {
	orders := dal.NewDatabaseCollectionRef("orders", "", "Invoice", "o")
	countries := dal.NewDatabaseCollectionRef("countries", "", "Country", "c")
	inner := dal.NewQueryBuilder(dal.From(countries)).SelectColumns(dal.Column{Expression: dal.NewFieldRef("c", "name")})
	exists := dal.NewExistsCondition(dal.NewQueryBuilder(dal.From(countries)).Where(dal.NewComparison(dal.NewFieldRef("c", "id"), dal.Equal, dal.NewFieldRef("o", "country_id"))).SelectColumns(dal.Column{Expression: dal.NewFieldRef("c", "id")}))
	scalar := dal.NewQueryExpression(inner, "n")
	derived := dal.NewQuerySource(dal.NewQueryBuilder(dal.From(orders)).SelectColumns(dal.Column{Expression: dal.NewFieldRef("o", "amount")}), "d")
	query := dal.NewQueryBuilder(dal.From(&derived).Join(dal.NewJoinedSource(countries, dal.JoinInner))).
		Where(dal.NewGroupCondition(dal.And, exists, &exists)).
		SelectColumns(dal.Column{Expression: scalar}, dal.Column{Expression: &scalar}, dal.Column{Expression: dal.NewFieldRef("d", "amount")})
	leaves, err := federatedLeaves(query)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, leaf := range leaves {
		got = append(got, leaf.Database+"."+leaf.Collection+":"+strings.Join(leaf.Columns, ","))
	}
	// d.amount names the derived source, no leaf: it counts against every leaf.
	want := "orders.Invoice:amount,country_id|countries.Country:amount,id,name|countries.Country:amount,id,name|countries.Country:amount,id,name|countries.Country:amount,id|countries.Country:amount,id"
	if strings.Join(got, "|") != want {
		t.Fatalf("leaves:\n%s\nwant\n%s", strings.Join(got, "|"), want)
	}
	// A nested query without columns reads every column of its own leaves only.
	leaves, err = federatedLeaves(dal.NewQueryBuilder(dal.From(orders)).Where(dal.NewExistsCondition(dal.NewQueryBuilder(dal.From(countries)).SelectColumns())).SelectColumns(dal.Column{Expression: dal.NewFieldRef("o", "id")}))
	if err != nil || leaves[0].AllColumns || !leaves[1].AllColumns {
		t.Fatalf("leaves=%+v err=%v", leaves, err)
	}
}

// Every Executor entry point that reads a source refuses a labelled column
// when the session carries a guard. A new entry point fails this test until
// it is listed here or exempted with a reason.
func TestEveryReadEntryPointChecksLabels(t *testing.T) {
	source := exactSQLite(t)
	ctx := context.Background()
	guard := &labelGuard{labelled: map[string]bool{"items.Item.Category": true}}
	protectedSQL := &FieldProtectedError{Message: "labelled source"}
	session := Session{Unrestricted: true, FederatedColumns: guard.check, SourceCatalogs: map[string][]string{source: {"items"}},
		NativeSQL: func(context.Context, string, string) error { return protectedSQL }}
	e := NewExecutor(session)
	doc := "from: {name: Item}\ncolumns: [{field: Category}]\n"
	parsed, err := tugql.Deserialize([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	federated := "from: {database: items, name: Item}\ncolumns: [{field: Category}]\n"
	preview := exactPreview(t, exactOriginal, queryops.MatchingSource, []queryops.AggregateTerm{{Column: "Value", Function: "SUM", Alias: "total"}})
	reads := map[string]func() error{
		"RunStructured":                func() error { _, err := e.RunStructured(ctx, source, parsed, nil); return err },
		"RunStructuredInsecureForTest": func() error { _, err := e.RunStructuredInsecureForTest(ctx, source, parsed, nil); return err },
		"RunTugQL":                     func() error { _, err := e.RunTugQL(ctx, source, []byte(doc), nil); return err },
		"RunRecursiveTugQL":            func() error { _, err := e.RunRecursiveTugQL(ctx, source, []byte(doc), nil); return err },
		"RunExactAggregateTugQL": func() error {
			_, err := e.RunExactAggregateTugQL(ctx, source, []byte(exactOriginal), []byte(preview.TugQL), map[string]any{"minimum": json.Number("0")}, exactColumns)
			return err
		},
		"RunFederatedTugQL": func() error {
			_, err := e.RunFederatedTugQL(ctx, []byte(federated), map[string]string{"items": source}, nil)
			return err
		},
		"StreamFederatedTugQL": func() error {
			_, err := e.StreamFederatedTugQL(ctx, []byte(federated), map[string]string{"items": source}, nil)
			return err
		},
		"RunNativeSQL":      func() error { _, err := e.RunNativeSQL(ctx, source, "SELECT Category FROM Item"); return err },
		"GuardTugQLColumns": func() error { return e.GuardTugQLColumns(ctx, []byte(doc), "items", source) },
	}
	exempt := map[string]string{
		"Authorize":              "decides a policy question and reads no row",
		"CanReadWholeCollection": "decides a policy question and reads no row",
		"RunSnapshot":            "reads a recordset an earlier, guarded read returned; it names no source",
		"WithLabels":             "returns an executor with label guards and reads no row",
		"WithCatalog":            "returns an executor that names one more source and reads no row",
	}
	methods := reflect.TypeOf(e)
	for i := 0; i < methods.NumMethod(); i++ {
		name := methods.Method(i).Name
		if _, ok := exempt[name]; ok {
			continue
		}
		read, ok := reads[name]
		if !ok {
			t.Errorf("Executor.%s is neither checked here nor exempted", name)
			continue
		}
		var protected *FieldProtectedError
		if err := read(); !errors.As(err, &protected) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	var protected *FieldProtectedError
	// An unstructured query cannot be inspected, and a document that does not
	// parse is refused before any read.
	if _, err := e.RunStructured(ctx, source, dal.NewTextQuery("SELECT 1", nil), nil); !errors.Is(err, errUninspectable) {
		t.Fatalf("err = %v", err)
	}
	if err := e.guardDocuments(ctx, source, []byte("not: [tugql")); err == nil || !strings.Contains(err.Error(), "secureread: parse TugQL") {
		t.Fatalf("err = %v", err)
	}
	// A query the walker cannot follow is refused.
	if err := e.guardSource(ctx, source, deepQuery(dal.NewRootCollectionRef("Item", ""), maxQueryDepth+1)); !errors.As(err, &protected) || !strings.Contains(protected.Message, "cannot inspect") {
		t.Fatalf("err = %v", err)
	}
	// Unlabelled columns pass every check.
	clean := NewExecutor(Session{Unrestricted: true, FederatedColumns: (&labelGuard{}).check, SourceCatalogs: map[string][]string{source: {"items"}}})
	if _, err := clean.RunExactAggregateTugQL(ctx, source, []byte(exactOriginal), []byte(preview.TugQL), map[string]any{"minimum": json.Number("0")}, exactColumns); err != nil {
		t.Fatal(err)
	}
	// An HTTP source is checked like any other: one the session cannot name
	// is refused, and a named one is checked against its labels.
	if err := e.guardSource(ctx, "https://example.com/api", parsed); !errors.Is(err, errUnknownCatalog) {
		t.Fatalf("err = %v", err)
	}
	httpSource := "http://" + t.TempDir()
	var seen FederatedLeaf
	named := e.WithLabels(func(_ context.Context, leaf FederatedLeaf) error { seen = leaf; return protectedSQL }, nil, map[string][]string{httpSource: {"items"}})
	if err := named.guardSource(ctx, httpSource, parsed); !errors.Is(err, protectedSQL) || seen.Database != "items" || seen.Scheme != "http" || seen.Collection != "Item" {
		t.Fatalf("err = %v, leaf = %+v", err, seen)
	}
	// WithCatalog names one more URL and leaves the session it copies alone.
	other := "https://example.com/api"
	if err := named.WithCatalog(other, "items").guardSource(ctx, other, parsed); !errors.Is(err, protectedSQL) {
		t.Fatalf("err = %v", err)
	}
	if _, ok := named.session.SourceCatalogs[other]; ok {
		t.Fatal("WithCatalog changed the session it copies")
	}
	// WithLabels keeps the policy session and replaces only the guards.
	if bare := e.WithLabels(nil, nil, nil); bare.session.FederatedColumns != nil || !bare.session.Unrestricted {
		t.Fatalf("session = %+v", bare.session)
	}
	// Two catalogs on one URL: the labels of each apply.
	guard = &labelGuard{labelled: map[string]bool{"other.Item.Category": true}}
	shared := NewExecutor(Session{Unrestricted: true, FederatedColumns: guard.check, SourceCatalogs: map[string][]string{source: {"items", "other"}},
		NativeSQL: func(_ context.Context, catalog, _ string) error {
			if catalog == "other" {
				return protectedSQL
			}
			return nil
		}})
	if _, err := shared.RunTugQL(ctx, source, []byte(doc), nil); !errors.As(err, &protected) {
		t.Fatalf("err = %v", err)
	}
	if _, err := shared.RunNativeSQL(ctx, source, "SELECT 1"); !errors.Is(err, protectedSQL) {
		t.Fatalf("err = %v", err)
	}
}
