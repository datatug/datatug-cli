package sqlinfoschema

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/datatug-core/schemer"
)

var _ schemer.ColumnsProvider = (*ColumnsProvider)(nil)

type ColumnsProvider struct {
	DB  *sql.DB
	SQL string
}

func (v ColumnsProvider) GetColumns(c context.Context, catalog string, filter schemer.ColumnsFilter) ([]schemer.Column, error) {
	reader, err := v.GetColumnsReader(c, catalog, filter)
	if err != nil {
		return nil, err
	}
	var columns []schemer.Column
	for {
		column, err := reader.NextColumn()
		if err != nil {
			return nil, err
		}
		if column.Name == "" {
			break
		}
		columns = append(columns, column)
	}
	return columns, nil
}

func (v ColumnsProvider) GetColumnsReader(_ context.Context, catalog string, filter schemer.ColumnsFilter) (schemer.ColumnsReader, error) {
	_ = catalog
	_ = filter
	rows, err := v.DB.Query(v.SQL)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve columns: %w", err)
	}
	return ColumnsReader{rows: rows}, nil
}

var _ schemer.ColumnsReader = (*ColumnsReader)(nil)

type ColumnsReader struct {
	rows *sql.Rows
}

func (s ColumnsReader) NextColumn() (column schemer.Column, err error) {
	if !s.rows.Next() {
		err = s.rows.Err()
		if err != nil {
			err = fmt.Errorf("failed to retrieve column row: %w", err)
		}
		return column, err
	}
	column = schemer.Column{}
	c := &column.ColumnInfo
	var isNullable string
	var charSetCatalog, charSetSchema, charSetName sql.NullString
	var collationCatalog, collationSchema, collationName sql.NullString
	if err = s.rows.Scan(
		&column.SchemaName,
		&column.TableName,
		&c.Name,
		&c.OrdinalPosition,
		&c.Default,
		&isNullable,
		&c.DbType,
		&c.CharMaxLength,
		&c.CharOctetLength,
		&charSetCatalog,
		&charSetSchema,
		&charSetName,
		&collationCatalog,
		&collationSchema,
		&collationName,
	); err != nil {
		return column, fmt.Errorf("failed to scan column row into ColumnInfo struct: %w", err)
	}
	switch isNullable {
	case "YES":
		c.IsNullable = true
	case "NO":
		c.IsNullable = false
	default:
		err = fmt.Errorf("unknown value for IS_NULLABLE: %v", isNullable)
		return
	}
	if charSetName.Valid && charSetName.String != "" {
		c.CharacterSet = &datatug.CharacterSet{Name: charSetName.String}
		if charSetSchema.Valid {
			c.CharacterSet.Schema = charSetSchema.String
		}
		if charSetCatalog.Valid {
			c.CharacterSet.Catalog = charSetCatalog.String
		}
	}
	if collationName.Valid && collationName.String != "" {
		c.Collation = &datatug.Collation{Name: collationName.String}
	}
	return column, nil
}
