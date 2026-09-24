package chat

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dtql"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

type relatedRecord struct {
	key    ForeignKey
	result secureread.Result
}

type relatedPreviewMessage struct {
	sequence int
	related  []relatedRecord
	err      error
}

type cellDetail struct {
	sequence     int
	title        string
	column       string
	value        any
	columns      []string
	values       []any
	qualified    string
	dbType       string
	loading      bool
	related      []relatedRecord
	relatedError error
	offset       int
}

func (d *cellDetail) copyValue() string { return FormatValue(d.value) }

func (u *UI) openCellDetail() tea.Cmd {
	g, entry, record := u.activeInspectorGrid()
	selectedColumn := 0
	var row []any
	if g != nil {
		selectedColumn = g.SelectedColumn()
		row = g.rawRow(g.CurrentIndex())
	}
	if g == nil || row == nil || selectedColumn < 0 || selectedColumn >= len(g.Columns()) {
		return nil
	}
	u.detailSequence++
	columns := make([]string, len(g.Columns()))
	for i, column := range g.Columns() {
		columns[i] = column.Name
	}
	meta := u.columnMeta(record, columns[selectedColumn])
	var value any
	if selectedColumn < len(row) {
		value = row[selectedColumn]
	}
	d := &cellDetail{sequence: u.detailSequence, title: g.baseTitle, column: columns[selectedColumn], value: value, columns: columns, values: append([]any(nil), row...), qualified: meta.qualified, dbType: meta.dbType}
	u.detail = d
	if record == nil || entry == nil || u.sessions == nil || meta.qualified == "" || meta.qualified == "ambiguous source" {
		return nil
	}
	application, ok := u.sessions.joinApplication.(ForeignKeyJoinApplication)
	if !ok {
		return nil
	}
	// All values come from the structured result. Physical provenance is
	// established by columnMeta; duplicate/derived names are never guessed.
	physical := map[string]any{}
	for i, column := range columns {
		resolved := u.columnMeta(record, column)
		if resolved.qualified != "" && resolved.qualified != "ambiguous source" && i < len(row) {
			physical[strings.ToLower(resolved.qualified)] = row[i]
		}
	}
	d.loading = true
	sequence, selected := d.sequence, meta.qualified
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(u.ctx, 10*time.Second)
		defer cancel()
		related, err := application.PreviewRelated(ctx, *record, selected, physical)
		return relatedPreviewMessage{sequence: sequence, related: related, err: err}
	}
}

// PreviewRelated resolves only an authoritative outgoing FK and reads up to
// five matching records through the same DTQL/policy executor as chat queries.
func (a ForeignKeyJoinApplication) PreviewRelated(ctx context.Context, record RecordSet, selected string, row map[string]any) ([]relatedRecord, error) {
	if record.Source != a.Source || a.Executor == nil {
		return nil, nil
	}
	snapshot, err := a.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	var previews []relatedRecord
	for _, fk := range snapshot.Keys {
		prefix := strings.ToLower(fk.Schema + "." + fk.FromRelation + ".")
		selectedField := strings.TrimPrefix(strings.ToLower(selected), prefix)
		if !strings.HasPrefix(strings.ToLower(selected), prefix) {
			continue
		}
		found := false
		for _, field := range fk.FromFields {
			if strings.EqualFold(field, selectedField) {
				found = true
				break
			}
		}
		if !found || len(fk.FromFields) != len(fk.ToFields) {
			continue
		}
		conditions := make([]dal.Condition, 0, len(fk.FromFields))
		complete := true
		for i, field := range fk.FromFields {
			value, ok := row[prefix+strings.ToLower(field)]
			if !ok || value == nil {
				complete = false
				break
			}
			conditions = append(conditions, dal.NewComparison(dal.NewFieldRef("", fk.ToFields[i]), dal.Equal, dal.NewConstant(value)))
		}
		if !complete {
			continue
		}
		target := RelationInstance{Schema: fk.ToSchema, Relation: fk.ToRelation}
		if a.CanReadTarget != nil && a.CanReadTarget(ctx, target) != nil {
			continue
		}
		if a.Secure && a.CanReadTarget == nil {
			continue
		}
		collection := dal.NewRootCollectionRef(fk.ToRelation, "")
		if fk.ToSchema != "" && !strings.EqualFold(fk.ToSchema, "main") {
			collection = dal.NewQualifiedRootCollectionRef(fk.ToSchema, fk.ToRelation, "")
		}
		query := dal.From(collection).NewQuery().Where(conditions...).Limit(5).SelectColumns()
		doc, err := dtql.Serialize(query)
		if err != nil {
			return nil, err
		}
		result, err := a.Executor.RunDTQL(ctx, a.Source, doc, nil)
		if err != nil {
			return nil, err
		}
		previews = append(previews, relatedRecord{key: fk, result: result})
	}
	return previews, nil
}

