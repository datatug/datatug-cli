package chat

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/tugql"
	"github.com/datatug/datatug-cli/pkg/queryops"
	"github.com/datatug/datatug-cli/pkg/resultops"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/strongo/aichat/tui/chatshell"
)

type derivedOperation struct {
	Group      bool
	Columns    []string
	Count      bool
	Aggregates []resultops.AggregateTerm
	Sort       []queryops.SortTerm
	Scope      queryops.Scope
}
type recursiveTugQLExecutor interface {
	RunRecursiveTugQL(context.Context, string, []byte, map[string]any) (secureread.Result, error)
}
type exactAggregateTugQLExecutor interface {
	RunExactAggregateTugQL(context.Context, string, []byte, []byte, map[string]any, []resultops.Column) (secureread.ExactAggregateResult, error)
}

func exactAggregateColumns(catalog ProjectCatalog, record RecordSet) ([]resultops.Column, error) {
	query, err := tugqlDeserialize([]byte(record.TugQL))
	if err != nil {
		return nil, fmt.Errorf("original query is unavailable")
	}
	if _, direct := query.From().Base().(dal.CollectionRef); !direct || len(query.From().Joins()) != 0 || dal.HasAggregation(query) || query.Having() != nil {
		return nil, fmt.Errorf("exact server aggregates currently need a direct single-source query without existing aggregates or HAVING")
	}
	if err = secureread.ValidateRecursiveQuery(query); err != nil {
		return nil, err
	}
	columns := make([]resultops.Column, len(record.Result.Columns))
	for i, name := range record.Result.Columns {
		meta := columnMetaFor(catalog, &record, name)
		if meta.dbType == "" {
			return nil, fmt.Errorf("exact server aggregation needs authoritative types for every projected column")
		}
		columns[i] = resultops.Column{Name: name, Type: meta.dbType}
	}
	return columns, nil
}

// A bounded original output needs an ordering that identifies its complete
// rows. A same-named column alone is never evidence of a unique identity.
func deterministicOriginal(catalog ProjectCatalog, record RecordSet, query dal.StructuredQuery) bool {
	if query.Limit() == 0 && query.Offset() == 0 {
		return true
	}
	if dal.HasAggregation(query) && len(query.GroupBy()) == 0 {
		return true
	}
	ordered := map[string]bool{}
	for _, order := range query.OrderBy() {
		ordered[order.Expression().String()] = true
	}
	if len(query.GroupBy()) > 0 {
		for _, key := range query.GroupBy() {
			if !ordered[key.String()] {
				return false
			}
		}
		return true
	}
	instances := relationInstances(query.From())
	if len(instances) != 1 {
		return false
	}
	root := instances[0]
	for _, object := range catalog.Objects {
		parts := strings.SplitN(object.Reference.ObjectID, ".", 2)
		if len(parts) == 1 {
			parts = []string{"main", parts[0]}
		}
		if object.Reference.SourceID != record.Database || len(object.PrimaryKey) == 0 || !sameRelation(root.Schema, root.Relation, parts[0], parts[1]) {
			continue
		}
		for _, key := range object.PrimaryKey {
			found := false
			for _, order := range query.OrderBy() {
				if field, ok := order.Expression().(dal.FieldRef); ok && strings.EqualFold(field.Name(), key) && (field.Source() == "" || strings.EqualFold(field.Source(), root.Alias) || strings.EqualFold(field.Source(), root.Relation)) {
					found = true
				}
			}
			if !found {
				return false
			}
		}
		return true
	}
	return false
}

