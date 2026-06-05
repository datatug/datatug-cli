package filter

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseTableList(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"users", []string{"users"}},
		{"users,orders", []string{"users", "orders"}},
		{"  users , orders ", []string{"users", "orders"}},
		{",users,,orders,", []string{"users", "orders"}}, // empty segments dropped
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := ParseTableList(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseTableList(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestDirectives_Validate_IncludeExcludeMutex(t *testing.T) {
	d := &Directives{
		IncludeTables: []string{"users"},
		ExcludeTables: []string{"logs"},
	}
	err := d.PreValidate()
	if err == nil {
		t.Fatal("expected error for include+exclude both set, got nil")
	}
	if !strings.Contains(err.Error(), "--include") || !strings.Contains(err.Error(), "--exclude") {
		t.Fatalf("error %q must name both flags", err)
	}
}

func TestDirectives_PreValidate_NilSafe(t *testing.T) {
	var d *Directives
	if err := d.PreValidate(); err != nil {
		t.Fatalf("nil receiver should not error, got %v", err)
	}
}

func TestParseWhereFlag(t *testing.T) {
	cases := []struct {
		in        string
		wantTable string
		wantField string
		wantOp    OperatorToken
		wantValue string
		wantErr   bool
	}{
		{"Customer:Country:=:USA", "Customer", "Country", OpEqual, "USA", false},
		{"Invoice:Total:>=:100.50", "Invoice", "Total", OpGreaterOrEqual, "100.50", false},
		{"User:tags:in:admin,staff", "User", "tags", OpIn, "admin,staff", false},
		{"Log:msg:=:hello\\:world", "Log", "msg", OpEqual, "hello:world", false}, // \: escape
		{"only-three:parts:=", "", "", "", "", true},
		{"", "", "", "", "", true},
		{"a:b:badop:c", "", "", "", "", true},
		// Deferred operator rejected at parse time (REQ:operator-vocabulary).
		{"User:deleted_at:is_null:", "", "", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			table, pred, err := ParseWhereFlag(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseWhereFlag(%q) err=%v wantErr=%v", tc.in, err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if table != tc.wantTable || pred.Field != tc.wantField || pred.Operator != tc.wantOp || pred.Value != tc.wantValue {
				t.Fatalf("ParseWhereFlag(%q) = (%q, %+v), want (%q, {Field:%q Op:%q Value:%q})",
					tc.in, table, pred, tc.wantTable, tc.wantField, tc.wantOp, tc.wantValue)
			}
		})
	}
}

func TestParseTableList_AllEmpty(t *testing.T) {
	// All segments trim to empty — should return nil (line 27-29 of cli.go).
	got := ParseTableList(",  , ,")
	if got != nil {
		t.Fatalf("ParseTableList(\",  , ,\") = %v, want nil", got)
	}
}

func TestParseWhereFlag_EmptyTableOrField(t *testing.T) {
	cases := []struct {
		in string
	}{
		{":field:=:v"}, // blank table
		{"t:::v"},      // blank field (op also blank but table check fires first)
		{"t::=:v"},     // blank field
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			_, _, err := ParseWhereFlag(tc.in)
			if err == nil {
				t.Fatalf("ParseWhereFlag(%q): expected error for blank table/field, got nil", tc.in)
			}
			if !strings.Contains(err.Error(), "non-empty") {
				t.Fatalf("ParseWhereFlag(%q): error %q should mention non-empty", tc.in, err)
			}
		})
	}
}

func TestParseLimitFlag(t *testing.T) {
	cases := []struct {
		in        string
		wantTable string
		wantN     int
		wantErr   bool
	}{
		{"Customer:1000", "Customer", 1000, false},
		{"  Customer : 1000 ", "Customer", 1000, false},
		{"Customer:0", "", 0, true},
		{"Customer:-5", "", 0, true},
		{"Customer:abc", "", 0, true},
		{"Customer", "", 0, true},
		{":1000", "", 0, true},
		{"", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			table, n, err := ParseLimitFlag(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseLimitFlag(%q) err=%v wantErr=%v", tc.in, err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if table != tc.wantTable || n != tc.wantN {
				t.Fatalf("ParseLimitFlag(%q) = (%q, %d), want (%q, %d)",
					tc.in, table, n, tc.wantTable, tc.wantN)
			}
		})
	}
}
