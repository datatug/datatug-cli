package chat

import (
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dtql"
)

func groupedRecord(record *RecordSet) bool {
	if record == nil {
		return false
	}
	query, err := dtql.Deserialize([]byte(record.DTQL))
	return err == nil && dal.HasAggregation(query)
}

// inspectionIdentity uses schema key metadata plus query projection provenance.
// Neither an Id-like name nor the current visual row number proves identity.
func (u *ChatUI) inspectionIdentity(g *gridState, record *RecordSet) string {
	fallback := sanitizeTerminalText(g.baseTitle) + " / identity unavailable"
	if record == nil || groupedRecord(record) {
		return fallback
	}
	query, err := dtql.Deserialize([]byte(record.DTQL))
	if err != nil {
		return fallback
	}
	root := relationInstances(query.From())[0]
	physical := u.physicalRow(g, record)
	for _, object := range u.catalog.Objects {
		if !strings.EqualFold(object.Reference.ObjectID, relationKey(root.Schema, root.Relation)) || (record.Database != "" && object.Reference.SourceID != record.Database) || len(object.PrimaryKey) == 0 {
			continue
		}
		parts := []string{sanitizeTerminalText(nonempty(object.Reference.Title, root.Relation))}
		for _, column := range object.PrimaryKey {
			value, ok := physical[strings.ToLower(object.Reference.ObjectID+"."+column)]
			if !ok || value == nil {
				return fallback
			}
			label := sanitizeTerminalText(FormatValue(value))
			if len(object.PrimaryKey) > 1 {
				label = sanitizeTerminalText(column) + "=" + label
			}
			parts = append(parts, label)
		}
		return strings.Join(parts, " / ")
	}
	return fallback
}

func (u *ChatUI) physicalRow(g *gridState, record *RecordSet) map[string]any {
	raw := g.rawRow(g.CurrentIndex())
	physical := map[string]any{}
	for i := range g.Columns() {
		if i >= len(record.Result.Columns) {
			continue
		}
		meta := u.columnMeta(record, record.Result.Columns[i])
		if meta.qualified != "" && meta.qualified != "ambiguous source" && i < len(raw) {
			physical[strings.ToLower(meta.qualified)] = raw[i]
		}
	}
	return physical
}