func deriveResultPreview(catalog ProjectCatalog, record RecordSet, operation derivedOperation) (queryops.Preview, error) {
	query, err := tugql.Deserialize([]byte(record.TugQL))
	if err != nil {
		return queryops.Preview{}, fmt.Errorf("original TugQL is unavailable")
	}
	if query.Limit() > maxRows {
		return queryops.Preview{}, fmt.Errorf("original query exceeds the %d row execution budget", maxRows)
	}
	if operation.Scope == queryops.OriginalOutput && !deterministicOriginal(catalog, record, query) {
		return queryops.Preview{}, fmt.Errorf("original query output is limited without a proven unique ordering; choose Matching source rows or edit the original query")
	}
	var preview queryops.Preview
	if operation.Group {
		terms := make([]queryops.AggregateTerm, len(operation.Aggregates))
		for i, term := range operation.Aggregates {
			terms[i] = queryops.AggregateTerm{Column: term.Column, Function: term.Function, Alias: term.Alias}
		}
		preview, err = queryops.DeriveGroupAggregates(record.TugQL, operation.Columns, operation.Count, terms, operation.Scope)
	} else {
		preview, err = queryops.DeriveSort(record.TugQL, operation.Sort, operation.Scope)
	}
	if err != nil {
		return preview, err
	}
	derived, err := tugqlDeserialize([]byte(preview.TugQL))
	if err != nil || derived.Limit() < 1 || derived.Limit() > maxRows {
		return queryops.Preview{}, fmt.Errorf("derived query exceeds the bounded execution budget")
	}
	return preview, nil
}

func (c *SessionChat) executeDerivedResult(ctx context.Context, sessionID, parentID string, operation derivedOperation, expectedDocument string) (ChatSession, QueryResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeID != sessionID {
		return ChatSession{}, QueryResult{}, ErrActiveSessionChanged
	}
	snapshot, err := c.store.Load(ctx, sessionID)
	if err != nil {
		return ChatSession{}, QueryResult{}, err
	}
	parent, ok := snapshot.RecordSets[parentID]
	if !ok || c.queryExecutor == nil {
		return ChatSession{}, QueryResult{}, fmt.Errorf("origin result cannot execute a derived query")
	}
	preview, err := deriveResultPreview(c.catalog, parent, operation)
	if err != nil {
		return ChatSession{}, QueryResult{}, err
	}
	if preview.TugQL != expectedDocument {
		return ChatSession{}, QueryResult{}, fmt.Errorf("query preview changed; review it again")
	}
	live, ok := c.store.info.Sources[parent.Database]
	if !ok || !storedSourceNames(parent.Source, live) {
		return ChatSession{}, QueryResult{}, fmt.Errorf("origin source is no longer available")
	}
	if err = ctx.Err(); err != nil {
		return ChatSession{}, QueryResult{}, err
	}
	var result secureread.Result
	if operation.Group && len(operation.Aggregates) > 0 {
		executor, ok := c.queryExecutor.(exactAggregateTugQLExecutor)
		if !ok {
			return ChatSession{}, QueryResult{}, fmt.Errorf("this executor cannot run exact server aggregates")
		}
		columns, columnErr := exactAggregateColumns(c.catalog, parent)
		if columnErr != nil {
			return ChatSession{}, QueryResult{}, columnErr
		}
		exact, exactErr := executor.RunExactAggregateTugQL(ctx, live, []byte(parent.TugQL), []byte(preview.TugQL), parent.Parameters, columns)
		result, err = exact.Result, exactErr
		result.ColumnTypes = exact.ColumnTypes
	} else if operation.Scope == queryops.OriginalOutput {
		executor, ok := c.queryExecutor.(recursiveTugQLExecutor)
		if !ok {
			return ChatSession{}, QueryResult{}, fmt.Errorf("this executor cannot safely run Original query output")
		}
		result, err = executor.RunRecursiveTugQL(ctx, live, []byte(preview.TugQL), parent.Parameters)
	} else {
		result, err = c.queryExecutor.RunTugQL(ctx, live, []byte(preview.TugQL), parent.Parameters)
	}
	if err != nil {
		return ChatSession{}, QueryResult{}, fmt.Errorf("derived query failed: %s", publicQueryError(err, parent.Parameters))
	}
	if err = ctx.Err(); err != nil {
		return ChatSession{}, QueryResult{}, err
	}
	action := "Sort"
	if operation.Group {
		action = "Group"
	}
	origin, err := c.store.AppendUser(ctx, sessionID, action+": "+parent.Title)
	if err != nil {
		return ChatSession{}, QueryResult{}, err
	}
	query, err := c.store.AppendQuery(ctx, sessionID, origin.ID, live, QueryResult{Title: action + ": " + parent.Title, TugQL: preview.TugQL, Source: live, SourceID: parent.Database, Parameters: parent.Parameters, Result: result, Lineage: &JoinLineage{ParentRecordSetID: parent.ID, AppliedEdges: append([]AppliedJoinEdge(nil), appliedEdges(parent)...)}})
	if err != nil {
		return ChatSession{}, QueryResult{}, err
	}
	c.notifyChanged()
	snapshot, err = c.store.Load(ctx, sessionID)
	return snapshot, query, err
}

