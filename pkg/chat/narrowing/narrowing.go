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
// configured, an error, a timeout, an incomplete or uncalibrated answer, an
// uncertain answer, none-of-these, or a stopped engine (quota, budget,
// misconfigured). A stop is never read as "ask a bigger paid model to decide": it
// is recorded in the Record, and the chat simply keeps its full context. After a
// failure the engine is not asked again for a cool-down, so a slow or refusing
// service costs one wait, not one per turn.
//
// A wrong narrowing would be silent, so three things make it recoverable and
// visible: tables the engine judged only possibly relevant stay in the model's
// context; tables on the foreign-key path between selected tables are added, and
// the previous turn's tables are carried into a follow-up; and the model is told
// the names of the omitted tables and can read any one's definition with the
// describe_relation tool (Narrower.Describe). The user sees one line naming the
// tables the model was given (Record.Notice).
//
// What leaves the machine when an engine is configured: the user's question and
// up to three earlier questions of the session, every table name and its column
// names (never types or row data), the interaction id and the client context
// the cloud client already sends. See the README.
package narrowing

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/strongo/aichat/ai/decision"
)

const (
	defaultTimeout  = 1500 * time.Millisecond
	defaultCoolDown = 5 * time.Minute
	defaultProduct  = "datatug"
	// questionID names the one relevance question asked of the engine.
	questionID = "tables"
	// questionInstructions is the criterion applied to each candidate table.
	questionInstructions = "Is this table needed to answer the user's question?"
	// maxCandidates is the most tables put in one question. Above it the engine is
	// not asked (the full schema is kept); rules still apply.
	maxCandidates = 255
	// maxColumnBytes caps one column name in a candidate's description, and
	// maxDescriptionBytes the whole description, so neither one very long name nor
	// a very wide table can make the request unbounded or hide later columns.
	maxColumnBytes      = 64
	maxDescriptionBytes = 512
	// maxRecordedScores caps the scores kept in a Record.
	maxRecordedScores = 32
	// maxPreviousQuestions and maxQuestionBytes bound the history sent along.
	maxPreviousQuestions = 3
	maxQuestionBytes     = 500
	// maxPathHops is the longest foreign-key path closed between two selected tables.
	maxPathHops = 5
	// maxOmittedBytes bounds the omitted-table names listed in the note.
	maxOmittedBytes = 4096
)

// Link is one foreign-key relationship between two tables (either direction;
// the schema is optional and matched case-insensitively with the table name).
type Link struct {
	FromSchema, From string
	ToSchema, To     string
}

// Config assembles a Narrower. The engine, the clock and the formatter are
// injected so that tests need no network and no wall clock.
type Config struct {
	// Relations are the healthy tables and views of the active source, in the
	// order the full schema context lists them.
	Relations []api.CatalogRelation
	// Rules are the project's deterministic knowledge, tried before the engine.
	Rules []Rule
	// Links, when set, returns the schema's foreign keys, read each turn. They let
	// the decision keep the tables on the path between selected tables.
	Links func() []Link
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
	// Timeout bounds the engine call; default 1.5s.
	Timeout time.Duration
	// CoolDown is how long the engine is left alone after a failure or a stop;
	// default 5 minutes.
	CoolDown time.Duration
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
	// ids has one entry per relation: its candidate id, or "" for a relation that
	// cannot be a candidate (an empty name), which always stays in the schema.
	ids        []string
	candidate  []decision.Candidate
	candidates []int // indexes into relations that are candidates, in schema order
	byQual     map[string]int
	byName     map[string]int // lower-cased name -> index, only when unique
	taxonomy   decision.Taxonomy
	chain      decision.Chain // the project rules, when there are any
	links      func() []Link
	engine     decision.ScoredProvider
	policy     decision.SelectionPolicy
	format     func(*api.CatalogSchema) string
	product    string
	timeout    time.Duration
	coolDown   time.Duration
	now        func() time.Time
	fullBytes  int
	warnings   []string

	mu        sync.Mutex
	coolUntil time.Time
	coolStop  string
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
		links:     cfg.Links,
		engine:    cfg.Engine,
		policy:    cfg.Policy,
		format:    cfg.Format,
		product:   cfg.Product,
		timeout:   cfg.Timeout,
		coolDown:  cfg.CoolDown,
		now:       cfg.Now,
	}
	if n.product == "" {
		n.product = defaultProduct
	}
	if n.timeout <= 0 {
		n.timeout = defaultTimeout
	}
	if n.coolDown <= 0 {
		n.coolDown = defaultCoolDown
	}
	if n.now == nil {
		n.now = time.Now
	}
	n.index()
	if ruleProvider != nil {
		n.chain = decision.Chain{Providers: []decision.Provider{ruleProvider}, Policy: &n.policy}
	}
	n.fullBytes = len(n.format(&api.CatalogSchema{Relations: n.relations}))
	n.warnings = n.ruleWarnings(cfg.Rules)
	return n, nil
}

