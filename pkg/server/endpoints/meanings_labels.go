package endpoints

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/tugql"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/meaninglabelsfile"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/meaninglabels"
	"github.com/datatug/datatug-core/pkg/semantic"
)

// Meaning Graph sensitivity and agent-facing meaning (backstage spec
// features/meaning-sensitivity): describe, find, a masked table read, and the
// label document's list and change routes. All take a JSON body by POST.

// agentHeader marks a request made in an agent context. It is self-declared:
// local masking is accident prevention for honest agents.
const agentHeader = "X-DataTug-Agent"

const localNotice = "Local masking is accident prevention for honest agents: an agent that runs DataTug as its person, without declaring itself, is that person. Under a control plane, protected fields open only through an elevated approval."

const (
	maxMeaningObjects = 300
	maxReadLimit      = 500
	defaultReadLimit  = 100
	maxFindMatches    = 100
)

func isAgentRequest(r *http.Request) bool { return r.Header.Get(agentHeader) != "" }

// labelStore is the project's label store with its audit trail.
type labelStore interface {
	meaninglabels.Store
	Append(meaninglabelsfile.AuditRecord) error
}

var labelsStoreFor = func(projectDir string) labelStore { return meaninglabelsfile.Store{Dir: projectDir} }

var nowUTC = func() time.Time { return time.Now().UTC() }

// sourceObject is one table or view a source reports.
type sourceObject struct {
	Name string
	View bool
	// Columns are a view's column names, when the source reports them.
	Columns []string
}

var listSourceObjectsHook = listSourceObjects

