package crossverify

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

func quoteOracle(s string) string { return `"` + s + `"` }

// oracleTypeText is a column type as the plan spells it, in lower case: varchar2(255 char), varchar2(4000 byte),
// number(10,2), number, timestamp(3), raw(4), date, clob, blob, binary_double.
func oracleTypeText(dataType string, precision, scale, charLength, dataLength any, charUsed string) string {
	name := strings.ToLower(dataType)
	switch name {
	case "varchar2", "nvarchar2", "char", "nchar":
		unit := "byte"
		if charUsed == "C" {
			unit = "char"
		}
		n := number(charLength)
		if unit == "byte" {
			n = number(dataLength)
		}
		return fmt.Sprintf("%s(%d %s)", name, n, unit)
	case "raw":
		return fmt.Sprintf("raw(%d)", number(dataLength))
	case "number":
		if precision == nil {
			return "number"
		}
		return fmt.Sprintf("number(%d,%d)", number(precision), number(scale))
	}
	return name
}

var (
	reOracleNotNull = regexp.MustCompile(`^"[^"]*" IS NOT NULL$`)
	reIndexPlain    = regexp.MustCompile(`^"([^"]+)"$`)
	reIndexCase     = regexp.MustCompile(`THEN\s+"([^"]+)"\s+END`)
)

