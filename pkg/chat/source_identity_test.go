package chat

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

// What a chat store stores for a source is its display form (dbcopy.SourceDisplay):
// it never holds a password. The display form is not an identity: it cuts a path
// at a "?" or a "#", which Parse keeps in the path of a project directory, and it
// drops the user name and the query of a URL. These tests hold the three places
// that used it as one: the scope hash, the refresh of a stored result, and the
// JOIN and cell-detail actions on a stored result.

// hashSource is the source of a project in a directory with a "#" in its name,
// as api.ListSources writes it: the display form is "ingitdb:///tmp/a".
const hashSource = "ingitdb:///tmp/a#b/data/ingitdb"

// identityScope is the scope the golden hashes below were computed for.
func identityScope(sources map[string]string) ChatScope {
	return ChatScope{ProjectID: "demo-project", Environment: "local", Database: "db", AccessFingerprint: "admin-policy-v1", Sources: sources}
}

// The scope hashes the store on main (61a21bd) wrote for these scopes, read from
// a session stored by that code. previousScope must keep producing them: they are
// what an existing chat store holds.
var previousScopeGoldens = []struct {
	name    string
	sources map[string]string
	golden  string
}{
	{"a path with a hash", map[string]string{"db": "sqlite:///chinook.db", "recordset": "ingitdb:///tmp/a#b/data/ingitdb", "http": "http:///tmp/a#b"}, "515d7b89a35dc405bba5f94cde3439ec9d592e02bc35ec04a9792595533df89e"},
	{"a secret in the query", map[string]string{"db": "sqlite:///chinook.db", "private": "sqlite:///private.db?token=secret"}, "59cd3145c35f611941f6309367448994a103b8d3765b709f9c23f4bdc7f49860"},
	{"nothing to hide", map[string]string{"db": "sqlite:///chinook.db"}, "55deb21c421094638d3158498a52927fd0e5644e949ffb79c68534cfae343a4c"},
}

func TestPreviousScope_IsTheHashTheStoreWroteOnMain(t *testing.T) {
	t.Parallel()
	for _, tc := range previousScopeGoldens {
		if got := previousScope(identityScope(tc.sources)); got != tc.golden {
			t.Errorf("%s: previousScope = %s, want %s", tc.name, got, tc.golden)
		}
	}
}

