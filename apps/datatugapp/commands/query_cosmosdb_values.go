package commands

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	"github.com/dal-go/dalgo2cosmosdb"
)

func cosmosInputValue(raw json.RawMessage) (any, error) {
	// Reuse the strict duplicate-key, Unicode, depth, node and size admission;
	// UseNumber retains every native numeric parameter lexeme unchanged.
	input := append([]byte(`{"value":`), raw...)
	input = append(input, '}')
	var value struct {
		Value any `json:"value"`
	}
	if err := decodeDynamoDocument(input, &value); err != nil {
		return nil, err
	}
	return value.Value, nil
}

type cosmosWireDocument struct {
	Key  cosmosNativeKey `json:"key"`
	Data map[string]any  `json:"data"`
}
type cosmosWireBudget struct{ nodes, bytes int }

// Values originate in the provider's bounded closed JSON tree. This additional
// closed conversion refuses unexpected native types/cycles and caps aggregate
// tagging/escaping expansion before allocating the buffered stdout envelope.
func cosmosWireValue(ctx context.Context, value any, depth int, budget *cosmosWireBudget) (any, error) {
	if err := dynamoFresh(ctx); err != nil {
		return nil, err
	}
	budget.nodes++
	budget.bytes += 32
	if depth > 32 || budget.nodes > 65536 || budget.bytes > 8<<20 {
		return nil, errDynamoResponse
	}
	switch v := value.(type) {
	case nil, bool:
		return value, nil
	case string:
		if !utf8.ValidString(v) || len(v) > 1<<20 {
			return nil, errDynamoResponse
		}
		budget.bytes += 6 * len(v)
		if budget.bytes > 8<<20 {
			return nil, errDynamoResponse
		}
		return v, nil
	case json.Number:
		if len(v) > 128 || !influxJSONNumber.MatchString(string(v)) {
			return nil, errDynamoResponse
		}
		budget.bytes += len(v)
		if budget.bytes > 8<<20 {
			return nil, errDynamoResponse
		}
		return influxNumberJSON{Kind: "number", Value: string(v)}, nil
	case []any:
		if len(v) > 65536-budget.nodes {
			return nil, errDynamoResponse
		}
		out := make([]any, len(v))
		for i, entry := range v {
			var err error
			out[i], err = cosmosWireValue(ctx, entry, depth+1, budget)
			if err != nil {
				return nil, err
			}
		}
		return out, nil
	case map[string]any:
		if len(v) > 65536-budget.nodes {
			return nil, errDynamoResponse
		}
		out := make(map[string]any, len(v))
		for key, entry := range v {
			if !utf8.ValidString(key) || len(key) > 1<<20 {
				return nil, errDynamoResponse
			}
			budget.bytes += 6 * len(key)
			var err error
			out[key], err = cosmosWireValue(ctx, entry, depth+1, budget)
			if err != nil {
				return nil, err
			}
		}
		return out, nil
	default:
		return nil, errDynamoResponse
	}
}
func cosmosWireDocuments(ctx context.Context, documents []*dalgo2cosmosdb.Document) ([]any, error) {
	if len(documents) > 1000 {
		return nil, errDynamoResponse
	}
	result := make([]any, len(documents))
	budget := &cosmosWireBudget{}
	for i, document := range documents {
		if document == nil {
			continue
		}
		if !utf8.ValidString(document.Key.ID) || !utf8.ValidString(document.Key.Partition) {
			return nil, errDynamoResponse
		}
		budget.bytes += 6 * (len(document.Key.ID) + len(document.Key.Partition))
		value, err := cosmosWireValue(ctx, document.Data, 0, budget)
		if err != nil {
			return nil, err
		}
		// The only admitted root above is the native document object.
		result[i] = cosmosWireDocument{Key: cosmosNativeKey{ID: document.Key.ID, Partition: document.Key.Partition}, Data: value.(map[string]any)}
	}
	return result, nil
}
