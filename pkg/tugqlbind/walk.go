package tugqlbind

import "github.com/dal-go/dalgo/dal"

// maxDepth bounds the walk. A parsed document is a tree, so the bound only
// guards a hand-built query that holds itself.
const maxDepth = 64

// Sources calls visit with every collection a query reads: its root and joined
// sources and, as DALgo's own database collection does, the sources of every
// subquery — a FROM subquery, an EXISTS or NOT EXISTS, and a subquery in a
// column, a condition, a GROUP BY or an ORDER BY expression.
func Sources(query dal.StructuredQuery, visit func(dal.CollectionRef)) {
	walkQuery(query, visit, 0)
}

// Databases is the set of database names the sources of query carry.
func Databases(query dal.StructuredQuery) map[string]bool {
	databases := map[string]bool{}
	Sources(query, func(ref dal.CollectionRef) {
		if ref.Database() != "" {
			databases[ref.Database()] = true
		}
	})
	return databases
}

// Unqualified is the first source of query, in walk order, that names no
// database, or "". DALgo's federated execution needs every source qualified.
func Unqualified(query dal.StructuredQuery) string {
	name := ""
	Sources(query, func(ref dal.CollectionRef) {
		if name == "" && ref.Database() == "" {
			name = ref.Name()
		}
	})
	return name
}

func walkQuery(query dal.StructuredQuery, visit func(dal.CollectionRef), depth int) {
	if query == nil || depth > maxDepth {
		return
	}
	depth++
	walkFrom(query.From(), visit, depth)
	walkCondition(query.Where(), visit, depth)
	walkCondition(query.Having(), visit, depth)
	for _, column := range query.Columns() {
		walkExpression(column.Expression, visit, depth)
	}
	for _, expression := range query.GroupBy() {
		walkExpression(expression, visit, depth)
	}
	for _, order := range query.OrderBy() {
		if order != nil {
			walkExpression(order.Expression(), visit, depth)
		}
	}
}

func walkFrom(from dal.FromSource, visit func(dal.CollectionRef), depth int) {
	if from == nil {
		return
	}
	walkSource(from.Base(), visit, depth)
	for _, joined := range from.Joins() {
		child := joined.From()
		if child == nil {
			child = dal.From(joined.RecordsetSource)
		}
		walkFrom(child, visit, depth)
		for _, on := range joined.On() {
			walkCondition(on, visit, depth)
		}
	}
}

func walkSource(source dal.RecordsetSource, visit func(dal.CollectionRef), depth int) {
	switch value := source.(type) {
	case dal.CollectionRef:
		visit(value)
	case *dal.CollectionRef:
		if value != nil {
			visit(*value)
		}
	case dal.QuerySource:
		walkQuery(value.Query(), visit, depth)
	case *dal.QuerySource:
		if value != nil {
			walkQuery(value.Query(), visit, depth)
		}
	}
}

func walkCondition(condition dal.Condition, visit func(dal.CollectionRef), depth int) {
	switch value := condition.(type) {
	case dal.ExistsCondition:
		walkQuery(value.Query(), visit, depth)
	case *dal.ExistsCondition:
		if value != nil {
			walkQuery(value.Query(), visit, depth)
		}
	case dal.IsNullCondition:
		walkExpression(value.Operand(), visit, depth)
	case *dal.IsNullCondition:
		if value != nil {
			walkExpression(value.Operand(), visit, depth)
		}
	case dal.Comparison:
		walkExpression(value.Left, visit, depth)
		walkExpression(value.Right, visit, depth)
	case *dal.Comparison:
		if value != nil {
			walkCondition(*value, visit, depth)
		}
	case dal.GroupCondition:
		for _, child := range value.Conditions() {
			walkCondition(child, visit, depth)
		}
	case *dal.GroupCondition:
		if value != nil {
			walkCondition(*value, visit, depth)
		}
	}
}

func walkExpression(expression dal.Expression, visit func(dal.CollectionRef), depth int) {
	switch value := expression.(type) {
	case dal.QueryExpression:
		walkQuery(value.Query(), visit, depth)
	case *dal.QueryExpression:
		if value != nil {
			walkQuery(value.Query(), visit, depth)
		}
	case dal.BinaryExpression:
		walkExpression(value.Left, visit, depth)
		walkExpression(value.Right, visit, depth)
	case *dal.BinaryExpression:
		if value != nil {
			walkExpression(*value, visit, depth)
		}
	case dal.AggregateFunc:
		for _, arg := range value.FuncArgs() {
			walkExpression(arg, visit, depth)
		}
	}
}
