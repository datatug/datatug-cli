package secureread

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
)

func TestStatisticsAccumulatorUpdatesAsRowsArrive(t *testing.T) {
	a := newStatisticsAccumulator()
	a.addRow(map[string]any{"country": "IE", "active": true, "amount": 2.5})
	a.addRow(map[string]any{"country": "GB", "active": false, "amount": 3.5})
	a.addRow(map[string]any{"country": "IE", "active": nil})
	a.addRow(map[string]any{"active": true}) // absent values are nulls too.
	stats := a.finalize([]string{"country", "active", "amount", "never"})
	if stats.RowCount != 4 {
		t.Fatalf("row count = %d", stats.RowCount)
	}
	country := statisticsColumn(t, stats, "country")
	if country.NullCount != 1 || country.NonNullCount != 3 || country.Cardinality != 3 {
		t.Fatalf("country = %+v", country)
	}
	if got := frequencyCount(country, ValueKindString, "IE"); got != 2 {
		t.Fatalf("IE frequency = %d", got)
	}
	if got := frequencyCount(country, ValueKindNull, "(null)"); got != 1 {
		t.Fatalf("null frequency = %d", got)
	}
	active := statisticsColumn(t, stats, "active")
	if active.Types.Boolean != 3 || active.Types.Null != 1 {
		t.Fatalf("active type observations = %+v", active.Types)
	}
	amount := statisticsColumn(t, stats, "amount")
	if amount.NullCount != 2 || amount.Types.Number != 2 {
		t.Fatalf("amount = %+v", amount)
	}
	never := statisticsColumn(t, stats, "never")
	if never.NullCount != 4 || never.Cardinality != 1 {
		t.Fatalf("missing column = %+v", never)
	}
}

func TestStatisticsCapsDistinctValuesExplicitly(t *testing.T) {
	rows := make([]Row, MaxStatisticDistinctValues+1)
	for i := range rows {
		rows[i] = Row{Data: map[string]any{"value": fmt.Sprintf("value-%03d", i)}}
	}
	column := statisticsColumn(t, StatisticsForRows([]string{"value"}, rows), "value")
	if !column.CardinalityIncomplete || !column.FrequenciesIncomplete {
		t.Fatalf("cap was not explicit: %+v", column)
	}
	if column.Cardinality != MaxStatisticDistinctValues || len(column.Frequencies) != MaxStatisticDistinctValues {
		t.Fatalf("retained distinct values = %d / %d", column.Cardinality, len(column.Frequencies))
	}
}

func TestStatisticsCappedDateNumericPairsAreDeterministic(t *testing.T) {
	values := make(map[string]any)
	for dateIndex := range 9 {
		values[fmt.Sprintf("date%02d", dateIndex)] = "2026-09-21"
	}
	for numberIndex := range 8 {
		values[fmt.Sprintf("number%02d", numberIndex)] = numberIndex
	}
	var first []DateNumericSum
	for attempt := range 12 {
		stats := StatisticsForRows(nil, []Row{{Data: values}})
		if !stats.DateNumericSumsIncomplete || len(stats.DateNumericSums) != MaxStatisticDateNumericPairs {
			t.Fatalf("attempt %d: capped pairs = %+v", attempt, stats.DateNumericSums)
		}
		if attempt == 0 {
			first = stats.DateNumericSums
		} else if !reflect.DeepEqual(first, stats.DateNumericSums) {
			t.Fatalf("attempt %d retained different pairs", attempt)
		}
	}
	if first[0].DateColumn != "date00" || first[0].NumericColumn != "number00" || first[len(first)-1].DateColumn != "date07" || first[len(first)-1].NumericColumn != "number07" {
		t.Fatalf("retained pair order = first %+v, last %+v", first[0], first[len(first)-1])
	}
}

func TestStatisticsKeepsWideIntegerIdentitiesAndSafeSums(t *testing.T) {
	const first int64 = 9007199254740992
	const second int64 = first + 1
	stats := StatisticsForRows([]string{"signed", "unsigned", "when", "amount"}, []Row{
		{Data: map[string]any{"signed": first, "unsigned": uint64(first), "when": time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC), "amount": math.MaxFloat64}},
		{Data: map[string]any{"signed": second, "unsigned": uint64(second), "when": time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC), "amount": math.MaxFloat64}},
		{Data: map[string]any{"signed": second, "unsigned": uint64(second), "when": time.Date(2026, 9, 21, 11, 0, 0, 0, time.UTC), "amount": 1.0}},
	})
	for _, name := range []string{"signed", "unsigned"} {
		column := statisticsColumn(t, stats, name)
		if column.Cardinality != 2 || frequencyCount(column, ValueKindNumber, fmt.Sprint(first)) != 1 || frequencyCount(column, ValueKindNumber, fmt.Sprint(second)) != 2 {
			t.Fatalf("%s wide integer statistics = %+v", name, column)
		}
	}
	when := statisticsColumn(t, stats, "when")
	if len(when.DateBuckets) != 2 || when.DateBuckets[0] != (DateBucket{Bucket: "2026-09-21T10:00:00Z", Count: 2}) || when.DateBuckets[1] != (DateBucket{Bucket: "2026-09-21T11:00:00Z", Count: 1}) {
		t.Fatalf("datetime buckets = %+v", when.DateBuckets)
	}
	var amountSums *DateNumericSum
	for i := range stats.DateNumericSums {
		if stats.DateNumericSums[i].DateColumn == "when" && stats.DateNumericSums[i].NumericColumn == "amount" {
			amountSums = &stats.DateNumericSums[i]
			break
		}
	}
	if amountSums == nil || !amountSums.Incomplete || math.IsInf(amountSums.Buckets[0].Sum, 0) {
		t.Fatalf("overflowing sums = %+v", stats.DateNumericSums)
	}
	if _, err := json.Marshal(stats); err != nil {
		t.Fatalf("overflow-safe statistics do not encode: %v", err)
	}
}