// ReadOracle reads the catalog of schema (the connection's current schema when empty) from the ALL_ views of Oracle.
// The result has the shape of the MySQL catalog: the backing index of the primary key is the primary key, the backing
// index of a UNIQUE constraint is a unique index of the constraint's name, a function-based unique index over CASE
// expressions is a filtered index over the columns it names, and a foreign key or check constraint that is disabled or
// not validated is flagged.
func ReadOracle(ctx context.Context, db Queryer, schema string) (*MySQLCatalog, error) {
	if schema == "" {
		if err := db.QueryRowContext(ctx, `SELECT SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') FROM DUAL`).Scan(&schema); err != nil {
			return nil, fmt.Errorf("read the current schema: %w", err)
		}
	}
	cat := &MySQLCatalog{Schema: schema}
	byName := map[string]*MyTable{}
	wrap := func(what string, err error) error {
		if err != nil {
			return fmt.Errorf("read %s: %w", what, err)
		}
		return nil
	}
	args := []any{sql.Named("schema", schema)}
	err := query(ctx, db, 1, `SELECT table_name FROM all_tables WHERE owner = :schema AND temporary = 'N' AND table_name NOT LIKE 'BIN$%' ORDER BY table_name`, args, func(v []any) {
		t := &MyTable{Name: text(v[0])}
		cat.Tables = append(cat.Tables, t)
		byName[t.Name] = t
	})
	if err := wrap("tables", err); err != nil {
		return nil, err
	}
	err = query(ctx, db, 11, `SELECT table_name, column_name, data_type, data_precision, data_scale, char_length, char_used, data_length, nullable, identity_column, data_default
FROM all_tab_columns WHERE owner = :schema ORDER BY table_name, column_id`, args, func(v []any) {
		t := byName[text(v[0])]
		if t == nil {
			return
		}
		identity := text(v[9]) == "YES"
		def := strings.ToLower(strings.TrimSpace(text(v[10])))
		t.Columns = append(t.Columns, MyColumn{Name: text(v[1]), Type: oracleTypeText(text(v[2]), v[3], v[4], v[5], v[7], text(v[6])),
			Nullable: text(v[8]) == "Y", AutoIncrement: identity, HasDefault: !identity && def != "" && def != "null", Default: text(v[10])})
	})
	if err := wrap("columns", err); err != nil {
		return nil, err
	}
	// Constraints: columns by constraint, then the constraints themselves.
	consCols := map[string][]string{}
	err = query(ctx, db, 3, `SELECT table_name, constraint_name, column_name FROM all_cons_columns WHERE owner = :schema ORDER BY table_name, constraint_name, position`, args, func(v []any) {
		k := text(v[0]) + "\x1f" + text(v[1])
		consCols[k] = append(consCols[k], text(v[2]))
	})
	if err := wrap("constraint columns", err); err != nil {
		return nil, err
	}
	type cons struct {
		table, name, kind, status, validated, deferrable, deferred, deleteRule, refName, indexName, condition string
	}
	var all []cons
	tableOf := map[string]string{}
	err = query(ctx, db, 11, `SELECT table_name, constraint_name, constraint_type, status, validated, "DEFERRABLE", deferred, delete_rule, r_constraint_name, index_name, search_condition_vc
FROM all_constraints WHERE owner = :schema AND constraint_type IN ('P', 'U', 'R', 'C') ORDER BY table_name, constraint_name`, args, func(v []any) {
		c := cons{text(v[0]), text(v[1]), text(v[2]), text(v[3]), text(v[4]), text(v[5]), text(v[6]), text(v[7]), text(v[8]), text(v[9]), text(v[10])}
		all = append(all, c)
		tableOf[c.name] = c.table
	})
	if err := wrap("constraints", err); err != nil {
		return nil, err
	}
	backing := map[string]bool{} // the indexes of primary and unique constraints
	for _, c := range all {
		t := byName[c.table]
		if t == nil {
			continue
		}
		cols := consCols[c.table+"\x1f"+c.name]
		switch c.kind {
		case "P":
			t.PK = cols
			backing[c.table+"\x1f"+firstNonEmptyString(c.indexName, c.name)] = true
		case "U":
			ix := MyIndex{Name: c.name, Unique: true}
			for _, n := range cols {
				ix.Columns = append(ix.Columns, MyIndexColumn{Name: n})
			}
			t.Indexes = append(t.Indexes, ix)
			backing[c.table+"\x1f"+firstNonEmptyString(c.indexName, c.name)] = true
		case "R":
			fk := MyFK{Name: c.name, Columns: cols, RefTable: tableOf[c.refName], RefColumns: consCols[tableOf[c.refName]+"\x1f"+c.refName],
				OnUpdate: "NO ACTION", OnDelete: c.deleteRule, Deferrable: c.deferrable == "DEFERRABLE", Deferred: c.deferred == "DEFERRED"}
			t.FKs = append(t.FKs, fk)
			if c.status != "ENABLED" || c.validated != "VALIDATED" {
				t.Untrusted = append(t.Untrusted, "foreign key "+c.name)
			}
		case "C":
			if reOracleNotNull.MatchString(strings.TrimSpace(c.condition)) {
				continue // the NOT NULL of a column is a check constraint in the catalog
			}
			t.Checks++
			if c.status != "ENABLED" || c.validated != "VALIDATED" {
				t.Untrusted = append(t.Untrusted, "check constraint "+c.name)
			}
		}
	}
	// Indexes: the expressions of the function-based ones first.
	exprs := map[string]string{}
	err = query(ctx, db, 3, `SELECT index_name, column_position, column_expression FROM all_ind_expressions WHERE index_owner = :schema`, args, func(v []any) {
		exprs[text(v[0])+"\x1f"+text(v[1])] = text(v[2])
	})
	if err := wrap("index expressions", err); err != nil {
		return nil, err
	}
	err = query(ctx, db, 7, `SELECT i.table_name, i.index_name, i.uniqueness, i.index_type, c.column_name, c.descend, c.column_position
FROM all_indexes i JOIN all_ind_columns c ON c.index_owner = i.owner AND c.index_name = i.index_name
WHERE i.owner = :schema AND i.table_owner = :schema AND i.index_type NOT LIKE 'LOB%' ORDER BY i.table_name, i.index_name, c.column_position`, args, func(v []any) {
		t := byName[text(v[0])]
		if t == nil || backing[text(v[0])+"\x1f"+text(v[1])] {
			return
		}
		name, col, desc := text(v[1]), text(v[4]), text(v[5]) == "DESC"
		filtered := false
		if e, ok := exprs[name+"\x1f"+text(v[6])]; ok {
			if m := reIndexPlain.FindStringSubmatch(strings.TrimSpace(e)); m != nil {
				col = m[1]
			} else if m := reIndexCase.FindStringSubmatch(e); m != nil {
				col, filtered = m[1], true
			} else {
				col = e
			}
		}
		ic := MyIndexColumn{Name: col, Desc: desc}
		if n := len(t.Indexes); n > 0 && t.Indexes[n-1].Name == name {
			t.Indexes[n-1].Columns = append(t.Indexes[n-1].Columns, ic)
			return
		}
		t.Indexes = append(t.Indexes, MyIndex{Name: name, Unique: text(v[2]) == "UNIQUE", Filtered: filtered, Columns: []MyIndexColumn{ic}})
	})
	if err := wrap("indexes", err); err != nil {
		return nil, err
	}
	return cat, nil
}

func firstNonEmptyString(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
