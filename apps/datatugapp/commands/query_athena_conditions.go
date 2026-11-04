package commands

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/dal-go/dalgo/dal"
)

var errAthenaInput = errors.New("invalid athena input")

var athenaDecimalPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

func compileAthenaCondition(ctx context.Context, table athenaTableConfig, raw json.RawMessage) (out dal.Condition, err error) {
	if ctx == nil {
		return nil, errAthenaInput
	}
	defer func() {
		if ctxErr := athenaFresh(ctx); ctxErr != nil {
			out = nil
			err = ctxErr
			return
		}
		if err != nil {
			out = nil
		}
	}()
	if err = athenaFresh(ctx); err != nil {
		return nil, err
	}
	if len(table.Columns) > athenaMaxColumns {
		return nil, errAthenaInput
	}
	columns := make(map[string]bool, len(table.Columns))
	for _, column := range table.Columns {
		if !athenaName(column.Name) || columns[column.Name] {
			return nil, errAthenaInput
		}
		columns[column.Name] = true
	}
	nodes, params := 0, 0
	return athenaCondition(ctx, table, columns, raw, 0, &nodes, &params)
}

func athenaCondition(ctx context.Context, table athenaTableConfig, columns map[string]bool, raw json.RawMessage, depth int, nodes, params *int) (out dal.Condition, err error) {
	if err = athenaFresh(ctx); err != nil {
		return nil, err
	}
	defer func() {
		if ctxErr := athenaFresh(ctx); ctxErr != nil {
			out = nil
			err = ctxErr
			return
		}
		if err != nil {
			out = nil
		}
	}()
	*nodes += 1
	if depth > 16 || *nodes > 64 {
		return nil, errAthenaInput
	}
	fields, fieldErr := snowflakeFields(raw, true, "and", "or", "field", "documentId", "operator", "value")
	if fieldErr != nil {
		return nil, errAthenaInput
	}
	for _, group := range []string{"and", "or"} {
		childrenRaw, exists := fields[group]
		if !exists {
			continue
		}
		if len(fields) != 1 {
			return nil, errAthenaInput
		}
		var children []json.RawMessage
		if err = json.Unmarshal(childrenRaw, &children); err != nil || len(children) == 0 || len(children) > 64 || *nodes+len(children) > 64 {
			return nil, errAthenaInput
		}
		conditions := make([]dal.Condition, 0, len(children))
		for _, child := range children {
			if err = athenaFresh(ctx); err != nil {
				return nil, err
			}
			condition, childErr := athenaCondition(ctx, table, columns, child, depth+1, nodes, params)
			if childErr != nil {
				return nil, childErr
			}
			conditions = append(conditions, condition)
		}
		operator := dal.Operator(dal.And)
		if group == "or" {
			operator = dal.Operator(dal.Or)
		}
		return dal.NewGroupCondition(operator, conditions...), nil
	}
	if len(fields) != 3 {
		return nil, errAthenaInput
	}
	fieldRaw, hasField := fields["field"]
	idRaw, hasID := fields["documentId"]
	if hasField == hasID {
		return nil, errAthenaInput
	}
	var field dal.FieldRef
	if hasField {
		var name string
		if err = json.Unmarshal(fieldRaw, &name); err != nil || !columns[name] {
			return nil, errAthenaInput
		}
		field = dal.Field(name)
	} else {
		var yes bool
		if err = json.Unmarshal(idRaw, &yes); err != nil || !yes || table.IDColumn == "" || !columns[table.IDColumn] {
			return nil, errAthenaInput
		}
		field = dal.DocumentID()
	}
	var operatorText string
	if err = json.Unmarshal(fields["operator"], &operatorText); err != nil {
		return nil, errAthenaInput
	}
	operator, ok := athenaOperator(operatorText)
	if !ok {
		return nil, errAthenaInput
	}
	// fields has exactly three keys and field/documentId and operator are already
	// accounted for, so the remaining key is value.
	valueRaw := fields["value"]
	if operator == dal.In || operator == dal.NotIn {
		var members []json.RawMessage
		if err = json.Unmarshal(valueRaw, &members); err != nil || len(members) == 0 || len(members) > 64 || *params+len(members) > 64 {
			return nil, errAthenaInput
		}
		values := make([]any, len(members))
		for i, member := range members {
			if err = athenaFresh(ctx); err != nil {
				return nil, err
			}
			value, scalarErr := athenaScalar(member)
			if scalarErr != nil || value == nil {
				return nil, errAthenaInput
			}
			values[i] = value
		}
		*params += len(members)
		return dal.Comparison{Left: field, Operator: operator, Right: dal.Array{Value: values}}, nil
	}
	value, scalarErr := athenaScalar(valueRaw)
	if scalarErr != nil {
		return nil, errAthenaInput
	}
	if value == nil {
		if operator != dal.Equal && operator != dal.Operator("!=") {
			return nil, errAthenaInput
		}
	} else {
		*params += 1
		if *params > 64 {
			return nil, errAthenaInput
		}
	}
	return dal.Comparison{Left: field, Operator: operator, Right: dal.Constant{Value: value}}, nil
}

func athenaOperator(text string) (dal.Operator, bool) {
	operators := map[string]dal.Operator{
		"=": dal.Equal, "!=": dal.Operator("!="), ">": dal.GreaterThen, ">=": dal.GreaterOrEqual,
		"<": dal.LessThen, "<=": dal.LessOrEqual, "in": dal.In, "notIn": dal.NotIn,
	}
	operator, ok := operators[text]
	return operator, ok
}

func athenaScalar(raw json.RawMessage) (any, error) {
	value, err := cosmosInputValue(raw)
	if err != nil {
		return nil, errAthenaInput
	}
	switch v := value.(type) {
	case nil, bool:
		return value, nil
	case string:
		if !utf8.ValidString(v) || len(v) > 4096 {
			return nil, errAthenaInput
		}
		parameter := "'" + strings.ReplaceAll(v, "'", "''") + "'"
		if utf8.RuneCountInString(parameter) > 1024 {
			return nil, errAthenaInput
		}
		return v, nil
	case map[string]any:
		if len(v) != 2 || v["kind"] != "number" {
			return nil, errAthenaInput
		}
		text, ok := v["value"].(string)
		if !ok {
			return nil, errAthenaInput
		}
		digits := len(text) - strings.Count(text, "-") - strings.Count(text, ".")
		if len(text) > 128 || digits > 38 || !athenaDecimalPattern.MatchString(text) {
			return nil, errAthenaInput
		}
		return json.Number(text), nil
	default:
		return nil, errAthenaInput
	}
}
