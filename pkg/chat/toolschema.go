package chat

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// argsSchema derives a JSON Schema object (ai.Tool.Schema) from a Go args
// struct by reflection, so a tool's schema can never drift from the struct
// its Handler actually decodes. Field mapping mirrors the encoding/json
// convention already used throughout this package:
//
//   - the exported field's `json` tag name (or its Go name when absent)
//     becomes the property name; `json:"-"` skips the field;
//   - a field without `,omitempty` in its `json` tag is schema-required;
//   - the optional `jsonschema:"..."` tag becomes the property description,
//     matching the tag convention the ADK function tools already used.
//
// It supports the plain scalar/slice/nested-struct shapes DataTug's tool
// argument types use; anything else is a programmer error and panics at
// tool-construction time (caught by tests, never reached at runtime).
func argsSchema(v any) json.RawMessage {
	t := reflect.TypeOf(v)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	schema := structSchema(t)
	b, err := json.Marshal(schema)
	if err != nil {
		panic(fmt.Sprintf("chat: marshal tool schema for %s: %v", t, err))
	}
	return b
}

func structSchema(t reflect.Type) map[string]any {
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("chat: argsSchema: %s is not a struct", t))
	}
	properties := map[string]any{}
	var required []string
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" { // unexported
			continue
		}
		name, omitempty, skip := jsonFieldName(field)
		if skip {
			continue
		}
		prop := fieldSchema(field.Type)
		if desc := field.Tag.Get("jsonschema"); desc != "" {
			prop["description"] = desc
		}
		properties[name] = prop
		if !omitempty {
			required = append(required, name)
		}
	}
	sort.Strings(required)
	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func jsonFieldName(field reflect.StructField) (name string, omitempty, skip bool) {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return "", false, true
	}
	parts := strings.Split(tag, ",")
	name = field.Name
	if parts[0] != "" {
		name = parts[0]
	}
	for _, opt := range parts[1:] {
		if opt == "omitempty" {
			omitempty = true
		}
	}
	return name, omitempty, false
}

func fieldSchema(t reflect.Type) map[string]any {
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": fieldSchema(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object"}
	case reflect.Struct:
		return structSchema(t)
	case reflect.Pointer:
		return fieldSchema(t.Elem())
	default:
		panic(fmt.Sprintf("chat: argsSchema: unsupported field kind %s", t.Kind()))
	}
}
