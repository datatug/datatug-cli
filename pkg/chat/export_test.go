package chat

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	godbf "github.com/LindsayBradford/go-dbf"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/ingr-io/ingr-go/ingr"
	"github.com/xuri/excelize/v2"
)

func exportFixture(title string) RecordSet {
	return RecordSet{Title: title, Result: secureread.Result{Columns: []string{"InvoiceId", "BillingCountry", "Total"}, Rows: []secureread.Row{{Data: map[string]any{"InvoiceId": 7, "BillingCountry": "Czech Republic", "Total": 12.5}}, {Data: map[string]any{"InvoiceId": 8, "BillingCountry": nil, "Total": 0}}}}}
}

func TestExportFlatFormats(t *testing.T) {
	for _, format := range []ExportFormat{ExportCSV, ExportJSON, ExportYAML, ExportINGR, ExportDBF} {
		t.Run(string(format), func(t *testing.T) {
			var output bytes.Buffer
			if err := ExportRecordSets(context.Background(), []RecordSet{exportFixture("Invoices")}, format, &output); err != nil {
				t.Fatal(err)
			}
			if output.Len() == 0 {
				t.Fatal("empty output")
			}
			switch format {
			case ExportCSV:
				rows, err := csv.NewReader(&output).ReadAll()
				if err != nil || len(rows) != 3 || rows[1][1] != "Czech Republic" {
					t.Fatalf("invalid CSV: %v %#v", err, rows)
				}
			case ExportJSON:
				var data struct {
					Columns []string `json:"columns"`
					Rows    [][]any  `json:"rows"`
				}
				if err := json.Unmarshal(output.Bytes(), &data); err != nil || len(data.Rows) != 2 || data.Columns[0] != "InvoiceId" {
					t.Fatalf("invalid JSON: %v %#v", err, data)
				}
			case ExportYAML:
				if !strings.Contains(output.String(), "- BillingCountry") || !strings.Contains(output.String(), "- Czech Republic") {
					t.Fatalf("invalid YAML: %s", output.String())
				}
			case ExportINGR:
				var rows []map[string]any
				if err := ingr.NewDecoder(&output).Decode(&rows); err != nil || len(rows) != 2 {
					t.Fatalf("invalid INGR: %v %#v", err, rows)
				}
			case ExportDBF:
				table, err := godbf.NewFromByteArray(output.Bytes(), "UTF8")
				if err != nil || table.NumberOfRecords() != 2 {
					t.Fatalf("invalid DBF: %v", err)
				}
				if table.Fields()[0].FieldType() != godbf.Numeric || table.Fields()[2].FieldType() != godbf.Float {
					t.Fatalf("DBF lost numeric field types: %#v", table.Fields())
				}
			}
		})
	}
}

