package chat

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

func TestEnterOpensAndClosesCellDetailWithoutChangingSelection(t *testing.T) {
	u := NewUI(context.Background(), nil, "stub")
	u.appendTurn(Turn{Queries: []QueryResult{{Title: "Invoices", Result: secureread.Result{Columns: []string{"InvoiceId", "CustomerId"}, Rows: []secureread.Row{{Data: map[string]any{"InvoiceId": 7, "CustomerId": 1}}}}}}})
	if !u.focusLatestGrid() {
		t.Fatal("grid unavailable")
	}
	if _, handled := u.updateGrid(tea.KeyPressMsg{Code: tea.KeyEnter}); !handled || u.detail == nil {
		t.Fatal("Enter did not open cell detail")
	}
	if !strings.Contains(u.View().Content, "Cell · InvoiceId") || !strings.Contains(u.View().Content, "CustomerId: 1") {
		t.Fatal("dialog missing row or cell")
	}
	if _, cmd := u.Update(tea.KeyPressMsg{Code: tea.KeyEsc}); cmd != nil || u.detail != nil || !u.gridFocused {
		t.Fatal("Esc did not return to grid")
	}
}

func TestPreviewRelatedUsesDTQLAndStructuredResult(t *testing.T) {
	executor := &joinExecutorStub{}
	application := ForeignKeyJoinApplication{Source: "sqlite:///fixture.db", Snapshot: joinSnapshot(), Executor: executor}
	record := RecordSet{Source: application.Source}
	preview, err := application.PreviewRelated(context.Background(), record, "main.Invoice.CustomerId", map[string]any{"main.invoice.customerid": 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview) != 1 || preview[0].key.ToRelation != "Customer" {
		t.Fatalf("unexpected preview: %#v", preview)
	}
	if !strings.Contains(executor.doc, "Customer") || !strings.Contains(executor.doc, "CustomerId") || !strings.Contains(executor.doc, "limit: 5") {
		t.Fatalf("expected bounded DTQL: %s", executor.doc)
	}
}

func TestPreviewRelatedRequiresCompleteCompositeKey(t *testing.T) {
	executor := &joinExecutorStub{}
	application := ForeignKeyJoinApplication{Source: "sqlite:///fixture.db", Snapshot: joinSnapshot(), Executor: executor}
	preview, err := application.PreviewRelated(context.Background(), RecordSet{Source: application.Source}, "main.Order.Country", map[string]any{"main.order.country": "US"})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview) != 0 || executor.doc != "" {
		t.Fatalf("incomplete composite key must not query: %#v %q", preview, executor.doc)
	}
}

func TestChinookRelatedCustomerPreview(t *testing.T) {
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
	application := ForeignKeyJoinApplication{Source: source, Snapshot: snapshot, Executor: secureread.NewExecutor(secureread.Session{Unrestricted: true})}
	preview, err := application.PreviewRelated(ctx, RecordSet{Source: source}, "main.Invoice.CustomerId", map[string]any{"main.invoice.customerid": 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview) != 1 || len(preview[0].result.Rows) != 1 {
		t.Fatalf("expected real Chinook customer: %#v", preview)
	}
	if preview[0].result.Rows[0].Data["FirstName"] != "Luís" {
		t.Fatalf("wrong customer: %#v", preview[0].result.Rows[0].Data)
	}
}
