package dbcopy

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo2sqlite"
	"github.com/dal-go/record"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/datatug/datatug-cli/internal/pgstandin"
)

// definitionSource is a source that reports the definitions it is given. It wraps a real, empty SQLite database for
// everything else, so a copy that reads rows from it finds none.
type definitionSource struct {
	dal.DB
	dbschema.SchemaReader
	defs []dbschema.CollectionDef
}

func newDefinitionSource(t *testing.T, defs ...dbschema.CollectionDef) definitionSource {
	t.Helper()
	path := filepath.Join(t.TempDir(), "empty.db")
	makeSQLiteFile(t, path, `CREATE TABLE placeholder (id INTEGER PRIMARY KEY)`)
	db, err := dalgo2sqlite.NewDatabase(path)
	require.NoError(t, err)
	return definitionSource{DB: db, SchemaReader: db, defs: defs}
}

func (s definitionSource) ListCollections(context.Context, *record.Key) ([]dal.CollectionRef, error) {
	refs := make([]dal.CollectionRef, len(s.defs))
	for i, def := range s.defs {
		refs[i] = dal.NewRootCollectionRef(def.Name, "")
	}
	return refs, nil
}

func (s definitionSource) DescribeCollection(_ context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	for _, def := range s.defs {
		if def.Name == ref.Name() {
			return &def, nil
		}
	}
	return nil, assert.AnError
}

// table is a definition of a table of integer columns, the first of them its key.
func table(name string, columns ...string) dbschema.CollectionDef {
	def := dbschema.CollectionDef{Name: name, PrimaryKey: []dal.FieldName{dal.FieldName(columns[0])}}
	for _, column := range columns {
		def.Fields = append(def.Fields, dbschema.FieldDef{Name: dal.FieldName(column), Type: dbschema.Int})
	}
	return def
}

func withIndex(def dbschema.CollectionDef, name string, fields ...string) dbschema.CollectionDef {
	index := dbschema.IndexDef{Name: name, Collection: def.Name}
	for _, field := range fields {
		index.Fields = append(index.Fields, dal.FieldName(field))
	}
	def.Indexes = append(def.Indexes, index)
	return def
}

// A name the target cannot take refuses the copy before the first statement, whichever object of the source holds it
// and wherever in the source it comes: the stand-in server of the target records no statement at all, so no target
// table was dropped, and none was created. The error names the object of the source and says what to do.
func TestCopy_ToPostgres_ARefusedNameIsRefusedBeforeAnyStatement(t *testing.T) {
	t.Parallel()
	good := func(name string) dbschema.CollectionDef { return table(name, "id", "val") }
	tooLong := strings.Repeat("a", 64)
	for name, tc := range map[string]struct {
		defs   []dbschema.CollectionDef
		object string
		reason string
		advice string
	}{
		"the third table has a space in its name": {
			defs: []dbschema.CollectionDef{good("a1"), good("b2"), good("c 3")}, object: `table "c 3"`, reason: reasonNameNotPlain, advice: adviceRenameOrExclude},
		"the third table has a name of 64 bytes": {
			defs: []dbschema.CollectionDef{good("a1"), good("b2"), good(tooLong)}, object: `table "` + tooLong + `"`, reason: reasonNameTooLong, advice: adviceRenameOrExclude},
		"a column of the third table has a space in its name": {
			defs: []dbschema.CollectionDef{good("a1"), good("b2"), table("c3", "id", "my col")}, object: `column "my col" of table "c3"`, reason: reasonNameNotPlain, advice: adviceRenameOrExclude},
		"a primary-key column is not a plain identifier": {
			defs: []dbschema.CollectionDef{good("a1"), good("b2"), func() dbschema.CollectionDef {
				def := good("c3")
				def.PrimaryKey = []dal.FieldName{"the id"}
				return def
			}()}, object: `primary-key column "the id" of table "c3"`, reason: reasonNameNotPlain, advice: adviceRenameOrExclude},
		"an index of the third table has a dash in its name": {
			defs: []dbschema.CollectionDef{good("a1"), good("b2"), withIndex(good("c3"), "ix-val", "val")}, object: `index "ix-val" of table "c3"`, reason: reasonNameNotPlain, advice: adviceRenameIndex},
		"the table an index is on is not the table of the index": {
			defs: []dbschema.CollectionDef{good("a1"), good("b2"), func() dbschema.CollectionDef {
				def := withIndex(good("c3"), "ix_val", "val")
				def.Indexes[0].Collection = "other table"
				return def
			}()}, object: `the table index "ix_val" of table "c3" is on`, reason: reasonNameNotPlain, advice: adviceRenameIndex},
		"a column of an index is a Kelvin sign where the table has a k": {
			defs: []dbschema.CollectionDef{good("a1"), good("b2"), withIndex(table("c3", "id", "k"), "ix_k", "K")}, object: "column \"K\" of index \"ix_k\" of table \"c3\"", reason: reasonNameNotPlain, advice: adviceRenameIndex},
	} {
		for _, overwrite := range []string{"recreate", "", "reload"} {
			t.Run(name+" with overwrite "+overwrite, func(t *testing.T) {
				if overwrite == "reload" && strings.Contains(name, "index") {
					t.Skip("a reload writes no index: see TestCopy_ToPostgres_AReloadChecksNoIndex")
				}
				t.Parallel()
				target, recorder := pgstandin.Recording(t, true)
				var stderr bytes.Buffer

				summary, err := Copy(t.Context(), newDefinitionSource(t, tc.defs...), target, CopyOpts{Overwrite: overwrite, Stderr: &stderr})

				var refused *RefusedNameError
				require.ErrorAs(t, err, &refused)
				assert.Equal(t, tc.object, refused.Object)
				assert.Equal(t, tc.reason, refused.Reason)
				assert.Equal(t, tc.advice, refused.Advice)
				assert.Empty(t, recorder.Statements(), "nothing was sent to the target: no table was dropped or created")
				assert.Zero(t, summary.Created)
				assert.Empty(t, stderr.String())
			})
		}
	}
}

