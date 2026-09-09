package secureread

import (
	"context"
	"sort"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
)

// hiddenColumnsFor reports the queried collection's fields some policy's
// field allow-list hides, for an implicit (wildcard, no explicit
// Columns()) select — the only shape where DALgo silently redacts fields
// per row rather than refusing the query outright. An explicit reference to
// a hidden field is refused before execution by accesspolicies.Run itself
// (AC hidden-column-refused); this function only explains what an allowed,
// wildcard-shaped query already had removed from it.
//
// Best-effort: a source without dbschema.SchemaReader, or a collection
// DescribeCollection cannot resolve, yields nil. The row-level redaction
// already happened via access.SecureReadSession regardless of whether this
// Limitation can be computed — a nil return only means the caller does not
// get told which columns, not that redaction was skipped.
func hiddenColumnsFor(ctx context.Context, db dal.DB, query dal.StructuredQuery, lines []accesspolicies.Line) []string {
	if !anyFieldLists(lines) {
		return nil
	}
	ref, ok := baseCollectionRef(query)
	if !ok {
		return nil
	}
	reader, ok := dal.As[dbschema.SchemaReader](db)
	if !ok {
		return nil
	}
	def, err := reader.DescribeCollection(ctx, &ref)
	if err != nil || def == nil {
		return nil
	}
	var hidden []string
	for _, field := range def.Fields {
		name := string(field.Name)
		for _, line := range lines {
			for _, list := range line.FieldLists {
				if !fieldAllowed(list, name) {
					hidden = append(hidden, name)
				}
			}
		}
	}
	sort.Strings(hidden)
	return dedupeStrings(hidden)
}

func anyFieldLists(lines []accesspolicies.Line) bool {
	for _, line := range lines {
		if len(line.FieldLists) > 0 {
			return true
		}
	}
	return false
}

// baseCollectionRef extracts the base collection a structured query reads,
// when it reads one directly (not a collection group or another
// RecordsetSource kind schema description does not apply to).
func baseCollectionRef(query dal.StructuredQuery) (dal.CollectionRef, bool) {
	switch source := query.From().Base().(type) {
	case dal.CollectionRef:
		return source, true
	case *dal.CollectionRef:
		return *source, true
	default:
		return dal.CollectionRef{}, false
	}
}

func dedupeStrings(sorted []string) []string {
	out := sorted[:0]
	for i, name := range sorted {
		if i == 0 || name != sorted[i-1] {
			out = append(out, name)
		}
	}
	return out
}

// fieldAllowed and segmentMatches mirror accesspolicies' own (unexported)
// field-pattern matcher — see pkg/accesspolicies/fields.go — so a column
// judged "hidden" here uses the exact same DALgo field-pattern grammar
// (dotted segments, each a literal, "*", "prefix*" or "*suffix") the DAL's
// own row/column redaction is built on. Duplicated rather than exported from
// accesspolicies to keep that package's surface unchanged for this stream.
func fieldAllowed(patterns []string, path string) bool {
	segments := strings.Split(path, ".")
	for _, pattern := range patterns {
		want := strings.Split(strings.TrimSuffix(pattern, ".*"), ".")
		if len(want) > len(segments) {
			continue
		}
		matched := true
		for i, segment := range want {
			if !segmentMatches(segment, segments[i]) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func segmentMatches(pattern, segment string) bool {
	switch {
	case pattern == "*":
		return true
	case strings.HasPrefix(pattern, "*"):
		return strings.HasSuffix(segment, pattern[1:])
	case strings.HasSuffix(pattern, "*"):
		return strings.HasPrefix(segment, pattern[:len(pattern)-1])
	default:
		return pattern == segment
	}
}
