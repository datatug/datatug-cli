package commands

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/spf13/cobra"
)

const defaultChatModel = "gpt-5.6-luna"

type chatOptions struct {
	project  string
	env      string
	database string
	ai       string
	model    string
	baseURL  string
	thinking string
	apiKey   string
	as       string
	roles    []string
	groups   []string
}

func chatCommand() *cobra.Command {
	options := chatOptions{}
	cmd := &cobra.Command{
		Use:   "chat",
		Short: "Ask questions about project data using DTQL",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := applyLastChatOptions(cmd, &options); err != nil {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: load previous chat options: %v\n", err)
			}
			return runChat(cmd, options)
		},
	}
	flags := cmd.Flags()
	flags.StringVarP(&options.project, "project", "p", ".", "DataTug project directory or registered project ID")
	flags.StringVar(&options.env, "env", "local", "Project environment ID")
	flags.StringVar(&options.database, "database", "", "Database catalog ID (auto-selected when the environment has one)")
	flags.StringVar(&options.ai, "ai", "", "Configured AI profile name")
	flags.StringVar(&options.model, "model", defaultChatModel, "Model name (for example gpt-5.6-luna, claude-haiku, or ollama/qwen3:4b)")
	flags.StringVar(&options.baseURL, "base-url", "", "OpenAI-compatible model API base URL")
	flags.StringVar(&options.thinking, "thinking", "low", "Model reasoning effort: low, medium, or high (provider support varies)")
	flags.StringVar(&options.as, "as", "", "Principal ID used for access policies")
	flags.StringSliceVar(&options.roles, "role", nil, "Principal role (repeatable)")
	flags.StringSliceVar(&options.groups, "group", nil, "Principal group (repeatable)")
	return cmd
}

// runChatProjectFunc is a seam over runChatProject: runChat's project-switch
// loop (options.project = nextProject) is otherwise only reachable by
// driving ChatUI's real F3 project-picker overlay end to end through
// chat.SetRunTeaProgramForTest's runTeaProgram seam, which the coverage
// lanes' other tests deliberately avoid.
// Always runChatProject in production.
var runChatProjectFunc = runChatProject

func runChat(cmd *cobra.Command, options chatOptions) error {
	if options.ai != "" {
		if err := resolveChatAIProfile(&options, cmd); err != nil {
			return Exit(err.Error(), exitCodeUsage)
		}
	}
	for {
		nextProject, err := runChatProjectFunc(cmd, options)
		if err != nil || nextProject == "" || nextProject == options.project {
			return err
		}
		options.project = nextProject
		options.database = "" // resolve the new project's source independently
	}
}

// The following are narrow seams over runChatProject's real-dependency
// constructors, each of which can otherwise only fail after every prior
// step in the same synchronous call has already succeeded -- no real
// fixture can diverge, say, a just-opened store's chat.NewSessionChat call
// from the chat.OpenSessionStore call three lines above it. Always the
// named real function/method in production.
var (
	newSessionChat       = chat.NewSessionChat
	newSessionChatUI     = chat.NewSessionChatUI
	startBrowserBridge   = chat.StartBrowserBridge
	setSavedQueryService = func(ui *chat.ChatUI, service chat.SavedQueryService) error {
		return ui.SetSavedQueryService(service)
	}
)

