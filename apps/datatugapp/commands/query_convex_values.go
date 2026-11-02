package commands

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	convexsdk "github.com/dal-go/dalgo2convex"
)

// Control records have exact field names; native data objects are separately
// decoded by the public codec, preserving valid __proto__/constructor fields.
func convexControlFields(fields map[string]json.RawMessage, names ...string) error {
	if fields == nil {
		return errDynamoInput
	}
	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		allowed[name] = true
	}
	for name := range fields {
		if !allowed[name] {
			return errDynamoInput
		}
	}
	return nil
}
func decodeConvexConfig(raw []byte, c *convexNativeConfig) error {
	var top map[string]json.RawMessage
	if err := decodeDynamoDocument(raw, &top); err != nil {
		return err
	}
	if err := convexControlFields(top, "endpoint", "tokenEnv", "anonymous", "caFile", "collections", "queries"); err != nil {
		return err
	}
	for _, item := range []struct {
		name   string
		fields []string
	}{{"collections", []string{"getFunction", "idArgument", "idField", "staticArgs", "fields"}}, {"queries", []string{"function", "collection", "argumentNames", "staticArgs"}}} {
		var aliases map[string]json.RawMessage
		if value, ok := top[item.name]; ok {
			if json.Unmarshal(value, &aliases) != nil {
				return errDynamoInput
			}
			for _, value := range aliases {
				var fields map[string]json.RawMessage
				if json.Unmarshal(value, &fields) != nil {
					return errDynamoInput
				}
				if err := convexControlFields(fields, item.fields...); err != nil {
					return err
				}
			}
		}
	}
	return decodeDynamoDocument(raw, c)
}
func decodeConvexRequest(raw []byte, r *convexNativeRequest) error {
	var top map[string]json.RawMessage
	if err := decodeDynamoDocument(raw, &top); err != nil {
		return err
	}
	if err := convexControlFields(top, "operation", "collection", "id", "ids", "query", "args", "pageSize", "pages"); err != nil {
		return err
	}
	return decodeDynamoDocument(raw, r)
}

func convexInputObject(ctx context.Context, raw json.RawMessage) (convexsdk.Object, error) {
	if err := dynamoFresh(ctx); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return convexsdk.Object{}, nil
	}
	value, err := convexsdk.DecodeValue(raw)
	if err != nil {
		return nil, err
	}
	object, ok := value.Object()
	if !ok {
		return nil, errDynamoInput
	}
	if err := dynamoFresh(ctx); err != nil {
		return nil, err
	}
	return object, nil
}

type convexWireDocument struct {
	ID   string          `json:"id"`
	Data json.RawMessage `json:"data"`
}

func convexWireDocuments(ctx context.Context, rows []*convexsdk.Row) ([]any, error) {
	if len(rows) > 1000 {
		return nil, errDynamoResponse
	}
	out := make([]any, len(rows))
	bytes := 128
	for i, row := range rows {
		if err := dynamoFresh(ctx); err != nil {
			return nil, err
		}
		if row == nil {
			continue
		}
		if row.ID == "" || len(row.ID) > 1024 || !utf8.ValidString(row.ID) || row.Document == nil {
			return nil, errDynamoResponse
		}
		data, err := convexEncodeObject(row.Document)
		if err != nil {
			return nil, errDynamoResponse
		}
		bytes += len(data) + 6*len(row.ID) + 128
		if bytes > 8<<20 {
			return nil, errDynamoResponse
		}
		if err := dynamoFresh(ctx); err != nil {
			return nil, err
		}
		out[i] = convexWireDocument{ID: row.ID, Data: json.RawMessage(data)}
	}
	return out, nil
}

func convexEncodeObject(object convexsdk.Object) ([]byte, error) {
	value, err := convexsdk.ObjectValue(object)
	if err != nil {
		return nil, err
	}
	return convexsdk.EncodeValue(value)
}
