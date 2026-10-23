package chat

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/strongo/aichat/ai"
	"github.com/strongo/aichat/ai/agent"
)

const toolResultColumns = "result_columns"
const toolApplyResultColumns = "apply_result_columns"

type resultColumnsContextKey struct{}
type resultColumnsCallbacks struct {
	plan  func(context.Context, string, []string) ([]ResultColumnRow, ResultColumnsPreview, error)
	apply func(context.Context, string, string) (QueryResult, error)
}
type resultColumnsArgs struct {
	RecordSetID  string   `json:"recordSetId" jsonschema:"Exact persisted RecordSet ID returned by this turn's run_tugql"`
	CandidateIDs []string `json:"candidateIds,omitempty" jsonschema:"Omit to discover admitted omitted columns; supply exact opaque IDs to prepare one enrichment plan"`
}
type applyResultColumnsArgs struct {
	RecordSetID string `json:"recordSetId" jsonschema:"Exact RecordSet ID used to prepare the column plan"`
	Fingerprint string `json:"fingerprint" jsonschema:"Exact fingerprint returned by result_columns for the chosen columns"`
}
type resultColumnsResponse struct {
	OK          bool                  `json:"ok"`
	Columns     []ResultColumnRow     `json:"columns,omitempty"`
	Preview     *ResultColumnsPreview `json:"preview,omitempty"`
	RecordSetID string                `json:"recordSetId,omitempty"`
	Error       string                `json:"error,omitempty"`
}

func (c *AIConversation) resultColumnsTools() []ai.Tool {
	return []ai.Tool{
		{Name: toolResultColumns, Description: "Discover permitted omitted fields or prepare a safe display-column plan for the result just executed in this turn. Use schema-supported names/labels beside keys when useful; respect explicit user projections. Metadata only; no row reads. Never author JOIN/ON. Supply several candidate IDs in one plan.", Schema: argsSchema(resultColumnsArgs{})},
		{Name: toolApplyResultColumns, Description: "Automatically execute this turn's exact metadata-derived column plan once, within the user's query authorization. Requires its exact fingerprint; derives and securely executes one immutable child query. This is automatic model enrichment, never human-reviewed. Do not use when the user requested only explicit columns.", Schema: argsSchema(applyResultColumnsArgs{})},
	}
}

func (c *AIConversation) installResultColumnsHandlers(handlers map[string]agent.Handler) {
	handlers[toolResultColumns] = func(ctx context.Context, call ai.ToolCall) (ai.ToolResult, error) {
		var args resultColumnsArgs
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return errorResult(call.ID, "Invalid column plan arguments"), nil
		}
		callbacks, ok := ctx.Value(resultColumnsContextKey{}).(resultColumnsCallbacks)
		if !ok {
			return jsonResult(call.ID, resultColumnsResponse{Error: "Column planning is unavailable in this chat"}), nil
		}
		rows, preview, err := callbacks.plan(ctx, args.RecordSetID, args.CandidateIDs)
		if err != nil {
			return jsonResult(call.ID, resultColumnsResponse{Error: conciseError(err)}), nil
		}
		response := resultColumnsResponse{OK: true, Columns: rows}
		if len(args.CandidateIDs) > 0 {
			response.Preview = &preview
		}
		return jsonResult(call.ID, response), nil
	}
	handlers[toolApplyResultColumns] = func(ctx context.Context, call ai.ToolCall) (ai.ToolResult, error) {
		var args applyResultColumnsArgs
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return errorResult(call.ID, "Invalid column execution arguments"), nil
		}
		callbacks, ok := ctx.Value(resultColumnsContextKey{}).(resultColumnsCallbacks)
		if !ok {
			return jsonResult(call.ID, resultColumnsResponse{Error: "Column planning is unavailable in this chat"}), nil
		}
		query, err := callbacks.apply(ctx, args.RecordSetID, args.Fingerprint)
		if err != nil {
			return jsonResult(call.ID, resultColumnsResponse{Error: conciseError(err)}), nil
		}
		// Already persisted by the trusted callback under the session lock.
		c.mu.Lock()
		c.pending = append(c.pending, query)
		c.joinApplied = true
		c.mu.Unlock()
		return jsonResult(call.ID, resultColumnsResponse{OK: true, RecordSetID: query.RecordSetID}), nil
	}
}

// Called by prepareTurn while SessionChat.mu is held. These callbacks MUST NOT
// re-lock it. Authorization is bounded to the just-persisted user message and
// to the exact opaque plans produced within that turn; historical IDs refuse.
func (c *SessionChat) resultColumnsContext(ctx context.Context, originID string) context.Context {
	sessionID := c.activeID
	calls := 0
	applied := false
	type prepared struct {
		parent string
		ids    []string
	}
	plans := map[string]prepared{}
	check := func(ctx context.Context, id string) (RecordSet, error) {
		if err := ctx.Err(); err != nil {
			return RecordSet{}, err
		}
		if c.activeID != sessionID {
			return RecordSet{}, ErrActiveSessionChanged
		}
		s, err := c.store.Load(ctx, sessionID)
		if err != nil {
			return RecordSet{}, err
		}
		r, ok := s.RecordSets[id]
		if !ok || r.OriginMessageID != originID {
			return RecordSet{}, fmt.Errorf("automatic enrichment is limited to a query from the current user turn")
		}
		return r, nil
	}
	callbacks := resultColumnsCallbacks{
		plan: func(ctx context.Context, id string, ids []string) ([]ResultColumnRow, ResultColumnsPreview, error) {
			calls++
			if calls > 4 || applied {
				return nil, ResultColumnsPreview{}, fmt.Errorf("column planning limit reached for this turn")
			}
			r, err := check(ctx, id)
			if err != nil {
				return nil, ResultColumnsPreview{}, err
			}
			m, a, err := c.resultColumnEvidence(ctx, r)
			if err != nil {
				return nil, ResultColumnsPreview{}, err
			}
			if len(ids) == 0 {
				rows, err := discoverResultColumns(r, m, a)
				return rows, ResultColumnsPreview{}, err
			}
			p, err := planResultColumns(r, m, a, ids)
			if err == nil {
				plans[p.Fingerprint] = prepared{parent: id, ids: append([]string(nil), ids...)}
			}
			return nil, p, err
		},
		apply: func(ctx context.Context, id, fingerprint string) (QueryResult, error) {
			if applied {
				return QueryResult{}, fmt.Errorf("automatic column enrichment already executed in this turn")
			}
			p, ok := plans[fingerprint]
			if !ok || p.parent != id {
				return QueryResult{}, fmt.Errorf("use the exact current metadata plan fingerprint")
			}
			if _, err := check(ctx, id); err != nil {
				return QueryResult{}, err
			}
			applied = true
			_, query, err := c.executeResultColumns(ctx, sessionID, id, p.ids, fingerprint, originID)
			return query, err
		},
	}
	return context.WithValue(ctx, resultColumnsContextKey{}, callbacks)
}
