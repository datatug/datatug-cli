package mcpserver

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strconv"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/queryparams"
	"github.com/datatug/datatug-cli/pkg/querywrite"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
)

type noInput struct{}
type projectInput struct {
	Project string `json:"project" jsonschema:"Project ID returned by datatug_list_projects"`
}
type queryInput struct {
	Project string `json:"project" jsonschema:"Discovered project ID"`
	QueryID string `json:"queryId" jsonschema:"Saved SQL query ID returned by datatug_list_queries, including its folder"`
}
type sourceInput struct {
	Project     string `json:"project" jsonschema:"Discovered project ID"`
	Environment string `json:"environment" jsonschema:"Environment ID from datatug_describe_project"`
	Source      string `json:"source" jsonschema:"Source ID from datatug_describe_project; never a path or URL"`
}
type tableInput struct {
	Project     string `json:"project" jsonschema:"Discovered project ID"`
	Environment string `json:"environment" jsonschema:"Discovered environment ID"`
	Source      string `json:"source" jsonschema:"Discovered source ID"`
	Schema      string `json:"schema" jsonschema:"Schema name from datatug_list_tables; empty for SQLite"`
	Table       string `json:"table" jsonschema:"Table name from datatug_list_tables or an existing known table"`
}
type runInput struct {
	Project     string            `json:"project" jsonschema:"Discovered project ID"`
	Environment string            `json:"environment" jsonschema:"Discovered environment ID"`
	Source      string            `json:"source" jsonschema:"Discovered source ID"`
	QueryID     string            `json:"queryId" jsonschema:"Saved SQL query ID from datatug_list_queries"`
	Parameters  map[string]string `json:"parameters" jsonschema:"Declared parameter IDs mapped to text values. Integer and decimal values use exact decimal text; boolean values use true or false. Strings are bound literally, never interpolated into SQL."`
	Limit       int               `json:"limit,omitempty" jsonschema:"Returned row limit from 1 to 500; default 100. Does not bound database work."`
}

type named struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}
type parameter struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Required   bool   `json:"required"`
	MultiValue bool   `json:"multiValue,omitempty"`
}
type query struct {
	ID         string      `json:"id"`
	Title      string      `json:"title,omitempty"`
	Type       string      `json:"type"`
	Text       string      `json:"text,omitempty"`
	Parameters []parameter `json:"parameters"`
}
type environment struct {
	ID      string   `json:"id"`
	Sources []string `json:"sources"`
}
type projectDescription struct {
	ID           string        `json:"id"`
	Environments []environment `json:"environments"`
	Entities     []named       `json:"entities"`
}

// safeText is for untrusted metadata, never for authorized result cell values.
func safeText(text string) string {
	if _, found := querywrite.CredentialReason(text); found {
		return "[redacted metadata]"
	}
	return dbcopy.RedactText(text)
}

func (c *Client) info(ctx context.Context) (apicontract.AgentInfo, error) {
	var info apicontract.AgentInfo
	err := c.request(ctx, "agent-info", nil, nil, &info)
	if err == nil && info.Validate() != nil {
		err = errors.New("local agent returned invalid session metadata")
	}
	return info, err
}

func (c *Client) agentInfo(ctx context.Context, _ noInput) (any, error) {
	info, err := c.info(ctx)
	if err != nil {
		return nil, err
	}
	// The securityContextId is used internally, not accepted from tool inputs.
	return struct {
		Version      string                        `json:"version"`
		Principal    apicontract.AgentPrincipal    `json:"principal"`
		Capabilities apicontract.AgentCapabilities `json:"capabilities"`
	}{safeText(info.Version), apicontract.AgentPrincipal{ID: safeText(info.Principal.ID), Roles: safeTexts(info.Principal.Roles), Groups: safeTexts(info.Principal.Groups)}, info.Capabilities}, nil
}

func safeTexts(values []string) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = safeText(value)
	}
	return out
}