func runChatProject(cmd *cobra.Command, options chatOptions) (string, error) {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	projectDir, projectStore, err := resolveQueryProject(options.project)
	if err != nil {
		return "", Exit(err.Error()+"\nChoose an existing project: datatug projects, then datatug chat --project <ID-or-directory>\nStart a new project: datatug init <ID> <directory>", exitCodeUsage)
	}

	database := options.database
	if database == "" {
		database, err = resolveQueryDatabase(ctx, projectStore, options.env, &datatug.QueryDef{ID: "chat"})
		if err != nil {
			return "", Exit(err.Error(), exitCodeUsage)
		}
	}
	projectCatalog, sourceURLs, err := buildChatProjectCatalog(ctx, projectDir, projectStore, options.env)
	if err != nil {
		return "", Exit(fmt.Sprintf("load project explorer: %v", err), exitCodeUsage)
	}
	sourceURL, sourceErr := resolveQuerySourceURL(ctx, projectStore, projectDir, options.env, database)
	if sourceErr != nil {
		sourceURL = "unavailable://" + url.PathEscape(database)
		sourceURLs[database] = sourceURL // stable degraded session scope; never executed
		setCatalogSourceIssue(&projectCatalog, database, "Source unavailable: "+sourceErr.Error())
	}
	storedSchema, schemaErr := api.GetCatalogSchemaPartial(projectDir, options.env, database)
	if schemaErr != nil {
		setCatalogSourceIssue(&projectCatalog, database, "Schema unavailable: "+schemaErr.Error())
	}
	schemaContext := ""
	healthyRelations := 0
	if storedSchema != nil {
		var healthy api.CatalogSchema
		for _, relation := range storedSchema.Relations {
			if relation.Issue == "" {
				healthy.Relations = append(healthy.Relations, relation)
				healthyRelations++
			}
		}
		schemaContext = chat.FormatSchemaContext(&healthy)
	}
	if healthyRelations == 0 || sourceErr != nil {
		schemaContext = projectSchemaContext(projectCatalog, sourceURLs, database)
	}

	session, err := resolveServeSession(projectDir, serveFlags{
		projectDir: projectDir,
		as:         options.as,
		roles:      options.roles,
		groups:     options.groups,
	})
	if err != nil {
		return "", Exit("chat: "+strings.TrimPrefix(err.Error(), "serve: "), exitCodeUsage)
	}
	executor := secureread.NewExecutor(session)
	var conversation chat.ContextualConversation
	if !hasQueryableProjectTables(projectCatalog, sourceURLs) {
		conversation = unavailableSchemaConversation{database: database}
	} else {
		provider, providerErr := chat.NewLLMProvider(options.model, options.baseURL, options.apiKey)
		if providerErr != nil {
			return "", Exit(fmt.Sprintf("configure chat model %q: %v", options.model, providerErr), exitCodeUsage)
		}
		conversation, err = chat.NewAIConversation(provider, executor, sourceURL, schemaContext, chat.WithThinkingLevel(options.thinking), chat.WithSources(sourceURLs))
		if err != nil {
			return "", Exit(err.Error(), exitCodeUsage)
		}
	}
	storePath, err := chat.DefaultChatStorePath(projectDir)
	if err != nil {
		return "", Exit(fmt.Sprintf("resolve chat storage: %v", err), exitCodeUsage)
	}
	store, err := chat.OpenSessionStore(storePath, chat.ChatScope{
		ProjectID:   projectCatalog.ID,
		Environment: options.env, Database: database,
		AccessFingerprint: accesspolicies.Fingerprint(session.Policies, session.Unrestricted, session.Principal),
		Sources:           sourceURLs,
	})
	if err != nil {
		return "", Exit(fmt.Sprintf("open chat sessions: %v", err), exitCodeUsage)
	}
	defer func() { _ = store.Close() }()
	joinApplication, closeJoin, joinErr := loadChatJoinApplication(ctx, sourceURL, executor, session.Unrestricted)
	if closeJoin != nil {
		defer closeJoin()
	}
	if joinErr != nil {
		setCatalogSourceIssue(&projectCatalog, database, "JOIN metadata unavailable: "+joinErr.Error())
	}
	sessions, err := newSessionChat(ctx, store, conversation, sourceURL, projectCatalog)
	if err != nil {
		return "", Exit(fmt.Sprintf("restore chat session: %v", err), exitCodeUsage)
	}
	sessions.ConfigureQueryExecutor(executor)
	if joinApplication != nil {
		sessions.ConfigureJoinApplication(*joinApplication)
	}
	ui, err := newSessionChatUI(ctx, sessions, options.model)
	if err != nil {
		return "", Exit(fmt.Sprintf("render chat session: %v", err), exitCodeUsage)
	}
	if err := setSavedQueryService(ui, chatSavedQueries{projectDir: projectDir, store: projectStore, executor: executor, env: options.env, projectID: projectCatalog.ID, session: session}); err != nil {
		return "", Exit(fmt.Sprintf("list saved project queries: %v", err), exitCodeUsage)
	}
	bridge, err := startBrowserBridge(sessions)
	if err != nil {
		return "", Exit(fmt.Sprintf("start browser chat: %v", err), exitCodeUsage)
	}
	defer func() { _ = bridge.Close() }()
	ui.SetBrowserURL(bridge.URL)
	if err := saveLastChatOptions(cmd, options); err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: save chat options: %v\n", err)
	}
	ui.SetProjectChoices(chatProjectChoices(options.project, projectDir, projectCatalog))
	if err := ui.Run(); err != nil {
		return "", err
	}
	return ui.SelectedProject(), nil
}

type unavailableSchemaConversation struct{ database string }

func (c unavailableSchemaConversation) AskWithContext(context.Context, string, string) (chat.Turn, error) {
	return chat.Turn{Text: "I can't query " + c.database + " because its source or schema is unavailable. Open Project explorer for details."}, nil
}

// JOIN discovery is optional: a failed metadata read should disable JOIN
// suggestions for this source, not prevent chat or other sources from loading.
// openJoinMetadataDB is a seam over sql.Open: modernc.org/sqlite's driver
// never errors eagerly for a syntactically valid DSN (the connection itself
// is lazy), so no real fixture reaches loadChatJoinApplication's sql.Open
// error branch. Always sql.Open in production.
var openJoinMetadataDB = sql.Open

