package faithful

import (
	"fmt"
	"sort"
	"strings"
)

// This file is the seam of the SQL-script export: the neutral model of a script target (what each
// table, column and key looks like on the target) and the Dialect interface that a target engine
// implements. The live SQLite <-> PostgreSQL copy does not use any of it; it is a separate path that
// writes a file and never talks to a server.

// Warning kinds that only the script export raises.
const (
	// WarnKeyTypeBounded: a text or blob column of a key got a bounded type (an engine cannot index an unbounded one).
	WarnKeyTypeBounded = "key-type-bounded"
	// WarnRowSizeBounded: VARCHAR columns became TEXT so a row fits the engine's row size limit.
	WarnRowSizeBounded = "row-size-bounded"
	// WarnFKActionNotCarried: a foreign key is carried, one of its actions or its deferral is not.
	WarnFKActionNotCarried = "fk-action-not-carried"
	// WarnNameRenamed: a table or column name the engine cannot store was written under another name.
	WarnNameRenamed = "name-renamed"
	// WarnUniqueFiltered: a unique key over nullable columns that hold several NULLs is carried as a unique index filtered to the rows without NULL.
	WarnUniqueFiltered = "unique-filtered"
	// WarnNameCaseCollision: two names differ only by case and collide on an engine with case-insensitive names.
	WarnNameCaseCollision = "name-case-collision"
)

// Dialect is a target engine of the script export. Every method is pure: a dialect holds no connection.
type Dialect interface {
	// Name is the --dialect value ("mysql").
	Name() string
	// QuoteIdent quotes a name that StorableName accepted.
	QuoteIdent(name string) string
	// StorableName says why the engine cannot store the name, "" when it can.
	StorableName(name string) string
	// MaxIdentLen is the longest name in characters.
	MaxIdentLen() int
	// Choose picks the column type for the values the pre-scan saw.
	Choose(c *Column, st *ScriptStats, use ColumnUse) Choice
	// Adjust applies the rules that need the whole plan: unification of foreign key columns, key and row size
	// budgets, foreign key and index support. It may add warnings and may refuse (an error).
	Adjust(p *ScriptPlan) error
	// Default translates a SQLite default expression for the column; note is a warning text ("" for none).
	Default(expr string, ch Choice) (def string, note string, ok bool)
	// Check translates a SQLite CHECK expression; ok is false when it cannot, with the reason in note.
	Check(expr string, t *ScriptTable) (out string, note string, ok bool)
	// Literal is the one place where a value becomes SQL text.
	Literal(ch Choice, v any) (string, error)
	// Statements.
	CreateTable(t *ScriptTable) string
	IndexSQL(t *ScriptTable, ix Index) string
	CheckSQL(t *ScriptTable, ck Check) string
	FKSQL(p *ScriptPlan, t *ScriptTable, fk ForeignKey) string
	CounterSQL(t *ScriptTable, c *ScriptColumn, next int64) string
	// Script framing.
	Header(p *ScriptPlan, h Header) []string
	Prelude(p *ScriptPlan, h Header) []string
	Epilogue(p *ScriptPlan, h Header) []string
	BeginData(t *ScriptTable) []string
	EndData(t *ScriptTable) []string
	// InsertHead is the first line of a multi-row insert; rows follow one per line, "," between and ";" after.
	InsertHead(t *ScriptTable) string
	// BatchLimits are the most rows and bytes of one insert statement.
	BatchLimits() (rows, bytes int)
}

// ColumnUse tells Choose where a column is used.
type ColumnUse struct{ PK, Unique, Index, FK bool }

// Keyed is true when the column must be indexable.
func (u ColumnUse) Keyed() bool { return u.PK || u.Unique || u.Index || u.FK }

// Choice is the target type of a column and what is needed to write its values.
type Choice struct {
	Kind Kind
	// Family is the storage family: int, float, decimal, text, bytes, date, datetime, time.
	Family string
	Type   string
	// Bits is the width of an int (16, 32, 64). Len is the character length of a bounded text or the byte
	// length of a bounded binary, 0 for an unbounded type. Prec and Scale of a decimal; Fsp of a time.
	Bits, Len, Prec, Scale, Fsp int
	// Note is why the declared type could not be carried, "" when it was.
	Note string
	// Normalized is how many text values are spelled differently by the target (decimal text), with the first as Example.
	Normalized int64
	Example    string
	// Bounded marks a key column given a bounded type that was not declared so.
	Bounded bool
	// Tight is the shortest length that holds every value (characters of a text, bytes of a binary), at least 1.
	Tight int
	// MaxSize is the longest value in bytes; it decides the TEXT or BLOB size class of an unbounded type.
	MaxSize int
}

// ScriptColumn is a column as the script creates it.
type ScriptColumn struct {
	// Object names the column in a warning: table.column.
	Object  string
	Src     *Column
	Name    string
	Choice  Choice
	NotNull bool
	Default string
	AutoInc bool
	Stats   *ScriptStats
	Use     ColumnUse
}

// ScriptTable is a table as the script creates it.
type ScriptTable struct {
	Src     *Table
	Name    string
	Columns []*ScriptColumn
	PK      []string // target column names
	Uniques []Unique
	Indexes []Index
	Checks  []Check
	FKs     []ForeignKey
	Rows    int64
	// OrderBy is the ORDER BY of the source query: a total order.
	OrderBy string
}

// Col returns the target column called name, or nil.
func (t *ScriptTable) Col(name string) *ScriptColumn {
	for _, c := range t.Columns {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// ScriptPlan is the whole script as it will be written.
type ScriptPlan struct {
	Dialect  Dialect
	Tables   []*ScriptTable
	Warnings []Warning
}

// Table returns the target table called name, or nil.
func (p *ScriptPlan) Table(name string) *ScriptTable {
	for _, t := range p.Tables {
		if t.Name == name {
			return t
		}
	}
	return nil
}

func (p *ScriptPlan) warn(kind, object, format string, args ...any) {
	p.Warnings = append(p.Warnings, Warning{Kind: kind, Object: object, Message: fmt.Sprintf(format, args...)})
}

// Header is what the script says about itself.
type Header struct {
	// Name is the dataset name, Edition the contract edition label, License and Attribution as the provider gives them.
	Name, Edition, License, Attribution string
	// Notice is the multi-line licence notice (copyright line, permission notice, disclaimer, attribution) the
	// script header carries whole, as supplied. A dialect writes it with NoticeLines; ExportScript validates it.
	Notice       string
	SourceSHA256 string
	Version      string
}

var dialects = map[string]Dialect{}

func registerDialect(d Dialect) { dialects[d.Name()] = d }

// DialectByName returns the dialect called name.
func DialectByName(name string) (Dialect, error) {
	if d, ok := dialects[name]; ok {
		return d, nil
	}
	names := make([]string, 0, len(dialects))
	for n := range dialects {
		names = append(names, n)
	}
	sort.Strings(names)
	return nil, fmt.Errorf("unknown or not yet implemented dialect %q (implemented: %s)", name, strings.Join(names, ", "))
}