func TestStatisticsFromRecordsetKeepsTypedDateAndDecimalEvidence(t *testing.T) {
	recordset := apicontract.Recordset{
		Columns: []apicontract.Column{{Name: "day", Type: "date"}, {Name: "total", Type: "decimal"}, {Name: "paid", Type: "boolean"}},
		Rows: [][]apicontract.TypedValue{
			{apicontract.NewDateValue("2026-09-20"), apicontract.NewDecimalValue("1.25"), apicontract.NewBooleanValue(true)},
			{apicontract.NewDateValue("2026-09-20"), apicontract.NewDecimalValue("2.75"), apicontract.NewBooleanValue(false)},
			{apicontract.NewDateValue("2026-09-21"), apicontract.NewDecimalValue("3.00"), apicontract.NewNullValue()},
		},
	}
	stats := StatisticsFromRecordset(recordset)
	day := statisticsColumn(t, stats, "day")
	if day.Types.Date != 3 || len(day.DateBuckets) != 2 || day.DateBuckets[0] != (DateBucket{Bucket: "2026-09-20", Count: 2}) {
		t.Fatalf("date stats = %+v", day)
	}
	total := statisticsColumn(t, stats, "total")
	if total.Types.Decimal != 3 || total.Types.Number != 0 {
		t.Fatalf("decimal type evidence = %+v", total.Types)
	}
	paid := statisticsColumn(t, stats, "paid")
	if paid.Types.Boolean != 2 || paid.Types.Null != 1 {
		t.Fatalf("boolean type evidence = %+v", paid.Types)
	}
	if len(stats.DateNumericSums) != 1 || stats.DateNumericSums[0].DateColumn != "day" || stats.DateNumericSums[0].NumericColumn != "total" {
		t.Fatalf("date numeric sums = %+v", stats.DateNumericSums)
	}
	buckets := stats.DateNumericSums[0].Buckets
	if len(buckets) != 2 || buckets[0].Bucket != "2026-09-20" || buckets[0].Count != 2 || buckets[0].Sum != 4 {
		t.Fatalf("date numeric buckets = %+v", buckets)
	}

	// Datetime buckets retain UTC instant precision for timestamp lines.
	withDatetime := StatisticsForRows([]string{"when"}, []Row{{Data: map[string]any{"when": time.Date(2026, 9, 21, 1, 0, 0, 0, time.FixedZone("east", 2*60*60))}}})
	when := statisticsColumn(t, withDatetime, "when")
	if when.Types.Datetime != 1 || when.DateBuckets[0].Bucket != "2026-09-20T23:00:00Z" {
		t.Fatalf("datetime stats = %+v", when)
	}

	// SQLite/DALgo-backed Chinook InvoiceDate values can be decoded as strings.
	// These are deliberately recognised only when they are exact ISO dates or
	// RFC3339 datetimes, never from a column-name heuristic.
	chinookStyle := StatisticsForRows([]string{"InvoiceDate", "Total"}, []Row{
		{Data: map[string]any{"InvoiceDate": "2013-12-22", "Total": 4.5}},
		{Data: map[string]any{"InvoiceDate": "2013-12-23T01:00:00+01:00", "Total": 5.5}},
	})
	invoiceDate := statisticsColumn(t, chinookStyle, "InvoiceDate")
	if invoiceDate.Types.Date != 1 || invoiceDate.Types.Datetime != 1 || len(invoiceDate.DateBuckets) != 2 || invoiceDate.DateBuckets[0] != (DateBucket{Bucket: "2013-12-22", Count: 1}) || invoiceDate.DateBuckets[1] != (DateBucket{Bucket: "2013-12-23T00:00:00Z", Count: 1}) {
		t.Fatalf("Chinook-style date string stats = %+v", invoiceDate)
	}
	if len(chinookStyle.DateNumericSums) != 1 || len(chinookStyle.DateNumericSums[0].Buckets) != 2 || chinookStyle.DateNumericSums[0].Buckets[0].Sum != 4.5 || chinookStyle.DateNumericSums[0].Buckets[1].Sum != 5.5 {
		t.Fatalf("Chinook-style date numeric sums = %+v", chinookStyle.DateNumericSums)
	}
}

func statisticsColumn(t *testing.T, stats RecordSetStatistics, name string) ColumnStatistics {
	t.Helper()
	for _, column := range stats.Columns {
		if column.Name == name {
			return column
		}
	}
	t.Fatalf("missing statistics column %q in %+v", name, stats.Columns)
	return ColumnStatistics{}
}

func frequencyCount(column ColumnStatistics, kind ValueKind, label string) int {
	for _, frequency := range column.Frequencies {
		if frequency.Type == kind && frequency.Label == label {
			return frequency.Count
		}
	}
	return 0
}
