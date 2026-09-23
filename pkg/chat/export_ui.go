package chat

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
)

type exportMessage struct {
	sessionID    string
	sessionTitle string
	path         string
	count        int
	err          error
}

// exportCommand is deliberately explicit about scope and target. It exports
// immutable in-session snapshots; no model call or database requery occurs.
func (u *UI) exportCommand(argument string) (tea.Cmd, error) {
	usage := func() error {
		return fmt.Errorf("usage: /export current|bucket <csv|json|yaml|ingr|dbf|sqlite|xlsx> <path>")
	}
	scope, remainder, ok := strings.Cut(strings.TrimSpace(argument), " ")
	if !ok {
		return nil, usage()
	}
	formatName, path, ok := strings.Cut(strings.TrimSpace(remainder), " ")
	if !ok || strings.TrimSpace(path) == "" {
		return nil, usage()
	}
	format, err := ParseExportFormat(formatName)
	if err != nil {
		return nil, err
	}
	var records []RecordSet
	switch scope {
	case "current":
		if u.activeGrid < 0 || u.activeGrid >= len(u.entries) {
			return nil, fmt.Errorf("select a RecordSet before exporting current")
		}
		id := u.entries[u.activeGrid].recordSetID
		record, ok := u.snapshot.RecordSets[id]
		if !ok {
			return nil, fmt.Errorf("selected RecordSet is unavailable")
		}
		records = append(records, record)
	case "bucket":
		for _, id := range u.snapshot.Workspace.ExportBucket {
			record, ok := u.snapshot.RecordSets[id]
			if !ok {
				return nil, fmt.Errorf("export bucket contains an unavailable RecordSet")
			}
			records = append(records, record)
		}
	default:
		return nil, fmt.Errorf("export scope must be current or bucket")
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("export bucket is empty; add a RecordSet with B")
	}
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(home, path[2:])
	}
	extension := "." + string(format)
	if scope == "bucket" && format != ExportXLSX && format != ExportSQLite {
		extension = ".zip"
	}
	if filepath.Ext(path) == "" {
		path += extension
	}
	if !strings.EqualFold(filepath.Ext(path), extension) {
		return nil, fmt.Errorf("%s export needs a %s path", scope, extension)
	}
	ctx := u.ctx
	sessionID, sessionTitle := u.sessionID, u.sessionTitle
	u.busy, u.exporting = true, true
	return func() tea.Msg {
		if scope == "bucket" {
			err = ExportBucketFile(ctx, records, format, path)
		} else {
			err = ExportRecordSetFile(ctx, records[0], format, path)
		}
		return exportMessage{sessionID: sessionID, sessionTitle: sessionTitle, path: path, count: len(records), err: err}
	}, nil
}
