package crossverify

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

func quoteMSSQL(s string) string { return "[" + strings.ReplaceAll(s, "]", "]]") + "]" }

// truthy reads a flag of the catalog: a bit comes back as a bool from the SQL Server driver and as 0 or 1
// from anything else.
func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case nil:
		return false
	}
	return number(v) != 0
}

// mssqlTypeText is a column type as the plan spells it, in lower case: nvarchar(255), nvarchar(max), decimal(10,2),
// datetime2(3), float(53).
func mssqlTypeText(name string, maxLen, prec, scale int) string {
	switch name {
	case "nvarchar", "nchar":
		if maxLen < 0 {
			return name + "(max)"
		}
		return name + "(" + strconv.Itoa(maxLen/2) + ")"
	case "varchar", "char", "varbinary", "binary":
		if maxLen < 0 {
			return name + "(max)"
		}
		return name + "(" + strconv.Itoa(maxLen) + ")"
	case "decimal", "numeric":
		return fmt.Sprintf("%s(%d,%d)", name, prec, scale)
	case "datetime2", "time", "datetimeoffset":
		return fmt.Sprintf("%s(%d)", name, scale)
	case "float":
		return fmt.Sprintf("float(%d)", prec)
	}
	return name
}

// ReadMSSQL reads the catalog of schema (the connection's current schema when empty) from the sys views of
// SQL Server. The result has the shape of the MySQL catalog: the backing index of the primary key is the primary
// key, the backing index of a UNIQUE constraint is a unique index of the constraint's name, and a foreign key,
// check constraint or index that is disabled or filtered is flagged.
func ReadMSSQL(ctx context.Context, db Queryer, schema string) (*MySQLCatalog, error) {
	if schema == "" {
		if err := db.QueryRowContext(ctx, `SELECT SCHEMA_NAME()`).Scan(&schema); err != nil {
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
	in := " JOIN sys.schemas s ON s.schema_id = t.schema_id WHERE s.name = @schema"
	args := []any{sql.Named("schema", schema)}
	err := query(ctx, db, 1, `SELECT t.name FROM sys.tables t`+in+` AND t.is_ms_shipped = 0 ORDER BY t.object_id`, args, func(v []any) {
		t := &MyTable{Name: text(v[0])}
		cat.Tables = append(cat.Tables, t)
		byName[t.Name] = t
	})
	if err := wrap("tables", err); err != nil {
		return nil, err
	}
	err = query(ctx, db, 9, `SELECT t.name, c.name, ty.name, c.max_length, c.precision, c.scale, c.is_nullable, c.is_identity, c.default_object_id
FROM sys.tables t JOIN sys.columns c ON c.object_id = t.object_id JOIN sys.types ty ON ty.user_type_id = c.user_type_id`+in+` ORDER BY t.object_id, c.column_id`, args, func(v []any) {
		t := byName[text(v[0])]
		if t == nil {
			return
		}
		t.Columns = append(t.Columns, MyColumn{Name: text(v[1]), Type: mssqlTypeText(strings.ToLower(text(v[2])), number(v[3]), number(v[4]), number(v[5])),
			Nullable: truthy(v[6]), AutoIncrement: truthy(v[7]), HasDefault: number(v[8]) != 0})
	})
	if err := wrap("columns", err); err != nil {
		return nil, err
	}
	err = query(ctx, db, 8, `SELECT t.name, i.name, i.is_unique, i.is_primary_key, i.is_unique_constraint, i.has_filter, c.name, ic.is_descending_key
FROM sys.tables t JOIN sys.indexes i ON i.object_id = t.object_id JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
JOIN sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id`+in+` AND i.index_id > 0 AND ic.is_included_column = 0 AND ic.key_ordinal > 0
ORDER BY t.object_id, i.index_id, ic.key_ordinal`, args, func(v []any) {
		t := byName[text(v[0])]
		if t == nil {
			return
		}
		col := MyIndexColumn{Name: text(v[6]), Desc: truthy(v[7])}
		name := text(v[1])
		switch {
		case truthy(v[3]):
			t.PK = append(t.PK, col.Name)
		case len(t.Indexes) > 0 && t.Indexes[len(t.Indexes)-1].Name == name:
			t.Indexes[len(t.Indexes)-1].Columns = append(t.Indexes[len(t.Indexes)-1].Columns, col)
		default:
			t.Indexes = append(t.Indexes, MyIndex{Name: name, Unique: truthy(v[2]), Filtered: truthy(v[5]), Columns: []MyIndexColumn{col}})
		}
	})
	if err := wrap("indexes", err); err != nil {
		return nil, err
	}
	err = query(ctx, db, 10, `SELECT t.name, f.name, pc.name, rt.name, rc.name, f.update_referential_action_desc, f.delete_referential_action_desc, f.is_disabled, f.is_not_trusted, f.object_id
FROM sys.foreign_keys f JOIN sys.tables t ON t.object_id = f.parent_object_id JOIN sys.foreign_key_columns fc ON fc.constraint_object_id = f.object_id
JOIN sys.columns pc ON pc.object_id = fc.parent_object_id AND pc.column_id = fc.parent_column_id JOIN sys.tables rt ON rt.object_id = fc.referenced_object_id
JOIN sys.columns rc ON rc.object_id = fc.referenced_object_id AND rc.column_id = fc.referenced_column_id`+in+` ORDER BY f.object_id, fc.constraint_column_id`, args, func(v []any) {
		t := byName[text(v[0])]
		if t == nil {
			return
		}
		if n := len(t.FKs); n > 0 && t.FKs[n-1].Name == text(v[1]) {
			t.FKs[n-1].Columns = append(t.FKs[n-1].Columns, text(v[2]))
			t.FKs[n-1].RefColumns = append(t.FKs[n-1].RefColumns, text(v[4]))
			return
		}
		t.FKs = append(t.FKs, MyFK{Name: text(v[1]), Columns: []string{text(v[2])}, RefTable: text(v[3]), RefColumns: []string{text(v[4])},
			OnUpdate: strings.ReplaceAll(text(v[5]), "_", " "), OnDelete: strings.ReplaceAll(text(v[6]), "_", " ")})
		if truthy(v[7]) || truthy(v[8]) {
			t.Untrusted = append(t.Untrusted, "foreign key "+text(v[1]))
		}
	})
	if err := wrap("foreign keys", err); err != nil {
		return nil, err
	}
	err = query(ctx, db, 4, `SELECT t.name, cc.name, cc.is_disabled, cc.is_not_trusted FROM sys.check_constraints cc JOIN sys.tables t ON t.object_id = cc.parent_object_id`+in, args, func(v []any) {
		t := byName[text(v[0])]
		if t == nil {
			return
		}
		t.Checks++
		if truthy(v[2]) || truthy(v[3]) {
			t.Untrusted = append(t.Untrusted, "check constraint "+text(v[1]))
		}
	})
	if err := wrap("check constraints", err); err != nil {
		return nil, err
	}
	return cat, nil
}
