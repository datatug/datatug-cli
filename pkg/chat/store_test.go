package chat

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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
	return ChatScope{ProjectID: "demo-project", Environment: "local", Database: "chinook", AccessFingerprint: "admin-policy-v1", Sources: map[string]string{"chinook": "sqlite:///chinook.db", "private": "sqlite:///private.db?token=secret"}}
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

func TestSessionStoreSeparatesReconfiguredSources(t *testing.T) {
	ctx := context.Background()
	path := testStorePath(t)
	original := testScope()
	store := openTestStore(t, path, original)
	session, err := store.Create(ctx, "Old Chinook")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, session.ID, "show customers")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendTurn(ctx, session.ID, user.ID, original.Sources[original.Database], Turn{Queries: []QueryResult{{
		DTQL:   "from: {name: Customer}\nlimit: 1",
		Result: secureread.Result{Columns: []string{"CustomerId"}, Rows: []secureread.Row{{Data: map[string]any{"CustomerId": 5}}}},
	}}}); err != nil {
		t.Fatal(err)
	}
	changed := testScope()
	changed.Sources = map[string]string{"chinook": "sqlite:///replacement.db"}
	replacement := openTestStore(t, path, changed)
	if list, err := replacement.List(ctx); err != nil || len(list) != 0 {
		t.Fatalf("repointed source reopened old sessions: %+v, %v", list, err)
	}
	if _, err := replacement.Load(ctx, session.ID); err == nil {
		t.Fatal("repointed source reopened old RecordSet")
	}
	registryChanged := testScope()
	registryChanged.Sources["secondary"] = "sqlite:///new-secondary.db"
	otherRegistry := openTestStore(t, path, registryChanged)
	if list, err := otherRegistry.List(ctx); err != nil || len(list) != 0 {
		t.Fatalf("changed source registry reopened old sessions: %+v, %v", list, err)
	}
	restored := openTestStore(t, path, original)
	if got, err := restored.Load(ctx, session.ID); err != nil || len(got.RecordSets) != 1 {
		t.Fatalf("original source could not restore its snapshot: %+v, %v", got, err)
	}
}

