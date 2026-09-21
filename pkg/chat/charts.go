package chat

import (
	"fmt"
	"sort"
	"strings"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// ChartKind is independent of the current terminal renderer. Pie and donut
// are reserved for future adapters; Phase 6 renders bar and line only.
type ChartKind string

const (
	ChartBar     ChartKind = "bar"
	ChartLine    ChartKind = "line"
	ChartScatter ChartKind = "scatter"
	ChartPie     ChartKind = "pie"
	ChartDonut   ChartKind = "donut"
)

// ChartPoint is one already-aggregated, ordered value in a ChartSpec.
type ChartPoint struct {
	Label string
	Value float64
}

// ChartSpec carries chart meaning and already-aggregated points, but no
// NTCharts, Bubble Tea, terminal width or color types.
type ChartSpec struct {
	Kind          ChartKind
	Title         string
	Dimension     string
	Measure       string
	Aggregation   string
	Ordering      string
	Limit         int
	SourceColumns []string
	Labels        []string
	Bucket        string
	Points        []ChartPoint
}

// ChartCandidate records a stable ranking score and a human-readable reason.
type ChartCandidate struct {
	Spec   ChartSpec
	Score  int
	Reason string
}

// InferChartCandidates is pure and deterministic. It uses only the finalized
// RecordSet analysis, never database access, model text, or terminal state.
func InferChartCandidates(stats secureread.RecordSetStatistics) []ChartCandidate {
	if stats.RowCount < 2 {
		return nil
	}
	var candidates []ChartCandidate
	for _, column := range stats.Columns {
		if candidate, ok := categoryCandidate(stats.RowCount, column); ok {
			candidates = append(candidates, candidate)
		}
		if candidate, ok := countLineCandidate(column); ok {
			candidates = append(candidates, candidate)
		}
	}
	for _, sums := range stats.DateNumericSums {
		if candidate, ok := sumLineCandidate(stats, sums); ok {
			candidates = append(candidates, candidate)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.Score != right.Score {
			return left.Score > right.Score
		}
		if left.Spec.Title != right.Spec.Title {
			return left.Spec.Title < right.Spec.Title
		}
		if left.Spec.Kind != right.Spec.Kind {
			return left.Spec.Kind < right.Spec.Kind
		}
		return left.Spec.Dimension < right.Spec.Dimension
	})
	return candidates
}

func categoryCandidate(rowCount int, column secureread.ColumnStatistics) (ChartCandidate, bool) {
	if column.NonNullCount == 0 || column.Cardinality == 0 || column.CardinalityIncomplete || column.FrequenciesIncomplete {
		return ChartCandidate{}, false
	}
	if column.Types.Date+column.Types.Datetime > 0 {
		return ChartCandidate{}, false
	}
	points := make([]ChartPoint, 0, len(column.Frequencies)+1)
	hasNull := false
	for _, frequency := range column.Frequencies {
		label := frequency.Label
		if frequency.Type == secureread.ValueKindNull {
			label = "∅ NULL"
			hasNull = true
		}
		points = append(points, ChartPoint{Label: label, Value: float64(frequency.Count)})
	}
	if column.NullCount > 0 && !hasNull {
		points = append(points, ChartPoint{Label: "∅ NULL", Value: float64(column.NullCount)})
	}
	sort.Slice(points, func(i, j int) bool {
		if points[i].Value != points[j].Value {
			return points[i].Value > points[j].Value
		}
		return points[i].Label < points[j].Label
	})
	if len(points) > 10 {
		points = points[:10]
	}
	if len(points) == 0 {
		return ChartCandidate{}, false
	}
	name := column.Name
	lower := strings.ToLower(name)
	score := 55
	reason := "groupable values"
	switch {
	case strings.Contains(lower, "country"), strings.Contains(lower, "city"), strings.Contains(lower, "status"), strings.Contains(lower, "type"), strings.Contains(lower, "category"):
		score += 25
		reason = "semantic category"
	case column.Types.Boolean > 0:
		score -= 30
		reason = "boolean category"
	case column.Types.Number+column.Types.Decimal > 0:
		score -= 25
		reason = "numeric category"
	}
	if strings.HasSuffix(lower, "id") || strings.HasSuffix(lower, "key") || strings.HasPrefix(lower, "id_") {
		score -= 70
		reason = "identifier-like values"
	}
	if column.Cardinality <= 1 {
		score -= 35
	}
	if column.NonNullCount > 0 && float64(column.Cardinality)/float64(column.NonNullCount) >= 0.9 && column.Cardinality > 5 {
		score -= 45
	}
	if rowCount > 0 && float64(column.NullCount)/float64(rowCount) >= 0.6 {
		score -= 30
	}
	labels := make([]string, len(points))
	for i, point := range points {
		labels[i] = point.Label
	}
	return ChartCandidate{Score: score, Reason: reason, Spec: ChartSpec{
		Kind: ChartBar, Title: "By " + name, Dimension: name, Measure: "Rows", Aggregation: "count", Ordering: "count desc, label asc", Limit: 10,
		SourceColumns: []string{name}, Labels: labels, Bucket: "value", Points: points,
	}}, true
}

func countLineCandidate(column secureread.ColumnStatistics) (ChartCandidate, bool) {
	if column.DateBucketsIncomplete || len(column.DateBuckets) < 2 {
		return ChartCandidate{}, false
	}
	points, bucket := bucketDateCounts(column.DateBuckets)
	return ChartCandidate{Score: 90, Reason: "ordered date/time dimension", Spec: ChartSpec{
		Kind: ChartLine, Title: "Rows over " + column.Name, Dimension: column.Name, Measure: "Rows", Aggregation: "count", Ordering: "date asc",
		SourceColumns: []string{column.Name}, Bucket: bucket, Points: points,
	}}, true
}

func sumLineCandidate(stats secureread.RecordSetStatistics, sums secureread.DateNumericSum) (ChartCandidate, bool) {
	if sums.Incomplete || len(sums.Buckets) < 2 {
		return ChartCandidate{}, false
	}
	for _, column := range stats.Columns {
		if column.Name == sums.NumericColumn {
			lower := strings.ToLower(column.Name)
			if strings.HasSuffix(lower, "id") || strings.HasSuffix(lower, "key") {
				return ChartCandidate{}, false
			}
			break
		}
	}
	points, bucket := bucketDateSums(sums.Buckets)
	return ChartCandidate{Score: 100, Reason: "ordered date and numeric measure", Spec: ChartSpec{
		Kind: ChartLine, Title: fmt.Sprintf("%s over %s", sums.NumericColumn, sums.DateColumn), Dimension: sums.DateColumn,
		Measure: sums.NumericColumn, Aggregation: "sum", Ordering: "date asc", SourceColumns: []string{sums.DateColumn, sums.NumericColumn},
		Bucket: bucket, Points: points,
	}}, true
}

func dateBucketSize(count int, first, last string) string {
	if count > 60 || (len(first) >= 7 && len(last) >= 7 && first[:7] != last[:7] && count > 30) {
		return "month"
	}
	return "day"
}

func bucketDateCounts(buckets []secureread.DateBucket) ([]ChartPoint, string) {
	mode := dateBucketSize(len(buckets), buckets[0].Bucket, buckets[len(buckets)-1].Bucket)
	values := map[string]float64{}
	for _, bucket := range buckets {
		label := bucket.Bucket
		if mode == "month" && len(label) >= 7 {
			label = label[:7]
		}
		values[label] += float64(bucket.Count)
	}
	return orderedDatePoints(values), mode
}

func bucketDateSums(buckets []secureread.DateNumericBucket) ([]ChartPoint, string) {
	mode := dateBucketSize(len(buckets), buckets[0].Bucket, buckets[len(buckets)-1].Bucket)
	values := map[string]float64{}
	for _, bucket := range buckets {
		label := bucket.Bucket
		if mode == "month" && len(label) >= 7 {
			label = label[:7]
		}
		values[label] += bucket.Sum
	}
	return orderedDatePoints(values), mode
}

func orderedDatePoints(values map[string]float64) []ChartPoint {
	labels := make([]string, 0, len(values))
	for label := range values {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	points := make([]ChartPoint, len(labels))
	for i, label := range labels {
		points[i] = ChartPoint{Label: label, Value: values[label]}
	}
	return points
}
