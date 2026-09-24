package chat

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dtql"
)

// The inspector reads the structured, immutable result and the scanned project
// catalog. It never infers a database constraint from a displayed value.
func (u *UI) inspectorWorkspaceView(width, height int) string {
	tabs := []string{"1 Current row", "2 Current column", "3 Current recordset"}
	if width < 55 {
		tabs = []string{"1 Row", "2 Column", "3 Recordset"}
	}
	for i := range tabs {
		if i == u.inspectorTab {
			tabs[i] = "● " + tabs[i]
		}
	}
	header := ansi.Truncate(strings.Join(tabs, " · "), width, "…")
	var lines []string
	switch u.inspectorTab {
	case 1:
		lines = append(lines, u.currentColumnDetails(width)...)
	case 2:
		lines = append(lines, u.currentRecordsetDetails(width)...)
	default:
		lines = append(lines, u.currentRowDetails(width)...)
	}
	visible := max(1, height-2)
	u.inspectorOffset = min(u.inspectorOffset, max(0, len(lines)-visible))
	end := min(len(lines), u.inspectorOffset+visible)
	return strings.Join(append([]string{header, ""}, lines[u.inspectorOffset:end]...), "\n")
}

func (u *UI) activeInspectorGrid() (*gridState, *historyEntry, *RecordSet) {
	if u.activeGrid < 0 || u.activeGrid >= len(u.entries) {
		return nil, nil, nil
	}
	entry := &u.entries[u.activeGrid]
	if entry.grid == nil {
		return nil, nil, nil
	}
	if record, ok := u.snapshot.RecordSets[entry.recordSetID]; ok {
		return entry.grid, entry, &record
	}
	return entry.grid, entry, nil
}

func (u *UI) currentRowDetails(width int) []string {
	g, _, record := u.activeInspectorGrid()
	rowIndex := -1
	if g != nil {
		rowIndex = g.CurrentIndex()
	}
	if g == nil || rowIndex < 0 || rowIndex >= len(g.Rows()) {
		return []string{u.selectedDetails(width)}
	}
	rawRow := g.rawRow(rowIndex)
	lines := []string{fmt.Sprintf("Row %d of %d · %s", rowIndex+1, len(g.Rows()), sanitizeTerminalText(g.Title())), ""}
	nameWidth, numberWidth := 0, 0
	for i, column := range g.Columns() {
		nameWidth = max(nameWidth, ansi.StringWidth(column.Name))
		if column.Numeric {
			numberWidth = max(numberWidth, ansi.StringWidth(g.Cell(rowIndex, i)))
		}
	}
	nameWidth = min(nameWidth, max(8, width/3))
	numberWidth = min(numberWidth, 20)
	for i, column := range g.Columns() {
		value := g.Cell(rowIndex, i)
		if value == "" {
			value = "—"
			if i < len(rawRow) && rawRow[i] == nil {
				value = "NULL"
			}
		}
		meta := u.columnMeta(record, column.Name)
		typeLabel := meta.dbType
		if typeLabel == "" {
			typeLabel = "?"
		}
		if column.Numeric {
			value = strings.Repeat(" ", max(0, numberWidth-ansi.StringWidth(value))) + value
		}
		name := ansi.Truncate(column.Name, nameWidth, "…")
		name = strings.Repeat(" ", max(0, nameWidth-ansi.StringWidth(name))) + name
		line := fmt.Sprintf("  %s  %-10s  %s", name, ansi.Truncate(typeLabel, 10, "…"), value)
		lines = append(lines, ansi.Truncate(line, width, "…"), "")
	}
	if selection := u.snapshot.Workspace.CurrentSelectionID; selection != "" {
		lines = append(lines, "Selection", u.selectedDetails(width))
	}
	return lines
}

type inspectorColumnMeta struct {
	qualified string
	dbType    string
	objects   []string
}

