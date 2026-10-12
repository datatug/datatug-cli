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

type parameterLookupStub struct {
	result *SavedQueryLookup
	err    error
}

func (s parameterLookupStub) List(context.Context) ([]SavedQuery, error) { return nil, nil }
func (s parameterLookupStub) Run(context.Context, string) (QueryResult, error) {
	return QueryResult{}, nil
}
func (s parameterLookupStub) LookupParameter(context.Context, string, string) (*SavedQueryLookup, error) {
	return s.result, s.err
}

func TestSavedQueryFKLookupDiscovery(t *testing.T) {
	snapshot := joinSnapshot()
	plan := DiscoverSavedQueryLookup(snapshot, "Invoice", "CustomerId")
	if plan == nil || plan.Relation != "Customer" || plan.Key != "CustomerId" || !strings.Contains(string(plan.Document()), "limit: 100") {
		t.Fatalf("FK lookup plan = %+v", plan)
	}
	for _, pair := range [][2]string{{"Invoice", "InvoiceId"}, {"Order", "Country"}} {
		if got := DiscoverSavedQueryLookup(snapshot, pair[0], pair[1]); got != nil {
			t.Fatalf("%v unexpectedly offered scalar lookup: %+v", pair, got)
		}
	}
}

func TestSavedQueryFKLookupReferencedMetaAndDTQLBinding(t *testing.T) {
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
	snapshot, err := LoadSQLiteForeignKeySnapshot(context.Background(), source, db)
	if err != nil {
		t.Fatal(err)
	}
	// The demo query has Meta Customer.ID while the actual FK is
	// Invoice.CustomerId -> Customer.CustomerId.
	doc := []byte("from: {schema: main, name: Invoice, alias: i}\ncolumns:\n  - field: InvoiceId\nwhere: {op: '==', left: {field: CustomerId}, right: {param: CustomerId}}\n")
	plan := DiscoverSavedQueryParameterLookup(doc, snapshot, "CustomerId", "main.Customer", "ID")
	if plan == nil || plan.Relation != "Customer" || plan.Key != "CustomerId" {
		t.Fatalf("demo-style FK lookup = %+v", plan)
	}
	if got := DiscoverSavedQueryParameterLookup(doc, snapshot, "other", "Customer", "ID"); got != nil {
		t.Fatalf("unbound parameter unexpectedly matched: %+v", got)
	}
	if got := DiscoverSavedQueryParameterLookup(doc, snapshot, "CustomerId", "Customer", "WrongId"); got != nil {
		t.Fatalf("wrong target field unexpectedly matched: %+v", got)
	}
}

func TestSavedQueryMultiFKBindingRequiresDeclarationAndOperator(t *testing.T) {
	snapshot := joinSnapshot()
	for _, op := range []string{"In", "NotIn"} {
		doc := []byte("from: {name: Invoice}\nwhere: {op: " + op + ", left: {field: CustomerId}, right: {param: CustomerIds}}\n")
		plan := DiscoverSavedQueryParameterLookupWithMode(doc, snapshot, "CustomerIds", "Customer", "ID", true)
		if plan == nil || !plan.Multi || plan.Key != "CustomerId" {
			t.Fatalf("%s multi lookup = %+v", op, plan)
		}
		if got := DiscoverSavedQueryParameterLookupWithMode(doc, snapshot, "CustomerIds", "Customer", "ID", false); got != nil {
			t.Fatalf("%s scalar declaration matched: %+v", op, got)
		}
	}
	for _, op := range []string{"==", ">"} {
		doc := []byte("from: {name: Invoice}\nwhere: {op: '" + op + "', left: {field: CustomerId}, right: {param: CustomerIds}}\n")
		if got := DiscoverSavedQueryParameterLookupWithMode(doc, snapshot, "CustomerIds", "Customer", "ID", true); got != nil {
			t.Fatalf("%s multi declaration matched: %+v", op, got)
		}
	}
}

func TestSavedQueryMultiFKInExecutesChinook(t *testing.T) {
	path, err := filepath.Abs(filepath.Join("..", "dbcopy", "testdata", "chinook.db"))
	if err != nil {
		t.Fatal(err)
	}
	source := "sqlite://" + path
	doc := []byte("from: {name: Invoice}\ncolumns:\n  - field: InvoiceId\n  - field: CustomerId\nwhere: {op: In, left: {field: CustomerId}, right: {param: CustomerIds}}\nlimit: 20\n")
	result, err := secureread.NewExecutor(secureread.Session{Unrestricted: true}).RunDTQL(context.Background(), source, doc, map[string]any{"CustomerIds": []any{1, 2}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) == 0 {
		t.Fatal("In query returned no Chinook invoices")
	}
	for _, row := range result.Rows {
		id := fmt.Sprint(row.Data["CustomerId"])
		if id != "1" && id != "2" {
			t.Fatalf("unexpected CustomerId %s", id)
		}
	}
}

func TestSavedQueryMultiFKNotInExecutesChinook(t *testing.T) {
	path, err := filepath.Abs(filepath.Join("..", "dbcopy", "testdata", "chinook.db"))
	if err != nil {
		t.Fatal(err)
	}
	source := "sqlite://" + path
	doc := []byte("from: {name: Invoice}\ncolumns:\n  - field: InvoiceId\n  - field: CustomerId\nwhere: {op: NotIn, left: {field: CustomerId}, right: {param: CustomerIds}}\nlimit: 20\n")
	result, err := secureread.NewExecutor(secureread.Session{Unrestricted: true}).RunDTQL(context.Background(), source, doc, map[string]any{"CustomerIds": []any{1, 2}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) == 0 {
		t.Fatal("NotIn query returned no Chinook invoices")
	}
	for _, row := range result.Rows {
		id := fmt.Sprint(row.Data["CustomerId"])
		if id == "1" || id == "2" {
			t.Fatalf("excluded CustomerId %s was returned", id)
		}
	}
}
