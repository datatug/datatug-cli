package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/dal-go/dalgo/dal"
	dbrsdk "github.com/dal-go/dalgo2databricks"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

type admittedDatabricksRead struct {
	operation, collection, id string
	ids                       []string
	query                     dal.Query
	limit                     int
	table                     dbrsdk.Table
}

type databricksRequestJSON struct {
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

func databricksFields(raw []byte, allowValueNull bool, names ...string) (map[string]json.RawMessage, error) {
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

func databricksName(name string) bool {
	return name != "" && len(name) <= 4096 && utf8.ValidString(name) && !strings.ContainsRune(name, 0)
}

func databricksID(id string) bool {
	return id != "" && len(id) <= dynamoDocumentBytes && utf8.ValidString(id)
}

func isDatabricksHost(hostname string) bool {
	lower := strings.ToLower(hostname)
	if strings.HasSuffix(lower, ".azuredatabricks.net") ||
		strings.HasSuffix(lower, ".cloud.databricks.com") ||
		strings.HasSuffix(lower, ".gcp.databricks.com") ||
		strings.HasSuffix(lower, ".corp.internal") ||
		lower == "localhost" ||
		lower == "127.0.0.1" {
		return true
	}
	return false
}

func decodeDatabricksConfig(raw []byte, c *databricksNativeConfig) error {
	fields, err := databricksFields(raw, false, "endpoint", "warehouseId", "catalog", "schema", "tables", "authKind", "tokenEnv", "caFile")
	if err != nil {
		return err
	}
	for _, key := range []string{"endpoint", "warehouseId", "catalog", "schema", "tables", "authKind", "tokenEnv"} {
		if _, ok := fields[key]; !ok {
			return errDynamoInput
		}
	}
	var tableFields map[string]json.RawMessage
	if err = json.Unmarshal(fields["tables"], &tableFields); err != nil || len(tableFields) == 0 || len(tableFields) > 128 {
		return errDynamoInput
	}
	for alias, rawTable := range tableFields {
		if !databricksName(alias) || len(alias) > 128 {
			return errDynamoInput
		}
		f, err := databricksFields(rawTable, false, "name", "idColumn")
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
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Opaque != "" || u.User != nil || u.Host == "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || !isDatabricksHost(u.Hostname()) {
		return errDynamoInput
	}
	if !databricksName(c.WarehouseID) || !databricksName(c.Catalog) || !databricksName(c.Schema) || !dbcopy.ValidEnvName(c.TokenEnv) || (c.AuthKind != "pat" && c.AuthKind != "oauthM2M" && c.AuthKind != "bearer") {
		return errDynamoInput
	}
	for _, table := range c.Tables {
		if !databricksName(table.Name) || !databricksName(table.IDColumn) {
			return errDynamoInput
		}
	}
	return nil
}

func admitDatabricksRequest(ctx context.Context, c databricksNativeConfig, raw []byte) (admittedDatabricksRead, error) {
	var out admittedDatabricksRead
	var fields map[string]json.RawMessage
	if err := decodeDynamoDocument(raw, &fields); err != nil {
		return out, err
	}
	var request databricksRequestJSON
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
	if _, err := databricksFields(raw, false, names...); err != nil {
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
		if !databricksID(request.ID) {
			return out, errDynamoInput
		}
		out.id = request.ID
	case "getMany":
		if request.IDs == nil || len(request.IDs) > 100 {
			return out, errDynamoInput
		}
		out.ids = append([]string{}, request.IDs...)
		for _, id := range out.ids {
			if !databricksID(id) {
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
			condition, err := databricksCondition(ctx, request.Where, 0, &nodes)
			if err != nil {
				return out, err
			}
			builder.Where(condition)
		}
		seen := make(map[string]bool)
		for _, rawOrder := range request.Order {
			f, err := databricksFields(rawOrder, false, "field", "direction")
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
			if !databricksName(order.Field) || seen[order.Field] || (order.Direction != "asc" && order.Direction != "desc") {
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
			if !databricksName(name) || seen[name] {
				return out, errDynamoInput
			}
			seen[name] = true
			columns = append(columns, dal.Column{Expression: dal.Field(name)})
		}
		out.query = builder.SelectColumns(columns...)
	}
	return out, dynamoFresh(ctx)
}

func databricksScalar(raw json.RawMessage) (any, error) {
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

func databricksCondition(ctx context.Context, raw json.RawMessage, depth int, nodes *int) (dal.Condition, error) {
	if err := dynamoFresh(ctx); err != nil {
		return nil, err
	}
	*nodes += 1
	if depth > 8 || *nodes > 64 {
		return nil, errDynamoInput
	}
	fields, err := databricksFields(raw, true, "and", "or", "field", "documentId", "operator", "value")
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
				condition, err := databricksCondition(ctx, child, depth+1, nodes)
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
		if err = json.Unmarshal(fieldRaw, &name); err != nil || !databricksName(name) {
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
	operators := map[string]dal.Operator{
		"=":     dal.Equal,
		"!=":    dal.Operator("!="),
		">":     dal.GreaterThen,
		">=":    dal.GreaterOrEqual,
		"<":     dal.LessThen,
		"<=":    dal.LessOrEqual,
		"in":    dal.In,
		"notIn": dal.NotIn,
	}
	operator, ok := operators[opText]
	if !ok {
		return nil, errDynamoInput
	}
	valueRaw := fields["value"]
	if operator == dal.In || operator == dal.NotIn {
		var members []json.RawMessage
		if err = json.Unmarshal(valueRaw, &members); err != nil || len(members) == 0 || len(members) > 64 {
			return nil, errDynamoInput
		}
		values := make([]any, len(members))
		for i, member := range members {
			values[i], err = databricksScalar(member)
			if err != nil || values[i] == nil {
				return nil, errDynamoInput
			}
		}
		return dal.Comparison{Left: field, Operator: operator, Right: dal.Array{Value: values}}, nil
	}
	value, err := databricksScalar(valueRaw)
	if err != nil {
		return nil, err
	}
	if value == nil && operator != dal.Equal && operator != dal.Operator("!=") {
		return nil, errDynamoInput
	}
	return dal.Comparison{Left: field, Operator: operator, Right: dal.Constant{Value: value}}, nil
}
