package chat

import (
	"context"
	"database/sql"
	"path/filepath"
	"sort"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/joinmeta"
)

// Chat sessions store each applied JOIN with its constraint ID, and its
// candidate ID hashes that ID and the target schema. The dalgo schema reader
// must give a SQLite database the keys the PRAGMA reader it replaced gave, so
// a join saved before stays recognised as applied.
func TestSchemaReaderKeepsTheSQLiteConstraintIDsSessionsStored(t *testing.T) {
	ctx := context.Background()
	path, err := filepath.Abs(filepath.Join("..", "dbcopy", "testdata", "chinook.db"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	source := "sqlite://" + path
	legacy, err := LoadSQLiteForeignKeySnapshot(ctx, source, raw)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := dbcopy.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	db, err := ref.OpenProtected(ctx)
	if err != nil {
		t.Fatal(err)
	}
	current, err := joinmeta.LoadForeignKeySnapshot(ctx, db, source)
	if err != nil {
		t.Fatal(err)
	}
	byID := func(keys []ForeignKey) []ForeignKey {
		sorted := append([]ForeignKey(nil), keys...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].ConstraintID < sorted[j].ConstraintID })
		return sorted
	}
	want, got := byID(legacy.Keys), byID(current.Keys)
	if len(want) == 0 || len(want) != len(got) {
		t.Fatalf("keys: legacy %d, schema reader %d", len(want), len(got))
	}
	for i := range want {
		w, g := want[i], got[i]
		if w.ConstraintID != g.ConstraintID || w.Schema != g.Schema || w.ToSchema != g.ToSchema || w.FromRelation != g.FromRelation || w.ToRelation != g.ToRelation {
			t.Fatalf("key %d: legacy %+v, schema reader %+v", i, w, g)
		}
	}

	// a join applied with the legacy IDs hides its candidate and its reverse
	query := []byte("from: {name: Invoice}\n")
	before, err := DiscoverJoinCandidates(query, legacy)
	if err != nil {
		t.Fatal(err)
	}
	after, err := DiscoverJoinCandidates(query, current)
	if err != nil {
		t.Fatal(err)
	}
	ids := func(candidates []JoinCandidate) []string {
		var out []string
		for _, candidate := range candidates {
			out = append(out, string(candidateID(candidate)))
		}
		sort.Strings(out)
		return out
	}
	if a, b := ids(before), ids(after); len(a) == 0 || len(a) != len(b) {
		t.Fatalf("candidates: legacy %v, schema reader %v", a, b)
	} else {
		for i := range a {
			if a[i] != b[i] {
				t.Fatalf("candidate IDs differ: legacy %v, schema reader %v", a, b)
			}
		}
	}
}
