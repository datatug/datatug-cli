package secureread

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/tugql"
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
}

func (g *labelGuard) check(_ context.Context, leaf FederatedLeaf) error {
	if leaf.DB == nil || leaf.Scheme != "sqlite" {
		return errors.New("the guard was not given the opened source")
	}
	g.seen = append(g.seen, leaf)
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
	// A leaf whose source cannot be opened refuses before the guard sees it.
	delete(urls, "countries")
	calls = 0
	_, err = guardedExecutor(func(context.Context, FederatedLeaf) error { calls++; return nil }).
		StreamFederatedTugQL(context.Background(), []byte(guardJoinTugQL), urls, nil)
	if err == nil || !strings.Contains(err.Error(), `database "countries" is not configured`) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
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

func TestFederatedGuardRefusesWhatItCannotInspect(t *testing.T) {
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
	_, err := guardedExecutor(func(context.Context, FederatedLeaf) error { return nil }).
		StreamFederatedTugQL(context.Background(), []byte(subquery), urls, nil)
	var protected *FieldProtectedError
	if !errors.As(err, &protected) || !strings.Contains(protected.Message, "cannot inspect") {
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
		"subquery":        dal.NewQueryBuilder(dal.From(ref)).Where(dal.NewExistsCondition(dal.NewQueryBuilder(dal.From(ref)).SelectColumns())).SelectColumns(),
	} {
		if _, err := federatedLeaves(query); err == nil {
			t.Errorf("%s: an uninspectable query was accepted", name)
		}
	}
}