func loadChatJoinApplication(ctx context.Context, sourceURL string, executor *secureread.Executor, unrestricted bool) (*chat.ForeignKeyJoinApplication, func(), error) {
	if strings.HasPrefix(sourceURL, "unavailable://") {
		return nil, nil, nil
	}
	joinSource, err := dbcopy.Parse(sourceURL)
	if err != nil {
		return nil, nil, err
	}
	if joinSource.Scheme != "sqlite" {
		return nil, nil, nil
	}
	if err := dbcopy.CheckSourceFile(joinSource.Path); err != nil {
		return nil, nil, err
	}
	readOnlyURL := (&url.URL{Scheme: "file", Path: joinSource.Path, RawQuery: "mode=ro"}).String()
	metadataDB, err := openJoinMetadataDB("sqlite", readOnlyURL)
	if err != nil {
		return nil, nil, err
	}
	closeDB := func() { _ = metadataDB.Close() }
	refresh := func(ctx context.Context) (chat.ForeignKeySnapshot, error) {
		return chat.LoadSQLiteForeignKeySnapshot(ctx, sourceURL, metadataDB)
	}
	snapshot, err := refresh(ctx)
	if err != nil {
		closeDB()
		return nil, nil, err
	}
	application := &chat.ForeignKeyJoinApplication{
		Source: sourceURL, Snapshot: snapshot, Refresh: refresh,
		Executor: executor, Secure: !unrestricted,
		CanReadTarget: func(ctx context.Context, target chat.RelationInstance) error {
			if target.Schema != "" && !strings.EqualFold(target.Schema, "main") {
				return fmt.Errorf("JOIN target schema is not supported by this policy preflight")
			}
			return executor.CanReadWholeCollection(ctx, target.Relation)
		},
	}
	return application, closeDB, nil
}

func setCatalogSourceIssue(catalog *chat.ProjectCatalog, sourceID, issue string) {
	for i := range catalog.Objects {
		object := &catalog.Objects[i]
		if object.Reference.Kind == "source" && object.Reference.SourceID == sourceID {
			if object.Issue != "" {
				object.Issue += "; "
			}
			object.Issue += issue
			return
		}
	}
}

func hasQueryableProjectTables(catalog chat.ProjectCatalog, urls map[string]string) bool {
	for _, object := range catalog.Objects {
		if (object.Reference.Kind == "table" || object.Reference.Kind == "project_view") && object.Issue == "" {
			if source := urls[object.Reference.SourceID]; source != "" && !strings.HasPrefix(source, "unavailable://") {
				return true
			}
		}
	}
	return false
}

func projectSchemaContext(catalog chat.ProjectCatalog, urls map[string]string, selectedSource string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Selected source %q has unavailable schema or connection. Do not query it. For another listed source, always set sourceId in run_dtql.\n", selectedSource)
	for _, object := range catalog.Objects {
		ref := object.Reference
		if (ref.Kind != "table" && ref.Kind != "project_view") || object.Issue != "" || ref.SourceID == selectedSource {
			continue
		}
		if source := urls[ref.SourceID]; source == "" || strings.HasPrefix(source, "unavailable://") {
			continue
		}
		fmt.Fprintf(&b, "- sourceId %q, %s: ", ref.SourceID, ref.ObjectID)
		for i, column := range object.Columns {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%s [%s]", column, object.ColumnTypes[column])
		}
		b.WriteByte('\n')
		if b.Len() > 8000 {
			break
		}
	}
	return b.String()
}

func chatProjectChoices(current, currentDir string, catalog chat.ProjectCatalog) []chat.ProjectChoice {
	choices := []chat.ProjectChoice{{Key: current, Title: catalog.Title, Detail: currentDir}}
	settings, err := getChatSettings()
	if err != nil { // A direct project path can run without a project registry.
		return choices
	}
	for _, project := range settings.Projects {
		if project == nil || project.ID == "" || project.ID == catalog.ID || project.Path == "" || sameProjectDirectory(project.Path, currentDir) {
			continue
		}
		title := project.Title
		if title == "" {
			title = project.ID
		}
		choices = append(choices, chat.ProjectChoice{Key: project.ID, Title: title, Detail: project.Path})
	}
	return choices
}

func sameProjectDirectory(a, b string) bool {
	left, leftErr := os.Stat(a)
	right, rightErr := os.Stat(b)
	return leftErr == nil && rightErr == nil && left.IsDir() && right.IsDir() && os.SameFile(left, right)
}

