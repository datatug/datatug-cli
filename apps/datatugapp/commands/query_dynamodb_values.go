package commands

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/dal-go/dalgo2dynamodb"
)

var errDynamoResponse = errors.New("DynamoDB response could not be encoded safely")
var dynamoNumberSyntax = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

type dynamoKeyJSON map[string]map[string]json.RawMessage

func dynamoNumber(value string) bool {
	if len(value) > 128 || !dynamoNumberSyntax.MatchString(value) {
		return false
	}
	if i := strings.IndexAny(value, "eE"); i >= 0 {
		exponent, err := strconv.Atoi(value[i+1:])
		if err != nil || exponent < -130 || exponent > 130 {
			return false
		}
	}
	_, ok := new(big.Rat).SetString(value)
	return ok
}
func decodeDynamoKey(input dynamoKeyJSON, table dalgo2dynamodb.Table) (dalgo2dynamodb.Item, error) {
	count := 1
	if table.SortKey != "" {
		count++
	}
	if len(input) != count {
		return nil, errDynamoInput
	}
	result := dalgo2dynamodb.Item{}
	for name, tagged := range input {
		if name != table.PartitionKey && (table.SortKey == "" || name != table.SortKey) || len(tagged) != 1 {
			return nil, errDynamoInput
		}
		value, err := decodeDynamoScalar(tagged)
		if err != nil {
			return nil, err
		}
		result[name] = value
	}
	return result, nil
}
func decodeDynamoScalar(tagged map[string]json.RawMessage) (types.AttributeValue, error) {
	if len(tagged) != 1 {
		return nil, errDynamoInput
	}
	var tag string
	var raw json.RawMessage
	for tag, raw = range tagged {
		break
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil || text == "" || !utf8.ValidString(text) {
		return nil, errDynamoInput
	}
	switch tag {
	case "S":
		return &types.AttributeValueMemberS{Value: text}, nil
	case "N":
		if !dynamoNumber(text) {
			return nil, errDynamoInput
		}
		return &types.AttributeValueMemberN{Value: text}, nil
	case "B":
		binary, err := base64.StdEncoding.Strict().DecodeString(text)
		if err != nil || len(binary) == 0 {
			return nil, errDynamoInput
		}
		return &types.AttributeValueMemberB{Value: binary}, nil
	default:
		return nil, errDynamoInput
	}
}

// Values have already passed decodeDynamoScalar; compare without float64.
func dynamoScalarOrder(left, right types.AttributeValue) (int, bool) {
	switch a := left.(type) {
	case *types.AttributeValueMemberS:
		b, ok := right.(*types.AttributeValueMemberS)
		if ok {
			return strings.Compare(a.Value, b.Value), true
		}
	case *types.AttributeValueMemberB:
		b, ok := right.(*types.AttributeValueMemberB)
		if ok {
			return bytes.Compare(a.Value, b.Value), true
		}
	case *types.AttributeValueMemberN:
		b, ok := right.(*types.AttributeValueMemberN)
		if ok {
			x, _ := new(big.Rat).SetString(a.Value)
			y, _ := new(big.Rat).SetString(b.Value)
			return x.Cmp(y), true
		}
	}
	return 0, false
}

func dynamoWireItem(item dalgo2dynamodb.Item) (map[string]any, error) {
	if item == nil {
		return nil, nil
	}
	nodes := 0
	return dynamoWireMap(item, 0, &nodes)
}
func dynamoWireMap(item dalgo2dynamodb.Item, depth int, nodes *int) (map[string]any, error) {
	if depth > 32 || len(item) > 10_000 {
		return nil, errDynamoResponse
	}
	result := make(map[string]any, len(item))
	for name, value := range item {
		if name == "" || !utf8.ValidString(name) {
			return nil, errDynamoResponse
		}
		tagged, err := dynamoWireValue(value, depth+1, nodes)
		if err != nil {
			return nil, err
		}
		result[name] = tagged
	}
	return result, nil
}
func dynamoWireValue(value types.AttributeValue, depth int, nodes *int) (map[string]any, error) {
	*nodes += 1
	if depth > 32 || *nodes > 100_000 {
		return nil, errDynamoResponse
	}
	switch v := value.(type) {
	case *types.AttributeValueMemberS:
		if v != nil && utf8.ValidString(v.Value) {
			return map[string]any{"S": v.Value}, nil
		}
	case *types.AttributeValueMemberN:
		if v != nil && dynamoNumber(v.Value) {
			return map[string]any{"N": v.Value}, nil
		}
	case *types.AttributeValueMemberB:
		if v != nil {
			return map[string]any{"B": base64.StdEncoding.EncodeToString(v.Value)}, nil
		}
	case *types.AttributeValueMemberBOOL:
		if v != nil {
			return map[string]any{"BOOL": v.Value}, nil
		}
	case *types.AttributeValueMemberNULL:
		if v != nil && v.Value {
			return map[string]any{"NULL": true}, nil
		}
	case *types.AttributeValueMemberM:
		if v != nil {
			nested, err := dynamoWireMap(v.Value, depth, nodes)
			if err != nil {
				return nil, err
			}
			return map[string]any{"M": nested}, nil
		}
	case *types.AttributeValueMemberL:
		if v != nil && len(v.Value) <= 10_000 {
			list := make([]any, len(v.Value))
			for i, entry := range v.Value {
				tagged, err := dynamoWireValue(entry, depth+1, nodes)
				if err != nil {
					return nil, err
				}
				list[i] = tagged
			}
			return map[string]any{"L": list}, nil
		}
	case *types.AttributeValueMemberSS:
		if v != nil && len(v.Value) > 0 {
			for _, s := range v.Value {
				if !utf8.ValidString(s) {
					return nil, errDynamoResponse
				}
			}
			return map[string]any{"SS": v.Value}, nil
		}
	case *types.AttributeValueMemberNS:
		if v != nil && len(v.Value) > 0 {
			for _, s := range v.Value {
				if !dynamoNumber(s) {
					return nil, errDynamoResponse
				}
			}
			return map[string]any{"NS": v.Value}, nil
		}
	case *types.AttributeValueMemberBS:
		if v != nil && len(v.Value) > 0 {
			list := make([]string, len(v.Value))
			for i, b := range v.Value {
				list[i] = base64.StdEncoding.EncodeToString(b)
			}
			return map[string]any{"BS": list}, nil
		}
	}
	return nil, errDynamoResponse
}

// The provider bounds parsed data to 1 MiB here; JSON escaping/tagging can expand
// it. This additional 8 MiB delivery bound fails closed without partial stdout.
func writeDynamoResult(ctx context.Context, output io.Writer, value any) error {
	var buffer bytes.Buffer
	if err := json.NewEncoder(&buffer).Encode(value); err != nil {
		return errDynamoResponse
	}
	if buffer.Len() > 8<<20 {
		return errDynamoResponse
	}
	if err := dynamoFresh(ctx); err != nil {
		return err
	}
	n, err := output.Write(buffer.Bytes())
	if err != nil {
		return err
	}
	if n != buffer.Len() {
		return io.ErrShortWrite
	}
	return nil
}
