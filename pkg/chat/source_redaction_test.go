package chat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

const redactionSecret = "s3cr3t-DT01"

// storeBytes returns everything the chat store wrote under path's directory:
// the database file and any journal or write-ahead log beside it.
func storeBytes(t *testing.T, path string) string {
	t.Helper()
	files, err := filepath.Glob(path + "*")
	if err != nil || len(files) == 0 {
		t.Fatalf("no store files for %s: %v", path, err)
	}
	var all strings.Builder
	for _, file := range files {
		data, readErr := os.ReadFile(file)
		if readErr != nil {
			t.Fatal(readErr)
		}
		all.Write(data)
	}
	return all.String()
}

func TestFriendlyQueryErrorRedactsSourceURLs(t *testing.T) {
	err := errors.New(`dial "postgres://alice:` + redactionSecret + `@db.example.com/shop" failed; password=` + redactionSecret)
	got := friendlyQueryError(err)
	if strings.Contains(got, redactionSecret) || !strings.Contains(got, "postgres://alice:xxxxx@db.example.com/shop") {
		t.Fatalf("friendlyQueryError = %q", got)
	}
	// Redaction happens before truncation, so a long message cannot cut a URL
	// into a shape the redaction misses.
	long := errors.New(strings.Repeat("x", 220) + " postgres://alice:" + redactionSecret + "@db.example.com/shop")
	if got := friendlyQueryError(long); strings.Contains(got, redactionSecret) || strings.Contains(got, "alice:s") {
		t.Fatalf("truncated message leaks: %q", got)
	}
}

func TestChatStoreNeverPersistsASourcePassword(t *testing.T) {
	ctx := context.Background()
	path := testStorePath(t)
	literal := "postgres://alice:" + redactionSecret + "@db.example.com:5432/shop?sslmode=require"
	scope := testScope()
	scope.Sources = map[string]string{"shop": literal, "chinook": "sqlite:///chinook.db"}
	scope.Database = "shop"
	store := openTestStore(t, path, scope)
	session, err := store.Create(ctx, "Orders")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, session.ID, "show orders")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.AppendTurn(ctx, session.ID, user.ID, literal, Turn{Queries: []QueryResult{{
		Title: "Orders", DTQL: "from: {name: orders}", SourceID: "shop",
		Result: secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Key: "1", Data: map[string]any{"id": int64(1)}}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := store.Load(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantSource := "postgres://alice:xxxxx@db.example.com:5432/shop?sslmode=require"
	if got := restored.RecordSets[turn.Queries[0].RecordSetID].Source; got != wantSource {
		t.Fatalf("recordset source = %q, want %q", got, wantSource)
	}
	if got := restored.Queries[0].Source; got != wantSource {
		t.Fatalf("query source = %q, want %q", got, wantSource)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if stored := storeBytes(t, path); strings.Contains(stored, redactionSecret) {
		t.Fatal("the chat store file holds the source password")
	}
}

func TestChatScopeIdentityIgnoresSourcePasswords(t *testing.T) {
	ctx := context.Background()
	path := testStorePath(t)
	scopeWith := func(password string) ChatScope {
		scope := testScope()
		scope.Database = "shop"
		scope.Sources = map[string]string{"shop": "postgres://alice:" + password + "@db.example.com/shop"}
		return scope
	}
	first := openTestStore(t, path, scopeWith(redactionSecret))
	created, err := first.Create(ctx, "Orders")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	// Rotating the password keeps the same sessions: the password is no part of the identity.
	second := openTestStore(t, path, scopeWith("another-"+redactionSecret))
	defer func() { _ = second.Close() }()
	sessions, err := second.List(ctx)
	if err != nil || len(sessions) != 1 || sessions[0].ID != created.ID {
		t.Fatalf("sessions after a password rotation = %+v, %v", sessions, err)
	}
}

func TestChatStoreKeepsAnEnvSourceVerbatim(t *testing.T) {
	ctx := context.Background()
	path := testStorePath(t)
	scope := testScope()
	scope.Database = "shop"
	scope.Sources = map[string]string{"shop": "env:SHOP_PG_URL"}
	store := openTestStore(t, path, scope)
	defer func() { _ = store.Close() }()
	session, err := store.Create(ctx, "Orders")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, session.ID, "show orders")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendTurn(ctx, session.ID, user.ID, "env:SHOP_PG_URL", Turn{Queries: []QueryResult{{Title: "Orders", DTQL: "from: {name: orders}", SourceID: "shop", Result: secureread.Result{Columns: []string{"id"}}}}}); err != nil {
		t.Fatal(err)
	}
	restored, err := store.Load(ctx, session.ID)
	if err != nil || restored.Queries[0].Source != "env:SHOP_PG_URL" {
		t.Fatalf("env source did not round-trip: %+v, %v", restored.Queries, err)
	}
}
