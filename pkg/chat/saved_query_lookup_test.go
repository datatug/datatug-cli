package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
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

func TestSavedQueryLookupFirstHundredBoundaryAndManualKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lookup.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`CREATE TABLE Customer (CustomerId INTEGER PRIMARY KEY, Name TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 101; i++ {
		if _, err := db.Exec(`INSERT INTO Customer (CustomerId, Name) VALUES (?, ?)`, i, fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	plan := SavedQueryLookupPlan{Relation: "Customer", Key: "CustomerId", Columns: []string{"CustomerId", "Name"}}
	result, err := secureread.NewExecutor(secureread.Session{Unrestricted: true}).RunDTQL(context.Background(), "sqlite://"+path, plan.Document(), nil)
	if err != nil || len(result.Rows) != savedQueryLookupLimit {
		t.Fatalf("bounded lookup rows = %d, %v", len(result.Rows), err)
	}
	if fmt.Sprint(result.Rows[len(result.Rows)-1].Data["CustomerId"]) != "100" {
		t.Fatalf("lookup did not return the first 100 keys: %+v", result.Rows[len(result.Rows)-1])
	}
	u := &UI{ctx: context.Background(), width: 100, height: 35, savedQueryService: parameterLookupStub{result: &SavedQueryLookup{Key: plan.Key, Result: result}}}
	u.openQueryParametersDialog(SavedQuery{ID: "q", Parameters: []SavedQueryParameter{{ID: "CustomerId", Required: true, Entity: "Invoice", Field: "CustomerId"}}})
	command := u.updateQueryParametersDialog(tea.KeyPressMsg{Code: tea.KeyEnter})
	u.receiveParameterLookup(command().(parameterLookupMessage))
	if !strings.Contains(u.parameterLookupOverlay(""), "First 100") {
		t.Fatal("bounded lookup did not explain the limit")
	}
	u.updateQueryParametersDialog(tea.KeyPressMsg{Code: tea.KeyEscape})
	u.queryParameters.inputs[0].SetValue("101")
	parsed, err := accesspolicies.ParseVariables([]string{"CustomerId=" + u.queryParameters.inputs[0].Value()})
	if err != nil || parsed["CustomerId"] != 101 {
		t.Fatalf("unlisted key was not enterable: %#v, %v", parsed, err)
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

func TestSavedQueryMultiFKPickerPreservesSelectionAcrossFilter(t *testing.T) {
	u := &UI{ctx: context.Background(), width: 100, height: 35, savedQueryService: parameterLookupStub{result: &SavedQueryLookup{Key: "CustomerId", Multi: true, Result: secureread.Result{Columns: []string{"CustomerId", "Name"}, Rows: []secureread.Row{{Data: map[string]any{"CustomerId": int64(5), "Name": "Alice"}}, {Data: map[string]any{"CustomerId": int64(6), "Name": "Bob"}}}}}}}
	u.openQueryParametersDialog(SavedQuery{ID: "q", Parameters: []SavedQueryParameter{{ID: "CustomerIds", Required: true, Multi: true, Entity: "Customer", Field: "ID"}}})
	command := u.updateQueryParametersDialog(tea.KeyPressMsg{Code: tea.KeyEnter})
	u.receiveParameterLookup(command().(parameterLookupMessage))
	u.updateQueryParametersDialog(tea.KeyPressMsg{Code: tea.KeyEnter})
	if u.queryParameters.lookup == nil || !strings.Contains(u.queryParameters.err, "at least one") {
		t.Fatal("zero selection was accepted")
	}
	u.updateQueryParametersDialog(tea.KeyPressMsg{Code: tea.KeySpace})
	if !strings.Contains(u.queryParameters.lookup.grid.model.Rows[0][0], "☑") {
		t.Fatal("selected marker missing")
	}
	for _, r := range "Bob" {
		u.updateQueryParametersDialog(tea.KeyPressMsg{Text: string(r)})
	}
	if len(u.queryParameters.lookup.selected) != 1 || len(u.queryParameters.lookup.grid.model.Rows) != 1 {
		t.Fatal("filter lost selection")
	}
	u.updateQueryParametersDialog(tea.KeyPressMsg{Code: tea.KeySpace})
	if len(u.queryParameters.lookup.selected) != 2 {
		t.Fatal("second row not selected")
	}
	u.updateQueryParametersDialog(tea.KeyPressMsg{Code: tea.KeyEnter})
	if u.queryParameters.lookup != nil {
		t.Fatal("confirm did not close picker")
	}
	parsed, err := accesspolicies.ParseVariables([]string{"CustomerIds=" + u.queryParameters.inputs[0].Value()})
	if err != nil || !reflect.DeepEqual(parsed["CustomerIds"], []any{5, 6}) {
		t.Fatalf("typed selection = %v, %v", parsed, err)
	}
}

func TestSavedQueryMultiFKPickerQuotesStringKeys(t *testing.T) {
	value := "x,] \\\"quoted\\\""
	u := &UI{ctx: context.Background(), width: 100, height: 35, savedQueryService: parameterLookupStub{result: &SavedQueryLookup{Key: "Code", Multi: true, Result: secureread.Result{Columns: []string{"Code"}, Rows: []secureread.Row{{Data: map[string]any{"Code": value}}}}}}}
	u.openQueryParametersDialog(SavedQuery{ID: "q", Parameters: []SavedQueryParameter{{ID: "Codes", Required: true, Multi: true, Entity: "Item", Field: "ID"}}})
	command := u.updateQueryParametersDialog(tea.KeyPressMsg{Code: tea.KeyEnter})
	u.receiveParameterLookup(command().(parameterLookupMessage))
	u.updateQueryParametersDialog(tea.KeyPressMsg{Code: tea.KeySpace})
	u.updateQueryParametersDialog(tea.KeyPressMsg{Code: tea.KeyEnter})
	parsed, err := accesspolicies.ParseVariables([]string{"Codes=" + u.queryParameters.inputs[0].Value()})
	if err != nil || !reflect.DeepEqual(parsed["Codes"], []any{value}) {
		t.Fatalf("string selection = %v, %v", parsed, err)
	}
}

func TestSavedQueryScalarFKPickerPreservesStringKey(t *testing.T) {
	u := &UI{ctx: context.Background(), width: 100, height: 35, savedQueryService: parameterLookupStub{result: &SavedQueryLookup{Key: "Code", Result: secureread.Result{Columns: []string{"Code"}, Rows: []secureread.Row{{Data: map[string]any{"Code": "001"}}}}}}}
	u.openQueryParametersDialog(SavedQuery{ID: "q", Parameters: []SavedQueryParameter{{ID: "Code", Required: true, Entity: "Item", Field: "Code"}}})
	command := u.updateQueryParametersDialog(tea.KeyPressMsg{Code: tea.KeyEnter})
	u.receiveParameterLookup(command().(parameterLookupMessage))
	u.updateQueryParametersDialog(tea.KeyPressMsg{Code: tea.KeyEnter})
	parsed, err := accesspolicies.ParseVariables([]string{"Code=" + u.queryParameters.inputs[0].Value()})
	if err != nil || parsed["Code"] != "001" {
		t.Fatalf("selected string key lost its type: %#v, %v", parsed, err)
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

func TestSavedQueryMultiFKPolicyErrorKeepsManualInput(t *testing.T) {
	u := &UI{ctx: context.Background(), savedQueryService: parameterLookupStub{err: errors.New("policy denied secret")}}
	u.openQueryParametersDialog(SavedQuery{ID: "q", Parameters: []SavedQueryParameter{{ID: "CustomerIds", Required: true, Multi: true, Entity: "Customer", Field: "ID"}}})
	command := u.updateQueryParametersDialog(tea.KeyPressMsg{Code: tea.KeyEnter})
	u.receiveParameterLookup(command().(parameterLookupMessage))
	if u.queryParameters.lookup != nil || !strings.Contains(u.queryParameters.err, "access denied") || strings.Contains(u.queryParameters.err, "secret") {
		t.Fatalf("policy error state = %+v", u.queryParameters)
	}
	u.queryParameters.inputs[0].SetValue("[1, 2]")
	if got := u.queryParameters.inputs[0].Value(); got != "[1, 2]" {
		t.Fatalf("manual array lost: %q", got)
	}
}

func TestSavedQueryFKLookupFilterAndSelect(t *testing.T) {
	u := &UI{ctx: context.Background(), width: 100, height: 35, savedQueryService: parameterLookupStub{result: &SavedQueryLookup{Key: "CustomerId", Result: secureread.Result{Columns: []string{"CustomerId", "Name"}, Rows: []secureread.Row{{Data: map[string]any{"CustomerId": 5, "Name": "Alice"}}, {Data: map[string]any{"CustomerId": 6, "Name": "Bob"}}}}}}}
	u.openQueryParametersDialog(SavedQuery{ID: "q", Parameters: []SavedQueryParameter{{ID: "CustomerId", Required: true, Entity: "Invoice", Field: "CustomerId"}}})
	command := u.updateQueryParametersDialog(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command == nil {
		t.Fatal("FK field did not request lookup")
	}
	u.receiveParameterLookup(command().(parameterLookupMessage))
	if u.queryParameters.lookup == nil {
		t.Fatal("lookup did not open")
	}
	for _, r := range "Bob" {
		u.updateQueryParametersDialog(tea.KeyPressMsg{Text: string(r)})
	}
	if len(u.queryParameters.lookup.grid.model.Rows) != 1 {
		t.Fatal("filter did not narrow rows")
	}
	u.updateQueryParametersDialog(tea.KeyPressMsg{Code: tea.KeyEnter})
	if value := u.queryParameters.inputs[0].Value(); value != "6" {
		t.Fatalf("selected key = %q", value)
	}
}

func TestSavedQueryFKLookupPolicyErrorKeepsManualInput(t *testing.T) {
	u := &UI{ctx: context.Background(), savedQueryService: parameterLookupStub{err: errors.New("policy denied private field")}}
	u.openQueryParametersDialog(SavedQuery{ID: "q", Parameters: []SavedQueryParameter{{ID: "CustomerId", Required: true, Entity: "Invoice", Field: "CustomerId"}}})
	command := u.updateQueryParametersDialog(tea.KeyPressMsg{Code: tea.KeyEnter})
	u.receiveParameterLookup(command().(parameterLookupMessage))
	if u.queryParameters.lookup != nil || !strings.Contains(u.queryParameters.err, "access denied") || strings.Contains(u.queryParameters.err, "private field") {
		t.Fatalf("policy error state = %+v", u.queryParameters)
	}
}

func TestSavedQueryFKLookupNoFKFallsBackToManualInput(t *testing.T) {
	u := &UI{ctx: context.Background(), savedQueryService: parameterLookupStub{}}
	u.openQueryParametersDialog(SavedQuery{ID: "q", Parameters: []SavedQueryParameter{{ID: "CustomerId", Required: true, Entity: "Invoice", Field: "CustomerId"}}})
	command := u.updateQueryParametersDialog(tea.KeyPressMsg{Code: tea.KeyEnter})
	u.receiveParameterLookup(command().(parameterLookupMessage))
	if u.queryParameters.lookup != nil || !strings.Contains(u.queryParameters.err, "No single-column") {
		t.Fatal("missing FK did not retain manual entry")
	}
	u.queryParameters.inputs[0].SetValue("42")
	if u.queryParameters.inputs[0].Value() != "42" {
		t.Fatal("manual input was lost")
	}
}
