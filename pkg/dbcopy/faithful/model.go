// Package faithful copies a SQL database between SQLite and PostgreSQL and keeps
// everything the two engines can both express: tables, columns, nullability,
// primary keys (composite too), foreign keys (composite too, with their ON
// DELETE / ON UPDATE actions), unique constraints, indexes, check constraints,
// defaults, auto-increment columns, and every value byte for byte.
//
// What an engine cannot carry is never dropped silently: every object that is
// not carried, and every column whose type had to be widened, is a Warning of
// the Report (text for people, JSON for machines).
//
// The package works on database/sql handles and reads each engine's own
// catalog (PRAGMA tables for SQLite, pg_catalog for PostgreSQL) because the
// engine-neutral DALgo dbschema model has no place for a declared column type,
// a check constraint or a default text, which is what a faithful copy needs.
package faithful

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// Engine names a SQL engine this package copies between.
type Engine string

// The engines of phase 1.
const (
	SQLite   Engine = "sqlite"
	Postgres Engine = "postgres"
)

// Kind is the logical kind of a column, the vocabulary both directions of the
// type mapping meet in.
type Kind int

// The logical kinds of a column.
const (
	KindOther Kind = iota
	KindInt
	KindFloat
	KindDecimal
	KindText
	KindBytes
	KindBool
	KindDate
	KindTimestamp
	KindTimestampTZ
	KindTime
)

func (k Kind) String() string {
	switch k {
	case KindInt:
		return "int"
	case KindFloat:
		return "float"
	case KindDecimal:
		return "decimal"
	case KindText:
		return "text"
	case KindBytes:
		return "bytes"
	case KindBool:
		return "bool"
	case KindDate:
		return "date"
	case KindTimestamp:
		return "timestamp"
	case KindTimestampTZ:
		return "timestamptz"
	case KindTime:
		return "time"
	}
	return "other"
}

// Identity is how a column generates its own value.
type Identity int

// The ways a column generates its value.
const (
	IdentityNone Identity = iota
	// IdentityRowid is a SQLite INTEGER PRIMARY KEY (an alias of the rowid).
	IdentityRowid
	// IdentityAutoincrement is a SQLite INTEGER PRIMARY KEY AUTOINCREMENT.
	IdentityAutoincrement
	// IdentityGenerated is a PostgreSQL identity column or serial.
	IdentityGenerated
)

// Column is a column as the source engine declares it.
type Column struct {
	Name string
	// SourceType is the declared type text in the source engine, as the engine reports it.
	SourceType string
	Kind       Kind
	// Special is a PostgreSQL type with no logical kind of its own that is carried as text: json, jsonb, uuid.
	Special string
	// Width is the byte width of an integer (2, 4, 8) or float (4, 8) when the source declares one.
	Width int
	// Length is the character length of a varchar / char column, 0 when none.
	Length int
	// Fixed marks a blank-padded char(n).
	Fixed bool
	// Precision and Scale of a decimal, -1 when the source declares none.
	Precision, Scale int
	NotNull          bool
	// Default is the source default expression as the source reports it, "" for none.
	Default  string
	Identity Identity
	// Marker is what an earlier datatug copy recorded on this column (PostgreSQL comment).
	Marker *Marker
	// Comment is a PostgreSQL column comment that is not a datatug marker.
	Comment string
	// Generated is the expression of a generated column, "" for an ordinary column.
	Generated string
	// SequenceStart is the next value of an identity / autoincrement counter, 0 when unknown.
	SequenceStart int64
}

// ForeignKey is a foreign key constraint.
type ForeignKey struct {
	Name       string
	Columns    []string
	RefTable   string
	RefColumns []string
	OnUpdate   string // NO ACTION, RESTRICT, CASCADE, SET NULL, SET DEFAULT
	OnDelete   string
	Deferrable bool
	Deferred   bool
	NotValid   bool
}

// Unique is a UNIQUE constraint.
type Unique struct {
	Name    string
	Columns []string
}

// IndexColumn is one key column of an index.
type IndexColumn struct {
	Name string
	Desc bool
}

