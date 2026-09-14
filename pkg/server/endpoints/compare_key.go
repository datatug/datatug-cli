package endpoints

import (
	"context"
	"fmt"
	"slices"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
)

type compareKeyMapping struct {
	column string
	entity string
	field  string
}

// resolveCompareKey implements the only implicit key allowed by the Compare
// feature: both live query definitions must declare the same primary-key
// columns, and each key column must map to the same entity field on both
// sides. The recordset comparer still verifies presence and uniqueness in the
// two actual policy-visible results.
func resolveCompareKey(ctx context.Context, request apicontract.CompareRequest) ([]string, error) {
	left, err := mappedCompareKey(ctx, request.QueryID, request.Left)
	if err != nil {
		return nil, err
	}
	right, err := mappedCompareKey(ctx, request.QueryID, request.Right)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(left, right) {
		return nil, newInvalidRequest("key", "the two live query scopes do not declare the same mapped key")
	}
	key := make([]string, len(left))
	for i, mapping := range left {
		key[i] = mapping.column
	}
	return key, nil
}

func mappedCompareKey(ctx context.Context, queryID string, side apicontract.CompareSideSpec) ([]compareKeyMapping, error) {
	if side.Kind == apicontract.CompareSideRecord {
		return nil, newInvalidRequest("key", "an explicit key is required when comparing an execution record")
	}
	projectDir, ok := api.ProjectDir(side.Project)
	if !ok {
		return nil, newNotFound(fmt.Sprintf("unknown project %q", side.Project))
	}
	canonicalID, err := api.ResolveQueryID(projectDir, queryID)
	if err != nil {
		return nil, newNotFound(fmt.Sprintf("query %q not found", queryID))
	}
	store, err := api.ProjectStoreFor(side.Project)
	if err != nil {
		return nil, newInvalidRequest("project", err.Error())
	}
	query, err := store.LoadQuery(ctx, canonicalID)
	if err != nil {
		return nil, newNotFound(fmt.Sprintf("query %q not found", queryID))
	}
	if len(query.Recordsets) != 1 || query.Recordsets[0].PrimaryKey == nil || len(query.Recordsets[0].PrimaryKey.Columns) == 0 {
		return nil, newInvalidRequest("key", "the live query must declare one recordset with a primary key or the caller must provide --key")
	}
	recordset := query.Recordsets[0]
	mappings := make([]compareKeyMapping, len(recordset.PrimaryKey.Columns))
	for i, keyColumn := range recordset.PrimaryKey.Columns {
		column := findRecordsetColumn(recordset.Columns, keyColumn)
		if column == nil || column.Meta == nil || column.Meta.Entity == "" || column.Meta.Field == "" {
			return nil, newInvalidRequest("key", fmt.Sprintf("key column %q has no mapped entity field", keyColumn))
		}
		mappings[i] = compareKeyMapping{column: keyColumn, entity: column.Meta.Entity, field: column.Meta.Field}
	}
	return mappings, nil
}

func findRecordsetColumn(columns datatug.RecordsetColumnDefs, name string) *datatug.RecordsetColumnDef {
	for i := range columns {
		if columns[i].Name == name {
			return &columns[i]
		}
	}
	return nil
}
