package commands

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/queryresults"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/shopspring/decimal"
	"gopkg.in/yaml.v3"
)

// ClickHouse scalars bypass YAML's float decoder entirely. Name validation uses
// the shared policy grammar; the adapter remains the final binding authority.
func parseClickHouseVariables(pairs []string) (map[string]any, error) {
	if len(pairs) > 256 {
		return nil, fmt.Errorf("too many ClickHouse bindings")
	}
	values := make(map[string]any, len(pairs))
	for _, pair := range pairs {
		name, raw, ok := strings.Cut(pair, "=")
		name = strings.TrimSpace(name)
		if !ok || len(raw) > 4096 {
			return nil, fmt.Errorf("invalid ClickHouse binding")
		}
		if _, err := accesspolicies.ParseVariables([]string{name + "=placeholder"}); err != nil {
			return nil, fmt.Errorf("invalid or reserved ClickHouse binding name")
		}
		if _, exists := values[name]; exists {
			return nil, fmt.Errorf("ClickHouse binding name appears more than once")
		}
		if raw == "" {
			values[name] = ""
			continue
		}
		var document yaml.Node
		if err := yaml.Unmarshal([]byte(raw), &document); err != nil || len(document.Content) != 1 {
			return nil, fmt.Errorf("ClickHouse bindings require scalar YAML values")
		}
		scalar := document.Content[0]
		if scalar.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("ClickHouse bindings require scalar values")
		}
		// YAML may infer an overflowing plain JSON number (for example 1e9999)
		// as a string. Recognize its raw numeric lexeme before trusting that tag.
		// Quoted/tagged strings retain their explicit string semantics.
		text := strings.TrimSpace(raw)
		numericStart := len(text) > 0 && (text[0] == '-' || text[0] >= '0' && text[0] <= '9')
		plainJSONNumber := scalar.Style == 0 && numericStart && json.Valid([]byte(text))
		if plainJSONNumber || scalar.Tag == "!!int" || scalar.Tag == "!!float" {
			if len(text) > 256 || scalar.Style != 0 || !json.Valid([]byte(text)) {
				return nil, fmt.Errorf("ClickHouse numeric bindings require plain finite JSON numbers")
			}
			number, err := decimal.NewFromString(text)
			if err != nil || number.Exponent() < -76 || number.Exponent() > 76 || number.Coefficient().BitLen() > 256 {
				return nil, fmt.Errorf("ClickHouse numeric binding exceeds the exact scalar budget")
			}
			values[name] = json.Number(text)
			continue
		}
		switch scalar.Tag {
		case "!!bool":
			values[name] = strings.EqualFold(scalar.Value, "true")
		case "!!null":
			values[name] = nil
		case "!!str", "!!timestamp":
			values[name] = scalar.Value
		default:
			return nil, fmt.Errorf("unsupported ClickHouse scalar binding")
		}
	}
	return values, nil
}

func clickHouseTypedParameters(variables map[string]any) map[string]apicontract.TypedValueOrSet {
	if len(variables) == 0 {
		return nil
	}
	result := make(map[string]apicontract.TypedValueOrSet, len(variables))
	for name, value := range variables {
		if number, ok := value.(json.Number); ok {
			integer := apicontract.NewIntegerValue(number.String())
			if integer.Validate() == nil {
				result[name] = apicontract.ScalarValue(integer)
				continue
			}
			exact, err := decimal.NewFromString(number.String())
			if err == nil && exact.Exponent() >= -76 && exact.Exponent() <= 76 {
				result[name] = apicontract.ScalarValue(apicontract.NewDecimalValue(exact.String()))
			}
		} else {
			result[name] = apicontract.ScalarValue(queryresults.ConvertValue(value))
		}
	}
	return result
}
