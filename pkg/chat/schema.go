package chat

import (
	"fmt"
	"strings"

	"github.com/datatug/datatug-cli/pkg/api"
)

// FormatSchemaContext converts DataTug's stored schema into a compact prompt
// fragment. It is deterministic so model and snapshot tests stay stable.
func FormatSchemaContext(schema *api.CatalogSchema) string {
	if schema == nil || len(schema.Relations) == 0 {
		return "(no scanned tables or views)"
	}
	var b strings.Builder
	for _, relation := range schema.Relations {
		fmt.Fprintf(&b, "- %s (schema: %s; %s): ", relation.Name, relation.Schema, relation.DbType)
		for i, column := range relation.Columns {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(column.Name)
			if column.DbType != "" {
				fmt.Fprintf(&b, " [%s]", column.DbType)
			}
		}
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}
