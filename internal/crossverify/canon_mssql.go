package crossverify

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/datatug/datatug-cli/pkg/dbcopy/faithful"
)

// canonMSSQL is the canonical form (see canon.go) of a value read from SQL Server through its Go driver, which
// returns integers as int64, a bit as bool, a float as float64, a decimal as the bytes of its text, NVARCHAR as
// string, binary as bytes, and date, datetime2 and time as time.Time. A date or time is written with an
// explicit format, as many decimals as the column holds: the verifier never asks the engine for text.
func canonMSSQL(ch faithful.Choice, v any) (string, error) {
	if v == nil {
		return "N", nil
	}
	switch ch.Family {
	case "int", "bool":
		switch x := v.(type) {
		case int64:
			return "i" + strconv.FormatInt(x, 10), nil
		case bool:
			if x {
				return "i1", nil
			}
			return "i0", nil
		}
	case "float":
		if x, ok := v.(float64); ok {
			return fmt.Sprintf("f%016x", math.Float64bits(x)), nil
		}
	case "decimal":
		var s string
		switch x := v.(type) {
		case []byte:
			s = string(x)
		case string:
			s = x
		default:
			return "", fmt.Errorf("value %v (%T) does not fit the %s column", v, v, ch.Type)
		}
		r, err := ratOf(s)
		if err != nil {
			return "", err
		}
		return "d" + r.String(), nil
	case "text":
		switch x := v.(type) {
		case string:
			return fmt.Sprintf("t%d:%s", len(x), x), nil
		case []byte:
			return fmt.Sprintf("t%d:%s", len(x), x), nil
		}
	case "bytes":
		if x, ok := v.([]byte); ok {
			return fmt.Sprintf("b%d:%s", len(x), x), nil
		}
	case "date":
		if x, ok := v.(time.Time); ok {
			return "D" + x.Format("2006-01-02"), nil
		}
	case "datetime":
		if x, ok := v.(time.Time); ok {
			return "T" + trimFraction(x.Format("2006-01-02 15:04:05.9999999")), nil
		}
	case "time":
		if x, ok := v.(time.Time); ok {
			return "T" + trimFraction(x.Format("15:04:05.9999999")), nil
		}
	}
	return "", fmt.Errorf("value %v (%T) does not fit the %s column", v, v, ch.Type)
}

// checkIdentity checks the counter of the identity column of a table that holds rows: the last number it used
// is the one before the number SQLite would give the next row, or the highest id when SQLite keeps no counter.
func checkIdentity(ctx context.Context, in Input, t *faithful.ScriptTable, add func(obj, aspect, want, got string)) error {
	var id *faithful.ScriptColumn
	for _, c := range t.Columns {
		if c.AutoInc && c.Choice.Family == "int" {
			id = c
		}
	}
	if id == nil || t.Rows == 0 {
		return nil
	}
	want := id.Src.SequenceStart - 1
	if id.Src.SequenceStart <= 0 {
		var max sql.NullInt64
		if err := in.Source.QueryRowContext(ctx, "SELECT MAX("+quoteSQLite(id.Src.Name)+") FROM "+quoteSQLite(t.Src.Name)).Scan(&max); err != nil {
			return fmt.Errorf("read the highest id of the source: %w", err)
		}
		want = max.Int64
	}
	var got string
	if err := in.Target.QueryRowContext(ctx, "SELECT IDENT_CURRENT(@table)", sql.Named("table", quoteMSSQL(t.Name))).Scan(&got); err != nil {
		return fmt.Errorf("read the identity counter: %w", err)
	}
	add(t.Name+"."+id.Name, "identity counter", strconv.FormatInt(want, 10), got)
	return nil
}