// index names the candidates and builds the lookups.
func (n *Narrower) index() {
	n.ids = candidateIDs(n.relations)
	n.byQual = make(map[string]int, len(n.relations))
	names := make(map[string]int, len(n.relations))
	n.byName = map[string]int{}
	for i, relation := range n.relations {
		if n.ids[i] == "" {
			continue
		}
		n.candidates = append(n.candidates, i)
		n.candidate = append(n.candidate, decision.Candidate{ID: n.ids[i], Description: describe(relation)})
		n.byQual[strings.ToLower(relation.Schema+"."+relation.Name)] = i
		lower := strings.ToLower(relation.Name)
		names[lower]++
		n.byName[lower] = i
	}
	for lower, count := range names {
		if count > 1 {
			delete(n.byName, lower)
		}
	}
	scopes := make([]string, 0, len(n.candidates))
	for _, i := range n.candidates {
		scopes = append(scopes, n.ids[i])
	}
	n.taxonomy = decision.Taxonomy{Modules: []decision.ModuleSpec{{Name: moduleName, Intents: []string{intentName}, Scopes: scopes}}}
}

// Candidates returns the ids the engine is asked about, in schema order.
func (n *Narrower) Candidates() []string {
	out := make([]string, 0, len(n.candidates))
	for _, i := range n.candidates {
		out = append(out, n.ids[i])
	}
	return out
}

// Warnings are problems found when the narrower was built, in words for the user:
// a rule that names a table the schema lacks never fires. They never stop a chat.
func (n *Narrower) Warnings() []string { return slices.Clone(n.warnings) }

func (n *Narrower) ruleWarnings(set []Rule) []string {
	var out []string
	for i, rule := range set {
		for _, table := range rule.Tables {
			if _, ok := n.lookup(table, true); ok {
				continue
			}
			message := fmt.Sprintf("table rule %d (%s) names the table %s, which the schema does not have; the rule never fires", i+1, quoted(rule.Phrase), quoted(table))
			if i, ok := n.lookup(table, false); ok {
				message += fmt.Sprintf(" (did you mean %s? table names are case-sensitive)", quoted(n.ids[i]))
			}
			out = append(out, message)
		}
	}
	return out
}

// lookup finds a candidate by id (exact) or, when exact is false, by a
// case-insensitive name.
func (n *Narrower) lookup(name string, exact bool) (int, bool) {
	for _, i := range n.candidates {
		if exact && n.ids[i] == name {
			return i, true
		}
		if !exact && strings.EqualFold(n.ids[i], name) {
			return i, true
		}
	}
	return 0, false
}

// Describe returns the definition of one table of the schema, as it would appear
// in the model's context. It is the read-only lookup behind describe_relation:
// the model can recover a table the narrowing left out. The name is matched
// exactly, then case-insensitively.
func (n *Narrower) Describe(name string) (string, bool) {
	i, ok := n.lookup(name, true)
	if !ok {
		i, ok = n.lookup(strings.TrimSpace(name), false)
	}
	if !ok {
		return "", false
	}
	return n.format(&api.CatalogSchema{Relations: []api.CatalogRelation{n.relations[i]}}), true
}

