package chat

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"testing"

	"github.com/strongo/aichat/ai"
)

// This provider consumes the actual request/tool responses. It can only form
// the enrichment calls after receiving current opaque IDs and a fingerprint.
type columnJourneyProvider struct {
	t           *testing.T
	step        int
	requests    []ai.ChatRequest
	parent      string
	ids         []string
	fingerprint string
	explicit    bool
}

func (*columnJourneyProvider) Name() string { return "column-journey" }
func (p *columnJourneyProvider) Stream(_ context.Context, req ai.ChatRequest) iter.Seq2[ai.Event, error] {
	return func(yield func(ai.Event, error) bool) {
		p.requests = append(p.requests, req)
		if !strings.Contains(req.System, "Respect explicit user projections") || !strings.Contains(req.System, "apply_result_columns") {
			p.t.Fatal("actual system request lost column guidance")
		}
		found := map[string]bool{}
		for _, tool := range req.Tools {
			found[tool.Name] = true
		}
		if !found[toolResultColumns] || !found[toolApplyResultColumns] {
			p.t.Fatal("actual request lost column tools")
		}
		var response resultColumnsResponse
		var run runTugQLResponse
		for _, message := range req.Messages {
			for _, r := range message.ToolResults {
				_ = json.Unmarshal([]byte(r.Content), &response)
				_ = json.Unmarshal([]byte(r.Content), &run)
			}
		}
		var call ai.ToolCall
		switch p.step {
		case 0:
			call = toolCall("base", toolRunTugQL, map[string]any{"title": "Invoice counts", "tugql": resultColumnsDocument})
		case 1:
			p.parent = run.RecordSetID
			if p.parent == "" {
				p.t.Fatal("query response has no persisted ID")
			}
			if p.explicit {
				yield(ai.Event{Type: ai.EventTextDelta, Text: "Requested columns only."}, nil)
				yield(ai.Event{Type: ai.EventCompleted, StopReason: ai.StopReasonEnd}, nil)
				p.step++
				return
			}
			call = toolCall("discover", toolResultColumns, resultColumnsArgs{RecordSetID: p.parent})
		case 2:
			for _, row := range response.Columns {
				if row.Addable && (row.Name == "Customer.FirstName" || row.Name == "Customer.LastName") {
					p.ids = append(p.ids, row.ID)
				}
			}
			if len(p.ids) != 2 {
				p.t.Fatalf("names not offered: %+v", response)
			}
			call = toolCall("plan", toolResultColumns, resultColumnsArgs{RecordSetID: p.parent, CandidateIDs: p.ids})
		case 3:
			if response.Preview == nil || response.Preview.Fingerprint == "" {
				p.t.Fatalf("no actual plan response: %+v", response)
			}
			p.fingerprint = response.Preview.Fingerprint
			call = toolCall("apply", toolApplyResultColumns, applyResultColumnsArgs{RecordSetID: p.parent, Fingerprint: p.fingerprint})
		default:
			if !response.OK || response.RecordSetID == "" {
				p.t.Fatalf("automatic execution failed: %+v", response)
			}
			yield(ai.Event{Type: ai.EventTextDelta, Text: "Added customer names automatically."}, nil)
			yield(ai.Event{Type: ai.EventCompleted, StopReason: ai.StopReasonEnd}, nil)
			return
		}
		p.step++
		if !yield(ai.Event{Type: ai.EventToolCall, ToolCall: &call}, nil) {
			return
		}
		yield(ai.Event{Type: ai.EventCompleted, StopReason: ai.StopReasonToolCalls}, nil)
	}
}

func TestResultColumnsActualTerminalAgentJourneyAndExplicitProjection(t *testing.T) {
	t.Parallel()
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{true: "explicit", false: "useful"}[explicit], func(t *testing.T) {
			c, _, executor, _ := resultColumnsFixture(t, resultColumnsSchema, resultColumnsDocument)
			provider := &columnJourneyProvider{t: t, explicit: explicit}
			conversation, err := NewAIConversation(provider, executor, c.source, "Invoice: InvoiceId, CustomerId\nCustomer: CustomerId, FirstName, LastName")
			if err != nil {
				t.Fatal(err)
			}
			user, err := c.store.AppendUser(context.Background(), c.activeID, "customers with number of invoices per customer")
			if err != nil {
				t.Fatal(err)
			}
			ctx := withQueryObserver(context.Background(), func(q QueryResult) (QueryResult, error) {
				q.SourceID = c.store.info.Database
				return c.store.AppendQuery(context.Background(), c.activeID, user.ID, c.source, q)
			})
			ctx = c.resultColumnsContext(ctx, user.ID)
			prompt := "customers with number of invoices per customer"
			if explicit {
				prompt = "Show only CustomerId and InvoiceCount"
			}
			turn, err := conversation.AskWithContext(ctx, prompt, "")
			if err != nil {
				t.Fatal(err)
			}
			wantQueries, wantCalls := 2, 2
			if explicit {
				wantQueries, wantCalls = 1, 1
			}
			if len(turn.Queries) != wantQueries || executor.calls != wantCalls {
				t.Fatalf("queries/calls %d/%d: %+v", len(turn.Queries), executor.calls, turn)
			}
			if !explicit {
				last := turn.Queries[len(turn.Queries)-1]
				if last.Lineage == nil || last.Lineage.ParentRecordSetID != provider.parent || !strings.Contains(last.TugQL, "Customer_LastName") {
					t.Fatal("not an immutable enriched query", last)
				}
			}
		})
	}
}

func TestResultColumnsAgentCannotUseHistoricalOrInventedPlan(t *testing.T) {
	t.Parallel()
	c, parent, e, _ := resultColumnsFixture(t, resultColumnsSchema, resultColumnsDocument)
	ctx := c.resultColumnsContext(context.Background(), "another-origin")
	callbacks := ctx.Value(resultColumnsContextKey{}).(resultColumnsCallbacks)
	if _, _, err := callbacks.plan(ctx, parent.ID, nil); err == nil {
		t.Fatal("historical record authorized")
	}
	if _, err := callbacks.apply(ctx, parent.ID, "invented"); err == nil || e.calls != 0 {
		t.Fatal("invented plan executed")
	}
}

func TestResultColumnsNarrowedActualRequestRetainsToolsAndGuidance(t *testing.T) {
	t.Parallel()
	request, _ := askNarrowed(t, narrowingQuestion, WithTableNarrowing(chinookNarrower(t, narrowingScorer(narrowingMusicSales))))
	if !strings.Contains(request.System, "Respect explicit user projections") || !strings.Contains(request.System, "result_columns") {
		t.Fatal("narrowed prompt dropped guidance")
	}
	found := map[string]bool{}
	for _, tool := range request.Tools {
		found[tool.Name] = true
	}
	if !found[toolResultColumns] || !found[toolApplyResultColumns] {
		t.Fatal("narrowed request dropped tools")
	}
}
