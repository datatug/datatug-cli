package queryops

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/tugql"
)

const simpleQuery = `from: {name: Sales, alias: s}
where: {op: '==', left: {field: Region, source: s}, right: {param: Region}}
orderBy: [{field: total, source: s, desc: true}]
limit: 2
offset: 1
columns: [{field: category, source: s, as: category}, {field: total, source: s, as: total}]
`

func parsePreview(t *testing.T, preview Preview) dal.StructuredQuery {
	t.Helper()
	query, err := tugql.Deserialize([]byte(preview.DTQL))
	if err != nil {
		t.Fatalf("preview did not deserialize: %v\n%s", err, preview.DTQL)
	}
	encoded, err := tugql.Serialize(query)
	if err != nil || !bytes.Equal(encoded, []byte(preview.DTQL)) {
		t.Fatalf("preview not canonical/exact: %v\n%s\n%s", err, encoded, preview.DTQL)
	}
	return query
}

func TestDeriveMatchingSourceGroupKeepsSourceFilterParametersAndBoundsOutput(t *testing.T) {
	t.Parallel()
	preview, err := DeriveGroup(simpleQuery, []string{"category"}, true, MatchingSource)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Scope != MatchingSource || len(preview.OutputColumns) != 2 || preview.OutputColumns[0] != "category" || preview.OutputColumns[1] != "count" {
		t.Fatalf("preview metadata = %+v", preview)
	}
	query := parsePreview(t, preview)
	if query.Limit() != 2 || query.Offset() != 1 {
		t.Fatalf("matching source output bound/offset = %d/%d", query.Limit(), query.Offset())
	}
	if _, ok := query.From().Base().(dal.CollectionRef); !ok {
		t.Fatalf("matching source was unexpectedly wrapped: %T", query.From().Base())
	}
	if query.Where() == nil || !strings.Contains(preview.DTQL, "param: Region") {
		t.Fatalf("source filter parameter was not retained:\n%s", preview.DTQL)
	}
	if len(query.GroupBy()) != 1 || query.GroupBy()[0].String() != "s.category" {
		t.Fatalf("group keys = %v", query.GroupBy())
	}
	if len(query.Columns()) != 2 || query.Columns()[1].Alias != "count" || query.Limit() == 0 {
		t.Fatalf("group/count projection or bound = %+v", query.Columns())
	}
	original, err := tugql.Deserialize([]byte(simpleQuery))
	if err != nil {
		t.Fatal(err)
	}
	if original.Limit() != 2 || len(original.GroupBy()) != 0 || len(original.OrderBy()) != 1 {
		t.Fatalf("original AST was mutated: %+v", original)
	}
}

func TestDeriveMatchingSourceSortCanReachRowsOutsideOriginalLimit(t *testing.T) {
	t.Parallel()
	preview, err := DeriveSort(simpleQuery, []SortTerm{{Column: "total", Descending: true}}, MatchingSource)
	if err != nil {
		t.Fatal(err)
	}
	query := parsePreview(t, preview)
	if _, ok := query.From().Base().(dal.CollectionRef); !ok {
		t.Fatalf("sort wrapped the limited source: %T", query.From().Base())
	}
	if query.Limit() != 2 || query.Offset() != 1 {
		t.Fatalf("matching-source cap/offset = %d/%d", query.Limit(), query.Offset())
	}
	if len(query.OrderBy()) != 1 || query.OrderBy()[0].Expression().String() != "s.total" || !query.OrderBy()[0].Descending() {
		t.Fatalf("matching-source sort = %v", query.OrderBy())
	}
	if query.Where() == nil || !strings.Contains(preview.DTQL, "param: Region") {
		t.Fatalf("sort lost filter or parameter:\n%s", preview.DTQL)
	}
}

func TestOriginalOutputRetainsInnerLimitAndSortsOnlyItsOutput(t *testing.T) {
	t.Parallel()
	preview, err := DeriveSort(simpleQuery, []SortTerm{{Column: "total", Descending: true}}, OriginalOutput)
	if err != nil {
		t.Fatal(err)
	}
	query := parsePreview(t, preview)
	source, ok := query.From().Base().(dal.QuerySource)
	if !ok || source.Alias() != "original" {
		t.Fatalf("not wrapped as original output: %T", query.From().Base())
	}
	if source.Query().Limit() != 2 || query.Limit() != 2 || source.Query().Offset() != 1 {
		t.Fatalf("inner/outer scope limits changed: inner %d/%d outer %d", source.Query().Limit(), source.Query().Offset(), query.Limit())
	}
	if len(query.OrderBy()) != 1 || query.OrderBy()[0].Expression().String() != "original.total" {
		t.Fatalf("outer sort = %v", query.OrderBy())
	}
	if !strings.Contains(preview.DTQL, "param: Region") {
		t.Fatalf("inner parameter was dropped:\n%s", preview.DTQL)
	}
}

