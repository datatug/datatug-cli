package secureread

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dtql"
	"github.com/dal-go/dalgo2http"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/httpsource"
	"github.com/datatug/datatug-cli/pkg/queryops"
	"github.com/datatug/datatug-cli/pkg/resultops"
)

// Not parallel: mutates package seams.
func TestCoverage100_Recursive_ErrorBranches(t *testing.T) {
	ctx := context.Background()
	e := NewExecutor(Session{Unrestricted: true})

	// 1. Invalid variables (fails accesspolicies.ExactVariables)
	if _, err := e.RunRecursiveDTQL(ctx, "sqlite://file", []byte("from: {name: Item}\ncolumns: [{field: Id}]\n"), map[string]any{"bad": float32(1.23)}); err == nil {
		t.Fatal("expected invalid variable error")
	}

	// 2. Invalid document (fails dtql.Deserialize)
	if _, err := e.RunRecursiveDTQL(ctx, "sqlite://file", []byte("::: invalid"), nil); err == nil {
		t.Fatal("expected deserialize error")
	}

	// 3. Invalid query validation (fails ValidateRecursiveQuery)
	badQueryDoc := "from: {database: mydb, name: Item}\ncolumns: [{field: Id}]\n"
	if _, err := e.RunRecursiveDTQL(ctx, "sqlite://file", []byte(badQueryDoc), nil); err == nil {
		t.Fatal("expected validate error")
	}

	// 4. Misplaced parameter (fails bindRecursiveParameters -> accesspolicies.QueryParameters)
	misplacedParamDoc := "from: {name: Item}\ncolumns: [{field: Id}]\nwhere: {op: '==', left: {param: p}, right: {field: Id}}\n"
	if _, err := e.RunRecursiveDTQL(ctx, "sqlite://file", []byte(misplacedParamDoc), map[string]any{"p": 1}); err == nil {
		t.Fatal("expected misplaced param error")
	}

	// 5. condevalSubstitute seam error
	origSubstitute := condevalSubstitute
	defer func() { condevalSubstitute = origSubstitute }()
	condevalSubstitute = func(cond dal.Condition, fn func(name string) (any, bool)) (dal.Condition, error) {
		return nil, errors.New("simulated substitute error")
	}
	paramDoc := "from: {name: Item}\ncolumns: [{field: Id}]\nwhere: {op: '==', left: {field: Id}, right: {param: p}}\n"
	if _, err := e.RunRecursiveDTQL(ctx, "sqlite://file", []byte(paramDoc), map[string]any{"p": 1}); err == nil {
		t.Fatal("expected substitute error")
	}
	condevalSubstitute = origSubstitute

	// 6. Derived source with outer joins
	subqueryWithJoins := "from: {query: {from: {name: Item}, columns: [{field: Id}], as: sub}, joins: [{from: {name: Other}, on: [{left: {field: Id, source: sub}, op: '==', right: {field: OtherId, source: Other}}]}]}\ncolumns: [{field: Id}]\n"
	if q, err := dtql.Deserialize([]byte(subqueryWithJoins)); err == nil {
		if _, err := bindRecursiveParameters(q, nil); err == nil {
			t.Fatal("expected outer joins unsupported error")
		}
	} else {
		t.Fatalf("deserialize subqueryWithJoins: %v", err)
	}

	// 7. Inner query parameter binding error
	subqueryWithBadParam := "from: {query: {from: {name: Item}, columns: [{field: Id}], where: {op: '==', left: {param: p}, right: {field: Id}}, as: sub}}\ncolumns: [{field: Id}]\n"
	if q, err := dtql.Deserialize([]byte(subqueryWithBadParam)); err == nil {
		if _, err := bindRecursiveParameters(q, map[string]any{"p": 1}); err == nil {
			t.Fatal("expected inner bind error")
		}
	} else {
		t.Fatalf("deserialize subqueryWithBadParam: %v", err)
	}

	// 8. openSource error
	validDoc := "from: {name: Item}\ncolumns: [{field: Id}]\n"
	if _, err := e.RunRecursiveDTQL(ctx, "unsupported://db", []byte(validDoc), nil); err == nil {
		t.Fatal("expected openSource error")
	}

	// 9. collectRowsFn error
	source := exactSQLite(t)
	origCollect := collectRowsFn
	defer func() { collectRowsFn = origCollect }()
	collectRowsFn = func(reader dal.RecordsReader) ([]Row, *statisticsAccumulator, error) {
		return nil, nil, errors.New("simulated collect error")
	}
	if _, err := e.RunRecursiveDTQL(ctx, source, []byte(validDoc), nil); err == nil {
		t.Fatal("expected collectRows error")
	}
	collectRowsFn = origCollect
}

