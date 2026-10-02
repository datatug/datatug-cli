// Package narrowing decides, before a chat turn reaches the AI model, which of
// a project's tables the model needs to see.
//
// A project can hold hundreds of tables. Sending every definition to a large
// generative model on each turn is slow and costly, and most of it is noise. A
// decision model such as Jev (TypeSafe AI's external model, reached through the
// cloud decider) scores each table's relevance to the question cheaply, and
// only the tables it selects are passed on as schema context.
//
// The decision walks a ladder and stops at the first rung that answers:
//
//  1. Deterministic project knowledge (Rule): free, exact, and the engine is
//     never called.
//  2. A decision engine (a decision.ScoredProvider) asked one relevance
//     question over the candidate tables, judged by a decision.SelectionPolicy.
//  3. The full schema, which is exactly what the chat did before this package.
//
// Every way the engine can fail to give a usable answer lands on rung 3: not
// configured, an error, a timeout, an uncalibrated answer, an uncertain answer,
// none-of-these, or a stopped engine (quota, budget, misconfigured). A stop is
// never read as "ask a bigger paid model to decide": it is recorded in the
// Record, and the chat simply keeps its full context.
//
// Ambiguity is preserved, not hidden: tables the engine judged only possibly
// relevant stay in the model's context next to the selected ones, and an
// uncertain answer keeps the whole schema.
package narrowing

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/strongo/aichat/ai/decision"
)

const (
	defaultTimeout = 3 * time.Second
	defaultProduct = "datatug"
	// questionID names the one relevance question asked of the engine.
	questionID = "tables"
	// questionInstructions is the criterion applied to each candidate table.
	questionInstructions = "Is this table needed to answer the user's question?"
	// maxDescriptionBytes caps one candidate's description (its column names), so
	// a very wide table cannot make the request unbounded.
	maxDescriptionBytes = 512
	// maxRecordedScores caps the scores kept in a Record.
	maxRecordedScores = 32
)

// Config assembles a Narrower. The engine, the clock and the formatter are
// injected so that tests need no network and no wall clock.
type Config struct {
	// Relations are the healthy tables and views of the active source, in the
	// order the full schema context lists them.
	Relations []api.CatalogRelation
	// Rules are the project's deterministic knowledge, tried before the engine.
	Rules []Rule
	// Engine scores candidate tables. Nil means no engine: only rules narrow.
	Engine decision.ScoredProvider
	// Policy turns the engine's probabilities into a verdict. It must be valid
	// (decision.NarrowingPolicy is the intended one).
	Policy decision.SelectionPolicy
	// Format renders relations as the model's schema context (the chat's
	// FormatSchemaContext), so the byte counts compare like with like.
	Format func(*api.CatalogSchema) string
	// Product is sent to the engine; default "datatug".
	Product string
	// Timeout bounds the engine call; default 3s.
	Timeout time.Duration
	// Now is the clock; default time.Now.
	Now func() time.Time
}

// Outcome is one decision. An empty Context means the narrowing did not apply
// and the caller keeps its full schema context.
type Outcome struct {
	Context string
	Record  Record
}

// Narrower decides which tables a turn's model sees. Its zero value is not
// usable; build one with New. It is safe for concurrent use.
type Narrower struct {
	relations []api.CatalogRelation
	ids       []string
	candidate []decision.Candidate
	taxonomy  decision.Taxonomy
	chain     decision.Chain // the project rules, when there are any
	engine    decision.ScoredProvider
	policy    decision.SelectionPolicy
	format    func(*api.CatalogSchema) string
	product   string
	timeout   time.Duration
	now       func() time.Time
	fullBytes int
}

// ScorerOf returns p as a decision.ScoredProvider, which a decision engine must
// be to answer a relevance question. The cloud decider is one.
func ScorerOf(p decision.Provider) (decision.ScoredProvider, error) {
	scorer, ok := p.(decision.ScoredProvider)
	if !ok {
		return nil, fmt.Errorf("decision provider %q cannot score candidates", p.Name())
	}
	return scorer, nil
}

// New builds a Narrower, checking the configuration once.
func New(cfg Config) (*Narrower, error) {
	if cfg.Format == nil {
		return nil, errors.New("narrowing: a schema formatter is required")
	}
	if err := cfg.Policy.Validate(); err != nil {
		return nil, fmt.Errorf("narrowing: selection policy: %w", err)
	}
	ruleProvider, err := rulesProvider(cfg.Rules)
	if err != nil {
		return nil, fmt.Errorf("narrowing: %w", err)
	}
	n := &Narrower{
		relations: slices.Clone(cfg.Relations),
		engine:    cfg.Engine,
		policy:    cfg.Policy,
		format:    cfg.Format,
		product:   cfg.Product,
		timeout:   cfg.Timeout,
		now:       cfg.Now,
	}
	if n.product == "" {
		n.product = defaultProduct
	}
	if n.timeout <= 0 {
		n.timeout = defaultTimeout
	}
	if n.now == nil {
		n.now = time.Now
	}
	n.ids = candidateIDs(n.relations)
	for i, relation := range n.relations {
		n.candidate = append(n.candidate, decision.Candidate{ID: n.ids[i], Description: describe(relation)})
	}
	n.taxonomy = decision.Taxonomy{Modules: []decision.ModuleSpec{{Name: moduleName, Intents: []string{intentName}, Scopes: n.ids}}}
	if ruleProvider != nil {
		n.chain = decision.Chain{Providers: []decision.Provider{ruleProvider}, Policy: &n.policy}
	}
	n.fullBytes = len(n.format(&api.CatalogSchema{Relations: n.relations}))
	return n, nil
}

