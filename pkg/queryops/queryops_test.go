package queryops

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dtql"
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
	query, err := dtql.Deserialize([]byte(preview.DTQL))
	if err != nil {
		t.Fatalf("preview did not deserialize: %v\n%s", err, preview.DTQL)
	}
	encoded, err := dtql.Serialize(query)
	if err != nil || !bytes.Equal(encoded, []byte(preview.DTQL)) {
		t.Fatalf("preview not canonical/exact: %v\n%s\n%s", err, encoded, preview.DTQL)
	}
	return query
}

func TestDeriveMatchingSourceGroupKeepsSourceFilterParametersAndBoundsOutput(t *testing.T) {
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
	original, err := dtql.Deserialize([]byte(simpleQuery))
	if err != nil {
		t.Fatal(err)
	}
	if original.Limit() != 2 || len(original.GroupBy()) != 0 || len(original.OrderBy()) != 1 {
		t.Fatalf("original AST was mutated: %+v", original)
	}
}

func TestDeriveMatchingSourceSortCanReachRowsOutsideOriginalLimit(t *testing.T) {
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
