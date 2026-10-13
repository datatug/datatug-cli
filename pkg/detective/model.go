// Package detective implements local project Checks and investigation Cases.
// The JSON types in this file are the shared web, terminal and agent contract.
package detective

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/investigation"
)

type Value = investigation.TypedValue

// InputValue uses Core's scalar/set wire form and adds a typed closed range.
// A range is investigation context; a saved query must explicitly bind its
// start/end to scalar parameters before execution.
type InputValue struct {
	Scalar *Value
	Set    *apicontract.TypedValueSet
	Range  *ValueRange
}

type ValueRange struct {
	Type  string `json:"type"` // range
	Start Value  `json:"start"`
	End   Value  `json:"end"`
}

func (v InputValue) Validate() error {
	n := 0
	if v.Scalar != nil {
		n++
	}
	if v.Set != nil {
		n++
	}
	if v.Range != nil {
		n++
	}
	if n != 1 {
		return fmt.Errorf("exactly one scalar, set or range is required")
	}
	if v.Scalar != nil {
		return v.Scalar.Validate()
	}
	if v.Set != nil {
		return v.Set.Validate()
	}
	if v.Range.Type != "range" || v.Range.Start.Type != v.Range.End.Type {
		return fmt.Errorf("invalid typed range")
	}
	if err := v.Range.Start.Validate(); err != nil {
		return err
	}
	return v.Range.End.Validate()
}

func (v InputValue) MarshalJSON() ([]byte, error) {
	if err := v.Validate(); err != nil {
		return nil, err
	}
	if v.Scalar != nil {
		return json.Marshal(v.Scalar)
	}
	if v.Set != nil {
		return json.Marshal(v.Set)
	}
	return json.Marshal(v.Range)
}

func (v *InputValue) UnmarshalJSON(data []byte) error {
	var kind struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &kind); err != nil {
		return err
	}
	switch kind.Type {
	case "set":
		var x apicontract.TypedValueSet
		if err := json.Unmarshal(data, &x); err != nil {
			return err
		}
		*v = InputValue{Set: &x}
	case "range":
		var x ValueRange
		if err := json.Unmarshal(data, &x); err != nil {
			return err
		}
		*v = InputValue{Range: &x}
	default:
		var x Value
		if err := json.Unmarshal(data, &x); err != nil {
			return err
		}
		*v = InputValue{Scalar: &x}
	}
	return v.Validate()
}

type Input struct {
	Name        string                  `json:"name"`
	Type        investigation.ValueType `json:"type"`
	Cardinality string                  `json:"cardinality"` // scalar | set | range
	Required    bool                    `json:"required,omitempty"`
}

// Binding maps a child input to a parent input or a query-result row column.
type Binding struct {
	Input      string `json:"input"`
	FromInput  string `json:"fromInput,omitempty"`
	Part       string `json:"part,omitempty"` // start | end for range context
	FromColumn string `json:"fromColumn,omitempty"`
}

type Validator struct {
	ID            string    `json:"id"`
	ValidatorID   string    `json:"validatorId,omitempty"` // reference to promoted catalog definition
	Scope         string    `json:"scope"`                 // row | set
	Kind          string    `json:"kind"`                  // non-null | equals-fields | row-count | unique | sum-equals | check
	Severity      string    `json:"severity"`              // critical | error | warning | info
	Field         string    `json:"field,omitempty"`
	OtherField    string    `json:"otherField,omitempty"`
	Expected      *Value    `json:"expected,omitempty"`
	CheckID       string    `json:"checkId,omitempty"`
	Bindings      []Binding `json:"bindings,omitempty"`
	OnUnavailable string    `json:"onUnavailable,omitempty"` // block | fail | warn | skip
}

// PublishedValidator is explicitly promoted; ordinary Validators remain
// attached to their query step. The promoted version is snapshotted in runs.
type PublishedValidator struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Version   int64     `json:"version"`
	Validator Validator `json:"validator"`
}

