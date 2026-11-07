// Package labelguard holds the sensitivity-label decisions every read of a
// project source shares, whoever reads it: `query run`, `chat`, `datatug://`
// reads and `datatug serve`'s exec/run_query. The callers own the I/O (loading
// the labels, describing a table); the rule is here, once:
//
//   - a stored protected label (owner or proposed, on the column or on its
//     whole table) refuses for every principal;
//   - a column protected only by a read-time suggestion refuses a strict
//     principal (an agent, or a policy-governed session);
//   - anyone else reads it, and is warned once per column.
package labelguard

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/datatug/datatug-core/pkg/meaninglabels"
)

// StoredLabelCovers reports whether a stored protected label (owner or
// proposed, on the column or its whole table) covers column. A column's
// effective origin can read "suggested" when a tied suggestion replaced such a
// label, so whether a label is stored is decided here, from the document.
func StoredLabelCovers(doc meaninglabels.Document, source, table, column string) bool {
	for _, l := range doc.Labels {
		if l.Class.Protected() && strings.EqualFold(l.Target.Source, source) && meaninglabels.TableMatches(l.Target.Table, table) &&
			(l.Target.Column == "" || strings.EqualFold(l.Target.Column, column)) {
			return true
		}
	}
	return false
}

// SourceHasProtectedLabel reports whether any protected label (owner or
// proposed) is stored for source.
func SourceHasProtectedLabel(doc meaninglabels.Document, source string) bool {
	for _, l := range doc.Labels {
		if l.Class.Protected() && strings.EqualFold(l.Target.Source, source) {
			return true
		}
	}
	return false
}

// StoredTableLabel is the first protected label stored for table (on one of
// its columns or on the whole table): a read of every column of table reads it.
func StoredTableLabel(doc meaninglabels.Document, source, table string) (meaninglabels.ColumnLabel, bool) {
	for _, l := range doc.Labels {
		if l.Class.Protected() && strings.EqualFold(l.Target.Source, source) && meaninglabels.TableMatches(l.Target.Table, table) {
			return meaninglabels.ColumnLabel{Column: l.Target.Column, Class: l.Class}, true
		}
	}
	return meaninglabels.ColumnLabel{}, false
}

// Columns converts column names to label columns.
func Columns(names []string) []meaninglabels.Column {
	columns := make([]meaninglabels.Column, len(names))
	for i, name := range names {
		columns[i] = meaninglabels.Column{Name: name}
	}
	return columns
}

// CheckColumns judges the columns a read names on one table (view reports
// whether it is a view). refused is the label that refuses the read, if any;
// otherwise suggested lists, sorted as table.column, the columns that only
// read-time suggestions protect, which a non-strict principal reads.
func CheckColumns(doc meaninglabels.Document, source, table string, view bool, columns []string, strict bool) (refused *meaninglabels.ColumnLabel, suggested []string) {
	for _, l := range meaninglabels.EffectiveObject(doc, source, table, view, Columns(columns)) {
		switch {
		case !l.Protected():
		case strict || l.Origin != meaninglabels.FromSuggested || StoredLabelCovers(doc, source, table, l.Column):
			label := l
			return &label, nil
		default:
			suggested = append(suggested, table+"."+l.Column)
		}
	}
	sort.Strings(suggested)
	return nil, suggested
}

// SuggestedColumns lists, sorted, the table.column names whose effective
// label is protected.
func SuggestedColumns(doc meaninglabels.Document, source string, tables map[string][]meaninglabels.Column) []string {
	var names []string
	for table, columns := range tables {
		for _, l := range meaninglabels.Effective(doc, source, table, columns) {
			if l.Protected() {
				names = append(names, table+"."+l.Column)
			}
		}
	}
	sort.Strings(names)
	return names
}

// Warner writes one warning per suggested column, however many reads (chat,
// a serve process) or leaves name it.
type Warner struct {
	mu     sync.Mutex
	out    io.Writer
	warned map[string]bool
}

// NewWarner returns a Warner writing to out.
func NewWarner(out io.Writer) *Warner {
	return &Warner{out: out, warned: map[string]bool{}}
}

// Warn names the suggested columns of source that what reads unmasked, each
// once.
func (w *Warner) Warn(what, source string, columns []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var fresh []string
	for _, column := range columns {
		if !w.warned[source+"."+column] {
			w.warned[source+"."+column] = true
			fresh = append(fresh, column)
		}
	}
	if len(fresh) > 0 {
		_, _ = fmt.Fprintf(w.out, "warning: %s is not masked; read-time suggestions mark %s of source %s sensitive: label them to protect them from agents\n", what, strings.Join(fresh, ", "), source)
	}
}