const aggregatedQuery = `from: {name: Sales, alias: s}
where: {op: '==', left: {field: Region, source: s}, right: {param: Region}}
groupBy: [{field: category, source: s}]
having: {op: '>', left: {aggregate: {function: count, args: [{star: true}]}}, right: {value: 1}}
limit: 3
columns: [{field: category, source: s, as: category}, {aggregate: {function: count, args: [{star: true}]}, as: count}]
`

func TestOriginalOutputGroupRetainsExistingAggregationHavingAndLimit(t *testing.T) {
	t.Parallel()
	preview, err := DeriveGroup(aggregatedQuery, []string{"category"}, true, OriginalOutput)
	if err != nil {
		t.Fatal(err)
	}
	query := parsePreview(t, preview)
	source, ok := query.From().Base().(dal.QuerySource)
	if !ok {
		t.Fatalf("not a derived original-output source: %T", query.From().Base())
	}
	inner := source.Query()
	if len(inner.GroupBy()) != 1 || inner.Having() == nil || inner.Limit() != 3 || inner.Where() == nil {
		t.Fatalf("inner clauses were lost: group=%v having=%v limit=%d where=%v", inner.GroupBy(), inner.Having(), inner.Limit(), inner.Where())
	}
	if query.Limit() != 3 || len(query.GroupBy()) != 1 || query.GroupBy()[0].String() != "original.category" {
		t.Fatalf("outer group/limit = group %v limit %d", query.GroupBy(), query.Limit())
	}
	if len(query.Columns()) != 2 || query.Columns()[1].Alias != "count" {
		t.Fatalf("outer explicit count = %+v", query.Columns())
	}
	if !strings.Contains(preview.DTQL, "param: Region") || !strings.Contains(preview.DTQL, "having:") {
		t.Fatalf("preview lost inner parameter/HAVING:\n%s", preview.DTQL)
	}
}

func TestDerivationRejectsAmbiguousUnknownUnsafeAndUnsupportedColumns(t *testing.T) {
	t.Parallel()
	ambiguous := `from: {name: T}
columns: [{field: id, as: key}, {field: name, as: key}]
`
	if _, err := DeriveSort(ambiguous, []SortTerm{{Column: "key"}}, OriginalOutput); !errors.Is(err, ErrInvalidColumn) {
		t.Fatalf("ambiguous alias error = %v", err)
	}
	if _, err := DeriveGroup(simpleQuery, []string{"missing"}, false, MatchingSource); !errors.Is(err, ErrInvalidColumn) {
		t.Fatalf("unknown column error = %v", err)
	}
	unsafe := `from: {name: T}
columns: [{field: id, as: bad-name}]
`
	if _, err := DeriveSort(unsafe, []SortTerm{{Column: "bad-name"}}, OriginalOutput); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsafe alias error = %v", err)
	}
	wildcard := `from: {name: T}
columns: [{wildcard: {exclude: []}}]
`
	if _, err := DeriveGroup(wildcard, []string{"id"}, false, OriginalOutput); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("wildcard projection error = %v", err)
	}
	derived := `from: {query: {from: {name: T}, columns: [{field: id}]}, as: d}
columns: [{field: id, source: d, as: id}]
`
	if _, err := DeriveGroup(derived, []string{"id"}, false, MatchingSource); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("derived MatchingSource error = %v", err)
	}
}

func TestMatchingSourceRefusesExistingAggregationAndCountAliasCollision(t *testing.T) {
	t.Parallel()
	if _, err := DeriveGroup(aggregatedQuery, []string{"category"}, true, MatchingSource); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("existing aggregate accepted: %v", err)
	}
	query := `from: {name: T}
columns: [{field: count}]
`
	if _, err := DeriveGroup(query, []string{"count"}, true, MatchingSource); !errors.Is(err, ErrInvalidColumn) {
		t.Fatalf("count alias collision = %v", err)
	}
	caseInsensitive := `from: {name: T}
columns: [{field: count, as: COUNT}]
`
	if _, err := DeriveGroup(caseInsensitive, []string{"COUNT"}, true, OriginalOutput); !errors.Is(err, ErrInvalidColumn) {
		t.Fatalf("case-insensitive count alias collision = %v", err)
	}
}