// A reload writes no index, so the names of the indexes are not its business.
func TestCopy_ToPostgres_AReloadChecksNoIndex(t *testing.T) {
	t.Parallel()
	target, _ := pgstandin.Recording(t, true)
	source := newDefinitionSource(t, withIndex(table("c3", "id", "val"), "ix-val", "val"))

	_, err := Copy(t.Context(), source, target, CopyOpts{Overwrite: "reload"})

	var refused *RefusedNameError
	assert.NotErrorAs(t, err, &refused, "the copy goes on to read the target, which the stand-in cannot answer")
	assert.Error(t, err)
}

// An index the driver cannot recreate is not a refused name: the copy goes on, creates the tables and the other
// indexes, leaves that index out, and the summary names it. The recorder shows which statements the target was sent.
func TestCopy_ToPostgres_AnIndexTheDriverCannotRecreateIsSkippedAndReported(t *testing.T) {
	t.Parallel()
	people := withIndex(withIndex(withIndex(withIndex(table("people", "id", "name"), "ix_name", "name"),
		"ix_lower", "lower(name)"), "ix_partial", "name > 'a'"), "ix_nothing")
	orders := withIndex(table("orders", "id", "total"), "ix_total", "TOTAL")
	target, recorder := pgstandin.Recording(t, true)
	var stderr bytes.Buffer

	summary, err := Copy(t.Context(), newDefinitionSource(t, people, orders), target, CopyOpts{Overwrite: "recreate", SchemaOnly: true, Stderr: &stderr})

	require.NoError(t, err)
	assert.Equal(t, 2, summary.Created)
	assert.ElementsMatch(t, []string{"people", "orders"}, summary.CreatedNames)
	sent := strings.Join(recorder.Statements(), "\n")
	assert.Contains(t, sent, `DROP TABLE IF EXISTS "people"`)
	assert.Contains(t, sent, `DROP TABLE IF EXISTS "orders"`)
	assert.Contains(t, sent, `CREATE TABLE "people"`)
	assert.Contains(t, sent, `CREATE TABLE "orders"`)
	assert.Contains(t, sent, `CREATE INDEX "ix_name" ON "people" ("name")`)
	assert.Contains(t, sent, `CREATE INDEX "ix_total" ON "orders" ("total")`, "an index column is a column of the table by any case")
	assert.NotContains(t, sent, "ix_lower")
	assert.NotContains(t, sent, "ix_partial")
	assert.NotContains(t, sent, "ix_nothing")
	assert.Equal(t, []SkippedIndex{
		{Table: "people", Index: "ix_lower", Reason: `it is on "lower(name)", which is not a column of the table (an expression, a condition, an operator class or an ordering of nulls cannot be recreated)`},
		{Table: "people", Index: "ix_partial", Reason: `it is on "name > 'a'", which is not a column of the table (an expression, a condition, an operator class or an ordering of nulls cannot be recreated)`},
		{Table: "people", Index: "ix_nothing", Reason: "its definition names no column"},
	}, summary.SkippedIndexes)
	var notes bytes.Buffer
	WriteSkippedIndexes(&notes, summary)
	assert.Equal(t, 3, strings.Count(notes.String(), "\n"), "one line for each index, once")
	assert.Contains(t, notes.String(), `index "ix_lower" of table "people" was not copied`)
}