func (u *UI) columnMeta(record *RecordSet, name string) inspectorColumnMeta {
	meta := inspectorColumnMeta{}
	var sourceRelations map[string]bool
	shortName := name
	if last := strings.LastIndexByte(name, '.'); last >= 0 {
		shortName = name[last+1:]
	}
	if record != nil {
		if record.DTQL == "" {
			return meta
		}
		query, err := dtql.Deserialize([]byte(record.DTQL))
		if err != nil {
			return meta
		}
		instances := relationInstances(query.From())
		if len(instances) == 0 {
			return meta
		}
		allowed := func(source string) map[string]bool {
			matches := map[string]bool{}
			if source == "" && len(instances) != 1 {
				return matches
			}
			for _, instance := range instances {
				if source == "" || strings.EqualFold(source, instance.Alias) || strings.EqualFold(source, instance.Relation) {
					matches[relationKey(instance.Schema, instance.Relation)] = true
				}
			}
			return matches
		}
		if len(query.Columns()) == 0 {
			sourceRelations = allowed("")
		} else {
			matched := false
			for _, projection := range query.Columns() {
				if projection.Wildcard != nil {
					if slices.Contains(projection.Wildcard.Exclude, shortName) {
						continue
					}
					relations := allowed(projection.Wildcard.Source)
					if len(relations) == 1 {
						sourceRelations = relations
						matched = true
					}
					continue
				}
				field, ok := projection.Expression.(dal.FieldRef)
				if !ok {
					if projection.Alias == name {
						return meta // Derived value; a same-named physical field is not provenance.
					}
					continue
				}
				output := projection.Alias
				if output == "" {
					output = field.Name()
				}
				if !strings.EqualFold(output, name) {
					continue
				}
				if matched {
					return meta // Duplicate output names cannot be attributed safely.
				}
				shortName = field.Name()
				sourceRelations = allowed(field.Source())
				matched = true
			}
			if !matched {
				return meta
			}
		}
		if len(sourceRelations) == 0 {
			return meta
		}
	}
	for _, object := range u.catalog.Objects {
		if object.Reference.Kind != "table" && object.Reference.Kind != "project_view" {
			continue
		}
		if record != nil && record.Database != "" && object.Reference.SourceID != record.Database {
			continue
		}
		if len(sourceRelations) > 0 && !sourceRelations[strings.ToLower(object.Reference.ObjectID)] {
			continue
		}
		for _, column := range object.Columns {
			if !strings.EqualFold(column, shortName) {
				continue
			}
			qualified := object.Reference.ObjectID + "." + column
			meta.objects = append(meta.objects, qualified)
			if len(meta.objects) == 1 {
				meta.qualified = qualified
				meta.dbType = object.ColumnTypes[column]
			} else {
				meta.qualified = "ambiguous source"
				meta.dbType = ""
			}
		}
	}
	return meta
}

func (u *UI) currentColumnDetails(width int) []string {
	g, entry, record := u.activeInspectorGrid()
	if g == nil || g.SelectedColumn() < 0 || g.SelectedColumn() >= len(g.Columns()) {
		return []string{"Focus a result grid to inspect its current column."}
	}
	column := g.Columns()[g.SelectedColumn()]
	meta := u.columnMeta(record, column.Name)
	lines := []string{"Column: " + column.Name}
	if meta.qualified != "" {
		lines = append(lines, "Source: "+meta.qualified)
	} else {
		lines = append(lines, "Source: unavailable")
	}
	if meta.dbType != "" {
		lines = append(lines, "Type: "+meta.dbType)
	} else {
		lines = append(lines, "Type: not available in catalog")
	}
	if len(meta.objects) > 1 {
		lines = append(lines, "", "Possible source tables:")
		for _, object := range meta.objects {
			lines = append(lines, "  "+ansi.Truncate(object, max(1, width-2), "…"))
		}
	}
	var related []string
	var constraints []string
	for _, candidate := range entry.joinCandidates {
		for _, pair := range candidate.Fields {
			if strings.EqualFold(pair.SourceField, column.Name) {
				related = append(related, candidate.Target.Relation+" · "+candidate.Cardinality)
				constraints = append(constraints, candidate.ConstraintID)
				break
			}
		}
	}
	if len(constraints) == 0 {
		lines = append(lines, "Constraints: not available in compact catalog")
	} else {
		lines = append(lines, "FK constraints: "+strings.Join(constraints, ", "))
	}
	lines = append(lines, "", "Related tables:")
	if len(related) == 0 {
		lines = append(lines, "  No available JOIN candidate for this column.")
	} else {
		for _, relation := range related {
			lines = append(lines, "  "+ansi.Truncate(relation, max(1, width-2), "…"))
		}
	}
	return lines
}

func (u *UI) currentRecordsetDetails(width int) []string {
	g, _, record := u.activeInspectorGrid()
	if g == nil {
		return []string{"Focus a result grid to inspect its RecordSet."}
	}
	lines := []string{g.Title(), fmt.Sprintf("%d rows · %d columns", len(g.Rows()), len(g.Columns())), ""}
	for _, column := range g.Columns() {
		meta := u.columnMeta(record, column.Name)
		qualified := meta.qualified
		if qualified == "" {
			qualified = column.Name
		}
		typeLabel := meta.dbType
		if typeLabel == "" {
			typeLabel = "type unavailable"
		}
		lines = append(lines, ansi.Truncate("  "+qualified+"  "+typeLabel, width, "…"), "")
	}
	lines = append(lines, "Constraints require full schema metadata.")
	return lines
}
