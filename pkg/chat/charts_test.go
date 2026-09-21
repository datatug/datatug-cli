package chat

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

func TestInferChartCandidatesRanksSemanticCategoriesAndOrderedLines(t *testing.T) {
	stats := secureread.RecordSetStatistics{RowCount: 100, Columns: []secureread.ColumnStatistics{
		{Name: "InvoiceId", NonNullCount: 100, Cardinality: 100, Types: secureread.TypeObservations{Number: 100}, Frequencies: []secureread.ValueFrequency{{Label: "1", Count: 1}}},
		{Name: "Country", NonNullCount: 100, Cardinality: 40, Types: secureread.TypeObservations{String: 100}, Frequencies: []secureread.ValueFrequency{{Label: "Brazil", Count: 10}, {Label: "USA", Count: 15}}},
		{Name: "IsActive", NonNullCount: 100, Cardinality: 2, Types: secureread.TypeObservations{Boolean: 100}, Frequencies: []secureread.ValueFrequency{{Label: "false", Count: 30}, {Label: "true", Count: 70}}},
		{Name: "InvoiceDate", NonNullCount: 100, Cardinality: 2, Types: secureread.TypeObservations{Datetime: 100}, DateBuckets: []secureread.DateBucket{{Bucket: "2013-01-01", Count: 40}, {Bucket: "2013-01-02", Count: 60}}},
		{Name: "Total", NonNullCount: 100, Cardinality: 20, Types: secureread.TypeObservations{Number: 100}},
	}, DateNumericSums: []secureread.DateNumericSum{{DateColumn: "InvoiceDate", NumericColumn: "Total", Buckets: []secureread.DateNumericBucket{{Bucket: "2013-01-01", Sum: 40}, {Bucket: "2013-01-02", Sum: 80}}}}}
	first := InferChartCandidates(stats)
	second := InferChartCandidates(stats)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("candidate ranking is not deterministic")
	}
	if len(first) < 4 {
		t.Fatalf("got %d candidates, want line sum/count plus categories", len(first))
	}
	if first[0].Spec.Kind != ChartLine || first[0].Spec.Aggregation != "sum" || first[0].Spec.Dimension != "InvoiceDate" || first[0].Spec.Measure != "Total" {
		t.Fatalf("top candidate = %+v, want date+sum line", first[0])
	}
	var country, invoiceID, boolean *ChartCandidate
	for i := range first {
		switch first[i].Spec.Dimension {
		case "Country":
			country = &first[i]
		case "InvoiceId":
			invoiceID = &first[i]
		case "IsActive":
			boolean = &first[i]
		}
	}
	if country == nil || invoiceID == nil || boolean == nil || country.Score <= boolean.Score || boolean.Score <= invoiceID.Score {
		t.Fatalf("semantic category did not outrank boolean/ID: country=%+v boolean=%+v ID=%+v", country, boolean, invoiceID)
	}
}

func TestCategoricalTopTenAndDeterministicTies(t *testing.T) {
	frequencies := make([]secureread.ValueFrequency, 12)
	for i := range frequencies {
		frequencies[i] = secureread.ValueFrequency{Label: string(rune('A' + i)), Count: 2}
	}
	stats := secureread.RecordSetStatistics{RowCount: 30, Columns: []secureread.ColumnStatistics{{Name: "Country", NonNullCount: 24, NullCount: 6, Cardinality: 12, Types: secureread.TypeObservations{String: 24}, Frequencies: frequencies}}}
	candidates := InferChartCandidates(stats)
	if len(candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(candidates))
	}
	points := candidates[0].Spec.Points
	if len(points) != 10 || points[0].Label != "∅ NULL" || points[0].Value != 6 || points[1].Label != "A" || points[9].Label != "I" {
		t.Fatalf("Top 10/ties = %+v", points)
	}
	if candidates[0].Spec.Limit != 10 || candidates[0].Spec.Ordering != "count desc, label asc" {
		t.Fatalf("chart metadata = %+v", candidates[0].Spec)
	}
}

func TestDateLineBucketsToMonthsDeterministically(t *testing.T) {
	buckets := make([]secureread.DateBucket, 61)
	for i := range buckets {
		month, day := 1, i+1
		if i >= 31 {
			month, day = 3, i-30
		}
		buckets[i] = secureread.DateBucket{Bucket: dateLabel(2013, month, day), Count: 1}
	}
	stats := secureread.RecordSetStatistics{RowCount: 61, Columns: []secureread.ColumnStatistics{{Name: "InvoiceDate", NonNullCount: 61, Cardinality: 61, Types: secureread.TypeObservations{Date: 61}, DateBuckets: buckets}}}
	candidates := InferChartCandidates(stats)
	if len(candidates) != 1 || candidates[0].Spec.Bucket != "month" || len(candidates[0].Spec.Points) != 2 {
		t.Fatalf("monthly line = %+v", candidates)
	}
}

func dateLabel(year, month, day int) string {
	return fmt.Sprintf("%04d-%02d-%02d", year, month, day)
}

func TestIncompleteFrequenciesNeverClaimExactTopTen(t *testing.T) {
	stats := secureread.RecordSetStatistics{RowCount: 5000, Columns: []secureread.ColumnStatistics{{Name: "Country", NonNullCount: 5000, Cardinality: 4096, CardinalityIncomplete: true, FrequenciesIncomplete: true, Frequencies: []secureread.ValueFrequency{{Label: "USA", Count: 10}}}}}
	if got := InferChartCandidates(stats); len(got) != 0 {
		t.Fatalf("incomplete data generated false exact chart: %+v", got)
	}
}

