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
