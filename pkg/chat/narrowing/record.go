package narrowing

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/strongo/aichat/ai/cloudproto"
	"github.com/strongo/aichat/ai/decision"
)

// Mechanism says which rung of the decision ladder produced the narrowing.
// The ladder is: deterministic project knowledge, then a decision engine; the
// full schema is the floor below both.
type Mechanism string

const (
	// MechanismNone: nothing decided, so the chat kept its full schema context.
	MechanismNone Mechanism = ""
	// MechanismDeterministic: a project rule answered, and no engine was called.
	MechanismDeterministic Mechanism = "deterministic"
	// MechanismEngine: a decision engine (Jev, through the cloud decider) scored
	// the candidate tables and the selection policy chose.
	MechanismEngine Mechanism = "engine"
)

// Fallback reasons recorded when the full schema was kept, besides the ones
// that name an engine outcome as the library reports it: an attempt outcome
// (timeout, unavailable, unsupported, auth, rejected, quota, budget,
// misconfigured, cancelled, error, invalid) or a selection outcome (uncertain,
// none, unscored). All are short identifiers, safe as telemetry dimensions.
const (
	ReasonDisabled     = "disabled"
	ReasonNoCandidates = "no_candidates"
	ReasonNoReduction  = "no_reduction"
	// ReasonTooManyCandidates: more tables than one engine question may hold.
	ReasonTooManyCandidates = "too_many_candidates"
	// ReasonCoolingDown: the engine failed or refused recently and is left alone
	// for a while.
	ReasonCoolingDown = "cooling_down"
)

// Score is one candidate's probability as the engine reported it.
type Score struct {
	ID          string  `json:"id"`
	Probability float64 `json:"probability"`
}

// Attempt is one provider's part in the decision, in the library's vocabulary
// (decision.Attempt outcomes such as decided, timeout, quota, unavailable).
type Attempt struct {
	Provider  string `json:"provider"`
	Outcome   string `json:"outcome"`
	Detail    string `json:"detail,omitempty"`
	LatencyMs int64  `json:"latencyMs"`
}

// Record is the inspectable provenance of one narrowing decision. It is stored
// with the chat session and summarised into telemetry. It holds table names,
// scores and counts, never row data.
type Record struct {
	DecidedAt time.Time `json:"decidedAt"`
	// Mechanism is the rung that decided; Engine and Model name the answering
	// engine and its model id when one was asked.
	Mechanism Mechanism `json:"mechanism,omitempty"`
	Engine    string    `json:"engine,omitempty"`
	Model     string    `json:"model,omitempty"`
	// Provenance is the decision.Provenance class of the answer that stood:
	// deterministic, calibrated or self_reported.
	Provenance string `json:"provenance,omitempty"`
	Policy     string `json:"policy,omitempty"`
	// DeciderEnabled is true when a decision engine was configured when the
	// question was asked. The question of a turn is only reused as history for a
	// later question if it was (a question typed before the user opted in must
	// never be sent after).
	DeciderEnabled bool `json:"deciderEnabled,omitempty"`
	// Verdict is the selection policy's judgement, for example "several" or
	// "uncertain: only_potential".
	Verdict string `json:"verdict,omitempty"`
	// Narrowed is true only when the model was given fewer tables than the
	// full schema. FallbackReason says why not, when the answer is false.
	Narrowed       bool   `json:"narrowed"`
	FallbackReason string `json:"fallbackReason,omitempty"`
	// StoppedBy is set when the engine refused for a reason that must not be
	// answered by asking a bigger, paid model: an exhausted allowance (quota), a
	// spent budget (budget) or a misconfigured endpoint (misconfigured).
	StoppedBy string `json:"stoppedBy,omitempty"`

	CandidatesBefore int `json:"candidatesBefore"`
	CandidatesAfter  int `json:"candidatesAfter"`
	// Selected are the tables the engine or rule selected; Strong is the subset
	// it was most sure of; Potential are tables it judged possibly relevant, kept
	// in the model's context so that ambiguity is preserved, not hidden.
	// Proposed holds the picks of an uncalibrated answer, which are never a
	// selection and are recorded only.
	Selected  []string `json:"selected,omitempty"`
	Strong    []string `json:"strong,omitempty"`
	Potential []string `json:"potential,omitempty"`
	Proposed  []string `json:"proposed,omitempty"`
	// Carried are tables kept because the previous turn kept them (a follow-up is
	// about the same data); Closure are tables kept because they lie on the
	// foreign-key path between selected tables.
	Carried []string `json:"carried,omitempty"`
	Closure []string `json:"closure,omitempty"`
	// Kept are the tables whose definitions reached the model, in schema order.
	Kept   []string `json:"kept,omitempty"`
	Scores []Score  `json:"scores,omitempty"`

	ContextBytesBefore int `json:"contextBytesBefore"`
	ContextBytesAfter  int `json:"contextBytesAfter"`

	Attempts     []Attempt `json:"attempts,omitempty"`
	LatencyMs    int64     `json:"latencyMs"`
	InputTokens  int       `json:"inputTokens,omitempty"`
	OutputTokens int       `json:"outputTokens,omitempty"`
}

