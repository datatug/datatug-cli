// Package strictjson decodes security-relevant JSON documents (connection
// descriptors, recovery references) that must be exactly one object with no
// duplicate keys at any depth, so two readers can never disagree on a value.
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// DecodeObject rejects duplicate keys at every depth and anything but one
// JSON object, then decodes it into target when target is not nil.
func DecodeObject(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := scanValue(d); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("expected one JSON value")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return errors.New("expected JSON object")
	}
	if target != nil {
		return json.Unmarshal(data, target)
	}
	return nil
}

func scanValue(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if delim == '{' {
		keys := map[string]bool{}
		for d.More() {
			// Inside an object the decoder only yields string keys.
			keyToken, err := d.Token()
			if err != nil {
				return err
			}
			key := keyToken.(string)
			if keys[key] {
				return errors.New("duplicate JSON object key")
			}
			keys[key] = true
			if err := scanValue(d); err != nil {
				return err
			}
		}
	} else {
		// d.Token never yields a closing delimiter here, so this is '['.
		for d.More() {
			if err := scanValue(d); err != nil {
				return err
			}
		}
	}
	_, err = d.Token()
	return err
}
