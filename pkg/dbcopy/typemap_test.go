package dbcopy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo2sqlite"
	"github.com/stretchr/testify/assert"
)

// chinookFixturePath is the path to the canonical Chinook SQLite
// fixture, relative to the test binary's working directory.
const chinookFixturePath = "testdata/chinook.db"

// collectChinookFieldTypes opens the Chinook fixture and returns the
// dbschema.Type values for every field of every collection that
// dalgo2sqlite can describe. Collections that fail DescribeCollection
// (a driver-level gap; dalgo2sqlite currently rejects DATETIME and
// NUMERIC(p,s)) are skipped with t.Logf so the test stays honest about
// what the fixture exercises today. When the driver gains DATETIME /
// NUMERIC support, dbschema.Time and dbschema.Decimal will start
// flowing through this iteration automatically — both are already in
// the supported set in typemap.go.
func collectChinookFieldTypes(t *testing.T) []dbschema.Type {
	t.Helper()
	ctx := context.Background()
	db, err := dalgo2sqlite.NewDatabase(chinookFixturePath)
	if err != nil {
		t.Fatalf("open chinook fixture %q: %v", chinookFixturePath, err)
	}
	t.Cleanup(func() { _ = db.Close() })

	collections, err := dbschema.ListCollections(ctx, db, nil)
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	if len(collections) == 0 {
		t.Fatalf("chinook fixture %q has no collections", chinookFixturePath)
	}

	var types []dbschema.Type
	described := 0
	for _, ref := range collections {
		ref := ref
		cd, err := dbschema.DescribeCollection(ctx, db, &ref)
		if err != nil {
			t.Logf("skipping collection %q: DescribeCollection failed (driver gap, not a dbcopy concern): %v", ref.Name(), err)
			continue
		}
		described++
		for _, f := range cd.Fields {
			types = append(types, f.Type)
		}
	}
	if described == 0 {
		t.Fatalf("no chinook collections could be described")
	}
	if len(types) == 0 {
		t.Fatalf("chinook fixture produced no field types")
	}
	return types
}

// TestMapType_ChinookCoverage_SQLiteToInGitDB asserts that every
// column type appearing on a Chinook field maps cleanly with sqlite as
// the source and ingitdb as the target.
func TestMapType_ChinookCoverage_SQLiteToInGitDB(t *testing.T) {
	t.Parallel()
	types := collectChinookFieldTypes(t)
	for _, src := range types {
		out, err := MapType(src, BackendSQLite, BackendInGitDB)
		assert.NoError(t, err, "MapType(%s, sqlite, ingitdb)", src)
		assert.Equal(t, src, out, "MapType(%s, sqlite, ingitdb) should be identity for MVP", src)
	}
}

// TestMapType_ChinookCoverage_InGitDBToSQLite asserts the reverse
// direction over the same column-type set. There is no inGitDB Chinook
// fixture in this repo; the dbschema vocabulary is engine-neutral so
// the same Type values are valid as inGitDB-source inputs.
func TestMapType_ChinookCoverage_InGitDBToSQLite(t *testing.T) {
	t.Parallel()
	types := collectChinookFieldTypes(t)
	for _, src := range types {
		out, err := MapType(src, BackendInGitDB, BackendSQLite)
		assert.NoError(t, err, "MapType(%s, ingitdb, sqlite)", src)
		assert.Equal(t, src, out, "MapType(%s, ingitdb, sqlite) should be identity for MVP", src)
	}
}

// TestMapType_UnsupportedKind_ReturnsTypedError feeds a synthetic
// dbschema.Type whose integer value is outside the declared enum range
// and asserts that MapType returns a *UnsupportedTypeError that names
// both the type and the target backend.
func TestMapType_UnsupportedKind_ReturnsTypedError(t *testing.T) {
	t.Parallel()
	// 127 is far above any declared dbschema.Type constant (the enum
	// is an int8 and currently tops out at Decimal = 7). It is a
	// deliberately synthetic out-of-range value.
	synthetic := dbschema.Type(127)
	_, err := MapType(synthetic, BackendSQLite, BackendInGitDB)
	if !assert.Error(t, err, "synthetic out-of-range Type must error") {
		return
	}
	var typed *UnsupportedTypeError
	if !assert.True(t, errors.As(err, &typed), "want *UnsupportedTypeError, got %T", err) {
		return
	}
	assert.Equal(t, synthetic, typed.SourceType, "SourceType field must echo the input")
	assert.Equal(t, BackendInGitDB, typed.TargetBackend, "TargetBackend field must echo the input")
	assert.Contains(t, err.Error(), BackendInGitDB, "error message must name the target backend")
	assert.Contains(t, err.Error(), synthetic.String(), "error message must name the source type")

	// dbschema.Null is the zero value and reserved for "unset"; it
	// must also be rejected, not silently passed through.
	_, err = MapType(dbschema.Null, BackendInGitDB, BackendSQLite)
	if assert.Error(t, err, "dbschema.Null must error") {
		assert.True(t, errors.As(err, &typed), "Null rejection must use *UnsupportedTypeError")
	}
}

// TestMapType_UnknownBackend_ReturnsError verifies that an unknown
// backend (e.g. oracle) is rejected with a plain error — silently
// mapping to anything would mask configuration mistakes.
func TestMapType_UnknownBackend_ReturnsError(t *testing.T) {
	t.Parallel()
	_, err := MapType(dbschema.Int, BackendSQLite, "oracle")
	if assert.Error(t, err, "unknown target backend must error") {
		assert.Contains(t, strings.ToLower(err.Error()), "oracle")
	}
	_, err = MapType(dbschema.Int, "oracle", BackendSQLite)
	if assert.Error(t, err, "unknown source backend must error") {
		assert.Contains(t, strings.ToLower(err.Error()), "oracle")
	}
}
