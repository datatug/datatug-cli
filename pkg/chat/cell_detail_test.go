package chat

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

// TestEnterOpensAndClosesCellDetailWithoutChangingSelection was ported onto
// ChatUI's cellDetailOverlay in chatui_inspector_test.go.

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

// TestCopyValueFormatsThroughFormatValue covers cellDetail.copyValue's own
// (trivial but uncovered) delegation to FormatValue.
func TestCopyValueFormatsThroughFormatValue(t *testing.T) {
	d := &cellDetail{value: int64(42)}
	if got, want := d.copyValue(), FormatValue(int64(42)); got != want {
		t.Fatalf("copyValue() = %q, want %q", got, want)
	}
}

func TestPreviewRelatedSourceMismatchIsANoOp(t *testing.T) {
	application := ForeignKeyJoinApplication{Source: "sqlite:///fixture.db", Snapshot: joinSnapshot(), Executor: &joinExecutorStub{}}
	preview, err := application.PreviewRelated(context.Background(), RecordSet{Source: "sqlite:///other.db"}, "main.Invoice.CustomerId", map[string]any{"main.invoice.customerid": 7})
	if err != nil || preview != nil {
		t.Fatalf("source mismatch should be a silent no-op: preview=%#v err=%v", preview, err)
	}
}

func TestPreviewRelatedPropagatesSnapshotError(t *testing.T) {
	// ForeignKeyJoinApplication.snapshot errors when its own static Snapshot
	// wasn't loaded for this Source and no Refresh callback is set (join.go).
	application := ForeignKeyJoinApplication{Source: "sqlite:///fixture.db", Snapshot: ForeignKeySnapshot{Source: "sqlite:///different.db"}, Executor: &joinExecutorStub{}}
	_, err := application.PreviewRelated(context.Background(), RecordSet{Source: application.Source}, "main.Invoice.CustomerId", map[string]any{"main.invoice.customerid": 7})
	if err == nil {
		t.Fatal("expected a snapshot error to propagate")
	}
}

// TestPreviewRelatedRespectsCanReadTarget covers both CanReadTarget
// branches: a denied target is skipped, and Secure with no CanReadTarget
// callback at all denies every target (fail closed).
func TestPreviewRelatedRespectsCanReadTarget(t *testing.T) {
	row := map[string]any{"main.invoice.customerid": 7}

	t.Run("CanReadTarget denies the target", func(t *testing.T) {
		executor := &joinExecutorStub{}
		application := ForeignKeyJoinApplication{
			Source: "sqlite:///fixture.db", Snapshot: joinSnapshot(), Executor: executor,
			CanReadTarget: func(context.Context, RelationInstance) error { return errors.New("denied") },
		}
		preview, err := application.PreviewRelated(context.Background(), RecordSet{Source: application.Source}, "main.Invoice.CustomerId", row)
		if err != nil || len(preview) != 0 || executor.doc != "" {
			t.Fatalf("denied target should be skipped: preview=%#v err=%v doc=%q", preview, err, executor.doc)
		}
	})

	t.Run("Secure with no CanReadTarget denies every target", func(t *testing.T) {
		executor := &joinExecutorStub{}
		application := ForeignKeyJoinApplication{Source: "sqlite:///fixture.db", Snapshot: joinSnapshot(), Executor: executor, Secure: true}
		preview, err := application.PreviewRelated(context.Background(), RecordSet{Source: application.Source}, "main.Invoice.CustomerId", row)
		if err != nil || len(preview) != 0 || executor.doc != "" {
			t.Fatalf("Secure with no CanReadTarget should fail closed: preview=%#v err=%v doc=%q", preview, err, executor.doc)
		}
	})
}

// TestPreviewRelatedPropagatesExecutorError covers the RunDTQL error
// passthrough (dtql.Serialize itself has no realistic failure mode here --
// the query is always built from the same fixed shape).
func TestPreviewRelatedPropagatesExecutorError(t *testing.T) {
	executor := &fakeExecutor{err: errors.New("query failed")}
	application := ForeignKeyJoinApplication{Source: "sqlite:///fixture.db", Snapshot: joinSnapshot(), Executor: executor}
	_, err := application.PreviewRelated(context.Background(), RecordSet{Source: application.Source}, "main.Invoice.CustomerId", map[string]any{"main.invoice.customerid": 7})
	if err == nil || !strings.Contains(err.Error(), "query failed") {
		t.Fatalf("err = %v, want the executor's own error", err)
	}
}

// TestPreviewRelatedSkipsFieldThatDoesNotMatchAnyForeignKey covers the
// found==false branch: the selected column's prefix names a real FK's
// schema/relation, but the field itself isn't one of that FK's FromFields.
func TestPreviewRelatedSkipsFieldThatDoesNotMatchAnyForeignKey(t *testing.T) {
	executor := &joinExecutorStub{}
	application := ForeignKeyJoinApplication{Source: "sqlite:///fixture.db", Snapshot: joinSnapshot(), Executor: executor}
	preview, err := application.PreviewRelated(context.Background(), RecordSet{Source: application.Source}, "main.Invoice.InvoiceDate", map[string]any{"main.invoice.invoicedate": "2024-01-01"})
	if err != nil || len(preview) != 0 || executor.doc != "" {
		t.Fatalf("non-FK field must not query: preview=%#v err=%v doc=%q", preview, err, executor.doc)
	}
}

// TestPreviewRelatedQualifiesNonMainSchemaTarget covers the ToSchema != ""
// && != "main" branch (dal.NewQualifiedRootCollectionRef): every FK in
// joinSnapshot() targets "main", so this needs its own snapshot with a
// differently-schema'd target.
func TestPreviewRelatedQualifiesNonMainSchemaTarget(t *testing.T) {
	snapshot := ForeignKeySnapshot{Source: "sqlite:///fixture.db", Columns: map[string][]string{
		"main.invoice":  {"InvoiceId", "CustomerId"},
		"lookup.status": {"StatusId"},
	}, Keys: []ForeignKey{
		{ConstraintID: "fk_invoice_status", Schema: "main", FromRelation: "Invoice", FromFields: []string{"CustomerId"}, ToSchema: "lookup", ToRelation: "Status", ToFields: []string{"StatusId"}},
	}}
	executor := &joinExecutorStub{}
	application := ForeignKeyJoinApplication{Source: "sqlite:///fixture.db", Snapshot: snapshot, Executor: executor}
	preview, err := application.PreviewRelated(context.Background(), RecordSet{Source: application.Source}, "main.Invoice.CustomerId", map[string]any{"main.invoice.customerid": 7})
	if err != nil || len(preview) != 1 {
		t.Fatalf("preview = %#v, err = %v", preview, err)
	}
	if !strings.Contains(executor.doc, "lookup") {
		t.Fatalf("qualified schema target missing from DTQL: %s", executor.doc)
	}
}

func TestPreviewRelatedPropagatesSerializeError(t *testing.T) {
	restore := serializePreviewQuery
	t.Cleanup(func() { serializePreviewQuery = restore })
	serializeErr := errors.New("serialize failed")
	serializePreviewQuery = func(dal.StructuredQuery) ([]byte, error) { return nil, serializeErr }
	application := ForeignKeyJoinApplication{Source: "sqlite:///fixture.db", Snapshot: joinSnapshot(), Executor: &joinExecutorStub{}}
	_, err := application.PreviewRelated(context.Background(), RecordSet{Source: application.Source}, "main.Invoice.CustomerId", map[string]any{"main.invoice.customerid": 7})
	if !errors.Is(err, serializeErr) {
		t.Fatalf("err = %v, want %v", err, serializeErr)
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