// Not parallel: mutates database/seams.
func TestCoverage100_Recursive_SuccessAndLeafReader(t *testing.T) {
	ctx := context.Background()
	e := NewExecutor(Session{Unrestricted: true})
	source := exactSQLite(t)

	// 1. Success with basic query
	basicDoc := "from: {name: Item}\ncolumns: [{field: Id}, {field: Category}]\n"
	res, err := e.RunRecursiveDTQL(ctx, source, []byte(basicDoc), nil)
	if err != nil {
		t.Fatalf("RunRecursiveDTQL failed: %v", err)
	}
	if len(res.Rows) == 0 {
		t.Fatal("expected rows")
	}

	// 2. Success with parameters
	paramDoc := "from: {name: Item}\ncolumns: [{field: Id}, {field: Category}]\nwhere: {op: '==', left: {field: Id}, right: {param: id}}\n"
	res, err = e.RunRecursiveDTQL(ctx, source, []byte(paramDoc), map[string]any{"id": 1})
	if err != nil {
		t.Fatalf("RunRecursiveDTQL with param failed: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(res.Rows))
	}

	// 3. Subquery execution and recursiveBoundQuery.From()
	subDoc := "from: {query: {from: {name: Item}, columns: [{field: Id}, {field: Category}], as: sub}}\ncolumns: [{field: Id}]\n"
	subQuery, err := dtql.Deserialize([]byte(subDoc))
	if err != nil {
		t.Fatal(err)
	}
	boundQ, err := bindRecursiveParameters(subQuery, nil)
	if err != nil {
		t.Fatal(err)
	}
	if boundQ.From() == nil {
		t.Fatal("expected From() to return source")
	}
	res, err = e.RunRecursiveDTQL(ctx, source, []byte(subDoc), nil)
	if err != nil {
		t.Fatalf("RunRecursiveDTQL subquery failed: %v", err)
	}
	if len(res.Rows) == 0 {
		t.Fatal("expected rows from subquery")
	}

	// 4. Aggregation query (guardAggregation = true, exercises exactAggregationReader)
	aggDoc := "from: {name: Item}\ncolumns: [{field: Category}]\ngroupBy: [{field: Category}]\n"
	res, err = e.RunRecursiveDTQL(ctx, source, []byte(aggDoc), nil)
	if err != nil {
		t.Fatalf("RunRecursiveDTQL agg failed: %v", err)
	}
	if len(res.Rows) == 0 {
		t.Fatal("expected aggregated rows")
	}

	// 5. recursiveAggregation checks
	if recursiveAggregation(subQuery) {
		t.Fatal("expected subQuery to not have recursive aggregation")
	}
	aggSubDoc := "from: {query: {from: {name: Item}, columns: [{field: Category}], groupBy: [{field: Category}], as: sub}}\ncolumns: [{field: Category}]\n"
	aggSubQ, _ := dtql.Deserialize([]byte(aggSubDoc))
	if !recursiveAggregation(aggSubQ) {
		t.Fatal("expected recursiveAggregation to be true for inner query with groupBy")
	}

	// 6. singleSourceSecuredLeaf error branches
	db, closeSrc, err := openSource(ctx, source, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSrc()

	var limitations []Limitation
	leaf := singleSourceSecuredLeaf{
		securedLeaf:      securedLeaf{db: db, session: e.session, variables: nil, limitations: &limitations},
		guardAggregation: true,
	}

	// Not structured query
	if _, err := leaf.ExecuteQueryToRecordsReader(ctx, dal.NewTextQuery("SELECT 1", nil)); err == nil {
		t.Fatal("expected error for non-structured query")
	}

	// Join count != 0
	joinedQ, _ := dtql.Deserialize([]byte("from: {name: Item, joins: [{from: {name: Other}}]}\ncolumns: [{field: Id}]\n"))
	if _, err := leaf.ExecuteQueryToRecordsReader(ctx, joinedQ); err == nil {
		t.Fatal("expected error for join query")
	}

	// Base not collection ref
	querySourceQ, _ := dtql.Deserialize([]byte(subDoc))
	if _, err := leaf.ExecuteQueryToRecordsReader(ctx, querySourceQ); err == nil {
		t.Fatal("expected error for non-collection ref base in leaf")
	}

	// Base collection ref with Database != ""
	namedDbQ, _ := dtql.Deserialize([]byte("from: {database: db1, name: Item}\ncolumns: [{field: Id}]\n"))
	if _, err := leaf.ExecuteQueryToRecordsReader(ctx, namedDbQ); err == nil {
		t.Fatal("expected error for named database")
	}

	// Pointer to CollectionRef
	rootRef := dal.NewRootCollectionRef("Item", "")
	ptrRefQuery := dal.NewQueryBuilder(dal.From(&rootRef)).SelectColumns(dal.Column{Expression: dal.NewFieldRef("Id", "")})
	mockLeaf := singleSourceSecuredLeaf{
		securedLeaf:      securedLeaf{db: aggregateTestDB{reader: dal.NewRecordsReader(nil)}, session: e.session, variables: nil, limitations: &limitations},
		guardAggregation: true,
	}
	if r, err := mockLeaf.ExecuteQueryToRecordsReader(ctx, ptrRefQuery); err != nil || r == nil {
		t.Fatalf("expected success with *CollectionRef: %v", err)
	} else {
		_ = r.Close()
	}

	// Pointer to CollectionRef with Database != ""
	namedRef := dal.NewDatabaseCollectionRef("db1", "", "Item", "")
	ptrNamedQuery := dal.NewQueryBuilder(dal.From(&namedRef)).SelectColumns(dal.Column{Expression: dal.NewFieldRef("Id", "")})
	if _, err := leaf.ExecuteQueryToRecordsReader(ctx, ptrNamedQuery); err == nil {
		t.Fatal("expected error for pointer to named database")
	}

	// 7. exactAggregationReader Next() branches
	// Empty reader -> Next() returns EOF or nil item
	emptyReader := exactAggregationReader{RecordsReader: dal.NewRecordsReader(nil)}
	item, err := emptyReader.Next()
	if err == nil && item != nil {
		t.Fatal("expected nil item on empty reader")
	}

	// Item exists false
	notExistsItem := record.NewRecord(record.NewKeyWithID("Item", 1))
	_ = notExistsItem.SetError(record.ErrRecordNotFound)
	notExistsReader := exactAggregationReader{RecordsReader: dal.NewRecordsReader([]record.Record{notExistsItem})}
	if it, _ := notExistsReader.Next(); it == nil || it.Exists() {
		t.Fatal("expected not exists item returned")
	}

	// Item with unmarshalable data (resultDataMap error)
	badDataItem := record.NewRecordWithData(record.NewKeyWithID("Item", 1), make(chan int))
	badDataReader := exactAggregationReader{RecordsReader: dal.NewRecordsReader([]record.Record{badDataItem})}
	if _, err := badDataReader.Next(); err == nil {
		t.Fatal("expected resultDataMap error on Next")
	}

	// Item with non-exact variable (int64 exceeding exact comparison range)
	floatDataItem := record.NewRecordWithData(record.NewKeyWithID("Item", 1), map[string]any{"v": int64(9007199254740993)})
	floatDataReader := exactAggregationReader{RecordsReader: dal.NewRecordsReader([]record.Record{floatDataItem})}
	if _, err := floatDataReader.Next(); err == nil {
		t.Fatal("expected exact variables error on Next")
	}
}

// Not parallel: tests ValidateRecursiveQuery branches.
func TestCoverage100_ValidateRecursiveQuery(t *testing.T) {
	// query == nil
	if err := ValidateRecursiveQuery(nil); err == nil {
		t.Fatal("expected error for nil query")
	}

	// query.From() == nil
	emptyQ := dal.NewQueryBuilder(nil).SelectColumns()
	if err := ValidateRecursiveQuery(emptyQ); err == nil {
		t.Fatal("expected error for nil from")
	}

	// SUM / AVERAGE aggregation function in columns
	sumQ, _ := dtql.Deserialize([]byte("from: {name: Item}\ncolumns: [{expression: {function: SUM, args: [{field: Id}]}}]\n"))
	if err := ValidateRecursiveQuery(sumQ); err == nil {
		t.Fatal("expected error for SUM in query")
	}
	avgQ, _ := dtql.Deserialize([]byte("from: {name: Item}\ncolumns: [{expression: {function: AVERAGE, args: [{field: Id}]}}]\n"))
	if err := ValidateRecursiveQuery(avgQ); err == nil {
		t.Fatal("expected error for AVERAGE in query")
	}

	// BinaryExpression in column
	binColQ := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("Item", ""))).
		SelectColumns(dal.Column{Expression: dal.Binary(dal.NewFieldRef("Id", ""), dal.Add, dal.NewFieldRef("Id", ""))})
	if err := ValidateRecursiveQuery(binColQ); err == nil {
		t.Fatal("expected error for BinaryExpression in column")
	}

	// *BinaryExpression in column
	binExpr := dal.Binary(dal.NewFieldRef("Id", ""), dal.Add, dal.NewFieldRef("Id", ""))
	ptrBinColQ := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("Item", ""))).
		SelectColumns(dal.Column{Expression: &binExpr})
	if err := ValidateRecursiveQuery(ptrBinColQ); err == nil {
		t.Fatal("expected error for *BinaryExpression in column")
	}

	// Aggregate with arg error (SUM inside COUNT)
	countSumQ, _ := dtql.Deserialize([]byte("from: {name: Item}\ncolumns: [{expression: {function: COUNT, args: [{expression: {function: SUM, args: [{field: Id}]}}]}}]\n"))
	if err := ValidateRecursiveQuery(countSumQ); err == nil {
		t.Fatal("expected error for nested SUM in COUNT")
	}

	// OrderBy expression error
	orderBinQ := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("Item", ""))).
		OrderBy(dal.Ascending(dal.Binary(dal.NewFieldRef("Id", ""), dal.Add, dal.NewFieldRef("Id", "")))).
		SelectColumns()
	if err := ValidateRecursiveQuery(orderBinQ); err == nil {
		t.Fatal("expected error for BinaryExpression in OrderBy")
	}

	// Having condition error (Comparison with binary expr)
	havingQ := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("Item", ""))).
		Having(dal.NewComparison(dal.Binary(dal.NewFieldRef("Id", ""), dal.Add, dal.NewFieldRef("Id", "")), dal.Equal, dal.NewConstant(1))).
		SelectColumns()
	if err := ValidateRecursiveQuery(havingQ); err == nil {
		t.Fatal("expected error for BinaryExpression in Having")
	}

	// Where condition error (Comparison Right with binary expr)
	whereQ := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("Item", ""))).
		Where(dal.NewComparison(dal.NewFieldRef("Id", ""), dal.Equal, dal.Binary(dal.NewFieldRef("Id", ""), dal.Add, dal.NewFieldRef("Id", "")))).
		SelectColumns()
	if err := ValidateRecursiveQuery(whereQ); err == nil {
		t.Fatal("expected error for BinaryExpression in Where comparison right")
	}

	// *Comparison condition
	cmp := dal.NewComparison(dal.Binary(dal.NewFieldRef("Id", ""), dal.Add, dal.NewFieldRef("Id", "")), dal.Equal, dal.NewConstant(1))
	ptrCmpQ := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("Item", ""))).Where(&cmp).SelectColumns()
	if err := ValidateRecursiveQuery(ptrCmpQ); err == nil {
		t.Fatal("expected error for *Comparison with binary expr")
	}

	// IsNullCondition with binary expr
	isNullQ := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("Item", ""))).
		Where(dal.NewIsNullCondition(dal.Binary(dal.NewFieldRef("Id", ""), dal.Add, dal.NewFieldRef("Id", "")))).
		SelectColumns()
	if err := ValidateRecursiveQuery(isNullQ); err == nil {
		t.Fatal("expected error for IsNull on binary expr")
	}

	// *IsNullCondition with binary expr
	isNullCond := dal.NewIsNullCondition(dal.Binary(dal.NewFieldRef("Id", ""), dal.Add, dal.NewFieldRef("Id", "")))
	ptrIsNullQ := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("Item", ""))).Where(&isNullCond).SelectColumns()
	if err := ValidateRecursiveQuery(ptrIsNullQ); err == nil {
		t.Fatal("expected error for *IsNull on binary expr")
	}

	// GroupCondition with child error
	grpCond := dal.NewGroupCondition(dal.And, dal.NewComparison(dal.Binary(dal.NewFieldRef("Id", ""), dal.Add, dal.NewFieldRef("Id", "")), dal.Equal, dal.NewConstant(1)))
	grpQ := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("Item", ""))).Where(grpCond).SelectColumns()
	if err := ValidateRecursiveQuery(grpQ); err == nil {
		t.Fatal("expected error for GroupCondition child")
	}

	// *GroupCondition
	ptrGrpO := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("Item", ""))).Where(&grpCond).SelectColumns()
	if err := ValidateRecursiveQuery(ptrGrpO); err == nil {
		t.Fatal("expected error for *GroupCondition child")
	}

	// Joins error
	joinQ, _ := dtql.Deserialize([]byte("from: {name: Item, joins: [{from: {name: Other}}]}\ncolumns: [{field: Id}]\n"))
	if err := ValidateRecursiveQuery(joinQ); err == nil {
		t.Fatal("expected error for joins in ValidateRecursiveQuery")
	}

	// *dal.QuerySource
	validInner, _ := dtql.Deserialize([]byte("from: {name: Item}\ncolumns: [{field: Id}]\n"))
	qs := dal.NewQuerySource(validInner, "sub")
	ptrQsQ := dal.NewQueryBuilder(dal.From(&qs)).SelectColumns(dal.Column{Expression: dal.NewFieldRef("Id", "")})
	if err := ValidateRecursiveQuery(ptrQsQ); err != nil {
		t.Fatalf("expected success with *QuerySource: %v", err)
	}

	// *dal.CollectionRef with database
	rootWithDb := dal.NewDatabaseCollectionRef("db1", "", "Item", "")
	ptrNamedQ := dal.NewQueryBuilder(dal.From(&rootWithDb)).SelectColumns(dal.Column{Expression: dal.NewFieldRef("Id", "")})
	if err := ValidateRecursiveQuery(ptrNamedQ); err == nil {
		t.Fatal("expected error for *CollectionRef with database")
	}
}