// Candidates returns the ids the engine is asked about, in schema order.
func (n *Narrower) Candidates() []string { return slices.Clone(n.ids) }

// candidateIDs names each relation by its table name, qualified by schema when
// two relations share a name, and made unique if even that collides.
func candidateIDs(relations []api.CatalogRelation) []string {
	count := make(map[string]int, len(relations))
	for _, relation := range relations {
		count[relation.Name]++
	}
	ids := make([]string, len(relations))
	seen := make(map[string]bool, len(relations))
	for i, relation := range relations {
		id := relation.Name
		if count[id] > 1 && relation.Schema != "" {
			id = relation.Schema + "." + relation.Name
		}
		for base, n := id, 2; seen[id]; n++ {
			id = fmt.Sprintf("%s#%d", base, n)
		}
		seen[id] = true
		ids[i] = id
	}
	return ids
}

// describe is a candidate's description for the engine: its column names. No
// column types, no values: names only.
func describe(relation api.CatalogRelation) string {
	var b strings.Builder
	for i, column := range relation.Columns {
		piece := column.Name
		if i > 0 {
			piece = ", " + piece
		}
		if b.Len()+len(piece) > maxDescriptionBytes {
			b.WriteString(" ...")
			break
		}
		b.WriteString(piece)
	}
	return b.String()
}

// Narrow decides which tables the model sees for prompt. It never fails: every
// problem is a fallback to the full schema, recorded in the returned Record.
func (n *Narrower) Narrow(ctx context.Context, prompt string) Outcome {
	start := n.now()
	rec := Record{
		DecidedAt: start, Policy: n.policy.Name,
		CandidatesBefore: len(n.ids), CandidatesAfter: len(n.ids),
		ContextBytesBefore: n.fullBytes, ContextBytesAfter: n.fullBytes,
	}
	kept := n.decide(ctx, prompt, start, &rec)
	out := Outcome{}
	if kept != nil {
		out.Context = n.apply(kept, &rec)
	}
	rec.LatencyMs = n.now().Sub(start).Milliseconds()
	out.Record = rec
	return out
}

// decide runs the ladder and returns the ids to keep, or nil to keep everything.
func (n *Narrower) decide(ctx context.Context, prompt string, start time.Time, rec *Record) []string {
	if len(n.ids) == 0 {
		rec.FallbackReason = ReasonNoCandidates
		return nil
	}
	// The project rules run through a real decision.Chain, so a rule decision
	// that names a table the schema lacks fails decision.Validate and is
	// recorded as invalid, and only a decision the chain judged is acted on.
	d, ok, trace := n.chain.Decide(ctx, decision.Request{Product: n.product, Text: prompt, Taxonomy: n.taxonomy, Now: start})
	rec.Attempts = attemptsOf(trace.Attempts)
	if ok && d.Actionable() {
		rec.Mechanism, rec.Provenance, rec.Verdict = MechanismDeterministic, string(d.Provenance()), string(d.Outcome)
		rec.Selected = slices.Clone(d.RequiredScopes)
		return d.RequiredScopes
	}
	if n.engine == nil {
		rec.FallbackReason = ReasonDisabled
		return nil
	}
	return n.ask(ctx, prompt, rec)
}

