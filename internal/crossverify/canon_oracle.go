package crossverify

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"time"
	"unicode/utf8"

	"strings"

	"github.com/datatug/datatug-cli/pkg/dbcopy/faithful"
)

// canonOracle is the canonical form (see canon.go) of a value read from Oracle through its Go driver
// (github.com/sijms/go-ora/v3), which returns every NUMBER as the string of its exact decimal value, a BINARY_DOUBLE as
// float64, VARCHAR2 and CLOB as string, RAW and BLOB as bytes, and DATE and TIMESTAMP as time.Time. A date or time is
// written with an explicit format, as many decimals as the column holds: the verifier never asks the engine for text.
func canonOracle(ch faithful.Choice, v any) (string, error) {
	if v == nil {
		return "N", nil
	}
	switch ch.Family {
	case "int", "bool":
		if s, ok := v.(string); ok {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return "", fmt.Errorf("value %q of the %s column is not an integer: %w", s, ch.Type, err)
			}
			return "i" + strconv.FormatInt(n, 10), nil
		}
	case "float":
		if x, ok := v.(float64); ok {
			return fmt.Sprintf("f%016x", math.Float64bits(x)), nil
		}
	case "decimal":
		if s, ok := v.(string); ok {
			r, err := ratOf(s)
			if err != nil {
				return "", err
			}
			return "d" + r.String(), nil
		}
	case "text":
		if x, ok := v.(string); ok {
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
	}
	return "", fmt.Errorf("value %v (%T) does not fit the %s column", v, v, ch.Type)
}

// normalizeSourceOracle is what Oracle makes of the form of a SQLite value: an empty text and an empty RAW are NULL, and a
// negative zero is a zero. It is the model the verifier holds the load to, and the plan says the same in its warnings.
func normalizeSourceOracle(c *faithful.ScriptColumn, form string) string {
	switch {
	case c.Choice.Family == "text" && form == "t0:":
		return "N"
	case c.Choice.Family == "bytes" && c.Choice.Len > 0 && form == "b0:":
		return "N"
	}
	return form
}

// lobLengthOracle reads the length of a LOB: the driver gives an empty BLOB as NULL, and the length tells the two apart.
func lobLengthOracle(quoted string) string { return "DBMS_LOB.GETLENGTH(" + quoted + ")" }

var reStartWith = regexp.MustCompile(`START WITH:\s*(\d+)`)

// checkIdentityOracle checks the start of the identity counter of a table: the number the next row gets is the counter
// SQLite keeps, or one after the highest id, and at least 1.
func checkIdentityOracle(ctx context.Context, in Input, t *faithful.ScriptTable, add func(obj, aspect, want, got string)) error {
	var id *faithful.ScriptColumn
	for _, c := range t.Columns {
		if c.AutoInc && c.Choice.Family == "int" {
			id = c
		}
	}
	if id == nil {
		return nil
	}
	want := id.Src.SequenceStart
	if want <= 0 {
		want = 1
		var highest any
		if err := in.Source.QueryRowContext(ctx, "SELECT MAX("+quoteSQLite(id.Src.Name)+") FROM "+quoteSQLite(t.Src.Name)).Scan(&highest); err != nil {
			return fmt.Errorf("read the highest id of the source: %w", err)
		}
		if n, ok := highest.(int64); ok {
			want = n + 1
		}
	}
	want = max(want, 1)
	var options string
	if err := in.Target.QueryRowContext(ctx, `SELECT identity_options FROM all_tab_identity_cols WHERE owner = :schema AND table_name = :t AND column_name = :c`,
		sql.Named("schema", in.Schema), sql.Named("t", t.Name), sql.Named("c", id.Name)).Scan(&options); err != nil {
		return fmt.Errorf("read the identity counter: %w", err)
	}
	got := ""
	if m := reStartWith.FindStringSubmatch(options); m != nil {
		got = m[1]
	}
	add(t.Name+"."+id.Name, "identity counter", strconv.FormatInt(want, 10), got)
	return nil
}

// unstorableOracle is why Oracle cannot store a name, by rules written here and not taken from the exporter: empty, not
// valid UTF-8, longer than 128 bytes, holding a double quotation mark, ROWID, a control or line-break character or a
// byte-order mark. "" for a name it can store.
func unstorableOracle(name string) string {
	switch {
	case name == "":
		return "empty"
	case !utf8.ValidString(name):
		return "not valid UTF-8"
	case len(name) > 128:
		return "longer than 128 bytes"
	case strings.Contains(name, `"`):
		return "holds a double quotation mark"
	case name == "ROWID":
		return "ROWID"
	case strings.HasPrefix(strings.ToUpper(name), "BIN$"):
		return "starts with BIN$ (the recycle bin)"
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f || r == 0x2028 || r == 0x2029 || r == 0xFEFF || r == utf8.RuneError {
			return "a character it cannot hold"
		}
	}
	return ""
}

// noColumnTwin: Oracle keeps two columns whose names differ by case apart, so a rename of a column is never justified by a twin.
func noColumnTwin(*faithful.ScriptTable, *faithful.ScriptColumn) bool { return false }
