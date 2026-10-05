package dbcopy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo2postgres"
	"github.com/dal-go/dalgo2sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/datatug/datatug-cli/internal/pgstandin"
)

// namesOfEveryKind are names the rule is held to, with the verdict it must give: what the driver of a
// PostgreSQL target accepts is a plain identifier of at most 63 bytes after lower-casing.
var namesOfEveryKind = []struct {
	label  string
	name   string
	reason string // "" when accepted
}{
	{"a lower-case name", "orders", ""},
	{"a name with capitals and digits", "Order2Lines", ""},
	{"a name that starts with an underscore", "_orders", ""},
	{"a reserved word", "select", ""},
	{"a name of 63 bytes", strings.Repeat("a", 63), ""},
	{"a name of 63 bytes with capitals", strings.Repeat("N", 63), ""},
	{"a name of 64 bytes", strings.Repeat("a", 64), reasonNameTooLong},
	{"a name of 64 bytes with capitals", strings.Repeat("N", 64), reasonNameTooLong},
	{"a name that is empty", "", reasonNameNotPlain},
	{"a name with a space", "Order Details", reasonNameNotPlain},
	{"a name with a dash", "order-lines", reasonNameNotPlain},
	{"a name with a dot", "main.orders", reasonNameNotPlain},
	{"a name with a double quote", `a"b`, reasonNameNotPlain},
	{"a name with a backslash", `a\b`, reasonNameNotPlain},
	{"a name with a NUL byte", "a\x00b", reasonNameNotPlain},
	{"a name that starts with a digit", "1orders", reasonNameNotPlain},
	{"a name with a non-ASCII letter", "café", reasonNameNotPlain},
	{"a name in another script", "заказы", reasonNameNotPlain},
	{"a Kelvin sign, which lower-cases to k", "\u212a", reasonNameNotPlain},
	{"63 Kelvin signs, 63 bytes once lower-cased", strings.Repeat("\u212a", 63), reasonNameNotPlain},
	{"a dotted capital I, which lower-cases to i", "\u0130d", reasonNameNotPlain},
	{"a name that is 44 bytes and 66 once lower-cased", strings.Repeat("\u023a", 22), reasonNameTooLong},
}

func TestPostgresNameRefusal_GivesTheReasonForEachKindOfName(t *testing.T) {
	t.Parallel()
	for _, tc := range namesOfEveryKind {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.reason, postgresNameRefusal(tc.name))
		})
	}
}

// The rule of this package is a copy of the driver's: this test holds the two verdicts equal for every
// name of the table, in every position a name is written (a table, a column, a primary-key column, an index
// and a column of an index), by calling the driver's own entries over a handle whose server refuses every
// statement. A name that the driver refuses is refused before any statement (no statement is recorded) and
// with dalgo2sql.ErrUnsafeName; a name it accepts reaches the first statement, which fails with the server's
// error. When the driver's rule changes and this one does not, a row of this test fails.
func TestPostgresNameRefusal_AgreesWithTheDriver(t *testing.T) {
	t.Parallel()
	for _, tc := range namesOfEveryKind {
		for position, call := range driverEntries() {
			t.Run(tc.label+" as "+position, func(t *testing.T) {
				t.Parallel()
				db, recorder := pgstandin.Recording(t, false)

				err := call(t.Context(), db, tc.name)

				require.Error(t, err)
				refusedByTheDriver := errors.Is(err, dalgo2sql.ErrUnsafeName)
				assert.Equal(t, postgresNameRefusal(tc.name) != "", refusedByTheDriver,
					"the rule of the copy and the driver's disagree about %q as %s: the driver says %v", tc.name, position, err)
				if refusedByTheDriver {
					assert.Empty(t, recorder.Statements(), "a refused name is refused before any statement")
				} else {
					assert.NotEmpty(t, recorder.Statements(), "an accepted name reaches a statement, which the server refuses")
				}
			})
		}
	}
}

