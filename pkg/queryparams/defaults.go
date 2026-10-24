// Package queryparams converts declared query defaults without changing driver row types.
package queryparams

import (
	"encoding/json"
	"errors"
	"strconv"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
)

// ScalarDefault validates a declared scalar default and preserves exact numeric text.
func ScalarDefault(p datatug.ParameterDef) (apicontract.TypedValue, error) {
	var v apicontract.TypedValue
	if p.DefaultValue == nil || p.IsCredential || p.IsMultiValue {
		return v, errors.New("unsupported scalar default")
	}
	if err := p.Validate(); err != nil {
		return v, err
	}
	switch p.Type {
	case "string":
		v = apicontract.NewStringValue(p.DefaultValue.(string))
	case "boolean":
		v = apicontract.NewBooleanValue(p.DefaultValue.(bool))
	case "integer":
		var text string
		switch n := p.DefaultValue.(type) {
		case json.Number:
			text = string(n)
		case int:
			text = strconv.Itoa(n)
		case float64:
			text = strconv.FormatFloat(n, 'f', -1, 64)
		}
		v = apicontract.NewIntegerValue(text)
	case "number":
		n, ok := p.DefaultValue.(float64)
		if !ok {
			n, _ = p.DefaultValue.(json.Number).Float64()
		}
		v = apicontract.NewNumberValue(n)
	case "decimal", "date", "datetime":
		var text string
		switch n := p.DefaultValue.(type) {
		case string:
			text = n
		case json.Number:
			if p.Type == "decimal" {
				text = string(n)
			}
		case int:
			if p.Type == "decimal" {
				text = strconv.Itoa(n)
			}
		}
		v = apicontract.TypedValue{Type: apicontract.ValueType(p.Type), Str: text}
	default:
		return v, errors.New("unsupported scalar default type")
	}
	if err := v.Validate(); err != nil {
		return apicontract.TypedValue{}, err
	}
	return v, nil
}
