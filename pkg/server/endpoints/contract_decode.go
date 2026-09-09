package endpoints

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// decodeContractBody strictly decodes a POST body into v: unknown top-level
// fields are rejected (json.Decoder.DisallowUnknownFields — every rewritten
// struct's own nested UnmarshalJSON, e.g. TypedValue, already does the
// same), and duplicate JSON keys anywhere in the document are rejected
// before v is even touched. api-contract.md "Scope and identity": "Repeated
// identity in two locations, duplicate JSON keys, unknown security-relevant
// fields, and a client-supplied principal or role are rejected, never
// reconciled by precedence."
func decodeContractBody(body []byte, v any) error {
	if err := checkNoDuplicateKeys(body); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

// checkNoDuplicateKeys walks data as generic JSON and fails if any object
// anywhere in the document (at any nesting depth — top-level Scope fields,
// a Parameters map, a nested Fact, etc.) repeats a key. encoding/json's
// normal Unmarshal silently lets the last occurrence win, which is exactly
// the "reconciled by precedence" behavior the appendix forbids.
func checkNoDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := walkJSONValue(dec); err != nil {
		return err
	}
	return nil
}

func walkJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil // a scalar value: nothing to walk.
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyTok.(string)
			if !ok {
				return fmt.Errorf("expected an object key, got %v", keyTok)
			}
			if seen[key] {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = true
			if err := walkJSONValue(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token() // consume the closing '}'
		return err
	case '[':
		for dec.More() {
			if err := walkJSONValue(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token() // consume the closing ']'
		return err
	}
	return nil
}