func TestExportBucketXLSXAndSQLite(t *testing.T) {
	records := []RecordSet{exportFixture("Invoices"), exportFixture("Invoices")}
	for _, format := range []ExportFormat{ExportXLSX, ExportSQLite} {
		t.Run(string(format), func(t *testing.T) {
			var output bytes.Buffer
			if err := ExportRecordSets(context.Background(), records, format, &output); err != nil {
				t.Fatal(err)
			}
			if format == ExportXLSX {
				book, err := excelize.OpenReader(bytes.NewReader(output.Bytes()))
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = book.Close() }()
				if sheets := book.GetSheetList(); len(sheets) != 2 || sheets[0] == sheets[1] {
					t.Fatalf("wrong sheets: %#v", sheets)
				}
				value, err := book.GetCellValue("Invoices", "B2")
				if err != nil || value != "Czech Republic" {
					t.Fatalf("wrong XLSX value %q: %v", value, err)
				}
				return
			}
			path := filepath.Join(t.TempDir(), "bucket.sqlite")
			if err := os.WriteFile(path, output.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM "Invoices_2"`).Scan(&count); err != nil || count != 2 {
				t.Fatalf("wrong SQLite table: %v %d", err, count)
			}
		})
	}
}

func TestExportBucketZIPAndNoOverwrite(t *testing.T) {
	records := []RecordSet{exportFixture("Invoices"), exportFixture("Invoices")}
	var output bytes.Buffer
	if err := ExportRecordSets(context.Background(), records, ExportCSV, &output); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != 2 || archive.File[0].Name == archive.File[1].Name {
		t.Fatalf("wrong ZIP contents: %#v", archive.File)
	}
	var singleBucket bytes.Buffer
	if err := ExportBucket(context.Background(), records[:1], ExportCSV, &singleBucket); err != nil {
		t.Fatal(err)
	}
	if _, err := zip.NewReader(bytes.NewReader(singleBucket.Bytes()), int64(singleBucket.Len())); err != nil {
		t.Fatalf("single-item bucket must still be a ZIP: %v", err)
	}
	path := filepath.Join(t.TempDir(), "out.csv")
	if err := os.WriteFile(path, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ExportBucketFile(context.Background(), records[:1], ExportCSV, path); err == nil {
		t.Fatal("expected no-overwrite error")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "untouched" {
		t.Fatalf("existing file changed: %q %v", data, err)
	}
}

func TestXLSXValuesDoNotBecomeFormulas(t *testing.T) {
	record := RecordSet{Title: "Values", Result: secureread.Result{Columns: []string{"Text"}, Rows: []secureread.Row{{Data: map[string]any{"Text": "=1+1"}}}}}
	var output bytes.Buffer
	if err := ExportRecordSets(context.Background(), []RecordSet{record}, ExportXLSX, &output); err != nil {
		t.Fatal(err)
	}
	book, err := excelize.OpenReader(bytes.NewReader(output.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = book.Close() }()
	formula, err := book.GetCellFormula("Values", "A2")
	if err != nil || formula != "" {
		t.Fatalf("data became a formula: %q %v", formula, err)
	}
}

func TestExportRestoredNumericValues(t *testing.T) {
	record := RecordSet{Title: "Restored", Result: secureread.Result{Columns: []string{"ID", "Amount"}, Rows: []secureread.Row{{Data: map[string]any{"ID": json.Number("412"), "Amount": json.Number("1.99")}}}}}
	for _, format := range []ExportFormat{ExportXLSX, ExportSQLite} {
		var output bytes.Buffer
		if err := ExportRecordSets(context.Background(), []RecordSet{record}, format, &output); err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		if format == ExportSQLite {
			path := filepath.Join(t.TempDir(), "restored.sqlite")
			if err := os.WriteFile(path, output.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			var idType, amountType, amount string
			err = db.QueryRow(`SELECT typeof(ID), typeof(Amount), Amount FROM Restored`).Scan(&idType, &amountType, &amount)
			_ = db.Close()
			if err != nil || idType != "integer" || amount != "1.99" {
				t.Fatalf("restored numeric SQLite values: %s %s %s %v", idType, amountType, amount, err)
			}
		}
	}
}

func TestExportPreservesLongDecimal(t *testing.T) {
	const exact = "0.123456789012345678901"
	record := RecordSet{Title: "Precise", Result: secureread.Result{Columns: []string{"Amount"}, Rows: []secureread.Row{{Data: map[string]any{"Amount": json.Number(exact)}}}}}
	for _, format := range []ExportFormat{ExportJSON, ExportYAML, ExportINGR, ExportXLSX, ExportSQLite} {
		var output bytes.Buffer
		if err := ExportRecordSets(context.Background(), []RecordSet{record}, format, &output); err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		if format == ExportJSON || format == ExportYAML || format == ExportINGR {
			if !bytes.Contains(output.Bytes(), []byte(exact)) {
				t.Fatalf("%s rounded decimal: %s", format, output.String())
			}
		}
		if format == ExportXLSX {
			book, err := excelize.OpenReader(bytes.NewReader(output.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			value, err := book.GetCellValue("Precise", "A2")
			_ = book.Close()
			if err != nil || value != exact {
				t.Fatalf("XLSX rounded decimal: %q %v", value, err)
			}
		}
	}
}

func TestINGRExportPreservesSourceRowKey(t *testing.T) {
	record := RecordSet{Title: "Customers", Result: secureread.Result{Columns: []string{"Name"}, Rows: []secureread.Row{{Key: "customer-123", Data: map[string]any{"Name": "Ana"}}}}}
	var output bytes.Buffer
	if err := ExportRecordSets(context.Background(), []RecordSet{record}, ExportINGR, &output); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := ingr.NewDecoder(&output).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["$ID"] != "customer-123" {
		t.Fatalf("source key lost in INGR: %#v", rows)
	}
}

func TestExportBucketActionIsSessionScoped(t *testing.T) {
	session := ChatSession{RecordSets: map[string]RecordSet{"r1": {ID: "r1"}}}
	state := WorkspaceState{}
	var err error
	state, _, err = state.apply(session, ProjectCatalog{}, WorkspaceAction{Kind: "bucket_add", RecordSetID: "r1"})
	if err != nil || len(state.ExportBucket) != 1 {
		t.Fatalf("bucket add: %#v %v", state.ExportBucket, err)
	}
	state, _, err = state.apply(session, ProjectCatalog{}, WorkspaceAction{Kind: "bucket_add", RecordSetID: "r1"})
	if err != nil || len(state.ExportBucket) != 1 {
		t.Fatalf("bucket duplicate: %#v %v", state.ExportBucket, err)
	}
	if err := validateLoadedWorkspace(ChatSession{RecordSets: session.RecordSets, Workspace: state}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := state.apply(session, ProjectCatalog{}, WorkspaceAction{Kind: "bucket_add", RecordSetID: "missing"}); err == nil {
		t.Fatal("missing RecordSet accepted")
	}
}

func TestExportBucketSurvivesSessionReload(t *testing.T) {
	ctx := context.Background()
	path := testStorePath(t)
	store := openTestStore(t, path, testScope())
	chat, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", workspaceTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	session, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id := workspaceTestRecord(t, store, session.ID)
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "bucket_add", RecordSetID: id}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path, testScope())
	restored, err := NewSessionChat(ctx, reopened, &contextualStub{}, "sqlite:///chinook.db", workspaceTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := restored.Snapshot(ctx)
	if err != nil || len(snapshot.Workspace.ExportBucket) != 1 || snapshot.Workspace.ExportBucket[0] != id {
		t.Fatalf("bucket was not restored: %#v %v", snapshot.Workspace.ExportBucket, err)
	}
}

func TestRealChinookRecordSetExports(t *testing.T) {
	ctx := context.Background()
	path, err := filepath.Abs(filepath.Join("..", "dbcopy", "testdata", "chinook.db"))
	if err != nil {
		t.Fatal(err)
	}
	executor := secureread.NewExecutor(secureread.Session{Unrestricted: true})
	result, err := executor.RunDTQL(ctx, "sqlite://"+path, []byte("from: {name: Invoice}\ncolumns:\n  - field: InvoiceId\n  - field: CustomerId\n  - field: Total\norderBy:\n  - field: InvoiceId\n    desc: true\nlimit: 5\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 5 {
		t.Fatalf("expected real invoices: %d", len(result.Rows))
	}
	record := RecordSet{Title: "Recent invoices", Result: result}
	for _, format := range []ExportFormat{ExportCSV, ExportJSON, ExportYAML, ExportINGR, ExportDBF, ExportXLSX, ExportSQLite} {
		var output bytes.Buffer
		if err := ExportRecordSets(ctx, []RecordSet{record}, format, &output); err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		if output.Len() == 0 {
			t.Fatalf("%s exported nothing", format)
		}
	}
}

func TestChatUIBucketAndExportCommand(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	chat, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", workspaceTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	session, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id := workspaceTestRecord(t, store, session.ID)
	u, err := NewSessionUI(ctx, chat, "stub")
	if err != nil {
		t.Fatal(err)
	}
	if !u.focusLatestGrid() {
		t.Fatal("grid was not restored")
	}
	if _, handled := u.updateGrid(tea.KeyPressMsg{Code: 'B', Text: "B"}); !handled {
		t.Fatal("B did not toggle bucket")
	}
	if len(u.snapshot.Workspace.ExportBucket) != 1 || u.snapshot.Workspace.ExportBucket[0] != id {
		t.Fatalf("bucket action did not persist: %#v", u.snapshot.Workspace.ExportBucket)
	}
	path := filepath.Join(t.TempDir(), "customers.csv")
	command := u.runSessionCommand("/export current csv " + path)
	if command == nil || !u.exporting {
		t.Fatal("export command did not start")
	}
	message := command()
	_, _ = u.Update(message)
	if u.exporting {
		t.Fatal("export stayed busy")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("export missing: %v", err)
	}
}
