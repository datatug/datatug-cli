package chat

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/dal-go/dalgo/dtql"
)

func httpResponseChanged(current, previous HTTPResponse) bool {
	return current.StatusCode != previous.StatusCode || current.ContentType != previous.ContentType || !bytes.Equal(current.Body, previous.Body)
}

// RefreshRecordSet re-executes the exact stored DTQL through the normal,
// policy-bound DataTug executor and appends an immutable result version.
func (c *SessionChat) RefreshRecordSet(ctx context.Context, sessionID, recordSetID string) (ChatSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeID != sessionID {
		return ChatSession{}, fmt.Errorf("the active chat session changed")
	}
	if c.queryExecutor == nil {
		return ChatSession{}, fmt.Errorf("query refresh is unavailable")
	}
	session, err := c.store.Load(ctx, sessionID)
	if err != nil {
		return ChatSession{}, err
	}
	record, ok := session.RecordSets[recordSetID]
	if !ok || record.HTTPResponseID != "" {
		return ChatSession{}, fmt.Errorf("query result is unavailable for refresh")
	}
	allowed := false
	for _, source := range c.store.info.Sources {
		if source == record.Source {
			allowed = true
			break
		}
	}
	if !allowed {
		return ChatSession{}, fmt.Errorf("the saved data source is no longer available")
	}
	doc := strings.TrimSpace(record.DTQL)
	query, err := dtql.Deserialize([]byte(doc))
	if err != nil || query.Limit() < 1 || query.Limit() > maxRows {
		return ChatSession{}, fmt.Errorf("the saved query is no longer valid")
	}
	result, err := c.queryExecutor.RunDTQL(ctx, record.Source, []byte(doc), record.Parameters)
	if err != nil {
		return ChatSession{}, fmt.Errorf("query failed: %s", publicQueryError(err, record.Parameters))
	}
	origin, err := c.store.AppendUser(ctx, session.ID, "Refresh: "+record.Title)
	if err != nil {
		return ChatSession{}, err
	}
	_, err = c.store.AppendQuery(ctx, session.ID, origin.ID, record.Source, QueryResult{Title: record.Title, DTQL: doc, Source: record.Source, SourceID: record.Database, Parameters: record.Parameters, Result: result, RefreshParentID: record.ID})
	if err != nil {
		return ChatSession{}, err
	}
	return c.store.Load(ctx, session.ID)
}

// hiddenRefreshVersions limits the visible card history while retaining every
// immutable snapshot in session storage. Raising the setting restores older
// cards without rerunning or losing their data.
func hiddenRefreshVersions(session ChatSession, keep int) (map[string]bool, map[string]bool) {
	if keep < 1 {
		keep = defaultResultVersionsToKeep
	}
	hiddenRecords, hiddenHTTP := map[string]bool{}, map[string]bool{}
	recordGroups := map[string][]RecordSet{}
	for _, record := range session.RecordSets {
		root := record.ID
		for seen := map[string]bool{}; !seen[root]; {
			seen[root] = true
			current, ok := session.RecordSets[root]
			if !ok || current.RefreshParentID == "" {
				break
			}
			root = current.RefreshParentID
		}
		recordGroups[root] = append(recordGroups[root], record)
	}
	for _, group := range recordGroups {
		sort.Slice(group, func(i, j int) bool { return group[i].CreatedAt.After(group[j].CreatedAt) })
		for _, record := range group[min(keep, len(group)):] {
			hiddenRecords[record.ID] = true
		}
	}
	httpGroups := map[string][]HTTPResponse{}
	for _, response := range session.HTTPResponses {
		root := response.ID
		for seen := map[string]bool{}; !seen[root]; {
			seen[root] = true
			current, ok := session.HTTPResponses[root]
			if !ok || current.RefreshParentID == "" {
				break
			}
			root = current.RefreshParentID
		}
		httpGroups[root] = append(httpGroups[root], response)
	}
	for _, group := range httpGroups {
		sort.Slice(group, func(i, j int) bool { return group[i].CreatedAt.After(group[j].CreatedAt) })
		for _, response := range group[min(keep, len(group)):] {
			hiddenHTTP[response.ID] = true
		}
	}
	return hiddenRecords, hiddenHTTP
}