// Not parallel: tests rows and cell detachment edge cases.
func TestCoverage100_Rows_DetachmentEdgeCases(t *testing.T) {
	// resultDataMap with nil pointer / interface
	var nilPtr *int
	res, err := resultDataMap(nilPtr)
	if err != nil || res != nil {
		t.Fatalf("expected nil, nil: %v, %v", res, err)
	}

	// resultDataMap with struct that marshals to JSON
	type SimpleStruct struct {
		Name string `json:"name"`
		Val  int    `json:"val"`
	}
	res, err = resultDataMap(SimpleStruct{Name: "test", Val: 42})
	if err != nil || res["name"] != "test" {
		t.Fatalf("expected struct to unmarshal to map: %v, %v", res, err)
	}

	// resultDataMap with non-map data that marshals to a JSON scalar (cannot decode to map)
	if _, err := resultDataMap(12345); err == nil {
		t.Fatal("expected decode error for scalar")
	}

	// resultDataMap with data that fails json.Marshal
	if _, err := resultDataMap(make(chan int)); err == nil {
		t.Fatal("expected marshal error for chan")
	}

	// resultDataMap with map containing detach error
	if _, err := resultDataMap(map[string]any{"err": make(chan int)}); err == nil {
		t.Fatal("expected column detach error")
	}

	// detachResultCell depth > 64
	if _, err := detachResultCell(1, 65); err == nil {
		t.Fatal("expected depth > 64 error")
	}

	// detachResultCell float64 NaN/Inf
	if _, err := detachResultCell(math.NaN(), 0); err == nil {
		t.Fatal("expected NaN error")
	}
	if _, err := detachResultCell(math.Inf(1), 0); err == nil {
		t.Fatal("expected Inf error")
	}

	// detachResultCell float32 NaN/Inf
	if _, err := detachResultCell(float32(math.NaN()), 0); err == nil {
		t.Fatal("expected float32 NaN error")
	}
	if _, err := detachResultCell(float32(math.Inf(-1)), 0); err == nil {
		t.Fatal("expected float32 Inf error")
	}

	// detachResultCell []byte
	b := []byte("hello")
	detached, err := detachResultCell(b, 0)
	if err != nil || string(detached.([]byte)) != "hello" {
		t.Fatalf("expected byte slice: %v, %v", detached, err)
	}

	// detachResultCell nil pointer
	if cell, err := detachResultCell((*string)(nil), 0); err != nil || cell != nil {
		t.Fatalf("expected nil cell: %v, %v", cell, err)
	}

	// detachResultCell valid pointer
	strVal := "abc"
	if cell, err := detachResultCell(&strVal, 0); err != nil || cell != "abc" {
		t.Fatalf("expected unwrapped string: %v, %v", cell, err)
	}

	// detachResultCell map with non-string keys
	if _, err := detachResultCell(map[int]string{1: "a"}, 0); err == nil {
		t.Fatal("expected non-string keys error")
	}

	// detachResultCell map with nested error
	if _, err := detachResultCell(map[string]any{"bad": make(chan int)}, 0); err == nil {
		t.Fatal("expected map nested error")
	}

	// detachResultCell slice with nested error
	if _, err := detachResultCell([]any{make(chan int)}, 0); err == nil {
		t.Fatal("expected slice nested error")
	}

	// detachResultCell array with nested error
	if _, err := detachResultCell([1]any{make(chan int)}, 0); err == nil {
		t.Fatal("expected array nested error")
	}

	// detachResultCell default with chan (json.Marshal error)
	if _, err := detachResultCell(make(chan int), 0); err == nil {
		t.Fatal("expected chan error")
	}

	// detachResultCell default with time.Time
	now := time.Now()
	if cell, err := detachResultCell(now, 0); err != nil || cell != now {
		t.Fatalf("expected time.Time: %v, %v", cell, err)
	}
}