func TestChartInferenceEdgeCases(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stats secureread.RecordSetStatistics
		want  int
	}{
		{name: "empty", stats: secureread.RecordSetStatistics{}, want: 0},
		{name: "one row", stats: secureread.RecordSetStatistics{RowCount: 1, Columns: []secureread.ColumnStatistics{{Name: "Country", NonNullCount: 1, Cardinality: 1, Frequencies: []secureread.ValueFrequency{{Label: "USA", Count: 1}}}}}, want: 0},
		{name: "all null", stats: secureread.RecordSetStatistics{RowCount: 10, Columns: []secureread.ColumnStatistics{{Name: "Country", NullCount: 10}}}, want: 0},
		{name: "constant", stats: secureread.RecordSetStatistics{RowCount: 10, Columns: []secureread.ColumnStatistics{{Name: "Country", NonNullCount: 10, Cardinality: 1, Frequencies: []secureread.ValueFrequency{{Label: "USA", Count: 10}}}}}, want: 1},
		{name: "two values", stats: secureread.RecordSetStatistics{RowCount: 10, Columns: []secureread.ColumnStatistics{{Name: "Country", NonNullCount: 10, Cardinality: 2, Frequencies: []secureread.ValueFrequency{{Label: "USA", Count: 6}, {Label: "Canada", Count: 4}}}}}, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(InferChartCandidates(tc.stats)); got != tc.want {
				t.Fatalf("candidate count = %d, want %d", got, tc.want)
			}
		})
	}
	mostlyNull := secureread.RecordSetStatistics{RowCount: 100, Columns: []secureread.ColumnStatistics{
		{Name: "City", NonNullCount: 10, NullCount: 90, Cardinality: 2, Frequencies: []secureread.ValueFrequency{{Label: "Prague", Count: 5}, {Label: "Paris", Count: 5}}},
		{Name: "Country", NonNullCount: 100, Cardinality: 2, Frequencies: []secureread.ValueFrequency{{Label: "Czechia", Count: 50}, {Label: "France", Count: 50}}},
	}}
	candidates := InferChartCandidates(mostlyNull)
	if len(candidates) != 2 || candidates[0].Spec.Dimension != "Country" {
		t.Fatalf("null-heavy dimension incorrectly outranked complete one: %+v", candidates)
	}
}

func TestNTChartsAdapterRendersSupportedSpecs(t *testing.T) {
	for _, spec := range []ChartSpec{
		{Kind: ChartBar, Measure: "Rows", Points: []ChartPoint{{Label: "Brazil", Value: 5}, {Label: "USA", Value: 3}}},
		{Kind: ChartLine, Bucket: "day", Points: []ChartPoint{{Label: "2013-01-01", Value: 5}, {Label: "2013-01-02", Value: 3}}},
	} {
		view := renderChart(spec, 48, 12)
		if view == "" || strings.Contains(view, "not available") || strings.Contains(view, "could not") {
			t.Errorf("kind %s did not render: %q", spec.Kind, view)
		}
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > 48 {
				t.Errorf("kind %s exceeded pane width: %q", spec.Kind, line)
			}
		}
	}
	if got := renderChart(ChartSpec{Kind: ChartPie, Points: []ChartPoint{{Label: "A", Value: 1}}}, 48, 12); !strings.Contains(got, "not available") {
		t.Errorf("unsupported kind fallback = %q", got)
	}
}

func TestNTChartsAdapterKeepsHistoricalLineRangeAndBarLabels(t *testing.T) {
	historical := renderChart(ChartSpec{
		Kind:   ChartLine,
		Bucket: "day",
		Points: []ChartPoint{{Label: "2013-01-01", Value: 5}, {Label: "2013-02-01", Value: 9}},
	}, 64, 12)
	if !strings.Contains(historical, "'13") {
		t.Fatalf("historical line omitted its data year:\n%s", historical)
	}
	if currentYear := time.Now().UTC().Format("'06"); strings.Contains(historical, currentYear) {
		t.Fatalf("historical line used current-year axis %q:\n%s", currentYear, historical)
	}

	bar := ansi.Strip(renderChart(ChartSpec{
		Kind:    ChartBar,
		Measure: "Rows",
		Points:  []ChartPoint{{Label: "Brazil", Value: 5}, {Label: "USA", Value: 3}},
	}, 32, 8))
	for _, label := range []string{"Brazil", "USA"} {
		if !strings.Contains(bar, label) {
			t.Errorf("horizontal bar omitted %q:\n%s", label, bar)
		}
	}
}

func TestChinookInvoiceChartsUseRealDALgoRows(t *testing.T) {
	path, err := filepath.Abs(filepath.Join("..", "dbcopy", "testdata", "chinook.db"))
	if err != nil {
		t.Fatal(err)
	}
	doc := []byte("from: {name: Invoice}\ncolumns:\n  - field: InvoiceDate\n  - field: Total\norderBy:\n  - field: InvoiceId\n    desc: true\nlimit: 100\n")
	result, err := secureread.NewExecutor(secureread.Session{Unrestricted: true}).RunDTQL(context.Background(), "sqlite://"+path, doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 100 || result.Statistics.RowCount != 100 {
		t.Fatalf("Chinook rows/stats = %d/%d, want 100/100", len(result.Rows), result.Statistics.RowCount)
	}
	candidates := InferChartCandidates(result.Statistics)
	var countLine, sumLine bool
	for _, candidate := range candidates {
		if candidate.Spec.Kind != ChartLine || candidate.Spec.Dimension != "InvoiceDate" {
			continue
		}
		if candidate.Spec.Aggregation == "count" {
			countLine = true
		}
		if candidate.Spec.Aggregation == "sum" && candidate.Spec.Measure == "Total" {
			sumLine = true
		}
	}
	if !countLine || !sumLine {
		t.Fatalf("real invoice date/count and date/sum lines missing: %+v", candidates)
	}
}
