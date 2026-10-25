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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/privateadmission"
	"github.com/datatug/datatug-core/pkg/datatug"
	core "github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"github.com/gofrs/flock"
)

var ErrConflict = core.ErrConflict
var ErrNotFound = core.ErrNotFound
var ErrInvalid = core.ErrInvalid
var ErrCapacity = core.ErrCapacity
var ErrOutcomeUncertain = core.ErrOutcomeUncertain

const maxItems = 200

type AuditEvent = core.AuditEvent

type AuditStore interface {
	Append(context.Context, AuditEvent) error
}

type fileLocker interface {
	TryLockContext(ctx context.Context, retryDelay time.Duration) (bool, error)
	Unlock() error
}

type tempFile interface {
	Chmod(mode os.FileMode) error
	Write(b []byte) (n int, err error)
	Sync() error
	Close() error
	Name() string
}

type syncCloserWriter interface {
	Write(p []byte) (n int, err error)
	Sync() error
	Close() error
}

var (
	newFlock            = func(path string) fileLocker { return flock.New(path) }
	storeAbs            = filepath.Abs
	storeEvalSymlinks   = filepath.EvalSymlinks
	storeResolveQueryID = api.ResolveQueryID
	storeLstat          = os.Lstat
	storeStat           = os.Stat
	storeRename         = os.Rename
	runEntryInfo        = func(e os.DirEntry) (os.FileInfo, error) { return e.Info() }
	stateMarshal        = json.Marshal
	auditMarshal        = json.Marshal
	auditOpenFile       = func(path string, flag int, perm os.FileMode) (syncCloserWriter, error) {
		return os.OpenFile(path, flag, perm)
	}
	writeItemSyncDir    = syncPrivateDirectory
	writeItemCreateTemp = func(dir, pattern string) (tempFile, error) {
		return os.CreateTemp(dir, pattern)
	}
)

type localJSONLAudit struct{ path string }

// LocalStore keeps reviewable definitions in the project and sensitive case,
// run and audit material in a separate server-private directory.
type LocalStore struct {
	projectDir  string
	privateDir  string
	projectID   string
	lock        fileLocker
	audit       AuditStore
	nativeFence func(context.Context, sessioncontext.DetectiveFence) error
}

func NewLocalStore(projectID, projectDir, privateBase string) (*LocalStore, error) {
	return NewLocalStoreContext(context.Background(), projectID, projectDir, privateBase)
}

