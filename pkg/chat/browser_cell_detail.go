package chat

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

type BrowserRelatedRecords struct {
	ConstraintID string
	Target       string
	Columns      []string
	Rows         []secureread.Row
}

type BrowserCellDetail struct {
	Title     string
	Column    string
	Value     any
	Row       map[string]any
	Qualified string
	DBType    string
	Related   []BrowserRelatedRecords
}

func (c *SessionChat) CellDetailActive(ctx context.Context, sessionID, recordSetID string, rowIndex int, column string) (BrowserCellDetail, error) {
	c.mu.Lock()
	if c.activeID != sessionID {
		c.mu.Unlock()
		return BrowserCellDetail{}, ErrActiveSessionChanged
	}
	session, err := c.store.Load(ctx, c.activeID)
	catalog := c.catalog
	application, hasApplication := c.joinApplication.(ForeignKeyJoinApplication)
	c.mu.Unlock()
	if err != nil {
		return BrowserCellDetail{}, err
	}
	record, ok := session.RecordSets[recordSetID]
	if !ok || rowIndex < 0 || rowIndex >= len(record.Result.Rows) {
		return BrowserCellDetail{}, fmt.Errorf("result row unavailable")
	}
	found := false
	for _, name := range record.Result.Columns {
		if name == column {
			found = true
			break
		}
	}
	if !found {
		return BrowserCellDetail{}, fmt.Errorf("result column unavailable")
	}
	row := record.Result.Rows[rowIndex]
	meta := columnMetaFor(catalog, &record, column)
	detail := BrowserCellDetail{Title: record.Title, Column: column, Value: row.Data[column], Row: row.Data, Qualified: meta.qualified, DBType: meta.dbType, Related: []BrowserRelatedRecords{}}
	if !hasApplication || meta.qualified == "" || meta.qualified == "ambiguous source" {
		return detail, nil
	}
	physical := map[string]any{}
	for _, name := range record.Result.Columns {
		resolved := columnMetaFor(catalog, &record, name)
		if resolved.qualified != "" && resolved.qualified != "ambiguous source" {
			physical[strings.ToLower(resolved.qualified)] = row.Data[name]
		}
	}
	previewCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	previews, err := application.PreviewRelated(previewCtx, record, meta.qualified, physical)
	if err != nil {
		return detail, fmt.Errorf("related records unavailable")
	}
	for _, preview := range previews {
		detail.Related = append(detail.Related, BrowserRelatedRecords{ConstraintID: preview.key.ConstraintID, Target: preview.key.ToSchema + "." + preview.key.ToRelation, Columns: preview.result.Columns, Rows: preview.result.Rows})
	}
	return detail, nil
}
