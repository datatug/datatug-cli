package sqliteschema

import (
	"database/sql"
	"fmt"
	"io"
	"strings"

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/datatug-core/schemer"
)

type collectionsFilter struct {
	CollectionType datatug.CollectionType
}

// To load table parentKey.ID == "tables"
func getCollections(db *sql.DB, filter collectionsFilter) (reader schemer.CollectionsReader, err error) {
	r := &collectionsReader{}
	reader = r
	switch filter.CollectionType {
	case datatug.CollectionTypeAny, datatug.CollectionTypeUnknown:
		r.rows, err = db.Query(`SELECT type, name, sql FROM sqlite_schema WHERE type in ('table', 'view') ORDER BY name`)
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve list of SQLite tables and views: %w", err)
		}
	case datatug.CollectionTypeTable:
		r.collectionType = datatug.CollectionTypeTable
		r.rows, err = db.Query(`SELECT name, sql FROM sqlite_schema WHERE type  'table' ORDER BY name`)
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve list of SQLite tables : %w", err)
		}
	case datatug.CollectionTypeView:
		r.collectionType = datatug.CollectionTypeView
		r.rows, err = db.Query(`SELECT name, sql FROM sqlite_schema WHERE type  'view' ORDER BY name`)
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
}

func (s *collectionsReader) NextCollection() (c *datatug.CollectionInfo, err error) {
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
	switch strings.ToLower(dbType) {
	case "table":
		collectionType = datatug.CollectionTypeTable
	case "view":
		collectionType = datatug.CollectionTypeView
	default:
		err = fmt.Errorf("unsupported DB type: %s", dbType)
		return
	}
	c = &datatug.CollectionInfo{
		DBCollectionKey: datatug.NewCollectionKey(collectionType, name, "", "", nil),
		DDL:             sqlText,
	}
	return
}
