package dbviewer

import (
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// trickyValue holds a backslash, a newline and a bracketed fragment. The legacy
// text emitter refuses the first two and strips every [...] pair from the
// statement, string literals included; the sqlite dialect binds the value as a
// parameter.
const trickyValue = "a\\b\n[c]"

// TestViewerReads_NonASCIIColumnAndEscapedFilterValue is the viewer's read-path
// acceptance: the content load reads a table with a non-ASCII column name, and
// the foreign-key preview filters it by a value with a backslash, a newline and
// a bracketed fragment.
func TestViewerReads_NonASCIIColumnAndEscapedFilterValue(t *testing.T) {
	path := createTestSqliteDb(t,
		`CREATE TABLE people (id INTEGER PRIMARY KEY, "naïve" TEXT)`,
		`INSERT INTO people (id, "naïve") VALUES (1, 'plain')`,
		`INSERT INTO people (id, "naïve") VALUES (2, 'a\b`+"\n"+`[c]')`,
	)
	db := dtviewers.GetSQLiteDbContext(path)

	t.Run("content", func(t *testing.T) {
		msg := loadContent(dtviewers.CollectionContext{
			DbContext:     db,
			CollectionRef: dal.NewRootCollectionRef("people", ""),
		})().(contentLoaded)
		require.NoError(t, msg.err)
		assert.Equal(t, 2, msg.rs.RowsCount())
		assert.Equal(t, "naïve", msg.rs.GetColumnByIndex(1).Name())
	})

	t.Run("preview", func(t *testing.T) {
		q := dal.From(dal.NewCollectionRef("people", "", nil)).NewQuery().
			WhereField("naïve", dal.Equal, dal.NewConstant(trickyValue)).
			SelectIntoRecordset()
		msg := loadPreview(1, db, q)().(previewLoaded)
		require.NoError(t, msg.err)
		assert.Equal(t, 1, msg.rs.RowsCount())
		value, err := msg.rs.GetColumnByIndex(1).GetValue(0)
		require.NoError(t, err)
		assert.Equal(t, trickyValue, value)
	})
}
