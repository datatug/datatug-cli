package sqliteschema

import (
	"testing"

	"github.com/datatug/datatug-cli/pkg/datatug-core/schemer"
)

func assertForeignKeys(t *testing.T, table string, expected, actual []schemer.ForeignKey, isReferrer bool) {
	t.Helper()
	if len(actual) != len(expected) {
		t.Errorf("expected %d foreign keys/referrers for %s, got %d", len(expected), table, len(actual))
	}

	for _, expectedFK := range expected {
		found := false
		for _, actualFK := range actual {
			if (isReferrer && actualFK.From.Name == expectedFK.From.Name) || (!isReferrer && actualFK.To.Name == expectedFK.To.Name) {
				// Check columns
				if len(actualFK.From.Columns) != len(expectedFK.From.Columns) {
					continue
				}
				match := true
				for i := range actualFK.From.Columns {
					if actualFK.From.Columns[i] != expectedFK.From.Columns[i] ||
						actualFK.To.Columns[i] != expectedFK.To.Columns[i] {
						match = false
						break
					}
				}
				if match {
					found = true
					break
				}
			}
		}
		if !found {
			if isReferrer {
				t.Errorf("missing expected referrer for %s from %s with columns %v -> %v", table, expectedFK.From.Name, expectedFK.From.Columns, expectedFK.To.Columns)
			} else {
				t.Errorf("missing expected foreign key for %s to %s with columns %v -> %v", table, expectedFK.To.Name, expectedFK.From.Columns, expectedFK.To.Columns)
			}
		}
	}
}
