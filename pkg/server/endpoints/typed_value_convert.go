package endpoints

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
)

// This file holds the driver-value <-> apicontract.TypedValue conversion
// glue the CLI's now-deleted provisional schema package used to carry as
// TypedValue methods (Native, Equal, FromGoValue and friends).
// datatug-core's pkg/apicontract (the
// schema authority, v0.26.0) defines the wire TypedValue shape, its
// New*Value constructors and Validate(), but no driver-interop helpers —
// shaping a database/sql row value into a TypedValue, or converting one back
// to a native Go value for parameter binding, is this server's own
// engineering rule (no richer per-column type registry exists yet in this
// codebase to consult instead), not part of the shared wire schema. S78's
// report to the lead names this as a real local-symbol gap, but not a
// datatug-core one: these are CLI-side conversions built ON TOP of core's
// TypedValue, not a duplicate of it.
//
// Go cannot add methods to a type defined in another package, so every
// TypedValue.<Method>() call site the old package supported is now a free
// function taking an apicontract.TypedValue.

// nativeValue converts v to a plain Go value suitable for driver-bound query
// parameter binding (dal.Param substitution -> database/sql args) and for
// JSON-free comparisons: string/date/datetime stay string, integer parses to
// int64 (returning an error for a canonical value too large for int64 —
// Phase 1's demo parameters never need bigint), decimal stays string
// (preserve precision; no lossy float64 conversion), number is float64,
// boolean is bool, null is nil.
func nativeValue(v apicontract.TypedValue) (any, error) {
	switch v.Type {
	case apicontract.ValueTypeString, apicontract.ValueTypeDate, apicontract.ValueTypeDatetime, apicontract.ValueTypeDecimal:
		return v.Str, nil
	case apicontract.ValueTypeInteger:
		n, err := strconv.ParseInt(v.Str, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("apicontract: integer %q does not fit int64: %w", v.Str, err)
		}
		return n, nil
	case apicontract.ValueTypeNumber:
		return v.Num, nil
	case apicontract.ValueTypeBoolean:
		return v.Bool, nil
	case apicontract.ValueTypeNull:
		return nil, nil
	default:
		return nil, fmt.Errorf("apicontract: unknown TypedValue type %q", v.Type)
	}
}

// typedValueEqual reports whether a and b carry the same type AND the same
// value — AC:typed-context-isolation's "distinct typed values 5 and \"5\"...
// types stay distinct": TypedValue{integer,"5"} and TypedValue{string,"5"}
// are never equal, regardless of how their Go-native forms might compare.
func typedValueEqual(a, b apicontract.TypedValue) bool {
	if a.Type != b.Type {
		return false
	}
	switch a.Type {
	case apicontract.ValueTypeNumber:
		return a.Num == b.Num
	case apicontract.ValueTypeBoolean:
		return a.Bool == b.Bool
	case apicontract.ValueTypeNull:
		return true
	default:
		return a.Str == b.Str
	}
}

// newIntegerText builds a ValueTypeInteger TypedValue from already-decimal
// text (e.g. one too large for int64), validating it is canonical —
// core's own apicontract.NewIntegerValue does not validate at construction
// time (validation is Validate()'s job, called separately), but this
// server's own conversion path needs to fail fast on a value derived from
// an untrusted driver value, not ship an invalid one onto the wire.
func newIntegerText(text string) (apicontract.TypedValue, error) {
	tv := apicontract.NewIntegerValue(text)
	if err := tv.Validate(); err != nil {
		return apicontract.TypedValue{}, fmt.Errorf("apicontract: %w", err)
	}
	return tv, nil
}

// newDecimalText builds a ValueTypeDecimal TypedValue from already-formatted
// canonical decimal text, validating it (see newIntegerText's doc comment).
func newDecimalText(text string) (apicontract.TypedValue, error) {
	tv := apicontract.NewDecimalValue(text)
	if err := tv.Validate(); err != nil {
		return apicontract.TypedValue{}, fmt.Errorf("apicontract: %w", err)
	}
	return tv, nil
}

// newDateText builds a ValueTypeDate TypedValue from a YYYY-MM-DD string,
// validating it is a real calendar date (see newIntegerText's doc comment).
func newDateText(text string) (apicontract.TypedValue, error) {
	tv := apicontract.NewDateValue(text)
	if err := tv.Validate(); err != nil {
		return apicontract.TypedValue{}, fmt.Errorf("apicontract: %w", err)
	}
	return tv, nil
}

// newDateTimeValue builds a ValueTypeDatetime TypedValue, normalizing t to
// UTC and formatting it RFC3339 with a literal "Z" offset — core's
// apicontract.NewDatetimeValue takes an already-formatted string, with no
// time.Time convenience constructor of its own.
func newDateTimeValue(t time.Time) apicontract.TypedValue {
	return apicontract.NewDatetimeValue(t.UTC().Format(time.RFC3339Nano))
}

// fromGoValue converts a Go-native value (as returned by secureread's row
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
func fromGoValue(v any, declaredType string) (apicontract.TypedValue, error) {
	if v == nil {
		return apicontract.NewNullValue(), nil
	}
	switch value := v.(type) {
	case apicontract.TypedValue:
		return value, nil
	case string:
		return typedFromString(value, declaredType)
	case []byte:
		return typedFromString(string(value), declaredType)
	case bool:
		return apicontract.NewBooleanValue(value), nil
	case int:
		return newIntegerText(strconv.FormatInt(int64(value), 10))
	case int32:
		return newIntegerText(strconv.FormatInt(int64(value), 10))
	case int64:
		return newIntegerText(strconv.FormatInt(value, 10))
	case float32:
		return numberOrDecimal(float64(value), declaredType)
	case float64:
		return numberOrDecimal(value, declaredType)
	case time.Time:
		if declaredType == string(apicontract.ValueTypeDate) {
			return newDateText(value.UTC().Format("2006-01-02"))
		}
		return newDateTimeValue(value), nil
	default:
		return apicontract.TypedValue{}, fmt.Errorf("apicontract: cannot convert Go value of type %T to a TypedValue", v)
	}
}

// typedFromString shapes a string driver value per declaredType (verbatim
// pass-through for "string"/"decimal"/"date"/"datetime"; parsed for
// "integer"), defaulting to ValueTypeString when declaredType is
// empty/unknown — the safest fallback, since a text column's driver value is
// legitimately a string regardless of what the entity model thinks it means.
func typedFromString(s, declaredType string) (apicontract.TypedValue, error) {
	switch apicontract.ValueType(declaredType) {
	case apicontract.ValueTypeInteger:
		return newIntegerText(s)
	case apicontract.ValueTypeDecimal:
		return newDecimalText(s)
	case apicontract.ValueTypeDate:
		return newDateText(s)
	case apicontract.ValueTypeDatetime:
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return newDateTimeValue(t), nil
		}
		return apicontract.NewStringValue(s), nil
	default:
		return apicontract.NewStringValue(s), nil
	}
}

func numberOrDecimal(f float64, declaredType string) (apicontract.TypedValue, error) {
	if apicontract.ValueType(declaredType) == apicontract.ValueTypeDecimal {
		return newDecimalText(strconv.FormatFloat(f, 'f', -1, 64))
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return apicontract.TypedValue{}, fmt.Errorf("apicontract: non-finite float %v cannot be a TypedValue{type:number}", f)
	}
	return apicontract.NewNumberValue(f), nil
}