// NewLocalStoreContext preserves cancellation while acquiring the private scope lock.
func NewLocalStoreContext(ctx context.Context, projectID, projectDir, privateBase string) (*LocalStore, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validServedProjectID(projectID) || projectDir == "" || privateBase == "" {
		return nil, ErrInvalid
	}
	project, err := storeAbs(projectDir)
	if err != nil {
		return nil, err
	}
	project, err = storeEvalSymlinks(project)
	if err != nil {
		return nil, err
	}
	base, err := storeAbs(privateBase)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(base, 0700); err != nil {
		return nil, err
	}
	base, err = storeEvalSymlinks(base)
	if err != nil {
		return nil, err
	}
	project, err = privateadmission.PhysicalDirectory(project)
	if err != nil {
		return nil, err
	}
	base, err = privateadmission.PhysicalDirectory(base)
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
	store := &LocalStore{projectDir: project, privateDir: private, projectID: projectID, lock: newFlock(lockPath), audit: localJSONLAudit{path: auditPath}}
	// The private directory predates per-project receipts and is keyed by the
	// physical checkout. Bind it to its first served project ID under the same
	// lock, including when upgrading an existing directory, so two logical
	// projects cannot read each other's Cases through one checkout alias.
	err = store.withLock(ctx, func() error {
		data, err := safeReadFile(private, "scope")
		if errors.Is(err, os.ErrNotExist) {
			return writeItem(private, "scope", struct {
				ProjectID string `json:"projectId"`
			}{ProjectID: projectID})
		}
		if err != nil {
			return err
		}
		var scope struct {
			ProjectID string `json:"projectId"`
		}
		if json.Unmarshal(data, &scope) != nil || scope.ProjectID != projectID {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return store, nil
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
	info, err := storeLstat(path)
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
	info, err := storeLstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalid
	}
	return nil
}

func ensureAbsentOrRegular(path string) error {
	info, err := storeLstat(path)
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
	info, err := storeLstat(path)
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
	return privateadmission.Local(ctx, filepath.Join(s.privateDir, "store.lock"), func() error {
		ok, err := s.lock.TryLockContext(ctx, 50*time.Millisecond)
		if err != nil {
			return err
		}
		if !ok {
			return context.DeadlineExceeded
		}
		defer func() { _ = s.lock.Unlock() }()
		return fn()
	})
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
	return writeItemDurability(dir, id, item, false)
}

// writeDurableItem acknowledges journal acceptance only after both the file
// rename and directory entries are durable on supported bridge platforms.
func writeDurableItem(dir, id string, item any) error {
	return writeItemDurability(dir, id, item, true)
}

func writeItemDurability(dir, id string, item any, syncDirectory bool) error {
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
	if _, err := storeLstat(path); err == nil {
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
	tmp, err := writeItemCreateTemp(dir, ".detective-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
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
	if err = storeRename(tmp.Name(), path); err != nil {
		return err
	}
	if !syncDirectory {
		return nil
	}
	if err = writeItemSyncDir(dir); err != nil {
		return err
	}
	return writeItemSyncDir(filepath.Dir(dir))
}

func (s *LocalStore) State(ctx context.Context) (State, error) { return s.StatePage(ctx, "") }

// StatePage returns a bounded latest run page. The opaque cursor orders file
// write time plus ID, so history does not make the catalog unusable.
func (s *LocalStore) StatePage(ctx context.Context, cursor string) (State, error) {
	var state State
	err := s.withLock(ctx, func() error {
		var err error
		state, err = s.statePageLocked(cursor)
		return err
	})
	return state, err
}

// statePageLocked reads one coherent local Detective image. Callers must hold
// the project lock; AI dispatch uses it to recheck reviewed basis at admission.
func (s *LocalStore) statePageLocked(cursor string) (State, error) {
	var state State
	var err error
	if state.Checks, err = readItems[Check](s.definitionDir("checks")); err != nil {
		return State{}, err
	}
	if state.Validators, err = readItems[PublishedValidator](s.definitionDir("validators")); err != nil {
		return State{}, err
	}
	if state.QueryValidators, err = readItems[QueryValidators](s.definitionDir("query-validators")); err != nil {
		return State{}, err
	}
	if state.Scenarios, err = readItems[Scenario](s.definitionDir("scenarios")); err != nil {
		return State{}, err
	}
	if state.Cases, err = readItems[Case](s.privateItemDir("cases")); err != nil {
		return State{}, err
	}
	for i := range state.Cases {
		if len(state.Cases[i].Timeline) > 50 {
			trim := len(state.Cases[i].Timeline) - 50
			state.Cases[i].TimelineOffset += trim
			state.Cases[i].Timeline = append([]CaseEvent(nil), state.Cases[i].Timeline[trim:]...)
		}
	}
	if state.Runs, state.RunsHasMore, state.RunsNextCursor, err = readRunPage(s.privateItemDir("runs"), cursor); err != nil {
		return State{}, err
	}
	sort.Slice(state.Checks, func(i, j int) bool { return state.Checks[i].ID < state.Checks[j].ID })
	sort.Slice(state.Validators, func(i, j int) bool { return state.Validators[i].ID < state.Validators[j].ID })
	sort.Slice(state.QueryValidators, func(i, j int) bool { return state.QueryValidators[i].QueryID < state.QueryValidators[j].QueryID })
	sort.Slice(state.Cases, func(i, j int) bool { return state.Cases[i].UpdatedAt.After(state.Cases[j].UpdatedAt) })
	sort.Slice(state.Runs, func(i, j int) bool { return state.Runs[i].StartedAt.After(state.Runs[j].StartedAt) })
	sort.Slice(state.Scenarios, func(i, j int) bool { return state.Scenarios[i].ID < state.Scenarios[j].ID })
	state.Capabilities = Capabilities{LocalOnly: true, CanRead: true, CanWrite: true, CanRun: true, MaxRows: 500, MaxFanout: core.MaxFanout}
	encoded, err := stateMarshal(state)
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
		info, err := runEntryInfo(entry)
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

// resolveCreatedAt is the one creation-date rule for checks and scenarios,
// shared by SaveCheck/SaveScenario, action apply and AI confirm. A true create
// takes the caller's (already validated) date, else now. An update keeps the
// stored value as is, so a legacy undated file is never backfilled and a
// caller cannot backdate.
func resolveCreatedAt(caller, stored string, exists bool, now time.Time) string {
	if exists {
		return stored
	}
	if caller != "" {
		return caller
	}
	return datatug.NewCreatedAt(now)
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
	for i := range check.Steps {
		if check.Steps[i].Kind == "query" {
			canonical, err := s.resolveQueryRef(check.Steps[i].QueryID)
			if err != nil {
				return Check{}, err
			}
			check.Steps[i].QueryID = canonical
		}
	}
	err := s.withLock(ctx, func() error {
		if err := s.reconcilePendingActions(ctx, "checks", check.ID, ""); err != nil {
			return err
		}
		old, err := s.getCheck(check.ID)
		if err == nil {
			if check.Version != old.Version {
				return ErrConflict
			}
			check.Version++
			check.CreatedAt = resolveCreatedAt(check.CreatedAt, old.CreatedAt, true, time.Now())
		} else if errors.Is(err, ErrNotFound) {
			if check.Version != 0 {
				return ErrConflict
			}
			check.Version = 1
			check.CreatedAt = resolveCreatedAt(check.CreatedAt, "", false, time.Now())
		} else {
			return err
		}
		all, err := readItems[Check](s.definitionDir("checks"))
		if err != nil {
			return err
		}
		if check.Version == 1 && len(all) >= maxItems {
			return ErrCapacity
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
		if err := s.ensureQueryRefs(graph, queryValidators); err != nil {
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

type caseEventChunk struct {
	CaseID string      `json:"caseId"`
	Start  int         `json:"start"`
	Events []CaseEvent `json:"events"`
}

func caseTimelinePrefix(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "t-" + hex.EncodeToString(sum[:8]) + "-"
}

func (s *LocalStore) compactCaseTimeline(c *Case) error {
	if len(c.Timeline) <= 400 {
		return nil
	}
	n := len(c.Timeline) - 300
	chunk := caseEventChunk{CaseID: c.ID, Start: c.TimelineOffset, Events: append([]CaseEvent(nil), c.Timeline[:n]...)}
	id := fmt.Sprintf("%s%09d", caseTimelinePrefix(c.ID), chunk.Start)
	if err := writeItem(s.privateItemDir("case-timeline"), id, chunk); err != nil {
		return err
	}
	c.TimelineOffset += n
	c.Timeline = append([]CaseEvent(nil), c.Timeline[n:]...)
	return nil
}

// CaseEventsPage returns up to 100 immutable timeline events, newest page
// first by cursor while preserving chronological order inside the page.
func (s *LocalStore) CaseEventsPage(ctx context.Context, id string, before int) (CaseEventsPage, error) {
	if !validSlug(id) || before < 0 {
		return CaseEventsPage{}, ErrInvalid
	}
	var page CaseEventsPage
	err := s.withLock(ctx, func() error {
		c, err := s.getCase(id)
		if err != nil {
			return err
		}
		total := c.TimelineOffset + len(c.Timeline)
		if before == 0 {
			before = total
		}
		if before > total {
			return ErrInvalid
		}
		start := before - 100
		if start < 0 {
			start = 0
		}
		page = CaseEventsPage{CaseID: id, Events: make([]CaseEvent, before-start), NextBefore: start, HasMore: start > 0}
		filled := make([]bool, len(page.Events))
		for i := start; i < before; i++ {
			if i >= c.TimelineOffset {
				page.Events[i-start] = c.Timeline[i-c.TimelineOffset]
				filled[i-start] = true
			}
		}
		if start < c.TimelineOffset {
			dir := s.privateItemDir("case-timeline")
			if err := ensureDirIfExists(dir); err != nil {
				return err
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				return err
			}
			prefix := caseTimelinePrefix(id)
			var starts []int
			for _, entry := range entries {
				name := entry.Name()
				if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".json") {
					continue
				}
				n, parseErr := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".json"))
				if parseErr == nil && n < before {
					starts = append(starts, n)
				}
			}
			sort.Sort(sort.Reverse(sort.IntSlice(starts)))
			for _, chunkStart := range starts {
				chunkID := fmt.Sprintf("%s%09d", prefix, chunkStart)
				data, err := safeReadFile(dir, chunkID)
				if err != nil {
					return err
				}
				var chunk caseEventChunk
				if err := json.Unmarshal(data, &chunk); err != nil {
					return err
				}
				if chunk.CaseID != id || chunk.Start != chunkStart {
					return ErrInvalid
				}
				for j, event := range chunk.Events {
					pos := chunkStart + j
					if pos >= start && pos < before && pos < c.TimelineOffset {
						page.Events[pos-start] = event
						filled[pos-start] = true
					}
				}
				if chunkStart <= start {
					break
				}
			}
		}
		for _, ok := range filled {
			if !ok {
				return ErrInvalid
			}
		}
		return nil
	})
	return page, err
}

func (s *LocalStore) SaveRun(ctx context.Context, run CheckRun) error {
	return s.withLock(ctx, func() error {
		if !validSlug(run.ID) {
			return ErrInvalid
		}
		if _, err := storeStat(filepath.Join(s.privateItemDir("runs"), run.ID+".json")); err == nil {
			return ErrConflict
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := writeItem(s.privateItemDir("runs"), run.ID, run); err != nil {
			return err
		}
		audit := core.AuditForRun(run)
		audit.Project = s.projectID
		return s.auditMutation(ctx, audit)
	})
}

func (s *LocalStore) SaveScenario(ctx context.Context, scenario Scenario, actor string) (Scenario, error) {
	if err := core.ValidateScenario(scenario); err != nil {
		return Scenario{}, err
	}
	for i := range scenario.Steps {
		if scenario.Steps[i].Kind == "query" {
			canonical, err := s.resolveQueryRef(scenario.Steps[i].Ref)
			if err != nil {
				return Scenario{}, err
			}
			scenario.Steps[i].Ref = canonical
		} else if _, err := s.getCheck(scenario.Steps[i].Ref); err != nil {
			return Scenario{}, err
		}
	}
	err := s.withLock(ctx, func() error {
		if err := s.reconcilePendingActions(ctx, "scenarios", scenario.ID, ""); err != nil {
			return err
		}
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
			scenario.CreatedAt = resolveCreatedAt(scenario.CreatedAt, old.CreatedAt, true, time.Now())
		} else if errors.Is(err, os.ErrNotExist) {
			if scenario.Version != 0 {
				return ErrConflict
			}
			items, listErr := readItems[Scenario](s.definitionDir("scenarios"))
			if listErr != nil {
				return listErr
			}
			if len(items) >= maxItems {
				return ErrCapacity
			}
			scenario.Version = 1
			scenario.CreatedAt = resolveCreatedAt(scenario.CreatedAt, "", false, time.Now())
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
	if err := core.ValidatePublishedValidator(item); err != nil {
		return PublishedValidator{}, err
	}
	err := s.withLock(ctx, func() error {
		if err := s.reconcilePendingActions(ctx, "validators", item.ID, ""); err != nil {
			return err
		}
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
		if item.Version == 1 && len(validators) >= maxItems {
			return ErrCapacity
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
		if err := s.ensureQueryRefs(graph, localMap); err != nil {
			return err
		}
		if err := writeItem(s.definitionDir("validators"), item.ID, item); err != nil {
			return err
		}
		return s.auditMutation(ctx, AuditEvent{ID: fmt.Sprintf("validator-%s-%d", item.ID, item.Version), At: time.Now().UTC(), Actor: actor, Project: s.projectID, Action: "save-validator"})
	})
	return item, err
}

func validQueryRef(id string) bool { return core.ValidQueryRef(id) }

func (s *LocalStore) resolveQueryRef(id string) (string, error) {
	if !validQueryRef(id) {
		return "", ErrInvalid
	}
	canonical, err := storeResolveQueryID(s.projectDir, id)
	if err != nil {
		return "", ErrNotFound
	}
	if !validQueryRef(canonical) {
		return "", ErrInvalid
	}
	queriesDir := filepath.Join(s.projectDir, "queries")
	if err := ensureDirIfExists(queriesDir); err != nil {
		return "", err
	}
	parts := strings.Split(canonical, "/")
	parent := queriesDir
	for _, part := range parts[:len(parts)-1] {
		parent = filepath.Join(parent, part)
		if err := ensureDirIfExists(parent); err != nil {
			return "", err
		}
	}
	if err := ensureAbsentOrRegular(filepath.Join(parent, parts[len(parts)-1]+".query.json")); err != nil {
		return "", err
	}
	return canonical, nil
}

func (s *LocalStore) ensureQueryRefs(checks map[string]Check, locals map[string]QueryValidators) error {
	seen := map[string]bool{}
	for _, check := range checks {
		for _, step := range check.Steps {
			if step.Kind != "query" || seen[step.QueryID] {
				continue
			}
			canonical, err := s.resolveQueryRef(step.QueryID)
			if err != nil || canonical != step.QueryID {
				return ErrNotFound
			}
			seen[step.QueryID] = true
		}
	}
	for id := range locals {
		if seen[id] {
			continue
		}
		canonical, err := s.resolveQueryRef(id)
		if err != nil || canonical != id {
			return ErrNotFound
		}
		seen[id] = true
	}
	return nil
}

func queryValidatorFileID(queryID string) string {
	sum := sha256.Sum256([]byte(queryID))
	return "q-" + hex.EncodeToString(sum[:16])
}

func (s *LocalStore) SaveQueryValidators(ctx context.Context, item QueryValidators, actor string) (QueryValidators, error) {
	if err := core.ValidateQueryValidators(item); err != nil {
		return QueryValidators{}, err
	}
	canonical, err := s.resolveQueryRef(item.QueryID)
	if err != nil {
		return QueryValidators{}, err
	}
	item.QueryID = canonical
	err = s.withLock(ctx, func() error {
		if err := s.reconcilePendingActions(ctx, "query-validators", queryValidatorFileID(item.QueryID), ""); err != nil {
			return err
		}
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
		if item.Version == 1 && len(locals) >= maxItems {
			return ErrCapacity
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
		if err := s.ensureQueryRefs(graph, localMap); err != nil {
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
	data, err := auditMarshal(event)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	f, err := auditOpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
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

func validSlug(id string) bool { return core.ValidSlug(id) }

// The API has already resolved this ID against its served-project map. It is
// only an audit label here, never a filesystem component; existing project
// IDs need not follow the slugs chosen for new Detective resources.
func validServedProjectID(id string) bool {
	return id != "" && len(id) <= 512 && !strings.ContainsAny(id, "\x00\n\r")
}