// Index is a plain secondary index.
type Index struct {
	Name    string
	Unique  bool
	Columns []IndexColumn
	// Where is the predicate of a partial index in source dialect, "" for a full index.
	Where string
}

// Check is a CHECK constraint, its expression in the source dialect.
type Check struct {
	Name string
	Expr string
}

// Table is a table and everything the source declares about it.
type Table struct {
	Name    string
	Columns []*Column
	// PKName is the constraint name, "" when the engine does not report one.
	PKName  string
	PK      []string
	Uniques []Unique
	Indexes []Index
	Checks  []Check
	FKs     []ForeignKey
	// Rows is the row count read from the source.
	Rows int64
	// CreateSQL is the CREATE TABLE statement of a SQLite table, as stored.
	CreateSQL string
}

// Column returns the column called name, or nil.
func (t *Table) Column(name string) *Column {
	for _, c := range t.Columns {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// Catalog is everything read from a source database.
type Catalog struct {
	Engine Engine
	Schema string
	Tables []*Table
	// Skipped names objects the reader saw and cannot carry, as warnings.
	Notes []Warning
	// Objects are the stored definitions of the views, triggers and indexes of a SQLite database, in creation order.
	Objects []RawObject
}

// RawObject is a SQLite schema object as sqlite_master stores it.
type RawObject struct {
	Type, Name, Table, SQL string
}

// Table returns the table called name, or nil.
func (c *Catalog) Table(name string) *Table {
	for _, t := range c.Tables {
		if t.Name == name {
			return t
		}
	}
	return nil
}

// Warning kinds. Every feature that is not carried has its own kind so a machine
// can filter on it.
const (
	WarnViewNotCarried       = "view-not-carried"
	WarnTriggerNotCarried    = "trigger-not-carried"
	WarnTypeWidened          = "type-widened"
	WarnTypeApproximated     = "type-approximated"
	WarnDefaultNotCarried    = "default-not-carried"
	WarnDefaultTranslated    = "default-translated"
	WarnIndexNotCarried      = "index-not-carried"
	WarnCheckNotCarried      = "check-not-carried"
	WarnFKNotValidated       = "fk-not-validated"
	WarnFKNotCarried         = "fk-not-carried"
	WarnIdentityNotCarried   = "identity-not-carried"
	WarnGeneratedColumn      = "generated-column"
	WarnCollationNotCarried  = "collation-not-carried"
	WarnStorageOption        = "storage-option-not-carried"
	WarnCommentNotCarried    = "comment-not-carried"
	WarnObjectNotCarried     = "object-not-carried"
	WarnConstraintNameLost   = "constraint-name-not-carried"
	WarnIndexRenamed         = "index-renamed"
	WarnValuesNormalized     = "values-normalized"
	WarnForeignKeyViolations = "fk-violations-in-source"
	WarnNarrowedOnReturn     = "type-narrowed"
)

// Warning is one thing that was not carried exactly.
type Warning struct {
	Kind string `json:"kind"`
	// Object names the table, column, index or constraint the warning is about ("t", "t.c", "t.idx").
	Object  string `json:"object"`
	Message string `json:"message"`
}

func (w Warning) String() string {
	return fmt.Sprintf("%s: %s: %s", w.Kind, w.Object, w.Message)
}

func sortWarnings(ws []Warning) {
	sort.SliceStable(ws, func(i, j int) bool {
		if ws[i].Object != ws[j].Object {
			return ws[i].Object < ws[j].Object
		}
		return ws[i].Kind < ws[j].Kind
	})
}

// Marker is what a copy records on a PostgreSQL column so the column can go back to SQLite as it was.
type Marker struct {
	Src      string `json:"src"`
	Type     string `json:"type"`
	Identity string `json:"identity,omitempty"` // "rowid" | "autoincrement"
	// Carried is the PostgreSQL type text the column was given, so a later copy can tell a widened column.
	Widened string `json:"widened,omitempty"`
}

const markerPrefix = "datatug:"

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func quoteLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func quoteList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = quoteIdent(n)
	}
	return strings.Join(q, ", ")
}

// Queryer is what the catalog readers need: a *sql.DB, a *sql.Conn or a *sql.Tx.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
