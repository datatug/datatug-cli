// Package projectschema holds the integration test bridging datatug-core's
// static inGitDB project schema (ingitdbschema.WriteSchema) with the
// dalgo2ingitdb driver that reads and writes it.
//
// It lives in datatug-cli, not datatug-core, because datatug-core's go.mod
// must not carry a DALgo driver dependency (dalgo2ingitdb), not even for a
// test — see spec/features/dalgo-project-store REQ:project-store-is-a-dalgo-database.
// datatug-cli already depends on dalgo2ingitdb v0.6.1 and ingitdb-go/ingitdb
// v0.6.1 (Task 1 of the dalgo-project-store plan), so the round-trip evidence
// for Task 2's AC (dalgo-project-store#ac:collection-declares-the-canonical-record-file)
// belongs here.
package projectschema

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-core/pkg/storage/ingitdbschema"
	"github.com/ingitdb/dalgo2ingitdb"
	"github.com/ingitdb/ingitdb-go/ingitdb/validator"
)

// TestQueryRoundTripsThroughStaticSchema copies datatug-core's static inGitDB
// project schema into a fresh store, opens it with dalgo2ingitdb.NewDatabase,
// writes query "q1" for project "p1" at key path
// ext/datatug/projects/p1/queries/q1, and asserts the record lands at exactly
// ext/datatug/projects/p1/queries/q1/q1.query.json — the canonical layout
// dalgo-project-store#REQ:canonical-project-layout and
// REQ:extension-namespace declare, produced by today's (v0.6.1) driver over
// the schema authored in datatug-core PR #335.
func TestQueryRoundTripsThroughStaticSchema(t *testing.T) {
	dir := t.TempDir()
	if err := ingitdbschema.WriteSchema(dir); err != nil {
		t.Fatalf("WriteSchema(%s): %v", dir, err)
	}

	db, err := dalgo2ingitdb.NewDatabase(dir, validator.NewCollectionsReader())
	if err != nil {
		t.Fatalf("dalgo2ingitdb.NewDatabase(%s): %v", dir, err)
	}

	ctx := context.Background()
	extKey := record.NewKeyWithID("ext", "datatug")
	projectKey := record.NewKeyWithParentAndID(extKey, "projects", "p1")
	queryKey := record.NewKeyWithParentAndID(projectKey, "queries", "q1")

	// A QueryDef-shaped record: id/title/type are the columns the queries
	// definition marks required (datatug.QueryDef embeds ProjectItem, whose
	// ValidateWithOptions(true) requires id and title; QueryDef.Validate's
	// own switch requires type).
	wantData := map[string]any{"id": "q1", "title": "Query 1", "type": "SQL"}
	err = db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Set(ctx, record.NewRecordWithData(queryKey, wantData))
	})
	if err != nil {
		t.Fatalf("write query q1: %v", err)
	}

	wantPath := filepath.Join(dir, filepath.FromSlash(
		"ext/datatug/projects/p1/queries/q1/q1.query.json"))
	if _, statErr := os.Stat(wantPath); statErr != nil {
		t.Fatalf("expected query record at %s: %v", wantPath, statErr)
	}

	// The canonical layout is a directory per record ("records_dir: '.'"),
	// never the flat file a {key}-templated name would produce without it,
	// and never nested under an inGitDB "$records" directory.
	flatPath := filepath.Join(dir, filepath.FromSlash(
		"ext/datatug/projects/p1/queries/q1.query.json"))
	if _, statErr := os.Stat(flatPath); statErr == nil {
		t.Errorf("did not expect a flat record file at %s", flatPath)
	}
	if recordsDirs := findDirsNamed(t, dir, "$records"); len(recordsDirs) > 0 {
		t.Errorf("did not expect any $records directory, found: %v", recordsDirs)
	}

	// Read the record back through the driver (not just the filesystem) and
	// confirm the data written is the data read back.
	got := record.NewRecordWithData(queryKey, map[string]any{})
	if err = db.Get(ctx, got); err != nil {
		t.Fatalf("read back query q1: %v", err)
	}
	gotData, ok := got.Data().(map[string]any)
	if !ok {
		t.Fatalf("read back data is %T, want map[string]any", got.Data())
	}
	for _, field := range []string{"id", "title", "type"} {
		if gotData[field] != wantData[field] {
			t.Errorf("read back %s = %v, want %v", field, gotData[field], wantData[field])
		}
	}

	// No project record was ever written for the "ext/datatug" scoping
	// parent (REQ:extension-namespace: it need not exist as a record file).
	extRecordPath := filepath.Join(dir, filepath.FromSlash("ext/datatug/datatug.ext.json"))
	if _, statErr := os.Stat(extRecordPath); statErr == nil {
		t.Errorf("did not expect a record file for the ext/datatug scoping parent at %s", extRecordPath)
	}
}

// findDirsNamed walks root and returns every directory path whose base name
// equals name.
func findDirsNamed(t *testing.T, root, name string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() && d.Name() == name {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return found
}

// TestSecondProjectDoesNotCollide proves the schema serves two projects in
// one store under disjoint subtrees, addressed the same way task 14 of the
// plan will later verify at the store-behaviour layer; here it is only a
// sanity check that the same schema/driver combination the AC test above
// exercises does not collide two project ids under one collection directory.
func TestSecondProjectDoesNotCollide(t *testing.T) {
	dir := t.TempDir()
	if err := ingitdbschema.WriteSchema(dir); err != nil {
		t.Fatalf("WriteSchema(%s): %v", dir, err)
	}

	db, err := dalgo2ingitdb.NewDatabase(dir, validator.NewCollectionsReader())
	if err != nil {
		t.Fatalf("dalgo2ingitdb.NewDatabase(%s): %v", dir, err)
	}

	ctx := context.Background()
	extKey := record.NewKeyWithID("ext", "datatug")

	write := func(projectID, queryID string) error {
		projectKey := record.NewKeyWithParentAndID(extKey, "projects", projectID)
		queryKey := record.NewKeyWithParentAndID(projectKey, "queries", queryID)
		return db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
			return tx.Set(ctx, record.NewRecordWithData(queryKey, map[string]any{"id": queryID}))
		})
	}
	if err = write("p1", "q1"); err != nil {
		t.Fatalf("write p1/q1: %v", err)
	}
	if err = write("p2", "q1"); err != nil {
		t.Fatalf("write p2/q1: %v", err)
	}

	for _, rel := range []string{
		"ext/datatug/projects/p1/queries/q1/q1.query.json",
		"ext/datatug/projects/p2/queries/q1/q1.query.json",
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if _, statErr := os.Stat(p); statErr != nil {
			t.Errorf("expected %s to exist: %v", p, statErr)
		}
	}
}
