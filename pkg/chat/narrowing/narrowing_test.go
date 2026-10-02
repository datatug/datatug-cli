package narrowing_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-cli/pkg/chat/narrowing"
	"github.com/datatug/datatug-cli/pkg/chat/narrowing/narrowingtest"
	"github.com/strongo/aichat/ai/decision"
)

const salesQuestion = "Which countries buy the most music?"

// musicSales is how a calibrated decision model would score Chinook's 11 tables
// for salesQuestion: two clearly relevant, one possibly relevant, the rest low.
// The figures are illustrative, as in the design note for this slice.
var musicSales = map[string]float64{
	"Invoice": 0.96, "InvoiceLine": 0.71, "Customer": 0.42, "Track": 0.20,
}

func newScorer(probabilities map[string]float64) *narrowingtest.Scorer {
	return &narrowingtest.Scorer{Probabilities: probabilities, Floor: 0.05, Calibrated: true, Model: "jev-1.13.0", Usage: decision.Usage{InputTokens: 900, OutputTokens: 40}}
}

// narrower builds a Narrower over Chinook with the given engine and the narrowing
// policy; mutate adjusts the configuration first.
func narrower(t *testing.T, engine decision.ScoredProvider, mutate ...func(*narrowing.Config)) *narrowing.Narrower {
	t.Helper()
	cfg := narrowing.Config{
		Relations: narrowingtest.Chinook(), Engine: engine,
		Policy: decision.NarrowingPolicy(), Format: chat.FormatSchemaContext,
		Timeout: time.Second,
	}
	for _, m := range mutate {
		m(&cfg)
	}
	n, err := narrowing.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return n
}

func fullSchemaBytes() int {
	return len(chat.FormatSchemaContext(&api.CatalogSchema{Relations: narrowingtest.Chinook()}))
}

// A calibrated Jev answer narrows 11 Chinook tables to the selected few, before
// the AI conversation is created, and the final context is materially smaller.
func TestNarrowShrinksChinookContext(t *testing.T) {
	tests := []struct {
		name          string
		probabilities map[string]float64
		wantKept      []string
		wantSelected  []string
		wantPotential []string
		wantVerdict   string
	}{
		{
			name:          "two selected, one potential stays for the model",
			probabilities: musicSales,
			wantKept:      []string{"Customer", "Invoice", "InvoiceLine"},
			wantSelected:  []string{"Invoice", "InvoiceLine"},
			wantPotential: []string{"Customer"},
			wantVerdict:   "several",
		},
		{
			name:          "one clear table",
			probabilities: map[string]float64{"Employee": 0.93},
			wantKept:      []string{"Employee"},
			wantSelected:  []string{"Employee"},
			wantVerdict:   "selected",
		},
		{
			name:          "three clear tables",
			probabilities: map[string]float64{"Invoice": 0.97, "InvoiceLine": 0.88, "Customer": 0.74},
			wantKept:      []string{"Customer", "Invoice", "InvoiceLine"},
			wantSelected:  []string{"Invoice", "InvoiceLine", "Customer"},
			wantVerdict:   "several",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := newScorer(tt.probabilities)
			out := narrower(t, engine).Narrow(context.Background(), salesQuestion)
			rec := out.Record

			if !rec.Narrowed || rec.Mechanism != narrowing.MechanismEngine || rec.FallbackReason != "" {
				t.Fatalf("record = %+v, want an engine narrowing", rec)
			}
			if rec.CandidatesBefore != 11 || rec.CandidatesAfter != len(tt.wantKept) {
				t.Fatalf("candidates %d -> %d, want 11 -> %d", rec.CandidatesBefore, rec.CandidatesAfter, len(tt.wantKept))
			}
			if !slices.Equal(rec.Kept, tt.wantKept) {
				t.Fatalf("kept = %v, want %v (schema order)", rec.Kept, tt.wantKept)
			}
			if !slices.Equal(rec.Selected, tt.wantSelected) || !slices.Equal(rec.Potential, tt.wantPotential) || rec.Verdict != tt.wantVerdict {
				t.Fatalf("selected=%v potential=%v verdict=%q", rec.Selected, rec.Potential, rec.Verdict)
			}
			// The model's context holds exactly the kept tables' definitions, plus a
			// one-line note that the list is a selection.
			var kept []api.CatalogRelation
			for _, relation := range narrowingtest.Chinook() {
				if slices.Contains(tt.wantKept, relation.Name) {
					kept = append(kept, relation)
				}
			}
			want := chat.FormatSchemaContext(&api.CatalogSchema{Relations: kept}) +
				fmt.Sprintf("\nNote: DataTug narrowed this schema to the %d of 11 relations judged relevant to this request. Do not guess the names of other relations.", len(tt.wantKept))
			if out.Context != want {
				t.Fatalf("context =\n%s\nwant\n%s", out.Context, want)
			}
			for _, relation := range narrowingtest.Chinook() {
				if listed := strings.Contains(out.Context, "- "+relation.Name+" ("); listed != slices.Contains(tt.wantKept, relation.Name) {
					t.Fatalf("table %s listed in the context = %v", relation.Name, listed)
				}
			}
			if rec.ContextBytesBefore != fullSchemaBytes() || rec.ContextBytesAfter != len(out.Context) {
				t.Fatalf("bytes %d -> %d, want %d -> %d", rec.ContextBytesBefore, rec.ContextBytesAfter, fullSchemaBytes(), len(out.Context))
			}
			if rec.ContextBytesAfter*4 >= rec.ContextBytesBefore*3 {
				t.Fatalf("context is not materially smaller: %d -> %d bytes", rec.ContextBytesBefore, rec.ContextBytesAfter)
			}
			if got := len(engine.Requests()); got != 1 {
				t.Fatalf("engine calls = %d, want exactly one", got)
			}
		})
	}
}

