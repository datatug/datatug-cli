package endpoints

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/semantic"
)

// applicableRequest is POST queries/applicable's request body
// (api-contract.md "Endpoint table": Scope + {values:Fact[]}). Core's
// pkg/apicontract (the schema authority, v0.26.0) defines ApplicableResponse
// but not a request envelope for this endpoint — only Scope and Fact
// themselves are shared schema types, both reused here unchanged; the
// enclosing envelope is real appendix-defined shape with no
// pkg/apicontract type of its own. S78's report to the lead names this as a
// datatug-core gap (the appendix should probably grow an
// apicontract.ApplicableRequest alongside ExecutionRequest), not something
// to fork the underlying Scope/Fact types over.
type applicableRequest struct {
	apicontract.Scope
	Values []apicontract.Fact `json:"values"`
}

// semanticApplicableHandler is POST /datatug/queries/applicable, rewritten
// (Task 12) to the appendix's exact envelope: Scope + {values:Fact[]},
// response {applicable:Candidate[],notYet:Candidate[]} — replacing the
// previous ApplicableRequest/ApplicableEntry/NotYetEntry shape.
func semanticApplicableHandler(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeContractError(w, r, newInvalidRequest("", "failed to read request body: "+err.Error()))
		return
	}
	var req applicableRequest
	if err := decodeContractBody(body, &req); err != nil {
		writeContractError(w, r, newInvalidRequest("", err.Error()))
		return
	}
	resp, err := computeSemanticApplicable(r.Context(), req)
	writeContractResponse(w, r, err, resp)
}

func computeSemanticApplicable(ctx context.Context, req applicableRequest) (apicontract.ApplicableResponse, error) {
	if err := validateScope(req.Scope); err != nil {
		return apicontract.ApplicableResponse{}, err
	}
	projectDir, ok := api.ProjectDir(req.Project)
	if !ok {
		return apicontract.ApplicableResponse{}, newNotFound("unknown project")
	}
	projStore, err := api.ProjectStoreFor(req.Project)
	if err != nil {
		return apicontract.ApplicableResponse{}, newInvalidRequest("project", err.Error())
	}
	queries, err := loadModuleQueries(projectDir)
	if err != nil {
		return apicontract.ApplicableResponse{}, err
	}

	// Disabled facts are ignored (api-contract.md "Binding and context
	// behavior"). More than one DISTINCT value within the same entity.field
	// is ambiguous and is excluded from `available` entirely, so
	// semantic.Applicable naturally reports every query needing it as
	// missing that field too — buildApplicableCandidate then attaches the
	// Ambiguous detail (as opposed to a plain "nothing supplied") when it
	// re-derives the same key. This is Task 12's OWN simplified ambiguity
	// rule: it does not yet implement the appendix's full origin-tier
	// precedence (an explicit selection beats a context fact rather than
	// conflicting with it) — that's plan task 15's scope
	// (AC:typed-context-isolation); every distinct value at ANY origin
	// currently conflicts, which is conservative (never silently picks a
	// value the browser would not have) rather than wrong.
	enabledByField := map[string][]apicontract.Fact{}
	for _, f := range req.Values {
		if !f.Enabled {
			continue
		}
		key := fieldKey(f.Entity, f.Field)
		enabledByField[key] = append(enabledByField[key], f)
	}
	ambiguous := map[string]bool{}
	var available []semantic.SemanticValue
	latestFactByField := map[string]apicontract.Fact{}
	for key, facts := range enabledByField {
		if distinctFactValues(facts) > 1 {
			ambiguous[key] = true
			continue
		}
		f := facts[len(facts)-1]
		native, nativeErr := nativeValue(f.Value)
		if nativeErr != nil {
			continue // an unconvertible TypedValue simply cannot bind anything; treated as absent, not a request-level error.
		}
		provenance := semantic.Inferred
		if f.Mapping == "declared" {
			provenance = semantic.Declared
		}
		sv := semantic.SemanticValue{Entity: f.Entity, Field: f.Field, Value: fmt.Sprint(native), Provenance: provenance}
		if f.Physical != nil {
			sv.Source, sv.Collection, sv.Column = f.Physical.Source, f.Physical.Collection, f.Physical.Column
		}
		available = append(available, sv)
		latestFactByField[key] = f
	}

	applicableQ, notYetQ := semantic.Applicable(queries, available)

	var candidates []apicontract.Candidate
	for _, aq := range applicableQ {
		candidates = append(candidates, buildApplicableCandidate(ctx, projStore, projectDir, req.Environment, aq, ambiguous, enabledByField, latestFactByField))
	}
	for _, nq := range notYetQ {
		candidates = append(candidates, buildNotYetCandidate(ctx, projStore, projectDir, req.Environment, nq, ambiguous, enabledByField))
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].QueryID < candidates[j].QueryID })

	resp := apicontract.ApplicableResponse{Applicable: []apicontract.Candidate{}, NotYet: []apicontract.Candidate{}}
	for _, c := range candidates {
		if c.State == apicontract.CandidateStateRunnable {
			resp.Applicable = append(resp.Applicable, c)
		} else {
			resp.NotYet = append(resp.NotYet, c)
		}
	}
	return resp, nil
}

