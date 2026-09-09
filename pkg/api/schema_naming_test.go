package api

import "testing"

// TestPolicyCollectionName covers S101 Fix 1's exact required cases:
// main.Customer -> /Customer matches (via stripping to "Customer");
// Customer -> matches (already bare, unchanged); archive.Customer -> no
// match unless the policy says so (a NON-default schema is never
// stripped, so it keeps its own distinct policy identity).
func TestPolicyCollectionName(t *testing.T) {
	tests := []struct {
		name         string
		physicalName string
		driver       string
		want         string
	}{
		{"sqlite3 default schema is stripped", "main.Customer", "sqlite3", "Customer"},
		{"sqlite alias also strips the default schema", "main.Customer", "sqlite", "Customer"},
		{"already-bare name is unchanged", "Customer", "sqlite3", "Customer"},
		{"non-default schema stays qualified", "archive.Customer", "sqlite3", "archive.Customer"},
		{"default-schema match is case-insensitive", "MAIN.Customer", "sqlite3", "Customer"},
		{"postgres default schema is stripped", "public.customers", "postgres", "customers"},
		{"postgres non-default schema stays qualified", "reporting.customers", "postgres", "reporting.customers"},
		{"a driver with no default-schema convention never strips anything", "main.Customer", "ingitdb", "main.Customer"},
		{"an empty name stays empty", "", "sqlite3", ""},
		{"a name equal to just the schema (no dot-suffix) is not treated as qualified", "main", "sqlite3", "main"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := PolicyCollectionName(tc.physicalName, tc.driver)
			if got != tc.want {
				t.Errorf("PolicyCollectionName(%q, %q) = %q, want %q", tc.physicalName, tc.driver, got, tc.want)
			}
		})
	}
}
