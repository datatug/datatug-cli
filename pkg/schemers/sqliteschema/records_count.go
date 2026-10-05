package sqliteschema

import (
	"context"
	"database/sql"
	"fmt"
)

func (s schemaProvider) RecordsCount(_ context.Context, catalog, schema, object string) (count *int, err error) {
	_ = catalog
	var sqliteDB *sql.DB
	sqliteDB, err = s.getSqliteDB()
	if err != nil {
		return
	}
	quotedObject := quoteIdentifier(object)
	var query string
	if schema == "" {
		query = "SELECT COUNT(1) FROM " + quotedObject
	} else {
		quotedSchema := quoteIdentifier(schema)
		query = "SELECT COUNT(1) FROM " + quotedSchema + "." + quotedObject
	}
	var rows *sql.Rows
	rows, err = sqliteDB.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to get records count for %v.%v: %w", schema, object, err)
	}
	defer func() {
		err2 := rows.Close()
		if err == nil {
			err = err2
		}
	}()
	_ = rows.Next()
	count = new(int)
	return count, rows.Scan(count)
}