func (c *Client) listProjects(ctx context.Context, _ noInput) (any, error) {
	info, err := c.info(ctx)
	if err != nil {
		return nil, err
	}
	projects := []named{}
	for _, project := range info.Projects {
		if identifier(project.ID) {
			projects = append(projects, named{ID: project.ID})
		}
	}
	return struct {
		Projects []named `json:"projects"`
	}{projects}, nil
}

func (c *Client) describeProject(ctx context.Context, input projectInput) (any, error) {
	if !identifier(input.Project) {
		return nil, errors.New("INVALID_REQUEST: use a discovered project ID")
	}
	// Decode a closed projection. Full project DTOs contain connection profiles,
	// server hosts, paths and repository data that must never reach the model.
	var raw struct {
		Environments []struct {
			ID      string `json:"id"`
			Servers []struct {
				Catalogs []string `json:"catalogs"`
			} `json:"dbServers"`
		} `json:"environments"`
		Entities []named `json:"entities"`
		Models   []struct {
			Environments []struct {
				ID       string  `json:"id"`
				Catalogs []named `json:"DbCatalogs"`
			} `json:"environments"`
		} `json:"dbModels"`
	}
	if err := c.request(ctx, "projects/project_full", url.Values{"id": {input.Project}}, nil, &raw); err != nil {
		return nil, err
	}
	out := projectDescription{ID: input.Project, Environments: []environment{}, Entities: []named{}}
	sources := map[string]map[string]bool{}
	ensure := func(id string) map[string]bool {
		if sources[id] == nil {
			sources[id] = map[string]bool{}
		}
		return sources[id]
	}
	for _, env := range raw.Environments {
		if !identifier(env.ID) {
			continue
		}
		ids := ensure(env.ID)
		for _, server := range env.Servers {
			for _, source := range server.Catalogs {
				if identifier(source) {
					ids[source] = true
				}
			}
		}
	}
	for _, model := range raw.Models {
		for _, env := range model.Environments {
			if !identifier(env.ID) {
				continue
			}
			ids := ensure(env.ID)
			for _, source := range env.Catalogs {
				if identifier(source.ID) {
					ids[source.ID] = true
				}
			}
		}
	}
	for id, ids := range sources {
		env := environment{ID: id, Sources: []string{}}
		for source := range ids {
			env.Sources = append(env.Sources, source)
		}
		sort.Strings(env.Sources)
		out.Environments = append(out.Environments, env)
	}
	sort.Slice(out.Environments, func(i, j int) bool { return out.Environments[i].ID < out.Environments[j].ID })
	for _, entity := range raw.Entities {
		if identifier(entity.ID) {
			out.Entities = append(out.Entities, named{ID: entity.ID, Title: safeText(entity.Title)})
		}
	}
	return out, nil
}

func publicQuery(q *datatug.QueryDef, id string, withText bool) (query, error) {
	if q == nil || q.Type != datatug.QueryTypeSQL {
		return query{}, errors.New("UNSUPPORTED_QUERY: this MCP release runs saved SQL queries only")
	}
	if !queryIdentifier(id) {
		return query{}, errors.New("INVALID_REQUEST: saved query has an invalid ID")
	}
	if _, _, found := querywrite.QueryCredentialReason(q); found {
		return query{}, errors.New("UNSAFE_METADATA: saved query contains embedded connection credentials")
	}
	out := query{ID: id, Title: safeText(q.Title), Type: "SQL", Parameters: []parameter{}}
	if withText {
		out.Text = q.Text
	}
	for _, p := range q.Parameters {
		if !identifier(p.ID) || p.IsCredential {
			return query{}, errors.New("UNSAFE_METADATA: saved query has an unsupported credential parameter")
		}
		out.Parameters = append(out.Parameters, parameter{ID: p.ID, Type: safeText(p.Type), Required: p.IsRequired, MultiValue: p.IsMultiValue})
	}
	return out, nil
}