func (u *UI) detailOverlay(background string) string {
	d := u.detail
	if d == nil {
		return background
	}
	width := max(20, min(u.width-4, 84))
	height := max(8, min(u.height-4, 28))
	inside := max(1, width-6)
	lines := []string{"Row · " + d.title, ""}
	for i, name := range d.columns {
		value := "NULL"
		if i < len(d.values) {
			value = formatGridValue(name, d.values[i])
		}
		marker := "  "
		if name == d.column {
			marker = "› "
		}
		lines = append(lines, marker+name+": "+value)
	}
	lines = append(lines, "", "Cell · "+d.column, "Type: "+nonempty(d.dbType, "unknown"), "Source: "+nonempty(d.qualified, "unavailable"), "Value: "+FormatValue(d.value))
	if d.loading {
		lines = append(lines, "", "Related records: loading…")
	}
	if d.relatedError != nil {
		lines = append(lines, "", "Related records unavailable.")
	}
	for _, related := range d.related {
		lines = append(lines, "", fmt.Sprintf("FK %s → %s.%s", related.key.ConstraintID, related.key.ToSchema, related.key.ToRelation))
		for i, sourceField := range related.key.FromFields {
			if i < len(related.key.ToFields) {
				lines = append(lines, "  "+related.key.FromRelation+"."+sourceField+" → "+related.key.ToRelation+"."+related.key.ToFields[i])
			}
		}
		for _, object := range u.catalog.Objects {
			if !strings.EqualFold(object.Reference.ObjectID, related.key.ToSchema+"."+related.key.ToRelation) {
				continue
			}
			if len(object.Columns) > 0 {
				meta := make([]string, 0, len(object.Columns))
				for _, column := range object.Columns {
					label := column
					if kind := object.ColumnTypes[column]; kind != "" {
						label += " " + kind
					}
					meta = append(meta, label)
				}
				lines = append(lines, "Columns: "+strings.Join(meta, " · "))
			}
			break
		}
		lines = append(lines, fmt.Sprintf("Related records (showing %d, max 5):", len(related.result.Rows)))
		for index, row := range related.result.Rows {
			lines = append(lines, fmt.Sprintf("  Record %d", index+1))
			for _, column := range related.result.Columns {
				lines = append(lines, "    "+column+": "+formatGridValue(column, row.Data[column]))
			}
		}
		if len(related.result.Rows) == 0 {
			lines = append(lines, "No matching records")
		}
	}
	if len(d.related) == 0 && !d.loading && d.relatedError == nil {
		lines = append(lines, "", "No related FK records for this cell")
	}
	visible := max(1, height-5)
	d.offset = min(d.offset, max(0, len(lines)-visible))
	end := min(len(lines), d.offset+visible)
	shown := make([]string, 0, visible+2)
	for _, line := range lines[d.offset:end] {
		shown = append(shown, ansi.Truncate(sanitizeTerminalText(line), inside, "…"))
	}
	for len(shown) < visible {
		shown = append(shown, "")
	}
	shown = append(shown, fmt.Sprintf("↑↓ scroll · Y copy cell · Esc close   %d–%d/%d", d.offset+1, end, len(lines)))
	box := lipgloss.NewStyle().Width(width-2).Height(height-2).Padding(0, 1).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("51")).Background(lipgloss.Color("235")).Foreground(lipgloss.Color("252")).Render(strings.Join(shown, "\n"))
	canvas := lipgloss.NewCanvas(u.width, u.height)
	canvas.Compose(lipgloss.NewLayer(background))
	canvas.Compose(lipgloss.NewLayer(box).X(max(0, (u.width-lipgloss.Width(box))/2)).Y(max(0, (u.height-lipgloss.Height(box))/2)))
	return canvas.Render()
}

func nonempty(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
