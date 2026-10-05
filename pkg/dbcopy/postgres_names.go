// postgres_names.go: the names `datatug db copy` checks before it changes a PostgreSQL target.
//
// Implements REQ:names-checked-before-writes and REQ:index-not-recreated of
// spec/features/cli/db/copy/README.md.
//
// The PostgreSQL driver refuses a name that is not a plain identifier of at most 63 bytes, in any
// statement that creates, alters or drops. A copy hands it names it read from a source, and with
// --overwrite=recreate it drops target tables one call at a time before it creates any. So the copy
// asks the question itself, for every name it is about to write, before its first change: a refusal
// then leaves the target as it was.

package dbcopy

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo2sql"
)

// postgresAdapterName is the Name() of the adapter of a PostgreSQL database.
const postgresAdapterName = "dalgo2postgres"

// maxPostgresNameBytes is how many bytes PostgreSQL keeps of an identifier.
const maxPostgresNameBytes = 63

// postgresPlainName matches a plain identifier: ASCII letters, digits and underscores, not starting
// with a digit.
var postgresPlainName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// The reasons postgresNameRefusal gives.
const (
	reasonNameNotPlain = "is not a plain identifier (ASCII letters, digits and underscores, not starting with a digit)"
	reasonNameTooLong  = "is longer than the 63 bytes PostgreSQL keeps of a name"
)

// postgresNameRefusal is the one rule of the names written to a PostgreSQL target: it returns why name
// is refused, or "" when it is accepted. A name is accepted when it is a plain identifier of at most
// 63 bytes after lower-casing (the driver writes names in lower case). The length is that of the
// lower-cased name and the characters are those of the name as given, as the driver reads them, so a
// capital outside ASCII whose lower case is an ASCII letter is refused.
//
// A test holds this verdict equal to the driver's for a table of names (see
// TestPostgresNameRefusal_AgreesWithTheDriver): when the driver changes its rule, that test fails.
func postgresNameRefusal(name string) string {
	switch {
	case len(strings.ToLower(name)) > maxPostgresNameBytes:
		return reasonNameTooLong
	case !postgresPlainName.MatchString(name):
		return reasonNameNotPlain
	}
	return ""
}

// RefusedNameError is returned by Copy, before anything is changed in a PostgreSQL target, when a name
// of the source cannot be written there. It matches dalgo2sql.ErrUnsafeName, as the driver's own
// refusal of the same name does.
type RefusedNameError struct {
	// Object says which object of the source holds the name, for example `column "a b" of table "t"`.
	Object string
	// Reason says what is wrong with the name.
	Reason string
	// Advice says what the person can do about it.
	Advice string
}

func (e *RefusedNameError) Error() string {
	return fmt.Sprintf("cannot copy to PostgreSQL: the name of %s %s; nothing in the target was changed. %s", e.Object, e.Reason, e.Advice)
}

// Unwrap makes errors.Is(err, dalgo2sql.ErrUnsafeName) true.
func (*RefusedNameError) Unwrap() error { return dalgo2sql.ErrUnsafeName }

const (
	adviceRenameOrExclude = "Rename it in the source, or leave the table out with --exclude."
	adviceRenameIndex     = "Rename or drop the index in the source, or leave the table out with --exclude."
)

// refuseName returns the error for a name that postgresNameRefusal refuses, or nil when it accepts it.
func refuseName(object, name, advice string) error {
	reason := postgresNameRefusal(name)
	if reason == "" {
		return nil
	}
	return &RefusedNameError{Object: object, Reason: reason, Advice: advice}
}

func tableObject(table string) string { return "table " + strconv.Quote(table) }

// SkippedIndex is an index of the source that was not copied to a PostgreSQL target.
type SkippedIndex struct {
	Table  string
	Index  string
	Reason string
}

func (s SkippedIndex) String() string {
	return fmt.Sprintf("index %s of table %s was not copied: %s", strconv.Quote(s.Index), strconv.Quote(s.Table), s.Reason)
}

// describedTable is the outcome of reading one source table before anything is written.
type describedTable struct {
	def *dbschema.CollectionDef
	err error
}

// isPostgresTarget reports whether target is a PostgreSQL database.
func isPostgresTarget(target dal.DB) bool { return adapterName(target) == postgresAdapterName }

