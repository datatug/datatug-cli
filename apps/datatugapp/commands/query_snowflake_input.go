package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/dal-go/dalgo/dal"
	snowsdk "github.com/dal-go/dalgo2snowflake"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

type admittedSnowflakeRead struct {
	operation, collection, id string
	ids                       []string
	query                     dal.Query
	limit                     int
	table                     snowsdk.Table
}
type snowflakeRequestJSON struct {
	Operation  string            `json:"operation"`
	Collection string            `json:"collection,omitempty"`
	ID         string            `json:"id,omitempty"`
	IDs        []string          `json:"ids,omitempty"`
	Projection []string          `json:"projection,omitempty"`
	Where      json.RawMessage   `json:"where,omitempty"`
	Order      []json.RawMessage `json:"order,omitempty"`
	Limit      *int              `json:"limit,omitempty"`
	Offset     *int              `json:"offset,omitempty"`
}

func snowflakeFields(raw []byte, allowValueNull bool, names ...string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := decodeDynamoDocument(raw, &fields); err != nil {
		return nil, err
	}
	if err := convexControlFields(fields, names...); err != nil {
		return nil, err
	}
	for key, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) && (!allowValueNull || key != "value") {
			return nil, errDynamoInput
		}
	}
	return fields, nil
}
func snowflakeName(name string) bool {
	return name != "" && len(name) <= 4096 && utf8.ValidString(name) && !strings.ContainsRune(name, 0)
}
func snowflakeID(id string) bool {
	return id != "" && len(id) <= dynamoDocumentBytes && utf8.ValidString(id)
}
func decodeSnowflakeConfig(raw []byte, c *snowflakeNativeConfig) error {
	fields, err := snowflakeFields(raw, false, "endpoint", "database", "schema", "warehouse", "role", "tables", "authKind", "tokenEnv", "caFile")
	if err != nil {
		return err
	}
	for _, key := range []string{"endpoint", "database", "schema", "tables", "authKind", "tokenEnv"} {
		if _, ok := fields[key]; !ok {
			return errDynamoInput
		}
	}
	var tableFields map[string]json.RawMessage
	if err = json.Unmarshal(fields["tables"], &tableFields); err != nil || len(tableFields) == 0 || len(tableFields) > 128 {
		return errDynamoInput
	}
	for alias, rawTable := range tableFields {
		if !snowflakeName(alias) || len(alias) > 128 {
			return errDynamoInput
		}
		f, err := snowflakeFields(rawTable, false, "name", "idColumn")
		if err != nil {
			return err
		}
		if len(f) != 2 {
			return errDynamoInput
		}
	}
	if err = decodeDynamoDocument(raw, c); err != nil {
		return err
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.Host == "" || u.Port() != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || !strings.HasSuffix(strings.ToLower(u.Hostname()), ".snowflakecomputing.com") {
		return errDynamoInput
	}
	if !snowflakeName(c.Database) || !snowflakeName(c.Schema) || (c.Warehouse != "" && !snowflakeName(c.Warehouse)) || (c.Role != "" && !snowflakeName(c.Role)) || !dbcopy.ValidEnvName(c.TokenEnv) || (c.AuthKind != "oauth" && c.AuthKind != "keypairJwt") {
		return errDynamoInput
	}
	for _, table := range c.Tables {
		if !snowflakeName(table.Name) || !snowflakeName(table.IDColumn) {
			return errDynamoInput
		}
	}
	return nil
}
func admitSnowflakeRequest(ctx context.Context, c snowflakeNativeConfig, raw []byte) (admittedSnowflakeRead, error) {
	var out admittedSnowflakeRead
	var fields map[string]json.RawMessage
	if err := decodeDynamoDocument(raw, &fields); err != nil {
		return out, err
	}
	var request snowflakeRequestJSON
	if err := decodeDynamoDocument(raw, &request); err != nil {
		return out, err
	}
	names := []string{"operation"}
	switch request.Operation {
	case "get", "exists", "testConnection":
		names = append(names, "collection", "id")
	case "getMany":
		names = append(names, "collection", "ids")
	case "query":
		names = append(names, "collection", "projection", "where", "order", "limit", "offset")
	case "metadata":
	default:
		return out, errDynamoInput
	}
	if _, err := snowflakeFields(raw, false, names...); err != nil {
		return out, err
	}
	out.operation = request.Operation
	if request.Operation == "metadata" {
		return out, dynamoFresh(ctx)
	}
	table, ok := c.Tables[request.Collection]
	if !ok {
		return out, errDynamoInput
	}
	out.collection = request.Collection
	out.table = table
	switch request.Operation {
	case "get", "exists", "testConnection":
		if !snowflakeID(request.ID) {
			return out, errDynamoInput
		}
		out.id = request.ID
	case "getMany":
		if request.IDs == nil || len(request.IDs) > 100 {
			return out, errDynamoInput
		}
		out.ids = append([]string{}, request.IDs...)
		for _, id := range out.ids {
			if !snowflakeID(id) {
				return out, errDynamoInput
			}
		}
	case "query":
		if request.Limit == nil || *request.Limit < 1 || *request.Limit > 1000 || request.Offset != nil && (*request.Offset < 0 || *request.Offset > 1000000000) || len(request.Projection) > 128 || len(request.Order) > 16 {
			return out, errDynamoInput
		}
		if _, present := fields["projection"]; present && len(request.Projection) == 0 {
			return out, errDynamoInput
		}
		if _, present := fields["order"]; present && len(request.Order) == 0 {
			return out, errDynamoInput
		}
		builder := dal.From(dal.NewRootCollectionRef(request.Collection, "")).NewQuery()
		if request.Where != nil {
			nodes := 0
			condition, err := snowflakeCondition(ctx, request.Where, 0, &nodes)
			if err != nil {
				return out, err
			}
			builder.Where(condition)
		}
		seen := make(map[string]bool)
		for _, rawOrder := range request.Order {
			f, err := snowflakeFields(rawOrder, false, "field", "direction")
			if err != nil || len(f) != 2 {
				return out, errDynamoInput
			}
			var order struct {
				Field     string `json:"field"`
				Direction string `json:"direction"`
			}
			if err = decodeDynamoDocument(rawOrder, &order); err != nil {
				return out, err
			}
			if !snowflakeName(order.Field) || seen[order.Field] || (order.Direction != "asc" && order.Direction != "desc") {
				return out, errDynamoInput
			}
			seen[order.Field] = true
			if order.Direction == "desc" {
				builder.OrderBy(dal.Descending(dal.Field(order.Field)))
			} else {
				builder.OrderBy(dal.Ascending(dal.Field(order.Field)))
			}
		}
		builder.Limit(*request.Limit)
		out.limit = *request.Limit
		if request.Offset != nil {
			builder.Offset(*request.Offset)
		}
		columns := make([]dal.Column, 0, len(request.Projection))
		clear(seen)
		for _, name := range request.Projection {
			if !snowflakeName(name) || seen[name] {
				return out, errDynamoInput
			}
			seen[name] = true
			columns = append(columns, dal.Column{Expression: dal.Field(name)})
		}
		out.query = builder.SelectColumns(columns...)
	}
	return out, dynamoFresh(ctx)
}
func snowflakeScalar(raw json.RawMessage) (any, error) {
	value, err := cosmosInputValue(raw)
	if err != nil {
		return nil, err
	}
	switch v := value.(type) {
	case nil, string, bool:
		return value, nil
	case map[string]any:
		if len(v) != 2 || v["kind"] != "number" {
			return nil, errDynamoInput
		}
		text, ok := v["value"].(string)
		if !ok || !influxNumber(text) {
			return nil, errDynamoInput
		}
		return json.Number(text), nil
	default:
		return nil, errDynamoInput
	}
}
func snowflakeCondition(ctx context.Context, raw json.RawMessage, depth int, nodes *int) (dal.Condition, error) {
	if err := dynamoFresh(ctx); err != nil {
		return nil, err
	}
	*nodes += 1
	if depth > 8 || *nodes > 64 {
		return nil, errDynamoInput
	}
	fields, err := snowflakeFields(raw, true, "and", "or", "field", "documentId", "operator", "value")
	if err != nil {
		return nil, err
	}
	for _, group := range []string{"and", "or"} {
		if childrenRaw, exists := fields[group]; exists {
			if len(fields) != 1 {
				return nil, errDynamoInput
			}
			var children []json.RawMessage
			if err = json.Unmarshal(childrenRaw, &children); err != nil || len(children) == 0 || len(children) > 64 {
				return nil, errDynamoInput
			}
			conditions := make([]dal.Condition, 0, len(children))
			for _, child := range children {
				condition, err := snowflakeCondition(ctx, child, depth+1, nodes)
				if err != nil {
					return nil, err
				}
				conditions = append(conditions, condition)
			}
			op := dal.Operator(dal.And)
			if group == "or" {
				op = dal.Operator(dal.Or)
			}
			return dal.NewGroupCondition(op, conditions...), nil
		}
	}
	if len(fields) != 3 {
		return nil, errDynamoInput
	}
	fieldRaw, hasField := fields["field"]
	idRaw, hasID := fields["documentId"]
	if hasField == hasID {
		return nil, errDynamoInput
	}
	var field dal.FieldRef
	if hasField {
		var name string
		if err = json.Unmarshal(fieldRaw, &name); err != nil || !snowflakeName(name) {
			return nil, errDynamoInput
		}
		field = dal.Field(name)
	} else {
		var yes bool
		if err = json.Unmarshal(idRaw, &yes); err != nil || !yes {
			return nil, errDynamoInput
		}
		field = dal.DocumentID()
	}
	var opText string
	if err = json.Unmarshal(fields["operator"], &opText); err != nil {
		return nil, errDynamoInput
	}
	operators := map[string]dal.Operator{"=": dal.Equal, "!=": dal.Operator("!="), ">": dal.GreaterThen, ">=": dal.GreaterOrEqual, "<": dal.LessThen, "<=": dal.LessOrEqual, "in": dal.In, "notIn": dal.NotIn}
	operator, ok := operators[opText]
	if !ok {
		return nil, errDynamoInput
	}
	// snowflakeFields admits only and/or/field/documentId/operator/value, group
	// keys returned above, and exactly one selector plus a decoded operator are
	// already present among the three fields, so the third is necessarily value.
	valueRaw := fields["value"]
	if operator == dal.In || operator == dal.NotIn {
		var members []json.RawMessage
		if err = json.Unmarshal(valueRaw, &members); err != nil || len(members) == 0 || len(members) > 64 {
			return nil, errDynamoInput
		}
		values := make([]any, len(members))
		for i, member := range members {
			values[i], err = snowflakeScalar(member)
			if err != nil || values[i] == nil {
				return nil, errDynamoInput
			}
		}
		return dal.Comparison{Left: field, Operator: operator, Right: dal.Array{Value: values}}, nil
	}
	value, err := snowflakeScalar(valueRaw)
	if err != nil {
		return nil, err
	}
	if value == nil && operator != dal.Equal && operator != dal.Operator("!=") {
		return nil, errDynamoInput
	}
	return dal.Comparison{Left: field, Operator: operator, Right: dal.Constant{Value: value}}, nil
}