// The measured sizes on Chinook, pinned so that a regression in what the model
// receives is visible: 11 tables before, the 3 the music-sales decision keeps
// after.
func TestChinookContextSizesAreMeasured(t *testing.T) {
	out := narrower(t, newScorer(musicSales)).Narrow(context.Background(), salesQuestion)
	t.Logf("Chinook: %d tables / %d bytes -> %d tables / %d bytes",
		out.Record.CandidatesBefore, out.Record.ContextBytesBefore, out.Record.CandidatesAfter, out.Record.ContextBytesAfter)
	if out.Record.CandidatesBefore != 11 || out.Record.CandidatesAfter != 3 {
		t.Fatalf("tables %d -> %d, want 11 -> 3", out.Record.CandidatesBefore, out.Record.CandidatesAfter)
	}
	if out.Record.ContextBytesBefore != wantFullBytes || out.Record.ContextBytesAfter != wantNarrowedBytes {
		t.Fatalf("bytes %d -> %d, want %d -> %d", out.Record.ContextBytesBefore, out.Record.ContextBytesAfter, wantFullBytes, wantNarrowedBytes)
	}
}

// Ambiguity is preserved, not hidden.
func TestAmbiguityIsPreserved(t *testing.T) {
	t.Run("possibly relevant tables stay in the model's context", func(t *testing.T) {
		out := narrower(t, newScorer(musicSales)).Narrow(context.Background(), salesQuestion)
		if !strings.Contains(out.Context, "- Customer (") {
			t.Fatal("the potential table Customer was dropped from the context")
		}
		if !slices.Equal(out.Record.Potential, []string{"Customer"}) {
			t.Fatalf("potential = %v", out.Record.Potential)
		}
	})
	t.Run("no clear answer keeps the full schema and records the candidates", func(t *testing.T) {
		engine := newScorer(map[string]float64{"Invoice": 0.45, "Customer": 0.40, "Track": 0.35})
		out := narrower(t, engine).Narrow(context.Background(), salesQuestion)
		if out.Context != "" || out.Record.Narrowed {
			t.Fatalf("an uncertain answer narrowed the schema: %+v", out.Record)
		}
		if out.Record.FallbackReason != "uncertain" || out.Record.Verdict != "uncertain: only_potential" {
			t.Fatalf("reason=%q verdict=%q", out.Record.FallbackReason, out.Record.Verdict)
		}
		if !slices.Equal(out.Record.Potential, []string{"Invoice", "Customer", "Track"}) {
			t.Fatalf("the ambiguous candidates were hidden: potential = %v", out.Record.Potential)
		}
		if out.Record.CandidatesAfter != 11 || out.Record.ContextBytesAfter != out.Record.ContextBytesBefore {
			t.Fatalf("counts changed on a fallback: %+v", out.Record)
		}
	})
	t.Run("none of the tables fits keeps the full schema", func(t *testing.T) {
		out := narrower(t, newScorer(nil)).Narrow(context.Background(), "What is the weather?")
		if out.Context != "" || out.Record.FallbackReason != "none" {
			t.Fatalf("record = %+v", out.Record)
		}
	})
	t.Run("an engine that selects every table narrows nothing", func(t *testing.T) {
		engine := newScorer(nil)
		engine.Floor = 0.9
		out := narrower(t, engine).Narrow(context.Background(), "show everything")
		if out.Context != "" || out.Record.Narrowed || out.Record.FallbackReason != narrowing.ReasonNoReduction {
			t.Fatalf("record = %+v", out.Record)
		}
		if out.Record.Mechanism != narrowing.MechanismEngine || len(out.Record.Selected) != 11 {
			t.Fatalf("the engine's selection was not recorded: %+v", out.Record)
		}
	})
}

