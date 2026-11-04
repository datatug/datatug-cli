package commands

import (
	"context"
	"strconv"
	"testing"
)

func athenaItems(t *testing.T, object map[string]any) (map[string]any, []any) {
	t.Helper()
	items := object["tables"].(map[string]any)["items"].(map[string]any)
	return items, items["columns"].([]any)
}

func athenaAccepted(t *testing.T, object map[string]any) athenaNativeConfig {
	t.Helper()
	cfg, err := decodeAthenaConfig(context.Background(), athenaContractRaw(t, object))
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	return cfg
}

func athenaRefused(t *testing.T, object map[string]any) {
	t.Helper()
	cfg, err := decodeAthenaConfig(context.Background(), athenaContractRaw(t, object))
	if err != errAthenaConfig {
		t.Fatalf("err = %v, want errAthenaConfig", err)
	}
	athenaZeroConfig(t, cfg)
}

func TestAthenaConfigTableSchemaContracts(t *testing.T) {
	cases := []struct {
		name, tableField, columnField string
		column                        int
		value                         any
		remove                        bool
	}{
		{"missing table name", "name", "", 0, nil, true},
		{"table name boolean", "name", "", 0, false, false},
		{"empty table name", "name", "", 0, "", false},
		{"unknown table field", "owner", "", 0, "x", false},
		{"missing columns", "columns", "", 0, nil, true},
		{"nonarray columns", "columns", "", 0, "id", false},
		{"empty columns", "columns", "", 0, []any{}, false},
		{"idColumn boolean", "idColumn", "", 0, false, false},
		{"NUL table name", "name", "", 0, "bad\x00name", false},
		{"idColumn absent column", "idColumn", "", 0, "missing", false},
		{"idColumn numeric column", "idColumn", "", 0, "amount", false},
		{"column missing name", "", "name", 0, nil, true},
		{"column missing type", "", "type", 0, nil, true},
		{"column nonstring name", "", "name", 0, false, false},
		{"column nonstring type", "", "type", 0, false, false},
		{"column empty type", "", "type", 0, "", false},
		{"unknown column field", "", "owner", 0, "x", false},
		{"duplicate column names", "", "name", 1, "id", false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			object := athenaContractObject(t)
			items, columns := athenaItems(t, object)
			before := string(athenaContractRaw(t, object))
			target, field := items, test.tableField
			if field == "" {
				target = columns[test.column].(map[string]any)
				field = test.columnField
			}
			if test.remove {
				delete(target, field)
			} else {
				target[field] = test.value
			}
			if string(athenaContractRaw(t, object)) == before {
				t.Fatal("mutation did not change golden fixture")
			}
			athenaRefused(t, object)
		})
	}
	for _, idType := range []string{"varchar", "string", "char"} {
		t.Run("idColumn "+idType, func(t *testing.T) {
			object := athenaContractObject(t)
			items, columns := athenaItems(t, object)
			items["idColumn"] = "id"
			columns[0].(map[string]any)["type"] = idType
			cfg := athenaAccepted(t, object)
			table, ok := cfg.Tables["items"]
			if !ok || len(cfg.Tables) != 1 || table.Name != "actual" || table.IDColumn != "id" {
				t.Fatalf("table = %#v", table)
			}
			if len(table.Columns) != 2 || table.Columns[0] != (athenaColumnConfig{Name: "id", Type: idType}) || table.Columns[1] != (athenaColumnConfig{Name: "amount", Type: "decimal(38,10)"}) {
				t.Fatalf("columns = %#v", table.Columns)
			}
		})
	}
}

func athenaMappedFixture(t *testing.T, tables, columns int) map[string]any {
	object := athenaContractObject(t)
	mapped := make(map[string]any, tables)
	for i := 0; i < tables; i++ {
		values := make([]any, columns)
		for j := range values {
			values[j] = map[string]any{"name": "c" + strconv.Itoa(j), "type": "string"}
		}
		mapped["t"+strconv.Itoa(i)] = map[string]any{"name": "n" + strconv.Itoa(i), "columns": values}
	}
	object["tables"] = mapped
	return object
}

func TestAthenaConfigMappedCountAndChargeBounds(t *testing.T) {
	for _, test := range []struct {
		name         string
		tables, cols int
		ok           bool
	}{
		{"128 unique columns accepted", 1, 128, true},
		{"129 unique columns refused", 1, 129, false},
		{"256 tables accepted", 256, 1, true},
		{"257 tables refused", 257, 1, false},
		{"3 tables by 128 columns accepted", 3, 128, true},
		{"4 tables by 128 columns refused", 4, 128, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			object := athenaMappedFixture(t, test.tables, test.cols)
			if raw := athenaContractRaw(t, object); len(raw) > 64<<10 {
				t.Fatalf("encoded input %d exceeds wire cap", len(raw))
			}
			if !test.ok {
				athenaRefused(t, object)
				return
			}
			cfg := athenaAccepted(t, object)
			if len(cfg.Tables) != test.tables {
				t.Fatalf("tables = %d, want %d", len(cfg.Tables), test.tables)
			}
			for alias, table := range cfg.Tables {
				if len(table.Columns) != test.cols {
					t.Fatalf("%s columns = %d, want %d", alias, len(table.Columns), test.cols)
				}
				for i := range table.Columns {
					if table.Columns[i].Name != "c"+strconv.Itoa(i) || table.Columns[i].Type != "string" {
						t.Fatalf("%s column %d = %#v", alias, i, table.Columns[i])
					}
				}
			}
		})
	}
}
