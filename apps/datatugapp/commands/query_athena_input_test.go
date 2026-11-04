package commands

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
)

func athenaInputKeyedConfig(t *testing.T) athenaNativeConfig {
	t.Helper()
	cfg := athenaOptionsTestConfig(t)
	table := cfg.Tables["items"]
	table.IDColumn = "id"
	cfg.Tables["items"] = table
	return cfg
}

func athenaInputItemsTable(t *testing.T, idColumn string) athenaTableConfig {
	t.Helper()
	return athenaTableConfig{
		Name:     "actual",
		IDColumn: idColumn,
		Columns: []athenaColumnConfig{
			{Name: "id", Type: "varchar"},
			{Name: "amount", Type: "decimal(38,10)"},
		},
	}
}

func athenaInputRequireAccepted(t *testing.T, cfg athenaNativeConfig, document string) admittedAthenaRead {
	t.Helper()
	admitted, err := admitAthenaRequest(context.Background(), cfg, []byte(document))
	if err != nil {
		t.Fatal("valid native request was refused")
	}
	return admitted
}

func athenaInputRequireRefused(t *testing.T, cfg athenaNativeConfig, document string) {
	t.Helper()
	admitted, err := admitAthenaRequest(context.Background(), cfg, []byte(document))
	if !errors.Is(err, errAthenaInput) {
		t.Fatal("invalid native request did not return the input error")
	}
	if !reflect.DeepEqual(admitted, admittedAthenaRead{}) {
		t.Fatal("invalid native request returned a non-zero admission")
	}
}

func athenaInputRequireConditionRefused(t *testing.T, table athenaTableConfig, raw string) {
	t.Helper()
	condition, err := compileAthenaCondition(context.Background(), table, []byte(raw))
	if !errors.Is(err, errAthenaInput) {
		t.Fatal("invalid native condition did not return the input error")
	}
	if condition != nil {
		t.Fatal("invalid native condition returned a non-nil condition")
	}
}

func athenaInputFieldRef(t *testing.T, value any) dal.FieldRef {
	t.Helper()
	field, ok := value.(dal.FieldRef)
	if !ok {
		t.Fatal("comparison left side is not a native field reference")
	}
	return field
}

