package crossverify

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy/faithful"
	"modernc.org/sqlite"
)

// The Oracle verifier is tested against an emulated Oracle, the way the MySQL and SQL Server ones are: a SQLite database
// that holds the data of the plan with the values in the Go types the Oracle driver returns (a NUMBER as the string of its
// decimal value, a BINARY_DOUBLE as a float64, text as a string, bytes, time.Time) and the ALL_ views the reader queries as
// tables. The real engine is the journey's job (scripts/check-journey-oracle.sh).

var registerOracleFuncs sync.Once

const emulatedOracleSchema = "DEMO"

func planOracleFor(t testing.TB, src *sql.DB) *faithful.ScriptPlan {
	t.Helper()
	p, err := faithful.PlanScript(context.Background(), "oracle", src)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// oracleColumnRow is the catalog's view of a planned column: DATA_TYPE, DATA_PRECISION, DATA_SCALE, CHAR_LENGTH, CHAR_USED, DATA_LENGTH.
func oracleColumnRow(ch faithful.Choice) (dataType string, prec, scale, charLen any, charUsed any, dataLen int) {
	switch ch.Family {
	case "int":
		return "NUMBER", map[int]int{16: 5, 32: 10, 64: 19}[ch.Bits], 0, 0, nil, 22
	case "bool":
		return "NUMBER", 1, 0, 0, nil, 22
	case "float":
		return "BINARY_DOUBLE", nil, nil, 0, nil, 8
	case "decimal":
		return "NUMBER", ch.Prec, ch.Scale, 0, nil, 22
	case "date":
		return "DATE", nil, nil, 0, nil, 7
	case "datetime":
		return fmt.Sprintf("TIMESTAMP(%d)", ch.Fsp), nil, nil, 0, nil, 11
	case "text":
		if ch.Len == 0 {
			return "CLOB", nil, nil, 0, nil, 4000
		}
		if strings.HasSuffix(ch.Type, "BYTE)") {
			return "VARCHAR2", nil, nil, ch.Len, "B", ch.Len
		}
		return "VARCHAR2", nil, nil, ch.Len, "C", 4 * ch.Len
	}
	if ch.Len == 0 {
		return "BLOB", nil, nil, 0, nil, 4000
	}
	return "RAW", nil, nil, 0, nil, ch.Len
}

// emulateOracleValue is a SQLite value as the Oracle driver returns it from the column the plan created.
func emulateOracleValue(c *faithful.ScriptColumn, v any) any {
	ch := c.Choice
	if v == nil {
		return nil
	}
	switch ch.Family {
	case "int", "bool":
		return strconv.FormatInt(v.(int64), 10)
	case "float":
		if i, ok := v.(int64); ok {
			return float64(i)
		}
		return v
	case "decimal":
		switch x := v.(type) {
		case int64:
			return strconv.FormatInt(x, 10)
		case float64:
			return strconv.FormatFloat(x, 'f', -1, 64)
		}
		r, _ := new(big.Rat).SetString(fmt.Sprint(v))
		return strings.TrimRight(strings.TrimRight(r.FloatString(ch.Scale), "0"), ".")
	case "date", "datetime":
		return v
	case "text":
		switch x := v.(type) {
		case string:
			if x == "" {
				return nil
			}
			return x
		case int64:
			return strconv.FormatInt(x, 10)
		}
		return fmt.Sprint(v)
	}
	var b []byte
	switch x := v.(type) {
	case string:
		b = []byte(x)
	case []byte:
		b = x
	case int64:
		b = []byte(strconv.FormatInt(x, 10))
	}
	if len(b) == 0 && ch.Len > 0 {
		return nil // an empty RAW is NULL in Oracle
	}
	return b
}

// emulatedLobLength is what the emulated Oracle runs for DBMS_LOB.GETLENGTH, which SQLite has no function of that name for:
// the verifier's own expression is checked by TestLobLengthOracle.
func emulatedLobLength(t testing.TB) {
	t.Helper()
	was := oracleEngine.lobLength
	oracleEngine.lobLength = func(quoted string) string { return "LENGTH(" + quoted + ")" }
	t.Cleanup(func() { oracleEngine.lobLength = was })
}

// emulateOracle builds the emulated Oracle database for a plan and the SQLite source it was planned from.
func emulateOracle(t testing.TB, src *sql.DB, p *faithful.ScriptPlan) *sql.DB {
	t.Helper()
	registerOracleFuncs.Do(func() {
		sqlite.MustRegisterScalarFunction("SYS_CONTEXT", 2, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) { return emulatedOracleSchema, nil })
	})
	emulatedLobLength(t)
	db, err := sql.Open("sqlite", fmt.Sprintf("file:odst%d?mode=memory&cache=shared", dbCounter.Add(1)))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	x := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	for _, ddl := range []string{
		`CREATE TABLE dual (dummy)`,
		`CREATE TABLE all_tables (owner, table_name, temporary)`,
		`CREATE TABLE all_tab_columns (owner, table_name, column_id, column_name, data_type, data_precision, data_scale, char_length, char_used, data_length, nullable, identity_column, data_default)`,
		`CREATE TABLE all_cons_columns (owner, table_name, constraint_name, column_name, position)`,
		`CREATE TABLE all_constraints (owner, table_name, constraint_name, constraint_type, status, validated, "deferrable", deferred, delete_rule, r_constraint_name, index_name, search_condition_vc)`,
		`CREATE TABLE all_indexes (owner, table_owner, table_name, index_name, uniqueness, index_type)`,
		`CREATE TABLE all_ind_columns (index_owner, index_name, table_name, column_name, descend, column_position)`,
		`CREATE TABLE all_ind_expressions (index_owner, index_name, table_name, column_position, column_expression)`,
		`CREATE TABLE all_tab_identity_cols (owner, table_name, column_name, identity_options)`,
	} {
		x(ddl)
	}
	x(`INSERT INTO dual VALUES ('X')`)
	// the marker
	x(`CREATE TABLE "` + faithful.MySQLMarkerTable + `" (source_sha256, tables_count, rows_count)`)
	var rows int64
	for _, tb := range p.Tables {
		rows += tb.Rows
	}
	x(`INSERT INTO "`+faithful.MySQLMarkerTable+`" VALUES (?, ?, ?)`, strings.Repeat("ab", 32), len(p.Tables), rows)
	x(`INSERT INTO all_tables VALUES (?, ?, 'N')`, emulatedOracleSchema, faithful.MySQLMarkerTable)
	x(`INSERT INTO all_tab_columns VALUES (?, ?, 1, 'source_sha256', 'VARCHAR2', NULL, NULL, 64, 'C', 256, 'N', 'NO', NULL)`, emulatedOracleSchema, faithful.MySQLMarkerTable)
	// a table of another owner, one that is temporary, and one in the recycle bin are not read
	x(`INSERT INTO all_tables VALUES ('OTHER', 'ghost', 'N')`)
	x(`INSERT INTO all_tables VALUES (?, 'tmp', 'Y')`, emulatedOracleSchema)
	x(`INSERT INTO all_tables VALUES (?, 'BIN$abc', 'N')`, emulatedOracleSchema)
	owner := emulatedOracleSchema
	consNames := map[string]map[string]string{} // table -> columns joined -> constraint name that is a primary key or unique
	seq := 0
	for _, tb := range p.Tables {
		consNames[tb.Name] = map[string]string{}
		x(`INSERT INTO all_tables VALUES (?, ?, 'N')`, owner, tb.Name)
		var names, decls []string
		for j, c := range tb.Columns {
			names = append(names, `"`+c.Name+`"`)
			decl := ""
			switch c.Choice.Family {
			case "date":
				decl = " DATE"
			case "datetime":
				decl = " DATETIME"
			}
			decls = append(decls, `"`+c.Name+`"`+decl)
			dt, prec, scale, charLen, charUsed, dataLen := oracleColumnRow(c.Choice)
			def, identity := any(nil), "NO"
			if c.Default != "" {
				def = "'x'"
			}
			if c.AutoInc {
				identity, def = "YES", `"`+owner+`"."ISEQ$$_1".nextval`
			}
			nullable := "Y"
			if c.NotNull {
				nullable = "N"
			}
			x(`INSERT INTO all_tab_columns VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, owner, tb.Name, j+1, c.Name, dt, prec, scale, charLen, charUsed, dataLen, nullable, identity, def)
			if c.NotNull && !c.AutoInc {
				seq++
				x(`INSERT INTO all_constraints VALUES (?, ?, ?, 'C', 'ENABLED', 'VALIDATED', 'NOT DEFERRABLE', 'IMMEDIATE', NULL, NULL, NULL, ?)`, owner, tb.Name, fmt.Sprintf("SYS_C%d", seq), `"`+c.Name+`" IS NOT NULL`)
			}
		}
		x(`CREATE TABLE "` + tb.Name + `" (` + strings.Join(decls, ", ") + `)`)
		var sel []string
		for _, c := range tb.Columns {
			sel = append(sel, `+"`+strings.ReplaceAll(c.Src.Name, `"`, `""`)+`"`)
		}
		rs, err := src.Query("SELECT " + strings.Join(sel, ", ") + ` FROM "` + strings.ReplaceAll(tb.Src.Name, `"`, `""`) + `" ORDER BY ` + tb.OrderBy)
		if err != nil {
			t.Fatal(err)
		}
		vals := make([]any, len(tb.Columns))
		ptrs := make([]any, len(tb.Columns))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		var maxID int64
		hasID := false
		for rs.Next() {
			if err := rs.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			var args []any
			places := make([]string, len(vals))
			for i, c := range tb.Columns {
				v := emulateOracleValue(c, vals[i])
				if b, ok := v.([]byte); ok && len(b) == 0 {
					places[i] = "zeroblob(0)" // SQLite binds an empty []byte as NULL; an empty BLOB is written as one
				} else {
					places[i] = "?"
					args = append(args, v)
				}
				if v, ok := vals[i].(int64); ok && c.AutoInc && (!hasID || v > maxID) {
					maxID, hasID = v, true
				}
			}
			x(`INSERT INTO "`+tb.Name+`" (`+strings.Join(names, ", ")+`) VALUES (`+strings.Join(places, ", ")+`)`, args...)
		}
		_ = rs.Close()
		for _, c := range tb.Columns {
			if c.AutoInc {
				next := c.Src.SequenceStart
				if next <= 0 {
					next = 1
					if hasID {
						next = maxID + 1
					}
				}
				next = max(next, 1)
				x(`INSERT INTO all_tab_identity_cols VALUES (?, ?, ?, ?)`, owner, tb.Name, c.Name, fmt.Sprintf("START WITH: %d, INCREMENT BY: 1, MAX_VALUE: 9999999999999999999999999999, MIN_VALUE: 1, CYCLE_FLAG: N, CACHE_SIZE: 20", next))
			}
		}
		constraint := func(name, kind string, cols []string) {
			x(`INSERT INTO all_constraints VALUES (?, ?, ?, ?, 'ENABLED', 'VALIDATED', 'NOT DEFERRABLE', 'IMMEDIATE', NULL, NULL, ?, NULL)`, owner, tb.Name, name, kind, name)
			for i, c := range cols {
				x(`INSERT INTO all_cons_columns VALUES (?, ?, ?, ?, ?)`, owner, tb.Name, name, c, i+1)
			}
			consNames[tb.Name][strings.Join(cols, "\x1f")] = name
			x(`INSERT INTO all_indexes VALUES (?, ?, ?, ?, 'UNIQUE', 'NORMAL')`, owner, owner, tb.Name, name)
			for i, c := range cols {
				x(`INSERT INTO all_ind_columns VALUES (?, ?, ?, ?, 'ASC', ?)`, owner, name, tb.Name, c, i+1)
			}
		}
		if len(tb.PK) > 0 {
			constraint("PK_"+tb.Name, "P", tb.PK)
		}
		for k, u := range tb.Uniques {
			name := u.Name
			if name == "" {
				name = fmt.Sprintf("SYS_UQ_%s_%d", tb.Name, k)
			}
			constraint(name, "U", u.Columns)
		}
		for _, ix := range tb.Indexes {
			kind, unique := "NORMAL", "NONUNIQUE"
			if ix.Unique {
				unique = "UNIQUE"
			}
			hasDesc := false
			for _, c := range ix.Columns {
				hasDesc = hasDesc || c.Desc
			}
			if hasDesc || ix.Where != "" {
				kind = "FUNCTION-BASED NORMAL"
			}
			x(`INSERT INTO all_indexes VALUES (?, ?, ?, ?, ?, ?)`, owner, owner, tb.Name, ix.Name, unique, kind)
			for i, c := range ix.Columns {
				descend, col := "ASC", c.Name
				if c.Desc {
					descend = "DESC"
				}
				if hasDesc || ix.Where != "" {
					col = fmt.Sprintf("SYS_NC%05d$", i+1)
					expr := `"` + c.Name + `"`
					if ix.Where != "" {
						expr = fmt.Sprintf("CASE  WHEN (%s) THEN \"%s\" END", ix.Where, c.Name)
					}
					x(`INSERT INTO all_ind_expressions VALUES (?, ?, ?, ?, ?)`, owner, ix.Name, tb.Name, i+1, expr)
				}
				x(`INSERT INTO all_ind_columns VALUES (?, ?, ?, ?, ?, ?)`, owner, ix.Name, tb.Name, col, descend, i+1)
			}
		}
		for i, ck := range tb.Checks {
			seq++
			x(`INSERT INTO all_constraints VALUES (?, ?, ?, 'C', 'ENABLED', 'VALIDATED', 'NOT DEFERRABLE', 'IMMEDIATE', NULL, NULL, NULL, ?)`, owner, tb.Name, fmt.Sprintf("CK_%s_%d", tb.Name, i), ck.Expr)
		}
	}
	// foreign keys, once every table has its keys
	for _, tb := range p.Tables {
		for i, f := range tb.FKs {
			name := f.Name
			if name == "" {
				name = fmt.Sprintf("SYS_FK_%s_%d", tb.Name, i+1)
			}
			del := f.OnDelete
			if del == "" {
				del = "NO ACTION"
			}
			deferrable, deferred := "NOT DEFERRABLE", "IMMEDIATE"
			if f.Deferrable {
				deferrable = "DEFERRABLE"
			}
			if f.Deferred {
				deferred = "DEFERRED"
			}
			ref := consNames[f.RefTable][strings.Join(f.RefColumns, "\x1f")]
			x(`INSERT INTO all_constraints VALUES (?, ?, ?, 'R', 'ENABLED', 'VALIDATED', ?, ?, ?, ?, NULL, NULL)`, owner, tb.Name, name, deferrable, deferred, del, ref)
			for j, c := range f.Columns {
				x(`INSERT INTO all_cons_columns VALUES (?, ?, ?, ?, ?)`, owner, tb.Name, name, c, j+1)
			}
		}
	}
	return db
}