// QueryValidators are local to one saved query until explicitly promoted.
type QueryValidators struct {
	QueryID    string      `json:"queryId"`
	Version    int64       `json:"version"`
	Validators []Validator `json:"validators"`
}

type Step struct {
	ID            string      `json:"id"`
	Kind          string      `json:"kind"` // query | check
	QueryID       string      `json:"queryId,omitempty"`
	CheckID       string      `json:"checkId,omitempty"`
	Bindings      []Binding   `json:"bindings,omitempty"`
	Validators    []Validator `json:"validators,omitempty"`
	OnUnavailable string      `json:"onUnavailable,omitempty"`
}

type Check struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Version     int64     `json:"version"`
	Inputs      []Input   `json:"inputs"`
	Steps       []Step    `json:"steps"`
	UpdatedAt   time.Time `json:"updatedAt"`
	UpdatedBy   string    `json:"updatedBy"`
}

type EvidenceRef struct {
	ID   string `json:"id"`
	Kind string `json:"kind"` // check-run | query | external
	Ref  string `json:"ref"`
	Note string `json:"note,omitempty"`
}

type Finding struct {
	ID           string   `json:"id"`
	Text         string   `json:"text"`
	Status       string   `json:"status"` // hypothesis | confirmed
	EvidenceRefs []string `json:"evidenceRefs,omitempty"`
}

type CaseEvent struct {
	ID      string    `json:"id"`
	At      time.Time `json:"at"`
	Actor   string    `json:"actor"`
	Type    string    `json:"type"`
	Message string    `json:"message,omitempty"`
	Ref     string    `json:"ref,omitempty"`
}

type Case struct {
	ID            string                `json:"id"`
	Title         string                `json:"title"`
	Status        string                `json:"status"` // active | closed | archived
	Version       int64                 `json:"version"`
	Context       map[string]InputValue `json:"context"`
	IncidentRefs  []string              `json:"incidentRefs"`
	ScenarioRefs  []string              `json:"scenarioRefs"`
	RunRefs       []string              `json:"runRefs"`
	Evidence      []EvidenceRef         `json:"evidence"`
	Findings      []Finding             `json:"findings"`
	Timeline      []CaseEvent           `json:"timeline"`
	ClosureReason string                `json:"closureReason,omitempty"`
	CreatedAt     time.Time             `json:"createdAt"`
	UpdatedAt     time.Time             `json:"updatedAt"`
}

type ScenarioStep struct {
	ID   string `json:"id"`
	Kind string `json:"kind"` // query | check
	Ref  string `json:"ref"`
}

type Scenario struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Version     int64          `json:"version"`
	Inputs      []Input        `json:"inputs"`
	Steps       []ScenarioStep `json:"steps"`
}

type ValidationResult struct {
	ValidatorID     string       `json:"validatorId"`
	ExecutionStatus string       `json:"executionStatus"`
	Outcome         string       `json:"outcome"` // pass | fail | unknown
	Severity        string       `json:"severity"`
	RowIndex        *int         `json:"rowIndex,omitempty"`
	Message         string       `json:"message,omitempty"`
	Children        []StepResult `json:"children,omitempty"`
}

type StepResult struct {
	StepID            string                    `json:"stepId"`
	Kind              string                    `json:"kind"`
	QueryID           string                    `json:"queryId,omitempty"`
	Source            string                    `json:"source,omitempty"`
	ObservedAt        string                    `json:"observedAt,omitempty"`
	ExecutionProfile  string                    `json:"executionProfile,omitempty"`
	Execution         *apicontract.ExecutionRef `json:"execution,omitempty"`
	QueryRevision     string                    `json:"queryRevision,omitempty"`
	ResultFingerprint string                    `json:"resultFingerprint,omitempty"`
	ErrorCode         string                    `json:"errorCode,omitempty"`
	DurationMs        int64                     `json:"durationMs,omitempty"`
	CheckID           string                    `json:"checkId,omitempty"`
	ExecutionStatus   string                    `json:"executionStatus"` // pending | running | completed | error | skipped
	Outcome           string                    `json:"outcome"`         // pass | fail | unknown
	Severity          string                    `json:"severity,omitempty"`
	RowCount          int                       `json:"rowCount,omitempty"`
	Message           string                    `json:"message,omitempty"`
	Validators        []ValidationResult        `json:"validators,omitempty"`
	Children          []StepResult              `json:"children,omitempty"`
}

