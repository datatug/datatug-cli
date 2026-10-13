package detective

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/investigation"
	"github.com/google/uuid"
)

const maxDepth = 8
const maxNodes = 128
const maxFanout = 100

type QueryRunner func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error)

// ExecutionMetadata resolves a server-issued immutable execution reference.
// A local engine embedding may omit it, but the HTTP runner records receipts.
type ExecutionMetadata func(context.Context, *apicontract.ExecutionRef) (revision, fingerprint string, err error)

// QueryFailure carries only an allowlisted execution error code across the
// runner boundary. Driver errors may contain URLs or data values and are never
// copied into persisted runs or responses.
type QueryFailure struct{ Code string }

func (e *QueryFailure) Error() string { return "saved query execution failed" }

func validateCheck(c Check) error {
	if !validSlug(c.ID) || strings.TrimSpace(c.Title) == "" || len(c.Steps) == 0 || len(c.Steps) > 32 {
		return ErrInvalid
	}
	inputNames := map[string]bool{}
	for _, in := range c.Inputs {
		if !validParameterName(in.Name) || inputNames[in.Name] || !validInputType(in.Type) || !oneOf(in.Cardinality, "scalar", "set", "range") {
			return ErrInvalid
		}
		inputNames[in.Name] = true
	}
	stepIDs := map[string]bool{}
	for _, s := range c.Steps {
		if !validSlug(s.ID) || stepIDs[s.ID] || !oneOf(s.Kind, "query", "check") {
			return ErrInvalid
		}
		stepIDs[s.ID] = true
		if s.Kind == "query" {
			if s.QueryID == "" || s.CheckID != "" {
				return ErrInvalid
			}
		} else if !validSlug(s.CheckID) || s.QueryID != "" || len(s.Validators) > 0 {
			return ErrInvalid
		}
		if len(s.Validators) > 32 || !validPolicy(s.OnUnavailable) {
			return ErrInvalid
		}
		if err := validateBindings(s.Bindings); err != nil {
			return err
		}
		for _, v := range s.Validators {
			if err := validateValidator(v); err != nil {
				return err
			}
		}
	}
	return nil
}

func validInputType(t investigation.ValueType) bool {
	return oneOf(string(t), "string", "number", "integer", "decimal", "boolean", "date", "datetime")
}
func oneOf(v string, opts ...string) bool {
	for _, o := range opts {
		if v == o {
			return true
		}
	}
	return false
}
func validPolicy(p string) bool { return p == "" || oneOf(p, "block", "fail", "warn", "skip") }

func validateBindings(bindings []Binding) error {
	seen := map[string]bool{}
	for _, b := range bindings {
		if !validParameterName(b.Input) || seen[b.Input] || (b.FromInput == "") == (b.FromColumn == "") || b.Part != "" && (!oneOf(b.Part, "start", "end") || b.FromInput == "") || b.FromInput != "" && !validParameterName(b.FromInput) || b.FromColumn != "" && !validColumnName(b.FromColumn) {
			return ErrInvalid
		}
		seen[b.Input] = true
	}
	return nil
}

// Query parameter names are not resource slugs. Existing saved queries use
// snake_case and PascalCase; bindings must preserve their exact names.
func validParameterName(name string) bool {
	if len(name) == 0 || len(name) > 128 {
		return false
	}
	for i, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && (r >= '0' && r <= '9' || r == '_' || r == '-') {
			continue
		}
		return false
	}
	return true
}

