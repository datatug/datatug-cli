package chat

import (
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ContextReference is an identity, never a copy of an object's data. Project
// objects use their source and qualified object name; session objects use ID.
type ContextReference struct {
	Kind      string `json:"kind"`
	ProjectID string `json:"projectId,omitempty"`
	SourceID  string `json:"sourceId,omitempty"`
	ObjectID  string `json:"objectId"`
	Title     string `json:"title"`
}

type ProjectObject struct {
	Reference ContextReference
	Columns   []string
}

type ProjectCatalog struct {
	ID      string
	Title   string
	Objects []ProjectObject
}

type RecordSetView struct {
	ID          string    `json:"id"`
	RecordSetID string    `json:"recordSetId"`
	Title       string    `json:"title"`
	RowIndices  []int     `json:"rowIndices"`
	Columns     []string  `json:"columns,omitempty"`
	OrderBy     string    `json:"orderBy,omitempty"`
	Descending  bool      `json:"descending,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

// CellRange uses inclusive coordinates in the immutable RecordSet, not the
// current grid cursor or sorted display position.
type CellRange struct {
	FirstRow int `json:"firstRow"`
	LastRow  int `json:"lastRow"`
	FirstCol int `json:"firstCol"`
	LastCol  int `json:"lastCol"`
}

type Selection struct {
	ID        string      `json:"id"`
	ViewID    string      `json:"viewId"`
	Title     string      `json:"title"`
	Rows      []int       `json:"rows,omitempty"`
	Columns   []string    `json:"columns,omitempty"`
	Ranges    []CellRange `json:"ranges,omitempty"`
	CreatedAt time.Time   `json:"createdAt"`
}

type Dock struct {
	ID        string           `json:"id"`
	Reference ContextReference `json:"reference"`
	Title     string           `json:"title"`
}

type WorkspaceState struct {
	Views              map[string]RecordSetView `json:"views,omitempty"`
	Selections         map[string]Selection     `json:"selections,omitempty"`
	Attachments        []ContextReference       `json:"attachments,omitempty"`
	Docks              []Dock                   `json:"docks,omitempty"`
	CurrentSelectionID string                   `json:"currentSelectionId,omitempty"`
	ActiveTab          string                   `json:"activeTab,omitempty"`
}

type WorkspaceAction struct {
	Kind        string           `json:"kind"`
	Reference   ContextReference `json:"reference,omitempty" jsonschema:"Exact existing object reference; omit for dock to dock the current selection"`
	RecordSetID string           `json:"recordSetId,omitempty"`
	ViewID      string           `json:"viewId,omitempty"`
	Title       string           `json:"title,omitempty"`
	Column      string           `json:"column,omitempty"`
	Equals      string           `json:"equals,omitempty"`
	Contains    string           `json:"contains,omitempty"`
	OrderBy     string           `json:"orderBy,omitempty"`
	Descending  bool             `json:"descending,omitempty"`
	Limit       int              `json:"limit,omitempty"`
	RowStart    int              `json:"rowStart,omitempty"`
	RowEnd      int              `json:"rowEnd,omitempty"`
	Columns     []string         `json:"columns,omitempty"`
	Rows        []int            `json:"rows,omitempty"`
	Ranges      []CellRange      `json:"ranges,omitempty"`
	DockID      string           `json:"dockId,omitempty"`
	BookmarkID  string           `json:"bookmarkId,omitempty"`
	Tag         string           `json:"tag,omitempty"`
	Search      string           `json:"search,omitempty"`
	Tags        []string         `json:"tags,omitempty"`
}

func (w WorkspaceState) apply(session ChatSession, catalog ProjectCatalog, a WorkspaceAction) (WorkspaceState, ContextReference, error) {
	a.Reference.Kind = strings.ToLower(a.Reference.Kind)
	if w.Views == nil {
		w.Views = map[string]RecordSetView{}
	}
	if w.Selections == nil {
		w.Selections = map[string]Selection{}
	}
	switch a.Kind {
	case "select":
		return w.selectRows(session, a)
	case "sort_view":
		view, ok := w.Views[a.ViewID]
		if !ok {
			return w, ContextReference{}, fmt.Errorf("the selected View is no longer available")
		}
		record, ok := session.RecordSets[view.RecordSetID]
		if !ok {
			return w, ContextReference{}, fmt.Errorf("the View's RecordSet is no longer available")
		}
		if !containsColumn(record.Result.Columns, a.OrderBy) {
			return w, ContextReference{}, fmt.Errorf("cannot sort: this RecordSet has no %s column", a.OrderBy)
		}
		view.RowIndices = append([]int(nil), view.RowIndices...)
		sortRecordRows(record, view.RowIndices, a.OrderBy, a.Descending)
		view.OrderBy, view.Descending = a.OrderBy, a.Descending
		w.Views[view.ID] = view
		for id, selection := range w.Selections {
			if selection.ViewID != view.ID {
				continue
			}
			included := make(map[int]bool, len(selection.Rows))
			for _, row := range selection.Rows {
				included[row] = true
			}
			selection.Rows = selection.Rows[:0]
			for _, row := range view.RowIndices {
				if included[row] {
					selection.Rows = append(selection.Rows, row)
				}
			}
			w.Selections[id] = selection
		}
		return w, ContextReference{Kind: "view", ObjectID: view.ID, Title: view.Title}, nil
	case "clear_selection":
		w.CurrentSelectionID = ""
		return w, ContextReference{}, nil
	case "set_tab":
		if a.Title != "Project" && a.Title != "Selected" && a.Title != "Docked" && a.Title != "Bookmarks" {
			return w, ContextReference{}, fmt.Errorf("unknown workspace tab %q", a.Title)
		}
		w.ActiveTab = a.Title
		return w, ContextReference{}, nil
	case "attach":
		if err := validateContextReference(session, catalog, w, a.Reference); err != nil {
			return w, ContextReference{}, err
		}
		for _, attached := range w.Attachments {
			if sameReference(attached, a.Reference) {
				return w, attached, nil
			}
		}
		w.Attachments = append(w.Attachments, a.Reference)
		return w, a.Reference, nil
	case "detach":
		for i, attached := range w.Attachments {
			if sameReference(attached, a.Reference) {
				w.Attachments = append(w.Attachments[:i:i], w.Attachments[i+1:]...)
				return w, attached, nil
			}
		}
		return w, ContextReference{}, fmt.Errorf("%s is not attached", a.Reference.Title)
	case "dock":
		if a.Reference.ObjectID == "" && w.CurrentSelectionID != "" {
			if selection, ok := w.Selections[w.CurrentSelectionID]; ok {
				a.Reference = ContextReference{Kind: "selection", ObjectID: selection.ID, Title: selection.Title}
			}
		}
		if err := validateContextReference(session, catalog, w, a.Reference); err != nil {
			return w, ContextReference{}, err
		}
		if a.Reference.Kind != "recordset" && a.Reference.Kind != "view" && a.Reference.Kind != "selection" && a.Reference.Kind != "bookmark" {
			return w, ContextReference{}, fmt.Errorf("only a RecordSet, view, selection, or bookmark can be docked")
		}
		for _, dock := range w.Docks {
			if sameReference(dock.Reference, a.Reference) {
				return w, a.Reference, nil
			}
		}
		title := strings.TrimSpace(a.Title)
		if title == "" {
			title = a.Reference.Title
		}
		w.Docks = append(w.Docks, Dock{ID: uuid.NewString(), Reference: a.Reference, Title: normalizeGridTitle(title)})
		w.ActiveTab = "Docked"
		return w, a.Reference, nil
	case "undock":
		for i, dock := range w.Docks {
			if dock.ID == a.DockID || (a.DockID == "" && sameReference(dock.Reference, a.Reference)) {
				w.Docks = append(w.Docks[:i:i], w.Docks[i+1:]...)
				return w, dock.Reference, nil
			}
		}
		return w, ContextReference{}, fmt.Errorf("docked item not found")
	default:
		return w, ContextReference{}, fmt.Errorf("unknown workspace action %q", a.Kind)
	}
}

func (w WorkspaceState) selectRows(session ChatSession, a WorkspaceAction) (WorkspaceState, ContextReference, error) {
	recordID := a.RecordSetID
	if recordID == "" && a.ViewID != "" {
		recordID = w.Views[a.ViewID].RecordSetID
	}
	record, ok := session.RecordSets[recordID]
	if !ok {
		return w, ContextReference{}, fmt.Errorf("the selected RecordSet is no longer available")
	}
	rows := make([]int, 0, len(record.Result.Rows))
	var allowedRows map[int]bool
	var allowedColumns map[string]bool
	var visibleColumns []string
	if a.ViewID != "" {
		parent, ok := w.Views[a.ViewID]
		if !ok || parent.RecordSetID != recordID {
			return w, ContextReference{}, fmt.Errorf("the selected view is no longer available")
		}
		rows = append(rows, parent.RowIndices...)
		allowedRows = make(map[int]bool, len(parent.RowIndices))
		for _, row := range parent.RowIndices {
			allowedRows[row] = true
		}
		parentColumns := parent.Columns
		if len(parentColumns) == 0 {
			parentColumns = record.Result.Columns
		}
		visibleColumns = parentColumns
		allowedColumns = make(map[string]bool, len(parentColumns))
		for _, column := range parentColumns {
			allowedColumns[column] = true
		}
	} else {
		for i := range record.Result.Rows {
			rows = append(rows, i)
		}
	}
	if len(a.Rows) > 0 {
		rows = append([]int(nil), a.Rows...)
		for _, row := range rows {
			if row < 0 || row >= len(record.Result.Rows) {
				return w, ContextReference{}, fmt.Errorf("row %d is outside this RecordSet", row+1)
			}
			if allowedRows != nil && !allowedRows[row] {
				return w, ContextReference{}, fmt.Errorf("row %d is outside the selected View", row+1)
			}
		}
	}
	for _, column := range append(append([]string{}, a.Columns...), a.Column, a.OrderBy) {
		if column == "" {
			continue
		}
		if !containsColumn(record.Result.Columns, column) {
			return w, ContextReference{}, fmt.Errorf("cannot select %q: this RecordSet has no %s column", column, column)
		}
		if allowedColumns != nil && !allowedColumns[column] {
			return w, ContextReference{}, fmt.Errorf("cannot select %q: column is outside the selected View", column)
		}
	}
	for _, span := range a.Ranges {
		if span.FirstRow < 0 || span.LastRow < span.FirstRow || span.LastRow >= len(record.Result.Rows) || span.FirstCol < 0 || span.LastCol < span.FirstCol || span.LastCol >= len(record.Result.Columns) {
			return w, ContextReference{}, fmt.Errorf("cell range is outside this RecordSet")
		}
		if allowedRows != nil {
			for row := span.FirstRow; row <= span.LastRow; row++ {
				if !allowedRows[row] {
					return w, ContextReference{}, fmt.Errorf("cell range is outside the selected View")
				}
			}
		}
		if allowedColumns != nil {
			for column := span.FirstCol; column <= span.LastCol; column++ {
				if !allowedColumns[record.Result.Columns[column]] {
					return w, ContextReference{}, fmt.Errorf("cell range is outside the selected View")
				}
			}
		}
	}
	if len(a.Ranges) > 0 && len(a.Rows) == 0 {
		selectedRows := make(map[int]bool)
		for _, span := range a.Ranges {
			for row := span.FirstRow; row <= span.LastRow; row++ {
				selectedRows[row] = true
			}
		}
		filtered := rows[:0]
		for _, row := range rows {
			if selectedRows[row] {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	}
	if a.RowStart < 0 || a.RowEnd < 0 || a.Limit < 0 {
		return w, ContextReference{}, fmt.Errorf("row positions and limit cannot be negative")
	}
	if a.Column != "" {
		filtered := rows[:0]
		for _, i := range rows {
			value := FormatValue(record.Result.Rows[i].Data[a.Column])
			if a.Equals != "" && !strings.EqualFold(value, a.Equals) {
				continue
			}
			if a.Contains != "" && !strings.Contains(strings.ToLower(value), strings.ToLower(a.Contains)) {
				continue
			}
			filtered = append(filtered, i)
		}
		rows = filtered
	}
	if a.OrderBy != "" {
		sortRecordRows(record, rows, a.OrderBy, a.Descending)
	}
	start := min(a.RowStart, len(rows))
	end := len(rows)
	if a.RowEnd > 0 {
		end = min(end, a.RowEnd+1)
	}
	if end < start {
		end = start
	}
	rows = rows[start:end]
	if a.Limit > 0 && len(rows) > a.Limit {
		rows = rows[:a.Limit]
	}
	columns := a.Columns
	if len(columns) == 0 {
		if len(a.Ranges) > 0 {
			selectedColumns := make(map[int]bool)
			for _, span := range a.Ranges {
				for column := span.FirstCol; column <= span.LastCol; column++ {
					selectedColumns[column] = true
				}
			}
			for column, name := range record.Result.Columns {
				if selectedColumns[column] {
					columns = append(columns, name)
				}
			}
		} else {
			if visibleColumns != nil {
				columns = append([]string(nil), visibleColumns...)
			} else {
				columns = append([]string(nil), record.Result.Columns...)
			}
		}
	}
	if err := validateSelectionRangeProjection(record.Result.Columns, rows, columns, a.Ranges); err != nil {
		return w, ContextReference{}, err
	}
	title := normalizeGridTitle(a.Title)
	if a.Title == "" {
		title = record.Title
		if a.Equals != "" {
			title += " · " + a.Equals
		}
		title = fmt.Sprintf("%s · %d", title, len(rows))
	}
	now := time.Now().UTC()
	view := RecordSetView{ID: uuid.NewString(), RecordSetID: recordID, Title: title, RowIndices: append([]int{}, rows...), Columns: append([]string{}, columns...), OrderBy: a.OrderBy, Descending: a.Descending, CreatedAt: now}
	selection := Selection{ID: uuid.NewString(), ViewID: view.ID, Title: title, Rows: append([]int{}, rows...), Columns: append([]string{}, columns...), Ranges: append([]CellRange{}, a.Ranges...), CreatedAt: now}
	w.Views[view.ID] = view
	w.Selections[selection.ID] = selection
	w.CurrentSelectionID = selection.ID
	w.ActiveTab = "Selected"
	ref := ContextReference{Kind: "selection", ObjectID: selection.ID, Title: selection.Title}
	return w, ref, nil
}

// Range coordinates and the display row/column axes must describe the same
// selection. Otherwise a model-supplied Rows/Columns override could silently
// drop cells from the immutable range when the snapshot is projected.
func validateSelectionRangeProjection(recordColumns []string, rows []int, columns []string, ranges []CellRange) error {
	if len(ranges) == 0 {
		return nil
	}
	rangeRows := map[int]bool{}
	rangeColumns := map[string]bool{}
	for _, span := range ranges {
		for row := span.FirstRow; row <= span.LastRow; row++ {
			rangeRows[row] = true
		}
		for column := span.FirstCol; column <= span.LastCol; column++ {
			if column < 0 || column >= len(recordColumns) {
				return fmt.Errorf("cell range is outside this RecordSet")
			}
			rangeColumns[recordColumns[column]] = true
		}
	}
	selectedRows := map[int]bool{}
	for _, row := range rows {
		selectedRows[row] = true
	}
	selectedColumns := map[string]bool{}
	for _, column := range columns {
		selectedColumns[column] = true
	}
	if len(selectedRows) != len(rangeRows) || len(selectedColumns) != len(rangeColumns) || len(selectedRows) != len(rows) || len(selectedColumns) != len(columns) {
		return fmt.Errorf("cell ranges must match the selected rows and columns")
	}
	for row := range rangeRows {
		if !selectedRows[row] {
			return fmt.Errorf("cell ranges must match the selected rows and columns")
		}
	}
	for column := range rangeColumns {
		if !selectedColumns[column] {
			return fmt.Errorf("cell ranges must match the selected rows and columns")
		}
	}
	return nil
}

func sortRecordRows(record RecordSet, rows []int, column string, descending bool) {
	sort.SliceStable(rows, func(i, j int) bool {
		left := FormatValue(record.Result.Rows[rows[i]].Data[column])
		right := FormatValue(record.Result.Rows[rows[j]].Data[column])
		comparison := strings.Compare(strings.ToLower(left), strings.ToLower(right))
		if l, ok := new(big.Rat).SetString(left); ok {
			if r, ok := new(big.Rat).SetString(right); ok {
				comparison = l.Cmp(r)
			}
		}
		if descending {
			return comparison > 0
		}
		return comparison < 0
	})
}

func containsColumn(columns []string, name string) bool {
	for _, column := range columns {
		if column == name {
			return true
		}
	}
	return false
}

// selectionParameters resolves attached/docked values locally at the query
// boundary. The model sees parameter names and schemas, never rows.
func selectionParameters(session ChatSession) map[string]any {
	params := map[string]any{}
	for contextIndex, ref := range contextReferences(session) {
		var record RecordSet
		var view *RecordSetView
		var selection *Selection
		switch ref.Kind {
		case "selection":
			selected, ok := session.Workspace.Selections[ref.ObjectID]
			if !ok {
				continue
			}
			visible, ok := session.Workspace.Views[selected.ViewID]
			if !ok {
				continue
			}
			record, ok = session.RecordSets[visible.RecordSetID]
			if !ok {
				continue
			}
			view, selection = &visible, &selected
		case "bookmark":
			bookmark, ok := session.Bookmarks[ref.ObjectID]
			if !ok {
				continue
			}
			record, view, selection = bookmark.Snapshot.RecordSet, bookmark.Snapshot.View, bookmark.Snapshot.Selection
		default:
			continue
		}
		projected, _ := projectSnapshot(record, view, selection)
		for columnIndex, column := range projected.Columns {
			values := make([]any, 0, len(projected.Rows))
			for _, row := range projected.Rows {
				if value, present := row.Data[column]; present && value != nil {
					values = append(values, value)
				}
			}
			params[fmt.Sprintf("selection_%d_c%d", contextIndex+1, columnIndex+1)] = values
		}
	}
	return params
}

// Explicit attachments and docks are query context. Merely selecting a cell
// does not attach its values to later queries; the current selection remains
// visible only as an opaque workspace identity for commands like "dock them".
func contextReferences(session ChatSession) []ContextReference {
	refs := append([]ContextReference(nil), session.Workspace.Attachments...)
	for _, dock := range session.Workspace.Docks {
		found := false
		for _, ref := range refs {
			if sameReference(ref, dock.Reference) {
				found = true
				break
			}
		}
		if !found {
			refs = append(refs, dock.Reference)
		}
	}
	return refs
}

func sameReference(a, b ContextReference) bool {
	if a.Kind == "bookmark" && b.Kind == "bookmark" {
		return a.ProjectID == b.ProjectID && a.ObjectID == b.ObjectID
	}
	return a.Kind == b.Kind && a.ProjectID == b.ProjectID && a.SourceID == b.SourceID && a.ObjectID == b.ObjectID
}

func validateContextReference(session ChatSession, catalog ProjectCatalog, w WorkspaceState, ref ContextReference) error {
	if ref.ObjectID == "" {
		return fmt.Errorf("context object identity is empty")
	}
	switch ref.Kind {
	case "recordset":
		if _, ok := session.RecordSets[ref.ObjectID]; !ok {
			return fmt.Errorf("the selected RecordSet is no longer available")
		}
	case "view":
		if _, ok := w.Views[ref.ObjectID]; !ok {
			return fmt.Errorf("the selected view is no longer available")
		}
	case "selection":
		if _, ok := w.Selections[ref.ObjectID]; !ok {
			return fmt.Errorf("the selected selection is no longer available")
		}
	case "bookmark":
		bookmark, ok := session.Bookmarks[ref.ObjectID]
		if !ok || bookmark.ProjectID != catalog.ID {
			return fmt.Errorf("the selected bookmark is no longer available")
		}
		if ref.ProjectID != "" && ref.ProjectID != bookmark.ProjectID {
			return fmt.Errorf("bookmark belongs to a different project")
		}
		if ref.SourceID != "" && ref.SourceID != bookmark.SourceID {
			return fmt.Errorf("bookmark belongs to a different data source")
		}
	case "project", "source", "table", "project_view", "query":
		for _, object := range catalog.Objects {
			if sameReference(object.Reference, ref) {
				return nil
			}
		}
		return fmt.Errorf("project object %q is unavailable", ref.ObjectID)
	default:
		return fmt.Errorf("unsupported context kind %q", ref.Kind)
	}
	return nil
}