type CheckRun struct {
	ID                  string               `json:"id"`
	Kind                string               `json:"kind"` // check | query-validators
	CheckID             string               `json:"checkId"`
	QueryID             string               `json:"queryId,omitempty"`
	Definition          Check                `json:"definition"`
	Dependencies        []Check              `json:"dependencies"`
	PublishedValidators []PublishedValidator `json:"publishedValidators"`
	QueryValidators     []QueryValidators    `json:"queryValidators"`
	CaseID              string               `json:"caseId,omitempty"`
	Environment         string               `json:"environment"`
	InputNames          []string             `json:"inputNames"` // values deliberately excluded from history
	ExecutionStatus     string               `json:"executionStatus"`
	Outcome             string               `json:"outcome"`
	Severity            string               `json:"severity,omitempty"`
	Steps               []StepResult         `json:"steps"`
	StartedAt           time.Time            `json:"startedAt"`
	DurationMs          int64                `json:"durationMs"`
	CheckInvocations    int                  `json:"checkInvocations"`
	QueryExecutions     int                  `json:"queryExecutions"`
	Actor               string               `json:"actor"`
}

type Capabilities struct {
	LocalOnly bool `json:"localOnly"`
	CanRead   bool `json:"canRead"`
	CanWrite  bool `json:"canWrite"`
	CanRun    bool `json:"canRun"`
	MaxRows   int  `json:"maxRows"`
	MaxFanout int  `json:"maxFanout"`
}

type State struct {
	Checks          []Check              `json:"checks"`
	Validators      []PublishedValidator `json:"validators"`
	QueryValidators []QueryValidators    `json:"queryValidators"`
	Cases           []Case               `json:"cases"`
	Runs            []CheckRun           `json:"runs"`
	RunsHasMore     bool                 `json:"runsHasMore"`
	RunsNextCursor  string               `json:"runsNextCursor,omitempty"`
	Scenarios       []Scenario           `json:"scenarios"`
	Capabilities    Capabilities         `json:"capabilities"`
}

// ActionRequest is POST /datatug/detective/action. Project and securityContextId
// are included in the body and must match the URL/session respectively.
type ActionRequest struct {
	Type              string                `json:"type"` // save-check | save-validator | save-query-validators | create-case | update-case | append-event | run-check | run-query-validators | save-scenario
	Project           string                `json:"project"`
	SecurityContextID string                `json:"securityContextId"`
	Check             *Check                `json:"check,omitempty"`
	Validator         *PublishedValidator   `json:"validator,omitempty"`
	QueryValidators   *QueryValidators      `json:"queryValidators,omitempty"`
	Case              *Case                 `json:"case,omitempty"`
	Scenario          *Scenario             `json:"scenario,omitempty"`
	Event             *CaseEvent            `json:"event,omitempty"`
	CheckID           string                `json:"checkId,omitempty"`
	QueryID           string                `json:"queryId,omitempty"`
	CaseID            string                `json:"caseId,omitempty"`
	Environment       string                `json:"environment,omitempty"`
	Inputs            map[string]InputValue `json:"inputs,omitempty"`
}

type ActionResponse struct {
	Check           *Check              `json:"check,omitempty"`
	Validator       *PublishedValidator `json:"validator,omitempty"`
	QueryValidators *QueryValidators    `json:"queryValidators,omitempty"`
	Case            *Case               `json:"case,omitempty"`
	Run             *CheckRun           `json:"run,omitempty"`
	Scenario        *Scenario           `json:"scenario,omitempty"`
}
