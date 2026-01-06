package sqlinfoschema

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/datatug-core/schemer"
)

var _ schemer.ConstraintsProvider = (*ConstraintsProvider)(nil)

type ConstraintsProvider struct {
	DB  *sql.DB
	SQL string
}

func (v ConstraintsProvider) GetConstraints(_ context.Context, catalog, schema, table string) (schemer.ConstraintsReader, error) {
	_ = catalog
	_ = schema
	_ = table
	rows, err := v.DB.Query(v.SQL)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve constraints: %w", err)
	}
	return ConstraintsReader{rows: rows}, nil
}

var _ schemer.ConstraintsReader = (*ConstraintsReader)(nil)

type ConstraintsReader struct {
	rows *sql.Rows
}

func (s ConstraintsReader) NextConstraint() (constraint *schemer.Constraint, err error) {
	if !s.rows.Next() {
		err = s.rows.Err()
		if err != nil {
			err = fmt.Errorf("failed to retrive constraints record: %w", err)
		}
		return
	}
	constraint = new(schemer.Constraint)
	constraint.Constraint = new(datatug.Constraint)
	if err = s.rows.Scan(
		&constraint.SchemaName, &constraint.TableName,
		&constraint.Type, &constraint.Name,
		&constraint.ColumnName,
		&constraint.UniqueConstraintCatalog, &constraint.UniqueConstraintSchema, &constraint.UniqueConstraintName,
		&constraint.MatchOption, &constraint.UpdateRule, &constraint.DeleteRule,
		&constraint.RefTableCatalog, &constraint.RefTableSchema, &constraint.RefTableName, &constraint.RefColName,
	); err != nil {
		err = fmt.Errorf("failed to scan constraints record: %w", err)
		return
	}
	return
}