// Deterministic knowledge bypasses the engine.
func TestDeterministicKnowledgeBypassesTheEngine(t *testing.T) {
	rules := []narrowing.Rule{{Phrase: "Sales by country", Tables: []string{"Invoice", "Customer"}}}
	tests := []struct {
		name       string
		prompt     string
		wantCalls  int
		wantMech   narrowing.Mechanism
		wantKept   []string
		wantReason string
	}{
		{"exact phrase", "Sales by country", 0, narrowing.MechanismDeterministic, []string{"Customer", "Invoice"}, ""},
		{"normalised: case, spacing, trailing punctuation", "  sales   BY country?! ", 0, narrowing.MechanismDeterministic, []string{"Customer", "Invoice"}, ""},
		{"a different question goes to the engine", "Which tracks are longest?", 1, narrowing.MechanismEngine, []string{"Track"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := newScorer(map[string]float64{"Track": 0.9})
			out := narrower(t, engine, func(c *narrowing.Config) { c.Rules = rules }).Narrow(context.Background(), tt.prompt)
			if got := len(engine.Requests()); got != tt.wantCalls {
				t.Fatalf("engine calls = %d, want %d", got, tt.wantCalls)
			}
			if out.Record.Mechanism != tt.wantMech || !slices.Equal(out.Record.Kept, tt.wantKept) {
				t.Fatalf("record = %+v", out.Record)
			}
		})
	}

	t.Run("provenance names the rule and no engine", func(t *testing.T) {
		engine := newScorer(nil)
		out := narrower(t, engine, func(c *narrowing.Config) { c.Rules = rules }).Narrow(context.Background(), "sales by country")
		rec := out.Record
		if rec.Provenance != "deterministic" || rec.Verdict != "deterministic" || rec.Engine != "" || rec.Model != "" {
			t.Fatalf("record = %+v", rec)
		}
		if len(rec.Attempts) != 1 || rec.Attempts[0].Provider != "datatug-project-rules" || rec.Attempts[0].Outcome != "decided" {
			t.Fatalf("attempts = %+v", rec.Attempts)
		}
		if !slices.Equal(rec.Selected, []string{"Invoice", "Customer"}) || rec.InputTokens != 0 {
			t.Fatalf("selected=%v tokens=%d", rec.Selected, rec.InputTokens)
		}
		steps := rec.DetectionSteps()
		if len(steps) != 1 || steps[0].Method != "deterministic" || steps[0].Detector != "datatug.narrow.rules" || steps[0].Result != "narrowed:before=11:after=2" {
			t.Fatalf("steps = %+v", steps)
		}
	})

	t.Run("a rule that names a table the schema lacks is rejected, and the engine decides", func(t *testing.T) {
		engine := newScorer(map[string]float64{"Track": 0.9})
		bad := []narrowing.Rule{{Phrase: "longest tracks", Tables: []string{"Track", "NoSuchTable"}}}
		out := narrower(t, engine, func(c *narrowing.Config) { c.Rules = bad }).Narrow(context.Background(), "longest tracks")
		if len(engine.Requests()) != 1 || out.Record.Mechanism != narrowing.MechanismEngine {
			t.Fatalf("record = %+v", out.Record)
		}
		if out.Record.Attempts[0].Provider != "datatug-project-rules" || out.Record.Attempts[0].Outcome != "invalid" {
			t.Fatalf("attempts = %+v", out.Record.Attempts)
		}
	})

	t.Run("a rule covering every table narrows nothing but still bypasses the engine", func(t *testing.T) {
		all := make([]string, 0, 11)
		for _, relation := range narrowingtest.Chinook() {
			all = append(all, relation.Name)
		}
		engine := newScorer(nil)
		out := narrower(t, engine, func(c *narrowing.Config) {
			c.Rules = []narrowing.Rule{{Phrase: "everything", Tables: all}}
		}).Narrow(context.Background(), "everything")
		if len(engine.Requests()) != 0 || out.Context != "" || out.Record.FallbackReason != narrowing.ReasonNoReduction {
			t.Fatalf("record = %+v", out.Record)
		}
	})

	t.Run("rules alone narrow when no engine is configured", func(t *testing.T) {
		out := narrower(t, nil, func(c *narrowing.Config) { c.Rules = rules }).Narrow(context.Background(), "sales by country")
		if !out.Record.Narrowed || out.Record.CandidatesAfter != 2 {
			t.Fatalf("record = %+v", out.Record)
		}
	})
}

