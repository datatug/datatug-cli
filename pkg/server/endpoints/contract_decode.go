package endpoints

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"
)

// decodeContractBody strictly decodes a POST body into v: unknown top-level
// fields are rejected (json.Decoder.DisallowUnknownFields — every rewritten
// struct's own nested UnmarshalJSON, e.g. TypedValue, already does the
// same), and duplicate JSON keys anywhere in the document are rejected
// before v is even touched. api-contract.md "Scope and identity": "Repeated
// identity in two locations, duplicate JSON keys, unknown security-relevant
// fields, and a client-supplied principal or role are rejected, never
// reconciled by precedence." Two spellings of one struct field that differ
// only in case ("project" and "Project") are duplicates too: encoding/json
// matches them to the same field and would keep the last.
func decodeContractBody(body []byte, v any) error {
	if err := checkNoDuplicateKeys(body, reflect.TypeOf(v)); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

// checkNoDuplicateKeys walks data, which will be decoded into a value of
// type target (nil when unknown), and fails if any object anywhere in the
// document (at any nesting depth — top-level Scope fields, a Parameters
// map, a nested Fact, etc.) repeats a key. encoding/json's normal Unmarshal
// silently lets the last occurrence win, which is exactly the "reconciled
// by precedence" behavior the appendix forbids. Wherever the value decoded
// is a struct, keys are compared the way encoding/json matches them to
// fields - case-insensitively (foldJSONName) - since both spellings land in
// one field; map keys, and keys of a value of unknown type or one that
// decodes itself, are compared exactly, since encoding/json keeps them
// apart (run_query's parameter names "id" and "ID" stay two parameters).
// This mirrors datatug-core's apicontract.DecodeStrict.
func checkNoDuplicateKeys(data []byte, target reflect.Type) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := walkJSONValue(dec, target); err != nil {
		return err
	}
	return nil
}

func walkJSONValue(dec *json.Decoder, target reflect.Type) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil // a scalar value: nothing to walk.
	}
	target = derefType(target)
	switch delim {
	case '{':
		fields, fold := jsonObjectFields(target)
		seen := make(map[string]string)
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyTok.(string)
			if !ok {
				return fmt.Errorf("expected an object key, got %v", keyTok)
			}
			name := key
			if fold {
				name = foldJSONName(key)
			}
			if first, dup := seen[name]; dup {
				if first == key {
					return fmt.Errorf("duplicate JSON key %q", key)
				}
				return fmt.Errorf("duplicate JSON key %q: it names the same field as %q", key, first)
			}
			seen[name] = key
			var child reflect.Type
			switch {
			case fold:
				child = fields[name]
			case target != nil && target.Kind() == reflect.Map:
				child = target.Elem()
			}
			if err := walkJSONValue(dec, child); err != nil {
				return err
			}
		}
		_, err := dec.Token() // consume the closing '}'
		return err
	case '[':
		var elem reflect.Type
		if target != nil && (target.Kind() == reflect.Slice || target.Kind() == reflect.Array) {
			elem = target.Elem()
		}
		for dec.More() {
			if err := walkJSONValue(dec, elem); err != nil {
				return err
			}
		}
		_, err := dec.Token() // consume the closing ']'
		return err
	}
	return nil
}

// jsonUnmarshalerType is json.Unmarshaler's reflect.Type.
var jsonUnmarshalerType = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()

// derefType strips pointers from t.
func derefType(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// jsonObjectFields returns the fields encoding/json decodes a JSON object
// into when t is a struct that does not decode itself, keyed by
// foldJSONName of their JSON names, and whether it is one.
func jsonObjectFields(t reflect.Type) (map[string]reflect.Type, bool) {
	if t == nil || t.Kind() != reflect.Struct || t.Implements(jsonUnmarshalerType) || reflect.PointerTo(t).Implements(jsonUnmarshalerType) {
		return nil, false
	}
	fields := make(map[string]reflect.Type)
	addJSONFields(fields, t)
	return fields, true
}

// addJSONFields adds t's JSON fields to fields, flattening embedded
// structs the way encoding/json does.
func addJSONFields(fields map[string]reflect.Type, t reflect.Type) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			if embedded := derefType(f.Type); embedded.Kind() == reflect.Struct {
				addJSONFields(fields, embedded)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		if folded := foldJSONName(name); fields[folded] == nil {
			fields[folded] = f.Type
		}
	}
}

// foldJSONName folds name the way encoding/json does when it matches a key
// to a struct field: two names fold equal exactly when bytes.EqualFold
// holds for them (ASCII letters upper-cased, every other rune mapped to the
// smallest rune of its simple case-folding orbit, so the Kelvin sign folds
// with "k").
func foldJSONName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < utf8.RuneSelf {
			if 'a' <= r && r <= 'z' {
				r -= 'a' - 'A'
			}
			b.WriteRune(r)
			continue
		}
		b.WriteRune(foldRune(r))
	}
	return b.String()
}

// foldRune returns the smallest rune of r's simple case-folding orbit.
func foldRune(r rune) rune {
	for {
		next := unicode.SimpleFold(r)
		if next <= r {
			return next
		}
		r = next
	}
}
