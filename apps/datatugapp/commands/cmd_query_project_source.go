package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/condeval"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/meaninglabelsfile"
	"github.com/datatug/datatug-cli/pkg/policyexceptions"
	"github.com/datatug/datatug-core/pkg/meaninglabels"
)

// A project read is `query run --project <p> --source <db> --from <table>`
// (or -f): one table of one source of a project, read through that project's
// own policies/ directory (never ~/.datatug/policies), denied by default,
// with the project's applied local policy exceptions overlaid and its
// Meaning Graph sensitivity labels masking the output.

// projectReadNow is the clock exceptions are evaluated against; a seam for tests.
var projectReadNow = time.Now

// projectPoliciesDir is the policies folder of a project.
const projectPoliciesDir = "policies"

type projectRead struct {
	projectDir string
	projectID  string
	source     string
	env        string
	sourceURL  string
	backend    dbcopy.BackendRef
	exceptions policyexceptions.Store
	// applied maps the rule id of each exception overlaid on the loaded
	// policies to that exception.
	applied map[string]policyexceptions.Exception
	now     time.Time
	labels  meaninglabels.Document
	table   string
	masked  map[string]meaninglabels.ColumnLabel
	// view is whether the table is a view or an object of unknown kind: one
	// no owner reviewed is masked entirely, as the server masks it.
	view bool
	// key is the single primary-key column whose value is the record key, or
	// "" when the key is not a column of the table.
	key string
}

// resolveProjectRead resolves --project, --env and --source to the source the
// read opens.
func resolveProjectRead(ctx context.Context, o queryOptions) (*projectRead, error) {
	projectDir, projectID, store, err := resolveQueryProjectRef(o.project)
	if err != nil {
		return nil, Exit(err.Error(), exitCodeUsage)
	}
	env, err := resolveQueryEnvironment(ctx, store, o.env)
	if err != nil {
		return nil, Exit(err.Error(), exitCodeUsage)
	}
	source, err := resolveQuerySource(ctx, store, projectDir, env, o.source)
	var backend dbcopy.BackendRef
	if err == nil {
		backend, err = dbcopy.Parse(source.URL)
	}
	if err != nil {
		return nil, Exit(err.Error(), exitCodeUsage)
	}
	return &projectRead{projectDir: projectDir, projectID: projectID, source: o.source, env: env, sourceURL: source.URL, backend: backend.WithFlag("--source"), exceptions: policyexceptions.Store{ProjectDir: projectDir, Caller: detectCaller()}}, nil
}

// errNoProjectPolicies is the refusal of a project read in a project that has
// no policy: a project read is denied by default.
func errNoProjectPolicies() error {
	return Exit("access denied: the project has no policy in its policies/ folder, and a project read is denied by default", exitCodeAccessDenied)
}

// loadPolicies loads <project>/policies/ with the applied, unexpired local
// policy exceptions of this source overlaid. An exception store that cannot
// be read contributes nothing (with a warning), so it never widens a read.
func (r *projectRead) loadPolicies(stderr io.Writer) ([]accesspolicies.Loaded, error) {
	r.now = projectReadNow()
	all, unreadable, err := r.exceptions.List()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "warning: local policy exceptions are not applied: %v\n", err)
	}
	for _, name := range unreadable {
		_, _ = fmt.Fprintf(stderr, "warning: local policy exception %s is unreadable and is not applied\n", name)
	}
	r.exceptions.RecordExpiries(all, r.now)
	var reader policyexceptions.SessionBinding
	if caller := r.exceptions.Caller; caller != nil {
		reader = policyexceptions.SessionBinding{Harness: caller.Harness, Session: caller.Session}
	}
	r.applied = map[string]policyexceptions.Exception{}
	overlay := func(path string, data []byte, codec access.Codec) ([]byte, error) {
		rel, _ := filepath.Rel(r.projectDir, path) // both are paths of the project folder
		binding := policyexceptions.Binding{ProjectID: r.projectID, Env: r.env, Source: r.source, SourceIdentity: policyexceptions.SourceFingerprint(r.sourceURL), PolicyPath: filepath.ToSlash(rel), Reader: reader}
		out, ids, err := policyexceptions.Overlay(data, codec, binding, all, r.now)
		for _, id := range ids {
			for _, e := range all {
				if e.ID == id {
					r.applied[e.RuleID()] = e
				}
			}
		}
		return out, err
	}
	loaded, err := accesspolicies.LoadDirWith(filepath.Join(r.projectDir, projectPoliciesDir), overlay)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && len(loaded) == 0) {
		return nil, errNoProjectPolicies()
	}
	if err != nil {
		return nil, Exit(err.Error(), exitCodeUsage)
	}
	return loaded, nil
}

