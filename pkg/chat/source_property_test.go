package chat

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/internal/sourcecases"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

// TestProperty_ChatStoreNeverPersistsASourceSecret is the DT-0C acceptance
// property for the chat store, a file on disk: for every generated source string,
// given both as a source of the session's scope and as the source of a stored
// query, no secret of four or more characters is in any file the store wrote or
// in the source it reads back.
func TestProperty_ChatStoreNeverPersistsASourceSecret(t *testing.T) {
	ctx := context.Background()
	path := testStorePath(t)
	cases := sourcecases.CommandCases()

	scope := testScope()
	scope.Database = "shop"
	scope.Sources = map[string]string{"shop": "sqlite:///shop.db"}
	for i, c := range cases {
		scope.Sources[fmt.Sprintf("source-%d", i)] = c.Source
	}
	// What the scope hashes holds no secret either.
	for id, shown := range redactSources(scope.Sources) {
		for _, c := range cases {
			if leaked := sourcecases.Leaks(c, shown); len(leaked) > 0 {
				t.Errorf("%s: source %s is hashed as %q, which holds %q", c.Name, id, shown, leaked)
			}
		}
	}

	store := openTestStore(t, path, scope)
	session, err := store.Create(ctx, "Sources")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, session.ID, "show rows")
	if err != nil {
		t.Fatal(err)
	}
	result := secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Key: "1", Data: map[string]any{"id": int64(1)}}}}
	for _, c := range cases {
		if _, err := store.AppendTurn(ctx, session.ID, user.ID, c.Source, Turn{Queries: []QueryResult{{Title: "Rows", DTQL: "from: {name: t}", SourceID: "shop", Result: result}}}); err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
	}
	restored, err := store.Load(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	var back strings.Builder
	for _, query := range restored.Queries {
		back.WriteString(query.Source + "\n")
	}
	for _, record := range restored.RecordSets {
		back.WriteString(record.Source + "\n")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	stored := storeBytes(t, path)
	failed := 0
	for _, c := range cases {
		if leaked := sourcecases.Leaks(c, stored, back.String()); len(leaked) > 0 {
			failed++
			if failed <= 20 {
				t.Errorf("%s: the chat store holds %q", c.Name, leaked)
			}
		}
	}
	if failed > 0 {
		t.Errorf("%d of %d generated sources were persisted by the chat store", failed, len(cases))
	}
}