// With the decider disabled, timing out, uncertain or stopped, the chat behaves
// exactly as today: no narrowed context, the full schema.
func TestFallbackKeepsTheFullSchema(t *testing.T) {
	tests := []struct {
		name        string
		engine      func() *narrowingtest.Scorer // nil engine when it returns nil
		ctx         func() (context.Context, context.CancelFunc)
		wantReason  string
		wantStopped string
		wantOutcome string // outcome of the engine attempt
	}{
		{name: "decider disabled", engine: func() *narrowingtest.Scorer { return nil }, wantReason: narrowing.ReasonDisabled},
		{name: "decider timing out", engine: func() *narrowingtest.Scorer { return &narrowingtest.Scorer{Block: true} }, wantReason: "timeout", wantOutcome: "timeout"},
		{name: "uncalibrated answer is a proposal, never a selection", engine: func() *narrowingtest.Scorer {
			s := newScorer(musicSales)
			s.Calibrated = false
			return s
		}, wantReason: "unscored", wantOutcome: "uncertain"},
		{name: "transport error", engine: func() *narrowingtest.Scorer { return &narrowingtest.Scorer{Err: errors.New("connection reset")} }, wantReason: "error", wantOutcome: "error"},
		{name: "engine unavailable (breaker open)", engine: func() *narrowingtest.Scorer {
			return &narrowingtest.Scorer{Err: fmt.Errorf("jev: %w", decision.ErrUnavailable)}
		}, wantReason: "unavailable", wantOutcome: "unavailable"},
		{name: "server without a score route", engine: func() *narrowingtest.Scorer {
			return &narrowingtest.Scorer{Err: fmt.Errorf("cloud: ai/score: %w", decision.ErrUnsupported)}
		}, wantReason: "unsupported", wantOutcome: "unsupported"},
		{name: "credentials refused", engine: func() *narrowingtest.Scorer {
			return &narrowingtest.Scorer{Err: fmt.Errorf("cloud: %w", decision.ErrAuth)}
		}, wantReason: "auth", wantOutcome: "auth"},
		{name: "request rejected as invalid", engine: func() *narrowingtest.Scorer {
			return &narrowingtest.Scorer{Err: fmt.Errorf("cloud: %w", decision.ErrInvalidRequest)}
		}, wantReason: "rejected", wantOutcome: "rejected"},
		{name: "malformed result", engine: func() *narrowingtest.Scorer {
			return &narrowingtest.Scorer{Raw: map[string]decision.Answer{}}
		}, wantReason: "invalid", wantOutcome: "invalid"},
		{name: "cancelled by the user", engine: func() *narrowingtest.Scorer { return &narrowingtest.Scorer{Block: true} },
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			}, wantReason: "cancelled", wantOutcome: "cancelled"},
		// A stopped engine is not "ask a bigger paid model": the chat keeps its full
		// context, and the stop is recorded.
		{name: "allowance exhausted stops", engine: func() *narrowingtest.Scorer {
			return &narrowingtest.Scorer{Err: fmt.Errorf("cloud: %w", decision.ErrQuota)}
		}, wantReason: "quota", wantStopped: "quota", wantOutcome: "quota"},
		{name: "spending cap used up stops", engine: func() *narrowingtest.Scorer {
			return &narrowingtest.Scorer{Err: fmt.Errorf("budget: %w", decision.ErrBudget)}
		}, wantReason: "budget", wantStopped: "budget", wantOutcome: "budget"},
		{name: "misconfigured endpoint stops", engine: func() *narrowingtest.Scorer {
			return &narrowingtest.Scorer{Err: fmt.Errorf("cloud: %w", decision.ErrMisconfigured)}
		}, wantReason: "misconfigured", wantStopped: "misconfigured", wantOutcome: "misconfigured"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var engine decision.ScoredProvider
			if s := tt.engine(); s != nil {
				engine = s
			}
			n := narrower(t, engine, func(c *narrowing.Config) { c.Timeout = 20 * time.Millisecond })
			ctx, cancel := context.Background(), context.CancelFunc(func() {})
			if tt.ctx != nil {
				ctx, cancel = tt.ctx()
			}
			defer cancel()
			out := n.Narrow(ctx, salesQuestion)
			rec := out.Record

			if out.Context != "" || rec.Narrowed {
				t.Fatalf("the schema was narrowed: %+v", rec)
			}
			if rec.FallbackReason != tt.wantReason || rec.StoppedBy != tt.wantStopped {
				t.Fatalf("reason=%q stoppedBy=%q, want %q %q", rec.FallbackReason, rec.StoppedBy, tt.wantReason, tt.wantStopped)
			}
			if rec.CandidatesBefore != 11 || rec.CandidatesAfter != 11 || rec.ContextBytesAfter != fullSchemaBytes() || rec.Kept != nil {
				t.Fatalf("the full schema was not kept: %+v", rec)
			}
			if tt.wantOutcome != "" {
				last := rec.Attempts[len(rec.Attempts)-1]
				if last.Provider != "fake-jev" || last.Outcome != tt.wantOutcome {
					t.Fatalf("engine attempt = %+v, want outcome %q", last, tt.wantOutcome)
				}
			}
			summary := rec.Summary()
			if tt.wantStopped != "" && summary != "full_schema:reason=stopped_"+tt.wantStopped+":before=11" {
				t.Fatalf("summary = %q", summary)
			}
			if tt.wantStopped == "" && summary != "full_schema:reason="+tt.wantReason+":before=11" {
				t.Fatalf("summary = %q", summary)
			}
		})
	}
}