// queryIDIndexFunc is a seam over api.QueryIDIndex: buildChatProjectCatalog's
// QueryIDIndex error branch (a filesystem walk, not routed through
// projectStore) has no other fault-injection point available to a test.
// Always api.QueryIDIndex in production.
var queryIDIndexFunc = api.QueryIDIndex

func buildChatProjectCatalog(ctx context.Context, projectDir string, projectStore datatug.ProjectStore, environment string) (chat.ProjectCatalog, map[string]string, error) {
	project, err := projectStore.LoadProject(ctx)
	if err != nil {
		return chat.ProjectCatalog{}, nil, err
	}
	catalog := chat.ProjectCatalog{ID: project.ID, Title: project.Title}
	if catalog.Title == "" {
		catalog.Title = project.ID
	}
	catalog.Objects = append(catalog.Objects, chat.ProjectObject{Reference: chat.ContextReference{
		Kind: "project", ProjectID: project.ID, ObjectID: project.ID, Title: catalog.Title,
	}})
	sources, err := api.ListSources(ctx, projectStore, projectDir, environment)
	if err != nil {
		return catalog, nil, err
	}
	urls := make(map[string]string, len(sources))
	for _, source := range sources {
		urls[source.ID] = source.URL
	}
	dbs, err := projectStore.LoadEnvDbCatalogs(ctx, environment)
	if err != nil {
		return catalog, nil, err
	}
	sort.Slice(dbs, func(i, j int) bool { return dbs[i].ID < dbs[j].ID })
	for _, database := range dbs {
		if database == nil {
			continue
		}
		id := database.ID
		label := database.Title
		if label == "" {
			label = id
		}
		sourceIndex := len(catalog.Objects)
		catalog.Objects = append(catalog.Objects, chat.ProjectObject{Reference: chat.ContextReference{
			Kind: "source", ProjectID: project.ID, SourceID: id, ObjectID: id, Title: label,
		}})
		if urls[id] == "" {
			catalog.Objects[sourceIndex].Issue = "Source connection could not be resolved. Check its catalog driver and path."
		}
		// An unscanned catalog is still a usable source. Only its table list
		// depends on a dbModel; do not let it block another catalog's chat.
		if database.DbModel == "" {
			setCatalogSourceIssue(&catalog, id, "Schema not scanned (dbModel is not set). Run datatug scan for this source.")
			continue
		}
		schema, schemaErr := api.GetCatalogSchemaPartial(projectDir, environment, id)
		if schemaErr != nil {
			setCatalogSourceIssue(&catalog, id, "Schema unavailable: "+schemaErr.Error())
			continue
		}
		if len(schema.Relations) == 0 {
			setCatalogSourceIssue(&catalog, id, "No scanned tables or views. Run datatug scan for this source.")
			continue
		}
		for _, relation := range schema.Relations {
			name := relation.Name
			if relation.Schema != "" {
				name = relation.Schema + "." + relation.Name
			}
			kind := "table"
			if strings.EqualFold(relation.DbType, "VIEW") {
				kind = "project_view"
			}
			columns := make([]string, len(relation.Columns))
			columnTypes := make(map[string]string, len(relation.Columns))
			for i, column := range relation.Columns {
				columns[i] = column.Name
				columnTypes[column.Name] = column.DbType
			}
			catalog.Objects = append(catalog.Objects, chat.ProjectObject{Reference: chat.ContextReference{
				Kind: kind, ProjectID: project.ID, SourceID: id, ObjectID: name, Title: relation.Name,
			}, Columns: columns, ColumnTypes: columnTypes, Issue: relation.Issue})
		}
	}
	queryIDs, err := queryIDIndexFunc(projectDir)
	if err != nil {
		return catalog, nil, err
	}
	names := make([]string, 0, len(queryIDs))
	for id := range queryIDs {
		names = append(names, id)
	}
	sort.Strings(names)
	for _, id := range names {
		query, loadErr := projectStore.LoadQuery(ctx, id)
		if loadErr != nil {
			return catalog, nil, loadErr
		}
		title := query.Title
		if title == "" {
			title = id
		}
		// A saved query belongs to a source only when its target says so.
		// Otherwise retain project scope instead of guessing the active database.
		sourceID := ""
		ambiguousSource := false
		for _, target := range query.Targets {
			if target.Catalog != "" && urls[target.Catalog] != "" {
				if sourceID != "" && sourceID != target.Catalog {
					ambiguousSource = true
					break
				}
				sourceID = target.Catalog
			}
		}
		if ambiguousSource {
			sourceID = ""
		}
		catalog.Objects = append(catalog.Objects, chat.ProjectObject{Reference: chat.ContextReference{
			Kind: "query", ProjectID: project.ID, SourceID: sourceID, ObjectID: id, Title: title,
		}, QueryType: string(query.Type), QueryText: query.Text})
	}
	return catalog, urls, nil
}
