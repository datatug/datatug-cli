package chat

import (
	"context"
	"fmt"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/tugql"
)

func objectQueryDocument(object ProjectObject) (string, error) {
	if object.Reference.Kind != "table" && object.Reference.Kind != "view" && object.Reference.Kind != "project_view" {
		return "", fmt.Errorf("select a table or view")
	}
	parts := strings.SplitN(object.Reference.ObjectID, ".", 2)
	if len(parts) == 1 {
		parts = []string{"main", parts[0]}
	}
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("relation metadata is incomplete")
	}
	collection := dal.NewQualifiedRootCollectionRef(parts[0], parts[1], "")
	columns := make([]dal.Column, 0, len(object.Columns))
	for _, name := range object.Columns {
		columns = append(columns, dal.Column{Expression: dal.NewFieldRef("", name)})
	}
	doc, err := tugql.Serialize(dal.From(collection).NewQuery().Limit(100).SelectColumns(columns...))
	return string(doc), err
}

// executeWorkspaceQuery shares the policy-bound executor with chat. The
// source is resolved from the trusted catalog/store scope, never editable text.
func (c *SessionChat) executeWorkspaceQuery(ctx context.Context, sessionID string, ref ContextReference, document string) (ChatSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if sessionID != c.activeID {
		return ChatSession{}, ErrActiveSessionChanged
	}
	known := false
	for _, object := range c.catalog.Objects {
		if sameReference(object.Reference, ref) && object.Issue == "" && (ref.Kind == "table" || ref.Kind == "view" || ref.Kind == "project_view") {
			known = true
			break
		}
	}
	if !known {
		return ChatSession{}, fmt.Errorf("selected relation is unavailable")
	}
	source, ok := c.store.info.Sources[ref.SourceID]
	if !ok || c.queryExecutor == nil {
		return ChatSession{}, fmt.Errorf("this source cannot execute queries")
	}
	query, err := tugql.Deserialize([]byte(document))
	if err != nil || query.Limit() < 1 || query.Limit() > maxRows {
		return ChatSession{}, fmt.Errorf("use a valid TugQL query with a bounded limit (1–%d)", maxRows)
	}
	result, err := c.queryExecutor.RunTugQL(ctx, source, []byte(document), nil)
	if err != nil {
		return ChatSession{}, fmt.Errorf("query failed: %s", publicQueryError(err, nil))
	}
	if err := ctx.Err(); err != nil {
		return ChatSession{}, err
	}
	origin, err := c.store.AppendUser(ctx, sessionID, "Open data: "+ref.Title)
	if err != nil {
		return ChatSession{}, err
	}
	_, err = c.store.AppendQuery(ctx, sessionID, origin.ID, source, QueryResult{Title: ref.Title, Source: source, SourceID: ref.SourceID, TugQL: document, Result: result})
	if err != nil {
		return ChatSession{}, err
	}
	c.notifyChanged()
	return c.store.Load(ctx, sessionID)
}
