package mssqlschema

import (
	"context"

	"github.com/datatug/datatug-cli/pkg/datatug-core/schemer"
	"github.com/datatug/datatug-cli/pkg/schemers/sqlinfoschema"
)

var _ schemer.ConstraintsProvider = (*constraintsProvider)(nil)

type constraintsProvider struct {
	sqlinfoschema.ConstraintsProvider
}

func (v constraintsProvider) GetConstraints(c context.Context, catalog, schema, table string) (schemer.ConstraintsReader, error) {
	v.SQL = constraintsSQL
	return v.ConstraintsProvider.GetConstraints(c, catalog, schema, table)
}

//goland:noinspection SqlNoDataSourceInspection
const constraintsSQL = `
SELECT
	tc.TABLE_SCHEMA, tc.TABLE_NAME,
    tc.CONSTRAINT_TYPE, kcu.CONSTRAINT_NAME,
    kcu.COLUMN_NAME,-- kcu.ORDINAL_POSITION,
	rc.UNIQUE_CONSTRAINT_CATALOG, rc.UNIQUE_CONSTRAINT_SCHEMA, rc.UNIQUE_CONSTRAINT_NAME,
    rc.MATCH_OPTION, rc.UPDATE_RULE, rc.DELETE_RULE,
	kcu2.TABLE_CATALOG AS REF_TABLE_CATALOG, kcu2.TABLE_SCHEMA AS REF_TABLE_SCHEMA, kcu2.TABLE_NAME AS REF_TABLE_NAME, kcu2.COLUMN_NAME AS REF_COL_NAME
FROM INFORMATION_SCHEMA.KEY_COLUMN_USAGE AS kcu
INNER JOIN INFORMATION_SCHEMA.TABLE_CONSTRAINTS AS tc ON tc.CONSTRAINT_CATALOG = kcu.CONSTRAINT_CATALOG AND tc.CONSTRAINT_SCHEMA = kcu.CONSTRAINT_SCHEMA AND tc.CONSTRAINT_NAME = kcu.CONSTRAINT_NAME
LEFT JOIN INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS AS rc ON tc.CONSTRAINT_TYPE = 'FOREIGN KEY' AND rc.CONSTRAINT_CATALOG = tc.CONSTRAINT_CATALOG AND rc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND rc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
LEFT JOIN INFORMATION_SCHEMA.KEY_COLUMN_USAGE AS kcu2 ON kcu2.CONSTRAINT_CATALOG = rc.UNIQUE_CONSTRAINT_CATALOG AND kcu2.CONSTRAINT_SCHEMA = rc.UNIQUE_CONSTRAINT_SCHEMA AND kcu2.CONSTRAINT_NAME = rc.UNIQUE_CONSTRAINT_NAME AND kcu2.ORDINAL_POSITION = kcu.ORDINAL_POSITION
ORDER BY tc.TABLE_SCHEMA, tc.TABLE_NAME, tc.CONSTRAINT_TYPE, kcu.CONSTRAINT_NAME, kcu.ORDINAL_POSITION
`
