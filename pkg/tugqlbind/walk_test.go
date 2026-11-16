package tugqlbind

import (
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/tugql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func parse(t *testing.T, document string) dal.StructuredQuery {
	t.Helper()
	query, err := tugql.Deserialize([]byte(document))
	require.NoError(t, err)
	return query
}

func names(query dal.StructuredQuery) []string {
	var visited []string
	Sources(query, func(ref dal.CollectionRef) { visited = append(visited, ref.Database()+"::"+ref.Name()) })
	return visited
}

// Every place a subquery can stand is walked, as DALgo collects databases.
func TestSourcesWalksEverySubquery(t *testing.T) {
	sub := func(database, name string) string {
		return "{query: {from: {database: " + database + ", name: " + name + ", alias: s}, columns: [{field: v, source: s}]}}"
	}
	query := parse(t, `from:
  database: a
  name: Root
  alias: r
  joins:
    - from: {database: b, name: Joined, alias: j}
      on:
        - {left: {field: x, source: r}, op: "==", right: {field: x, source: j}}
where:
  and:
    - {exists: {query: {from: {database: d, name: Exists}}}}
    - {notExists: {query: {from: {database: e, name: NotExists}}}}
    - {isNull: {field: x, source: r}}
    - or:
        - {left: {field: x, source: r}, op: In, right: `+sub("g", "Compared")+`}
columns:
  - `+sub("i", "Column")+`
orderBy:
  - `+sub("k", "Ordered")+`
`)
	assert.Equal(t, []string{"a::Root", "b::Joined", "d::Exists", "e::NotExists", "g::Compared", "i::Column", "k::Ordered"}, names(query))
	grouped := parse(t, "from: {database: a, name: Root, alias: r}\ngroupBy: [{field: x, source: r}]\ncolumns: [{aggregate: {function: count, args: [{field: x, source: r}]}, as: n}]\n")
	assert.Equal(t, []string{"a::Root"}, names(grouped))
}

func TestSourcesWalksAFromSubquery(t *testing.T) {
	query := parse(t, "from:\n  query:\n    as: inner\n    from: {name: Inner, alias: i}\n    columns: [{field: v, source: i}]\n")
	assert.Equal(t, []string{"::Inner"}, names(query))
	assert.Equal(t, "Inner", Unqualified(query))
	assert.Empty(t, Databases(query))
}

// The dal tree may also be built by hand, of pointers and values alike.
func TestSourcesWalksHandBuiltNodes(t *testing.T) {
	inner := parse(t, "from: {database: x, name: Inner}\n")
	derived := dal.NewQuerySource(inner, "s")
	pointer := dal.NewDatabaseCollectionRef("p", "", "Pointer", "")
	var nilRef *dal.CollectionRef
	var nilSource *dal.QuerySource
	exists := dal.NewExistsCondition(inner)
	isNull := dal.NewIsNullCondition(dal.NewQueryExpression(inner, "q"))
	comparison := dal.NewComparison(dal.Field("a"), dal.Equal, dal.NewQueryExpression(inner, "q"))
	group := dal.NewGroupCondition(dal.And, comparison)
	binary := dal.BinaryExpression{Left: dal.NewQueryExpression(inner, "l"), Right: dal.Field("b")}
	expression := dal.NewQueryExpression(inner, "e")
	visit := func(f func(func(dal.CollectionRef))) []string {
		var seen []string
		f(func(ref dal.CollectionRef) { seen = append(seen, ref.Name()) })
		return seen
	}
	assert.Equal(t, []string{"Pointer"}, visit(func(v func(dal.CollectionRef)) { walkSource(&pointer, v, 0) }))
	assert.Equal(t, []string{"Inner"}, visit(func(v func(dal.CollectionRef)) { walkSource(derived, v, 0) }))
	assert.Equal(t, []string{"Inner"}, visit(func(v func(dal.CollectionRef)) { walkSource(&derived, v, 0) }))
	assert.Empty(t, visit(func(v func(dal.CollectionRef)) { walkSource(nilRef, v, 0) }))
	assert.Empty(t, visit(func(v func(dal.CollectionRef)) { walkSource(nilSource, v, 0) }))
	for name, condition := range map[string]dal.Condition{"exists": &exists, "isNull": &isNull, "comparison": &comparison, "group": &group} {
		assert.Equal(t, []string{"Inner"}, visit(func(v func(dal.CollectionRef)) { walkCondition(condition, v, 0) }), name)
	}
	var nilExists *dal.ExistsCondition
	var nilIsNull *dal.IsNullCondition
	var nilComparison *dal.Comparison
	var nilGroup *dal.GroupCondition
	for _, condition := range []dal.Condition{nilExists, nilIsNull, nilComparison, nilGroup} {
		assert.Empty(t, visit(func(v func(dal.CollectionRef)) { walkCondition(condition, v, 0) }))
	}
	assert.Equal(t, []string{"Inner"}, visit(func(v func(dal.CollectionRef)) { walkExpression(&binary, v, 0) }))
	assert.Equal(t, []string{"Inner"}, visit(func(v func(dal.CollectionRef)) { walkExpression(&expression, v, 0) }))
	// An aggregate's own ORDER BY may hold a subquery, as DALgo walks it too.
	ordered := dal.NewOrderedAggregate("string_agg", []dal.OrderExpression{nil, dal.Ascending(expression)}, dal.Field("a"))
	assert.Equal(t, []string{"Inner"}, visit(func(v func(dal.CollectionRef)) { walkExpression(ordered, v, 0) }))
	var nilBinary *dal.BinaryExpression
	var nilExpression *dal.QueryExpression
	assert.Empty(t, visit(func(v func(dal.CollectionRef)) { walkExpression(nilBinary, v, 0) }))
	assert.Empty(t, visit(func(v func(dal.CollectionRef)) { walkExpression(nilExpression, v, 0) }))
	// A join with no nested from reads its recordset; a nil from reads nothing.
	joined := dal.From(&pointer).Join(dal.NewJoinedSource(dal.NewCollectionRef("Plain", "c", nil), dal.JoinInner))
	assert.Equal(t, []string{"Pointer", "Plain"}, visit(func(v func(dal.CollectionRef)) { walkFrom(joined, v, 0) }))
	assert.Empty(t, visit(func(v func(dal.CollectionRef)) { walkFrom(nil, v, 0) }))
	// The walk stops past its depth bound, and at a nil query.
	assert.Empty(t, visit(func(v func(dal.CollectionRef)) { walkQuery(inner, v, maxDepth+1) }))
	assert.Empty(t, visit(func(v func(dal.CollectionRef)) { walkQuery(nil, v, 0) }))
}

func TestUnqualifiedFindsAJoinOrASubquery(t *testing.T) {
	assert.Equal(t, "Dim", Unqualified(parse(t, "from:\n  database: a\n  name: Root\n  alias: r\n  joins:\n    - from: {name: Dim, alias: d}\n      on: [{left: {field: x, source: r}, op: \"==\", right: {field: y, source: d}}]\n")))
	assert.Equal(t, "Customer", Unqualified(parse(t, "from: {database: a, name: Root}\nwhere: {exists: {query: {from: {name: Customer}}}}\n")))
	assert.Empty(t, Unqualified(parse(t, "from: {database: a, name: Root}\n")))
	assert.Equal(t, map[string]bool{"a": true, "b": true}, Databases(parse(t, "from: {database: a, name: Root}\nwhere: {exists: {query: {from: {database: b, name: Customer}}}}\n")))
}