// checkPostgresTarget reads the definition of every table in refs from source and checks every name
// the copy would write to a PostgreSQL target: each table name, and for each definition every column,
// primary-key column, and, when the copy creates indexes (creating), every index name and index
// column. It changes nothing and returns the first refusal, naming the object of the source.
//
// An index whose definition the driver cannot recreate (see unrecreatableIndex) is not an unsafe name:
// it is left out of the definition that is returned and listed in the second result, and the copy
// goes on without it.
//
// The definitions are returned by table name, so that the copy does not read them again; a table the
// source cannot describe is returned with its error.
func checkPostgresTarget(ctx context.Context, source dal.DB, refs []dal.CollectionRef, creating bool) (map[string]describedTable, []SkippedIndex, error) {
	for _, ref := range refs {
		if err := refuseName(tableObject(ref.Name()), ref.Name(), adviceRenameOrExclude); err != nil {
			return nil, nil, err
		}
	}
	tables := make(map[string]describedTable, len(refs))
	var skipped []SkippedIndex
	for i := range refs {
		ref := refs[i]
		def, err := dbschema.DescribeCollection(ctx, source, &ref)
		if err != nil {
			tables[ref.Name()] = describedTable{err: err}
			continue
		}
		if err = checkDefinitionNames(def); err != nil {
			return nil, nil, err
		}
		if creating {
			var skippedHere []SkippedIndex
			if def, skippedHere, err = keepRecreatableIndexes(def); err != nil {
				return nil, nil, err
			}
			skipped = append(skipped, skippedHere...)
		}
		tables[ref.Name()] = describedTable{def: def}
	}
	return tables, skipped, nil
}

// checkDefinitionNames checks the name of the table of def, and every column and primary-key column.
func checkDefinitionNames(def *dbschema.CollectionDef) error {
	table := tableObject(def.Name)
	if err := refuseName(table, def.Name, adviceRenameOrExclude); err != nil {
		return err
	}
	for _, field := range def.Fields {
		object := fmt.Sprintf("column %s of %s", strconv.Quote(string(field.Name)), table)
		if err := refuseName(object, string(field.Name), adviceRenameOrExclude); err != nil {
			return err
		}
	}
	for _, key := range def.PrimaryKey {
		object := fmt.Sprintf("primary-key column %s of %s", strconv.Quote(string(key)), table)
		if err := refuseName(object, string(key), adviceRenameOrExclude); err != nil {
			return err
		}
	}
	return nil
}

// keepRecreatableIndexes returns def without the indexes the driver cannot recreate, those indexes,
// and the first refusal among the names of the indexes it keeps (index name, the table it is on,
// and each column).
func keepRecreatableIndexes(def *dbschema.CollectionDef) (*dbschema.CollectionDef, []SkippedIndex, error) {
	var kept []dbschema.IndexDef
	var skipped []SkippedIndex
	table := tableObject(def.Name)
	for _, index := range def.Indexes {
		if reason := unrecreatableIndex(def, index); reason != "" {
			skipped = append(skipped, SkippedIndex{Table: def.Name, Index: index.Name, Reason: reason})
			continue
		}
		object := fmt.Sprintf("index %s of %s", strconv.Quote(index.Name), table)
		if err := refuseName(object, index.Name, adviceRenameIndex); err != nil {
			return nil, nil, err
		}
		if index.Collection != "" {
			if err := refuseName(fmt.Sprintf("the table index %s of %s is on", strconv.Quote(index.Name), table), index.Collection, adviceRenameIndex); err != nil {
				return nil, nil, err
			}
		}
		for _, column := range index.Fields {
			if err := refuseName(fmt.Sprintf("column %s of %s", strconv.Quote(string(column)), object), string(column), adviceRenameIndex); err != nil {
				return nil, nil, err
			}
		}
		kept = append(kept, index)
	}
	if len(skipped) == 0 {
		return def, nil, nil
	}
	copied := *def
	copied.Indexes = kept
	return &copied, skipped, nil
}

// unrecreatableIndex returns why the driver cannot recreate index of def, or "" when it can. A driver
// that reads an index reports, in place of a column name, the text of what the index holds when that is
// not a column: an expression, the condition of a partial index, an operator class or an ordering of
// nulls. Such an index is on no column of the table (a column the table has, by any case, is a name and
// is checked as one), and the driver cannot write it back.
func unrecreatableIndex(def *dbschema.CollectionDef, index dbschema.IndexDef) string {
	if len(index.Fields) == 0 {
		return "its definition names no column"
	}
	for _, field := range index.Fields {
		if !isColumnOf(def, string(field)) {
			return fmt.Sprintf("it is on %s, which is not a column of the table (an expression, a condition, an operator class or an ordering of nulls cannot be recreated)", strconv.Quote(string(field)))
		}
	}
	return ""
}

// isColumnOf reports whether name is a column of def, compared as SQL compares names: by case.
func isColumnOf(def *dbschema.CollectionDef, name string) bool {
	for _, field := range def.Fields {
		if strings.EqualFold(string(field.Name), name) {
			return true
		}
	}
	return false
}

// WriteSkippedIndexes writes one line to w for each index of the source that was not copied, in the
// order the tables were read: which index, and why. It writes nothing when every index was copied.
func WriteSkippedIndexes(w io.Writer, summary SourceSummary) {
	for _, skipped := range summary.SkippedIndexes {
		_, _ = fmt.Fprintln(w, "db copy: "+skipped.String())
	}
}
