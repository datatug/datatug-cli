package apicontract_local

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"time"
)

// ValueType is TypedValue's discriminant, exactly the appendix's eight
// variants (api-contract.md "Shared JSON types").
type ValueType string

// The eight TypedValue variants. Every other string is invalid.
const (
	TypeString   ValueType = "string"
	TypeNumber   ValueType = "number"
	TypeInteger  ValueType = "integer"
	TypeDecimal  ValueType = "decimal"
	TypeBoolean  ValueType = "boolean"
	TypeDate     ValueType = "date"
	TypeDateTime ValueType = "datetime"
	TypeNull     ValueType = "null"
)

// dateLayout/dateTimeLayout are the appendix's exact wire formats: "date" is
// YYYY-MM-DD; "datetime" is RFC3339 normalized to UTC (so it must carry a
// literal "Z" offset, not "+00:00" or any other zone).
const dateLayout = "2006-01-02"

var canonicalIntegerPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)
var canonicalDecimalPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

// TypedValue is the appendix's {type, value} discriminated union. Exactly
// one of the typed fields below is meaningful, selected by Type; the zero
// TypedValue{} is invalid (no valid Type) and must never be marshalled or
// returned to a caller — build one with the New* constructors or Null().
type TypedValue struct {
	Type ValueType
	// Text carries the wire "value" for string, integer, decimal, date and
	// datetime — every variant whose JSON value is itself a string.
	Text string
	// Number carries the wire "value" for TypeNumber: a finite float64,
	// exactly representable as a JSON number (never NaN/Inf).
	Number float64
	// Bool carries the wire "value" for TypeBoolean.
	Bool bool
}

// NewStringValue builds a TypeString TypedValue.
func NewStringValue(s string) TypedValue { return TypedValue{Type: TypeString, Text: s} }

// NewNumberValue builds a TypeNumber TypedValue. n must be finite.
func NewNumberValue(n float64) TypedValue { return TypedValue{Type: TypeNumber, Number: n} }

// NewIntegerValue builds a TypeInteger TypedValue from a Go int64, always
// producing the appendix's canonical decimal text (no leading zeros, "-0"
// normalized to "0").
func NewIntegerValue(n int64) TypedValue {
	return TypedValue{Type: TypeInteger, Text: strconv.FormatInt(n, 10)}
}

// NewIntegerText builds a TypeInteger TypedValue from already-decimal text
// (e.g. one too large for int64), validating it is canonical.
func NewIntegerText(text string) (TypedValue, error) {
	if !canonicalIntegerPattern.MatchString(text) {
		return TypedValue{}, fmt.Errorf("apicontract_local: %q is not a canonical decimal integer (no leading zeros or +, exactly one leading -)", text)
	}
	return TypedValue{Type: TypeInteger, Text: text}, nil
}

// NewDecimalValue builds a TypeDecimal TypedValue from already-formatted
// canonical decimal text, validating it.
func NewDecimalValue(text string) (TypedValue, error) {
	if !canonicalDecimalPattern.MatchString(text) {
		return TypedValue{}, fmt.Errorf("apicontract_local: %q is not a canonical decimal (no leading zeros or +, exactly one leading -)", text)
	}
	return TypedValue{Type: TypeDecimal, Text: text}, nil
}

// NewBooleanValue builds a TypeBoolean TypedValue.
func NewBooleanValue(b bool) TypedValue { return TypedValue{Type: TypeBoolean, Bool: b} }

// NewDateValue builds a TypeDate TypedValue from a YYYY-MM-DD string,
// validating it is a real calendar date.
func NewDateValue(text string) (TypedValue, error) {
	if _, err := time.Parse(dateLayout, text); err != nil {
		return TypedValue{}, fmt.Errorf("apicontract_local: %q is not a valid YYYY-MM-DD date: %w", text, err)
	}
	return TypedValue{Type: TypeDate, Text: text}, nil
}

// NewDateTimeValue builds a TypeDateTime TypedValue, normalizing t to UTC
// and formatting it RFC3339 with a literal "Z" offset.
func NewDateTimeValue(t time.Time) TypedValue {
	return TypedValue{Type: TypeDateTime, Text: t.UTC().Format(time.RFC3339Nano)}
}

// NullValue is the TypeNull TypedValue.
func NullValue() TypedValue { return TypedValue{Type: TypeNull} }

// wireTypedValue is TypedValue's exact JSON shape.
type wireTypedValue struct {
	Type  ValueType   `json:"type"`
	Value interface{} `json:"value"`
}