// Not parallel: tests exact aggregate edge cases and helper functions.
func TestCoverage100_ExactAggregate_EdgeCases(t *testing.T) {
	ctx := context.Background()
	e := NewExecutor(Session{Unrestricted: true})

	// RunExactAggregateDTQL with openSource error
	p, err := exactPlanForRows(t, "INTEGER", nil)
	if err != nil {
		t.Fatal(err)
	}
	original := "from: {name: Item}\ncolumns: [{field: Category}, {field: Value}]\nlimit: 2\n"
	preview := exactPreview(t, original, queryops.MatchingSource, nil)
	cols := []resultops.Column{{Name: "Category", Type: "TEXT"}, {Name: "Value", Type: "INTEGER"}}

	if _, err := e.RunExactAggregateDTQL(ctx, "unsupported://db", []byte(original), []byte(preview.DTQL), nil, cols); err == nil {
		t.Fatal("expected openSource error")
	}

	// Provenance recorder branch in RunExactAggregateDTQL
	httpCtx := httpsource.ContextWithDispatch(context.Background(), dalgo2http.ModeSnapshot, false, 0)
	httpSource := newHTTPSnapshotFixture(t)
	httpOriginal := "from: {name: countries}\ncolumns: [{field: code}, {field: name}]\nlimit: 2\n"
	httpPreview, err := queryops.DeriveGroupAggregates(httpOriginal, []string{"code"}, true, nil, queryops.MatchingSource)
	if err != nil {
		t.Fatal(err)
	}
	httpCols := []resultops.Column{{Name: "code", Type: "TEXT"}, {Name: "name", Type: "TEXT"}}
	res, err := e.RunExactAggregateDTQL(httpCtx, httpSource, []byte(httpOriginal), []byte(httpPreview.DTQL), nil, httpCols)
	if err != nil {
		t.Fatalf("HTTP exact aggregate: %v", err)
	}
	if res.Provenance == nil {
		t.Fatal("expected Provenance to be populated from recorder")
	}

	// prepareExactAggregate edge cases:
	// 1. Plan exceeds budget
	hugeDoc := make([]byte, (1<<20)+1)
	if _, err := prepareExactAggregate(hugeDoc, []byte(preview.DTQL), cols); err == nil {
		t.Fatal("expected metadata budget error")
	}

	// 2. Original deserialize error
	if _, err := prepareExactAggregate([]byte(":::"), []byte(preview.DTQL), cols); err == nil {
		t.Fatal("expected original deserialize error")
	}

	// 3. Preview deserialize error
	if _, err := prepareExactAggregate([]byte(original), []byte(":::"), cols); err == nil {
		t.Fatal("expected preview deserialize error")
	}

	// 4. Original has joins
	joinOrig := "from: {name: Item, joins: [{from: {name: Other}}]}\ncolumns: [{field: Category}]\nlimit: 2\n"
	if _, err := prepareExactAggregate([]byte(joinOrig), []byte(preview.DTQL), cols); err == nil {
		t.Fatal("expected joins error")
	}

	// 5. Original base not CollectionRef
	subOrig := "from: {query: {from: {name: Item}, columns: [{field: Category}], as: sub}}\ncolumns: [{field: Category}]\nlimit: 2\n"
	if _, err := prepareExactAggregate([]byte(subOrig), []byte(preview.DTQL), cols); err == nil {
		t.Fatal("expected non-collection ref base error")
	}

	// 6. Original ref with Database != ""
	dbOrig := "from: {database: db1, name: Item}\ncolumns: [{field: Category}]\nlimit: 2\n"
	if _, err := prepareExactAggregate([]byte(dbOrig), []byte(preview.DTQL), cols); err == nil {
		t.Fatal("expected named database error")
	}

	// 7. Original cursor / bound error (startFrom != "")
	startOrig := original + "startFrom: \"abc\"\n"
	if _, err := prepareExactAggregate([]byte(startOrig), []byte(preview.DTQL), cols); err == nil {
		t.Fatal("expected cursor error")
	}

	// 8. Original recursive validation error
	valOrig := "from: {name: Item}\ncolumns: [{expression: {function: SUM, args: [{field: Value}]}}]\nlimit: 2\n"
	if _, err := prepareExactAggregate([]byte(valOrig), []byte(preview.DTQL), cols); err == nil {
		t.Fatal("expected recursive validate error")
	}

	// 9. Preview having / unbounded:
	badPreviewHaving := preview.DTQL + "having: {op: '>', left: {field: count}, right: {value: 1}}\n"
	if _, err := prepareExactAggregate([]byte(original), []byte(badPreviewHaving), cols); err == nil {
		t.Fatal("expected having error")
	}

	// 10. Column with empty Name or Type
	if _, err := prepareExactAggregate([]byte(original), []byte(preview.DTQL), []resultops.Column{{Name: "", Type: "INT"}}); err == nil {
		t.Fatal("expected empty name error")
	}
	if _, err := prepareExactAggregate([]byte(original), []byte(preview.DTQL), []resultops.Column{{Name: "A", Type: ""}}); err == nil {
		t.Fatal("expected empty type error")
	}

	// 11. Duplicate column metadata
	if _, err := prepareExactAggregate([]byte(original), []byte(preview.DTQL), []resultops.Column{{Name: "Category", Type: "TEXT"}, {Name: "Category", Type: "TEXT"}}); err == nil {
		t.Fatal("expected duplicate metadata error")
	}

	// 12. Original with non-field expression (wildcard or complex)
	wildOrig := "from: {name: Item}\ncolumns: [{wildcard: true}]\nlimit: 2\n"
	if _, err := prepareExactAggregate([]byte(wildOrig), []byte(preview.DTQL), cols); err == nil {
		t.Fatal("expected wildcard error")
	}

	// 13. Ambiguous original projection (duplicate alias)
	dupAliasOrig := "from: {name: Item}\ncolumns: [{field: Category, as: X}, {field: Value, as: X}]\nlimit: 2\n"
	if _, err := prepareExactAggregate([]byte(dupAliasOrig), []byte(preview.DTQL), cols); err == nil {
		t.Fatal("expected duplicate alias error")
	}

	// 14. Ambiguous original field projection (duplicate field)
	dupFieldOrig := "from: {name: Item}\ncolumns: [{field: Category}, {field: Category}]\nlimit: 2\n"
	if _, err := prepareExactAggregate([]byte(dupFieldOrig), []byte(preview.DTQL), cols); err == nil {
		t.Fatal("expected duplicate field error")
	}

	// 15. Preview groupBy expression not FieldRef
	constGroupPrev := "from: {name: Item}\ncolumns: [{value: 1, as: Category}]\ngroupBy: [{value: 1}]\nlimit: 2\n"
	if _, err := prepareExactAggregate([]byte(original), []byte(constGroupPrev), cols); err == nil {
		t.Fatal("expected field ref error for group by")
	}

	// 16. Preview groupBy outside original projection
	otherGroupPrev := "from: {name: Item}\ncolumns: [{field: NonExistent}]\ngroupBy: [{field: NonExistent}]\nlimit: 2\n"
	if _, err := prepareExactAggregate([]byte(original), []byte(otherGroupPrev), cols); err == nil {
		t.Fatal("expected outside projection error")
	}

	// 17. Group projection doesn't match preview keys
	mismatchGroupPrev := "from: {name: Item}\ncolumns: [{field: Value, as: Value}]\ngroupBy: [{field: Category}]\nlimit: 2\n"
	if _, err := prepareExactAggregate([]byte(original), []byte(mismatchGroupPrev), cols); err == nil {
		t.Fatal("expected mismatch group projection error")
	}

	// 18. Unsupported aggregate expression (not aggregate func or args != 1)
	nonAggPrev := "from: {name: Item}\ncolumns: [{field: Category}, {field: Value}]\ngroupBy: [{field: Category}]\nlimit: 2\n"
	if _, err := prepareExactAggregate([]byte(original), []byte(nonAggPrev), cols); err == nil {
		t.Fatal("expected unsupported aggregate expression")
	}

	// 19. DISTINCT aggregate
	distinctPrev := "from: {name: Item}\ncolumns: [{field: Category}, {aggregate: {function: COUNT, distinct: true, args: [{field: Value}]}, as: c}]\ngroupBy: [{field: Category}]\nlimit: 2\n"
	if _, err := prepareExactAggregate([]byte(original), []byte(distinctPrev), cols); err == nil {
		t.Fatal("expected distinct aggregate error")
	}

	// 20. COUNT(*) not separate count output (e.g. alias != count)
	countStarPrev := "from: {name: Item}\ncolumns: [{field: Category}, {aggregate: {function: COUNT, args: [{wildcard: true}]}, as: total}]\ngroupBy: [{field: Category}]\nlimit: 2\n"
	if _, err := prepareExactAggregate([]byte(original), []byte(countStarPrev), cols); err == nil {
		t.Fatal("expected count(*) alias error")
	}

	// 21. Aggregate argument outside original projection
	aggOutsidePrev := "from: {name: Item}\ncolumns: [{field: Category}, {aggregate: {function: SUM, args: [{field: Missing}]}, as: total}]\ngroupBy: [{field: Category}]\nlimit: 2\n"
	if _, err := prepareExactAggregate([]byte(original), []byte(aggOutsidePrev), cols); err == nil {
		t.Fatal("expected aggregate arg outside projection error")
	}

	// 22. Unsupported aggregate function (e.g. AVG)
	avgPrev := "from: {name: Item}\ncolumns: [{field: Category}, {aggregate: {function: AVG, args: [{field: Value}]}, as: total}]\ngroupBy: [{field: Category}]\nlimit: 2\n"
	if _, err := prepareExactAggregate([]byte(original), []byte(avgPrev), cols); err == nil {
		t.Fatal("expected AVG unsupported aggregate error")
	}

	// 23. dtqlSerialize seam error
	origSerialize := dtqlSerialize
	defer func() { dtqlSerialize = origSerialize }()
	dtqlSerialize = func(q dal.StructuredQuery) ([]byte, error) {
		return nil, errors.New("simulated serialize error")
	}
	if _, err := prepareExactAggregate([]byte(original), []byte(preview.DTQL), cols); err == nil {
		t.Fatal("expected dtqlSerialize error")
	}
	dtqlSerialize = origSerialize

	// 24. OrderBy field not in outputs
	orderBadPrev := preview.DTQL + "orderBy: [{field: NonExistent}]\n"
	if _, err := prepareExactAggregate([]byte(original), []byte(orderBadPrev), cols); err == nil {
		t.Fatal("expected orderby output error")
	}

	// executeExactAggregate error branches:
	// 25. accesspoliciesRun seam error
	origRun := accesspoliciesRun
	defer func() { accesspoliciesRun = origRun }()
	accesspoliciesRun = func(ctx context.Context, session dal.ReadSession, query dal.Query, options accesspolicies.Options) (accesspolicies.Result, error) {
		return accesspolicies.Result{}, errors.New("simulated policy run error")
	}
	if _, err := e.executeExactAggregate(ctx, aggregateTestDB{}, p, nil); err == nil {
		t.Fatal("expected policy run error")
	}
	accesspoliciesRun = origRun

	// 26. secured.Reader == nil
	accesspoliciesRun = func(ctx context.Context, session dal.ReadSession, query dal.Query, options accesspolicies.Options) (accesspolicies.Result, error) {
		return accesspolicies.Result{Reader: nil}, nil
	}
	if _, err := e.executeExactAggregate(ctx, aggregateTestDB{}, p, nil); err == nil {
		t.Fatal("expected reader == nil error")
	}
	accesspoliciesRun = origRun

	// 27. resultopsCapture seam error (snapshot capture)
	origCapture := resultopsCapture
	defer func() { resultopsCapture = origCapture }()
	resultopsCapture = func(columns []resultops.Column, rows []resultops.Row) (resultops.Snapshot, error) {
		return resultops.Snapshot{}, errors.New("simulated capture error")
	}
	reader := &aggregateTestReader{RecordsReader: aggregateRecords([]map[string]any{{"Category": "A", "Value": 1}})}
	if _, err := e.executeExactAggregate(ctx, aggregateTestDB{reader: reader}, p, nil); err == nil {
		t.Fatal("expected capture error")
	}
	resultopsCapture = origCapture

	// 28. captureExactAggregateRows: reader.Next error
	errReader := &aggregateTestReader{RecordsReader: &failingNextReader{err: errors.New("reader fail")}}
	if _, err := e.executeExactAggregate(ctx, aggregateTestDB{reader: errReader}, p, nil); err == nil {
		t.Fatal("expected reader fail error")
	}

	// 29. captureExactAggregateRows: !item.Exists()
	notExistRecord := record.NewRecord(record.NewKeyWithID("Item", 1))
	_ = notExistRecord.SetError(record.ErrRecordNotFound)
	notExistReader := &aggregateTestReader{RecordsReader: dal.NewRecordsReader([]record.Record{notExistRecord})}
	r, err := e.executeExactAggregate(ctx, aggregateTestDB{reader: notExistReader}, p, nil)
	if err != nil || len(r.Rows) != 0 {
		t.Fatalf("expected 0 rows for non-existing record: %v %+v", err, r)
	}

	// 30. captureExactAggregateRows: resultDataMap error (chan in record data)
	chanRecord := record.NewRecordWithData(record.NewKeyWithID("Item", 1), make(chan int))
	chanReader := &aggregateTestReader{RecordsReader: dal.NewRecordsReader([]record.Record{chanRecord})}
	if _, err := e.executeExactAggregate(ctx, aggregateTestDB{reader: chanReader}, p, nil); err == nil {
		t.Fatal("expected resultDataMap error")
	}

	// 31. captureExactAggregateRows: missing input cell
	missingColReader := &aggregateTestReader{RecordsReader: aggregateRecords([]map[string]any{{"Category": "A"}})} // missing Value
	pWithVal, _ := exactPlanForRows(t, "INTEGER", []queryops.AggregateTerm{{Column: "Value", Function: "SUM", Alias: "total"}})
	if _, err := e.executeExactAggregate(ctx, aggregateTestDB{reader: missingColReader}, pWithVal, nil); err == nil {
		t.Fatal("expected missing column error")
	}

	// 32. captureExactAggregateRows: detachResultCell error (NaN in cell)
	nanReader := &aggregateTestReader{RecordsReader: aggregateRecords([]map[string]any{{"Category": "A", "Value": math.NaN()}})}
	if _, err := e.executeExactAggregate(ctx, aggregateTestDB{reader: nanReader}, pWithVal, nil); err == nil {
		t.Fatal("expected detach error on NaN")
	}

	// 33. exactAggregateJSONValue helper:
	// uint64
	uval, err := exactAggregateJSONValue(uint64(12345))
	if err != nil || uval != json.Number("12345") {
		t.Fatalf("uint64 exactAggregateJSONValue: %v, %v", uval, err)
	}
	// *big.Int
	bival, err := exactAggregateJSONValue(big.NewInt(67890))
	if err != nil || bival != json.Number("67890") {
		t.Fatalf("*big.Int exactAggregateJSONValue: %v, %v", bival, err)
	}
	// *big.Rat non-terminating decimal (1/3)
	if _, err := exactAggregateJSONValue(big.NewRat(1, 3)); err == nil {
		t.Fatal("expected non-terminating decimal error")
	}
	// *big.Rat text == "-0"
	ratMinusZero := big.NewRat(0, 1)
	if v, err := exactAggregateJSONValue(ratMinusZero); err != nil || v != json.Number("0") {
		t.Fatalf("expected 0 for -0: %v, %v", v, err)
	}

	// 34. boundedAggregateNumber helper:
	// len(text) > 2048
	longNum := json.Number(strings.Repeat("9", 2049))
	if err := boundedAggregateNumber(longNum); err == nil {
		t.Fatal("expected budget error for long text")
	}
	// exponent out of range (-1001, 1001)
	if err := boundedAggregateNumber(json.Number("1e1001")); err == nil {
		t.Fatal("expected budget error for exponent 1001")
	}
	// digits > 1000
	if err := boundedAggregateNumber(json.Number(strings.Repeat("1", 1001))); err == nil {
		t.Fatal("expected budget error for 1001 digits")
	}
	// *big.Int with bitlen > 3322
	hugeBigInt := new(big.Int).Lsh(big.NewInt(1), 3323)
	if err := boundedAggregateNumber(hugeBigInt); err == nil {
		t.Fatal("expected budget error for huge *big.Int")
	}
	// *big.Rat with bitlen > 6644
	hugeBigRat := new(big.Rat).SetInt(new(big.Int).Lsh(big.NewInt(1), 6645))
	if err := boundedAggregateNumber(hugeBigRat); err == nil {
		t.Fatal("expected budget error for huge *big.Rat")
	}

	// 35. preflightAggregateOutput helper:
	snap, err := resultops.Capture([]resultops.Column{{Name: "Category", Type: "TEXT"}}, []resultops.Row{{Cells: []resultops.Cell{{Present: true, Value: "A"}}}})
	if err != nil {
		t.Fatal(err)
	}
	// snapshot.GroupBy error: p.keys naming non-existent column
	badKeysPlan := p
	badKeysPlan.keys = []string{"NonExistent"}
	if err := preflightAggregateOutput(snap, []resultops.Row{{Cells: []resultops.Cell{{Present: true, Value: "A"}}}}, badKeysPlan); err == nil {
		t.Fatal("expected group by error in preflight")
	}
	// json.Marshal error in preflight
	badCellRows := []resultops.Row{{Cells: []resultops.Cell{{Present: true, Value: make(chan int)}}}}
	if err := preflightAggregateOutput(snap, badCellRows, p); err == nil {
		t.Fatal("expected json.Marshal error in preflight")
	}
	// exponent < 0 in preflight
	negExpRows := []resultops.Row{{Cells: []resultops.Cell{{Present: true, Value: json.Number("1e-5")}}}}
	if err := preflightAggregateOutput(snap, negExpRows, p); err != nil {
		t.Fatalf("negative exponent preflight: %v", err)
	}
	// *big.Rat width check in preflight
	ratRows := []resultops.Row{{Cells: []resultops.Cell{{Present: true, Value: big.NewRat(1, 2)}}}}
	if err := preflightAggregateOutput(snap, ratRows, p); err != nil {
		t.Fatalf("big.Rat preflight: %v", err)
	}

	// 36. canonicalAggregateDecimal helper:
	// negative decimal text with trailing zeros that converts to scientific notation
	negDec, err := canonicalAggregateDecimal("-1." + strings.Repeat("0", 1005))
	if err != nil || !strings.HasPrefix(string(negDec.(json.Number)), "-") {
		t.Fatalf("negative decimal canonicalAggregateDecimal: %v, %v", negDec, err)
	}
	// decimal point text with trailing zeros
	dotDec, err := canonicalAggregateDecimal("123.45000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000" + strings.Repeat("0", 1005))
	if err != nil {
		t.Fatalf("dot decimal canonical: %v", err)
	}
	_ = dotDec
	// "0" / "0.0"
	zeroDec, err := canonicalAggregateDecimal(strings.Repeat("0", 1001))
	if err != nil || zeroDec != json.Number("0") {
		t.Fatalf("zero canonical: %v, %v", zeroDec, err)
	}
	// budget error in canonicalAggregateDecimal (1001 non-zero digits)
	if _, err := canonicalAggregateDecimal(strings.Repeat("9", 1001)); err == nil {
		t.Fatal("expected budget error in canonicalAggregateDecimal")
	}
}

