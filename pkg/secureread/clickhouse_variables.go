package secureread

import (
	"encoding/json"
	"fmt"
	"github.com/dal-go/dalgo/dal"
	"math"
	"math/big"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/shopspring/decimal"
)

// This scalar-only domain belongs to the native compiler, not condeval's keyed
// comparison fallback. The effective AST's real adapter validator checks value
// ranges/types before I/O. Copies prevent caller mutation during preparation.
func clickHouseVariables(values map[string]any) (map[string]any, error) {
	fail := func() (map[string]any, error) {
		return nil, fmt.Errorf("%w: unsupported ClickHouse scalar variable", accesspolicies.ErrInvalidQuery)
	}
	if len(values) > 256 {
		return fail()
	}
	result := make(map[string]any, len(values))
	for name, value := range values {
		if len(name) > 128 || !dal.ValidParamName(name) || name == "currentUser" || strings.HasPrefix(name, "principal.") || strings.HasPrefix(name, "path.") {
			return fail()
		}
		switch v := value.(type) {
		case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, time.Time:
			result[name] = v
		case float32:
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) || (math.Trunc(float64(v)) == float64(v) && math.Abs(float64(v)) > 16777215) {
				return fail()
			}
			result[name] = v
		case float64:
			if math.IsNaN(v) || math.IsInf(v, 0) || (math.Trunc(v) == v && math.Abs(v) > 9007199254740991) {
				return fail()
			}
			result[name] = v
		case string:
			if len(v) > 4096 || !utf8.ValidString(v) {
				return fail()
			}
			result[name] = v
		case json.Number:
			if len(v) > 256 || !json.Valid([]byte(v)) {
				return fail()
			}
			number, err := decimal.NewFromString(string(v))
			if err != nil || number.Exponent() < -76 || number.Exponent() > 76 || number.Coefficient().BitLen() > 256 {
				return fail()
			}
			result[name] = v
		case *big.Int:
			if v == nil || v.BitLen() > 256 {
				return fail()
			}
			result[name] = new(big.Int).Set(v)
		case decimal.Decimal:
			if v.Exponent() < -76 || v.Exponent() > 76 || v.Coefficient().BitLen() > 256 {
				return fail()
			}
			result[name] = decimal.NewFromBigInt(v.Coefficient(), v.Exponent())
		default:
			return fail()
		}
	}
	return result, nil
}