// candidateIDs names each relation by its table name, qualified by schema when
// two relations share a name, and made unique if even that collides. A relation
// without a name has no id.
func candidateIDs(relations []api.CatalogRelation) []string {
	count := make(map[string]int, len(relations))
	for _, relation := range relations {
		count[relation.Name]++
	}
	ids := make([]string, len(relations))
	seen := make(map[string]bool, len(relations))
	for i, relation := range relations {
		if strings.TrimSpace(relation.Name) == "" {
			continue
		}
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

// describe is a candidate's description for the engine: its column names, each
// cut to a bounded length. No column types, no values: names only.
func describe(relation api.CatalogRelation) string {
	var b strings.Builder
	for i, column := range relation.Columns {
		piece := truncateBytes(column.Name, maxColumnBytes)
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

// truncateBytes cuts s to at most limit bytes at a character boundary.
func truncateBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "~"
}

// Narrow decides which tables the model sees for prompt. It never fails: every
// problem is a fallback to the full schema, recorded in the returned Record.
func (n *Narrower) Narrow(ctx context.Context, prompt string) Outcome {
	start := n.now()
	rec := Record{
		DecidedAt: start, Policy: n.policy.Name,
		CandidatesBefore: len(n.candidates), CandidatesAfter: len(n.candidates),
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

// decide runs the ladder and returns the candidate indexes to keep, or nil to
// keep everything.
func (n *Narrower) decide(ctx context.Context, prompt string, start time.Time, rec *Record) []int {
	if len(n.candidates) == 0 {
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
		return n.indexesOf(d.RequiredScopes)
	}
	switch {
	case n.engine == nil:
		rec.FallbackReason = ReasonDisabled
		return nil
	case len(n.candidates) > maxCandidates:
		rec.FallbackReason = ReasonTooManyCandidates
		return nil
	case n.coolingDown(rec):
		return nil
	}
	return n.ask(ctx, prompt, historyFrom(ctx), rec)
}

// indexesOf maps candidate ids to relation indexes.
func (n *Narrower) indexesOf(ids []string) []int {
	var out []int
	for _, i := range n.candidates {
		if slices.Contains(ids, n.ids[i]) {
			out = append(out, i)
		}
	}
	return out
}

// coolingDown reports whether the engine is being left alone after a failure,
// and records why.
func (n *Narrower) coolingDown(rec *Record) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.now().Before(n.coolUntil) {
		return false
	}
	rec.FallbackReason, rec.StoppedBy = ReasonCoolingDown, n.coolStop
	return true
}

// remember starts the cool-down after a failure or a stop.
func (n *Narrower) remember(stop string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.coolUntil, n.coolStop = n.now().Add(n.coolDown), stop
}

// coolsDown lists the engine outcomes after which the engine is left alone: it
// is slow, failing, refusing or misbehaving, and asking again next turn would
// only repeat the wait. A request the engine rejected as invalid or a call the
// user cancelled says nothing about the engine.
func coolsDown(outcome string) bool {
	switch outcome {
	case decision.AttemptRejected, decision.AttemptCancelled:
		return false
	}
	return true
}

// ask puts the one relevance question to the engine, applies the policy, and
// widens a selection with the carried tables and the foreign-key paths between
// them. It returns nil to keep everything.
func (n *Narrower) ask(ctx context.Context, prompt string, history History, rec *Record) []int {
	req := n.request(prompt, history)
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
		stop := ""
		switch outcome {
		case decision.AttemptQuota, decision.AttemptBudget, decision.AttemptMisconfigured:
			rec.StoppedBy, stop = outcome, outcome
		}
		if coolsDown(outcome) {
			n.remember(stop)
		}
		return nil
	}
	verr := decision.ValidateScoreResult(req, res)
	if verr == nil {
		verr = n.complete(res.Answers[questionID])
	}
	if verr != nil {
		judged.Outcome, judged.Detail = decision.AttemptInvalid, decision.InvalidDetail(verr)
		rec.Attempts = append(rec.Attempts, attemptsOf(append(slices.Clone(report.Attempts), judged))...)
		rec.FallbackReason = decision.AttemptInvalid
		n.remember("")
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
	return n.widen(sel, history, rec)
}

// request is the one relevance question, with the session's recent questions as
// context so that a follow-up is scored with what it follows.
func (n *Narrower) request(prompt string, history History) decision.ScoreRequest {
	req := decision.ScoreRequest{
		Product: n.product,
		Text:    prompt,
		Questions: []decision.Question{{
			ID: questionID, Kind: decision.KindRelevance,
			Instructions: questionInstructions, Candidates: n.candidate,
		}},
	}
	if previous := history.Questions[max(0, len(history.Questions)-maxPreviousQuestions):]; len(previous) > 0 {
		trimmed := make([]string, len(previous))
		for i, q := range previous {
			trimmed[i] = truncateBytes(q, maxQuestionBytes)
		}
		req.Context = map[string]any{"previousQuestions": trimmed}
	}
	return req
}

// complete checks the engine scored every candidate exactly once: a partial or
// repeating answer cannot be trusted to have judged the tables it left out.
func (n *Narrower) complete(answer decision.Answer) error {
	seen := make(map[string]bool, len(answer.Scores))
	for _, s := range answer.Scores {
		if seen[s.ID] {
			return errors.New("a candidate was scored twice")
		}
		seen[s.ID] = true
	}
	if len(seen) != len(n.candidates) {
		return errors.New("not every candidate was scored")
	}
	return nil
}

// widen turns a policy selection into the tables to keep: the selected and the
// possibly relevant tables, plus the previous turn's tables (a follow-up is
// about the same data) unless the engine is confidently about something else,
// plus the tables on the foreign-key path between all of them.
func (n *Narrower) widen(sel decision.Selection, history History, rec *Record) []int {
	keep := map[int]bool{}
	for _, id := range append(slices.Clone(sel.Picks), sel.Potential...) {
		i, _ := n.lookup(id, true) // the engine's ids were validated as candidates
		keep[i] = true
	}
	var previous []int
	for _, id := range history.Kept {
		if i, ok := n.lookup(id, true); ok {
			previous = append(previous, i)
		}
	}
	adjacency := n.adjacency()
	var carried []int
	if !n.topicChanged(sel, previous, adjacency) {
		for _, i := range previous {
			if !keep[i] {
				keep[i] = true
				carried = append(carried, i)
			}
		}
	}
	var endpoints []int
	for i := range keep {
		endpoints = append(endpoints, i)
	}
	slices.Sort(endpoints)
	var closure []int
	for _, i := range n.pathTables(endpoints, adjacency) {
		if !keep[i] {
			keep[i] = true
			closure = append(closure, i)
		}
	}
	rec.Carried, rec.Closure = n.idsOf(carried), n.idsOf(closure)
	var out []int
	for _, i := range n.candidates {
		if keep[i] {
			out = append(out, i)
		}
	}
	return out
}

// topicChanged is the rule for dropping the previous turn's tables: the engine is
// confident (a strong pick) about tables that are neither among the previous
// ones nor foreign-key neighbours of them. Without a previous turn or without
// foreign-key information a follow-up is always assumed, since keeping more is
// the safe error.
func (n *Narrower) topicChanged(sel decision.Selection, previous []int, adjacency map[int][]int) bool {
	if len(previous) == 0 || len(sel.Strong) == 0 || len(adjacency) == 0 {
		return false
	}
	for _, id := range sel.Strong {
		i, _ := n.lookup(id, true)
		if slices.Contains(previous, i) {
			return false
		}
		for _, neighbour := range adjacency[i] {
			if slices.Contains(previous, neighbour) {
				return false
			}
		}
	}
	return true
}

// adjacency is the foreign-key graph over the candidates, built from the
// current links. It is empty when no links are known.
func (n *Narrower) adjacency() map[int][]int {
	if n.links == nil {
		return nil
	}
	graph := map[int][]int{}
	for _, link := range n.links() {
		from, okFrom := n.resolve(link.FromSchema, link.From)
		to, okTo := n.resolve(link.ToSchema, link.To)
		if okFrom && okTo && from != to {
			graph[from] = append(graph[from], to)
			graph[to] = append(graph[to], from)
		}
	}
	return graph
}

// resolve finds the candidate a link end names.
func (n *Narrower) resolve(schema, name string) (int, bool) {
	if i, ok := n.byQual[strings.ToLower(schema+"."+name)]; ok {
		return i, true
	}
	i, ok := n.byName[strings.ToLower(name)]
	return i, ok
}

// pathTables returns the tables on a shortest foreign-key path (at most
// maxPathHops hops) between every pair of endpoints that are connected,
// endpoints included.
func (n *Narrower) pathTables(endpoints []int, graph map[int][]int) []int {
	var out []int
	for _, from := range endpoints {
		parent := map[int]int{from: -1}
		depth := map[int]int{from: 0}
		queue := []int{from}
		for len(queue) > 0 {
			at := queue[0]
			queue = queue[1:]
			if depth[at] == maxPathHops {
				continue
			}
			for _, next := range graph[at] {
				if _, seen := parent[next]; !seen {
					parent[next], depth[next] = at, depth[at]+1
					queue = append(queue, next)
				}
			}
		}
		for _, to := range endpoints {
			if _, reached := parent[to]; !reached || to == from {
				continue
			}
			for at := to; at != -1; at = parent[at] {
				out = append(out, at)
			}
		}
	}
	return out
}

func (n *Narrower) idsOf(indexes []int) []string {
	var out []string
	for _, i := range n.candidates {
		if slices.Contains(indexes, i) {
			out = append(out, n.ids[i])
		}
	}
	return out
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

// apply keeps the relations whose indexes are in kept (schema order; a relation
// that cannot be a candidate always stays), records them, and returns the
// narrowed schema context, or "" when nothing was dropped.
func (n *Narrower) apply(kept []int, rec *Record) string {
	var (
		relations []api.CatalogRelation
		ids       []string
		omitted   []string
	)
	for i, relation := range n.relations {
		switch {
		case n.ids[i] == "":
			relations = append(relations, relation)
		case slices.Contains(kept, i):
			relations = append(relations, relation)
			ids = append(ids, n.ids[i])
		default:
			omitted = append(omitted, n.ids[i])
		}
	}
	if len(omitted) == 0 {
		rec.FallbackReason = ReasonNoReduction
		return ""
	}
	// The note comes first: no table or column name can precede it, so none can
	// pass for it.
	text := noteFor(len(ids), len(n.candidates), omitted) + "\n" + n.format(&api.CatalogSchema{Relations: relations})
	rec.Narrowed, rec.Kept = true, ids
	rec.CandidatesAfter, rec.ContextBytesAfter = len(ids), len(text)
	return text
}

// noteFor tells the model the list is a selection, names (names only) the
// tables left out, and says how to get one: a model that cannot see a table it
// needs must be able to ask for it. Names are quoted, so a hostile name cannot
// break the line.
func noteFor(kept, total int, omitted []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Note: DataTug narrowed this schema to the %d of %d relations judged relevant to this request. Omitted relations (names only):", kept, total)
	listed := 0
	for _, name := range omitted {
		piece := " " + strconv.Quote(name)
		if b.Len()+len(piece) > maxOmittedBytes {
			break
		}
		b.WriteString(piece)
		listed++
	}
	if listed < len(omitted) {
		fmt.Fprintf(&b, " and %d more", len(omitted)-listed)
	}
	b.WriteString(". If the request needs one of them, call describe_relation with its exact name to read its definition; do not guess the names of other relations.")
	return b.String()
}

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