// recordUses writes a used event for every exception whose rule decided the
// read. A use that cannot be recorded refuses the read.
func (r *projectRead) recordUses(lines []accesspolicies.Line) error {
	for _, line := range lines {
		// A rule of a principal policy set is reported as <rule set>/<rule>;
		// an exception's rule set and rule share its rule id.
		_, rule, _ := strings.Cut(line.Rule, "/")
		e, ok := r.applied[rule]
		if !ok || !line.Allowed || line.Rule != rule+"/"+rule {
			continue
		}
		if err := r.exceptions.Record(policyexceptions.NewEvent("used", e, e.AppliedFields, r.now)); err != nil {
			return Exit("access denied: the read is allowed only by local policy exception "+e.ID+", and its use cannot be written to the audit log", exitCodeAccessDenied)
		}
	}
	return nil
}

// loadLabels is how a project read loads the Meaning Graph labels; a seam for tests.
var loadLabels = func(ctx context.Context, projectDir string) (meaninglabels.Document, error) {
	return meaninglabelsfile.Store{Dir: projectDir}.Load(ctx)
}

// errFieldProtected is a masking refusal.
type errFieldProtected struct{ message string }

func (e *errFieldProtected) Error() string { return e.message }

// prepareMasking checks the query against the project's labels once the
// policy has admitted it and before any row is read, so a masking refusal
// never speaks for a table the policy denies: one table, no subquery, and no filter, sort, grouping, alias or
// expression on a protected column. A label document that cannot be read
// refuses the read.
func (r *projectRead) prepareMasking(ctx context.Context, db dal.DB, query dal.Query) *errFieldProtected {
	doc, err := loadLabels(ctx, r.projectDir)
	if err != nil {
		return &errFieldProtected{"the project's sensitivity labels (datatug-labels.json) cannot be read, so the read is refused"}
	}
	r.labels = doc
	structured, ok := query.(dal.StructuredQuery)
	var base dal.CollectionRef
	if ok {
		base, ok = baseCollectionRef(structured)
	}
	if !ok || dal.HasSubquery(structured) || len(structured.From().Joins()) > 0 {
		return &errFieldProtected{"a project read takes one table without joins or subqueries, so its labelled columns can be masked"}
	}
	r.table = base.Name()
	r.describe(ctx, db)
	referenced, err := accesspolicies.NonProjectionFields(structured)
	if err != nil {
		return &errFieldProtected{"the query uses an expression the masking check cannot inspect"}
	}
	plan := meaninglabels.NewPlan(meaninglabels.EffectiveObject(doc, r.source, r.table, r.view, labelColumnsOf(referenced)), nil)
	if err := plan.CheckReferenced(r.table, referenced); err != nil {
		return &errFieldProtected{strings.TrimPrefix(err.Error(), meaninglabels.CodeFieldProtected+": ")}
	}
	r.masked = map[string]meaninglabels.ColumnLabel{}
	return nil
}

// listSourceViews lists the views of a source that can say; a seam for tests.
var listSourceViews = func(ctx context.Context, db dal.DB) ([]dbschema.SourceViewDef, error) {
	reader, ok := dal.As[dbschema.SourceViewReader](db)
	if !ok {
		return nil, nil
	}
	return reader.ListSourceViews(ctx)
}

// describe finds what kind of object the table is and which column holds
// its record key. Like the server, only a table of a SQL source that the
// source lists, and does not list as a view, is a table; every other object,
// and any failure to tell, is treated as a view.
func (r *projectRead) describe(ctx context.Context, db dal.DB) {
	r.view, r.key = true, ""
	if r.backend.Scheme != "sqlite" && !dbcopy.IsServerScheme(r.backend.Scheme) {
		return
	}
	collections, err := listCollections(ctx, db, nil)
	if err != nil {
		return
	}
	listed := false
	for _, ref := range collections {
		listed = listed || ref.Name() == r.table
	}
	views, err := listSourceViews(ctx, db)
	if !listed || err != nil {
		return
	}
	for _, v := range views {
		if v.Name == r.table {
			return
		}
	}
	r.view = false
	ref := dal.NewRootCollectionRef(r.table, "")
	if def, err := describeCollection(ctx, db, &ref); err == nil && len(def.PrimaryKey) == 1 {
		r.key = string(def.PrimaryKey[0])
	}
}

func baseCollectionRef(query dal.StructuredQuery) (dal.CollectionRef, bool) {
	switch source := query.From().Base().(type) {
	case dal.CollectionRef:
		return source, true
	case *dal.CollectionRef:
		return *source, true
	}
	return dal.CollectionRef{}, false
}

func labelColumnsOf(names []string) []meaninglabels.Column {
	columns := make([]meaninglabels.Column, len(names))
	for i, name := range names {
		columns[i] = meaninglabels.Column{Name: name}
	}
	return columns
}

// maskingReader replaces the value of every protected column of every row
// with meaninglabels.MaskedValue before any output sees it.
type maskingReader struct {
	dal.RecordsReader
	read   *projectRead
	labels map[string]meaninglabels.ColumnLabel
}