func (c *Client) listQueries(ctx context.Context, input projectInput) (any, error) {
	if !identifier(input.Project) {
		return nil, errors.New("INVALID_REQUEST: use a discovered project ID")
	}
	var root datatug.QueriesFolder
	if err := c.request(ctx, "queries/all_queries", url.Values{"project": {input.Project}}, nil, &root); err != nil {
		return nil, err
	}
	out := []query{}
	var walk func(*datatug.QueriesFolder, string, int) error
	walk = func(folder *datatug.QueriesFolder, prefix string, depth int) error {
		if depth > 32 {
			return errors.New("query folder tree is too deep")
		}
		for _, q := range folder.Items {
			if q == nil || q.Type != datatug.QueryTypeSQL {
				continue
			}
			item, err := publicQuery(q, prefix+q.ID, false)
			if err == nil {
				out = append(out, item)
			}
		}
		for _, child := range folder.Folders {
			if child != nil && identifier(child.ID) {
				if err := walk(child, prefix+child.ID+"/", depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(&root, "", 0); err != nil {
		return nil, err
	}
	return struct {
		Queries []query `json:"queries"`
	}{out}, nil
}

func (c *Client) loadQuery(ctx context.Context, input queryInput) (*datatug.QueryDefWithFolderPath, error) {
	if !identifier(input.Project) || !queryIdentifier(input.QueryID) {
		return nil, errors.New("INVALID_REQUEST: use discovered project and query IDs")
	}
	var q datatug.QueryDefWithFolderPath
	if err := c.request(ctx, "queries/get_query", url.Values{"project": {input.Project}, "query": {input.QueryID}}, nil, &q); err != nil {
		return nil, err
	}
	if _, err := publicQuery(&q.QueryDef, input.QueryID, true); err != nil {
		return nil, err
	}
	return &q, nil
}

func (c *Client) getQuery(ctx context.Context, input queryInput) (any, error) {
	q, err := c.loadQuery(ctx, input)
	if err != nil {
		return nil, err
	}
	return publicQuery(&q.QueryDef, input.QueryID, true)
}

func validSource(input sourceInput) bool {
	return identifier(input.Project) && identifier(input.Environment) && identifier(input.Source)
}

func (c *Client) listTables(ctx context.Context, input sourceInput) (any, error) {
	if !validSource(input) {
		return nil, errors.New("INVALID_REQUEST: use discovered project, environment and source IDs")
	}
	var out struct {
		Tables []table `json:"tables"`
		Views  []table `json:"views"`
	}
	if err := c.request(ctx, "catalog-tables", url.Values{"project": {input.Project}, "environment": {input.Environment}, "catalog": {input.Source}}, nil, &out); err != nil {
		return nil, err
	}
	for i := range out.Tables {
		out.Tables[i].Name = safeText(out.Tables[i].Name)
		out.Tables[i].Schema = safeText(out.Tables[i].Schema)
	}
	for i := range out.Views {
		out.Views[i].Name = safeText(out.Views[i].Name)
		out.Views[i].Schema = safeText(out.Views[i].Schema)
	}
	return out, nil
}

type table struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
}
type schemaResponse struct {
	Source  string `json:"source"`
	Schema  string `json:"schema"`
	Table   string `json:"table"`
	Columns []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"columns"`
	PrimaryKey   []string `json:"primaryKey"`
	ReferencedBy []string `json:"referencedBy"`
	ForeignKeys  []struct {
		Name       string   `json:"name"`
		Columns    []string `json:"columns"`
		RefTable   table    `json:"refTable"`
		RefColumns []string `json:"refColumns"`
	} `json:"foreignKeys"`
}

func (c *Client) describeTable(ctx context.Context, input tableInput) (any, error) {
	if !validSource(sourceInput{input.Project, input.Environment, input.Source}) || input.Table == "" || len(input.Table) > 256 || len(input.Schema) > 256 {
		return nil, errors.New("INVALID_REQUEST: use a discovered table and source")
	}
	var out schemaResponse
	body := struct {
		Project string `json:"project"`
		Env     string `json:"env"`
		Source  string `json:"source"`
		Schema  string `json:"schema"`
		Table   string `json:"table"`
	}{input.Project, input.Environment, input.Source, input.Schema, input.Table}
	if err := c.request(ctx, "records/schema", nil, body, &out); err != nil {
		return nil, err
	}
	// This closed wire projection cannot include rows, defaults or profiles.
	out.Source = safeText(out.Source)
	out.Schema = safeText(out.Schema)
	out.Table = safeText(out.Table)
	for i := range out.Columns {
		out.Columns[i].Name = safeText(out.Columns[i].Name)
		out.Columns[i].Type = safeText(out.Columns[i].Type)
	}
	out.PrimaryKey = safeTexts(out.PrimaryKey)
	out.ReferencedBy = safeTexts(out.ReferencedBy)
	for i := range out.ForeignKeys {
		out.ForeignKeys[i].Name = safeText(out.ForeignKeys[i].Name)
		out.ForeignKeys[i].Columns = safeTexts(out.ForeignKeys[i].Columns)
		out.ForeignKeys[i].RefTable.Schema = safeText(out.ForeignKeys[i].RefTable.Schema)
		out.ForeignKeys[i].RefTable.Name = safeText(out.ForeignKeys[i].RefTable.Name)
		out.ForeignKeys[i].RefColumns = safeTexts(out.ForeignKeys[i].RefColumns)
	}
	return out, nil
}

func bindParameters(def datatug.Parameters, supplied map[string]string) (map[string]apicontract.TypedValueOrSet, []apicontract.BindingOriginEntry, error) {
	parameters := map[string]apicontract.TypedValueOrSet{}
	origins := []apicontract.BindingOriginEntry{}
	byID := map[string]datatug.ParameterDef{}
	for _, p := range def {
		byID[p.ID] = p
	}
	for id, text := range supplied {
		p, ok := byID[id]
		if !ok || p.IsCredential || p.IsMultiValue {
			return nil, nil, errors.New("INVALID_REQUEST: unknown or unsupported parameter")
		}
		var v apicontract.TypedValue
		switch p.Type {
		case "string":
			v = apicontract.NewStringValue(text)
		case "integer":
			v = apicontract.NewIntegerValue(text)
		case "decimal":
			v = apicontract.NewDecimalValue(text)
		case "date":
			v = apicontract.NewDateValue(text)
		case "datetime":
			v = apicontract.NewDatetimeValue(text)
		case "boolean":
			if text != "true" && text != "false" {
				return nil, nil, errors.New("TYPE_MISMATCH: boolean parameter requires true or false")
			}
			v = apicontract.NewBooleanValue(text == "true")
		case "number":
			number, err := strconv.ParseFloat(text, 64)
			if err != nil {
				return nil, nil, errors.New("TYPE_MISMATCH: number parameter requires a number")
			}
			v = apicontract.NewNumberValue(number)
		default:
			return nil, nil, errors.New("TYPE_MISMATCH: unsupported SQL parameter type")
		}
		if v.Validate() != nil {
			return nil, nil, errors.New("TYPE_MISMATCH: invalid parameter value")
		}
		parameters[id] = apicontract.ScalarValue(v)
		origins = append(origins, apicontract.BindingOriginEntry{ParameterID: id, Origin: apicontract.BindingOriginManual})
	}
	for _, p := range def {
		if _, supplied := supplied[p.ID]; supplied || p.DefaultValue == nil {
			continue
		}
		v, err := queryparams.ScalarDefault(p)
		if err != nil {
			return nil, nil, errors.New("TYPE_MISMATCH: invalid or unsupported parameter default")
		}
		parameters[p.ID] = apicontract.ScalarValue(v)
		origins = append(origins, apicontract.BindingOriginEntry{ParameterID: p.ID, Origin: apicontract.BindingOriginDefault})
	}
	sort.Slice(origins, func(i, j int) bool { return origins[i].ParameterID < origins[j].ParameterID })
	return parameters, origins, nil
}

func (c *Client) prepareQuery(ctx context.Context, input runInput) (*datatug.QueryDefWithFolderPath, apicontract.AgentInfo, error) {
	if !validSource(sourceInput{input.Project, input.Environment, input.Source}) || !queryIdentifier(input.QueryID) || input.Limit < 0 || input.Limit > 500 {
		return nil, apicontract.AgentInfo{}, errors.New("INVALID_REQUEST: use discovered IDs and a limit from 1 to 500")
	}
	q, err := c.loadQuery(ctx, queryInput{input.Project, input.QueryID})
	if err != nil {
		return nil, apicontract.AgentInfo{}, err
	}
	info, err := c.info(ctx)
	return q, info, err
}

func (c *Client) runQuery(ctx context.Context, input runInput) (any, error) {
	q, info, err := c.prepareQuery(ctx, input)
	if err != nil {
		return nil, err
	}
	return c.executeQuery(ctx, input, q, info)
}

// executeQuery uses the exact definition and authority prepared by the caller;
// private sessions bind their capability to those same unredacted inputs.
func (c *Client) executeQuery(ctx context.Context, input runInput, q *datatug.QueryDefWithFolderPath, info apicontract.AgentInfo) (any, error) {
	parameters, origins, err := bindParameters(q.Parameters, input.Parameters)
	if err != nil {
		return nil, err
	}

	limit := input.Limit
	if limit == 0 {
		limit = 100
	}
	req := apicontract.ExecutionRequest{Project: input.Project, Environment: input.Environment, Source: input.Source, QueryID: input.QueryID, SecurityContextID: info.SecurityContextID, Parameters: parameters, BindingOrigins: origins, Mode: apicontract.ProvenanceModeLive, Limit: &limit}
	var result apicontract.Result
	if err := c.request(ctx, "exec/run_query", nil, req, &result); err != nil {
		return nil, err
	}
	if result.Validate() != nil || len(result.Recordset.Rows) > limit || result.Provenance.ExecutionProfile != apicontract.ExecutionProfileOpaquePrivileged {
		return nil, errors.New("local agent returned an invalid query result")
	}
	// Forward only the shared read contract. Execution/row-evidence extensions
	// and unknown fields are deliberately absent, as are connection DTOs.
	for i := range result.Limitations {
		result.Limitations[i].Policy = safeText(result.Limitations[i].Policy)
		result.Limitations[i].HiddenColumns = safeTexts(result.Limitations[i].HiddenColumns)
	}
	result.Provenance.Source = safeText(result.Provenance.Source)
	result.Provenance.QueryID = safeText(result.Provenance.QueryID)
	result.Provenance.Collection = safeText(result.Provenance.Collection)
	result.Provenance.SnapshotID = safeText(result.Provenance.SnapshotID)
	for i := range result.Recordset.Columns {
		result.Recordset.Columns[i].Name = safeText(result.Recordset.Columns[i].Name)
	}
	bindings := make([]binding, len(result.BindingsApplied))
	for i, item := range result.BindingsApplied {
		bindings[i] = binding{safeText(item.ParameterID), item.Value, item.Origin, item.OriginEvidence}
	}
	return struct {
		Recordset       apicontract.Recordset    `json:"recordset"`
		Limitations     []apicontract.Limitation `json:"limitations"`
		BindingsApplied []binding                `json:"bindingsApplied"`
		Provenance      apicontract.Provenance   `json:"provenance"`
		Truncated       bool                     `json:"truncated"`
	}{result.Recordset, result.Limitations, bindings, result.Provenance, result.Truncated}, nil
}

type binding struct {
	ParameterID    string                      `json:"parameterId"`
	Value          apicontract.TypedValueOrSet `json:"value"`
	Origin         string                      `json:"origin"`
	OriginEvidence string                      `json:"originEvidence"`
}