func TestUncalibratedAnswerRecordsProposalsOnly(t *testing.T) {
	engine := newScorer(musicSales)
	engine.Calibrated = false
	engine.EngineName = "llm-decider"
	out := narrower(t, engine).Narrow(context.Background(), salesQuestion)
	rec := out.Record
	if rec.Provenance != "self_reported" || len(rec.Selected) != 0 || !slices.Equal(rec.Proposed, []string{"Invoice", "InvoiceLine"}) {
		t.Fatalf("record = %+v", rec)
	}
	if steps := rec.DetectionSteps(); steps[0].Method != "llm" || steps[0].Confidence != nil {
		t.Fatalf("steps = %+v", steps)
	}
}

// What leaves the machine is the question, table names and column names: no
// types, no values, no extra context.
func TestWhatIsSentToTheEngine(t *testing.T) {
	engine := newScorer(musicSales)
	narrower(t, engine, func(c *narrowing.Config) { c.Product = "datatug-demo" }).Narrow(context.Background(), salesQuestion)
	requests := engine.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d", len(requests))
	}
	req := requests[0]
	if req.Product != "datatug-demo" || req.Text != salesQuestion || req.Context != nil || len(req.Questions) != 1 {
		t.Fatalf("request = %+v", req)
	}
	question := req.Questions[0]
	if question.Kind != decision.KindRelevance || question.NoneID != "" || len(question.Candidates) != 11 {
		t.Fatalf("question = %+v", question)
	}
	if err := decision.ValidateScoreRequest(req); err != nil {
		t.Fatalf("the request is not well formed: %v", err)
	}
	for _, candidate := range question.Candidates {
		if strings.ContainsAny(candidate.Description, "[]()") || strings.Contains(candidate.Description, "INTEGER") {
			t.Fatalf("candidate %s carries types: %q", candidate.ID, candidate.Description)
		}
	}
	if question.Candidates[5].ID != "Invoice" || !strings.HasPrefix(question.Candidates[5].Description, "InvoiceId, CustomerId, InvoiceDate, ") {
		t.Fatalf("candidate = %+v", question.Candidates[5])
	}
}