// Summary is the record as one telemetry-safe identifier: it states the counts
// and the reason, never a table name.
func (r Record) Summary() string {
	if r.Narrowed {
		return fmt.Sprintf("narrowed:before=%d:after=%d", r.CandidatesBefore, r.CandidatesAfter)
	}
	reason := r.FallbackReason
	if r.StoppedBy != "" {
		reason = "stopped_" + r.StoppedBy
	}
	return fmt.Sprintf("full_schema:reason=%s:before=%d", reason, r.CandidatesBefore)
}

// maxNoticeTables bounds the table names the user-facing line lists.
const maxNoticeTables = 12

// Notice is the one line shown to the user when the model was given fewer tables
// than the schema holds ("" when nothing was narrowed). Names are quoted so that
// a hostile name cannot add lines.
func (r Record) Notice() string {
	if !r.Narrowed {
		return ""
	}
	names := make([]string, 0, len(r.Kept))
	for _, name := range r.Kept[:min(len(r.Kept), maxNoticeTables)] {
		names = append(names, strconv.Quote(name))
	}
	line := fmt.Sprintf("Context narrowed to %d of %d tables: %s", r.CandidatesAfter, r.CandidatesBefore, strings.Join(names, ", "))
	if len(r.Kept) > maxNoticeTables {
		line += fmt.Sprintf(" and %d more", len(r.Kept)-maxNoticeTables)
	}
	return line + "."
}

// DetectionSteps renders the decision as the cloud interaction report's
// detection steps: one for the rung that decided or, when an engine was asked
// and the full schema was kept, one for that engine; none when no rule matched
// and no engine was asked (the decider is disabled). The steps carry counts,
// mechanism and the engine and model ids, but no table names or question text.
func (r Record) DetectionSteps() []cloudproto.DetectionStep {
	switch {
	case r.Mechanism == MechanismDeterministic:
		return []cloudproto.DetectionStep{{Method: "deterministic", Detector: "datatug.narrow.rules", Result: r.Summary()}}
	case r.Engine == "":
		return nil
	}
	step := cloudproto.DetectionStep{Method: engineMethod(r.Engine), Detector: "datatug.narrow.tables", Version: safeDimension(r.Model), Result: r.Summary()}
	if top := r.topPickProbability(); top > 0 {
		step.Confidence = &top
	}
	return []cloudproto.DetectionStep{step}
}

// topPickProbability is the best probability among the selected tables of a
// calibrated answer, 0 when there is none.
func (r Record) topPickProbability() float64 {
	if r.Provenance != string(decision.ProvenanceCalibrated) || len(r.Selected) == 0 {
		return 0
	}
	for _, s := range r.Scores {
		if s.ID == r.Selected[0] {
			return s.Probability
		}
	}
	return 0
}

// engineMethod names the detection method of an engine for telemetry. The
// server groups Jev answers by the substring "jev" in the engine name.
func engineMethod(engine string) string {
	switch name := strings.ToLower(engine); {
	case strings.Contains(name, "jev"):
		return "jev"
	case strings.Contains(name, "llm"):
		return "llm"
	}
	return "decision_engine"
}

// safeDimension returns s when it is a short telemetry identifier, else "".
func safeDimension(s string) string {
	if len(s) > 64 {
		return ""
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.ContainsRune("-_.:/+=", c):
		default:
			return ""
		}
	}
	return s
}