func fieldKey(entity, field string) string { return entity + "\x00" + field }

// distinctFactValues counts how many DISTINCT typedValueEqual values facts
// carries (api-contract.md: "More than one distinct typed value ... is
// ambiguous").
func distinctFactValues(facts []apicontract.Fact) int {
	var distinct []apicontract.TypedValue
	for _, f := range facts {
		found := false
		for _, d := range distinct {
			if typedValueEqual(d, f.Value) {
				found = true
				break
			}
		}
		if !found {
			distinct = append(distinct, f.Value)
		}
	}
	return len(distinct)
}

func mapFactOrigin(o string) string {
	switch o {
	case apicontract.FactOriginSelection:
		return apicontract.BindingOriginSelection
	case apicontract.FactOriginContext:
		return apicontract.BindingOriginContext
	default:
		return apicontract.BindingOriginManual
	}
}

// buildApplicableCandidate builds one Candidate for a query
// semantic.Applicable already found fully bindable. It still applies Task
// 12's own additions the datatug-core helper does not know about:
// non-semantic (no Meta) required parameters (always missing — nothing
// can auto-bind them) and target resolution (api.EligibleTargets) —
// either of which can still demote it out of the "runnable" state despite
// every semantic parameter being bound.
func buildApplicableCandidate(ctx context.Context, projStore datatug.ProjectStore, projectDir, environment string, aq semantic.ApplicableQuery, ambiguous map[string]bool, enabledByField map[string][]apicontract.Fact, latestFactByField map[string]apicontract.Fact) apicontract.Candidate {
	var bindings []apicontract.Binding
	var chain []apicontract.ChainStep
	for i, b := range aq.Bindings {
		key := fieldKey(b.From.Entity, b.From.Field)
		fact := latestFactByField[key]
		bindings = append(bindings, apicontract.Binding{
			ParameterID: b.Parameter, Value: fact.Value, Origin: mapFactOrigin(fact.Origin),
			OriginEvidence: apicontract.BindingOriginEvidenceClientReported, FactID: fact.ID,
		})
		explanation := b.Parameter
		if i < len(aq.Chain) {
			explanation = aq.Chain[i]
		}
		chain = append(chain, apicontract.ChainStep{ParameterID: b.Parameter, FactID: fact.ID, Explanation: explanation})
	}
	missing, extraChain, ambig := missingNonSemanticParameters(aq.Query)
	chain = append(chain, extraChain...)
	return finishCandidate(ctx, projStore, projectDir, environment, aq.Query, bindings, chain, missing, ambig)
}