// listSourceObjects lists the tables and views of a SQL source through DALgo's
// schema reader, sorted by name.
func listSourceObjects(ctx context.Context, sourceURL string) ([]sourceObject, error) {
	ref, err := dbcopyParse(sourceURL)
	if err != nil {
		return nil, err
	}
	boundRef, bound, err := api.BoundSourceBackend(ctx, sourceURL)
	if err != nil {
		return nil, err
	}
	var db dal.DB
	if bound {
		db, err = openBoundSchemaDB(ctx, boundRef)
	} else {
		db, err = ref.Open(ctx)
	}
	if err != nil {
		return nil, err
	}
	defer closeSchemaDB(db)
	reader, ok := dalAsSchemaReader(db)
	if !ok {
		return nil, errors.New("the source does not support schema introspection")
	}
	refs, err := reader.ListCollections(ctx, nil)
	if err != nil {
		return nil, err
	}
	views := map[string]bool{}
	viewColumns := map[string][]string{}
	if viewReader, ok := dal.As[dbschema.SourceViewReader](db); ok {
		defs, err := viewReader.ListSourceViews(ctx)
		if err != nil {
			return nil, err
		}
		for _, d := range defs {
			views[d.Name] = true
			viewColumns[d.Name] = d.Columns
		}
	}
	seen := map[string]bool{}
	var out []sourceObject
	for _, r := range refs {
		if !seen[r.Name()] {
			seen[r.Name()] = true
			out = append(out, sourceObject{Name: r.Name(), View: views[r.Name()], Columns: viewColumns[r.Name()]})
		}
	}
	for name := range views {
		if !seen[name] {
			out = append(out, sourceObject{Name: name, View: true, Columns: viewColumns[name]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// meaningSource is a resolved project source with its labels.
type meaningSource struct {
	project, env, source string
	projectDir           string
	projStore            datatug.ProjectStore
	registered           api.ResolvedSource
	executor             *secureread.Executor
	store                labelStore
	doc                  meaninglabels.Document
}

type meaningSourceRequest struct {
	Project string   `json:"project"`
	Env     string   `json:"env"`
	Source  string   `json:"source"`
	Tables  []string `json:"tables,omitempty"`
}

func (q meaningSourceRequest) validate() error {
	for field, v := range map[string]string{"project": q.Project, "env": q.Env, "source": q.Source} {
		if !plainSchemaObjectName(v) {
			return newInvalidRequest(field, "must be a plain name")
		}
	}
	if len(q.Tables) > maxMeaningObjects {
		return newInvalidRequest("tables", "too many tables")
	}
	return nil
}

func openMeaningSource(ctx context.Context, q meaningSourceRequest) (*meaningSource, error) {
	if err := q.validate(); err != nil {
		return nil, err
	}
	projectDir, ok := relatedProjectDir(q.Project)
	if !ok {
		return nil, newContractError(apicontract.ErrCodeNotFound, "unknown project", "project")
	}
	projStore, err := relatedProjectStoreFor(q.Project)
	if err != nil {
		return nil, newInvalidRequest("project", "the project store cannot be opened")
	}
	registered, err := apiResolveSource(ctx, projStore, projectDir, q.Env, q.Source)
	if err != nil {
		return nil, newContractError(apicontract.ErrCodeNotFound, "unknown source in this environment", "source")
	}
	executor, ok := secureExecutorHook()
	if !ok {
		return nil, newContractError(codeInternal, "server has no policy-enforced session configured", "")
	}
	store := labelsStoreFor(projectDir)
	doc, err := store.Load(ctx)
	if err != nil {
		return nil, labelContractError(err)
	}
	return &meaningSource{project: q.Project, env: q.Env, source: q.Source, projectDir: projectDir, projStore: projStore,
		registered: registered, executor: executor, store: store, doc: doc}, nil
}

// objects lists the readable objects of the source, narrowed to names (any
// case) when names is not empty. A table the principal may not read is left
// out, so the answer never names it.
func (m *meaningSource) objects(ctx context.Context, names []string) ([]sourceObject, error) {
	if m.registered.Kind != api.SourceKindSQL {
		return nil, newInvalidRequest("source", "meaning describe supports SQL sources")
	}
	all, err := listSourceObjectsHook(ctx, m.registered.URL)
	if err != nil {
		return nil, unavailableOr(m.source, err, newSourceUnavailable("the source cannot be read"))
	}
	var out []sourceObject
	for _, o := range all {
		if len(names) > 0 && !containsFoldString(names, o.Name) {
			continue
		}
		if m.executor.Authorize(ctx, authorizeQuery(o.Name), nil) != nil {
			continue
		}
		out = append(out, o)
		if len(out) == maxMeaningObjects {
			break
		}
	}
	return out, nil
}

// kindOf reports whether a table is a view. A kind that cannot be determined
// counts as a view, which masks it whole until an owner reviews it.
func (m *meaningSource) isView(ctx context.Context, table string) bool {
	if m.registered.Kind != api.SourceKindSQL {
		return true
	}
	all, err := listSourceObjectsHook(ctx, m.registered.URL)
	if err != nil {
		return true
	}
	for _, o := range all {
		if o.Name == table {
			return o.View
		}
	}
	return true
}

func containsFoldString(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

func labelColumns(columns []semantic.Column) []meaninglabels.Column {
	out := make([]meaninglabels.Column, len(columns))
	for i, c := range columns {
		out[i] = meaninglabels.Column{Name: c.Name, DbType: c.Type}
	}
	return out
}

// labelView is a column's effective label on the wire.
type labelView struct {
	Class         meaninglabels.Class `json:"class"`
	Kind          meaninglabels.Kind  `json:"kind,omitempty"`
	Origin        string              `json:"origin"`
	State         string              `json:"state"`
	Rule          string              `json:"rule,omitempty"`
	Revealable    bool                `json:"revealable"`
	NonRevealable bool                `json:"nonRevealable,omitempty"`
}

func toLabelView(l meaninglabels.ColumnLabel) *labelView {
	if l.Class == "" {
		return nil
	}
	return &labelView{Class: l.Class, Kind: l.Kind, Origin: string(l.Origin), State: string(l.State), Rule: l.Rule,
		Revealable: l.Revealable(), NonRevealable: l.NonRevealable}
}

type meaningRef struct {
	Entity      string `json:"entity"`
	Field       string `json:"field,omitempty"`
	Title       string `json:"title,omitempty"`
	Provenance  string `json:"provenance,omitempty"`
	Description string `json:"description,omitempty"`
}

type describedColumn struct {
	Name     string      `json:"name"`
	Type     string      `json:"type"`
	Meaning  *meaningRef `json:"meaning,omitempty"`
	Label    *labelView  `json:"label,omitempty"`
	KeyField bool        `json:"-"`
}

type tableRef struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
}

type describedForeignKey struct {
	Columns    []string `json:"columns"`
	RefTable   tableRef `json:"refTable"`
	RefColumns []string `json:"refColumns"`
}

type describedTable struct {
	Schema      string                `json:"schema"`
	Name        string                `json:"name"`
	Kind        string                `json:"kind"`
	Reviewed    bool                  `json:"reviewed"`
	Meanings    []meaningRef          `json:"meanings"`
	PrimaryKey  []string              `json:"primaryKey"`
	Columns     []describedColumn     `json:"columns"`
	ForeignKeys []describedForeignKey `json:"foreignKeys"`
}

type joinEnd struct {
	Table   string   `json:"table"`
	Columns []string `json:"columns"`
}

type describedJoin struct {
	From       joinEnd `json:"from"`
	To         joinEnd `json:"to"`
	Provenance string  `json:"provenance"`
}

type describeResponse struct {
	Source         string           `json:"source"`
	LabelsRevision string           `json:"labelsRevision"`
	Notice         string           `json:"notice"`
	Tables         []describedTable `json:"tables"`
	Joins          []describedJoin  `json:"joins"`
}

var resolveSourceTableHook = resolveSource

// describeTable resolves one object into its described form. Values are
// never read: columns, keys and labels only.
func (m *meaningSource) describeTable(ctx context.Context, o sourceObject, entities datatug.Entities) (describedTable, error) {
	resolved, err := resolveSourceTableHook(ctx, m.projStore, m.projectDir, m.env, m.source, o.Name)
	if err != nil {
		if !o.View || len(o.Columns) == 0 {
			return describedTable{}, err
		}
		// A view the schema reader cannot describe is described from its
		// definition's column names; every column is masked until reviewed.
		resolved = resolvedSource{}
		for _, name := range o.Columns {
			resolved.Columns = append(resolved.Columns, semantic.Column{Name: name})
		}
	}
	t := describedTable{Name: o.Name, Kind: "table", Reviewed: meaninglabels.Reviewed(m.doc, m.source, o.Name),
		Meanings: []meaningRef{}, PrimaryKey: []string{}, Columns: []describedColumn{}, ForeignKeys: []describedForeignKey{}}
	if o.View {
		t.Kind = "view"
	}
	if pk := resolved.Schema.PrimaryKey; pk != nil {
		t.PrimaryKey = append(t.PrimaryKey, pk.Columns...)
	}
	for _, e := range entities {
		if e == nil {
			continue
		}
		for _, k := range e.Tables {
			if strings.EqualFold(k.Ref.Name(), o.Name) {
				t.Meanings = append(t.Meanings, meaningRef{Entity: e.ID, Title: e.Title})
				break
			}
		}
	}
	labels := meaninglabels.EffectiveObject(m.doc, m.source, o.Name, o.View, labelColumns(resolved.Columns))
	byColumn := map[string]semantic.Resolution{}
	for _, res := range semantic.Resolve(entities, m.source, o.Name, resolved.Columns) {
		byColumn[res.Column] = res
	}
	for i, c := range resolved.Columns {
		dc := describedColumn{Name: c.Name, Type: c.Type, Label: toLabelView(labels[i])}
		if res, ok := byColumn[c.Name]; ok && res.Err == nil && res.Entity != "" {
			dc.Meaning = &meaningRef{Entity: res.Entity, Field: res.Field, Provenance: string(res.Provenance)}
			if f := entityField(entities, res.Entity, res.Field); f != nil {
				dc.Meaning.Title = f.Title
				dc.KeyField = f.IsKeyField && res.Provenance == semantic.Declared
			}
		}
		t.Columns = append(t.Columns, dc)
	}
	for _, fk := range resolved.ForeignKeys {
		t.ForeignKeys = append(t.ForeignKeys, describedForeignKey{Columns: fieldNames(fk.Fields),
			RefTable: tableRef{Schema: fk.ReferencedNamespace, Name: fk.ReferencedCollection}, RefColumns: fieldNames(fk.ReferencedFields)})
	}
	return t, nil
}

func entityField(entities datatug.Entities, entity, field string) *datatug.EntityField {
	for _, e := range entities {
		if e == nil || e.ID != entity {
			continue
		}
		for _, f := range e.Fields {
			if f != nil && f.ID == field {
				return f
			}
		}
	}
	return nil
}

// joinsOf lists foreign-key joins and joins implied by two columns declared
// mapped to the same entity key field.
func joinsOf(tables []describedTable) []describedJoin {
	joins := []describedJoin{}
	keys := map[string][]joinEnd{}
	var order []string
	for _, t := range tables {
		for _, fk := range t.ForeignKeys {
			joins = append(joins, describedJoin{From: joinEnd{Table: t.Name, Columns: fk.Columns}, To: joinEnd{Table: fk.RefTable.Name, Columns: fk.RefColumns}, Provenance: "foreign-key"})
		}
		for _, c := range t.Columns {
			if c.KeyField {
				k := c.Meaning.Entity + "." + c.Meaning.Field
				if _, ok := keys[k]; !ok {
					order = append(order, k)
				}
				keys[k] = append(keys[k], joinEnd{Table: t.Name, Columns: []string{c.Name}})
			}
		}
	}
	for _, k := range order {
		ends := keys[k]
		for i := 1; i < len(ends); i++ {
			joins = append(joins, describedJoin{From: ends[i], To: ends[0], Provenance: "meaning"})
		}
	}
	return joins
}

func decodeMeaningBody(w http.ResponseWriter, r *http.Request, into any) bool {
	body, ok := readRecordsBody(w, r)
	if !ok {
		return false
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		writeContractError(w, r, newInvalidRequest("body", "the request body is not the expected JSON"))
		return false
	}
	return true
}

// meaningsDescribeHandler is POST /datatug/meanings/describe.
func meaningsDescribeHandler(w http.ResponseWriter, r *http.Request) {
	var req meaningSourceRequest
	if !decodeMeaningBody(w, r, &req) {
		return
	}
	resp, err := computeDescribe(r.Context(), req)
	writeContractResponse(w, r, err, resp)
}

func computeDescribe(ctx context.Context, req meaningSourceRequest) (describeResponse, error) {
	m, err := openMeaningSource(ctx, req)
	if err != nil {
		return describeResponse{}, err
	}
	objects, err := m.objects(ctx, req.Tables)
	if err != nil {
		return describeResponse{}, err
	}
	entities, err := m.projStore.LoadEntities(ctx)
	if err != nil {
		entities = nil // meanings are an enrichment; labels and schema still answer
	}
	resp := describeResponse{Source: req.Source, LabelsRevision: m.doc.Revision, Notice: localNotice, Tables: []describedTable{}}
	for _, o := range objects {
		t, err := m.describeTable(ctx, o, entities)
		if err != nil {
			log.Printf("meanings/describe: a table could not be described")
			continue
		}
		resp.Tables = append(resp.Tables, t)
	}
	resp.Joins = joinsOf(resp.Tables)
	return resp, nil
}

type findRequest struct {
	Project string `json:"project"`
	Env     string `json:"env"`
	Source  string `json:"source"`
	Text    string `json:"text"`
}

type findMatch struct {
	Table   string      `json:"table"`
	Column  string      `json:"column,omitempty"`
	Type    string      `json:"type,omitempty"`
	Label   *labelView  `json:"label,omitempty"`
	Meaning *meaningRef `json:"meaning,omitempty"`
}

type findResponse struct {
	Matches []findMatch `json:"matches"`
}

// meaningsFindHandler is POST /datatug/meanings/find.
func meaningsFindHandler(w http.ResponseWriter, r *http.Request) {
	var req findRequest
	if !decodeMeaningBody(w, r, &req) {
		return
	}
	resp, err := computeFind(r.Context(), req)
	writeContractResponse(w, r, err, resp)
}

func computeFind(ctx context.Context, req findRequest) (findResponse, error) {
	text := strings.TrimSpace(req.Text)
	if text == "" || len(text) > 200 {
		return findResponse{}, newInvalidRequest("text", "text must be 1 to 200 characters")
	}
	described, err := computeDescribe(ctx, meaningSourceRequest{Project: req.Project, Env: req.Env, Source: req.Source})
	if err != nil {
		return findResponse{}, err
	}
	words := strings.Fields(strings.ToLower(text))
	matches := func(parts ...string) bool {
		hay := strings.ToLower(strings.Join(parts, " "))
		for _, w := range words {
			if !strings.Contains(hay, w) {
				return false
			}
		}
		return true
	}
	resp := findResponse{Matches: []findMatch{}}
	for _, t := range described.Tables {
		var tableWords []string
		tableWords = append(tableWords, t.Name)
		for _, mr := range t.Meanings {
			tableWords = append(tableWords, mr.Entity, mr.Title)
		}
		if matches(tableWords...) {
			resp.Matches = append(resp.Matches, findMatch{Table: t.Name})
		}
		for _, c := range t.Columns {
			parts := append([]string{c.Name}, tableWords...)
			if c.Meaning != nil {
				parts = append(parts, c.Meaning.Entity, c.Meaning.Field, c.Meaning.Title)
			}
			if c.Label != nil {
				parts = append(parts, string(c.Label.Kind))
			}
			own := []string{c.Name}
			if c.Meaning != nil {
				own = append(own, c.Meaning.Field, c.Meaning.Title)
			}
			if c.Label != nil {
				own = append(own, string(c.Label.Kind))
			}
			if matches(parts...) && anyWord(words, own) {
				resp.Matches = append(resp.Matches, findMatch{Table: t.Name, Column: c.Name, Type: c.Type, Label: c.Label, Meaning: c.Meaning})
			}
		}
		if len(resp.Matches) >= maxFindMatches {
			resp.Matches = resp.Matches[:maxFindMatches]
			break
		}
	}
	return resp, nil
}

type whereTerm struct {
	Column string `json:"column"`
	Op     string `json:"op"`
	Value  any    `json:"value"`
}

type orderTerm struct {
	Column string `json:"column"`
	Desc   bool   `json:"desc,omitempty"`
}

type tableReadRequest struct {
	Project string      `json:"project"`
	Env     string      `json:"env"`
	Source  string      `json:"source"`
	Schema  string      `json:"schema"`
	Table   string      `json:"table"`
	Columns []string    `json:"columns,omitempty"`
	Where   []whereTerm `json:"where,omitempty"`
	OrderBy []orderTerm `json:"orderBy,omitempty"`
	Limit   int         `json:"limit,omitempty"`
	Reveal  []string    `json:"reveal,omitempty"`
}

type tableReadResponse struct {
	apicontract.Result
	Protection map[string]*meaninglabels.Protection `json:"protection"`
	Notice     string                               `json:"notice"`
}

// readOperators maps the wire operators; DALgo has no not-equal operator, so
// "!=" is NotIn over one value.
var readOperators = map[string]dal.Operator{"=": dal.Equal, "!=": dal.NotIn, "<": dal.LessThen, "<=": dal.LessOrEqual, ">": dal.GreaterThen, ">=": dal.GreaterOrEqual}

// recordsReadHandler is POST /datatug/records/read: a masked table read.
func recordsReadHandler(w http.ResponseWriter, r *http.Request) {
	var req tableReadRequest
	if !decodeMeaningBody(w, r, &req) {
		return
	}
	resp, err := computeTableRead(r.Context(), req, isAgentRequest(r))
	writeContractResponse(w, r, err, resp)
}

func computeTableRead(ctx context.Context, req tableReadRequest, agent bool) (tableReadResponse, error) {
	if req.Limit == 0 {
		req.Limit = defaultReadLimit
	}
	if req.Limit < 1 || req.Limit > maxReadLimit {
		return tableReadResponse{}, newInvalidRequest("limit", "limit must be 1 to 500")
	}
	m, err := openMeaningSource(ctx, meaningSourceRequest{Project: req.Project, Env: req.Env, Source: req.Source})
	if err != nil {
		return tableReadResponse{}, err
	}
	resolved, collection, executor, err := resolveRecordsTableHook(ctx, recordsTableRequest{Project: req.Project, Env: req.Env, Source: req.Source, Schema: req.Schema, Table: req.Table})
	if err != nil {
		return tableReadResponse{}, err
	}
	labels := meaninglabels.EffectiveObject(m.doc, req.Source, collection, m.isView(ctx, collection), labelColumns(resolved.Columns))
	if len(req.Reveal) == 1 && req.Reveal[0] == "*" {
		if agent {
			return tableReadResponse{}, labelContractError(meaninglabels.CheckReveal(labels, req.Reveal, agent))
		}
		req.Reveal = nil
		for _, l := range labels {
			if l.Revealable() {
				req.Reveal = append(req.Reveal, l.Column)
			}
		}
	}
	if err := meaninglabels.CheckReveal(labels, req.Reveal, agent); err != nil {
		return tableReadResponse{}, labelContractError(err)
	}
	plan := meaninglabels.NewPlan(labels, req.Reveal)
	known := map[string]semantic.Column{}
	for _, c := range resolved.Columns {
		known[strings.ToLower(c.Name)] = c
	}
	requested := req.Columns
	if len(requested) == 0 {
		for _, c := range resolved.Columns {
			requested = append(requested, c.Name)
		}
	}
	var referenced []string
	builder := dal.IQueryBuilder(dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef(collection, ""))))
	for _, w := range req.Where {
		op, ok := readOperators[w.Op]
		if _, exists := known[strings.ToLower(w.Column)]; !ok || !exists {
			return tableReadResponse{}, newInvalidRequest("where", "unknown column or operator")
		}
		referenced = append(referenced, w.Column)
		value := w.Value
		if op == dal.NotIn {
			value = []any{w.Value}
		}
		builder = builder.WhereField(known[strings.ToLower(w.Column)].Name, op, value)
	}
	for _, o := range req.OrderBy {
		c, exists := known[strings.ToLower(o.Column)]
		if !exists {
			return tableReadResponse{}, newInvalidRequest("orderBy", "unknown column")
		}
		referenced = append(referenced, o.Column)
		if o.Desc {
			builder = builder.OrderBy(dal.DescendingField(c.Name))
		} else {
			builder = builder.OrderBy(dal.AscendingField(c.Name))
		}
	}
	if err := plan.CheckReferenced(collection, referenced); err != nil {
		return tableReadResponse{}, labelContractError(err)
	}
	var selected []dal.Column
	order := make([]string, 0, len(requested))
	for _, name := range requested {
		c, exists := known[strings.ToLower(name)]
		if !exists {
			return tableReadResponse{}, newInvalidRequest("columns", "unknown column")
		}
		order = append(order, c.Name)
		if _, masked := plan.IsMasked(c.Name); !masked {
			selected = append(selected, dal.Column{Expression: dal.Field(c.Name)})
		}
	}
	resp := tableReadResponse{Protection: map[string]*meaninglabels.Protection{}, Notice: localNotice}
	for _, name := range order {
		if p := plan.Protection(name); p != nil {
			resp.Protection[name] = p
		}
	}
	var recordset apicontract.Recordset
	if len(selected) > 0 {
		query := builder.Limit(req.Limit).SelectColumns(selected...)
		result, err := runStructuredRelatedHook(executor, ctx, resolved.URL, query, nil)
		if err != nil {
			if errors.Is(err, secureread.ErrAccessDenied) {
				return tableReadResponse{}, contractErrAccessDenied("access denied by policy")
			}
			log.Printf("records/read: the read failed")
			return tableReadResponse{}, unavailableOr(req.Source, err, newContractError(codeInternal, unclassifiedErrorMessage, ""))
		}
		if recordset, err = toContractRecordsetHook(result); err != nil {
			return tableReadResponse{}, newContractError(codeInternal, "the result could not be encoded", "")
		}
		resp.Limitations = toContractLimitations(result.Limitations)
	} else {
		recordset = apicontract.Recordset{Columns: []apicontract.Column{}, Rows: [][]apicontract.TypedValue{}}
		resp.Notice = localNotice + " Every requested column is masked, so no row was read."
	}
	resp.Recordset = withMaskedColumns(recordset, order, known, plan)
	if resp.Limitations == nil {
		resp.Limitations = []apicontract.Limitation{}
	}
	resp.BindingsApplied = []apicontract.Binding{}
	resp.Provenance = apicontract.Provenance{Source: req.Source, Collection: collection, Mode: apicontract.ProvenanceModeLive,
		ObservedAt: nowUTC().Format(time.RFC3339Nano), ExecutionProfile: apicontract.ExecutionProfileProtected}
	if len(req.Reveal) > 0 {
		if err := m.store.Append(meaninglabelsfile.AuditRecord{Event: meaninglabels.Event{Type: "meaning.field.revealed_locally",
			Target: meaninglabels.Target{Source: req.Source, Table: collection}, At: nowUTC()}, Columns: req.Reveal}); err != nil {
			return tableReadResponse{}, newContractError(codeInternal, "the reveal could not be audited, so it was refused", "")
		}
	}
	return resp, nil
}

var resolveRecordsTableHook = resolveRecordsTable

// withMaskedColumns returns the recordset in the requested column order, with
// every masked column present and every one of its cells null.
func withMaskedColumns(rs apicontract.Recordset, order []string, known map[string]semantic.Column, plan meaninglabels.Plan) apicontract.Recordset {
	index := map[string]int{}
	for i, c := range rs.Columns {
		index[c.Name] = i
	}
	out := apicontract.Recordset{Columns: make([]apicontract.Column, 0, len(order)), Rows: make([][]apicontract.TypedValue, len(rs.Rows))}
	for _, name := range order {
		typ := known[strings.ToLower(name)].Type
		if i, ok := index[name]; ok {
			typ = rs.Columns[i].Type
		}
		out.Columns = append(out.Columns, apicontract.Column{Name: name, Type: typ})
	}
	for r, row := range rs.Rows {
		cells := make([]apicontract.TypedValue, 0, len(order))
		for _, name := range order {
			i, ok := index[name]
			if _, masked := plan.IsMasked(name); masked || !ok {
				cells = append(cells, apicontract.NewNullValue())
				continue
			}
			cells = append(cells, row[i])
		}
		out.Rows[r] = cells
	}
	return out
}

type labelsTableView struct {
	Name     string             `json:"name"`
	Kind     string             `json:"kind"`
	Reviewed bool               `json:"reviewed"`
	Columns  []labelsColumnView `json:"columns"`
}

type labelsColumnView struct {
	Name  string     `json:"name"`
	Type  string     `json:"type"`
	Label *labelView `json:"label,omitempty"`
}

type labelsResponse struct {
	Revision string                 `json:"revision"`
	Tables   []labelsTableView      `json:"tables"`
	Stored   meaninglabels.Document `json:"stored"`
}

// meaningsLabelsHandler is POST /datatug/meanings/labels.
func meaningsLabelsHandler(w http.ResponseWriter, r *http.Request) {
	var req meaningSourceRequest
	if !decodeMeaningBody(w, r, &req) {
		return
	}
	described, err := computeDescribe(r.Context(), req)
	if err != nil {
		writeContractError(w, r, err)
		return
	}
	m, err := openMeaningSource(r.Context(), req)
	if err != nil {
		writeContractError(w, r, err)
		return
	}
	resp := labelsResponse{Revision: m.doc.Revision, Tables: []labelsTableView{}, Stored: m.doc}
	for _, t := range described.Tables {
		tv := labelsTableView{Name: t.Name, Kind: t.Kind, Reviewed: t.Reviewed, Columns: []labelsColumnView{}}
		for _, c := range t.Columns {
			tv.Columns = append(tv.Columns, labelsColumnView{Name: c.Name, Type: c.Type, Label: c.Label})
		}
		resp.Tables = append(resp.Tables, tv)
	}
	writeContractResponse(w, r, nil, resp)
}

type labelChangeRequest struct {
	Project    string               `json:"project"`
	Env        string               `json:"env"`
	Source     string               `json:"source"`
	IfRevision string               `json:"ifRevision"`
	Change     meaninglabels.Change `json:"change"`
}

type labelChangeResponse struct {
	Revision string              `json:"revision"`
	Event    meaninglabels.Event `json:"event"`
}

// localActor names the person behind the local agent, or the agent.
var localActor = func(agent bool) meaninglabels.Actor {
	if agent {
		return meaninglabels.Actor{ID: "agent:local", Agent: true, AgentUser: "user:local"}
	}
	return meaninglabels.Actor{ID: "user:local"}
}

// meaningsLabelsChangeHandler is POST /datatug/meanings/labels/change.
func meaningsLabelsChangeHandler(w http.ResponseWriter, r *http.Request) {
	var req labelChangeRequest
	if !decodeMeaningBody(w, r, &req) {
		return
	}
	resp, err := computeLabelChange(r.Context(), req, isAgentRequest(r))
	writeContractResponse(w, r, err, resp)
}

// labelWritesAllowed reports whether serve runs with --allow-writes. Owner
// label changes are project writes; an agent's proposal only tightens and is
// always accepted.
var labelWritesAllowed = func() bool { return api.GetCapabilities().AllowWrites }

func computeLabelChange(ctx context.Context, req labelChangeRequest, agent bool) (labelChangeResponse, error) {
	if !agent && req.Change.Action != meaninglabels.ActionPropose && !labelWritesAllowed() {
		return labelChangeResponse{}, contractErrAccessDenied("owner label changes are project writes: start datatug serve with --allow-writes")
	}
	if req.Change.Target.Source != req.Source {
		return labelChangeResponse{}, newInvalidRequest("change.target.source", "must equal source")
	}
	m, err := openMeaningSource(ctx, meaningSourceRequest{Project: req.Project, Env: req.Env, Source: req.Source})
	if err != nil {
		return labelChangeResponse{}, err
	}
	if req.IfRevision != m.doc.Revision {
		return labelChangeResponse{}, labelContractError(meaninglabels.CheckRevision(m.doc, req.IfRevision))
	}
	doc, event, err := meaninglabels.Apply(m.doc, req.Change, localActor(agent), nowUTC())
	if err != nil {
		return labelChangeResponse{}, labelContractError(err)
	}
	if event.Loosens && req.Change.Action != meaninglabels.ActionClassMark {
		tables, err := m.reportedTables(ctx, req.Change.Target.Table)
		if err != nil {
			return labelChangeResponse{}, err
		}
		if err := meaninglabels.CheckLooseningTarget(m.doc, req.Change.Target, tables); err != nil {
			return labelChangeResponse{}, labelContractError(err)
		}
	}
	if doc.Revision != m.doc.Revision || event.Type != "meaning.label.proposal_ignored" {
		if err := m.store.Save(ctx, doc, m.doc.Revision); err != nil {
			return labelChangeResponse{}, labelContractError(err)
		}
	}
	// The change is saved; an audit write that fails is logged, never silent.
	if err := m.store.Append(meaninglabelsfile.AuditRecord{Event: event}); err != nil {
		log.Printf("meanings/labels/change: the audit record could not be written")
	}
	return labelChangeResponse{Revision: doc.Revision, Event: event}, nil
}

// reportedTables maps the exact names the source reports for table (any
// case, any schema) to their columns, for the exact-target check.
func (m *meaningSource) reportedTables(ctx context.Context, table string) (map[string][]string, error) {
	objects, err := m.objects(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, o := range objects {
		if !meaninglabels.TableMatches(table, o.Name) {
			continue
		}
		resolved, err := resolveSourceTableHook(ctx, m.projStore, m.projectDir, m.env, m.source, o.Name)
		if err != nil {
			return nil, unavailableOr(m.source, err, newSourceUnavailable("the source cannot be read"))
		}
		for _, c := range resolved.Columns {
			out[o.Name] = append(out[o.Name], c.Name)
		}
		if out[o.Name] == nil {
			out[o.Name] = []string{}
		}
	}
	return out, nil
}

// anyWord reports whether any word occurs in any of parts, ignoring case.
func anyWord(words, parts []string) bool {
	hay := strings.ToLower(strings.Join(parts, " "))
	for _, w := range words {
		if strings.Contains(hay, w) {
			return true
		}
	}
	return false
}

// labelContractError maps a label error to the contract envelope.
func labelContractError(err error) error {
	code := meaninglabels.ErrorCode(err)
	if code == "" {
		return err
	}
	var e *meaninglabels.Error
	errors.As(err, &e)
	return newContractError(apicontract.ErrorCode(code), e.Message, "")
}

// labelHTTPStatus is the HTTP status of a Meaning Graph label error code, or 0.
func labelHTTPStatus(code apicontract.ErrorCode) int {
	switch string(code) {
	case meaninglabels.CodeFieldProtected, meaninglabels.CodeRawRefused, meaninglabels.CodeAgentNotAllowed,
		meaninglabels.CodeRevealNotForAgent, meaninglabels.CodeFieldNonRevealable:
		return http.StatusForbidden
	case meaninglabels.CodeRevisionConflict:
		return http.StatusConflict
	}
	if strings.HasPrefix(string(code), "LABEL") {
		return http.StatusBadRequest
	}
	return 0
}

// agentRawRefusal refuses an agent's saved SQL on a source with protected
// columns (spec REQ:raw-and-labels).
func agentRawRefusal(ctx context.Context, project, env, source string) error {
	refused := labelContractError(meaninglabels.RawRefusal(meaninglabels.Document{}, source, nil, true))
	m, err := openMeaningSource(ctx, meaningSourceRequest{Project: project, Env: env, Source: source})
	if err != nil {
		return refused
	}
	if meaninglabels.AnyProtected(m.doc, source, nil) {
		return refused
	}
	if m.registered.Kind != api.SourceKindSQL {
		return refused
	}
	// Every object, with no cap and no policy filter: raw SQL is governed by
	// its own grant, so a table hidden from structured reads still counts.
	objects, err := listSourceObjectsHook(ctx, m.registered.URL)
	if err != nil {
		return refused
	}
	tables := map[string][]meaninglabels.Column{}
	for _, o := range objects {
		var columns []meaninglabels.Column
		if resolved, err := resolveSourceTableHook(ctx, m.projStore, m.projectDir, env, source, o.Name); err == nil {
			columns = labelColumns(resolved.Columns)
		} else {
			for _, name := range o.Columns {
				columns = append(columns, meaninglabels.Column{Name: name})
			}
		}
		if len(columns) == 0 {
			return refused // a table that cannot be described refuses (fail closed)
		}
		tables[o.Name] = columns
		for _, l := range meaninglabels.EffectiveObject(m.doc, source, o.Name, o.View, columns) {
			if l.Protected() {
				return refused
			}
		}
	}
	return labelContractError(meaninglabels.RawRefusal(m.doc, source, tables, true))
}

// String is for log lines only.
func (o sourceObject) String() string { return fmt.Sprintf("%s(view=%v)", o.Name, o.View) }

type agentContextKey struct{}

// withAgentContext marks ctx as serving a declared agent request.
func withAgentContext(ctx context.Context, agent bool) context.Context {
	return context.WithValue(ctx, agentContextKey{}, agent)
}

func agentFromContext(ctx context.Context) bool {
	agent, _ := ctx.Value(agentContextKey{}).(bool)
	return agent
}

// agentTugQLGuard refuses an agent's TugQL read that could disclose a masked
// column: a wildcard select of a table with masked columns, any expression on
// a masked column, or a join or other source shape on a source with protected
// columns (agents use records/read there). Every failure refuses.
func agentTugQLGuard(ctx context.Context, project, env, source string, doc []byte) error {
	query, err := tugql.Deserialize(doc)
	if err != nil {
		return newInvalidRequest("tugql", "the TugQL document cannot be parsed")
	}
	return agentQueryGuard(ctx, project, env, source, query)
}

// agentQueryGuard is agentTugQLGuard for a parsed query.
func agentQueryGuard(ctx context.Context, project, env, source string, query dal.Query) error {
	// A query the policy denies gets the policy's own answer, never a
	// masking one that would confirm what the denied tables hold.
	if executor, configured := secureExecutorHook(); configured {
		if err := executor.Authorize(ctx, query, nil); errors.Is(err, secureread.ErrAccessDenied) {
			return contractErrAccessDenied("access denied by policy")
		}
	}
	structured, ok := query.(dal.StructuredQuery)
	var base dal.CollectionRef
	if ok {
		base, ok = baseCollectionOf(structured)
	}
	// A subquery (exists, in, a derived source) can test values in any
	// table, masked columns included: it is judged like raw SQL.
	if !ok || dal.HasSubquery(structured) {
		return agentRawRefusal(ctx, project, env, source)
	}
	if joins := structured.From().Joins(); len(joins) > 0 {
		return agentJoinGuard(ctx, project, env, source, base, joins)
	}
	m, err := openMeaningSource(ctx, meaningSourceRequest{Project: project, Env: env, Source: source})
	if err != nil {
		return err
	}
	resolved, err := resolveSourceTableHook(ctx, m.projStore, m.projectDir, env, source, base.Name())
	if err != nil {
		return labelContractError(meaninglabels.RawRefusal(m.doc, source, nil, true))
	}
	labels := meaninglabels.EffectiveObject(m.doc, source, base.Name(), m.isView(ctx, base.Name()), labelColumns(resolved.Columns))
	plan := meaninglabels.NewPlan(labels, nil)
	if plan.Empty() {
		return nil
	}
	if len(structured.Columns()) == 0 {
		return newContractError(meaninglabels.CodeFieldProtected, fmt.Sprintf("%s has masked columns (%s); select columns explicitly or use records/read", base.Name(), strings.Join(plan.Columns(), ", ")), "")
	}
	referenced, err := accesspolicies.ReferencedFields(structured)
	if err != nil {
		return newContractError(meaninglabels.CodeFieldProtected, "the query uses an expression the masking gate cannot inspect", "")
	}
	for _, c := range structured.Columns() {
		if c.Wildcard != nil {
			return newContractError(meaninglabels.CodeFieldProtected, fmt.Sprintf("%s has masked columns (%s); a wildcard projection may include them, so name each column", base.Name(), strings.Join(plan.Columns(), ", ")), "")
		}
		if c.Alias != "" {
			referenced = append(referenced, c.Alias)
		}
	}
	return labelContractError(plan.CheckReferenced(base.Name(), referenced))
}

func baseCollectionOf(query dal.StructuredQuery) (dal.CollectionRef, bool) {
	return collectionOf(query.From().Base())
}

// schemaColumnLabels is the effective labels of one table's columns for the
// records/schema answer. A label document that cannot be read refuses the
// answer: the edge fails closed, never treating the source as unlabelled.
func schemaColumnLabels(ctx context.Context, req recordsTableRequest, collection string, columns []semantic.Column) ([]meaninglabels.ColumnLabel, error) {
	m, err := openMeaningSource(ctx, meaningSourceRequest{Project: req.Project, Env: req.Env, Source: req.Source})
	if err != nil {
		return nil, err
	}
	return meaninglabels.EffectiveObject(m.doc, req.Source, collection, m.isView(ctx, collection), labelColumns(columns)), nil
}

// refuseAgentRows guards a route that returns rows without Meaning Graph
// masking: a declared agent is refused there and pointed at the masking
// routes (records/read, TugQL through exec/run_query).
func refuseAgentRows(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if isAgentRequest(r) {
			writeContractError(w, r, newContractError(meaninglabels.CodeRawRefused, "this route returns rows without field masking; agents read rows through records/read or TugQL", ""))
			return
		}
		next(w, r)
	}
}

// agentJoinGuard refuses an agent's TugQL join when any table it touches has
// a protected column, or when one of them cannot be described. The policy
// has already admitted the query, so only these tables matter.
func agentJoinGuard(ctx context.Context, project, env, source string, base dal.CollectionRef, joins []dal.JoinedSource) error {
	m, err := openMeaningSource(ctx, meaningSourceRequest{Project: project, Env: env, Source: source})
	if err != nil {
		return err
	}
	names := []string{base.Name()}
	for _, j := range joins {
		ref, ok := collectionOf(j.RecordsetSource)
		if !ok {
			return labelContractError(meaninglabels.RawRefusal(m.doc, source, nil, true))
		}
		names = append(names, ref.Name())
	}
	for _, name := range names {
		resolved, err := resolveSourceTableHook(ctx, m.projStore, m.projectDir, env, source, name)
		if err != nil || len(resolved.Columns) == 0 {
			return labelContractError(meaninglabels.RawRefusal(m.doc, source, nil, true))
		}
		for _, l := range meaninglabels.EffectiveObject(m.doc, source, name, m.isView(ctx, name), labelColumns(resolved.Columns)) {
			if l.Protected() {
				return newContractError(meaninglabels.CodeFieldProtected, fmt.Sprintf("%s has masked columns; agents join labelled tables through separate records/read calls", name), "")
			}
		}
	}
	return nil
}

func collectionOf(source dal.RecordsetSource) (dal.CollectionRef, bool) {
	switch s := source.(type) {
	case dal.CollectionRef:
		return s, true
	case *dal.CollectionRef:
		return *s, true
	}
	return dal.CollectionRef{}, false
}
