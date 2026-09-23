package commands

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)

// chatSavedQueries keeps project-file loading and source resolution out of
// the chat UI. Execution uses the existing policy-bound saved-query paths.
type chatSavedQueries struct {
	projectDir string
	store      datatug.ProjectStore
	executor   *secureread.Executor
	env        string
	projectID  string
	session    secureread.Session
}

func (s chatSavedQueries) Save(ctx context.Context, request chat.SavedQuerySaveRequest) (chat.SavedQuery, error) {
	writer, ok := s.store.(datatug.RevisionedQueriesStore)
	if !ok {
		return chat.SavedQuery{}, fmt.Errorf("this project store does not support safe query creation")
	}
	if request.Type != string(datatug.QueryTypeDTQL) && request.Type != string(datatug.QueryTypeHTTP) {
		return chat.SavedQuery{}, fmt.Errorf("only DTQL and HTTP results can be saved as project queries")
	}
	if strings.TrimSpace(request.Title) == "" || strings.TrimSpace(request.Text) == "" {
		return chat.SavedQuery{}, fmt.Errorf("query name and text are required")
	}
	if request.Type == string(datatug.QueryTypeDTQL) {
		var document yaml.Node
		if err := yaml.Unmarshal([]byte(request.Text), &document); err == nil && dtqlUsesParameters(&document) {
			return chat.SavedQuery{}, fmt.Errorf("parameterized DTQL cannot be captured yet; create a project query with parameter declarations")
		}
	}
	if request.Type == string(datatug.QueryTypeHTTP) {
		parsed, err := url.ParseRequestURI(request.Text)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return chat.SavedQuery{}, fmt.Errorf("project HTTP queries require HTTPS without credentials or URL parameters")
		}
	}
	id := savedQueryID(request.Title)
	if err := accesspolicies.AuthorizeWrite(ctx, accesspolicies.WriteOptions{
		Principal: s.session.Principal, Policies: s.session.Policies, Unrestricted: s.session.Unrestricted,
	}, access.Insert, accesspolicies.ProjectQueryResource(s.projectID, id)); err != nil {
		return chat.SavedQuery{}, err
	}
	definition := datatug.QueryDefWithFolderPath{QueryDef: datatug.QueryDef{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: id, Title: strings.TrimSpace(request.Title), ListOfTags: datatug.ListOfTags{Tags: append([]string(nil), request.Tags...)}}},
		Type:        datatug.QueryType(request.Type), Text: request.Text,
	}}
	if request.Type == string(datatug.QueryTypeDTQL) && request.Database != "" {
		definition.Targets = []datatug.QueryDefTarget{{Catalog: request.Database}}
	}
	if _, err := writer.PutQuery(ctx, &definition, datatug.QueryWriteCondition{IfNoneMatch: true}); err != nil {
		return chat.SavedQuery{}, err
	}
	return chat.SavedQuery{ID: id, Title: definition.Title, Type: request.Type, Tags: append([]string(nil), request.Tags...)}, nil
}

func savedQueryID(title string) string {
	var slug strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(title) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			slug.WriteRune(r)
			lastDash = false
		} else if !lastDash && slug.Len() > 0 {
			slug.WriteByte('-')
			lastDash = true
		}
		if slug.Len() >= 48 {
			break
		}
	}
	base := strings.Trim(slug.String(), "-")
	if base == "" {
		base = "query"
	}
	return base + "-" + uuid.NewString()[:8]
}

func (s chatSavedQueries) List(ctx context.Context) ([]chat.SavedQuery, error) {
	ids, err := api.QueryIDIndex(s.projectDir)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(ids))
	for id := range ids {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	queries := make([]chat.SavedQuery, 0, len(keys))
	for _, id := range keys {
		definition, err := s.store.LoadQuery(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("load project query %q: %w", id, err)
		}
		parameters := make([]chat.SavedQueryParameter, len(definition.Parameters))
		for i, parameter := range definition.Parameters {
			defaultValue, err := encodeParameterDefault(parameter.DefaultValue)
			if err != nil {
				return nil, fmt.Errorf("encode default for query %q parameter %q: %w", id, parameter.ID, err)
			}
			parameters[i] = chat.SavedQueryParameter{ID: parameter.ID, Title: parameter.Title, Type: parameter.Type, Required: parameter.IsRequired, Multi: parameter.IsMultiValue, DefaultValue: defaultValue}
			if parameter.Meta != nil {
				parameters[i].Entity, parameters[i].Field = parameter.Meta.Entity, parameter.Meta.Field
			}
		}
		queries = append(queries, chat.SavedQuery{ID: id, Title: definition.Title, Type: string(definition.Type), Tags: append([]string(nil), definition.Tags...), Parameters: parameters})
	}
	return queries, nil
}

func encodeParameterDefault(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	encoded, err := json.Marshal(value)
	return string(encoded), err
}