func TestPreviewLimitIsAlwaysBounded(t *testing.T) {
	t.Parallel()
	noLimit := strings.Replace(simpleQuery, "limit: 2\n", "", 1)
	preview, err := DeriveSort(noLimit, []SortTerm{{Column: "total"}}, MatchingSource)
	if err != nil {
		t.Fatal(err)
	}
	if query := parsePreview(t, preview); query.Limit() != MaxPreviewRows {
		t.Fatalf("default preview cap = %d", query.Limit())
	}
	large := strings.Replace(simpleQuery, "limit: 2\n", "limit: 9000\n", 1)
	preview, err = DeriveSort(large, []SortTerm{{Column: "total"}}, OriginalOutput)
	if err != nil {
		t.Fatal(err)
	}
	query := parsePreview(t, preview)
	source := query.From().Base().(dal.QuerySource)
	if source.Query().Limit() != 9000 || query.Limit() != 9000 {
		t.Fatalf("large input/outer cap = inner %d outer %d", source.Query().Limit(), query.Limit())
	}
}

func TestMatchingSourceRetainsJoinTreeAndJoinPredicates(t *testing.T) {
	t.Parallel()
	queryText := `from: {name: Customer, alias: c, joins: [{from: {name: Invoice, alias: i}, on: [{left: {field: CustomerId, source: c}, op: '==', right: {field: CustomerId, source: i}}]}]}
where: {op: '==', left: {field: Country, source: c}, right: {param: Country}}
limit: 4
columns: [{field: Name, source: c, as: customer}, {field: Total, source: i, as: total}]
`
	preview, err := DeriveSort(queryText, []SortTerm{{Column: "total", Descending: true}}, MatchingSource)
	if err != nil {
		t.Fatal(err)
	}
	derived := parsePreview(t, preview)
	if len(derived.From().Joins()) != 1 || len(derived.From().Joins()[0].On()) != 1 {
		t.Fatalf("join tree/predicate lost: %+v", derived.From().Joins())
	}
	if derived.Where() == nil || !strings.Contains(preview.DTQL, "param: Country") {
		t.Fatalf("WHERE parameter lost:\n%s", preview.DTQL)
	}
	if derived.OrderBy()[0].Expression().String() != "i.Total" || !derived.OrderBy()[0].Descending() {
		t.Fatalf("joined field ordering = %v", derived.OrderBy())
	}
}

func TestOriginalOutputGroupWithoutCountIsKeyOnly(t *testing.T) {
	t.Parallel()
	preview, err := DeriveGroup(simpleQuery, []string{"category"}, false, OriginalOutput)
	if err != nil {
		t.Fatal(err)
	}
	query := parsePreview(t, preview)
	if len(query.Columns()) != 1 || query.Columns()[0].Alias != "category" || len(preview.OutputColumns) != 1 {
		t.Fatalf("key-only grouping inferred an aggregate: %+v %+v", query.Columns(), preview.OutputColumns)
	}
	if strings.Contains(preview.DTQL, "aggregate:") {
		t.Fatalf("unexpected inferred aggregate:\n%s", preview.DTQL)
	}
}

