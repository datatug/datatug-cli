package chat

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

func joinSnapshot() ForeignKeySnapshot {
	return ForeignKeySnapshot{Source: "sqlite:///fixture.db", Columns: map[string][]string{
		"main.invoice":  {"InvoiceId", "CustomerId", "InvoiceDate", "Total"},
		"main.customer": {"CustomerId", "FirstName", "LastName"},
		"main.order":    {"OrderId", "Country", "Location"},
		"main.location": {"Country", "Code"},
		"main.employee": {"EmployeeId", "ManagerId"},
	}, Keys: []ForeignKey{
		{ConstraintID: "fk_invoice_customer", Schema: "main", FromRelation: "Invoice", FromFields: []string{"CustomerId"}, ToSchema: "main", ToRelation: "Customer", ToFields: []string{"CustomerId"}},
		{ConstraintID: "fk_order_location", Schema: "main", FromRelation: "Order", FromFields: []string{"Country", "Location"}, ToSchema: "main", ToRelation: "Location", ToFields: []string{"Country", "Code"}},
		{ConstraintID: "fk_employee_manager", Schema: "main", FromRelation: "Employee", FromFields: []string{"ManagerId"}, ToSchema: "main", ToRelation: "Employee", ToFields: []string{"EmployeeId"}},
	}}
}