// buildNotYetCandidate builds one Candidate for a query
// semantic.Applicable found still missing at least one semantic parameter,
// translating its "entity.field" Missing entries into actual parameter IDs
// (api-contract.md: "missing: string[] // parameter IDs") and attaching an
// Ambiguous entry instead of a plain "missing" explanation when that
// entity.field's block was due to conflicting facts, not an absent one.
func buildNotYetCandidate(ctx context.Context, projStore datatug.ProjectStore, projectDir, environment string, nq semantic.NotYetApplicable, ambiguous map[string]bool, enabledByField map[string][]apicontract.Fact) apicontract.Candidate {
	var missing []string
	var chain []apicontract.ChainStep
	var ambig []apicontract.Ambiguous
	for _, ef := range nq.Missing {
		for _, p := range nq.Query.Parameters {
			if p.Meta == nil || p.Meta.Entity+"."+p.Meta.Field != ef {
				continue
			}
			missing = append(missing, p.ID)
			key := fieldKey(p.Meta.Entity, p.Meta.Field)
			if ambiguous[key] {
				var factIDs []string
				for _, f := range enabledByField[key] {
					factIDs = append(factIDs, f.ID)
				}
				ambig = append(ambig, apicontract.Ambiguous{ParameterID: p.ID, FactIDs: factIDs})
				chain = append(chain, apicontract.ChainStep{ParameterID: p.ID, Explanation: fmt.Sprintf("%d conflicting values available for %s; select one to resolve", len(factIDs), ef)})
			} else {
				chain = append(chain, apicontract.ChainStep{ParameterID: p.ID, Explanation: fmt.Sprintf("%s is not available from the current selection or context", ef)})
			}
		}
	}
	extraMissing, extraChain, extraAmbig := missingNonSemanticParameters(nq.Query)
	missing = append(missing, extraMissing...)
	chain = append(chain, extraChain...)
	ambig = append(ambig, extraAmbig...)
	return finishCandidate(ctx, projStore, projectDir, environment, nq.Query, nil, chain, missing, ambig)
}

// missingNonSemanticParameters lists every required parameter with no
// Meta tag: semantic.Applicable never considers these at all ("A parameter
// with no Meta is never considered - it stays unbound and never blocks
// applicability"), but the appendix's own missing[] explicitly includes
// "non-semantic required parameters" too — nothing in this Feature auto-
// binds them, so they are always missing regardless of available facts.
func missingNonSemanticParameters(q *datatug.QueryDef) (missing []string, chain []apicontract.ChainStep, ambiguous []apicontract.Ambiguous) {
	for _, p := range q.Parameters {
		if p.Meta != nil || !p.IsRequired {
			continue
		}
		missing = append(missing, p.ID)
		chain = append(chain, apicontract.ChainStep{ParameterID: p.ID, Explanation: "required parameter has no semantic tag; supply it explicitly"})
	}
	return missing, chain, ambiguous
}

// finishCandidate applies target resolution and computes the final closed-
// set State, then assembles the Candidate. State priority when more than
// one condition applies (Task 12's own documented rule, since the
// appendix's State enum can hold only one value at a time): source-
// unavailable (nothing to run against, regardless of bindings) beats
// needs-input (a binding is missing/ambiguous) beats needs-target (bindings
// are fine but more than one authorized source exists) beats runnable.
func finishCandidate(ctx context.Context, projStore datatug.ProjectStore, projectDir, environment string, q *datatug.QueryDef, bindings []apicontract.Binding, chain []apicontract.ChainStep, missing []string, ambiguous []apicontract.Ambiguous) apicontract.Candidate {
	eligible, err := api.EligibleTargets(ctx, projStore, projectDir, environment, q)
	if err != nil {
		eligible = nil
	}
	sort.Strings(missing)

	var state string
	var selectedSource string
	targets := candidateTargets(eligible)
	switch {
	case len(eligible) == 0:
		state = apicontract.CandidateStateSourceUnavailable
	case len(missing) > 0 || len(ambiguous) > 0:
		state = apicontract.CandidateStateNeedsInput
	case len(eligible) == 1:
		state = apicontract.CandidateStateRunnable
		selectedSource = eligible[0].ID
	default:
		state = apicontract.CandidateStateNeedsTarget
	}

	if bindings == nil {
		bindings = []apicontract.Binding{}
	}
	if chain == nil {
		chain = []apicontract.ChainStep{}
	}
	if missing == nil {
		missing = []string{}
	}
	if ambiguous == nil {
		ambiguous = []apicontract.Ambiguous{}
	}
	if targets == nil {
		targets = []apicontract.CandidateTarget{}
	}
	return apicontract.Candidate{
		QueryID: q.ID, Targets: targets, SelectedSource: selectedSource,
		Bindings: bindings, Chain: chain, Missing: missing, Ambiguous: ambiguous, State: state,
	}
}