func athenaInputTestStrings(prefix string, count int) string {
	parts := make([]string, count)
	for i := range parts {
		parts[i] = `"` + prefix + strconv.Itoa(i) + `"`
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func athenaInputTestNumberCondition(value string) string {
	return `{"field":"amount","operator":"=","value":{"kind":"number","value":"` + value + `"}}`
}

func TestAthenaInputStructuredQueryOwnsNativeAST(t *testing.T) {
	cfg := athenaInputKeyedConfig(t)
	raw := []byte(`{
		"operation":"query",
		"collection":"items",
		"projection":["amount","id"],
		"order":[{"field":"amount","direction":"desc"},{"field":"id","direction":"asc"}],
		"limit":7,
		"offset":11,
		"pageSize":2,
		"where":{"and":[
			{"field":"amount","operator":">=","value":{"kind":"number","value":"9007199254740993.0001"}},
			{"or":[
				{"documentId":true,"operator":"=","value":null},
				{"field":"id","operator":"in","value":["","null"]}
			]}
		]}
	}`)
	admitted, err := admitAthenaRequest(context.Background(), cfg, raw)
	if err != nil {
		t.Fatal("valid structured query was refused")
	}
	if admitted.operation != "query" || admitted.collection != "items" ||
		!reflect.DeepEqual(admitted.table, athenaInputItemsTable(t, "id")) ||
		admitted.limit != 7 || admitted.pageSize != 2 {
		t.Fatal("admitted operation, table mapping or limits changed")
	}
	query, ok := admitted.query.(dal.StructuredQuery)
	if !ok {
		t.Fatal("admitted query is not a native structured query")
	}
	if query.Limit() != 7 || query.Offset() != 11 {
		t.Fatal("native query limit or offset changed")
	}
	columns := query.Columns()
	if len(columns) != 2 ||
		!reflect.DeepEqual(columns[0], dal.Column{Expression: dal.Field("amount")}) ||
		!reflect.DeepEqual(columns[1], dal.Column{Expression: dal.Field("id")}) {
		t.Fatal("native query projection changed")
	}
	orders := query.OrderBy()
	if len(orders) != 2 ||
		!reflect.DeepEqual(orders[0], dal.Descending(dal.Field("amount"))) ||
		!reflect.DeepEqual(orders[1], dal.Ascending(dal.Field("id"))) {
		t.Fatal("native query order changed")
	}
	outer, ok := query.Where().(dal.GroupCondition)
	if !ok || outer.Operator() != dal.Operator(dal.And) || len(outer.Conditions()) != 2 {
		t.Fatal("native query where group changed")
	}
	amount, ok := outer.Conditions()[0].(dal.Comparison)
	if !ok || amount.Operator != dal.GreaterOrEqual {
		t.Fatal("native amount comparison changed")
	}
	amountLeft := athenaInputFieldRef(t, amount.Left)
	if amountLeft.Name() != "amount" || amountLeft.IsID() {
		t.Fatal("native amount field reference changed")
	}
	amountConstant, ok := amount.Right.(dal.Constant)
	if !ok {
		t.Fatal("native amount value is not constant")
	}
	number, ok := amountConstant.Value.(json.Number)
	if !ok || number.String() != "9007199254740993.0001" {
		t.Fatal("native amount number lexical value changed")
	}
	nested, ok := outer.Conditions()[1].(dal.GroupCondition)
	if !ok || nested.Operator() != dal.Operator(dal.Or) || len(nested.Conditions()) != 2 {
		t.Fatal("native nested or group changed")
	}
	nullComparison, ok := nested.Conditions()[0].(dal.Comparison)
	if !ok || nullComparison.Operator != dal.Equal {
		t.Fatal("native document id null comparison changed")
	}
	nullLeft := athenaInputFieldRef(t, nullComparison.Left)
	if !nullLeft.IsID() {
		t.Fatal("native document id reference changed")
	}
	nullConstant, ok := nullComparison.Right.(dal.Constant)
	if !ok || nullConstant.Value != nil {
		t.Fatal("native SQL null changed")
	}
	inComparison, ok := nested.Conditions()[1].(dal.Comparison)
	if !ok || inComparison.Operator != dal.In {
		t.Fatal("native in comparison changed")
	}
	inLeft := athenaInputFieldRef(t, inComparison.Left)
	if inLeft.Name() != "id" || inLeft.IsID() {
		t.Fatal("native id in reference changed")
	}
	inArray, ok := inComparison.Right.(dal.Array)
	if !ok || !reflect.DeepEqual(inArray.Value, []any{"", "null"}) {
		t.Fatal("native in array values changed")
	}

	table := cfg.Tables["items"]
	table.Columns[0].Name, table.Columns[0].Type = "mutated", "int"
	cfg.Tables["items"] = table
	delete(cfg.Tables, "items")
	for i := range raw {
		raw[i] = 'x'
	}
	if admitted.collection != "items" || !reflect.DeepEqual(admitted.table, athenaInputItemsTable(t, "id")) {
		t.Fatal("admission changed after mutating input configuration")
	}
	columns = query.Columns()
	orders = query.OrderBy()
	if len(columns) != 2 ||
		!reflect.DeepEqual(columns[0], dal.Column{Expression: dal.Field("amount")}) ||
		len(orders) != 2 || !reflect.DeepEqual(orders[0], dal.Descending(dal.Field("amount"))) {
		t.Fatal("native query changed after mutating raw input")
	}
	retained, ok := query.Where().(dal.GroupCondition)
	if !ok || len(retained.Conditions()) != 2 {
		t.Fatal("native where changed after mutating raw input")
	}
	retainedAmount, ok := retained.Conditions()[0].(dal.Comparison)
	if !ok {
		t.Fatal("native amount comparison changed after mutating raw input")
	}
	retainedConstant, ok := retainedAmount.Right.(dal.Constant)
	if !ok {
		t.Fatal("native amount value changed after mutating raw input")
	}
	retainedNumber, ok := retainedConstant.Value.(json.Number)
	if !ok || retainedNumber.String() != "9007199254740993.0001" {
		t.Fatal("native amount number changed after mutating raw input")
	}

	minimal := athenaInputRequireAccepted(t, athenaOptionsTestConfig(t), `{"operation":"query","collection":"items","limit":1}`)
	if minimal.limit != 1 || minimal.pageSize != 100 {
		t.Fatal("omitted query bounds did not use the documented defaults")
	}
	minimalQuery, ok := minimal.query.(dal.StructuredQuery)
	if !ok || minimalQuery.Limit() != 1 || minimalQuery.Offset() != 0 ||
		len(minimalQuery.Columns()) != 0 || len(minimalQuery.OrderBy()) != 0 || minimalQuery.Where() != nil {
		t.Fatal("minimal native query gained projection, order, offset or where")
	}
	sized := athenaInputRequireAccepted(t, athenaOptionsTestConfig(t), `{"operation":"query","collection":"items","limit":1,"pageSize":1000}`)
	if sized.limit != 1 || sized.pageSize != 1000 {
		t.Fatal("explicit page size above the limit was refused")
	}
}

func TestAthenaInputOperationShapesAndOwnedBatch(t *testing.T) {
	cfg := athenaInputKeyedConfig(t)
	metadata := athenaInputRequireAccepted(t, cfg, `{"operation":"metadata"}`)
	if !reflect.DeepEqual(metadata, admittedAthenaRead{operation: "metadata"}) {
		t.Fatal("metadata admission retained collection or query state")
	}
	athenaInputRequireRefused(t, cfg, `{"operation":"delete"}`)

	wantTable := athenaInputItemsTable(t, "id")
	for _, operation := range []string{"get", "exists", "testConnection"} {
		document := `{"operation":"` + operation + `","collection":"items","id":"record-1"}`
		admitted := athenaInputRequireAccepted(t, cfg, document)
		if admitted.operation != operation || admitted.collection != "items" || admitted.id != "record-1" ||
			!reflect.DeepEqual(admitted.table, wantTable) {
			t.Fatal("keyed operation admission changed")
		}
	}

	empty := athenaInputRequireAccepted(t, cfg, `{"operation":"getMany","collection":"items","ids":[]}`)
	if empty.operation != "getMany" || empty.collection != "items" ||
		!reflect.DeepEqual(empty.ids, []string{}) || !reflect.DeepEqual(empty.table, wantTable) {
		t.Fatal("empty getMany batch admission changed")
	}

	ownedCfg := athenaInputKeyedConfig(t)
	ownedRaw := []byte(`{"operation":"getMany","collection":"items","ids":["b","a","a"]}`)
	owned, err := admitAthenaRequest(context.Background(), ownedCfg, ownedRaw)
	if err != nil || !reflect.DeepEqual(owned.ids, []string{"b", "a", "a"}) || !reflect.DeepEqual(owned.table, wantTable) {
		t.Fatal("duplicate getMany ids changed order")
	}
	ownedTable := ownedCfg.Tables["items"]
	ownedTable.Columns[0].Name = "mutated"
	ownedCfg.Tables["items"] = ownedTable
	delete(ownedCfg.Tables, "items")
	for i := range ownedRaw {
		ownedRaw[i] = 'x'
	}
	if !reflect.DeepEqual(owned.ids, []string{"b", "a", "a"}) || !reflect.DeepEqual(owned.table, wantTable) {
		t.Fatal("getMany admission changed after mutating input configuration")
	}

	ids := make([]string, 100)
	parts := make([]string, len(ids))
	for i := range ids {
		ids[i] = "record-" + strconv.Itoa(i)
		parts[i] = `"` + ids[i] + `"`
	}
	hundred := athenaInputRequireAccepted(t, athenaInputKeyedConfig(t),
		`{"operation":"getMany","collection":"items","ids":[`+strings.Join(parts, ",")+`]}`)
	if !reflect.DeepEqual(hundred.ids, ids) {
		t.Fatal("100 id getMany batch changed")
	}
	tooMany := append(append([]string{}, parts...), `"record-100"`)
	athenaInputRequireRefused(t, athenaInputKeyedConfig(t),
		`{"operation":"getMany","collection":"items","ids":[`+strings.Join(tooMany, ",")+`]}`)

	keyed := athenaInputKeyedConfig(t)
	unkeyed := athenaOptionsTestConfig(t)
	for _, test := range []struct {
		name     string
		document string
		cfg      athenaNativeConfig
	}{
		{"missing_collection", `{"operation":"get","id":"a"}`, keyed},
		{"missing_id", `{"operation":"get","collection":"items"}`, keyed},
		{"missing_ids", `{"operation":"getMany","collection":"items"}`, keyed},
		{"missing_limit", `{"operation":"query","collection":"items"}`, keyed},
		{"explicit_null", `{"operation":"query","collection":"items","limit":null}`, keyed},
		{"keyed_id_and_ids", `{"operation":"get","collection":"items","id":"a","ids":["b"]}`, keyed},
		{"query_id_field", `{"operation":"query","collection":"items","limit":1,"id":"a"}`, keyed},
		{"metadata_collection", `{"operation":"metadata","collection":"items"}`, keyed},
		{"unmapped_table", `{"operation":"query","collection":"missing","limit":1}`, keyed},
		{"empty_id", `{"operation":"get","collection":"items","id":""}`, keyed},
		{"missing_id_column", `{"operation":"get","collection":"items","id":"a"}`, unkeyed},
		{"limit_zero", `{"operation":"query","collection":"items","limit":0}`, keyed},
		{"limit_1001", `{"operation":"query","collection":"items","limit":1001}`, keyed},
		{"offset_negative", `{"operation":"query","collection":"items","limit":1,"offset":-1}`, keyed},
		{"offset_100001", `{"operation":"query","collection":"items","limit":1,"offset":100001}`, keyed},
		{"page_size_zero", `{"operation":"query","collection":"items","limit":1,"pageSize":0}`, keyed},
		{"page_size_1001", `{"operation":"query","collection":"items","limit":1,"pageSize":1001}`, keyed},
		{"unknown_field", `{"operation":"query","collection":"items","limit":1,"unknown":true}`, keyed},
		{"duplicate_field", `{"operation":"query","operation":"query","collection":"items","limit":1}`, keyed},
		{"unknown_projection", `{"operation":"query","collection":"items","limit":1,"projection":["nope"]}`, keyed},
		{"duplicate_projection", `{"operation":"query","collection":"items","limit":1,"projection":["amount","amount"]}`, keyed},
		{"empty_projection", `{"operation":"query","collection":"items","limit":1,"projection":[]}`, keyed},
		{"unknown_order", `{"operation":"query","collection":"items","limit":1,"order":[{"field":"nope","direction":"asc"}]}`, keyed},
		{"duplicate_order", `{"operation":"query","collection":"items","limit":1,"order":[{"field":"amount","direction":"asc"},{"field":"amount","direction":"desc"}]}`, keyed},
		{"empty_order", `{"operation":"query","collection":"items","limit":1,"order":[]}`, keyed},
		{"invalid_direction", `{"operation":"query","collection":"items","limit":1,"order":[{"field":"amount","direction":"sideways"}]}`, keyed},
	} {
		t.Run(test.name, func(t *testing.T) {
			athenaInputRequireRefused(t, test.cfg, test.document)
		})
	}

	for _, document := range []string{
		`{"operation":"query","collection":"items","limit":1}`,
		`{"operation":"query","collection":"items","limit":1000}`,
		`{"operation":"query","collection":"items","limit":1,"offset":100000}`,
	} {
		athenaInputRequireAccepted(t, keyed, document)
	}
}

func TestAthenaInputConditionBoundsAndCallerPriority(t *testing.T) {
	cfg := athenaInputKeyedConfig(t)
	table := cfg.Tables["items"]
	background := context.Background()

	children := []string{
		`{"field":"amount","operator":"in","value":` + athenaInputTestStrings("a", 32) + `}`,
		`{"field":"amount","operator":"in","value":` + athenaInputTestStrings("b", 32) + `}`,
		`{"field":"amount","operator":"=","value":null}`,
	}
	condition, err := compileAthenaCondition(background, table, []byte(`{"and":[`+strings.Join(children, ",")+`]}`))
	if err != nil || condition == nil {
		t.Fatal("64 shared parameters with a SQL null were refused")
	}
	group, ok := condition.(dal.GroupCondition)
	if !ok || group.Operator() != dal.Operator(dal.And) || len(group.Conditions()) != 3 {
		t.Fatal("shared parameter condition group changed")
	}
	for i := 0; i < 2; i++ {
		comparison, ok := group.Conditions()[i].(dal.Comparison)
		if !ok || comparison.Operator != dal.In {
			t.Fatal("shared parameter in comparison changed")
		}
		array, ok := comparison.Right.(dal.Array)
		if !ok {
			t.Fatal("shared parameter in array changed")
		}
		values, ok := array.Value.([]any)
		if !ok || len(values) != 32 {
			t.Fatal("shared parameter in array changed")
		}
	}
	nullComparison, ok := group.Conditions()[2].(dal.Comparison)
	if !ok || nullComparison.Operator != dal.Equal {
		t.Fatal("shared parameter SQL null comparison changed")
	}
	nullConstant, ok := nullComparison.Right.(dal.Constant)
	if !ok || nullConstant.Value != nil {
		t.Fatal("shared parameter SQL null changed")
	}
	negativeChildren := append(append([]string{}, children...), `{"field":"id","operator":"=","value":"x"}`)
	negative, negativeErr := compileAthenaCondition(background, table,
		[]byte(`{"and":[`+strings.Join(negativeChildren, ",")+`]}`))
	if !errors.Is(negativeErr, errAthenaInput) || negative != nil {
		t.Fatal("65 shared parameters were accepted")
	}

	thirtyEight := strings.Repeat("9", 38)
	accepted, err := compileAthenaCondition(background, table, []byte(athenaInputTestNumberCondition(thirtyEight)))
	if err != nil {
		t.Fatal("38 digit number was refused")
	}
	acceptedComparison, ok := accepted.(dal.Comparison)
	if !ok {
		t.Fatal("38 digit number is not a comparison")
	}
	acceptedConstant, ok := acceptedComparison.Right.(dal.Constant)
	if !ok {
		t.Fatal("38 digit number is not constant")
	}
	acceptedNumber, ok := acceptedConstant.Value.(json.Number)
	if !ok || acceptedNumber.String() != thirtyEight {
		t.Fatal("38 digit number lexical value changed")
	}
	for _, value := range []string{strings.Repeat("9", 39), "1e5"} {
		athenaInputRequireConditionRefused(t, table, athenaInputTestNumberCondition(value))
	}
	athenaInputRequireConditionRefused(t, table, `{"field":"amount","operator":"=","value":123}`)

	for _, test := range []struct {
		wire string
		want dal.Operator
	}{
		{"=", dal.Equal},
		{"!=", dal.Operator("!=")},
	} {
		raw := `{"field":"amount","operator":"` + test.wire + `","value":null}`
		condition, err := compileAthenaCondition(background, table, []byte(raw))
		if err != nil || condition == nil {
			t.Fatal("SQL null equality or inequality was refused")
		}
		comparison, ok := condition.(dal.Comparison)
		if !ok || comparison.Operator != test.want {
			t.Fatal("SQL null comparison changed")
		}
		constant, ok := comparison.Right.(dal.Constant)
		if !ok || constant.Value != nil {
			t.Fatal("SQL null value changed")
		}
	}
	for _, raw := range []string{
		`{"field":"amount","operator":">","value":null}`,
		`{"field":"amount","operator":"in","value":[null]}`,
	} {
		athenaInputRequireConditionRefused(t, table, raw)
	}

	apostrophes := strings.Repeat("'", 511)
	condition, err = compileAthenaCondition(background, table,
		[]byte(`{"field":"amount","operator":"=","value":"`+apostrophes+`"}`))
	if err != nil || condition == nil {
		t.Fatal("1024 character quoted string was refused")
	}
	comparison, ok := condition.(dal.Comparison)
	if !ok {
		t.Fatal("quoted string is not a comparison")
	}
	constant, ok := comparison.Right.(dal.Constant)
	if !ok || constant.Value != apostrophes {
		t.Fatal("quoted string value changed")
	}
	athenaInputRequireConditionRefused(t, table,
		`{"field":"amount","operator":"=","value":"`+strings.Repeat("'", 512)+`"}`)

	canceled, cancel := context.WithCancelCause(context.Background())
	defer func() { cancel(nil) }()
	cancel(errors.New("PRIVATE_CAUSE"))
	expired, stop := context.WithDeadlineCause(context.Background(), time.Now().Add(-time.Second), errors.New("PRIVATE_DEADLINE_CAUSE"))
	defer stop()
	var dynamicNil context.Context
	contexts := []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"canceled", canceled, context.Canceled},
		{"wrapped_canceled", athenaCancelContext{Context: canceled}, context.Canceled},
		{"expired", expired, context.DeadlineExceeded},
		{"wrapped_expired", athenaCancelContext{Context: expired}, context.DeadlineExceeded},
		{"nil", dynamicNil, errAthenaInput},
	}
	requests := []struct {
		name     string
		document string
	}{
		{"request_valid", `{"operation":"query","collection":"items","limit":1}`},
		{"request_malformed", `{`},
	}
	conditions := []struct {
		name string
		raw  string
	}{
		{"condition_valid", `{"field":"amount","operator":">=","value":{"kind":"number","value":"1"}}`},
		{"condition_malformed", `{`},
	}
	for _, caller := range contexts {
		for _, request := range requests {
			t.Run(caller.name+"/"+request.name, func(t *testing.T) {
				admitted, err := admitAthenaRequest(caller.ctx, cfg, []byte(request.document))
				if err != caller.want {
					t.Fatal("request caller context did not take priority")
				}
				if !reflect.DeepEqual(admitted, admittedAthenaRead{}) {
					t.Fatal("request caller context returned a non-zero admission")
				}
			})
		}
		for _, condition := range conditions {
			t.Run(caller.name+"/"+condition.name, func(t *testing.T) {
				got, err := compileAthenaCondition(caller.ctx, table, []byte(condition.raw))
				if err != caller.want {
					t.Fatal("condition caller context did not take priority")
				}
				if got != nil {
					t.Fatal("condition caller context returned a non-nil condition")
				}
			})
		}
	}
}
