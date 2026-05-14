package filter

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dal-go/dalgo/dbschema"
)

// CoerceValue converts the raw string value (as received from CLI or
// YAML) into the typed value expected by the column's introspected
// dbschema.Type. Returns the coerced value or an error naming the
// expected type and offending input (REQ:where-type-coercion).
//
// Type-mapping rules:
//   - Int      → int64 via strconv.ParseInt
//   - Float    → float64 via strconv.ParseFloat
//   - Decimal  → float64 (Decimal is carried as float per dalgo2ingitdb's
//     type-mapping table; lossy carrier documented in db copy spec)
//   - Bool     → bool via strconv.ParseBool ("true"/"false"/"1"/"0", case-insensitive)
//   - Time     → time.Time via time.Parse(time.DateOnly) for ISO date,
//     fallback time.RFC3339 for full datetime
//   - String   → passthrough
//
// Unknown column types (e.g. dbschema.Null, dbschema.Bytes) are passed
// through as strings — the source query will surface a type error if
// the operator can't handle them.
func CoerceValue(raw string, colType dbschema.Type) (any, error) {
	switch colType {
	case dbschema.Int:
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("value %q is not a valid integer", raw)
		}
		return v, nil

	case dbschema.Float, dbschema.Decimal:
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("value %q is not a valid number", raw)
		}
		return v, nil

	case dbschema.Bool:
		v, err := strconv.ParseBool(strings.ToLower(raw))
		if err != nil {
			return nil, fmt.Errorf("value %q is not a valid boolean (expected true/false/1/0)", raw)
		}
		return v, nil

	case dbschema.Time:
		// Try ISO date first, then full RFC3339 datetime.
		if t, err := time.Parse(time.DateOnly, raw); err == nil {
			return t, nil
		}
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			return t, nil
		}
		return nil, fmt.Errorf("value %q is not a valid date (expected YYYY-MM-DD or RFC3339)", raw)

	case dbschema.String:
		return raw, nil

	default:
		return raw, nil
	}
}
