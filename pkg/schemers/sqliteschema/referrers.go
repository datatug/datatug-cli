package sqliteschema

import (
	"context"
	"fmt"
	"strings"

	"github.com/datatug/datatug-cli/pkg/datatug-core/schemer"
)

func (s schemaProvider) GetReferrers(ctx context.Context, schema, table string) (referrers []schemer.ForeignKey, err error) {
	if table == "" {
		return nil, fmt.Errorf("table name cannot be empty")
	}
	db, err := s.getSqliteDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT name FROM sqlite_schema WHERE type = 'table'`)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("failed to scan table name: %w", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate over table names: %w", err)
	}

	targetTable := strings.Trim(table, "[]")
	for _, t := range tables {
		fks, err := s.GetForeignKeys(ctx, schema, t)
		if err != nil {
			return nil, err
		}
		for _, fk := range fks {
			if strings.Trim(fk.To.Name, "[]") == targetTable {
				referrers = append(referrers, fk)
			}
		}
	}
	return referrers, nil
}
