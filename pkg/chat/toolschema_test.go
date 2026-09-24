package chat

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type toolSchemaFixture struct {
	Required string `json:"required"`
	Optional string `json:"optional,omitempty"`
	Hidden   string `json:"-"`
	unexp    string //nolint:unused // exercises the unexported-field skip
	Desc     string `json:"desc" jsonschema:"a described field"`
	Nested   struct {
		Inner int `json:"inner"`
	} `json:"nested"`
	NestedPtr *struct {
		Inner int `json:"inner"`
	} `json:"nestedPtr,omitempty"`
	Items   []string       `json:"items,omitempty"`
	Flags   map[string]any `json:"flags,omitempty"`
	Count   int            `json:"count,omitempty"`
	Ratio   float64        `json:"ratio,omitempty"`
	Enabled bool           `json:"enabled,omitempty"`
}

func TestArgsSchemaCoversEveryFieldShape(t *testing.T) {
	raw := argsSchema(toolSchemaFixture{})
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v, raw=%s", err, raw)
	}
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Fatalf("schema envelope = %+v", schema)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties missing: %+v", schema)
	}
	if _, ok := properties["Hidden"]; ok {
		t.Fatal("json:\"-\" field should be skipped")
	}
	if _, ok := properties["unexp"]; ok {
		t.Fatal("unexported field should be skipped")
	}
	desc, ok := properties["desc"].(map[string]any)
	if !ok || desc["description"] != "a described field" {
		t.Fatalf("desc property = %+v", properties["desc"])
	}
	nested, ok := properties["nested"].(map[string]any)
	if !ok || nested["type"] != "object" {
		t.Fatalf("nested struct property = %+v", properties["nested"])
	}
	nestedPtr, ok := properties["nestedPtr"].(map[string]any)
	if !ok || nestedPtr["type"] != "object" {
		t.Fatalf("nested pointer-to-struct property = %+v", properties["nestedPtr"])
	}
	items, ok := properties["items"].(map[string]any)
	if !ok || items["type"] != "array" {
		t.Fatalf("slice property = %+v", properties["items"])
	}
	if flags, ok := properties["flags"].(map[string]any); !ok || flags["type"] != "object" {
		t.Fatalf("map property = %+v", properties["flags"])
	}
	if count, ok := properties["count"].(map[string]any); !ok || count["type"] != "integer" {
		t.Fatalf("int property = %+v", properties["count"])
	}
	if ratio, ok := properties["ratio"].(map[string]any); !ok || ratio["type"] != "number" {
		t.Fatalf("float property = %+v", properties["ratio"])
	}
	if enabled, ok := properties["enabled"].(map[string]any); !ok || enabled["type"] != "boolean" {
		t.Fatalf("bool property = %+v", properties["enabled"])
	}
	required, ok := schema["required"].([]any)
	if !ok || len(required) != 3 || required[0] != "desc" || required[1] != "nested" || required[2] != "required" {
		t.Fatalf("required = %+v, want [desc nested required] (sorted, omitempty fields excluded)", schema["required"])
	}
}

// TestArgsSchemaDereferencesPointerRoot covers argsSchema's own pointer-root
// loop (t = t.Elem()): the same struct schema must come out whether called
// with a value or a pointer to it.
func TestArgsSchemaDereferencesPointerRoot(t *testing.T) {
	value := argsSchema(toolSchemaFixture{})
	pointer := argsSchema(&toolSchemaFixture{})
	if string(value) != string(pointer) {
		t.Fatalf("pointer-root schema differs from value-root schema:\nvalue=%s\npointer=%s", value, pointer)
	}
}

func TestStructSchemaPanicsOnNonStruct(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil || !strings.Contains(r.(string), "is not a struct") {
			t.Fatalf("recover = %v, want a not-a-struct panic", r)
		}
	}()
	argsSchema(42)
}

// TestArgsSchemaPanicsOnMarshalFailure drives argsSchema's json.Marshal
// error branch through the marshalToolSchema seam: structSchema itself
// never builds an unmarshalable shape, so there is no real struct that
// reaches this branch honestly.
func TestArgsSchemaPanicsOnMarshalFailure(t *testing.T) {
	restore := marshalToolSchema
	t.Cleanup(func() { marshalToolSchema = restore })
	marshalErr := errors.New("boom")
	marshalToolSchema = func(any) ([]byte, error) { return nil, marshalErr }
	defer func() {
		r := recover()
		if r == nil || !strings.Contains(r.(string), "boom") {
			t.Fatalf("recover = %v, want a marshal-error panic containing %q", r, marshalErr)
		}
	}()
	argsSchema(toolSchemaFixture{})
}

func TestFieldSchemaPanicsOnUnsupportedKind(t *testing.T) {
	type unsupported struct {
		Ch chan int `json:"ch"`
	}
	defer func() {
		r := recover()
		if r == nil || !strings.Contains(r.(string), "unsupported field kind") {
			t.Fatalf("recover = %v, want an unsupported-kind panic", r)
		}
	}()
	argsSchema(unsupported{})
}