func TestWideTablesAreCappedAndNamesStayUnique(t *testing.T) {
	var wide api.CatalogRelation
	wide.Name = "Wide"
	for i := range 200 {
		wide.Columns = append(wide.Columns, api.CatalogColumn{Name: fmt.Sprintf("column_%03d", i)})
	}
	relations := []api.CatalogRelation{
		wide,
		{Schema: "sales", Name: "Orders"}, {Schema: "archive", Name: "Orders"},
		{Name: "Plain"}, {Name: "Plain"},
		{Schema: "x", Name: "Plain"},
	}
	engine := newScorer(nil)
	n := narrower(t, engine, func(c *narrowing.Config) { c.Relations = relations })
	if got, want := n.Candidates(), []string{"Wide", "sales.Orders", "archive.Orders", "Plain", "Plain#2", "x.Plain"}; !slices.Equal(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	n.Narrow(context.Background(), "anything")
	candidates := engine.Requests()[0].Questions[0].Candidates
	if d := candidates[0].Description; len(d) > 512+4 || !strings.HasSuffix(d, " ...") || !strings.HasPrefix(d, "column_000, column_001") {
		t.Fatalf("wide description = %d bytes %q", len(d), d[max(0, len(d)-20):])
	}
	// A column list that never fits at all still produces a bounded description.
	huge := api.CatalogRelation{Name: "Huge", Columns: []api.CatalogColumn{{Name: strings.Repeat("c", 600)}}}
	n = narrower(t, newScorer(nil), func(c *narrowing.Config) { c.Relations = []api.CatalogRelation{huge} })
	if n.Candidates()[0] != "Huge" {
		t.Fatal("candidates")
	}
}

func TestNoRelationsMeansNoDecision(t *testing.T) {
	engine := newScorer(musicSales)
	out := narrower(t, engine, func(c *narrowing.Config) { c.Relations = nil }).Narrow(context.Background(), salesQuestion)
	if out.Context != "" || out.Record.FallbackReason != narrowing.ReasonNoCandidates || len(engine.Requests()) != 0 {
		t.Fatalf("record = %+v", out.Record)
	}
}

// The decision has inspectable provenance: engine, model, policy, verdict,
// scores best first, every attempt, tokens and latency.
func TestRecordHasInspectableProvenance(t *testing.T) {
	clock := narrowingtest.NewClock()
	engine := newScorer(musicSales)
	engine.Clock, engine.Delay = clock, 120*time.Millisecond
	n := narrower(t, engine, func(c *narrowing.Config) { c.Now = clock.Now })
	rec := n.Narrow(context.Background(), salesQuestion).Record

	if rec.DecidedAt != clock.Now().Add(-120*time.Millisecond) || rec.LatencyMs != 120 {
		t.Fatalf("decidedAt=%v latency=%dms", rec.DecidedAt, rec.LatencyMs)
	}
	if rec.Engine != "fake-jev" || rec.Model != "jev-1.13.0" || rec.Provenance != "calibrated" || rec.Policy != "narrowing" {
		t.Fatalf("record = %+v", rec)
	}
	if rec.InputTokens != 900 || rec.OutputTokens != 40 {
		t.Fatalf("tokens = %d/%d", rec.InputTokens, rec.OutputTokens)
	}
	if len(rec.Scores) != 11 || rec.Scores[0] != (narrowing.Score{ID: "Invoice", Probability: 0.96}) || rec.Scores[1].ID != "InvoiceLine" {
		t.Fatalf("scores = %+v", rec.Scores)
	}
	if len(rec.Attempts) != 1 {
		t.Fatalf("attempts = %+v", rec.Attempts)
	}
	last := rec.Attempts[0]
	if last.Provider != "fake-jev" || last.Outcome != "decided" || last.Detail != "several" || last.LatencyMs != 120 {
		t.Fatalf("engine attempt = %+v", last)
	}
	steps := rec.DetectionSteps()
	if len(steps) != 1 {
		t.Fatalf("steps = %+v", steps)
	}
	step := steps[0]
	if step.Method != "jev" || step.Detector != "datatug.narrow.tables" || step.Version != "jev-1.13.0" || step.Result != "narrowed:before=11:after=3" {
		t.Fatalf("step = %+v", step)
	}
	if step.Confidence == nil || *step.Confidence != 0.96 {
		t.Fatalf("confidence = %v", step.Confidence)
	}
	// Telemetry carries counts and ids only: never a table name or the question.
	text := fmt.Sprintf("%+v", steps)
	for _, leaked := range []string{"Invoice", "Customer", "countries", "music"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("telemetry step leaks %q: %s", leaked, text)
		}
	}
}