// ask puts the one relevance question to the engine and applies the policy.
func (n *Narrower) ask(ctx context.Context, prompt string, rec *Record) []string {
	req := decision.ScoreRequest{
		Product: n.product,
		Text:    prompt,
		Questions: []decision.Question{{
			ID: questionID, Kind: decision.KindRelevance,
			Instructions: questionInstructions, Candidates: n.candidate,
		}},
	}
	rec.Engine = n.engine.Name()
	callCtx, cancel := context.WithTimeout(ctx, n.timeout)
	defer cancel()
	began := n.now()
	res, report, err := n.score(callCtx, req)
	judged := decision.Attempt{Provider: n.engine.Name(), Latency: n.now().Sub(began)}
	if err != nil {
		outcome := outcomeOf(callCtx, err)
		judged.Outcome, judged.Detail = outcome, boundedDetail(err.Error())
		rec.Attempts = append(rec.Attempts, attemptsOf(append(slices.Clone(report.Attempts), judged))...)
		rec.FallbackReason = outcome
		switch outcome {
		case decision.AttemptQuota, decision.AttemptBudget, decision.AttemptMisconfigured:
			rec.StoppedBy = outcome
		}
		return nil
	}
	if verr := decision.ValidateScoreResult(req, res); verr != nil {
		judged.Outcome, judged.Detail = decision.AttemptInvalid, decision.InvalidDetail(verr)
		rec.Attempts = append(rec.Attempts, attemptsOf(append(slices.Clone(report.Attempts), judged))...)
		rec.FallbackReason = decision.AttemptInvalid
		return nil
	}
	answer := res.Answers[questionID]
	sel := n.policy.Evaluate(answer)
	judged.Outcome, judged.Detail = decision.AttemptUncertain, sel.Detail()
	if sel.Actionable() {
		judged.Outcome = decision.AttemptDecided
	}
	rec.Attempts = append(rec.Attempts, attemptsOf(decision.MergeReport(report, judged, true))...)
	if res.Engine != "" {
		rec.Engine = res.Engine
	}
	rec.Model, rec.Verdict = res.Model, sel.Detail()
	rec.InputTokens, rec.OutputTokens = res.Usage.InputTokens, res.Usage.OutputTokens
	rec.Provenance = string(decision.ProvenanceSelfReported)
	if answer.Calibrated {
		rec.Provenance = string(decision.ProvenanceCalibrated)
	}
	ranked := decision.NewAnswer(questionID, answer.Kind, answer.Scores).Scores
	for _, s := range ranked[:min(len(ranked), maxRecordedScores)] {
		rec.Scores = append(rec.Scores, Score{ID: s.ID, Probability: s.Probability})
	}
	rec.Strong, rec.Potential, rec.Proposed = sel.Strong, sel.Potential, sel.Proposals
	if !sel.Actionable() {
		rec.FallbackReason = string(sel.Outcome)
		return nil
	}
	rec.Mechanism, rec.Selected = MechanismEngine, sel.Picks
	return append(slices.Clone(sel.Picks), sel.Potential...)
}

// score calls the engine, using its traced form when it has one.
func (n *Narrower) score(ctx context.Context, req decision.ScoreRequest) (decision.ScoreResult, decision.Report, error) {
	if traced, ok := n.engine.(decision.TracedScorer); ok {
		return traced.ScoreTraced(ctx, req)
	}
	res, err := n.engine.Score(ctx, req)
	var report decision.Report
	if res.Report != nil {
		report = *res.Report
	}
	return res, report, err
}

// outcomeOf names an engine error as the library's attempt outcome, in the same
// order decision.Chain classifies it: the conditions that must stop first.
func outcomeOf(ctx context.Context, err error) string {
	switch {
	case errors.Is(err, decision.ErrQuota):
		return decision.AttemptQuota
	case errors.Is(err, decision.ErrBudget):
		return decision.AttemptBudget
	case errors.Is(err, decision.ErrMisconfigured):
		return decision.AttemptMisconfigured
	case errors.Is(err, decision.ErrUnavailable):
		return decision.AttemptUnavailable
	case errors.Is(err, decision.ErrUnsupported):
		return decision.AttemptUnsupported
	case errors.Is(err, decision.ErrAuth):
		return decision.AttemptAuth
	case errors.Is(err, decision.ErrInvalidRequest):
		return decision.AttemptRejected
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return decision.AttemptTimeout
	case errors.Is(err, context.Canceled):
		return decision.AttemptCancelled
	}
	return decision.AttemptError
}

// apply keeps the relations whose ids are in kept (schema order), records them,
// and returns the narrowed schema context, or "" when nothing was dropped.
func (n *Narrower) apply(kept []string, rec *Record) string {
	var (
		relations []api.CatalogRelation
		ids       []string
	)
	for i, id := range n.ids {
		if slices.Contains(kept, id) {
			relations = append(relations, n.relations[i])
			ids = append(ids, id)
		}
	}
	if len(ids) == len(n.ids) {
		rec.FallbackReason = ReasonNoReduction
		return ""
	}
	text := n.format(&api.CatalogSchema{Relations: relations}) +
		fmt.Sprintf(noteFormat, len(ids), len(n.ids))
	rec.Narrowed, rec.Kept = true, ids
	rec.CandidatesAfter, rec.ContextBytesAfter = len(ids), len(text)
	return text
}

// noteFormat tells the model the list is a selection, so that it does not invent
// the name of a table it was not shown.
const noteFormat = "\nNote: DataTug narrowed this schema to the %d of %d relations judged relevant to this request. Do not guess the names of other relations."

// boundedDetail keeps an error text short enough to store.
func boundedDetail(text string) string {
	const limit = 200
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}

func attemptsOf(attempts []decision.Attempt) []Attempt {
	out := make([]Attempt, 0, len(attempts))
	for _, a := range attempts {
		out = append(out, Attempt{Provider: a.Provider, Outcome: a.Outcome, Detail: a.Detail, LatencyMs: a.Latency.Milliseconds()})
	}
	return out
}
