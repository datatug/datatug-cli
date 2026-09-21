package chat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

func openTestStore(t *testing.T, path string, scope ChatScope) *SessionStore {
	t.Helper()
	store, err := OpenSessionStore(path, scope)
	if err != nil {
		t.Fatalf("OpenSessionStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testScope() ChatScope {
	return ChatScope{Environment: "local", Database: "chinook", AccessFingerprint: "admin-policy-v1"}
}

func testStorePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "private", "chat.sqlite")
}

func TestSessionStoreLifecycleAndIsolation(t *testing.T) {
	ctx := context.Background()
	path := testStorePath(t)
	store := openTestStore(t, path, testScope())
	a, err := store.Create(ctx, "Orders")
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.Create(ctx, "Customers")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Rename(ctx, b.ID, "Prague customers"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendUser(ctx, a.ID, "orders"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendUser(ctx, b.ID, "customers"); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	reloaded := openTestStore(t, path, testScope())
	list, err := reloaded.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %+v, %v", list, err)
	}
	sessionA, err := reloaded.Load(ctx, a.ID)
	if err != nil || len(sessionA.Messages) != 1 || sessionA.Messages[0].Text != "orders" {
		t.Fatalf("A = %+v, %v", sessionA, err)
	}
	sessionB, err := reloaded.Load(ctx, b.ID)
	if err != nil || len(sessionB.Messages) != 1 || sessionB.Messages[0].Text != "customers" || sessionB.Title != "Prague customers" {
		t.Fatalf("B = %+v, %v", sessionB, err)
	}
	if err := reloaded.Clear(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	cleared, err := reloaded.Load(ctx, a.ID)
	if err != nil || len(cleared.Messages) != 0 || cleared.Title != "New chat" {
		t.Fatalf("cleared A = %+v, %v", cleared, err)
	}
	unchanged, err := reloaded.Load(ctx, b.ID)
	if err != nil || len(unchanged.Messages) != 1 {
		t.Fatalf("B after clear = %+v, %v", unchanged, err)
	}
	if err := reloaded.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.Load(ctx, a.ID); err == nil {
		t.Fatal("deleted session reopened")
	}
	list, err = reloaded.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != b.ID {
		t.Fatalf("list after delete = %+v, %v", list, err)
	}
}

func TestRecordSetSnapshotRestoresTypedRowsWithoutQueryRerun(t *testing.T) {
	ctx := context.Background()
	path := testStorePath(t)
	store := openTestStore(t, path, testScope())
	session, err := store.Create(ctx, "Invoices")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, session.ID, "show invoices")
	if err != nil {
		t.Fatal(err)
	}
	date := time.Date(2013, 12, 22, 0, 0, 0, 0, time.UTC)
	original := secureread.Result{Columns: []string{"InvoiceId", "InvoiceDate", "Total", "Note", "Binary"}, Rows: []secureread.Row{{Key: "k1", Data: map[string]any{"InvoiceId": int64(412), "InvoiceDate": date, "Total": 1.99, "Note": nil, "Binary": []byte{0, 255}}}}, Collection: "main.Invoice", Limitations: []secureread.Limitation{{Kind: secureread.LimitationPolicy, Note: "admin"}}}
	doc := "from: {name: main.Invoice}\nlimit: 1"
	turn, err := store.AppendTurn(ctx, session.ID, user.ID, "sqlite:///chinook.db", Turn{Queries: []QueryResult{{Title: "Newest invoice", DTQL: doc, Result: original}}})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Queries[0].RecordSetID == "" || turn.Queries[0].QueryID == "" {
		t.Fatalf("IDs missing: %+v", turn)
	}
	// A changed live source cannot mutate the persisted snapshot.
	original.Rows[0].Data["InvoiceId"] = int64(999)
	_ = store.Close()
	reloaded := openTestStore(t, path, testScope())
	saved, err := reloaded.Load(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Queries) != 1 || saved.Queries[0].DTQL != doc || saved.Queries[0].OriginMessageID != user.ID {
		t.Fatalf("queries = %+v", saved.Queries)
	}
	if len(saved.Messages) != 2 || saved.Messages[1].RecordSetID != turn.Queries[0].RecordSetID {
		t.Fatalf("messages = %+v", saved.Messages)
	}
	rs := saved.RecordSets[turn.Queries[0].RecordSetID]
	if rs.DTQL != doc || rs.Source != "sqlite:///chinook.db" || rs.Database != "chinook" || rs.Environment != "local" || rs.OriginMessageID != user.ID {
		t.Fatalf("provenance = %+v", rs)
	}
	if got := rs.Result.Rows[0].Data["InvoiceId"]; got != int64(412) {
		t.Fatalf("restored ID = %#v", got)
	}
	if got := rs.Result.Rows[0].Data["InvoiceDate"]; got != date {
		t.Fatalf("restored date = %#v", got)
	}
	if got := NewGridModel(rs.Result).Rows[0][1]; got != "2013-12-22" {
		t.Fatalf("restored grid date = %q", got)
	}
	if len(rs.Result.Limitations) != 1 {
		t.Fatalf("limitations = %+v", rs.Result.Limitations)
	}
	// Another execution is a distinct immutable snapshot.
	user2, err := reloaded.AppendUser(ctx, session.ID, "refresh")
	if err != nil {
		t.Fatal(err)
	}
	second, err := reloaded.AppendTurn(ctx, session.ID, user2.ID, "sqlite:///chinook.db", Turn{Queries: []QueryResult{{DTQL: doc, Result: original}}})
	if err != nil || second.Queries[0].RecordSetID == rs.ID {
		t.Fatalf("second ID = %+v, %v", second, err)
	}
	stillOld, err := reloaded.Load(ctx, session.ID)
	if err != nil || stillOld.RecordSets[rs.ID].Result.Rows[0].Data["InvoiceId"] != int64(412) {
		t.Fatalf("old snapshot changed: %v", err)
	}
}

func TestStoreEmptyResultAndRecoveryErrors(t *testing.T) {
	ctx := context.Background()
	path := testStorePath(t)
	store := openTestStore(t, path, testScope())
	session, err := store.Create(ctx, "Empty")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, session.ID, "nothing")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.AppendTurn(ctx, session.ID, user.ID, "sqlite:///test.db", Turn{Queries: []QueryResult{{DTQL: "from: {name: Customer}\nlimit: 1", Result: secureread.Result{Columns: []string{"ID"}, Rows: []secureread.Row{}}}}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(ctx, session.ID)
	if err != nil || len(loaded.RecordSets[turn.Queries[0].RecordSetID].Result.Rows) != 0 {
		t.Fatalf("empty result = %+v, %v", loaded, err)
	}
	if _, err := store.db.Exec(`DELETE FROM recordsets WHERE id = ?`, turn.Queries[0].RecordSetID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx, session.ID); err == nil || !strings.Contains(err.Error(), "missing RecordSet") {
		t.Fatalf("missing snapshot error = %v", err)
	}
	if _, err := store.db.Exec(`UPDATE sessions SET created_at = 'not-a-date' WHERE id = ?`, session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx, session.ID); err == nil || !strings.Contains(err.Error(), "corrupt chat session") {
		t.Fatalf("corrupt metadata error = %v", err)
	}
}

func TestStoreRestoresRealEmptyResultShape(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	session, err := store.Create(ctx, "Empty from DALgo")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, session.ID, "show absent customers")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.AppendTurn(ctx, session.ID, user.ID, "sqlite:///test.db", Turn{Queries: []QueryResult{{DTQL: "from: {name: Customer}\nlimit: 1", Result: secureread.Result{}}}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(loaded.RecordSets[turn.Queries[0].RecordSetID].Result.Rows); got != 0 {
		t.Fatalf("empty result has %d rows", got)
	}
}

func TestStoreAccessScopeAndPrivateFile(t *testing.T) {
	ctx := context.Background()
	path := testStorePath(t)
	admin := openTestStore(t, path, testScope())
	session, err := admin.Create(ctx, "Admin")
	if err != nil {
		t.Fatal(err)
	}
	otherScope := testScope()
	otherScope.AccessFingerprint = "viewer-policy-v1"
	viewer := openTestStore(t, path, otherScope)
	list, err := viewer.List(ctx)
	if err != nil || len(list) != 0 {
		t.Fatalf("viewer sessions = %+v, %v", list, err)
	}
	if _, err := viewer.Load(ctx, session.ID); err == nil {
		t.Fatal("viewer could open admin session")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("chat file permissions = %v, %v", info, err)
	}
}

func TestInterruptedTurnRollsBackQueryAndSnapshot(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	session, err := store.Create(ctx, "Interrupted")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, session.ID, "unsupported value")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.AppendTurn(ctx, session.ID, user.ID, "sqlite:///test.db", Turn{Queries: []QueryResult{{DTQL: "from: {name: Customer}\nlimit: 1", Result: secureread.Result{Columns: []string{"ID"}, Rows: []secureread.Row{{Data: map[string]any{"ID": func() {}}}}}}}})
	if err == nil {
		t.Fatal("expected result serialization error")
	}
	loaded, err := store.Load(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) != 1 || len(loaded.Queries) != 0 || len(loaded.RecordSets) != 0 {
		t.Fatalf("partial turn survived: %+v", loaded)
	}
}

func TestCorruptSnapshotPayloadIsReported(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	session, err := store.Create(ctx, "Corrupt")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, session.ID, "show")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.AppendTurn(ctx, session.ID, user.ID, "sqlite:///test.db", Turn{Queries: []QueryResult{{DTQL: "from: {name: Customer}\nlimit: 1", Result: secureread.Result{Columns: []string{"ID"}, Rows: []secureread.Row{}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE recordsets SET result_json = 'broken' WHERE id = ?`, turn.Queries[0].RecordSetID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx, session.ID); err == nil || !strings.Contains(err.Error(), "corrupt RecordSet") {
		t.Fatalf("corrupt snapshot load error = %v", err)
	}
}
