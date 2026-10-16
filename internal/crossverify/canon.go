package crossverify

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/datatug/datatug-cli/pkg/dbcopy/faithful"
)

// Canonical forms. Each value of a column is turned into one string by the family of the column the plan
// created, on both sides, and rows are compared by their canonical strings:
//
//	int, bool   i<decimal>              the source holds an INTEGER; the target an int64
//	float       f<16 hex digits>        the IEEE 754 bits of a float64; an INTEGER of the source is converted first
//	decimal     d<num>/<den>            an exact rational; the source holds an INTEGER, a REAL (read as the
//	                                    shortest decimal that identifies it, FormatFloat(x, 'f', -1, 64):
//	                                    Chinook's NUMERIC(10,2) holds REAL values) or a text; the target a DECIMAL text
//	text        t<len>:<bytes>          the UTF-8 bytes; an INTEGER of the source is its decimal text, a REAL is
//	                                    FormatFloat(x, 'g', -1, 64)
//	bytes       b<len>:<bytes>          the bytes; a text of the source is its UTF-8 bytes (a column carried as bytes)
//	date        D<yyyy-mm-dd>
//	datetime    T<yyyy-mm-dd hh:mm:ss[.f]>   trailing zeros of the fraction removed on both sides, as the column's
//	time        T<hh:mm:ss[.f]>              precision pads or cuts them
//
// NULL is "N" in every family.

func ratOf(s string) (*big.Rat, error) {
	r, ok := new(big.Rat).SetString(strings.TrimSpace(s))
	if !ok {
		return nil, fmt.Errorf("%q is not a decimal number", s)
	}
	return r, nil
}

func trimFraction(s string) string {
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

func sourceText(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case []byte:
		return string(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64), nil
	}
	return "", fmt.Errorf("value %v (%T) is not text", v, v)
}

// canonSource is the canonical form of a value read from SQLite (int64, float64, string or []byte).
func canonSource(ch faithful.Choice, v any) (string, error) {
	if v == nil {
		return "N", nil
	}
	switch ch.Family {
	case "int", "bool":
		if x, ok := v.(int64); ok {
			return "i" + strconv.FormatInt(x, 10), nil
		}
	case "float":
		switch x := v.(type) {
		case float64:
			return fmt.Sprintf("f%016x", math.Float64bits(x)), nil
		case int64:
			return fmt.Sprintf("f%016x", math.Float64bits(float64(x))), nil
		}
	case "decimal":
		var s string
		switch x := v.(type) {
		case int64:
			s = strconv.FormatInt(x, 10)
		case float64:
			s = strconv.FormatFloat(x, 'f', -1, 64)
		case string:
			s = x
		default:
			return "", fmt.Errorf("value %v (%T) is not a decimal", v, v)
		}
		r, err := ratOf(s)
		if err != nil {
			return "", err
		}
		return "d" + r.String(), nil
	case "text":
		s, err := sourceText(v)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("t%d:%s", len(s), s), nil
	case "bytes":
		s, err := sourceText(v)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("b%d:%s", len(s), s), nil
	case "date":
		if x, ok := v.(string); ok {
			return "D" + x, nil
		}
	case "datetime", "time":
		if x, ok := v.(string); ok {
			return "T" + trimFraction(x), nil
		}
	}
	return "", fmt.Errorf("value %v (%T) does not fit the %s column", v, v, ch.Type)
}

// canonTarget is the canonical form of a value read from MySQL through its Go driver (binary protocol:
// int64, float64, []byte).
func canonTarget(ch faithful.Choice, v any) (string, error) {
	if v == nil {
		return "N", nil
	}
	switch ch.Family {
	case "int", "bool":
		if x, ok := v.(int64); ok {
			return "i" + strconv.FormatInt(x, 10), nil
		}
	case "float":
		if x, ok := v.(float64); ok {
			return fmt.Sprintf("f%016x", math.Float64bits(x)), nil
		}
	case "decimal":
		if x, ok := v.([]byte); ok {
			r, err := ratOf(string(x))
			if err != nil {
				return "", err
			}
			return "d" + r.String(), nil
		}
	case "text":
		if x, ok := v.([]byte); ok {
			return fmt.Sprintf("t%d:%s", len(x), x), nil
		}
	case "bytes":
		if x, ok := v.([]byte); ok {
			return fmt.Sprintf("b%d:%s", len(x), x), nil
		}
	case "date":
		if x, ok := v.([]byte); ok {
			return "D" + string(x), nil
		}
	case "datetime", "time":
		if x, ok := v.([]byte); ok {
			return "T" + trimFraction(string(x)), nil
		}
	}
	return "", fmt.Errorf("value %v (%T) does not fit the %s column", v, v, ch.Type)
}

// rowDigest is a digest of the canonical forms of a row.
func rowDigest(forms []string) [32]byte {
	h := newHash()
	var n [8]byte
	for _, f := range forms {
		binary.BigEndian.PutUint64(n[:], uint64(len(f)))
		h.Write(n[:])
		h.Write([]byte(f))
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}