func dtqlUsesParameters(node *yaml.Node) bool {
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == "param" || dtqlUsesParameters(node.Content[i+1]) {
				return true
			}
		}
		return false
	}
	for _, child := range node.Content {
		if dtqlUsesParameters(child) {
			return true
		}
	}
	return false
}

func (s chatSavedQueries) LookupParameter(ctx context.Context, queryID, parameterID string) (*chat.SavedQueryLookup, error) {
	canonical, err := api.ResolveQueryID(s.projectDir, queryID)
	if err != nil {
		return nil, err
	}
	definition, err := s.store.LoadQuery(ctx, canonical)
	if err != nil {
		return nil, err
	}
	if definition.Type != datatug.QueryTypeDTQL {
		return nil, nil
	}
	var parameter *datatug.ParameterDef
	for i := range definition.Parameters {
		if definition.Parameters[i].ID == parameterID {
			parameter = &definition.Parameters[i]
			break
		}
	}
	if parameter == nil || parameter.Meta == nil || !parameter.IsRequired {
		return nil, nil
	}
	source, err := resolveSQLOrDTQLSourceURL(ctx, s.store, s.projectDir, s.env, definition)
	if err != nil {
		return nil, err
	}
	ref, err := dbcopy.Parse(source)
	if err != nil {
		return nil, err
	}
	if ref.Scheme != "sqlite" {
		return nil, nil
	}
	if err := dbcopy.CheckSourceFile(ref.Path); err != nil {
		return nil, err
	}
	readOnlyURL := (&url.URL{Scheme: "file", Path: ref.Path, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", readOnlyURL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	snapshot, err := chat.LoadSQLiteForeignKeySnapshot(ctx, source, db)
	if err != nil {
		return nil, err
	}
	if snapshot.Source != source {
		return nil, fmt.Errorf("foreign-key metadata source changed")
	}
	lookup := chat.DiscoverSavedQueryParameterLookupWithMode([]byte(definition.Text), snapshot, parameter.ID, parameter.Meta.Entity, parameter.Meta.Field, parameter.IsMultiValue)
	if lookup == nil {
		return nil, nil
	}
	result, err := s.executor.RunDTQL(ctx, source, lookup.Document(), nil)
	if err != nil {
		return nil, err
	}
	return &chat.SavedQueryLookup{Key: lookup.Key, Multi: lookup.Multi, Result: result}, nil
}

func (s chatSavedQueries) Run(ctx context.Context, id string) (chat.QueryResult, error) {
	return s.run(ctx, id, nil)
}

func (s chatSavedQueries) RunWithVariables(ctx context.Context, id string, raw map[string]string) (chat.QueryResult, error) {
	pairs := make([]string, 0, len(raw))
	for name, value := range raw {
		pairs = append(pairs, name+"="+value)
	}
	variables, err := accesspolicies.ParseVariables(pairs)
	if err != nil {
		return chat.QueryResult{}, fmt.Errorf("invalid query parameter name or value")
	}
	return s.run(ctx, id, variables)
}

func (s chatSavedQueries) run(ctx context.Context, id string, variables map[string]any) (chat.QueryResult, error) {
	canonical, err := api.ResolveQueryID(s.projectDir, id)
	if err != nil {
		return chat.QueryResult{}, err
	}
	definition, err := s.store.LoadQuery(ctx, canonical)
	if err != nil {
		return chat.QueryResult{}, err
	}
	var result secureread.Result
	source := ""
	database := ""
	switch definition.Type {
	case datatug.QueryTypeDTQL, datatug.QueryTypeSQL:
		database, err = resolveQueryDatabase(ctx, s.store, s.env, definition)
		if err != nil {
			return chat.QueryResult{}, err
		}
		source, err = resolveSQLOrDTQLSourceURL(ctx, s.store, s.projectDir, s.env, definition)
		if err != nil {
			return chat.QueryResult{}, err
		}
		if definition.Type == datatug.QueryTypeDTQL {
			result, err = s.executor.RunDTQL(ctx, source, []byte(definition.Text), variables)
		} else {
			result, err = runSQLSavedQuery(ctx, s.executor, s.store, s.projectDir, s.env, definition, variables)
		}
	case datatug.QueryTypeHTTP:
		source = "http://" + s.projectDir
		result, err = runHTTPSavedQuery(ctx, s.executor, s.projectDir, definition, variables)
	default:
		return chat.QueryResult{}, fmt.Errorf("query type %s is not runnable in chat", definition.Type)
	}
	if err != nil {
		return chat.QueryResult{}, err
	}
	query := chat.QueryResult{Title: definition.Title, Source: source, SourceID: database, Result: result, Parameters: variables}
	if definition.Type == datatug.QueryTypeDTQL {
		query.DTQL = definition.Text
	}
	return query, nil
}