func TestQueryOpsRemainingValidationAndErrorBranches(t *testing.T) {
	t.Parallel()
	// Empty terms in DeriveSort.
	if _, err := DeriveSort(simpleQuery, nil, MatchingSource); !errors.Is(err, ErrInvalidColumn) {
		t.Fatalf("expected ErrInvalidColumn for empty terms, got %v", err)
	}
	// Empty DTQL in DeriveSort.
	if _, err := DeriveSort("   ", []SortTerm{{Column: "total"}}, MatchingSource); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported for empty DTQL, got %v", err)
	}
	// Unknown scope in DeriveSort.
	if _, err := DeriveSort(simpleQuery, []SortTerm{{Column: "total"}}, Scope(99)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported for unknown scope, got %v", err)
	}
	// Duplicate sort selection.
	if _, err := DeriveSort(simpleQuery, []SortTerm{{Column: "total"}, {Column: "total"}}, MatchingSource); !errors.Is(err, ErrInvalidColumn) {
		t.Fatalf("expected ErrInvalidColumn for duplicate sort column, got %v", err)
	}
	// MatchingSource validation failure in DeriveSort.
	if _, err := DeriveSort(aggregatedQuery, []SortTerm{{Column: "category"}}, MatchingSource); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported for aggregated query in MatchingSource sort, got %v", err)
	}
	// serializePreview failure on nil query.
	if _, err := serializePreview(nil, nil, MatchingSource); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported from serializePreview(nil), got %v", err)
	}
	// projectedColumns edge cases: empty columns, nil expression, computed without alias.
	emptyColsQuery := dal.From(dal.NewRootCollectionRef("T", "")).NewQuery().SelectColumns()
	if _, err := projectedColumns(emptyColsQuery); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported for empty columns, got %v", err)
	}
	nilExprQuery := dal.From(dal.NewRootCollectionRef("T", "")).NewQuery().SelectColumns(dal.Column{Alias: "a"})
	if _, err := projectedColumns(nilExprQuery); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported for nil column expression, got %v", err)
	}
	computedNoAlias := dal.From(dal.NewRootCollectionRef("T", "")).NewQuery().SelectColumns(dal.Column{Expression: dal.Constant{Value: "x"}})
	if _, err := projectedColumns(computedNoAlias); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported for computed column without alias, got %v", err)
	}
	// validateMatchingSource edge cases: StartFrom, negative offset, wildcard, non-field expression.
	startFromQuery := dal.From(dal.NewRootCollectionRef("T", "")).NewQuery().StartFrom("cursor").SelectColumns(dal.Column{Alias: "id", Expression: dal.Field("id")})
	if err := validateMatchingSource(startFromQuery); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported for StartFrom query, got %v", err)
	}
	negOffsetQuery := dal.From(dal.NewRootCollectionRef("T", "")).NewQuery().Offset(-1).SelectColumns(dal.Column{Alias: "id", Expression: dal.Field("id")})
	if err := validateMatchingSource(negOffsetQuery); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported for negative offset, got %v", err)
	}
	wildcardQuery := dal.From(dal.NewRootCollectionRef("T", "")).NewQuery().SelectColumns(dal.Column{Wildcard: &dal.WildcardProjection{}})
	if err := validateMatchingSource(wildcardQuery); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported for wildcard column in validateMatchingSource, got %v", err)
	}
	constExprQuery := dal.From(dal.NewRootCollectionRef("T", "")).NewQuery().SelectColumns(dal.Column{Alias: "c", Expression: dal.Constant{Value: 1}})
	if err := validateMatchingSource(constExprQuery); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported for non-FieldRef column in validateMatchingSource, got %v", err)
	}
	// Cover wildcard in projectedColumns
	wildcardProjQ := dal.From(dal.NewRootCollectionRef("T", "")).NewQuery().SelectColumns(dal.Column{Wildcard: &dal.WildcardProjection{}})
	if _, err := projectedColumns(wildcardProjQ); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported for wildcard in projectedColumns, got %v", err)
	}

	// Cover derived source in validateMatchingSource
	subQ := dal.From(dal.NewRootCollectionRef("Sub", "")).NewQuery().SelectColumns(dal.Column{Alias: "id", Expression: dal.Field("id")})
	derivedQ := dal.From(dal.NewQuerySource(subQ, "sq")).NewQuery().SelectColumns(dal.Column{Alias: "id", Expression: dal.Field("id")})
	if err := validateMatchingSource(derivedQ); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported for derived source in validateMatchingSource, got %v", err)
	}

	// Cover nil and derived join in containsDerivedSource
	if !containsDerivedSource(nil) {
		t.Fatal("expected containsDerivedSource(nil) == true")
	}
	derivedChild := dal.From(dal.NewQuerySource(subQ, "sq"))
	fromWithDerivedJoin := dal.From(dal.NewRootCollectionRef("Main", "")).Join(dal.NewNestedJoinedSource(derivedChild, dal.JoinLeft, dal.NewComparison(dal.Field("Main.id"), dal.Equal, dal.Field("sq.id"))))
	if !containsDerivedSource(fromWithDerivedJoin) {
		t.Fatal("expected containsDerivedSource(fromWithDerivedJoin) == true")
	}

	// Cover join with nil From() in containsDerivedSource
	baseJoin := dal.From(dal.NewRootCollectionRef("Main", "")).Join(dal.NewJoinedSource(dal.NewRootCollectionRef("Other", "o"), dal.JoinInner, dal.NewComparison(dal.Field("Main.id"), dal.Equal, dal.Field("o.id"))))
	if containsDerivedSource(baseJoin) {
		t.Fatal("expected containsDerivedSource(baseJoin) == false")
	}
}