// A copy whose names are all acceptable is not changed: every table is created, no index is skipped.
func TestCopy_ToPostgres_ACopyOfAcceptableNamesCreatesEveryTable(t *testing.T) {
	t.Parallel()
	target, recorder := pgstandin.Recording(t, true)

	summary, err := Copy(t.Context(), newDefinitionSource(t, table("a1", "id"), withIndex(table("b2", "id", "val"), "ix_val", "val")), target, CopyOpts{SchemaOnly: true, Overwrite: "recreate"})

	require.NoError(t, err)
	assert.Equal(t, 2, summary.Created)
	assert.Empty(t, summary.SkippedIndexes)
	assert.Contains(t, strings.Join(recorder.Statements(), "\n"), `CREATE INDEX "ix_val" ON "b2" ("val")`)
}

// A table that cannot be described is skipped as it is for any target; the others are copied.
func TestCopy_ToPostgres_ATableTheSourceCannotDescribeIsSkippedAsBefore(t *testing.T) {
	t.Parallel()
	target, recorder := pgstandin.Recording(t, true)
	source := newDefinitionSource(t, table("a1", "id"), table("b2", "id"))
	source.defs[1].Name = "b2"
	broken := brokenDescribe{definitionSource: source, broken: "b2"}
	var stderr bytes.Buffer

	summary, err := Copy(t.Context(), broken, target, CopyOpts{Overwrite: "recreate", SchemaOnly: true, Stderr: &stderr})

	require.NoError(t, err)
	assert.Equal(t, []string{"b2"}, summary.Skipped)
	assert.Contains(t, stderr.String(), `skipping "b2": source DescribeCollection failed`)
	assert.Contains(t, strings.Join(recorder.Statements(), "\n"), `CREATE TABLE "a1"`)
}

// brokenDescribe is a source that cannot describe one of its tables.
type brokenDescribe struct {
	definitionSource
	broken string
}

func (b brokenDescribe) DescribeCollection(ctx context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	if ref.Name() == b.broken {
		return nil, assert.AnError
	}
	return b.definitionSource.DescribeCollection(ctx, ref)
}

// The other targets are not asked the question: a SQLite target and an inGitDB target take names a PostgreSQL target
// refuses (here a table and an index of 64 bytes), copy the rows, and an index the PostgreSQL driver cannot recreate is
// not a thing for them.
func TestCopy_ToOtherTargets_TakesTheNamesAPostgresTargetRefuses(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", 64)
	for name, open := range map[string]func(t *testing.T, dir string) dal.DB{
		"sqlite": func(t *testing.T, dir string) dal.DB {
			db, err := dalgo2sqlite.NewDatabase(filepath.Join(dir, "tgt.db"))
			require.NoError(t, err)
			return db
		},
		"ingitdb": func(t *testing.T, dir string) dal.DB {
			target := filepath.Join(dir, "tgt")
			require.NoError(t, os.Mkdir(target, 0o755))
			return dal.NewDB(newIngitDBForTest(t, target))
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			srcPath := filepath.Join(dir, "src.db")
			makeSQLiteFile(t, srcPath,
				`CREATE TABLE `+long+` (id INTEGER PRIMARY KEY, price INTEGER)`,
				`CREATE INDEX `+long+`_ix ON `+long+` (price)`,
				`INSERT INTO `+long+` VALUES (1, 10), (2, 20)`)
			src, err := dalgo2sqlite.NewDatabase(srcPath)
			require.NoError(t, err)

			summary, err := Copy(context.Background(), src, open(t, dir), CopyOpts{})

			require.NoError(t, err)
			assert.Equal(t, 1, summary.Created)
			assert.Equal(t, int64(2), summary.RowsCopied)
			assert.Empty(t, summary.SkippedIndexes)
		})
	}
}

// The name the definition carries is checked as well as the name the source lists: a source may describe a table by
// another name than it lists it by, and the definition is what is created.
func TestCopy_ToPostgres_TheNameOfTheDefinitionIsCheckedToo(t *testing.T) {
	t.Parallel()
	target, recorder := pgstandin.Recording(t, true)
	source := renamedOnDescribe{definitionSource: newDefinitionSource(t, table("a1", "id")), as: "a 1"}

	_, err := Copy(t.Context(), source, target, CopyOpts{Overwrite: "recreate", SchemaOnly: true})

	var refused *RefusedNameError
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, `table "a 1"`, refused.Object)
	assert.Empty(t, recorder.Statements())
}

// renamedOnDescribe is a source that describes its tables under another name than it lists them by.
type renamedOnDescribe struct {
	definitionSource
	as string
}

func (r renamedOnDescribe) DescribeCollection(ctx context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	def, err := r.definitionSource.DescribeCollection(ctx, ref)
	if err == nil {
		def.Name = r.as
	}
	return def, err
}
