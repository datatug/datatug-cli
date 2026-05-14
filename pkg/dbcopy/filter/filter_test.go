package filter

import "testing"

func TestDirectives_IsEmpty(t *testing.T) {
	cases := []struct {
		name string
		d    *Directives
		want bool
	}{
		{"nil", nil, true},
		{"zero-value", &Directives{}, true},
		{"include set", &Directives{IncludeTables: []string{"users"}}, false},
		{"exclude set", &Directives{ExcludeTables: []string{"logs"}}, false},
		{"limit set", &Directives{LimitsByTable: map[string]int{"users": 5}}, false},
		{"where set", &Directives{Where: map[string]*PredicateGroup{"users": {}}}, false},
		{"column rule set", &Directives{PerTableColumns: map[string]*ColumnRule{"users": {}}}, false},
		{"global exclude set", &Directives{GlobalExcludeColumns: []string{"created_at"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.d.IsEmpty(); got != tc.want {
				t.Fatalf("IsEmpty()=%v want %v", got, tc.want)
			}
		})
	}
}