func TestRefusedNameError_NamesTheObjectTheReasonAndWhatToDo(t *testing.T) {
	t.Parallel()
	err := &RefusedNameError{Object: `column "my col" of table "t"`, Reason: reasonNameNotPlain, Advice: adviceRenameOrExclude}

	assert.EqualError(t, err, `cannot copy to PostgreSQL: the name of column "my col" of table "t" `+reasonNameNotPlain+
		`; nothing in the target was changed. Rename it in the source, or leave the table out with --exclude.`)
	assert.ErrorIs(t, err, dalgo2sql.ErrUnsafeName, "it is the class of refusal the driver's own is")
}

func TestSkippedIndex_SaysWhichIndexOfWhichTableAndWhy(t *testing.T) {
	t.Parallel()
	assert.Equal(t, `index "ix_lower" of table "people" was not copied: because`,
		SkippedIndex{Table: "people", Index: "ix_lower", Reason: "because"}.String())

	var out strings.Builder
	WriteSkippedIndexes(&out, SourceSummary{})
	assert.Empty(t, out.String(), "no line when every index was copied")
	WriteSkippedIndexes(&out, SourceSummary{SkippedIndexes: []SkippedIndex{{"a", "i1", "x"}, {"b", "i2", "y"}}})
	assert.Equal(t, "db copy: index \"i1\" of table \"a\" was not copied: x\ndb copy: index \"i2\" of table \"b\" was not copied: y\n", out.String(), "one line for each index")
}

func TestIsPostgresTarget_IsTrueForTheDriverOnly(t *testing.T) {
	t.Parallel()
	db, _ := pgstandin.Recording(t, true)
	assert.True(t, isPostgresTarget(db))
	assert.False(t, isPostgresTarget(nil))
	assert.False(t, isPostgresTarget(nilAdapterDB{}))
	assert.False(t, isPostgresTarget(namedDB{name: "dalgo2sqlite"}))
}

// namedDB is a stand-in for a database of the adapter it is named for.
type namedDB struct {
	dal.DB
	name string
}

func (n namedDB) Adapter() dal.Adapter { return mockAdapter{name: n.name} }

// driverEntries are the entries of the driver that write a name, by the position of the name in the call: each takes the
// name under test and puts it in that position of an otherwise acceptable call.
func driverEntries() map[string]func(ctx context.Context, db *dalgo2postgres.Database, name string) error {
	column := dbschema.FieldDef{Name: "a", Type: dbschema.Int}
	return map[string]func(context.Context, *dalgo2postgres.Database, string) error{
		"a table": func(ctx context.Context, db *dalgo2postgres.Database, name string) error {
			return db.DropCollection(ctx, name)
		},
		"a column": func(ctx context.Context, db *dalgo2postgres.Database, name string) error {
			return db.CreateCollection(ctx, dbschema.CollectionDef{Name: "t", Fields: []dbschema.FieldDef{{Name: dal.FieldName(name), Type: dbschema.Int}}})
		},
		"a primary-key column": func(ctx context.Context, db *dalgo2postgres.Database, name string) error {
			return db.CreateCollection(ctx, dbschema.CollectionDef{Name: "t", Fields: []dbschema.FieldDef{column}, PrimaryKey: []dal.FieldName{dal.FieldName(name)}})
		},
		"an index": func(ctx context.Context, db *dalgo2postgres.Database, name string) error {
			return db.CreateCollection(ctx, dbschema.CollectionDef{Name: "t", Fields: []dbschema.FieldDef{column}, Indexes: []dbschema.IndexDef{{Name: name, Fields: []dal.FieldName{"a"}}}})
		},
		"a column of an index": func(ctx context.Context, db *dalgo2postgres.Database, name string) error {
			return db.CreateCollection(ctx, dbschema.CollectionDef{Name: "t", Fields: []dbschema.FieldDef{column}, Indexes: []dbschema.IndexDef{{Name: "i", Fields: []dal.FieldName{dal.FieldName(name)}}}})
		},
	}
}
