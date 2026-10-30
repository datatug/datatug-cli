package chat

import (
	"strings"
)

// exportFormats and exportFileStem are ui.go's original export_dialog.go
// declarations, kept unchanged: chatui_export_overlay.go's
// exportDialogOverlay (the ChatUI port of exportDialog) still uses both.
var exportFormats = []string{"xlsx", "csv", "json", "yaml", "ingr", "dbf", "sqlite"}

func exportFileStem(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		if r == '/' || r == '\\' || r == '.' || r < 32 {
			b.WriteByte('-')
		} else {
			b.WriteRune(r)
		}
	}
	name = strings.Trim(b.String(), " .-")
	if name == "" || name == "." || name == ".." {
		return "recordset"
	}
	return name
}
