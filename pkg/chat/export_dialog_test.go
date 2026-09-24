package chat

import "testing"

func TestExportFileStem(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"replaces path and control characters", "a/b\\c.d\x01e", "a-b-c-d-e"},
		{"trims surrounding dots dashes and spaces", "  .-report-.  ", "report"},
		{"empty input falls back to recordset", "", "recordset"},
		{"dot only input falls back to recordset", ".", "recordset"},
		{"dot dot input falls back to recordset", "..", "recordset"},
		{"plain name is kept as-is", "Orders 2024", "Orders 2024"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := exportFileStem(tc.in); got != tc.want {
				t.Fatalf("exportFileStem(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
