package sqliteschema

import (
	"fmt"
	"strings"
)

// The names of the tables and indexes this package reads are read out of the
// scanned database, and a database is not trusted: a name is data, in every
// statement made with it. The driver of a scan runs every statement of a query
// string, so a name pasted into SQL that ended its quote and went on with ATTACH
// DATABASE would make the scan create files wherever its user can write, and change
// the database it was asked to read. A name goes into a statement only through
// these functions, which keep it inside the quotes it is put in.

// quoteLiteral is name as an SQL string literal: in single quotes, with each single
// quote in it doubled, which is the only way out of one.
func quoteLiteral(name string) string {
	return "'" + strings.ReplaceAll(name, "'", "''") + "'"
}

// quoteIdentifier is name as an SQL identifier: in double quotes, with each double
// quote in it doubled. It is for a table or schema name in a FROM clause, where a
// literal cannot go, and it holds any name, including one with a "]" in it, which
// the bracket form of an identifier cannot.
func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// pragmaSQL is the statement `PRAGMA <pragma>('<argument>')`, for a pragma that
// takes a table or index name.
func pragmaSQL(pragma, argument string) string {
	return fmt.Sprintf("PRAGMA %s(%s)", pragma, quoteLiteral(argument))
}