func TestDetectionStepsForOtherEngines(t *testing.T) {
	cases := []struct {
		rec         narrowing.Record
		wantMethod  string
		wantVersion string
	}{
		{narrowing.Record{Engine: "cloud-decision", Model: "model with spaces"}, "decision_engine", ""},
		{narrowing.Record{Engine: "JEV", Model: strings.Repeat("m", 65)}, "jev", ""},
		{narrowing.Record{Engine: "llm-decider", Model: "claude-haiku-4-5"}, "llm", "claude-haiku-4-5"},
	}
	for _, c := range cases {
		steps := c.rec.DetectionSteps()
		if steps[0].Method != c.wantMethod || steps[0].Version != c.wantVersion {
			t.Errorf("%+v -> %+v", c.rec, steps)
		}
	}
	// No engine asked, not deterministic (the decider is disabled): one honest step.
	disabled := narrowing.Record{FallbackReason: narrowing.ReasonDisabled, CandidatesBefore: 11}
	if steps := disabled.DetectionSteps(); steps[0].Detector != "datatug.narrow.tables" || steps[0].Result != "full_schema:reason=disabled:before=11" {
		t.Fatalf("steps = %+v", steps)
	}
	// A calibrated record whose top pick has no score keeps no confidence.
	odd := narrowing.Record{Engine: "jev", Provenance: "calibrated", Selected: []string{"Ghost"}}
	if steps := odd.DetectionSteps(); steps[0].Confidence != nil {
		t.Fatalf("steps = %+v", steps)
	}
}

// tracedEngine is a scorer that reports how it answered, as the cloud decider and
// the compose combinators do.
type tracedEngine struct {
	*narrowingtest.Scorer
	report decision.Report
}

func (e tracedEngine) ScoreTraced(ctx context.Context, req decision.ScoreRequest) (decision.ScoreResult, decision.Report, error) {
	res, err := e.Score(ctx, req)
	return res, e.report, err
}

// reportingEngine returns its report inside the result, as a combinator does.
type reportingEngine struct{ inner *narrowingtest.Scorer }

func (e reportingEngine) Name() string { return e.inner.Name() }

func (e reportingEngine) Score(ctx context.Context, req decision.ScoreRequest) (decision.ScoreResult, error) {
	res, err := e.inner.Score(ctx, req)
	res.Report = &decision.Report{Strategy: "hedged", Engine: "fake-jev", Attempts: []decision.Attempt{
		{Provider: "fake-jev", Outcome: decision.AttemptDecided, Role: "primary"},
		{Provider: "llm-decider", Outcome: decision.AttemptCancelled, Role: "backup"},
	}}
	return res, err
}

func TestEngineAttemptsAreRecorded(t *testing.T) {
	t.Run("traced engine: the server's attempts, the answering one relabelled by the policy", func(t *testing.T) {
		engine := tracedEngine{newScorer(musicSales), decision.Report{Strategy: "hedged", Engine: "fake-jev", Attempts: []decision.Attempt{
			{Provider: "fake-jev", Outcome: decision.AttemptDecided, Role: "primary", Latency: 80 * time.Millisecond},
			{Provider: "llm-decider", Outcome: decision.AttemptCancelled, Role: "backup"},
		}}}
		rec := narrower(t, engine).Narrow(context.Background(), salesQuestion).Record
		got := rec.Attempts[len(rec.Attempts)-2:]
		if got[0].Provider != "fake-jev" || got[0].Outcome != "decided" || got[0].Detail != "several" || got[0].LatencyMs != 80 || got[1].Outcome != "cancelled" {
			t.Fatalf("attempts = %+v", rec.Attempts)
		}
	})
	t.Run("report inside the result", func(t *testing.T) {
		rec := narrower(t, reportingEngine{newScorer(musicSales)}).Narrow(context.Background(), salesQuestion).Record
		got := rec.Attempts[len(rec.Attempts)-2:]
		if got[0].Provider != "fake-jev" || got[1].Provider != "llm-decider" || got[1].Outcome != "cancelled" {
			t.Fatalf("attempts = %+v", rec.Attempts)
		}
	})
	t.Run("an error from a traced engine keeps its attempts and adds the failure", func(t *testing.T) {
		failing := newScorer(nil)
		failing.Err = fmt.Errorf("jev: %w", decision.ErrQuota)
		engine := tracedEngine{failing, decision.Report{Attempts: []decision.Attempt{{Provider: "jev", Outcome: decision.AttemptQuota}}}}
		rec := narrower(t, engine).Narrow(context.Background(), salesQuestion).Record
		if rec.StoppedBy != "quota" || len(rec.Attempts) < 2 || rec.Attempts[len(rec.Attempts)-2].Provider != "jev" {
			t.Fatalf("record = %+v", rec)
		}
	})
	t.Run("a long error is bounded", func(t *testing.T) {
		engine := newScorer(nil)
		engine.Err = errors.New(strings.Repeat("x", 500))
		rec := narrower(t, engine).Narrow(context.Background(), salesQuestion).Record
		if d := rec.Attempts[len(rec.Attempts)-1].Detail; len(d) != 203 || !strings.HasSuffix(d, "...") {
			t.Fatalf("detail length = %d", len(d))
		}
	})
}

