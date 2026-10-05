package sqliteschema

import (
	"database/sql"
	"fmt"
	"io"
	"strings"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/schemer"
)

type collectionsFilter struct {
	CollectionType datatug.CollectionType
	// warnings is where a table or view that is left out is named; nil says nothing.
	warnings io.Writer
}

// ownTablesFilter is the condition that keeps SQLite's own tables out of a list of
// tables: sqlite_sequence, sqlite_stat1 and every other name that starts with
// "sqlite_", which SQLite reserves for itself (a user cannot create one). They hold
// what SQLite keeps for its own use, and are not tables of the project.
const ownTablesFilter = `name NOT LIKE 'sqlite\_%' ESCAPE '\'`

// To load table parentKey.ID == "tables"
func getCollections(db *sql.DB, filter collectionsFilter) (reader schemer.CollectionsReader, err error) {
	r := &collectionsReader{warnings: filter.warnings}
	reader = r
	switch filter.CollectionType {
	case datatug.CollectionTypeAny, datatug.CollectionTypeUnknown:
		r.rows, err = db.Query(`SELECT type, name, sql FROM sqlite_schema WHERE type in ('table', 'view') AND ` + ownTablesFilter + ` ORDER BY name`)
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve list of SQLite tables and views: %w", err)
		}
	case datatug.CollectionTypeTable:
		r.collectionType = datatug.CollectionTypeTable
		r.rows, err = db.Query(`SELECT name, sql FROM sqlite_schema WHERE type = 'table' AND ` + ownTablesFilter + ` ORDER BY name`)
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve list of SQLite tables : %w", err)
		}
	case datatug.CollectionTypeView:
		r.collectionType = datatug.CollectionTypeView
		r.rows, err = db.Query(`SELECT name, sql FROM sqlite_schema WHERE type = 'view' ORDER BY name`)
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve list of SQLite views : %w", err)
		}
	default:
		err = fmt.Errorf("unexpected filter.CollectionType, got '%s'", filter.CollectionType)
		return
	}
	return
}

//goland:noinspection SqlNoDataSourceInspection

var _ schemer.CollectionsReader = (*collectionsReader)(nil)

type collectionsReader struct {
	collectionType datatug.CollectionType
	rows           *sql.Rows
	i              int
	warnings       io.Writer // where a table or view with no name is named; nil says nothing
}

func (s *collectionsReader) NextCollection() (c *datatug.CollectionInfo, err error) {
	for {
		s.i++
		if !s.rows.Next() {
			if err = s.rows.Err(); err != nil {
				err = fmt.Errorf("failed to retrieve db object row #%d: %w", s.i, err)
			} else {
				err = io.EOF
			}
			return
		}
		var name, dbType, sqlText string
		if s.collectionType == datatug.CollectionTypeUnknown {
			err = s.rows.Scan(&dbType, &name, &sqlText)
		} else {
			err = s.rows.Scan(&name, &sqlText)
		}
		if err != nil {
			return c, fmt.Errorf("failed to scan db row into CollectionInfo struct: %w", err)
		}
		var collectionType datatug.CollectionType
		var canonicalDbType string
		switch strings.ToLower(dbType) {
		case "table":
			collectionType = datatug.CollectionTypeTable
			canonicalDbType = "BASE TABLE" // the schemer scanner switches on "BASE TABLE"/"VIEW"
		case "view":
			collectionType = datatug.CollectionTypeView
			canonicalDbType = "VIEW"
		default:
			err = fmt.Errorf("unsupported DB type: %s", dbType)
			return
		}
		if name == "" {
			// SQLite allows an object with no name, and a collection key without a name
			// is a programming error in datatug-core. Name it and read on.
			warnf(s.warnings, "warning: %s %q of schema %q is left out of the project: its name is empty\n", strings.ToLower(dbType), name, mainSchema)
			continue
		}
		c = &datatug.CollectionInfo{
			DBCollectionKey: datatug.NewCollectionKey(collectionType, name, mainSchema, "", nil),
			TableProps: datatug.TableProps{
				DbType: canonicalDbType,
			},
			DDL: sqlText,
		}
		return
	}
}

// mainSchema is SQLite's default schema (database) name.
const mainSchema = "main"

// warnf writes a line to w, which may be nil: then nothing is said.
func warnf(w io.Writer, format string, args ...any) {
	if w != nil {
		_, _ = fmt.Fprintf(w, format, args...)
	}
}
