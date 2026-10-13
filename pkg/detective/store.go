package detective

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

var ErrConflict = errors.New("definition or case changed; reload before saving")
var ErrNotFound = errors.New("detective item not found")
var ErrInvalid = errors.New("invalid detective item")
var ErrOutcomeUncertain = errors.New("mutation may have been saved but audit failed; reload before retrying")

const maxItems = 200

// AuditEvent deliberately contains metadata only. No input values or result rows
// may be added to this generic project event stream.
type AuditEvent struct {
	ID                string    `json:"id"`
	At                time.Time `json:"at"`
	Actor             string    `json:"actor"`
	Project           string    `json:"project"`
	Action            string    `json:"action"`
	CaseID            string    `json:"caseId,omitempty"`
	CheckID           string    `json:"checkId,omitempty"`
	QueryID           string    `json:"queryId,omitempty"`
	QueryRevision     string    `json:"queryRevision,omitempty"`
	ResultFingerprint string    `json:"resultFingerprint,omitempty"`
	ExecutionID       string    `json:"executionId,omitempty"`
	Source            string    `json:"source,omitempty"`
	ErrorCode         string    `json:"errorCode,omitempty"`
	RunID             string    `json:"runId,omitempty"`
	Environment       string    `json:"environment,omitempty"`
	Outcome           string    `json:"outcome,omitempty"`
	RowCount          int       `json:"rowCount,omitempty"`
	DurationMs        int64     `json:"durationMs,omitempty"`
}

type AuditStore interface {
	Append(context.Context, AuditEvent) error
}

type localJSONLAudit struct{ path string }

// LocalStore keeps reviewable definitions in the project and sensitive case,
// run and audit material in a separate server-private directory.
type LocalStore struct {
	projectDir string
	privateDir string
	projectID  string
	lock       *flock.Flock
	audit      AuditStore
}

func NewLocalStore(projectID, projectDir, privateBase string) (*LocalStore, error) {
	if !validServedProjectID(projectID) || projectDir == "" || privateBase == "" {
		return nil, ErrInvalid
	}
	project, err := filepath.Abs(projectDir)
	if err != nil {
		return nil, err
	}
	project, err = filepath.EvalSymlinks(project)
	if err != nil {
		return nil, err
	}
	base, err := filepath.Abs(privateBase)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(base, 0700); err != nil {
		return nil, err
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		return nil, err
	}
	if within(project, base) || within(base, project) {
		return nil, fmt.Errorf("private Detective store must be outside project")
	}
	sum := sha256.Sum256([]byte(project))
	container := filepath.Join(base, "detective")
	if err := ensureDir(container); err != nil {
		return nil, err
	}
	private := filepath.Join(container, hex.EncodeToString(sum[:16]))
	if err := ensureDir(private); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(private, "store.lock")
	if err := ensureAbsentOrRegular(lockPath); err != nil {
		return nil, err
	}
	auditPath := filepath.Join(private, "audit.jsonl")
	if err := ensureAbsentOrRegular(auditPath); err != nil {
		return nil, err
	}
	return &LocalStore{projectDir: project, privateDir: private, projectID: projectID, lock: flock.New(lockPath), audit: localJSONLAudit{path: auditPath}}, nil
}

// NewLocalStoreWithAudit replaces the JSONL adapter for a trusted server
// configuration. The same mutation paths use the supplied adapter.
func NewLocalStoreWithAudit(projectID, projectDir, privateBase string, audit AuditStore) (*LocalStore, error) {
	s, err := NewLocalStore(projectID, projectDir, privateBase)
	if err != nil {
		return nil, err
	}
	if audit != nil {
		s.audit = audit
	}
	return s, nil
}

func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}

func ensureNoSymlink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalid
	}
	return nil
}

func ensureDir(path string) error {
	err := os.Mkdir(path, 0700)
	if err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalid
	}
	return nil
}

func ensureAbsentOrRegular(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return ErrInvalid
	}
	return nil
}

func safeReadFile(dir, id string) ([]byte, error) {
	if !validSlug(id) {
		return nil, ErrInvalid
	}
	if err := ensureDirIfExists(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, id+".json")
	if err := ensureAbsentOrRegular(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, ErrInvalid
	}
	return data, nil
}

func ensureDirIfExists(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalid
	}
	return nil
}