// MarshalJSON writes v as {"type":..., "value":...}, exactly the appendix's
// shape. An invalid Type or a non-finite Number is refused rather than
// silently emitting a malformed envelope.
func (v TypedValue) MarshalJSON() ([]byte, error) {
	w := wireTypedValue{Type: v.Type}
	switch v.Type {
	case TypeString, TypeInteger, TypeDecimal, TypeDate, TypeDateTime:
		w.Value = v.Text
	case TypeNumber:
		if math.IsNaN(v.Number) || math.IsInf(v.Number, 0) {
			return nil, fmt.Errorf("apicontract_local: TypedValue{type:number} must be finite, got %v", v.Number)
		}
		w.Value = v.Number
	case TypeBoolean:
		w.Value = v.Bool
	case TypeNull:
		w.Value = nil
	default:
		return nil, fmt.Errorf("apicontract_local: unknown TypedValue type %q", v.Type)
	}
	return json.Marshal(w)
}

// UnmarshalJSON parses {"type":..., "value":...}, validating value against
// type's exact wire rules (canonical integer/decimal text, calendar date,
// RFC3339-UTC datetime, finite number). Unknown fields alongside type/value
// are rejected: the appendix requires "exact field names", and this is also
// where a client-supplied principal/role hidden in a values payload would
// first be caught (defense in depth; the real guard is server-side scope
// validation elsewhere).
func (v *TypedValue) UnmarshalJSON(data []byte) error {
	var raw struct {
		Type  ValueType       `json:"type"`
		Value json.RawMessage `json:"value"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return fmt.Errorf("apicontract_local: invalid TypedValue: %w", err)
	}
	switch raw.Type {
	case TypeString:
		var s string
		if err := strictUnmarshal(raw.Value, &s); err != nil {
			return fmt.Errorf("apicontract_local: TypedValue{type:string}: %w", err)
		}
		*v = TypedValue{Type: TypeString, Text: s}
	case TypeNumber:
		var n float64
		if err := strictUnmarshal(raw.Value, &n); err != nil {
			return fmt.Errorf("apicontract_local: TypedValue{type:number}: %w", err)
		}
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return errors.New("apicontract_local: TypedValue{type:number} must be finite")
		}
		*v = TypedValue{Type: TypeNumber, Number: n}
	case TypeInteger:
		var s string
		if err := strictUnmarshal(raw.Value, &s); err != nil {
			return fmt.Errorf("apicontract_local: TypedValue{type:integer}: %w", err)
		}
		parsed, err := NewIntegerText(s)
		if err != nil {
			return err
		}
		*v = parsed
	case TypeDecimal:
		var s string
		if err := strictUnmarshal(raw.Value, &s); err != nil {
			return fmt.Errorf("apicontract_local: TypedValue{type:decimal}: %w", err)
		}
		parsed, err := NewDecimalValue(s)
		if err != nil {
			return err
		}
		*v = parsed
	case TypeBoolean:
		var b bool
		if err := strictUnmarshal(raw.Value, &b); err != nil {
			return fmt.Errorf("apicontract_local: TypedValue{type:boolean}: %w", err)
		}
		*v = TypedValue{Type: TypeBoolean, Bool: b}
	case TypeDate:
		var s string
		if err := strictUnmarshal(raw.Value, &s); err != nil {
			return fmt.Errorf("apicontract_local: TypedValue{type:date}: %w", err)
		}
		parsed, err := NewDateValue(s)
		if err != nil {
			return err
		}
		*v = parsed
	case TypeDateTime:
		var s string
		if err := strictUnmarshal(raw.Value, &s); err != nil {
			return fmt.Errorf("apicontract_local: TypedValue{type:datetime}: %w", err)
		}
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return fmt.Errorf("apicontract_local: TypedValue{type:datetime}: %q is not RFC3339: %w", s, err)
		}
		if t.Location() != time.UTC && t.Format("Z07:00") != "Z" {
			return fmt.Errorf("apicontract_local: TypedValue{type:datetime}: %q is not normalized to UTC (must end in Z)", s)
		}
		*v = TypedValue{Type: TypeDateTime, Text: s}
	case TypeNull:
		if string(raw.Value) != "null" && len(raw.Value) != 0 {
			return errors.New("apicontract_local: TypedValue{type:null} must carry value:null")
		}
		*v = TypedValue{Type: TypeNull}
	default:
		return fmt.Errorf("apicontract_local: unknown TypedValue type %q", raw.Type)
	}
	return nil
}

func strictUnmarshal(data json.RawMessage, out any) error {
	if len(data) == 0 {
		return errors.New("missing value")
	}
	return json.Unmarshal(data, out)
}

// Equal reports whether v and other carry the same type AND the same value —
// AC:typed-context-isolation's "distinct typed values 5 and \"5\"... types
// stay distinct": TypedValue{integer,"5"} and TypedValue{string,"5"} are
// never Equal, regardless of how their Go-native forms might compare.
func (v TypedValue) Equal(other TypedValue) bool {
	if v.Type != other.Type {
		return false
	}
	switch v.Type {
	case TypeNumber:
		return v.Number == other.Number
	case TypeBoolean:
		return v.Bool == other.Bool
	case TypeNull:
		return true
	default:
		return v.Text == other.Text
	}
}

// IsZero reports whether v is the zero TypedValue (no Type set) — never a
// valid wire value, only a "not built yet" sentinel for Go callers.
func (v TypedValue) IsZero() bool { return v.Type == "" }

// Native converts v to a plain Go value suitable for driver-bound query
// parameter binding (dal.Param substitution -> database/sql args) and for
// JSON-free comparisons: string/date/datetime stay string, integer parses to
// int64 (returning an error for a canonical value too large for int64 —
// Phase 1's demo parameters never need bigint), decimal stays string
// (preserve precision; no lossy float64 conversion), number is float64,
// boolean is bool, null is nil.
func (v TypedValue) Native() (any, error) {
	switch v.Type {
	case TypeString, TypeDate, TypeDateTime, TypeDecimal:
		return v.Text, nil
	case TypeInteger:
		n, err := strconv.ParseInt(v.Text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("apicontract_local: integer %q does not fit int64: %w", v.Text, err)
		}
		return n, nil
	case TypeNumber:
		return v.Number, nil
	case TypeBoolean:
		return v.Bool, nil
	case TypeNull:
		return nil, nil
	default:
		return nil, fmt.Errorf("apicontract_local: unknown TypedValue type %q", v.Type)
	}
}

// FromGoValue converts a Go-native value (as returned by secureread's row
// data: typically int64/float64/string/bool/[]byte/time.Time/nil from a
// database/sql driver) into a TypedValue. declaredType is an optional hint
// (a QueryDef/Recordset column's own declared "type" string: "integer",
// "number", "string", "boolean", "date", "datetime", "decimal") consulted
// first; when it is empty or does not match v's runtime shape, the type is
// inferred from v itself. This is Phase 1's own engineering rule for
// shaping a database row into the appendix's typed Result.recordset — the
// appendix defines the wire TypedValue shape but not how a server derives
// one from a driver value, and no richer per-column type registry exists
// yet in this codebase to consult instead.
func FromGoValue(v any, declaredType string) (TypedValue, error) {
	if v == nil {
		return NullValue(), nil
	}
	switch value := v.(type) {
	case TypedValue:
		return value, nil
	case string:
		return typedFromString(value, declaredType)
	case []byte:
		return typedFromString(string(value), declaredType)
	case bool:
		return NewBooleanValue(value), nil
	case int:
		return NewIntegerValue(int64(value)), nil
	case int32:
		return NewIntegerValue(int64(value)), nil
	case int64:
		return NewIntegerValue(value), nil
	case float32:
		return numberOrDecimal(float64(value), declaredType)
	case float64:
		return numberOrDecimal(value, declaredType)
	case time.Time:
		if declaredType == string(TypeDate) {
			return NewDateValue(value.UTC().Format(dateLayout))
		}
		return NewDateTimeValue(value), nil
	default:
		return TypedValue{}, fmt.Errorf("apicontract_local: cannot convert Go value of type %T to a TypedValue", v)
	}
}

// typedFromString shapes a string driver value per declaredType (verbatim
// pass-through for "string"/"decimal"/"date"/"datetime"; parsed for
// "integer"), defaulting to TypeString when declaredType is empty/unknown —
// the safest fallback, since a text column's driver value is legitimately a
// string regardless of what the entity model thinks it means.
func typedFromString(s, declaredType string) (TypedValue, error) {
	switch ValueType(declaredType) {
	case TypeInteger:
		return NewIntegerText(s)
	case TypeDecimal:
		return NewDecimalValue(s)
	case TypeDate:
		return NewDateValue(s)
	case TypeDateTime:
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return NewDateTimeValue(t), nil
		}
		return NewStringValue(s), nil
	default:
		return NewStringValue(s), nil
	}
}

func numberOrDecimal(f float64, declaredType string) (TypedValue, error) {
	if ValueType(declaredType) == TypeDecimal {
		return NewDecimalValue(strconv.FormatFloat(f, 'f', -1, 64))
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return TypedValue{}, fmt.Errorf("apicontract_local: non-finite float %v cannot be a TypedValue{type:number}", f)
	}
	return NewNumberValue(f), nil
}
