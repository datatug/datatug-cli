package httpsource

import "testing"

func TestInferRowsPath(t *testing.T) {
	cases := []struct {
		name    string
		sample  map[string]any
		columns []string
		want    string
	}{
		{
			name:    "root already matches declared columns",
			sample:  map[string]any{"amount": 1.0, "base": "USD", "date": "2026-09-08", "rates": map[string]any{"CAD": 1.38}},
			columns: []string{"amount", "base", "date", "rates"},
			want:    "",
		},
		{
			name:    "wrapped under data",
			sample:  map[string]any{"error": false, "msg": "ok", "data": map[string]any{"name": "Canada", "currency": "CAD"}},
			columns: []string{"name", "currency"},
			want:    "data",
		},
		{
			name:    "no sample",
			sample:  nil,
			columns: []string{"name"},
			want:    "",
		},
		{
			name:    "no declared columns",
			sample:  map[string]any{"data": map[string]any{"name": "Canada"}},
			columns: nil,
			want:    "",
		},
		{
			name:    "no match anywhere falls back to root",
			sample:  map[string]any{"error": false, "data": map[string]any{"unrelated": true}},
			columns: []string{"name"},
			want:    "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := inferRowsPath(tc.sample, tc.columns); got != tc.want {
				t.Fatalf("inferRowsPath() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInferKeyField(t *testing.T) {
	cases := []struct {
		name    string
		row     map[string]any
		paramID string
		columns []string
		want    string
	}{
		{
			name:    "parameter echoed back in row",
			row:     map[string]any{"name": "Canada", "currency": "CAD"},
			paramID: "name",
			columns: []string{"name", "currency"},
			want:    "name",
		},
		{
			name:    "parameter not in row falls back to first present column",
			row:     map[string]any{"amount": 1.0, "base": "USD", "date": "2026-09-08", "rates": map[string]any{"CAD": 1.38}},
			paramID: "to",
			columns: []string{"amount", "base", "date", "rates"},
			want:    "amount",
		},
		{
			name:    "no row falls back to paramID",
			row:     nil,
			paramID: "name",
			columns: []string{"name"},
			want:    "name",
		},
		{
			name:    "neither parameter nor any column present falls back to paramID",
			row:     map[string]any{"unrelated": true},
			paramID: "to",
			columns: []string{"amount"},
			want:    "to",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := inferKeyField(tc.row, tc.paramID, tc.columns); got != tc.want {
				t.Fatalf("inferKeyField() = %q, want %q", got, tc.want)
			}
		})
	}
}