func (s *LocalStore) withLock(ctx context.Context, fn func() error) error {
	ok, err := s.lock.TryLockContext(ctx, 50*time.Millisecond)
	if err != nil {
		return err
	}
	if !ok {
		return context.DeadlineExceeded
	}
	defer func() { _ = s.lock.Unlock() }()
	return fn()
}

func (s *LocalStore) definitionDir(kind string) string  { return filepath.Join(s.projectDir, kind) }
func (s *LocalStore) privateItemDir(kind string) string { return filepath.Join(s.privateDir, kind) }

func readItems[T any](dir string) ([]T, error) {
	if err := ensureDirIfExists(dir); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []T{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(entries) > maxItems {
		return nil, fmt.Errorf("detective catalog exceeds %d items", maxItems)
	}
	items := make([]T, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if !validSlug(strings.TrimSuffix(entry.Name(), ".json")) {
			return nil, ErrInvalid
		}
		path := filepath.Join(dir, entry.Name())
		if err := ensureNoSymlink(path); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if len(data) > 1<<20 {
			return nil, ErrInvalid
		}
		var item T
		if err := json.Unmarshal(data, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func writeItem(dir, id string, item any) error {
	if !validSlug(id) {
		return ErrInvalid
	}
	if err := ensureDir(dir); err != nil {
		return err
	}
	if err := ensureNoSymlink(dir); err != nil {
		return err
	}
	path := filepath.Join(dir, id+".json")
	if _, err := os.Lstat(path); err == nil {
		if err := ensureNoSymlink(path); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return ErrInvalid
	}
	tmp, err := os.CreateTemp(dir, ".detective-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (s *LocalStore) State(ctx context.Context) (State, error) { return s.StatePage(ctx, "") }

// StatePage returns a bounded latest run page. The opaque cursor orders file
// write time plus ID, so history does not make the catalog unusable.
func (s *LocalStore) StatePage(ctx context.Context, cursor string) (State, error) {
	var state State
	err := s.withLock(ctx, func() error {
		var err error
		if state.Checks, err = readItems[Check](s.definitionDir("checks")); err != nil {
			return err
		}
		if state.Validators, err = readItems[PublishedValidator](s.definitionDir("validators")); err != nil {
			return err
		}
		if state.QueryValidators, err = readItems[QueryValidators](s.definitionDir("query-validators")); err != nil {
			return err
		}
		if state.Scenarios, err = readItems[Scenario](s.definitionDir("scenarios")); err != nil {
			return err
		}
		if state.Cases, err = readItems[Case](s.privateItemDir("cases")); err != nil {
			return err
		}
		if state.Runs, state.RunsHasMore, state.RunsNextCursor, err = readRunPage(s.privateItemDir("runs"), cursor); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return State{}, err
	}
	sort.Slice(state.Checks, func(i, j int) bool { return state.Checks[i].ID < state.Checks[j].ID })
	sort.Slice(state.Validators, func(i, j int) bool { return state.Validators[i].ID < state.Validators[j].ID })
	sort.Slice(state.QueryValidators, func(i, j int) bool { return state.QueryValidators[i].QueryID < state.QueryValidators[j].QueryID })
	sort.Slice(state.Cases, func(i, j int) bool { return state.Cases[i].UpdatedAt.After(state.Cases[j].UpdatedAt) })
	sort.Slice(state.Runs, func(i, j int) bool { return state.Runs[i].StartedAt.After(state.Runs[j].StartedAt) })
	sort.Slice(state.Scenarios, func(i, j int) bool { return state.Scenarios[i].ID < state.Scenarios[j].ID })
	state.Capabilities = Capabilities{LocalOnly: true, CanRead: true, CanWrite: true, CanRun: true, MaxRows: 500, MaxFanout: maxFanout}
	encoded, err := json.Marshal(state)
	if err != nil {
		return State{}, err
	}
	if len(encoded) > 4<<20 {
		return State{}, fmt.Errorf("detective state response limit reached")
	}
	return state, nil
}

type runFile struct {
	name   string
	cursor string
}

func readRunPage(dir, cursor string) ([]CheckRun, bool, string, error) {
	if err := ensureDirIfExists(dir); err != nil {
		return nil, false, "", err
	}
	if cursor != "" {
		if len(cursor) > 180 || strings.ContainsAny(cursor, "/\\\n\r") || !strings.Contains(cursor, ":run-") {
			return nil, false, "", ErrInvalid
		}
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []CheckRun{}, false, "", nil
	}
	if err != nil {
		return nil, false, "", err
	}
	files := make([]runFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !validSlug(id) {
			return nil, false, "", ErrInvalid
		}
		path := filepath.Join(dir, entry.Name())
		if err := ensureNoSymlink(path); err != nil {
			return nil, false, "", err
		}
		info, err := entry.Info()
		if err != nil {
			return nil, false, "", err
		}
		key := fmt.Sprintf("%020d:%s", info.ModTime().UnixNano(), id)
		if cursor == "" || key < cursor {
			files = append(files, runFile{name: entry.Name(), cursor: key})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].cursor > files[j].cursor })
	hasMore := len(files) > 40
	if hasMore {
		files = files[:40]
	}
	runs := make([]CheckRun, 0, len(files))
	next := ""
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(dir, file.name))
		if err != nil {
			return nil, false, "", err
		}
		if len(data) > 1<<20 {
			return nil, false, "", ErrInvalid
		}
		var run CheckRun
		if err := json.Unmarshal(data, &run); err != nil {
			return nil, false, "", err
		}
		runs = append(runs, run)
		next = file.cursor
	}
	return runs, hasMore, next, nil
}

func (s *LocalStore) getCheck(id string) (Check, error) {
	if !validSlug(id) {
		return Check{}, ErrInvalid
	}
	data, err := safeReadFile(s.definitionDir("checks"), id)
	if errors.Is(err, os.ErrNotExist) {
		return Check{}, ErrNotFound
	}
	if err != nil {
		return Check{}, err
	}
	var check Check
	if err = json.Unmarshal(data, &check); err != nil {
		return Check{}, err
	}
	return check, nil
}

func (s *LocalStore) GetCheck(ctx context.Context, id string) (Check, error) {
	var c Check
	err := s.withLock(ctx, func() error { var e error; c, e = s.getCheck(id); return e })
	return c, err
}

func (s *LocalStore) SaveCheck(ctx context.Context, check Check, actor string) (Check, error) {
	if err := validateCheck(check); err != nil {
		return Check{}, err
	}
	err := s.withLock(ctx, func() error {
		old, err := s.getCheck(check.ID)
		if err == nil {
			if check.Version != old.Version {
				return ErrConflict
			}
			check.Version++
		} else if errors.Is(err, ErrNotFound) {
			if check.Version != 0 {
				return ErrConflict
			}
			check.Version = 1
		} else {
			return err
		}
		all, err := readItems[Check](s.definitionDir("checks"))
		if err != nil {
			return err
		}
		graph := make(map[string]Check, len(all)+1)
		for _, c := range all {
			graph[c.ID] = c
		}
		graph[check.ID] = check
		published, err := readItems[PublishedValidator](s.definitionDir("validators"))
		if err != nil {
			return err
		}
		locals, err := readItems[QueryValidators](s.definitionDir("query-validators"))
		if err != nil {
			return err
		}
		catalog := map[string]PublishedValidator{}
		for _, v := range published {
			catalog[v.ID] = v
		}
		queryValidators := map[string]QueryValidators{}
		for _, v := range locals {
			queryValidators[v.QueryID] = v
		}
		if err := validateGraph(graph, catalog, queryValidators); err != nil {
			return err
		}
		check.UpdatedBy = actor
		check.UpdatedAt = time.Now().UTC()
		if err := writeItem(s.definitionDir("checks"), check.ID, check); err != nil {
			return err
		}
		return s.auditMutation(ctx, AuditEvent{ID: fmt.Sprintf("check-%s-%d", check.ID, check.Version), At: check.UpdatedAt, Actor: actor, Project: s.projectID, Action: "save-check", CheckID: check.ID})
	})
	return check, err
}

func (s *LocalStore) getCase(id string) (Case, error) {
	if !validSlug(id) {
		return Case{}, ErrInvalid
	}
	data, err := safeReadFile(s.privateItemDir("cases"), id)
	if errors.Is(err, os.ErrNotExist) {
		return Case{}, ErrNotFound
	}
	if err != nil {
		return Case{}, err
	}
	var c Case
	if err = json.Unmarshal(data, &c); err != nil {
		return Case{}, err
	}
	return c, nil
}

func (s *LocalStore) SaveCase(ctx context.Context, c Case, actor, action string) (Case, error) {
	if !validSlug(c.ID) || strings.TrimSpace(c.Title) == "" {
		return Case{}, ErrInvalid
	}
	err := s.withLock(ctx, func() error {
		old, err := s.getCase(c.ID)
		now := time.Now().UTC()
		if errors.Is(err, ErrNotFound) {
			if action != "create-case" || c.Version != 0 {
				return ErrConflict
			}
			c.Status = "active"
			c.Version = 1
			c.RunRefs = nil
			initialEvidence := c.Evidence[:0]
			for _, item := range c.Evidence {
				if item.Kind != "check-run" {
					initialEvidence = append(initialEvidence, item)
				}
			}
			c.Evidence = initialEvidence
			c.CreatedAt = now
			c.Timeline = []CaseEvent{{ID: "created", At: now, Actor: actor, Type: "created"}}
		} else if err == nil {
			if action == "create-case" || c.Version != old.Version {
				return ErrConflict
			}
			if !validTransition(old.Status, c.Status, c.ClosureReason) {
				return ErrInvalid
			}
			// The client edits context/findings/links, but cannot replace history
			// or server-created run links and execution evidence.
			c.Timeline = append([]CaseEvent(nil), old.Timeline...)
			c.RunRefs = append([]string(nil), old.RunRefs...)
			userEvidence := make([]EvidenceRef, 0, len(c.Evidence)+len(old.Evidence))
			for _, item := range c.Evidence {
				if item.Kind != "check-run" {
					userEvidence = append(userEvidence, item)
				}
			}
			for _, item := range old.Evidence {
				if item.Kind == "check-run" {
					userEvidence = append(userEvidence, item)
				}
			}
			if len(userEvidence) == 0 {
				userEvidence = nil
			}
			c.Evidence = userEvidence
			c.CreatedAt = old.CreatedAt
			c.Version++
			if old.Status == "closed" && c.Status == "active" && c.ClosureReason == "" {
				c.ClosureReason = old.ClosureReason
			}
			if old.Status != c.Status {
				eventType := c.Status
				if old.Status == "closed" && c.Status == "active" {
					eventType = "reopened"
				}
				c.Timeline = append(c.Timeline, CaseEvent{ID: fmt.Sprintf("lifecycle-%d", c.Version), At: now, Actor: actor, Type: eventType, Message: c.ClosureReason})
			}
			changes := []struct {
				changed bool
				kind    string
			}{{!reflect.DeepEqual(old.Context, c.Context), "context-updated"}, {!reflect.DeepEqual(old.Findings, c.Findings), "findings-updated"}, {!reflect.DeepEqual(old.Evidence, c.Evidence), "evidence-updated"}, {!reflect.DeepEqual(old.IncidentRefs, c.IncidentRefs), "incident-links-updated"}, {!reflect.DeepEqual(old.ScenarioRefs, c.ScenarioRefs), "scenario-links-updated"}, {old.Title != c.Title, "title-updated"}}
			for _, change := range changes {
				if change.changed {
					c.Timeline = append(c.Timeline, CaseEvent{ID: fmt.Sprintf("%s-%d", change.kind, c.Version), At: now, Actor: actor, Type: change.kind})
				}
			}
		} else {
			return err
		}
		if c.Context == nil {
			c.Context = map[string]InputValue{}
		}
		for name, value := range c.Context {
			if !validParameterName(name) {
				return ErrInvalid
			}
			if err := value.Validate(); err != nil {
				return ErrInvalid
			}
		}
		for _, e := range c.Evidence {
			if !validSlug(e.ID) || !oneOf(e.Kind, "check-run", "query", "external") || e.Ref == "" {
				return ErrInvalid
			}
		}
		for _, f := range c.Findings {
			if !validSlug(f.ID) || f.Text == "" || !oneOf(f.Status, "hypothesis", "confirmed") {
				return ErrInvalid
			}
		}
		for _, ref := range c.ScenarioRefs {
			if !validSlug(ref) {
				return ErrInvalid
			}
			if _, err := safeReadFile(s.definitionDir("scenarios"), ref); err != nil {
				return ErrNotFound
			}
		}
		for _, ref := range c.IncidentRefs {
			if ref == "" || len(ref) > 256 || strings.ContainsAny(ref, "\n\r\x00") {
				return ErrInvalid
			}
		}
		if len(c.Timeline) > 500 || len(c.Evidence) > 200 || len(c.Findings) > 200 {
			return ErrInvalid
		}
		c.UpdatedAt = now
		if err := writeItem(s.privateItemDir("cases"), c.ID, c); err != nil {
			return err
		}
		return s.auditMutation(ctx, AuditEvent{ID: fmt.Sprintf("case-%s-%d", c.ID, c.Version), At: now, Actor: actor, Project: s.projectID, Action: action, CaseID: c.ID})
	})
	return c, err
}

func (s *LocalStore) AppendCaseEvent(ctx context.Context, id string, event CaseEvent, actor string) (Case, error) {
	var c Case
	err := s.withLock(ctx, func() error {
		var err error
		c, err = s.getCase(id)
		if err != nil {
			return err
		}
		if c.Status == "archived" || len(c.Timeline) >= 500 {
			return ErrInvalid
		}
		if event.ID == "" || event.Type == "" || !validSlug(event.ID) {
			return ErrInvalid
		}
		if event.Type == "check-run" {
			if !validSlug(event.Ref) {
				return ErrInvalid
			}
			if _, err := os.Stat(filepath.Join(s.privateItemDir("runs"), event.Ref+".json")); err != nil {
				return ErrNotFound
			}
		}
		for _, e := range c.Timeline {
			if e.ID == event.ID {
				return ErrConflict
			}
		}
		event.At = time.Now().UTC()
		event.Actor = actor
		c.Timeline = append(c.Timeline, event)
		if event.Type == "check-run" && validSlug(event.Ref) {
			c.RunRefs = append(c.RunRefs, event.Ref)
			c.Evidence = append(c.Evidence, EvidenceRef{ID: "evidence-" + event.Ref, Kind: "check-run", Ref: event.Ref})
		}
		c.Version++
		c.UpdatedAt = event.At
		if err = writeItem(s.privateItemDir("cases"), id, c); err != nil {
			return err
		}
		return s.auditMutation(ctx, AuditEvent{ID: event.ID, At: event.At, Actor: actor, Project: s.projectID, Action: event.Type, CaseID: id})
	})
	return c, err
}

func (s *LocalStore) SaveRun(ctx context.Context, run CheckRun) error {
	return s.withLock(ctx, func() error {
		if !validSlug(run.ID) {
			return ErrInvalid
		}
		if _, err := os.Stat(filepath.Join(s.privateItemDir("runs"), run.ID+".json")); err == nil {
			return ErrConflict
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := writeItem(s.privateItemDir("runs"), run.ID, run); err != nil {
			return err
		}
		rows, source, queryID, errorCode, revision, fingerprint, executionID := summarizeSteps(run.Steps)
		return s.auditMutation(ctx, AuditEvent{ID: run.ID, At: run.StartedAt, Actor: run.Actor, Project: s.projectID, Action: "run-" + run.Kind, CheckID: run.CheckID, QueryID: queryID, QueryRevision: revision, ResultFingerprint: fingerprint, ExecutionID: executionID, Source: source, ErrorCode: errorCode, CaseID: run.CaseID, RunID: run.ID, Environment: run.Environment, Outcome: run.Outcome, RowCount: rows, DurationMs: run.DurationMs})
	})
}

func summarizeSteps(steps []StepResult) (rows int, source, queryID, errorCode, revision, fingerprint, executionID string) {
	for _, step := range steps {
		rows += step.RowCount
		if source == "" {
			source = step.Source
		}
		if queryID == "" {
			queryID = step.QueryID
		}
		if errorCode == "" {
			errorCode = step.ErrorCode
		}
		if revision == "" {
			revision = step.QueryRevision
		}
		if fingerprint == "" {
			fingerprint = step.ResultFingerprint
		}
		if executionID == "" && step.Execution != nil {
			executionID = step.Execution.ExecutionID
		}
		childRows, childSource, childQuery, childCode, childRevision, childFingerprint, childExecutionID := summarizeSteps(step.Children)
		rows += childRows
		if source == "" {
			source = childSource
		}
		if queryID == "" {
			queryID = childQuery
		}
		if errorCode == "" {
			errorCode = childCode
		}
		if revision == "" {
			revision = childRevision
		}
		if fingerprint == "" {
			fingerprint = childFingerprint
		}
		if executionID == "" {
			executionID = childExecutionID
		}
	}
	return
}

func (s *LocalStore) SaveScenario(ctx context.Context, scenario Scenario, actor string) (Scenario, error) {
	if !validSlug(scenario.ID) || scenario.Title == "" || len(scenario.Steps) == 0 {
		return Scenario{}, ErrInvalid
	}
	if len(scenario.Steps) > 32 || len(scenario.Inputs) > 64 {
		return Scenario{}, ErrInvalid
	}
	inputNames := map[string]bool{}
	for _, input := range scenario.Inputs {
		if !validParameterName(input.Name) || inputNames[input.Name] || !validInputType(input.Type) || !oneOf(input.Cardinality, "scalar", "set", "range") {
			return Scenario{}, ErrInvalid
		}
		inputNames[input.Name] = true
	}
	stepIDs := map[string]bool{}
	for _, step := range scenario.Steps {
		if !validSlug(step.ID) || stepIDs[step.ID] || !oneOf(step.Kind, "query", "check") || step.Ref == "" || step.Kind == "check" && !validSlug(step.Ref) || step.Kind == "query" && !validQueryRef(step.Ref) {
			return Scenario{}, ErrInvalid
		}
		stepIDs[step.ID] = true
	}
	err := s.withLock(ctx, func() error {
		data, err := safeReadFile(s.definitionDir("scenarios"), scenario.ID)
		if err == nil {
			var old Scenario
			if e := json.Unmarshal(data, &old); e != nil {
				return e
			}
			if scenario.Version != old.Version {
				return ErrConflict
			}
			scenario.Version++
		} else if errors.Is(err, os.ErrNotExist) {
			if scenario.Version != 0 {
				return ErrConflict
			}
			scenario.Version = 1
		} else {
			return err
		}
		if err := writeItem(s.definitionDir("scenarios"), scenario.ID, scenario); err != nil {
			return err
		}
		return s.auditMutation(ctx, AuditEvent{ID: fmt.Sprintf("scenario-%s-%d", scenario.ID, scenario.Version), At: time.Now().UTC(), Actor: actor, Project: s.projectID, Action: "save-scenario"})
	})
	return scenario, err
}

func (s *LocalStore) SaveValidator(ctx context.Context, item PublishedValidator, actor string) (PublishedValidator, error) {
	if !validSlug(item.ID) || item.Title == "" || item.Validator.ValidatorID != "" || item.Validator.ID == "" {
		return PublishedValidator{}, ErrInvalid
	}
	if err := validateValidator(item.Validator); err != nil {
		return PublishedValidator{}, err
	}
	err := s.withLock(ctx, func() error {
		data, err := safeReadFile(s.definitionDir("validators"), item.ID)
		if err == nil {
			var old PublishedValidator
			if e := json.Unmarshal(data, &old); e != nil {
				return e
			}
			if item.Version != old.Version {
				return ErrConflict
			}
			item.Version++
		} else if errors.Is(err, os.ErrNotExist) {
			if item.Version != 0 {
				return ErrConflict
			}
			item.Version = 1
		} else {
			return err
		}
		checks, err := readItems[Check](s.definitionDir("checks"))
		if err != nil {
			return err
		}
		validators, err := readItems[PublishedValidator](s.definitionDir("validators"))
		if err != nil {
			return err
		}
		locals, err := readItems[QueryValidators](s.definitionDir("query-validators"))
		if err != nil {
			return err
		}
		graph := map[string]Check{}
		for _, c := range checks {
			graph[c.ID] = c
		}
		catalog := map[string]PublishedValidator{}
		for _, v := range validators {
			catalog[v.ID] = v
		}
		catalog[item.ID] = item
		localMap := map[string]QueryValidators{}
		for _, v := range locals {
			localMap[v.QueryID] = v
		}
		if err := validateGraph(graph, catalog, localMap); err != nil {
			return err
		}
		if err := writeItem(s.definitionDir("validators"), item.ID, item); err != nil {
			return err
		}
		return s.auditMutation(ctx, AuditEvent{ID: fmt.Sprintf("validator-%s-%d", item.ID, item.Version), At: time.Now().UTC(), Actor: actor, Project: s.projectID, Action: "save-validator"})
	})
	return item, err
}

func validQueryRef(id string) bool {
	if id == "" || len(id) > 512 {
		return false
	}
	for _, segment := range strings.Split(id, "/") {
		if segment == "" || segment == "." || segment == ".." || len(segment) > 128 {
			return false
		}
		for _, r := range segment {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-') {
				return false
			}
		}
	}
	return true
}

func queryValidatorFileID(queryID string) string {
	sum := sha256.Sum256([]byte(queryID))
	return "q-" + hex.EncodeToString(sum[:16])
}

func (s *LocalStore) SaveQueryValidators(ctx context.Context, item QueryValidators, actor string) (QueryValidators, error) {
	if !validQueryRef(item.QueryID) || len(item.Validators) == 0 || len(item.Validators) > 32 {
		return QueryValidators{}, ErrInvalid
	}
	seen := map[string]bool{}
	for _, v := range item.Validators {
		if err := validateValidator(v); err != nil || seen[v.ID] {
			return QueryValidators{}, ErrInvalid
		}
		seen[v.ID] = true
	}
	err := s.withLock(ctx, func() error {
		data, err := safeReadFile(s.definitionDir("query-validators"), queryValidatorFileID(item.QueryID))
		if err == nil {
			var old QueryValidators
			if e := json.Unmarshal(data, &old); e != nil {
				return e
			}
			if item.QueryID != old.QueryID || item.Version != old.Version {
				return ErrConflict
			}
			item.Version++
		} else if errors.Is(err, os.ErrNotExist) {
			if item.Version != 0 {
				return ErrConflict
			}
			item.Version = 1
		} else {
			return err
		}
		checks, err := readItems[Check](s.definitionDir("checks"))
		if err != nil {
			return err
		}
		validators, err := readItems[PublishedValidator](s.definitionDir("validators"))
		if err != nil {
			return err
		}
		locals, err := readItems[QueryValidators](s.definitionDir("query-validators"))
		if err != nil {
			return err
		}
		graph := map[string]Check{}
		for _, c := range checks {
			graph[c.ID] = c
		}
		catalog := map[string]PublishedValidator{}
		for _, v := range validators {
			catalog[v.ID] = v
		}
		localMap := map[string]QueryValidators{}
		for _, v := range locals {
			localMap[v.QueryID] = v
		}
		localMap[item.QueryID] = item
		if err := validateGraph(graph, catalog, localMap); err != nil {
			return err
		}
		if err := writeItem(s.definitionDir("query-validators"), queryValidatorFileID(item.QueryID), item); err != nil {
			return err
		}
		return s.auditMutation(ctx, AuditEvent{ID: fmt.Sprintf("query-validators-%s-%d", queryValidatorFileID(item.QueryID), item.Version), At: time.Now().UTC(), Actor: actor, Project: s.projectID, Action: "save-query-validators"})
	})
	return item, err
}

func (s *LocalStore) auditMutation(ctx context.Context, event AuditEvent) error {
	if err := s.audit.Append(ctx, event); err != nil {
		return fmt.Errorf("%w: audit store failed", ErrOutcomeUncertain)
	}
	return nil
}

func (a localJSONLAudit) Append(_ context.Context, event AuditEvent) error {
	path := a.path
	if err := ensureAbsentOrRegular(path); err != nil {
		return err
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("audit: %w", err)
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("audit: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	return nil
}

func (s *LocalStore) Append(ctx context.Context, event AuditEvent) error {
	return s.withLock(ctx, func() error { return s.audit.Append(ctx, event) })
}

func validTransition(old, next, reason string) bool {
	switch old {
	case "active":
		return next == "active" || (next == "closed" && strings.TrimSpace(reason) != "")
	case "closed":
		return next == "closed" || next == "active" || next == "archived"
	case "archived":
		return next == "archived"
	}
	return false
}

func validSlug(id string) bool {
	if len(id) == 0 || len(id) > 128 || id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			return false
		}
	}
	return true
}

// The API has already resolved this ID against its served-project map. It is
// only an audit label here, never a filesystem component; existing project
// IDs need not follow the slugs chosen for new Detective resources.
func validServedProjectID(id string) bool {
	return id != "" && len(id) <= 512 && !strings.ContainsAny(id, "\x00\n\r")
}
