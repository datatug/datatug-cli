package chat

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

func TestNormalizeGridTitle(t *testing.T) {
	if got := normalizeGridTitle("  latest\n  orders "); got != "latest orders" {
		t.Fatalf("normalized title = %q", got)
	}
	if got := normalizeGridTitle(""); got != "Query result" {
		t.Fatalf("empty title fallback = %q", got)
	}
	if got := normalizeGridTitle(strings.Repeat("x", maxGridTitleWidth+10)); ansi.StringWidth(got) > maxGridTitleWidth || !strings.HasSuffix(got, "…") || strings.Count(got, "…") != 1 {
		t.Fatalf("long title = %q (width %d)", got, ansi.StringWidth(got))
	}
	if got := normalizeGridTitle("\x1b[31mPrague\x1b[0m\norders\t"); got != "Prague orders" {
		t.Fatalf("unsafe title = %q", got)
	}
	if got := normalizeGridTitle("界界界界界界界界界界界界界界界界界界界界界界界界界界界界界界界界"); ansi.StringWidth(got) > maxGridTitleWidth {
		t.Fatalf("wide title width = %d, value %q", ansi.StringWidth(got), got)
	}
}

func TestNewGridModelPreservesStructureAndFormatsValues(t *testing.T) {
	when := time.Date(2026, 9, 20, 12, 30, 0, 0, time.UTC)
	result := secureread.Result{
		Columns: []string{"id", "name", "amount", "when", "empty", "blob"},
		Rows: []secureread.Row{{Data: map[string]any{
			"id": 2, "name": "Prague", "amount": 12.50, "when": when, "empty": nil, "blob": []byte{0xff},
		}}},
	}
	grid := NewGridModel(result)
	if len(grid.Columns) != 6 || !grid.Columns[0].Numeric || !grid.Columns[2].Numeric || grid.Columns[1].Numeric {
		t.Fatalf("columns = %+v", grid.Columns)
	}
	want := []string{"2", "Prague", "12.5", "2026-09-20T12:30:00Z", "NULL", "0xff"}
	for i := range want {
		if grid.Rows[0][i] != want[i] {
			t.Errorf("cell %d = %q, want %q", i, grid.Rows[0][i], want[i])
		}
	}
	if len(grid.RawRows) != 1 || grid.RawRows[0][0] != 2 || grid.RawRows[0][3] != when {
		t.Fatalf("raw values were not preserved: %+v", grid.RawRows)
	}
	if got := FormatValue(bytes.Repeat([]byte("a"), 2)); got != "aa" {
		t.Errorf("UTF-8 bytes = %q", got)
	}
}

func TestNewGridModelCompactsMidnightValuesInDateColumns(t *testing.T) {
	midnight := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	noon := time.Date(2026, 9, 20, 12, 30, 0, 0, time.UTC)
	grid := NewGridModel(secureread.Result{
		Columns: []string{"InvoiceDate", "ImportedDate", "ChangedDate", "UpdatedAt"},
		Rows: []secureread.Row{{Data: map[string]any{
			"InvoiceDate":  midnight,
			"ImportedDate": "2026-09-20T00:00:00Z",
			"ChangedDate":  noon,
			"UpdatedAt":    midnight,
		}}},
	})
	want := []string{"2026-09-20", "2026-09-20", "2026-09-20T12:30:00Z", "2026-09-20T00:00:00Z"}
	for column, expected := range want {
		if got := grid.Rows[0][column]; got != expected {
			t.Errorf("column %s = %q, want %q", grid.Columns[column].Name, got, expected)
		}
	}
}

func TestGridModelSanitizesDisplayTextButPreservesRawValues(t *testing.T) {
	rawHeader := "\x1b]0;title\aCustomer\u202e"
	rawCell := "\x1b[31m東京\x1b[0m\n\u202evisible"
	rawBytes := []byte("Cafe\t\u200d\U0001f469")
	grid := NewGridModel(secureread.Result{
		Columns: []string{rawHeader, "Name"},
		Rows:    []secureread.Row{{Data: map[string]any{rawHeader: rawCell, "Name": rawBytes}}},
	})
	if strings.ContainsAny(grid.Columns[0].Name, "\x1b\n\r\t") || strings.Contains(grid.Columns[0].Name, "\u202e") {
		t.Fatalf("unsafe header survived: %q", grid.Columns[0].Name)
	}
	if strings.ContainsAny(grid.Rows[0][0], "\x1b\n\r\t") || strings.Contains(grid.Rows[0][0], "\u202e") {
		t.Fatalf("unsafe string cell survived: %q", grid.Rows[0][0])
	}
	if !strings.Contains(grid.Rows[0][0], "東京") || !strings.Contains(grid.Rows[0][1], "Cafe") {
		t.Fatalf("ordinary international text was lost: %+v", grid.Rows)
	}
	if got := grid.RawRows[0][0]; got != rawCell {
		t.Fatalf("raw string changed: %#v", got)
	}
	if got := grid.RawRows[0][1]; string(got.([]byte)) != string(rawBytes) {
		t.Fatalf("raw bytes changed: %#v", got)
	}
}

// GridModel no longer sorts (nor tracks a sort column/header arrow, nor a
// parallel-sorted RawRows/SourceRows) — it is pure presentation data now;
// sorting is strongo/aichat's tui/grid.Model's, exercised via
// newGridState/newProjectedGridState (see ui.go) and gridState wrapping
// *grid.Model. That algorithm's own coverage (stable sort, numeric-aware via
// big.Rat, large-integer exactness, toggling asc/desc) lives with it in
// tui/grid's test suite (TestSortTogglesAscDesc and friends), replacing
// what were TestGridModelSortTogglesAndHandlesEmpty,
// TestGridModelNumericSortKeepsEqualValuesStable,
// TestGridModelSortsLargeIntegersExactly and
// TestGridModelSortHandlesPartialRawRows here. gridState's own stable-
// selection-across-sort behaviour (Row.Key/sourceRowKey/restoreByKey) is
// covered by TestGridSortRetainsSelectedSourceRowForInspector in
// ui_test.go.
