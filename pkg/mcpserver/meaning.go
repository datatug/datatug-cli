package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/meaninglabels"
)

// Meaning Graph sensitivity: the local agent routes meanings/describe,
// meanings/find, records/read, meanings/labels and meanings/labels/change.
// The exported methods serve both the MCP tools and `datatug meaning`, so there
// is one HTTP client. Metadata strings are redacted with safeText; cell values
// never are.

const (
	maxNames      = 256
	maxConditions = 32
	maxReason     = 1000
)

// MeaningScope names one source and, optionally, the tables to narrow to.
type MeaningScope struct {
	Project     string
	Environment string
	Source      string
	Tables      []string
}

// EffectiveLabel is a column's effective sensitivity label.
type EffectiveLabel struct {
	Class         string `json:"class,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Origin        string `json:"origin,omitempty"`
	State         string `json:"state,omitempty"`
	Rule          string `json:"rule,omitempty"`
	Revealable    bool   `json:"revealable"`
	NonRevealable bool   `json:"nonRevealable"`
}

// FieldMeaning is the semantic-layer meaning a table or column maps to.
type FieldMeaning struct {
	Entity      string `json:"entity,omitempty"`
	Field       string `json:"field,omitempty"`
	Title       string `json:"title,omitempty"`
	Provenance  string `json:"provenance,omitempty"`
	Description string `json:"description,omitempty"`
}

// MeaningColumn is one column of a described table.
type MeaningColumn struct {
	Name     string          `json:"name"`
	Type     string          `json:"type"`
	Nullable *bool           `json:"nullable,omitempty"`
	Meaning  *FieldMeaning   `json:"meaning,omitempty"`
	Label    *EffectiveLabel `json:"label,omitempty"`
}

// TableRef names a table.
type TableRef struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
}

// ForeignKey is a declared foreign key of a described table.
type ForeignKey struct {
	Columns    []string `json:"columns"`
	RefTable   TableRef `json:"refTable"`
	RefColumns []string `json:"refColumns"`
}

// MeaningTable is one table or view of a described source.
type MeaningTable struct {
	Schema      string          `json:"schema"`
	Name        string          `json:"name"`
	Kind        string          `json:"kind"`
	Reviewed    bool            `json:"reviewed"`
	Meanings    []FieldMeaning  `json:"meanings"`
	PrimaryKey  []string        `json:"primaryKey"`
	Columns     []MeaningColumn `json:"columns"`
	ForeignKeys []ForeignKey    `json:"foreignKeys"`
}

// JoinEnd is one side of a join.
type JoinEnd struct {
	Table   string   `json:"table"`
	Columns []string `json:"columns"`
}

// MeaningJoin is a join implied by a foreign key or by shared meaning.
type MeaningJoin struct {
	From       JoinEnd `json:"from"`
	To         JoinEnd `json:"to"`
	Provenance string  `json:"provenance"`
}

// MeaningDescription is the meanings/describe answer. It contains no values.
type MeaningDescription struct {
	Source         string         `json:"source"`
	LabelsRevision string         `json:"labelsRevision"`
	Notice         string         `json:"notice,omitempty"`
	Tables         []MeaningTable `json:"tables"`
	Joins          []MeaningJoin  `json:"joins"`
}

// MeaningMatch is one meanings/find match.
type MeaningMatch struct {
	Table   string          `json:"table"`
	Column  string          `json:"column,omitempty"`
	Type    string          `json:"type,omitempty"`
	Label   *EffectiveLabel `json:"label,omitempty"`
	Meaning *FieldMeaning   `json:"meaning,omitempty"`
}

// MeaningMatches is the meanings/find answer.
type MeaningMatches struct {
	Matches []MeaningMatch `json:"matches"`
}

// Condition is one structured-read filter; Value is a JSON string, number or
// boolean.
type Condition struct {
	Column string `json:"column" jsonschema:"Column name from datatug_describe_meaning; a masked column is refused"`
	Op     string `json:"op" jsonschema:"One of =, !=, <, <=, >, >="`
	Value  any    `json:"value" jsonschema:"A JSON string, number or boolean"`
}

// Order is one structured-read sort key.
type Order struct {
	Column string `json:"column" jsonschema:"Column name; a masked column is refused"`
	Desc   bool   `json:"desc,omitempty" jsonschema:"Sort descending"`
}

// TableReadRequest is a structured table read through the masking boundary.
type TableReadRequest struct {
	Project     string
	Environment string
	Source      string
	Schema      string
	Table       string
	Columns     []string
	Where       []Condition
	OrderBy     []Order
	Limit       int
	// Reveal names revealable columns a person reveals for this read only
	// ("*" for every revealable column). The agent refuses it for an agent.
	Reveal []string
}

// RawRecordset keeps every cell exactly as the agent returned it. A masked
// column's cells are null.
type RawRecordset struct {
	Columns []apicontract.Column `json:"columns"`
	Rows    [][]json.RawMessage  `json:"rows"`
}

// TableRead is the records/read answer.
type TableRead struct {
	Recordset   RawRecordset                        `json:"recordset"`
	Limitations []apicontract.Limitation            `json:"limitations"`
	Provenance  apicontract.Provenance              `json:"provenance"`
	Truncated   bool                                `json:"truncated"`
	Protection  map[string]meaninglabels.Protection `json:"protection,omitempty"`
	Notice      string                              `json:"notice,omitempty"`
}

// LabelsColumn is one column in the meanings/labels answer.
type LabelsColumn struct {
	Name  string          `json:"name"`
	Type  string          `json:"type"`
	Label *EffectiveLabel `json:"label,omitempty"`
}

// LabelsTable is one table in the meanings/labels answer.
type LabelsTable struct {
	Schema   string         `json:"schema"`
	Name     string         `json:"name"`
	Kind     string         `json:"kind"`
	Reviewed bool           `json:"reviewed"`
	Columns  []LabelsColumn `json:"columns"`
}

// LabelsList is the meanings/labels answer.
type LabelsList struct {
	Revision string                 `json:"revision"`
	Tables   []LabelsTable          `json:"tables"`
	Stored   meaninglabels.Document `json:"stored"`
}

// LabelChangeResult is the meanings/labels/change answer.
type LabelChangeResult struct {
	Revision string              `json:"revision"`
	Event    meaninglabels.Event `json:"event"`
}

type scopeBody struct {
	Project string   `json:"project"`
	Env     string   `json:"env"`
	Source  string   `json:"source"`
	Tables  []string `json:"tables,omitempty"`
}

// name is a table, schema or column name the source reports: free-form, but
// bounded and free of control characters.
func name(value string, optional bool) bool {
	if value == "" {
		return optional
	}
	return len(value) <= 256 && printable(value)
}

func printable(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func names(values []string) bool {
	if len(values) > maxNames {
		return false
	}
	for _, v := range values {
		if !name(v, false) {
			return false
		}
	}
	return true
}

func (s MeaningScope) body() (scopeBody, error) {
	if !validSource(sourceInput{s.Project, s.Environment, s.Source}) || !names(s.Tables) {
		return scopeBody{}, errors.New("INVALID_REQUEST: use discovered project, environment, source and table names")
	}
	return scopeBody{s.Project, s.Environment, s.Source, s.Tables}, nil
}

func safeLabel(l *EffectiveLabel) *EffectiveLabel {
	if l == nil {
		return nil
	}
	out := *l
	out.Class, out.Kind, out.Origin, out.State, out.Rule = safeText(l.Class), safeText(l.Kind), safeText(l.Origin), safeText(l.State), safeText(l.Rule)
	return &out
}

func safeMeaning(m *FieldMeaning) *FieldMeaning {
	if m == nil {
		return nil
	}
	return &FieldMeaning{safeText(m.Entity), safeText(m.Field), safeText(m.Title), safeText(m.Provenance), safeText(m.Description)}
}

func safeProtection(p meaninglabels.Protection) meaninglabels.Protection {
	p.Class = meaninglabels.Class(safeText(string(p.Class)))
	p.Kind = meaninglabels.Kind(safeText(string(p.Kind)))
	p.State, p.Origin, p.Ask = safeText(p.State), safeText(p.Origin), safeText(p.Ask)
	return p
}

// DescribeMeaning answers what a source's tables and columns mean, how they
// join and which are sensitive, in one call. It never returns values.
func (c *Client) DescribeMeaning(ctx context.Context, scope MeaningScope) (MeaningDescription, error) {
	var out MeaningDescription
	body, err := scope.body()
	if err != nil {
		return out, err
	}
	if err := c.request(ctx, "meanings/describe", nil, body, &out); err != nil {
		return MeaningDescription{}, err
	}
	out.Source, out.LabelsRevision, out.Notice = safeText(out.Source), safeText(out.LabelsRevision), safeText(out.Notice)
	if out.Tables == nil {
		out.Tables = []MeaningTable{}
	}
	if out.Joins == nil {
		out.Joins = []MeaningJoin{}
	}
	for i := range out.Tables {
		t := &out.Tables[i]
		t.Schema, t.Name, t.Kind = safeText(t.Schema), safeText(t.Name), safeText(t.Kind)
		if t.Meanings == nil {
			t.Meanings = []FieldMeaning{}
		}
		for j := range t.Meanings {
			t.Meanings[j] = *safeMeaning(&t.Meanings[j])
		}
		t.PrimaryKey = safeTexts(t.PrimaryKey)
		for j := range t.Columns {
			col := &t.Columns[j]
			col.Name, col.Type = safeText(col.Name), safeText(col.Type)
			col.Meaning, col.Label = safeMeaning(col.Meaning), safeLabel(col.Label)
		}
		for j := range t.ForeignKeys {
			fk := &t.ForeignKeys[j]
			fk.Columns, fk.RefColumns = safeTexts(fk.Columns), safeTexts(fk.RefColumns)
			fk.RefTable = TableRef{safeText(fk.RefTable.Schema), safeText(fk.RefTable.Name)}
		}
	}
	for i := range out.Joins {
		j := &out.Joins[i]
		j.From = JoinEnd{safeText(j.From.Table), safeTexts(j.From.Columns)}
		j.To = JoinEnd{safeText(j.To.Table), safeTexts(j.To.Columns)}
		j.Provenance = safeText(j.Provenance)
	}
	return out, nil
}

// FindMeaning finds the tables and columns whose names, entities or fields
// match text (1–200 characters).
func (c *Client) FindMeaning(ctx context.Context, scope MeaningScope, text string) (MeaningMatches, error) {
	var out MeaningMatches
	body, err := scope.body()
	if err != nil {
		return out, err
	}
	if n := utf8.RuneCountInString(text); n < 1 || n > 200 || !printable(text) {
		return out, errors.New("INVALID_REQUEST: text must be 1 to 200 characters")
	}
	request := struct {
		Project string `json:"project"`
		Env     string `json:"env"`
		Source  string `json:"source"`
		Text    string `json:"text"`
	}{body.Project, body.Env, body.Source, text}
	if err := c.request(ctx, "meanings/find", nil, request, &out); err != nil {
		return MeaningMatches{}, err
	}
	if out.Matches == nil {
		out.Matches = []MeaningMatch{}
	}
	for i := range out.Matches {
		m := &out.Matches[i]
		m.Table, m.Column, m.Type = safeText(m.Table), safeText(m.Column), safeText(m.Type)
		m.Label, m.Meaning = safeLabel(m.Label), safeMeaning(m.Meaning)
	}
	return out, nil
}

var readOps = map[string]bool{"=": true, "!=": true, "<": true, "<=": true, ">": true, ">=": true}

func scalar(value any) bool {
	switch value.(type) {
	case string, float64, bool, json.Number, int, int64:
		return true
	}
	return false
}

// ReadTable reads one table through the agent's structured read boundary:
// labelled columns come back masked (null cells) with their protection.
func (c *Client) ReadTable(ctx context.Context, in TableReadRequest) (TableRead, error) {
	var out TableRead
	if !validSource(sourceInput{in.Project, in.Environment, in.Source}) || !name(in.Table, false) || !name(in.Schema, true) || !names(in.Columns) || !reveals(in.Reveal) || len(in.Where) > maxConditions || len(in.OrderBy) > maxConditions || in.Limit < 0 || in.Limit > 500 {
		return out, errors.New("INVALID_REQUEST: use discovered IDs and names, at most 32 conditions and a limit from 1 to 500")
	}
	for _, w := range in.Where {
		if !name(w.Column, false) || !readOps[w.Op] || !scalar(w.Value) {
			return out, errors.New("INVALID_REQUEST: each where condition needs a column, an op of =, !=, <, <=, >, >= and a string, number or boolean value")
		}
	}
	for _, o := range in.OrderBy {
		if !name(o.Column, false) {
			return out, errors.New("INVALID_REQUEST: each orderBy entry needs a column")
		}
	}
	limit := in.Limit
	if limit == 0 {
		limit = 100
	}
	request := struct {
		Project string      `json:"project"`
		Env     string      `json:"env"`
		Source  string      `json:"source"`
		Schema  string      `json:"schema"`
		Table   string      `json:"table"`
		Columns []string    `json:"columns,omitempty"`
		Where   []Condition `json:"where,omitempty"`
		OrderBy []Order     `json:"orderBy,omitempty"`
		Limit   int         `json:"limit"`
		Reveal  []string    `json:"reveal,omitempty"`
	}{in.Project, in.Environment, in.Source, in.Schema, in.Table, in.Columns, in.Where, in.OrderBy, limit, in.Reveal}
	if err := c.request(ctx, "records/read", nil, request, &out); err != nil {
		return TableRead{}, err
	}
	if len(out.Recordset.Rows) > limit {
		return TableRead{}, errors.New("local agent returned an invalid table read")
	}
	for _, row := range out.Recordset.Rows {
		if len(row) != len(out.Recordset.Columns) {
			return TableRead{}, errors.New("local agent returned an invalid table read")
		}
	}
	if out.Recordset.Rows == nil {
		out.Recordset.Rows = [][]json.RawMessage{}
	}
	if out.Limitations == nil {
		out.Limitations = []apicontract.Limitation{}
	}
	for i := range out.Recordset.Columns {
		out.Recordset.Columns[i].Name = safeText(out.Recordset.Columns[i].Name)
		out.Recordset.Columns[i].Type = safeText(out.Recordset.Columns[i].Type)
	}
	for i := range out.Limitations {
		out.Limitations[i].Policy = safeText(out.Limitations[i].Policy)
		out.Limitations[i].HiddenColumns = safeTexts(out.Limitations[i].HiddenColumns)
	}
	p := &out.Provenance
	p.Source, p.Collection, p.QueryID, p.SnapshotID = safeText(p.Source), safeText(p.Collection), safeText(p.QueryID), safeText(p.SnapshotID)
	if out.Protection != nil {
		protection := make(map[string]meaninglabels.Protection, len(out.Protection))
		for column, item := range out.Protection {
			protection[safeText(column)] = safeProtection(item)
		}
		out.Protection = protection
	}
	out.Notice = safeText(out.Notice)
	return out, nil
}

// MeaningLabels lists every effective label of a source, with the stored
// label document and its revision.
func (c *Client) MeaningLabels(ctx context.Context, scope MeaningScope) (LabelsList, error) {
	var out LabelsList
	body, err := scope.body()
	if err != nil {
		return out, err
	}
	if err := c.request(ctx, "meanings/labels", nil, body, &out); err != nil {
		return LabelsList{}, err
	}
	out.Revision = safeText(out.Revision)
	if out.Tables == nil {
		out.Tables = []LabelsTable{}
	}
	for i := range out.Tables {
		t := &out.Tables[i]
		t.Schema, t.Name, t.Kind = safeText(t.Schema), safeText(t.Name), safeText(t.Kind)
		for j := range t.Columns {
			col := &t.Columns[j]
			col.Name, col.Type, col.Label = safeText(col.Name), safeText(col.Type), safeLabel(col.Label)
		}
	}
	return out, nil
}

// ChangeLabel applies one label change at ifRevision. With an agent context
// the local agent accepts only propose.
func (c *Client) ChangeLabel(ctx context.Context, scope MeaningScope, ifRevision string, change meaninglabels.Change) (LabelChangeResult, error) {
	var out LabelChangeResult
	body, err := scope.body()
	if err != nil {
		return out, err
	}
	change.Target.Source = scope.Source
	if !name(change.Target.Table, change.Action == meaninglabels.ActionClassMark) || !name(change.Target.Column, true) || len(change.Reason) > maxReason || len(ifRevision) > 256 {
		return out, errors.New("INVALID_REQUEST: a label change needs a table name and a reason of at most 1000 bytes")
	}
	request := struct {
		Project    string               `json:"project"`
		Env        string               `json:"env"`
		Source     string               `json:"source"`
		IfRevision string               `json:"ifRevision"`
		Change     meaninglabels.Change `json:"change"`
	}{body.Project, body.Env, body.Source, ifRevision, change}
	if err := c.request(ctx, "meanings/labels/change", nil, request, &out); err != nil {
		return LabelChangeResult{}, err
	}
	return out, nil
}

// MCP tool inputs and handlers.

type describeMeaningInput struct {
	Project     string   `json:"project" jsonschema:"Discovered project ID"`
	Environment string   `json:"environment" jsonschema:"Discovered environment ID"`
	Source      string   `json:"source" jsonschema:"Discovered source ID"`
	Tables      []string `json:"tables,omitempty" jsonschema:"Optional table names to narrow the answer to; compared case-insensitively"`
}

type findMeaningInput struct {
	Project     string `json:"project" jsonschema:"Discovered project ID"`
	Environment string `json:"environment" jsonschema:"Discovered environment ID"`
	Source      string `json:"source" jsonschema:"Discovered source ID"`
	Text        string `json:"text" jsonschema:"Text to find in table, column, entity and field names and titles; 1 to 200 characters"`
}

type readTableInput struct {
	Project     string      `json:"project" jsonschema:"Discovered project ID"`
	Environment string      `json:"environment" jsonschema:"Discovered environment ID"`
	Source      string      `json:"source" jsonschema:"Discovered source ID"`
	Schema      string      `json:"schema,omitempty" jsonschema:"Schema name; empty for SQLite"`
	Table       string      `json:"table" jsonschema:"Table name from datatug_describe_meaning"`
	Columns     []string    `json:"columns,omitempty" jsonschema:"Only the columns you need; all columns when omitted"`
	Where       []Condition `json:"where,omitempty" jsonschema:"Filters combined with AND"`
	OrderBy     []Order     `json:"orderBy,omitempty" jsonschema:"Sort keys"`
	Limit       int         `json:"limit,omitempty" jsonschema:"Returned row limit from 1 to 500; default 100"`
}

type proposeLabelInput struct {
	Project     string `json:"project" jsonschema:"Discovered project ID"`
	Environment string `json:"environment" jsonschema:"Discovered environment ID"`
	Source      string `json:"source" jsonschema:"Discovered source ID"`
	Table       string `json:"table" jsonschema:"Table name the source reports"`
	Column      string `json:"column,omitempty" jsonschema:"Column name; omit to propose a label for the whole table"`
	Class       string `json:"class" jsonschema:"secret, financial or pii"`
	Kind        string `json:"kind,omitempty" jsonschema:"Optional kind of the class, such as email or card-number"`
	Reason      string `json:"reason" jsonschema:"Why the field is sensitive"`
}

func (c *Client) describeMeaning(ctx context.Context, in describeMeaningInput) (any, error) {
	return c.DescribeMeaning(ctx, MeaningScope(in))
}

func (c *Client) findMeaning(ctx context.Context, in findMeaningInput) (any, error) {
	return c.FindMeaning(ctx, MeaningScope{Project: in.Project, Environment: in.Environment, Source: in.Source}, in.Text)
}

func (c *Client) readTable(ctx context.Context, in readTableInput) (any, error) {
	return c.ReadTable(ctx, TableReadRequest{Project: in.Project, Environment: in.Environment, Source: in.Source, Schema: in.Schema, Table: in.Table, Columns: in.Columns, Where: in.Where, OrderBy: in.OrderBy, Limit: in.Limit})
}

func (c *Client) proposeLabel(ctx context.Context, in proposeLabelInput) (any, error) {
	class, kind := meaninglabels.Class(in.Class), meaninglabels.Kind(in.Kind)
	if err := meaninglabels.ValidateClassKind(class, kind); err != nil || !class.Protected() {
		return nil, errors.New("INVALID_REQUEST: class must be secret, financial or pii, and kind must belong to it")
	}
	if strings.TrimSpace(in.Reason) == "" {
		return nil, errors.New("INVALID_REQUEST: a proposal needs a reason")
	}
	scope := MeaningScope{in.Project, in.Environment, in.Source, []string{in.Table}}
	labels, err := c.MeaningLabels(ctx, scope)
	if err != nil {
		return nil, err
	}
	change := meaninglabels.Change{Action: meaninglabels.ActionPropose, Target: meaninglabels.Target{Table: in.Table, Column: in.Column}, Class: class, Kind: kind, Reason: in.Reason}
	return c.ChangeLabel(ctx, scope, labels.Revision, change)
}

// reveals accepts column names or the single "*" for every revealable column.
func reveals(list []string) bool {
	if len(list) == 1 && list[0] == "*" {
		return true
	}
	for _, v := range list {
		if v == "*" {
			return false
		}
	}
	return names(list)
}