func TestLegacyChatScopeMigratesOnlyMatchingSource(t *testing.T) {
	ctx := context.Background()
	path := testStorePath(t)
	scope := testScope()
	store := openTestStore(t, path, scope)
	matching, err := store.Create(ctx, "Matching")
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := store.Create(ctx, "Wrong source")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ id, source string }{{matching.ID, scope.Sources[scope.Database]}, {wrong.ID, "sqlite:///old-database.db"}} {
		user, err := store.AppendUser(ctx, item.id, "show one")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendTurn(ctx, item.id, user.ID, item.source, Turn{Queries: []QueryResult{{
			DTQL:   "from: {name: Customer}\nlimit: 1",
			Result: secureread.Result{Columns: []string{"CustomerId"}, Rows: []secureread.Row{{Data: map[string]any{"CustomerId": 5}}}},
		}}}); err != nil {
			t.Fatal(err)
		}
	}
	legacyJSON, err := json.Marshal(struct {
		Environment       string
		Database          string
		AccessFingerprint string
	}{scope.Environment, scope.Database, scope.AccessFingerprint})
	if err != nil {
		t.Fatal(err)
	}
	legacyHash := sha256.Sum256(legacyJSON)
	if _, err := store.db.Exec(`UPDATE sessions SET scope = ? WHERE id IN (?, ?)`, hex.EncodeToString(legacyHash[:]), matching.ID, wrong.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path, scope)
	if _, err := reopened.Load(ctx, matching.ID); err != nil {
		t.Fatalf("matching Phase 2 session was not migrated: %v", err)
	}
	if _, err := reopened.Load(ctx, wrong.ID); err == nil {
		t.Fatal("legacy session from another source was migrated")
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

func bookmarkableSelection(t *testing.T, store *SessionStore) (ChatSession, ContextReference) {
	t.Helper()
	ctx := context.Background()
	session, err := store.Create(ctx, "Invoices")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, session.ID, "show invoices")
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	turn, err := store.AppendTurn(ctx, session.ID, user.ID, "sqlite:///private.db?token=secret", Turn{Queries: []QueryResult{{
		Title: "Invoices", DTQL: "from: {name: Invoice}", SourceID: "private",
		Result: secureread.Result{Columns: []string{"ID", "When", "Total"}, Rows: []secureread.Row{
			{Key: "a", Data: map[string]any{"ID": int64(1), "When": when, "Total": 1.25}},
			{Key: "b", Data: map[string]any{"ID": int64(2), "When": when.AddDate(0, 0, 1), "Total": 2.5}},
			{Key: "c", Data: map[string]any{"ID": int64(3), "When": when.AddDate(0, 0, 2), "Total": 3.75}},
		}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	view := RecordSetView{ID: "view-1", RecordSetID: turn.Queries[0].RecordSetID, Title: "Invoice subset", RowIndices: []int{2, 0}, Columns: []string{"ID", "When", "Total"}, CreatedAt: when}
	selection := Selection{ID: "selection-1", ViewID: view.ID, Title: "Chosen invoices", Rows: []int{2, 0}, Columns: []string{"ID", "When", "Total"}, Ranges: []CellRange{{FirstRow: 0, LastRow: 0, FirstCol: 0, LastCol: 1}, {FirstRow: 2, LastRow: 2, FirstCol: 1, LastCol: 2}}, CreatedAt: when}
	if err := store.SaveWorkspace(ctx, session.ID, WorkspaceState{Views: map[string]RecordSetView{view.ID: view}, Selections: map[string]Selection{selection.ID: selection}}); err != nil {
		t.Fatal(err)
	}
	return session, ContextReference{Kind: "selection", ObjectID: selection.ID, Title: selection.Title}
}

func TestBookmarkMigrationFromV2IsAtomicAndIdempotent(t *testing.T) {
	ctx := context.Background()
	path := testStorePath(t)
	store := openTestStore(t, path, testScope())
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE bookmarks; PRAGMA user_version = 2`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	migrated := openTestStore(t, path, testScope())
	var version int
	if err := migrated.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != 5 {
		t.Fatalf("schema version = %d, %v", version, err)
	}
	if _, err := migrated.db.ExecContext(ctx, `INSERT INTO bookmarks (id, project_id, scope, title, tags_json, target_kind, created_at, updated_at, snapshot_json) VALUES ('bad', ?, ?, 'bad', '[]', 'recordset', ?, ?, '{}')`, testScope().ProjectID, migrated.scope, stamp(time.Now().UTC()), stamp(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	if err := migrated.Close(); err != nil {
		t.Fatal(err)
	}
	// A second v5 open does not rerun or corrupt the completed migration.
	reopened := openTestStore(t, path, testScope())
	if _, err := reopened.ListBookmarks(ctx); err == nil || !strings.Contains(err.Error(), "corrupt bookmark") {
		t.Fatalf("invalid v3 snapshot was not rejected after restart: %v", err)
	}
}

func TestJoinLineageMigrationFromV4AddsAppliedEdges(t *testing.T) {
	ctx := context.Background()
	path := testStorePath(t)
	store := openTestStore(t, path, testScope())
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE recordsets DROP COLUMN join_applied_edges_json; PRAGMA user_version = 4`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	migrated := openTestStore(t, path, testScope())
	defer func() { _ = migrated.Close() }()
	var version int
	if err := migrated.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != 5 {
		t.Fatalf("migrated schema version = %d, %v", version, err)
	}
	session, err := migrated.Create(ctx, "Join migration")
	if err != nil {
		t.Fatal(err)
	}
	user, err := migrated.AppendUser(ctx, session.ID, "join customers")
	if err != nil {
		t.Fatal(err)
	}
	edge := AppliedJoinEdge{JoinPath: "root/0", SourcePath: "root", ConstraintID: "fk_invoice_customer", Direction: "outgoing", CandidateID: "candidate", Fields: []JoinFieldPair{{"CustomerId", "CustomerId"}}}
	query, err := migrated.AppendQuery(ctx, session.ID, user.ID, "sqlite:///fixture.db", QueryResult{Title: "Joined", DTQL: "from: {name: Invoice}\nlimit: 1", Result: secureread.Result{Columns: []string{"InvoiceId"}}, Lineage: &JoinLineage{ParentRecordSetID: "parent", CandidateID: edge.CandidateID, AppliedEdges: []AppliedJoinEdge{edge}}})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := migrated.Load(ctx, session.ID)
	if err != nil || restored.RecordSets[query.RecordSetID].Lineage == nil || len(restored.RecordSets[query.RecordSetID].Lineage.AppliedEdges) != 1 {
		t.Fatalf("migrated JOIN provenance did not round-trip: %+v, %v", restored.RecordSets[query.RecordSetID], err)
	}
}

func TestBookmarkSelectionSurvivesOriginDeletionAndRestart(t *testing.T) {
	ctx := context.Background()
	path := testStorePath(t)
	store := openTestStore(t, path, testScope())
	session, ref := bookmarkableSelection(t, store)
	bookmark, err := store.CreateBookmark(ctx, session.ID, ref, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddBookmarkTag(ctx, bookmark.ID, " Incident "); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddBookmarkTag(ctx, bookmark.ID, "incident"); err != nil {
		t.Fatal(err)
	}
	if err := store.Clear(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	if bookmarks, err := store.ListBookmarks(ctx); err != nil || len(bookmarks) != 1 {
		t.Fatalf("bookmarks after origin clear = %+v, %v", bookmarks, err)
	}
	if err := store.Delete(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path, testScope())
	bookmarks, err := reopened.ListBookmarks(ctx)
	if err != nil || len(bookmarks) != 1 || len(bookmarks[0].Tags) != 1 || bookmarks[0].Tags[0] != "Incident" {
		t.Fatalf("bookmarks after origin deletion/restart = %+v, %v", bookmarks, err)
	}
	result, sourceRows := bookmarkResult(bookmarks[0])
	if got, want := sourceRows, []int{2, 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("source rows = %v, want %v", got, want)
	}
	if got := result.Rows[0].Data["Total"]; got != 3.75 {
		t.Fatalf("typed selected value = %#v", got)
	}
	if _, ok := result.Rows[0].Data["ID"]; ok {
		t.Fatalf("range hole leaked ID into selected row: %+v", result.Rows[0].Data)
	}
	if got := result.Rows[1].Data["When"]; got != time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("typed date = %#v", got)
	}
	if _, ok := result.Rows[1].Data["Total"]; ok {
		t.Fatalf("range hole leaked Total into selected row: %+v", result.Rows[1].Data)
	}
}

func TestBookmarkTagsAndProjectScopeIsolation(t *testing.T) {
	ctx := context.Background()
	path := testStorePath(t)
	store := openTestStore(t, path, testScope())
	session, ref := bookmarkableSelection(t, store)
	first, err := store.CreateBookmark(ctx, session.ID, ref, "Prague invoices")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateBookmark(ctx, session.ID, ref, "Payment invoices")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		id   string
		tags []string
	}{{first.ID, []string{"Incident", "2026-09-21"}}, {second.ID, []string{"incident", "payments"}}} {
		for _, tag := range item.tags {
			if _, err := store.AddBookmarkTag(ctx, item.id, tag); err != nil {
				t.Fatal(err)
			}
		}
	}
	found, err := store.FindBookmarks(ctx, "prague", []string{"incident", "2026-09-21", "INCIDENT"})
	if err != nil || len(found) != 1 || found[0].ID != first.ID {
		t.Fatalf("AND tag search = %+v, %v", found, err)
	}
	if _, err := store.RemoveBookmarkTag(ctx, first.ID, "INCIDENT"); err != nil {
		t.Fatal(err)
	}
	if found, err := store.FindBookmarks(ctx, "", []string{"incident"}); err != nil || len(found) != 1 || found[0].ID != second.ID {
		t.Fatalf("removed tag search = %+v, %v", found, err)
	}
	otherProject := testScope()
	otherProject.ProjectID = "another-project"
	if found, err := openTestStore(t, path, otherProject).ListBookmarks(ctx); err != nil || len(found) != 0 {
		t.Fatalf("cross-project bookmarks = %+v, %v", found, err)
	}
	otherScope := testScope()
	otherScope.AccessFingerprint = "other-policy"
	if found, err := openTestStore(t, path, otherScope).ListBookmarks(ctx); err != nil || len(found) != 0 {
		t.Fatalf("cross-scope bookmarks = %+v, %v", found, err)
	}
}

func TestBookmarkDeleteBlocksPersistedReferencesAndSaveValidatesThem(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	origin, ref := bookmarkableSelection(t, store)
	bookmark, err := store.CreateBookmark(ctx, origin.ID, ref, "Chosen invoices")
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := store.Create(ctx, "Consumer")
	if err != nil {
		t.Fatal(err)
	}
	bookmarkRef := ContextReference{Kind: "bookmark", ObjectID: bookmark.ID, Title: bookmark.Title}
	if err := store.SaveWorkspace(ctx, consumer.ID, WorkspaceState{Attachments: []ContextReference{bookmarkRef}, Docks: []Dock{{ID: "dock-1", Reference: bookmarkRef}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteBookmark(ctx, bookmark.ID); err == nil || !strings.Contains(err.Error(), "still referenced") {
		t.Fatalf("delete referenced bookmark = %v", err)
	}
	if err := store.SaveWorkspace(ctx, consumer.ID, WorkspaceState{}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteBookmark(ctx, bookmark.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWorkspace(ctx, consumer.ID, WorkspaceState{Attachments: []ContextReference{{Kind: "bookmark", ObjectID: bookmark.ID}}}); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("saving deleted bookmark reference = %v", err)
	}
}