func TestRecordedScoresAreBounded(t *testing.T) {
	var relations []api.CatalogRelation
	for i := range 40 {
		relations = append(relations, api.CatalogRelation{Name: fmt.Sprintf("T%02d", i)})
	}
	engine := newScorer(map[string]float64{"T07": 0.95})
	rec := narrower(t, engine, func(c *narrowing.Config) { c.Relations = relations }).Narrow(context.Background(), "q").Record
	if len(rec.Scores) != 32 || rec.Scores[0].ID != "T07" {
		t.Fatalf("scores = %d first %v", len(rec.Scores), rec.Scores[0])
	}
}

func TestNewRejectsBadConfiguration(t *testing.T) {
	good := narrowing.Config{Relations: narrowingtest.Chinook(), Policy: decision.NarrowingPolicy(), Format: chat.FormatSchemaContext}
	tests := []struct {
		name   string
		mutate func(*narrowing.Config)
		want   string
	}{
		{"no formatter", func(c *narrowing.Config) { c.Format = nil }, "formatter"},
		{"zero policy selects everything", func(c *narrowing.Config) { c.Policy = decision.SelectionPolicy{} }, "selection policy"},
		{"rule without phrase", func(c *narrowing.Config) { c.Rules = []narrowing.Rule{{Phrase: " ?", Tables: []string{"Invoice"}}} }, "rule 1 needs a phrase"},
		{"rule without tables", func(c *narrowing.Config) { c.Rules = []narrowing.Rule{{Phrase: "x"}} }, "rule 1 needs a phrase"},
		{"repeated phrase", func(c *narrowing.Config) {
			c.Rules = []narrowing.Rule{{Phrase: "Sales", Tables: []string{"Invoice"}}, {Phrase: "sales!", Tables: []string{"Invoice"}}}
		}, "rule 2 repeats"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := good
			tt.mutate(&cfg)
			if _, err := narrowing.New(cfg); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("New error = %v, want %q", err, tt.want)
			}
		})
	}
	if n, err := narrowing.New(good); err != nil || n == nil {
		t.Fatalf("New(good) = %v, %v", n, err)
	}
}

type plainProvider struct{}

func (plainProvider) Name() string { return "plain" }
func (plainProvider) Decide(context.Context, decision.Request) (decision.Decision, bool, error) {
	return decision.Decision{}, false, nil
}

func TestScorerOf(t *testing.T) {
	if _, err := narrowing.ScorerOf(plainProvider{}); err == nil || !strings.Contains(err.Error(), `"plain"`) {
		t.Fatalf("a provider that cannot score was accepted: %v", err)
	}
	if got, err := narrowing.ScorerOf(scoringProvider{Scorer: newScorer(nil)}); err != nil || got == nil {
		t.Fatalf("ScorerOf(scoring provider) = %v, %v", got, err)
	}
}

type scoringProvider struct {
	plainProvider
	*narrowingtest.Scorer
}

func (p scoringProvider) Name() string { return "scoring" }

func TestLoadRules(t *testing.T) {
	dir := t.TempDir()
	if rules, err := narrowing.LoadRules(dir); err != nil || rules != nil {
		t.Fatalf("a project without rules = %v, %v", rules, err)
	}
	write := func(content string) {
		t.Helper()
		path := filepath.Join(dir, "ai", "table-rules.yaml")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("rules:\n  - phrase: Sales by country\n    tables: [Invoice, Customer]\n")
	rules, err := narrowing.LoadRules(dir)
	if err != nil || len(rules) != 1 || rules[0].Phrase != "Sales by country" || !slices.Equal(rules[0].Tables, []string{"Invoice", "Customer"}) {
		t.Fatalf("rules = %+v, %v", rules, err)
	}
	write("rules: [not, a, rule")
	if _, err := narrowing.LoadRules(dir); err == nil || !strings.Contains(err.Error(), "ai/table-rules.yaml") {
		t.Fatalf("malformed file error = %v", err)
	}
	// A rules path that is a directory cannot be read.
	if err := os.RemoveAll(filepath.Join(dir, "ai")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "ai", "table-rules.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := narrowing.LoadRules(dir); err == nil || !strings.Contains(err.Error(), "read table rules") {
		t.Fatalf("unreadable file error = %v", err)
	}
}

const (
	wantFullBytes     = 1829
	wantNarrowedBytes = 894
)