// A chat store written under the identity main used keeps its sessions, its
// bookmarks and its preferences after the upgrade, whatever the display form of
// the sources cut off.
func TestOpenSessionStore_MovesWhatThePreviousScopeIdentityHeld(t *testing.T) {
	ctx := context.Background()
	for _, tc := range previousScopeGoldens {
		t.Run(tc.name, func(t *testing.T) {
			path := testStorePath(t)
			scope := identityScope(tc.sources)
			store := openTestStore(t, path, scope)
			unchanged := store.scope == tc.golden
			if want := tc.name == "nothing to hide"; unchanged != want {
				t.Fatalf("the scope hash equals main's: %v, want %v", unchanged, want)
			}
			// What the older store left behind, written under its own hash.
			for _, statement := range []string{
				`INSERT INTO sessions (id, scope, title, created_at, updated_at) VALUES ('old-session', '` + tc.golden + `', 'Old chat', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
				`INSERT INTO bookmarks (id, project_id, scope, title, tags_json, target_kind, created_at, updated_at, snapshot_json) VALUES ('old-bookmark', 'demo-project', '` + tc.golden + `', 'Old', '[]', 'recordset', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', x'')`,
				`INSERT OR REPLACE INTO chat_preferences (scope, table_style, result_versions_to_keep) VALUES ('` + tc.golden + `', 'lines', 7)`,
			} {
				if _, err := store.db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			reopened := openTestStore(t, path, scope)
			sessions, err := reopened.List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, session := range sessions {
				found = found || session.ID == "old-session"
			}
			if !found {
				t.Errorf("the session stored under the previous identity is not listed: %+v", sessions)
			}
			var bookmarks int
			if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM bookmarks WHERE scope = ? AND id = 'old-bookmark'`, reopened.scope).Scan(&bookmarks); err != nil || bookmarks != 1 {
				t.Errorf("the bookmark was not moved to the new scope: %d, %v", bookmarks, err)
			}
			if kept, err := reopened.ResultVersionsToKeep(ctx); err != nil || kept != 7 {
				t.Errorf("the preference was not moved to the new scope: %d, %v", kept, err)
			}
			if !unchanged {
				var left int
				if err := reopened.db.QueryRow(`SELECT (SELECT COUNT(*) FROM sessions WHERE scope = ?) + (SELECT COUNT(*) FROM bookmarks WHERE scope = ?) + (SELECT COUNT(*) FROM chat_preferences WHERE scope = ?)`, tc.golden, tc.golden, tc.golden).Scan(&left); err != nil || left != 0 {
					t.Errorf("%d row(s) stay under the previous identity: %v", left, err)
				}
			}
		})
	}
}

// Phase 2 sessions (no source in their scope) are reopened when every saved
// query used the selected source; the source they stored is the one main wrote,
// which for a path with a "#" is the whole path.
func TestOpenSessionStore_ReopensAPhaseTwoSessionOfADirectoryWithAHash(t *testing.T) {
	ctx := context.Background()
	path := testStorePath(t)
	scope := identityScope(map[string]string{"db": hashSource})
	store := openTestStore(t, path, scope)
	legacy, _ := json.Marshal(struct {
		Environment       string
		Database          string
		AccessFingerprint string
	}{scope.Environment, scope.Database, scope.AccessFingerprint})
	sum := sha256.Sum256(legacy)
	legacyScope := hex.EncodeToString(sum[:])
	for _, statement := range []string{
		`INSERT INTO sessions (id, scope, title, created_at, updated_at) VALUES ('phase-two', '` + legacyScope + `', 'Phase two', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		`INSERT INTO queries (id, session_id, origin_message_id, title, dtql, source, parameters_json, executed_at, error) VALUES ('q1', 'phase-two', '', 'Rows', 'from: {name: t}', '` + hashSource + `', '{}', '2026-01-01T00:00:00Z', '')`,
	} {
		if _, err := store.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path, scope)
	sessions, err := reopened.List(ctx)
	if err != nil || len(sessions) != 1 || sessions[0].ID != "phase-two" {
		t.Fatalf("the Phase 2 session was not reopened: %+v, %v", sessions, err)
	}
}

func TestMoveChatScope_ErrorsAreReportedAndNothingIsHalfMoved(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, statement := range []string{
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, scope TEXT)`,
		`CREATE TABLE bookmarks (id TEXT PRIMARY KEY, scope TEXT)`,
		`CREATE TABLE chat_preferences (scope TEXT PRIMARY KEY, table_style TEXT)`,
		`INSERT INTO sessions VALUES ('s', 'old')`,
		`INSERT INTO bookmarks VALUES ('b', 'old')`,
		`INSERT INTO chat_preferences VALUES ('old', 'lines')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	origBegin, origExec := dbBeginFn, txExecFn
	t.Cleanup(func() { dbBeginFn, txExecFn = origBegin, origExec })

	if err := moveChatScope(db, "same", "same"); err != nil {
		t.Fatalf("moving a scope onto itself: %v", err)
	}

	dbBeginFn = func(*sql.DB) (*sql.Tx, error) { return nil, errors.New("begin boom") }
	if err := moveChatScope(db, "old", "new"); err == nil || !strings.Contains(err.Error(), "begin boom") {
		t.Fatalf("begin error = %v", err)
	}
	dbBeginFn = origBegin

	for _, table := range []string{"sessions", "bookmarks", "chat_preferences"} {
		txExecFn = func(tx *sql.Tx, query string, args ...any) (sql.Result, error) {
			if strings.Contains(query, "UPDATE "+table+" ") || strings.Contains(query, "UPDATE OR IGNORE "+table+" ") {
				return nil, errors.New("exec boom")
			}
			return origExec(tx, query, args...)
		}
		if err := moveChatScope(db, "old", "new"); err == nil || !strings.Contains(err.Error(), "exec boom") {
			t.Fatalf("%s: error = %v", table, err)
		}
		var moved int
		if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM sessions WHERE scope = 'new') + (SELECT COUNT(*) FROM bookmarks WHERE scope = 'new') + (SELECT COUNT(*) FROM chat_preferences WHERE scope = 'new')`).Scan(&moved); err != nil || moved != 0 {
			t.Fatalf("%s: a failed move left %d row(s) in the new scope: %v", table, moved, err)
		}
	}
	txExecFn = origExec

	if err := moveChatScope(db, "old", "new"); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM sessions WHERE scope = 'old') + (SELECT COUNT(*) FROM bookmarks WHERE scope = 'old') + (SELECT COUNT(*) FROM chat_preferences WHERE scope = 'old')`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("%d row(s) stay in the old scope: %v", left, err)
	}
}

func TestOpenSessionStore_ReportsAScopeMoveThatFails(t *testing.T) {
	origExec := txExecFn
	t.Cleanup(func() { txExecFn = origExec })
	txExecFn = func(tx *sql.Tx, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "UPDATE bookmarks") {
			return nil, errors.New("move boom")
		}
		return origExec(tx, query, args...)
	}
	// testScope's "private" source holds a query, so its display form differs from
	// the string and the previous identity is another hash: the move runs.
	if _, err := OpenSessionStore(testStorePath(t), testScope()); err == nil || !strings.Contains(err.Error(), "move boom") {
		t.Fatalf("OpenSessionStore error = %v, want the failed move", err)
	}
}

func TestStoredSourceNames(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		stored, live string
		want         bool
	}{
		{"the string itself", "sqlite:///chinook.db", "sqlite:///chinook.db", true},
		{"the display form of a path with a hash", "ingitdb:///tmp/a", hashSource, true},
		{"the display form of a URL with a password and a query", "postgres://db.example.com/shop", "postgres://alice:s3cret@db.example.com/shop?sslmode=require", true},
		{"another source", "sqlite:///other.db", "sqlite:///chinook.db", false},
		{"nothing stored for nothing live", "", "", true},
		{"a source that cannot be shown names nothing", dbcopy.UnparsableSource, "", false},
		{"an unparsable live string is not the placeholder", dbcopy.UnparsableSource, "not a source", false},
	} {
		if got := storedSourceNames(tc.stored, tc.live); got != tc.want {
			t.Errorf("%s: storedSourceNames(%q, %q) = %v, want %v", tc.name, tc.stored, tc.live, got, tc.want)
		}
	}
}

// A result stored from a project whose directory holds a "#" is refreshed: the
// stored source is the display form, and the live source is found by its ID.
func TestRefreshRecordSet_FindsTheLiveSourceByIDWhenTheStoredOneIsItsDisplayForm(t *testing.T) {
	ctx := context.Background()
	live := hashSource
	scope := testScope()
	scope.Sources["chinook"] = live
	store := openTestStore(t, testStorePath(t), scope)
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, live)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := store.AppendUser(ctx, sessions.activeID, "Show invoices")
	if err != nil {
		t.Fatal(err)
	}
	doc := "from: {name: Invoice}\nlimit: 20"
	query, err := store.AppendQuery(ctx, sessions.activeID, origin.ID, live, QueryResult{Title: "Invoices", DTQL: doc, SourceID: "chinook", Result: secureread.Result{Columns: []string{"InvoiceId"}}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(ctx, sessions.activeID)
	if err != nil {
		t.Fatal(err)
	}
	if stored := loaded.RecordSets[query.RecordSetID].Source; stored != "ingitdb:///tmp/a" {
		t.Fatalf("the stored source = %q, want the display form (no hash, no password)", stored)
	}
	executor := &fakeExecutor{result: secureread.Result{Columns: []string{"InvoiceId"}, Rows: []secureread.Row{{Data: map[string]any{"InvoiceId": 1}}}}}
	sessions.ConfigureQueryExecutor(executor)
	snapshot, err := sessions.RefreshRecordSet(ctx, sessions.activeID, query.RecordSetID)
	if err != nil {
		t.Fatalf("refresh of a result from a path with a hash: %v", err)
	}
	if executor.source != live || executor.calls != 1 || len(snapshot.RecordSets) != 2 {
		t.Fatalf("refresh ran against %q (%d call(s)), want the live source %q", executor.source, executor.calls, live)
	}
	for _, record := range snapshot.RecordSets {
		if strings.Contains(record.Source, "#") {
			t.Fatalf("the refreshed result stores %q", record.Source)
		}
	}

	// A result of another source is still refused, and so is one of a source that is gone.
	other, err := store.AppendQuery(ctx, sessions.activeID, origin.ID, "ingitdb:///tmp/other", QueryResult{Title: "Other", DTQL: doc, SourceID: "chinook", Result: secureread.Result{Columns: []string{"x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.RefreshRecordSet(ctx, sessions.activeID, other.RecordSetID); err == nil || !strings.Contains(err.Error(), "no longer available") {
		t.Fatalf("refresh of a result from another source: %v", err)
	}
	gone, err := store.AppendQuery(ctx, sessions.activeID, origin.ID, live, QueryResult{Title: "Gone", DTQL: doc, SourceID: "dropped", Result: secureread.Result{Columns: []string{"x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.RefreshRecordSet(ctx, sessions.activeID, gone.RecordSetID); err == nil || !strings.Contains(err.Error(), "no longer available") {
		t.Fatalf("refresh of a result whose source ID is not in the scope: %v", err)
	}
}

// The JOIN and cell-detail actions compare a stored result with the live source
// of their own: the display form of it names the same source.
func TestJoinActionsRecogniseAStoredResultByTheDisplayFormOfTheLiveSource(t *testing.T) {
	ctx := context.Background()
	live := "sqlite:///fixture.db?mode=ro&token=s3cret"
	snapshot := joinSnapshot()
	snapshot.Source = live
	application := ForeignKeyJoinApplication{Source: live, Snapshot: snapshot, Executor: &joinExecutorStub{}}
	stored := RecordSet{Source: dbcopy.SourceDisplay(live), DTQL: "from: {name: Invoice}\nlimit: 20"}
	if stored.Source != "sqlite:///fixture.db" {
		t.Fatalf("display form = %q", stored.Source)
	}

	candidates, err := application.Candidates(ctx, stored)
	if err != nil || len(candidates) == 0 {
		t.Fatalf("Candidates for a stored result: %v, %v", candidates, err)
	}
	if _, err := application.Apply(ctx, stored, candidates[0].ID); err != nil {
		t.Fatalf("Apply for a stored result: %v", err)
	}
	preview, err := application.PreviewRelated(ctx, stored, "main.Invoice.CustomerId", map[string]any{"main.invoice.customerid": 7})
	if err != nil || len(preview) != 1 {
		t.Fatalf("PreviewRelated for a stored result: %v, %v", preview, err)
	}

	elsewhere := RecordSet{Source: "sqlite:///other.db", DTQL: stored.DTQL}
	if got, err := application.Candidates(ctx, elsewhere); err != nil || len(got) != 0 {
		t.Fatalf("Candidates for another source: %v, %v", got, err)
	}
	if _, err := application.Apply(ctx, elsewhere, candidates[0].ID); err == nil {
		t.Fatal("Apply for another source was accepted")
	}
	if got, err := application.PreviewRelated(ctx, elsewhere, "main.Invoice.CustomerId", map[string]any{"main.invoice.customerid": 7}); err != nil || got != nil {
		t.Fatalf("PreviewRelated for another source: %v, %v", got, err)
	}
}