type failingNextReader struct {
	err error
}

func (f *failingNextReader) Cursor() (string, error) { return "", nil }
func (f *failingNextReader) Close() error            { return nil }
func (f *failingNextReader) Next() (record.Record, error) {
	return nil, f.err
}

func newHTTPSnapshotFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	queriesDir := filepath.Join(dir, "queries")
	if err := os.MkdirAll(queriesDir, 0o755); err != nil {
		t.Fatalf("mkdir queries: %v", err)
	}
	def := `{"id":"countries","type":"HTTP","parameters":[{"id":"code","type":"string","isRequired":true}]}`
	if err := os.WriteFile(filepath.Join(queriesDir, "countries.query.json"), []byte(def), 0o600); err != nil {
		t.Fatalf("write query def: %v", err)
	}
	urlTemplate := "https://example.com/unreachable?code={code}"
	if err := os.WriteFile(filepath.Join(queriesDir, "countries.query.http"), []byte(urlTemplate), 0o600); err != nil {
		t.Fatalf("write url template: %v", err)
	}
	fixturesDir := filepath.Join(dir, "fixtures", "http")
	if err := os.MkdirAll(fixturesDir, 0o755); err != nil {
		t.Fatalf("mkdir fixtures: %v", err)
	}
	snapshot := `{"code":"CA","name":"Canada"}`
	if err := os.WriteFile(filepath.Join(fixturesDir, "countries.json"), []byte(snapshot), 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	return "http://" + dir
}