func (o *resultOperationOverlay) operation() derivedOperation {
	scope := queryops.MatchingSource
	if o.originalOutput {
		scope = queryops.OriginalOutput
	}
	op := derivedOperation{Group: o.group, Columns: o.names(), Count: o.count, Scope: scope}
	if o.group {
		op.Aggregates = o.aggregateTerms()
	}
	for _, key := range o.sortKeys() {
		op.Sort = append(op.Sort, queryops.SortTerm{Column: key.Column, Descending: key.Descending})
	}
	return op
}
func (o *resultOperationOverlay) serverPreview() (queryops.Preview, error) {
	if len(o.selected) == 0 {
		return queryops.Preview{}, fmt.Errorf("select at least one column")
	}
	if !o.valid() {
		return queryops.Preview{}, fmt.Errorf("selected result changed; reopen this dialog")
	}
	if o.ui.sessions == nil {
		return queryops.Preview{}, fmt.Errorf("no policy executor is configured")
	}
	if o.group && len(o.aggregateTerms()) > 0 {
		if _, ok := o.ui.sessions.queryExecutor.(exactAggregateTugQLExecutor); !ok {
			return queryops.Preview{}, fmt.Errorf("executor cannot run exact server aggregates")
		}
		if _, err := exactAggregateColumns(o.ui.catalog, o.record); err != nil {
			return queryops.Preview{}, err
		}
	}
	if o.originalOutput {
		if _, ok := o.ui.sessions.queryExecutor.(recursiveTugQLExecutor); !ok {
			return queryops.Preview{}, fmt.Errorf("executor cannot safely run Original query output")
		}
	}
	preview, err := deriveResultPreview(o.ui.catalog, o.record, o.operation())
	if err == nil && o.originalOutput && (!o.group || len(o.aggregateTerms()) <= 0) {
		query, parseErr := tugqlDeserialize([]byte(preview.TugQL))
		if parseErr != nil {
			return queryops.Preview{}, parseErr
		}
		err = secureread.ValidateRecursiveQuery(query)
	}
	return preview, err
}
func (o *resultOperationOverlay) reviewServer() tea.Cmd {
	preview, err := o.serverPreview()
	if err != nil {
		o.err = sanitizeTerminalText(err.Error())
		return nil
	}
	query, _ := tugql.Deserialize([]byte(o.record.TugQL))
	scope := "Matching source rows · input count unknown\nSource, joins, WHERE and parameters retained; original ordering replaced.\nOriginal LIMIT is the new output cap; OFFSET applies after this operation."
	if o.originalOutput {
		scope = "Original query output · input count unknown\nOriginal projection, LIMIT, OFFSET, GROUP BY, aggregates and HAVING remain inside the subquery.\nThis operation transforms those output rows, including aggregate rows."
	}
	ordering := "Server-native collation/null ordering; ties are not guaranteed stable."
	if o.originalOutput {
		ordering = "DALgo evaluates the outer operation locally over policy-admitted source rows; ordering uses its generic evaluator. Ties are not guaranteed stable."
	}
	if o.group && len(o.aggregateTerms()) > 0 {
		ordering = "The bounded typed aggregator reads only policy-admitted rows. INTEGER/DECIMAL sums are exact; finite FLOAT sums use sequential IEEE754 arithmetic. COUNT ignores null cells; missing/unsupported values refuse execution. Group order follows first occurrence."
	}
	text := fmt.Sprintf("%s\nOriginal LIMIT: %d · OFFSET: %d · GROUP BY keys: %d · HAVING: %t\n%s\n\nExact TugQL submitted:\n%s", scope, query.Limit(), query.Offset(), len(query.GroupBy()), query.Having() != nil, ordering, preview.TugQL)
	p := &resultServerPreviewOverlay{operation: o, preview: preview, viewport: viewport.New(), text: text}
	return o.ui.shell.PushOverlay(p)
}