func validColumnName(name string) bool {
	if len(name) == 0 || len(name) > 128 {
		return false
	}
	for _, r := range name {
		if r < ' ' || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}

func validateValidator(v Validator) error {
	if !validSlug(v.ID) {
		return ErrInvalid
	}
	if v.ValidatorID != "" {
		if !validSlug(v.ValidatorID) || v.Kind != "" {
			return ErrInvalid
		}
		return nil
	}
	if !oneOf(v.Scope, "row", "set") || !oneOf(v.Severity, "critical", "error", "warning", "info") || !validPolicy(v.OnUnavailable) {
		return ErrInvalid
	}
	if !oneOf(v.Kind, "non-null", "equals-fields", "row-count", "unique", "sum-equals", "check") {
		return ErrInvalid
	}
	if v.Kind == "check" {
		if !validSlug(v.CheckID) || v.Scope != "row" || validateBindings(v.Bindings) != nil {
			return ErrInvalid
		}
	}
	if v.Kind == "non-null" || v.Kind == "unique" {
		if v.Field == "" {
			return ErrInvalid
		}
	}
	if v.Kind == "equals-fields" || v.Kind == "sum-equals" {
		if v.Field == "" || v.OtherField == "" {
			return ErrInvalid
		}
	}
	if v.Kind == "row-count" && v.Expected == nil {
		return ErrInvalid
	}
	return nil
}

func validateGraph(graph map[string]Check, catalog map[string]PublishedValidator, locals map[string]QueryValidators) error {
	visiting := map[string]bool{}
	visited := map[string]bool{}
	var visit func(string, int) error
	visit = func(id string, depth int) error {
		if depth > maxDepth || visiting[id] {
			return fmt.Errorf("%w: check dependency cycle or depth limit", ErrInvalid)
		}
		if visited[id] {
			return nil
		}
		c, ok := graph[id]
		if !ok {
			return nil
		}
		visiting[id] = true
		for _, s := range c.Steps {
			if s.Kind == "check" {
				if err := visit(s.CheckID, depth+1); err != nil {
					return err
				}
			}
			validators := append([]Validator(nil), s.Validators...)
			if local, ok := locals[s.QueryID]; ok {
				validators = append(validators, local.Validators...)
			}
			for _, v := range validators {
				if v.ValidatorID != "" {
					if promoted, ok := catalog[v.ValidatorID]; ok {
						v = promoted.Validator
					}
				}
				if v.Kind == "check" {
					if err := visit(v.CheckID, depth+1); err != nil {
						return err
					}
				}
			}
		}
		delete(visiting, id)
		visited[id] = true
		return nil
	}
	for id := range graph {
		if err := visit(id, 0); err != nil {
			return err
		}
	}
	return nil
}

type engine struct {
	checks          map[string]Check
	validators      map[string]PublishedValidator
	queryValidators map[string]QueryValidators
	runner          QueryRunner
	environment     string
	nodes           int
	queries         int
	cache           map[string]StepResult
	metadata        ExecutionMetadata
}

func (s *LocalStore) Run(ctx context.Context, checkID, environment, caseID, actor string, inputs map[string]InputValue, runner QueryRunner, metadata ...ExecutionMetadata) (CheckRun, error) {
	if !validSlug(checkID) || environment == "" || runner == nil {
		return CheckRun{}, ErrInvalid
	}
	state, err := s.State(ctx)
	if err != nil {
		return CheckRun{}, err
	}
	e := engine{checks: map[string]Check{}, validators: map[string]PublishedValidator{}, queryValidators: map[string]QueryValidators{}, runner: runner, environment: environment, cache: map[string]StepResult{}}
	if len(metadata) > 0 {
		e.metadata = metadata[0]
	}
	for _, c := range state.Checks {
		e.checks[c.ID] = c
	}
	for _, v := range state.Validators {
		e.validators[v.ID] = v
	}
	for _, v := range state.QueryValidators {
		e.queryValidators[v.QueryID] = v
	}
	root, ok := e.checks[checkID]
	if !ok {
		return CheckRun{}, ErrNotFound
	}
	if err := validateInputs(root.Inputs, inputs); err != nil {
		return CheckRun{}, err
	}
	if caseID != "" {
		found := false
		for _, c := range state.Cases {
			if c.ID == caseID {
				found = true
				if c.Status != "active" {
					return CheckRun{}, ErrInvalid
				}
				break
			}
		}
		if !found {
			return CheckRun{}, ErrNotFound
		}
	}
	start := time.Now().UTC()
	run := CheckRun{ID: "run-" + strings.ReplaceAll(uuid.NewString(), "-", ""), Kind: "check", CheckID: checkID, Definition: root, CaseID: caseID, Environment: environment, InputNames: sortedKeys(inputs), StartedAt: start, Actor: actor}
	seenChecks := map[string]bool{checkID: true}
	seenValidators := map[string]bool{}
	var snapshot func(Check)
	snapshot = func(c Check) {
		for _, step := range c.Steps {
			if step.Kind == "check" && !seenChecks[step.CheckID] {
				seenChecks[step.CheckID] = true
				if child, ok := e.checks[step.CheckID]; ok {
					run.Dependencies = append(run.Dependencies, child)
					snapshot(child)
				}
			}
			validators := append([]Validator(nil), step.Validators...)
			if local, ok := e.queryValidators[step.QueryID]; ok {
				run.QueryValidators = append(run.QueryValidators, local)
				validators = append(validators, local.Validators...)
			}
			for _, v := range validators {
				if v.ValidatorID != "" && !seenValidators[v.ValidatorID] {
					seenValidators[v.ValidatorID] = true
					if item, ok := e.validators[v.ValidatorID]; ok {
						run.PublishedValidators = append(run.PublishedValidators, item)
						v = item.Validator
					}
				}
				if v.Kind == "check" && !seenChecks[v.CheckID] {
					seenChecks[v.CheckID] = true
					if child, ok := e.checks[v.CheckID]; ok {
						run.Dependencies = append(run.Dependencies, child)
						snapshot(child)
					}
				}
			}
		}
	}
	snapshot(root)
	rootResult := e.runCheck(ctx, checkID, inputs, 0, map[string]bool{})
	run.Steps = rootResult.Children
	run.ExecutionStatus = rootResult.ExecutionStatus
	run.Outcome = rootResult.Outcome
	run.Severity = rootResult.Severity
	run.DurationMs = time.Since(start).Milliseconds()
	run.CheckInvocations = e.nodes
	run.QueryExecutions = e.queries
	if err := s.SaveRun(ctx, run); err != nil {
		return CheckRun{}, err
	}
	if caseID != "" {
		_, err = s.AppendCaseEvent(ctx, caseID, CaseEvent{ID: "event-" + strings.ReplaceAll(uuid.NewString(), "-", ""), Type: "check-run", Ref: run.ID}, actor)
		if err != nil {
			return run, fmt.Errorf("%w: case link was not saved", ErrOutcomeUncertain)
		}
	}
	return run, nil
}

// RunQueryValidators runs a saved query's local Validators without creating a
// global Check. The caller supplies the same secured query runner as Run.
func (s *LocalStore) RunQueryValidators(ctx context.Context, queryID, environment, caseID, actor string, inputs map[string]InputValue, runner QueryRunner, metadata ...ExecutionMetadata) (CheckRun, error) {
	if !validQueryRef(queryID) || environment == "" || runner == nil {
		return CheckRun{}, ErrInvalid
	}
	state, err := s.State(ctx)
	if err != nil {
		return CheckRun{}, err
	}
	var local QueryValidators
	found := false
	for _, item := range state.QueryValidators {
		if item.QueryID == queryID {
			local = item
			found = true
			break
		}
	}
	if !found {
		return CheckRun{}, ErrNotFound
	}
	if caseID != "" {
		caseFound := false
		for _, c := range state.Cases {
			if c.ID == caseID {
				caseFound = true
				if c.Status != "active" {
					return CheckRun{}, ErrInvalid
				}
				break
			}
		}
		if !caseFound {
			return CheckRun{}, ErrNotFound
		}
	}
	bindings := make([]Binding, 0, len(inputs))
	for name, value := range inputs {
		if !validParameterName(name) {
			return CheckRun{}, ErrInvalid
		}
		if value.Range != nil {
			return CheckRun{}, ErrInvalid
		}
		if err := value.Validate(); err != nil {
			return CheckRun{}, ErrInvalid
		}
		bindings = append(bindings, Binding{Input: name, FromInput: name})
	}
	start := time.Now().UTC()
	e := engine{checks: map[string]Check{}, validators: map[string]PublishedValidator{}, queryValidators: map[string]QueryValidators{queryID: local}, runner: runner, environment: environment, cache: map[string]StepResult{}}
	if len(metadata) > 0 {
		e.metadata = metadata[0]
	}
	for _, v := range state.Validators {
		e.validators[v.ID] = v
	}
	for _, c := range state.Checks {
		e.checks[c.ID] = c
	}
	step := e.runQueryStep(ctx, Step{ID: "query", Kind: "query", QueryID: queryID, Bindings: bindings}, inputs, 0, map[string]bool{})
	run := CheckRun{ID: "run-" + strings.ReplaceAll(uuid.NewString(), "-", ""), Kind: "query-validators", QueryID: queryID, CaseID: caseID, Environment: environment, InputNames: sortedKeys(inputs), QueryValidators: []QueryValidators{local}, StartedAt: start, Actor: actor, Steps: []StepResult{step}, ExecutionStatus: step.ExecutionStatus, Outcome: step.Outcome, Severity: step.Severity, DurationMs: time.Since(start).Milliseconds()}
	run.CheckInvocations = e.nodes
	run.QueryExecutions = e.queries
	for _, v := range local.Validators {
		if v.ValidatorID != "" {
			if p, ok := e.validators[v.ValidatorID]; ok {
				run.PublishedValidators = append(run.PublishedValidators, p)
				v = p.Validator
			}
		}
		if v.Kind == "check" {
			if c, ok := e.checks[v.CheckID]; ok {
				run.Dependencies = append(run.Dependencies, c)
			}
		}
	}
	if err := s.SaveRun(ctx, run); err != nil {
		return CheckRun{}, err
	}
	if caseID != "" {
		_, err = s.AppendCaseEvent(ctx, caseID, CaseEvent{ID: "event-" + strings.ReplaceAll(uuid.NewString(), "-", ""), Type: "check-run", Ref: run.ID}, actor)
		if err != nil {
			return run, fmt.Errorf("%w: case link was not saved", ErrOutcomeUncertain)
		}
	}
	return run, nil
}

func sortedKeys(m map[string]InputValue) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func validateInputs(schema []Input, values map[string]InputValue) error {
	byName := map[string]Input{}
	for _, in := range schema {
		byName[in.Name] = in
		if in.Required {
			if _, ok := values[in.Name]; !ok {
				return fmt.Errorf("%w: missing required input %s", ErrInvalid, in.Name)
			}
		}
	}
	for name, value := range values {
		in, ok := byName[name]
		if !ok {
			return fmt.Errorf("%w: unknown input %s", ErrInvalid, name)
		}
		if err := value.Validate(); err != nil {
			return ErrInvalid
		}
		switch in.Cardinality {
		case "scalar":
			if value.Scalar == nil || value.Scalar.Type != in.Type && value.Scalar.Type != investigation.ValueTypeNull {
				return ErrInvalid
			}
		case "set":
			if value.Set == nil || value.Set.Values[0].Type != in.Type {
				return ErrInvalid
			}
		case "range":
			if value.Range == nil || value.Range.Start.Type != in.Type {
				return ErrInvalid
			}
		}
	}
	return nil
}

func (e *engine) runCheck(ctx context.Context, id string, inputs map[string]InputValue, depth int, visiting map[string]bool) StepResult {
	res := StepResult{StepID: id, Kind: "check", CheckID: id, ExecutionStatus: "completed", Outcome: "unknown"}
	if depth > maxDepth || e.nodes >= maxNodes || visiting[id] {
		res.ExecutionStatus = "error"
		res.Message = "check graph bound or cycle reached"
		return res
	}
	c, ok := e.checks[id]
	if !ok {
		res.ExecutionStatus = "error"
		res.Message = "referenced check is unavailable"
		return res
	}
	if err := validateInputs(c.Inputs, inputs); err != nil {
		res.ExecutionStatus = "error"
		res.Message = "check inputs are invalid"
		return res
	}
	key := cacheKey(id, e.environment, inputs)
	if cached, ok := e.cache[key]; ok {
		return cached
	}
	e.nodes++
	visiting[id] = true
	defer delete(visiting, id)
	for _, step := range c.Steps {
		var child StepResult
		if step.Kind == "check" {
			mapped, err := mapBindings(step.Bindings, inputs, nil, nil)
			if err != nil {
				child = StepResult{StepID: step.ID, Kind: "check", CheckID: step.CheckID, ExecutionStatus: "error", Outcome: "unknown", Message: "check input binding is unavailable"}
			} else {
				child = e.runCheck(ctx, step.CheckID, mapped, depth+1, visiting)
				child.StepID = step.ID
			}
		} else {
			child = e.runQueryStep(ctx, step, inputs, depth, visiting)
		}
		if child.ExecutionStatus == "error" {
			applyUnavailable(&child, step.OnUnavailable)
		}
		res.Children = append(res.Children, child)
		if child.ExecutionStatus == "error" && step.OnUnavailable == "block" {
			break
		}
	}
	res.Outcome, res.ExecutionStatus, res.Severity = aggregate(res.Children)
	e.cache[key] = res
	return res
}

func (e *engine) runQueryStep(ctx context.Context, step Step, inputs map[string]InputValue, depth int, visiting map[string]bool) StepResult {
	res := StepResult{StepID: step.ID, Kind: "query", QueryID: step.QueryID, ExecutionStatus: "completed", Outcome: "unknown"}
	mapped, err := mapBindings(step.Bindings, inputs, nil, nil)
	if err != nil {
		res.ExecutionStatus = "error"
		res.Message = "query input binding is unavailable"
		return res
	}
	params := map[string]apicontract.TypedValueOrSet{}
	for name, value := range mapped {
		if value.Range != nil {
			res.ExecutionStatus = "error"
			res.Message = "range must bind its start or end explicitly"
			return res
		}
		params[name] = apicontract.TypedValueOrSet{Scalar: value.Scalar, Set: value.Set}
	}
	started := time.Now()
	e.queries++
	result, err := e.runner(ctx, step.QueryID, e.environment, params)
	res.DurationMs = time.Since(started).Milliseconds()
	if err != nil {
		res.ExecutionStatus = "error"
		res.Message = "saved query could not execute"
		var failure *QueryFailure
		if errors.As(err, &failure) && validExecutionCode(failure.Code) {
			res.ErrorCode = failure.Code
		} else {
			res.ErrorCode = "INTERNAL"
		}
		return res
	}
	res.Source = result.Provenance.Source
	res.ObservedAt = result.Provenance.ObservedAt
	res.ExecutionProfile = result.Provenance.ExecutionProfile
	res.Execution = result.Execution
	evidenceMissing := e.metadata != nil && result.Execution == nil
	if result.Execution != nil && e.metadata != nil {
		revision, fingerprint, metaErr := e.metadata(ctx, result.Execution)
		if metaErr == nil {
			res.QueryRevision = revision
			res.ResultFingerprint = fingerprint
		} else {
			evidenceMissing = true
		}
	}
	res.RowCount = len(result.Recordset.Rows)
	validators := append([]Validator(nil), step.Validators...)
	if local, ok := e.queryValidators[step.QueryID]; ok {
		validators = append(validators, local.Validators...)
	}
	for _, validator := range validators {
		v := validator
		if v.ValidatorID != "" {
			published, ok := e.validators[v.ValidatorID]
			if !ok {
				res.Validators = append(res.Validators, ValidationResult{ValidatorID: v.ValidatorID, ExecutionStatus: "error", Outcome: "unknown", Message: "published validator is unavailable"})
				continue
			}
			v = published.Validator
		}
		res.Validators = append(res.Validators, e.validate(ctx, v, result.Recordset, result.Truncated, inputs, depth, visiting)...)
	}
	res.Outcome, res.ExecutionStatus, res.Severity = aggregateValidators(res.Validators)
	if evidenceMissing {
		res.Outcome = "unknown"
		res.ExecutionStatus = "error"
		res.Message = "execution receipt metadata is unavailable"
	}
	if result.Truncated && res.Outcome == "pass" {
		res.Outcome = "unknown"
		res.Message = "query result was truncated"
	}
	return res
}

func validExecutionCode(code string) bool {
	if len(code) == 0 || len(code) > 64 {
		return false
	}
	for _, r := range code {
		if !((r >= 'A' && r <= 'Z') || r == '_') {
			return false
		}
	}
	return true
}

func (e *engine) validate(ctx context.Context, v Validator, rs apicontract.Recordset, truncated bool, inputs map[string]InputValue, depth int, visiting map[string]bool) []ValidationResult {
	base := ValidationResult{ValidatorID: v.ID, ExecutionStatus: "completed", Outcome: "pass", Severity: v.Severity}
	if truncated && v.Scope == "set" {
		base.Outcome = "unknown"
		base.Message = "set result is truncated"
		return []ValidationResult{base}
	}
	if v.Kind == "row-count" {
		if v.Expected == nil || v.Expected.Type != investigation.ValueTypeInteger {
			base.Outcome = "unknown"
		} else {
			var wanted int
			_, err := fmt.Sscan(v.Expected.Str, &wanted)
			if err != nil {
				base.Outcome = "unknown"
			} else if len(rs.Rows) != wanted {
				base.Outcome = "fail"
			}
		}
		return []ValidationResult{base}
	}
	indices := map[string]int{}
	for i, c := range rs.Columns {
		indices[c.Name] = i
	}
	fieldIndex, ok := indices[v.Field]
	if v.Kind != "check" && !ok {
		base.ExecutionStatus = "error"
		base.Outcome = "unknown"
		base.Message = "validator field is unavailable"
		return []ValidationResult{base}
	}
	otherIndex := indices[v.OtherField]
	if (v.Kind == "equals-fields" || v.Kind == "sum-equals") && v.OtherField != "" {
		if _, ok := indices[v.OtherField]; !ok {
			base.ExecutionStatus = "error"
			base.Outcome = "unknown"
			base.Message = "validator field is unavailable"
			return []ValidationResult{base}
		}
	}
	if v.Kind == "unique" {
		seen := map[string]bool{}
		for _, row := range rs.Rows {
			key := valueKey(row[fieldIndex])
			if seen[key] {
				base.Outcome = "fail"
				break
			}
			seen[key] = true
		}
		return []ValidationResult{base}
	}
	if v.Kind == "sum-equals" {
		left, right := new(big.Rat), new(big.Rat)
		for _, row := range rs.Rows {
			a, okA := numeric(row[fieldIndex])
			b, okB := numeric(row[otherIndex])
			if !okA || !okB {
				base.Outcome = "unknown"
				base.Message = "aggregate values are not numeric"
				return []ValidationResult{base}
			}
			left.Add(left, a)
			right.Add(right, b)
		}
		if left.Cmp(right) != 0 {
			base.Outcome = "fail"
		}
		return []ValidationResult{base}
	}
	results := make([]ValidationResult, 0, len(rs.Rows))
	if len(rs.Rows) > maxFanout && v.Kind == "check" {
		base.ExecutionStatus = "skipped"
		base.Outcome = "unknown"
		base.Message = "per-row check fanout exceeds limit"
		return []ValidationResult{base}
	}
	for i, row := range rs.Rows {
		item := base
		item.RowIndex = &i
		switch v.Kind {
		case "non-null":
			if row[fieldIndex].Type == investigation.ValueTypeNull {
				item.Outcome = "fail"
			}
		case "equals-fields":
			if valueKey(row[fieldIndex]) != valueKey(row[otherIndex]) {
				item.Outcome = "fail"
			}
		case "check":
			mapped, err := mapBindings(v.Bindings, inputs, rs.Columns, row)
			if err != nil {
				item.ExecutionStatus = "error"
				item.Outcome = "unknown"
				item.Message = "row binding is unavailable"
			} else {
				child := e.runCheck(ctx, v.CheckID, mapped, depth+1, visiting)
				item.Children = []StepResult{child}
				item.Outcome = child.Outcome
				item.ExecutionStatus = child.ExecutionStatus
				item.Severity = higherSeverity(item.Severity, child.Severity)
				if child.ExecutionStatus == "error" {
					applyUnavailableValidation(&item, v.OnUnavailable)
				}
			}
		}
		results = append(results, item)
	}
	if len(results) == 0 {
		base.Outcome = "unknown"
		base.Message = "no rows were evaluated"
		results = append(results, base)
	}
	return results
}

func mapBindings(bindings []Binding, inputs map[string]InputValue, cols []apicontract.Column, row []Value) (map[string]InputValue, error) {
	out := map[string]InputValue{}
	for _, b := range bindings {
		if b.FromInput != "" {
			v, ok := inputs[b.FromInput]
			if !ok {
				return nil, ErrInvalid
			}
			if b.Part != "" {
				if v.Range == nil {
					return nil, ErrInvalid
				}
				scalar := v.Range.Start
				if b.Part == "end" {
					scalar = v.Range.End
				}
				v = InputValue{Scalar: &scalar}
			}
			out[b.Input] = v
			continue
		}
		found := false
		for i, c := range cols {
			if c.Name == b.FromColumn && i < len(row) {
				v := row[i]
				out[b.Input] = InputValue{Scalar: &v}
				found = true
				break
			}
		}
		if !found {
			return nil, ErrInvalid
		}
	}
	return out, nil
}

func valueKey(v Value) string { data, _ := json.Marshal(v); return string(data) }

func numeric(v Value) (*big.Rat, bool) {
	var s string
	switch v.Type {
	case investigation.ValueTypeInteger, investigation.ValueTypeDecimal:
		s = v.Str
	case investigation.ValueTypeNumber:
		s = fmt.Sprintf("%.17g", v.Num)
	default:
		return nil, false
	}
	r, ok := new(big.Rat).SetString(s)
	return r, ok
}

func aggregate(children []StepResult) (string, string, string) {
	if len(children) == 0 {
		return "unknown", "completed", ""
	}
	outcome, status, severity := "pass", "completed", ""
	for _, c := range children {
		if c.ExecutionStatus == "error" {
			status = "error"
		}
		if c.Outcome == "fail" {
			outcome = "fail"
			severity = higherSeverity(severity, c.Severity)
		} else if c.Outcome == "unknown" && outcome != "fail" {
			outcome = "unknown"
			severity = higherSeverity(severity, c.Severity)
		}
	}
	return outcome, status, severity
}
func aggregateValidators(children []ValidationResult) (string, string, string) {
	if len(children) == 0 {
		return "unknown", "completed", ""
	}
	outcome, status, severity := "pass", "completed", ""
	for _, c := range children {
		if c.ExecutionStatus == "error" {
			status = "error"
		}
		if c.Outcome == "fail" {
			outcome = "fail"
			severity = higherSeverity(severity, c.Severity)
		} else if c.Outcome == "unknown" && outcome != "fail" {
			outcome = "unknown"
			severity = higherSeverity(severity, c.Severity)
		}
	}
	return outcome, status, severity
}
func higherSeverity(a, b string) string {
	rank := map[string]int{"": 0, "info": 1, "warning": 2, "error": 3, "critical": 4}
	if rank[b] > rank[a] {
		return b
	}
	return a
}
func applyUnavailable(r *StepResult, policy string) {
	if policy == "fail" {
		r.Outcome = "fail"
	} else if policy == "skip" {
		r.ExecutionStatus = "skipped"
		r.Outcome = "unknown"
	} else if policy == "warn" {
		r.Severity = "warning"
		r.Outcome = "unknown"
	}
}
func applyUnavailableValidation(r *ValidationResult, policy string) {
	if policy == "fail" {
		r.Outcome = "fail"
	} else if policy == "skip" {
		r.ExecutionStatus = "skipped"
		r.Outcome = "unknown"
	} else if policy == "warn" {
		r.Severity = "warning"
		r.Outcome = "unknown"
	}
}

func cacheKey(id, environment string, inputs map[string]InputValue) string {
	data, _ := json.Marshal(inputs)
	h := sha256.Sum256(append([]byte(id+"\x00"+environment+"\x00"), data...))
	return hex.EncodeToString(h[:])
}