// Not parallel: mutates package seams.
func TestCoverage100_RemainingFinalBlocks(t *testing.T) {
	ctx := context.Background()
	e := NewExecutor(Session{Unrestricted: true})
	source := exactSQLite(t)
	validDoc := "from: {name: Item}\ncolumns: [{field: Id}]\n"
	origDTQL, err := dtql.Deserialize([]byte(exactOriginal))
	if err != nil {
		t.Fatal(err)
	}

	// 1. recursive.go:21 (ExactVariables error in RunRecursiveDTQL)
	if _, err := e.RunRecursiveDTQL(ctx, "sqlite://file", []byte(validDoc), map[string]any{"bad": int64(9007199254740993)}); err == nil {
		t.Fatal("expected ExactVariables error in RunRecursiveDTQL")
	}

	// 2. recursive.go:42 (dalExecuteRecursiveQuery error)
	origExec := dalExecuteRecursiveQuery
	defer func() { dalExecuteRecursiveQuery = origExec }()
	dalExecuteRecursiveQuery = func(ctx context.Context, executor dal.QueryExecutor, q dal.StructuredQuery) (dal.RecordsReader, error) {
		return nil, errors.New("simulated exec error")
	}
	if _, err := e.RunRecursiveDTQL(ctx, source, []byte(validDoc), nil); err == nil {
		t.Fatal("expected dalExecuteRecursiveQuery error")
	}
	dalExecuteRecursiveQuery = origExec

	// 3. recursive.go:80-81 (*dal.QuerySource in bindRecursiveParameters)
	qs := dal.NewQuerySource(origDTQL, "sub")
	qPtrQs := dal.NewQueryBuilder(dal.From(&qs)).SelectColumns(dal.Column{Expression: dal.NewFieldRef("Id", "")})
	if _, err := bindRecursiveParameters(qPtrQs, map[string]any{"minimum": 0}); err != nil {
		t.Fatalf("bindRecursiveParameters *dal.QuerySource: %v", err)
	}

	// 4. recursive.go:110 (*dal.QuerySource in recursiveAggregation)
	if recursiveAggregation(qPtrQs) {
		t.Fatal("expected false for query without agg")
	}
	var nilPtrQs *dal.QuerySource
	qNilPtrQs := dal.NewQueryBuilder(dal.From(nilPtrQs)).SelectColumns()
	if recursiveAggregation(qNilPtrQs) {
		t.Fatal("expected false for nil QuerySource")
	}

	// 5. recursive.go:165 (expression level > 64)
	var expr dal.Expression = dal.NewFieldRef("Id", "")
	for i := 0; i < 66; i++ {
		expr = dal.NewAggregate("COUNT", false, expr)
	}
	qDeep := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("Item", ""))).SelectColumns(dal.Column{Expression: expr})
	if err := ValidateRecursiveQuery(qDeep); err == nil {
		t.Fatal("expected level > 64 error in ValidateRecursiveQuery")
	}

	// 5b. recursive.go:170 (SUM/AVG in ValidateRecursiveQuery)
	qSum := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("Item", ""))).SelectColumns(dal.Column{Expression: dal.NewAggregate("SUM", false, dal.NewFieldRef("a", ""))})
	if err := ValidateRecursiveQuery(qSum); err == nil {
		t.Fatal("expected SUM error in ValidateRecursiveQuery")
	}

	// 6. recursive.go:173-174 (COUNT with binary expression)
	countBin := dal.NewAggregate("COUNT", false, dal.Binary(dal.NewFieldRef("a", ""), dal.Add, dal.NewFieldRef("b", "")))
	qCountBin := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("Item", ""))).SelectColumns(dal.Column{Expression: countBin})
	if err := ValidateRecursiveQuery(qCountBin); err == nil {
		t.Fatal("expected count(binary) error in ValidateRecursiveQuery")
	}

	// 7. recursive.go:189 (condition level > 64)
	var cond dal.Condition = dal.NewComparison(dal.NewFieldRef("Id", ""), dal.Equal, dal.NewConstant(1))
	for i := 0; i < 66; i++ {
		cond = dal.NewGroupCondition(dal.And, cond)
	}
	qDeepCond := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("Item", ""))).Where(cond).SelectColumns()
	if err := ValidateRecursiveQuery(qDeepCond); err == nil {
		t.Fatal("expected condition level > 64 error in ValidateRecursiveQuery")
	}

	// 8. recursive.go:237 (joins in ValidateRecursiveQuery)
	qWithJoin, err := dtql.Deserialize([]byte("from: {name: Item, joins: [{from: {name: Other}, on: [{left: {field: a, source: Item}, op: '==', right: {field: b, source: Other}}]}]}\ncolumns: [{field: a, source: Item}]\n"))
	if err != nil {
		t.Fatalf("deserialize join query: %v", err)
	}
	if err := ValidateRecursiveQuery(qWithJoin); err == nil {
		t.Fatal("expected join error in ValidateRecursiveQuery")
	}

	// 9. rows.go:58, 102, 142-146
	m := map[string]any{"a": 1}
	if res, err := resultDataMap(&m); err != nil || res["a"] != 1 {
		t.Fatalf("resultDataMap with pointer to map: %v, %v", res, err)
	}
	if v, err := detachResultCell(float32(1.23), 0); err != nil || v != float32(1.23) {
		t.Fatalf("detachResultCell float32: %v, %v", v, err)
	}
	type testCellStruct struct{ A int }
	if v, err := detachResultCell(testCellStruct{A: 1}, 0); err != nil || v == nil {
		t.Fatalf("detachResultCell struct: %v, %v", v, err)
	}

	// 10. exact_aggregate.go:96 (joins in prepareExactAggregate)
	origDeserialize := dtqlDeserialize
	defer func() { dtqlDeserialize = origDeserialize }()
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		return qWithJoin, nil
	}
	cols := []resultops.Column{{Name: "Category", Type: "TEXT"}}
	if _, err := prepareExactAggregate([]byte(exactOriginal), []byte(exactOriginal), cols); err == nil {
		t.Fatal("expected joins error in prepareExactAggregate")
	}

	// 11. exact_aggregate.go:106 (negative offset/limit in prepareExactAggregate)
	qNegOffset := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("Item", ""))).
		Offset(-1).
		SelectColumns(dal.Column{Expression: dal.NewFieldRef("Category", "")})
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		return qNegOffset, nil
	}
	if _, err := prepareExactAggregate([]byte(exactOriginal), []byte(exactOriginal), cols); err == nil {
		t.Fatal("expected negative offset error in prepareExactAggregate")
	}

	// 12. exact_aggregate.go:112 (ValidateRecursiveQuery failure in prepareExactAggregate)
	qValErr := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("Item", ""))).
		SelectColumns(dal.Column{Expression: dal.Binary(dal.NewFieldRef("a", ""), dal.Add, dal.NewFieldRef("b", ""))})
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		return qValErr, nil
	}
	if _, err := prepareExactAggregate([]byte(exactOriginal), []byte(exactOriginal), cols); err == nil {
		t.Fatal("expected ValidateRecursiveQuery error in prepareExactAggregate")
	}

	prevBase := exactPreview(t, exactOriginal, queryops.MatchingSource, []queryops.AggregateTerm{{Column: "Value", Function: "SUM", Alias: "total"}})

	// 13. exact_aggregate.go:136 (non-field in original columns)
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		if string(data) == "bad_orig" {
			return dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("Item", ""))).SelectColumns(dal.Column{Expression: dal.Star()}), nil
		}
		return origDeserialize(data)
	}
	if _, err := prepareExactAggregate([]byte("bad_orig"), []byte(prevBase.DTQL), exactColumns); err == nil {
		t.Fatal("expected non-field projection error in prepareExactAggregate")
	}
	dtqlDeserialize = origDeserialize

	// 14. exact_aggregate.go:151 (ambiguous original field projection)
	dupOrigDTQL := "from: {name: Item}\ncolumns: [{field: Category, as: A}, {field: Category, as: B}]\nlimit: 2\n"
	if _, err := prepareExactAggregate([]byte(dupOrigDTQL), []byte(prevBase.DTQL), []resultops.Column{{Name: "A", Type: "TEXT"}, {Name: "B", Type: "TEXT"}}); err == nil {
		t.Fatal("expected duplicate field expression error in prepareExactAggregate")
	}

	// 15. exact_aggregate.go:192: unsupported aggregate expression (args != 1)
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		if string(data) == "bad_args" {
			q, _ := origDeserialize([]byte(prevBase.DTQL))
			q2 := dal.NewQueryBuilder(dal.From(q.From().Base())).
				GroupBy(q.GroupBy()...).
				Limit(q.Limit()).
				SelectColumns(q.Columns()[0], dal.Column{Expression: dal.NewConstant(1), Alias: "total"})
			return q2, nil
		}
		return origDeserialize(data)
	}
	if _, err := prepareExactAggregate([]byte(exactOriginal), []byte("bad_args"), exactColumns); err == nil {
		t.Fatal("expected unsupported aggregate expression (args != 1) error")
	}
	dtqlDeserialize = origDeserialize

	// 16. exact_aggregate.go:196: DISTINCT aggregate
	distinctDTQL := strings.Replace(prevBase.DTQL, "function: sum", "function: sum\n      distinct: true", 1)
	if _, err := prepareExactAggregate([]byte(exactOriginal), []byte(distinctDTQL), exactColumns); err == nil {
		t.Fatal("expected distinct agg error")
	}

	// 17. exact_aggregate.go:200: COUNT(*) with alias != "count"
	badCountAliasDTQL := strings.Replace(prevBase.DTQL, "as: count", "as: not_count", 1)
	if _, err := prepareExactAggregate([]byte(exactOriginal), []byte(badCountAliasDTQL), exactColumns); err == nil {
		t.Fatal("expected COUNT(*) alias error")
	}

	// 18. exact_aggregate.go:208: aggregate with missing column
	missingColDTQL := strings.Replace(prevBase.DTQL, "field: Value", "field: Missing", 1)
	if _, err := prepareExactAggregate([]byte(exactOriginal), []byte(missingColDTQL), exactColumns); err == nil {
		t.Fatal("expected missing arg error")
	}

	// 19. exact_aggregate.go:213: unsupported aggregate "AVG"
	avgDTQL := strings.Replace(prevBase.DTQL, "function: sum", "function: avg", 1)
	if _, err := prepareExactAggregate([]byte(exactOriginal), []byte(avgDTQL), exactColumns); err == nil {
		t.Fatal("expected AVG unsupported aggregate error")
	}

	// 20. exact_aggregate.go:235: DeriveGroupAggregates error (colliding alias)
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		if string(data) == "invalid_for_derive" {
			return origDTQL, nil
		}
		return origDeserialize(data)
	}
	if _, err := prepareExactAggregate([]byte("invalid_for_derive"), []byte(prevBase.DTQL), exactColumns); err == nil {
		t.Fatal("expected DeriveGroupAggregates error")
	}
	dtqlDeserialize = origDeserialize

	// 21. exact_aggregate.go:245: > 1024 output columns
	terms1025 := make([]queryops.AggregateTerm, 1024)
	for i := 0; i < 1024; i++ {
		terms1025[i] = queryops.AggregateTerm{Column: "Value", Function: "SUM", Alias: "v_" + strconv.Itoa(i)}
	}
	expected1025, err := queryops.DeriveGroupAggregates(exactOriginal, []string{"Category"}, false, terms1025, queryops.MatchingSource)
	if err != nil {
		t.Fatalf("DeriveGroupAggregates 1025 terms: %v", err)
	}
	origSerialize := dtqlSerialize
	defer func() { dtqlSerialize = origSerialize }()
	dtqlSerialize = func(q dal.StructuredQuery) ([]byte, error) {
		return []byte(expected1025.DTQL), nil
	}
	if _, err := prepareExactAggregate([]byte(exactOriginal), []byte(expected1025.DTQL), exactColumns); err == nil {
		t.Fatal("expected > 1024 output columns budget error")
	}
	dtqlSerialize = origSerialize

	// 22. exact_aggregate.go:254: orderBy not in outputs
	origSerialize = dtqlSerialize
	defer func() { dtqlSerialize = origSerialize }()
	dtqlSerialize = func(q dal.StructuredQuery) ([]byte, error) {
		return []byte(prevBase.DTQL), nil
	}
	qOrderBad, err := dtql.Deserialize([]byte(prevBase.DTQL))
	if err != nil {
		t.Fatal(err)
	}
	qOrderBadWithSort := dal.NewQueryBuilder(dal.From(qOrderBad.From().Base())).
		GroupBy(qOrderBad.GroupBy()...).
		OrderBy(dal.Ascending(dal.NewFieldRef("NonExistent", ""))).
		Limit(qOrderBad.Limit()).
		SelectColumns(qOrderBad.Columns()...)
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		if string(data) == exactOriginal {
			return origDTQL, nil
		}
		return qOrderBadWithSort, nil
	}
	if _, err := prepareExactAggregate([]byte(exactOriginal), []byte("preview"), exactColumns); err == nil {
		t.Fatal("expected orderBy not in outputs error")
	}
	dtqlDeserialize = origDeserialize
	dtqlSerialize = origSerialize

	// Now executeExactAggregate branches:
	p, err := exactPlanForRows(t, "INTEGER", nil)
	if err != nil {
		t.Fatal(err)
	}

	// line 296: canceled context
	cancCtx, cancel := context.WithCancel(context.Background())
	origCapture := resultopsCapture
	defer func() { resultopsCapture = origCapture }()
	resultopsCapture = func(cols []resultops.Column, rows []resultops.Row) (resultops.Snapshot, error) {
		cancel()
		return origCapture(cols, rows)
	}
	rdr := &aggregateTestReader{RecordsReader: aggregateRecords([]map[string]any{{"Category": "A", "Value": 1}})}
	if _, err := e.executeExactAggregate(cancCtx, aggregateTestDB{reader: rdr}, p, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled error: %v", err)
	}
	resultopsCapture = origCapture

	// line 316: second resultopsCapture fails
	callCount := 0
	resultopsCapture = func(cols []resultops.Column, rows []resultops.Row) (resultops.Snapshot, error) {
		callCount++
		if callCount == 2 {
			return resultops.Snapshot{}, errors.New("second capture fail")
		}
		return origCapture(cols, rows)
	}
	rdr = &aggregateTestReader{RecordsReader: aggregateRecords([]map[string]any{{"Category": "A", "Value": 1}})}
	if _, err := e.executeExactAggregate(context.Background(), aggregateTestDB{reader: rdr}, p, nil); err == nil {
		t.Fatal("expected second capture fail error")
	}
	resultopsCapture = origCapture

	// line 322: outputSort seam fails
	origSort := outputSort
	defer func() { outputSort = origSort }()
	outputSort = func(s resultops.Snapshot, order []resultops.SortKey) ([]int, error) {
		return nil, errors.New("simulated sort error")
	}
	pOrder := p
	pOrder.order = []resultops.SortKey{{Column: "Category"}}
	rdr = &aggregateTestReader{RecordsReader: aggregateRecords([]map[string]any{{"Category": "A", "Value": 1}})}
	if _, err := e.executeExactAggregate(context.Background(), aggregateTestDB{reader: rdr}, pOrder, nil); err == nil {
		t.Fatal("expected sort error")
	}
	outputSort = origSort

	// line 327: offset > len(order)
	pOffset := p
	pOffset.query = dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("Item", ""))).Offset(100).Limit(10).SelectColumns()
	rdr = &aggregateTestReader{RecordsReader: aggregateRecords([]map[string]any{{"Category": "A", "Value": 1}})}
	resOffset, err := e.executeExactAggregate(context.Background(), aggregateTestDB{reader: rdr}, pOffset, nil)
	if err != nil || len(resOffset.Rows) != 0 {
		t.Fatalf("offset > len: %v %+v", err, resOffset)
	}

	// line 331: limit < end-start
	pLimit := p
	pLimit.query = dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("Item", ""))).Limit(1).SelectColumns()
	rdr = &aggregateTestReader{RecordsReader: aggregateRecords([]map[string]any{{"Category": "A", "Value": 1}, {"Category": "B", "Value": 2}})}
	resLimit, err := e.executeExactAggregate(context.Background(), aggregateTestDB{reader: rdr}, pLimit, nil)
	if err != nil || len(resLimit.Rows) != 1 {
		t.Fatalf("limit 1 with 2 groups: %v %+v", err, resLimit)
	}

	// line 342: context canceled in loop
	loopCtx, cancelLoop := context.WithCancel(context.Background())
	origSort = outputSort
	defer func() { outputSort = origSort }()
	outputSort = func(s resultops.Snapshot, order []resultops.SortKey) ([]int, error) {
		cancelLoop()
		return origSort(s, order)
	}
	pOrderCancel := p
	pOrderCancel.order = []resultops.SortKey{{Column: "Category"}}
	rdr = &aggregateTestReader{RecordsReader: aggregateRecords([]map[string]any{{"Category": "A", "Value": 1}})}
	if _, err := e.executeExactAggregate(loopCtx, aggregateTestDB{reader: rdr}, pOrderCancel, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected loop context canceled: %v", err)
	}
	outputSort = origSort

	// line 348: !cell.Present
	origCapture = resultopsCapture
	defer func() { resultopsCapture = origCapture }()
	resultopsCapture = func(cols []resultops.Column, rows []resultops.Row) (resultops.Snapshot, error) {
		if len(rows) > 0 && len(rows[0].Cells) > 0 {
			rows[0].Cells[0].Present = false
		}
		return origCapture(cols, rows)
	}
	rdr = &aggregateTestReader{RecordsReader: aggregateRecords([]map[string]any{{"Category": "A", "Value": 1}})}
	if _, err := e.executeExactAggregate(context.Background(), aggregateTestDB{reader: rdr}, p, nil); err == nil {
		t.Fatal("expected missing output cell error")
	}
	resultopsCapture = origCapture

	// line 352: group with big.Rat(1, 3) (exactAggregateJSONValue fails)
	pRat, _ := exactPlanForRows(t, "DECIMAL", []queryops.AggregateTerm{{Column: "Value", Function: "MIN", Alias: "minimum"}})
	origCapture = resultopsCapture
	defer func() { resultopsCapture = origCapture }()
	resultopsCapture = func(cols []resultops.Column, rows []resultops.Row) (resultops.Snapshot, error) {
		if len(rows) > 0 && len(rows[0].Cells) > 1 {
			rows[0].Cells[1].Value = big.NewRat(1, 3)
		}
		return origCapture(cols, rows)
	}
	rdr = &aggregateTestReader{RecordsReader: aggregateRecords([]map[string]any{{"Category": "A", "Value": json.Number("1")}})}
	if _, err := e.executeExactAggregate(context.Background(), aggregateTestDB{reader: rdr}, pRat, nil); err == nil {
		t.Fatal("expected exactAggregateJSONValue error for 1/3")
	}
	resultopsCapture = origCapture

	// line 358: exactAggregateJSONMarshal seam fails in executeExactAggregate
	origMarshal := exactAggregateJSONMarshal
	defer func() { exactAggregateJSONMarshal = origMarshal }()
	resultopsCapture = func(cols []resultops.Column, rows []resultops.Row) (resultops.Snapshot, error) {
		exactAggregateJSONMarshal = func(v any) ([]byte, error) {
			return nil, errors.New("simulated marshal error in output loop")
		}
		return origCapture(cols, rows)
	}
	rdr = &aggregateTestReader{RecordsReader: aggregateRecords([]map[string]any{{"Category": "A", "Value": 1}})}
	if _, err := e.executeExactAggregate(context.Background(), aggregateTestDB{reader: rdr}, p, nil); err == nil {
		t.Fatal("expected marshal error")
	}
	exactAggregateJSONMarshal = origMarshal
	resultopsCapture = origCapture

	// line 362: exactAggregateMaxBytes exceeded in executeExactAggregate
	origMaxBytes := exactAggregateMaxBytes
	defer func() { exactAggregateMaxBytes = origMaxBytes }()
	origSort = outputSort
	defer func() { outputSort = origSort }()
	outputSort = func(s resultops.Snapshot, order []resultops.SortKey) ([]int, error) {
		exactAggregateMaxBytes = 10
		return origSort(s, order)
	}
	pOrderMaxBytes := p
	pOrderMaxBytes.order = []resultops.SortKey{{Column: "Category"}}
	rdr = &aggregateTestReader{RecordsReader: aggregateRecords([]map[string]any{{"Category": "A", "Value": 1}})}
	if _, err := e.executeExactAggregate(context.Background(), aggregateTestDB{reader: rdr}, pOrderMaxBytes, nil); err == nil || !strings.Contains(err.Error(), "exact aggregation exceeds 16 MiB output budget") {
		t.Fatalf("expected output budget error, got: %v", err)
	}
	exactAggregateMaxBytes = origMaxBytes
	outputSort = origSort

	// line 378: captureExactAggregateRows ctx.Err()
	cancCtxCapture, cancelCapture := context.WithCancel(context.Background())
	cancelCapture()
	if _, err := captureExactAggregateRows(cancCtxCapture, nil, p); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled in captureExactAggregateRows: %v", err)
	}

	// line 379: reader.Next() fails
	failRdr := &aggregateTestReader{RecordsReader: &failingNextReader{err: errors.New("simulated next fail")}}
	if _, err := e.executeExactAggregate(context.Background(), aggregateTestDB{reader: failRdr}, p, nil); err == nil {
		t.Fatal("expected next fail error")
	}

	// line 402: exactAggregateJSONMarshal fails in captureExactAggregateRows
	exactAggregateJSONMarshal = func(v any) ([]byte, error) {
		return nil, errors.New("simulated marshal error in capture")
	}
	rdr = &aggregateTestReader{RecordsReader: aggregateRecords([]map[string]any{{"Category": "A", "Value": 1}})}
	if _, err := e.executeExactAggregate(context.Background(), aggregateTestDB{reader: rdr}, p, nil); err == nil {
		t.Fatal("expected marshal error in captureExactAggregateRows")
	}
	exactAggregateJSONMarshal = origMarshal

	// line 447: den2 (2^10001) in exactAggregateJSONValue
	den2 := new(big.Int).Lsh(big.NewInt(1), 10001)
	if _, err := exactAggregateJSONValue(new(big.Rat).SetFrac(big.NewInt(1), den2)); err == nil {
		t.Fatal("expected budget error for den2")
	}

	// line 458: den5 (5^10001) in exactAggregateJSONValue
	den5 := new(big.Int).Exp(big.NewInt(5), big.NewInt(10001), nil)
	if _, err := exactAggregateJSONValue(new(big.Rat).SetFrac(big.NewInt(1), den5)); err == nil {
		t.Fatal("expected budget error for den5")
	}

	// line 473: exactAggregateRatFloatString returns "-0"
	origFloatStr := exactAggregateRatFloatString
	defer func() { exactAggregateRatFloatString = origFloatStr }()
	exactAggregateRatFloatString = func(r *big.Rat, prec int) string {
		return "-0"
	}
	valNegZero, err := exactAggregateJSONValue(big.NewRat(1, 2))
	if err != nil || valNegZero != json.Number("0") {
		t.Fatalf("expected 0 for -0 FloatString: %v, %v", valNegZero, err)
	}
	exactAggregateRatFloatString = origFloatStr
}