func (r *projectRead) mask(reader dal.RecordsReader) dal.RecordsReader {
	if reader == nil {
		return nil
	}
	return &maskingReader{RecordsReader: reader, read: r, labels: map[string]meaninglabels.ColumnLabel{}}
}

func (m *maskingReader) label(column string) meaninglabels.ColumnLabel {
	if l, ok := m.labels[column]; ok {
		return l
	}
	l := meaninglabels.EffectiveObject(m.read.labels, m.read.source, m.read.table, m.read.view, []meaninglabels.Column{{Name: column}})[0]
	m.labels[column] = l
	return l
}

func (m *maskingReader) Next() (record.Record, error) {
	rec, err := m.RecordsReader.Next()
	if err != nil || rec == nil || !rec.Exists() {
		return rec, err
	}
	data, err := condeval.ToMap(rec.Data())
	if err != nil {
		return nil, fmt.Errorf("masking: %w", err)
	}
	masked := make(map[string]any, len(data))
	for column, value := range data {
		if l := m.label(column); l.Protected() {
			masked[column] = meaninglabels.MaskedValue
			m.read.masked[column] = l
			continue
		}
		masked[column] = value
	}
	return record.NewRecordWithData(m.key(rec.Key(), masked), masked), nil
}

// key is the record key to show: the source's own key only when it is the
// value of the table's single key column, that column is in the row (the
// policy allows it) and it is not masked; otherwise a withheld key, so a key
// never carries a value the read may not see.
func (m *maskingReader) key(key *record.Key, row map[string]any) *record.Key {
	column := m.read.key
	if value, present := row[column]; key != nil && column != "" && present && value != meaninglabels.MaskedValue && fmt.Sprint(value) == fmt.Sprint(key.ID) {
		return key
	}
	return record.NewKeyWithID(m.read.table, meaninglabels.MaskedValue)
}

// maskedNote is the stderr line naming the columns a read masked.
func (r *projectRead) maskedNote() string {
	if len(r.masked) == 0 {
		return ""
	}
	names := make([]string, 0, len(r.masked))
	for column, l := range r.masked {
		names = append(names, fmt.Sprintf("%s.%s (%s)", r.table, column, l.Class))
	}
	sort.Strings(names)
	return "masked: " + strings.Join(names, ", ") + " by Meaning Graph sensitivity labels; local masking keeps an honest agent inside the lines"
}

// denialJSON is the structured refusal `query run --format json` writes to stderr.
type denialJSON struct {
	Error denialBody `json:"error"`
}

type denialBody struct {
	Code      string         `json:"code"`
	Policy    string         `json:"policy,omitempty"`
	Resource  string         `json:"resource,omitempty"`
	Operation string         `json:"operation"`
	Reason    string         `json:"reason"`
	Request   *denialRequest `json:"request,omitempty"`
}

type denialRequest struct {
	Command string `json:"command"`
	Note    string `json:"note"`
}

// reportedJSONDenial writes one JSON refusal to stderr and returns the error
// main reports by its exit code alone.
func reportedJSONDenial(stderr io.Writer, body denialBody, code int) error {
	encoder := json.NewEncoder(stderr)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(denialJSON{Error: body}) // strings only; one line
	return Reported(body.Reason, code)
}

// accessDenialBody describes a policy refusal of a query run.
func accessDenialBody(lines []accesspolicies.Line, err error, o queryOptions, query dal.Query) denialBody {
	body := denialBody{Code: "ACCESS_DENIED", Operation: "query", Reason: err.Error(), Resource: accesspolicies.BaseResource(query).String()}
	for _, line := range lines {
		if !line.Allowed {
			body.Policy, body.Resource, body.Reason = line.Policy, line.Resource, line.Explanation
			break
		}
	}
	if body.Policy == "" && len(lines) > 0 {
		body.Policy = lines[0].Policy
	}
	if o.source != "" {
		role := "<role>"
		if len(o.roles) > 0 {
			role = o.roles[0]
		}
		table := strings.TrimPrefix(body.Resource, "/")
		if structured, ok := query.(dal.StructuredQuery); ok {
			if base, ok := baseCollectionRef(structured); ok {
				table = base.Name()
			}
		}
		body.Request = &denialRequest{
			Command: fmt.Sprintf("datatug policy exception propose --project %s --source %s --role %s --table %s --fields <field,...> --for 15m --reason \"...\"", shellWord(o.project), shellWord(o.source), shellWord(role), shellWord(table)),
			Note:    "Propose a local policy exception; the person applies it in their own terminal. " + policyexceptions.HonestLimit,
		}
	}
	return body
}

// shellWord quotes s for a POSIX shell when it is not a plain word.
func shellWord(s string) string {
	plain := s != ""
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_./:,", c)) {
			plain = false
		}
	}
	if plain {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
