package filter

import (
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
)

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

// TestPreValidate_PerTableColumns covers the PerTableColumns mutex loop.
func TestPreValidate_PerTableColumns(t *testing.T) {
	t.Run("nil rule is skipped", func(t *testing.T) {
		d := &Directives{PerTableColumns: map[string]*ColumnRule{"t": nil}}
		if err := d.PreValidate(); err != nil {
			t.Fatalf("nil rule should be skipped, got %v", err)
		}
	})
	t.Run("both include and exclude on same table errors", func(t *testing.T) {
		d := &Directives{PerTableColumns: map[string]*ColumnRule{
			"t": {Include: []string{"a"}, Exclude: []string{"b"}},
		}}
		err := d.PreValidate()
		if err == nil {
			t.Fatal("expected mutex error, got nil")
		}
		if !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("error %q should mention mutually exclusive", err)
		}
	})
	t.Run("include only is ok", func(t *testing.T) {
		d := &Directives{PerTableColumns: map[string]*ColumnRule{
			"t": {Include: []string{"a"}},
		}}
		if err := d.PreValidate(); err != nil {
			t.Fatalf("include-only rule should not error, got %v", err)
		}
	})
}

func refs(names ...string) []dal.CollectionRef {
	out := make([]dal.CollectionRef, len(names))
	for i, n := range names {
		out[i] = dal.NewRootCollectionRef(n, "")
	}
	return out
}

// TestApplyTableFilter covers all branches of ApplyTableFilter.
func TestApplyTableFilter(t *testing.T) {
	t.Run("empty directives fast-path", func(t *testing.T) {
		in := refs("users", "orders")
		got, err := ApplyTableFilter(in, &Directives{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d refs, want 2", len(got))
		}
	})

	t.Run("include known tables preserves source order", func(t *testing.T) {
		in := refs("orders", "users", "logs")
		d := &Directives{IncludeTables: []string{"users", "orders"}}
		got, err := ApplyTableFilter(in, d)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d refs, want 2", len(got))
		}
		// source order: orders first, users second
		if got[0].Name() != "orders" || got[1].Name() != "users" {
			t.Fatalf("order wrong: got %v/%v", got[0].Name(), got[1].Name())
		}
	})

	t.Run("include missing table errors", func(t *testing.T) {
		in := refs("users")
		d := &Directives{IncludeTables: []string{"missing"}}
		_, err := ApplyTableFilter(in, d)
		if err == nil {
			t.Fatal("expected error for missing table in --include")
		}
		if !strings.Contains(err.Error(), "--include") {
			t.Fatalf("error %q should mention --include", err)
		}
	})

	t.Run("exclude known tables", func(t *testing.T) {
		in := refs("users", "logs", "orders")
		d := &Directives{ExcludeTables: []string{"logs"}}
		got, err := ApplyTableFilter(in, d)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d refs, want 2", len(got))
		}
	})

	t.Run("exclude missing table errors", func(t *testing.T) {
		in := refs("users")
		d := &Directives{ExcludeTables: []string{"missing"}}
		_, err := ApplyTableFilter(in, d)
		if err == nil {
			t.Fatal("expected error for missing table in --exclude")
		}
		if !strings.Contains(err.Error(), "--exclude") {
			t.Fatalf("error %q should mention --exclude", err)
		}
	})

	t.Run("no include/exclude with non-empty directives returns all", func(t *testing.T) {
		in := refs("users", "orders")
		d := &Directives{LimitsByTable: map[string]int{"users": 10}}
		got, err := ApplyTableFilter(in, d)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d refs, want 2", len(got))
		}
	})
}