type resultServerPreviewOverlay struct {
	operation *resultOperationOverlay
	preview   queryops.Preview
	viewport  viewport.Model
	text      string
	err       string
	controlY  int
}

func (*resultServerPreviewOverlay) LocalMouseCoordinates() bool { return true }

func (p *resultServerPreviewOverlay) HelpHints() []string {
	return []string{"Exact server query review", "↑↓ / PgUp/PgDn scroll", "Shift+Enter execute exact preview", "Esc return to retained choices"}
}

func (p *resultServerPreviewOverlay) View(w, h int) string {
	w = max(1, w-2) // workspace surface owns a marker and content inset
	p.viewport.SetWidth(max(1, w))
	wrapped := ansi.Wrap(p.text, max(1, w), " ")
	p.viewport.SetHeight(max(1, min(h-3, strings.Count(wrapped, "\n")+1)))
	p.viewport.SetContent(wrapped)
	p.controlY = 1 + strings.Count(p.viewport.View(), "\n") + 1
	return "Review exact server query · ↑↓ / PgUp/PgDn scroll\n" + p.viewport.View() + "\nRun on server ⇧↵ · Esc return to choices\n" + p.err
}
func (p *resultServerPreviewOverlay) Update(msg tea.Msg) (chatshell.Overlay, tea.Cmd, bool) {
	if click, ok := msg.(tea.MouseClickMsg); ok && click.Button == tea.MouseLeft && click.Y == p.controlY {
		if click.X >= 0 && click.X < ansi.StringWidth("Run on server ⇧↵") {
			return p.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
		}
		if click.X >= ansi.StringWidth("Run on server ⇧↵ · ") {
			return p.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
		}
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc":
			return p, p.operation.ui.shell.PushOverlay(p.operation), true
		case "shift+enter":
			o := p.operation
			current, err := o.serverPreview()
			if err != nil || current.TugQL != p.preview.TugQL {
				p.err = "Context or query changed; return to choices and review again."
				return p, nil, false
			}
			if o.ui.workspaceMutationPending || o.ui.backendBusy() {
				p.err = "Another result operation is still running."
				return p, nil, false
			}
			o.ui.workspaceMutationPending = true
			sessionID, generation, parentID, rowKey, entryID := o.sessionID, o.generation, o.record.ID, o.grid.sourceRowKey(), o.grid.entryID
			sessions, operation, document := o.ui.sessions, o.operation(), p.preview.TugQL
			interaction := o.ui.workspaceInteraction
			operationID, ctx := o.ui.beginOperation("Server " + map[bool]string{true: "Group", false: "Sort"}[o.group])
			return p, func() tea.Msg {
				snapshot, query, err := sessions.executeDerivedResult(ctx, sessionID, parentID, operation, document)
				return workspaceRelatedOpenedMsg{interaction: interaction, operationID: operationID, sessionID: sessionID, generation: generation, parentID: parentID, rowKey: rowKey, originEntry: entryID, snapshot: snapshot, query: query, err: err, label: query.Title}
			}, true
		}
	}
	var cmd tea.Cmd
	p.viewport, cmd = p.viewport.Update(msg)
	return p, cmd, false
}