func TestChinookInvoiceCustomerJoinThroughDALgo(t *testing.T) {
	ctx := context.Background()
	path, err := filepath.Abs(filepath.Join("..", "dbcopy", "testdata", "chinook.db"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	source := "sqlite://" + path
	snapshot, err := LoadSQLiteForeignKeySnapshot(ctx, source, db)
	if err != nil {
		t.Fatal(err)
	}
	parent := []byte("from: {name: Invoice}\ncolumns:\n  - field: InvoiceId\n  - field: CustomerId\n  - field: InvoiceDate\nwhere: {op: '>', left: {field: InvoiceId}, right: {value: 0}}\norderBy:\n  - field: InvoiceId\n    desc: true\nlimit: 20\n")
	candidates, err := DiscoverJoinCandidates(parent, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var customer JoinCandidate
	for _, candidate := range candidates {
		if candidate.Source.ID == "root" && candidate.Target.Relation == "Customer" && candidate.Direction == "outgoing" {
			customer = candidate
			break
		}
	}
	if customer.ID == "" {
		t.Fatalf("Invoice to Customer FK not discovered: %#v", candidates)
	}
	doc, _, err := DeriveJoinDTQL(parent, snapshot, customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	executor := secureread.NewExecutor(secureread.Session{Unrestricted: true})
	result, err := executor.RunDTQL(ctx, source, doc, nil)
	if err != nil {
		t.Fatalf("DALgo rejected derived DTQL:\n%s\n%v", doc, err)
	}
	if len(result.Rows) != 20 {
		t.Fatalf("joined rows = %d, want 20", len(result.Rows))
	}
	var wantCustomerID int64
	var wantFirstName string
	if err := db.QueryRowContext(ctx, "SELECT Invoice.CustomerId, Customer.FirstName FROM Invoice JOIN Customer ON Invoice.CustomerId = Customer.CustomerId WHERE Invoice.InvoiceId = 412").Scan(&wantCustomerID, &wantFirstName); err != nil {
		t.Fatal(err)
	}
	first := result.Rows[0].Data
	if fmt.Sprint(first["InvoiceId"]) != "412" || fmt.Sprint(first["CustomerId"]) != fmt.Sprint(wantCustomerID) || first["Customer_FirstName"] != wantFirstName {
		t.Fatalf("first joined row = %#v, want invoice 412 customer %d %q", first, wantCustomerID, wantFirstName)
	}
	chained, err := DiscoverJoinCandidates(doc, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var invoiceLine, employee bool
	for _, candidate := range chained {
		if candidate.Source.Relation == "Invoice" && candidate.Target.Relation == "InvoiceLine" {
			invoiceLine = true
		}
		if candidate.Source.Relation == "Customer" && candidate.Target.Relation == "Employee" {
			employee = true
		}
		if candidate.Source.Relation == "Invoice" && candidate.Target.Relation == "Customer" && candidate.Direction == "outgoing" {
			t.Fatal("active Customer edge was offered again")
		}
	}
	if !invoiceLine || !employee {
		t.Fatalf("chained candidates missing InvoiceLine or Employee: %#v", chained)
	}
	for _, candidate := range chained {
		if candidate.Source.Relation != "Customer" || candidate.Target.Relation != "Employee" || candidate.Direction != "outgoing" {
			continue
		}
		second, _, err := DeriveJoinDTQL(doc, snapshot, candidate.ID)
		if err != nil {
			t.Fatalf("derive chained JOIN: %v", err)
		}
		chainedResult, err := executor.RunDTQL(ctx, source, second, nil)
		if err != nil {
			t.Fatalf("DALgo rejected chained DTQL:\n%s\n%v", second, err)
		}
		if len(chainedResult.Rows) != 20 || chainedResult.Rows[0].Data["Employee_FirstName"] == nil {
			t.Fatalf("chained Employee results = %#v", chainedResult.Rows)
		}
		return
	}
	t.Fatal("Customer to Employee FK not found")
}

func TestDiscoverJoinCandidatesKeepsCompositeAndSelfDirections(t *testing.T) {
	candidates, err := DiscoverJoinCandidates([]byte("from:\n  schema: main\n  name: Employee\n  alias: e\n  joins:\n    - from: {schema: main, name: Invoice, alias: i}\n      on: [{left: {field: EmployeeId, source: e}, op: '==', right: {field: CustomerId, source: i}}]\n"), joinSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	var employee, invoice int
	for _, candidate := range candidates {
		if candidate.Source.ID == "root" && candidate.Target.Relation == "Employee" {
			employee++
		}
		if candidate.Source.ID == "root/0" && candidate.Target.Relation == "Customer" {
			invoice++
		}
	}
	if employee != 2 {
		t.Fatalf("self FK candidates = %d, want both directions", employee)
	}
	if invoice != 1 {
		t.Fatalf("nested invoice candidate count = %d", invoice)
	}
	order, err := DiscoverJoinCandidates([]byte("from: {schema: main, name: Order}\n"), joinSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != 1 || len(order[0].Fields) != 2 {
		t.Fatalf("composite FK was split: %#v", order)
	}
}

func TestJoinCandidatesSuppressOnlyExactActiveInstanceEdge(t *testing.T) {
	query := []byte(`from:
  name: Invoice
  alias: purchase
  joins:
    - from: {name: Customer, alias: buyer}
      on:
        - {left: {field: CustomerId, source: buyer}, op: '==', right: {field: CustomerId, source: purchase}}
    - from: {name: Invoice, alias: refund}
      on:
        - {left: {field: InvoiceId, source: purchase}, op: '==', right: {field: InvoiceId, source: refund}}
limit: 5
`)
	candidates, err := DiscoverJoinCandidates(query, joinSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	var rootToCustomer, refundToCustomer, buyerToInvoice bool
	for _, candidate := range candidates {
		if candidate.Source.ID == "root" && candidate.Target.Relation == "Customer" {
			rootToCustomer = true
		}
		if candidate.Source.ID == "root/1" && candidate.Target.Relation == "Customer" {
			refundToCustomer = true
		}
		if candidate.Source.ID == "root/0" && candidate.Target.Relation == "Invoice" {
			buyerToInvoice = true
		}
	}
	if rootToCustomer || !refundToCustomer || buyerToInvoice {
		t.Fatalf("active edge suppression lost instance identity: %#v", candidates)
	}
}

func TestCompositeJoinSuppressionIgnoresPredicateOrder(t *testing.T) {
	query := []byte(`from:
  name: Order
  alias: o
  joins:
    - from: {name: Location, alias: l}
      on:
        - {left: {field: Code, source: l}, op: '==', right: {field: Location, source: o}}
        - {left: {field: Country, source: o}, op: '==', right: {field: Country, source: l}}
`)
	candidates, err := DiscoverJoinCandidates(query, joinSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range candidates {
		if candidate.Source.ID == "root" && candidate.Target.Relation == "Location" {
			t.Fatalf("active composite relationship remained: %#v", candidate)
		}
	}
}

func TestDeriveJoinDTQLUsesCollisionSafeAliasAndPreservesLimit(t *testing.T) {
	parent := []byte("from: {schema: main, name: Invoice, alias: Customer}\nlimit: 7\n")
	candidates, err := DiscoverJoinCandidates(parent, joinSnapshot())
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates: %#v, %v", candidates, err)
	}
	derived, candidate, err := DeriveJoinDTQL(parent, joinSnapshot(), candidates[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	text := string(derived)
	for _, want := range []string{"name: Invoice", "name: Customer", "alias: Customer2", "limit: 7", "source: Customer"} {
		if !strings.Contains(text, want) {
			t.Fatalf("derived DTQL missing %q:\n%s", want, text)
		}
	}
	if candidate.ConstraintID != "fk_invoice_customer" {
		t.Fatalf("candidate = %#v", candidate)
	}
}

func TestDeriveJoinDTQLQualifiesAndExpandsProjection(t *testing.T) {
	parent := []byte(`from: {name: Invoice}
columns:
  - wildcard: {exclude: [Total]}
where: {op: '==', left: {field: CustomerId}, right: {value: 58}}
orderBy:
  - field: InvoiceDate
    desc: true
limit: 10
`)
	candidates, err := DiscoverJoinCandidates(parent, joinSnapshot())
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates = %#v, %v", candidates, err)
	}
	derived, _, err := DeriveJoinDTQL(parent, joinSnapshot(), candidates[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	text := string(derived)
	for _, want := range []string{"source: Invoice", "field: InvoiceId", "field: CustomerId", "field: InvoiceDate", "Customer_FirstName", "limit: 10"} {
		if !strings.Contains(text, want) {
			t.Fatalf("derived DTQL missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "field: Total") || strings.Contains(text, "wildcard:") {
		t.Fatalf("excluded/wildcard source was not expanded:\n%s", text)
	}
}

func TestDeriveJoinDTQLRefusesAggregateWithoutGuessing(t *testing.T) {
	parent := []byte(`from: {name: Invoice}
columns:
  - aggregate: {function: COUNT, args: [{star: true}]}
    as: Orders
limit: 10
`)
	candidates, err := DiscoverJoinCandidates(parent, joinSnapshot())
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates = %#v, %v", candidates, err)
	}
	if _, _, err := DeriveJoinDTQL(parent, joinSnapshot(), candidates[0].ID); err == nil || !strings.Contains(err.Error(), "aggregate") {
		t.Fatalf("aggregate JOIN should refuse clearly: %v", err)
	}
}

func TestDeriveJoinDTQLRefusesImplicitMultiRelationWildcard(t *testing.T) {
	snapshot := joinSnapshot()
	snapshot.Keys = append(snapshot.Keys, ForeignKey{ConstraintID: "fk_customer_employee", Schema: "main", FromRelation: "Customer", FromFields: []string{"EmployeeId"}, ToSchema: "main", ToRelation: "Employee", ToFields: []string{"EmployeeId"}})
	doc := []byte(`from:
  name: Invoice
  alias: i
  joins:
    - from: {name: Customer, alias: c}
      on: [{left: {field: CustomerId, source: i}, op: '==', right: {field: CustomerId, source: c}}]
limit: 5
`)
	candidates, err := DiscoverJoinCandidates(doc, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range candidates {
		if candidate.Source.Relation == "Customer" && candidate.Target.Relation == "Employee" {
			if _, _, err := DeriveJoinDTQL(doc, snapshot, candidate.ID); err == nil || !strings.Contains(err.Error(), "implicit wildcard") {
				t.Fatalf("multi-relation wildcard must not lose Customer columns: %v", err)
			}
			return
		}
	}
	t.Fatal("Customer to Employee candidate missing")
}

func TestDuplicateIdenticalForeignKeysKeepUnappliedCandidate(t *testing.T) {
	ctx := context.Background()
	snapshot := joinSnapshot()
	snapshot.Keys = append(snapshot.Keys, ForeignKey{ConstraintID: "fk_invoice_customer_duplicate", Schema: "main", FromRelation: "Invoice", FromFields: []string{"CustomerId"}, ToSchema: "main", ToRelation: "Customer", ToFields: []string{"CustomerId"}})
	parent := RecordSet{ID: "base", Source: snapshot.Source, Title: "Invoices", DTQL: "from: {name: Invoice}\ncolumns: [{field: InvoiceId}]\nlimit: 5\n"}
	app := ForeignKeyJoinApplication{Source: snapshot.Source, Snapshot: snapshot, Executor: &joinExecutorStub{}}
	candidates, err := app.Candidates(ctx, parent)
	if err != nil || len(candidates) != 2 {
		t.Fatalf("duplicate FK candidates = %#v, %v", candidates, err)
	}
	result, err := app.Apply(ctx, parent, candidates[0].ID)
	if err != nil || result.Lineage == nil || len(result.Lineage.AppliedEdges) != 1 {
		t.Fatalf("applied duplicate FK = %+v, %v", result.Lineage, err)
	}
	joined := RecordSet{ID: "joined", Source: snapshot.Source, DTQL: result.DTQL, Lineage: result.Lineage}
	next, err := app.Candidates(ctx, joined)
	if err != nil {
		t.Fatal(err)
	}
	var remaining []JoinCandidate
	for _, candidate := range next {
		if candidate.Source.ID == "root" && candidate.Target.Relation == "Customer" {
			remaining = append(remaining, candidate)
		}
	}
	if len(remaining) != 1 || remaining[0].ConstraintID == candidates[0].ConstraintID {
		t.Fatalf("applied edge suppression also hid distinct constraint: %#v", remaining)
	}
}

func TestRestrictedBaseDoesNotAdvertiseUnexecutableJoin(t *testing.T) {
	parent := RecordSet{ID: "base", Source: "sqlite:///fixture.db", DTQL: "from: {name: Invoice}\nlimit: 5\n"}
	executor := &joinExecutorStub{}
	app := ForeignKeyJoinApplication{Source: parent.Source, Snapshot: joinSnapshot(), Executor: executor, Secure: true,
		CanReadTarget: func(_ context.Context, target RelationInstance) error {
			if target.Relation == "Invoice" {
				return fmt.Errorf("base has row policy")
			}
			return nil
		},
	}
	candidates, err := app.Candidates(context.Background(), parent)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("restricted base advertised candidate: %#v, %v", candidates, err)
	}
	if _, err := app.Apply(context.Background(), parent, JoinCandidateID("any")); err == nil || executor.doc != "" {
		t.Fatalf("restricted base JOIN executed: %v, doc=%q", err, executor.doc)
	}
}

type joinExecutorStub struct{ doc string }

func (s *joinExecutorStub) RunDTQL(_ context.Context, _ string, doc []byte, _ map[string]any) (secureread.Result, error) {
	s.doc = string(doc)
	return secureread.Result{Columns: []string{"InvoiceId", "Customer_CustomerId"}}, nil
}

func TestSessionChatApplyJoinCandidatePersistsLineage(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	chat, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, chat.activeID, "show invoices")
	if err != nil {
		t.Fatal(err)
	}
	base, err := store.AppendQuery(ctx, chat.activeID, user.ID, "sqlite:///fixture.db", QueryResult{Title: "Invoices", DTQL: "from: {schema: main, name: Invoice}\n", Result: secureread.Result{Columns: []string{"InvoiceId"}}})
	if err != nil {
		t.Fatal(err)
	}
	executor := &joinExecutorStub{}
	chat.ConfigureJoinApplication(ForeignKeyJoinApplication{Source: "sqlite:///fixture.db", Snapshot: joinSnapshot(), Executor: executor})
	candidates, err := chat.JoinCandidates(ctx, base.RecordSetID)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates: %#v, %v", candidates, err)
	}
	joined, err := chat.ApplyJoinCandidate(ctx, base.RecordSetID, candidates[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if joined.Lineage == nil || joined.Lineage.ParentRecordSetID != base.RecordSetID || joined.Lineage.CandidateID != candidates[0].ID || len(joined.Lineage.AppliedEdges) != 1 {
		t.Fatalf("lineage: %#v", joined.Lineage)
	}
	if !strings.Contains(executor.doc, "name: Customer") {
		t.Fatalf("executor did not receive derived DTQL: %s", executor.doc)
	}
	reopened, err := store.Load(ctx, chat.activeID)
	if err != nil || reopened.RecordSets[joined.ID].Lineage == nil || len(reopened.RecordSets[joined.ID].Lineage.AppliedEdges) != 1 {
		t.Fatalf("reloaded lineage: %#v, %v", reopened.RecordSets[joined.ID], err)
	}
}
