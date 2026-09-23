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
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/dimetron/pi-go/pimodels"
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

func runChat(cmd *cobra.Command, options chatOptions) error {
	if options.ai != "" {
		if err := resolveChatAIProfile(&options, cmd); err != nil {
			return Exit(err.Error(), exitCodeUsage)
		}
	}
	for {
		nextProject, err := runChatProject(cmd, options)
		if err != nil || nextProject == "" || nextProject == options.project {
			return err
		}
		options.project = nextProject
		options.database = "" // resolve the new project's source independently
	}
}

func runChatProject(cmd *cobra.Command, options chatOptions) (string, error) {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	projectDir, projectStore, err := resolveQueryProject(options.project)
	if err != nil {
		return "", Exit(err.Error(), exitCodeUsage)
	}

	database := options.database
	if database == "" {
		database, err = resolveQueryDatabase(ctx, projectStore, options.env, &datatug.QueryDef{ID: "chat"})
		if err != nil {
			return "", Exit(err.Error(), exitCodeUsage)
		}
	}
	sourceURL, err := resolveQuerySourceURL(ctx, projectStore, projectDir, options.env, database)
	if err != nil {
		return "", Exit(err.Error(), exitCodeUsage)
	}
	storedSchema, err := api.GetCatalogSchema(projectDir, options.env, database)
	if err != nil {
		return "", Exit(fmt.Sprintf("load project schema: %v", err), exitCodeUsage)
	}
	if len(storedSchema.Relations) == 0 {
		return "", Exit("the selected database has no scanned schema; run datatug scan first", exitCodeUsage)
	}
	projectCatalog, sourceURLs, err := buildChatProjectCatalog(ctx, projectDir, projectStore, options.env)
	if err != nil {
		return "", Exit(fmt.Sprintf("load project explorer: %v", err), exitCodeUsage)
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
	llm, err := pimodels.New(ctx, options.model, chatModelOptions(options)...)
	if err != nil {
		return "", Exit(fmt.Sprintf("configure chat model %q: %v", options.model, err), exitCodeUsage)
	}
	conversation, err := chat.NewADKConversation(llm, executor, sourceURL, chat.FormatSchemaContext(storedSchema), chat.WithThinkingLevel(options.thinking), chat.WithSources(sourceURLs))
	if err != nil {
		return "", Exit(err.Error(), exitCodeUsage)
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
	sessions, err := chat.NewSessionChat(ctx, store, conversation, sourceURL, projectCatalog)
	if err != nil {
		return "", Exit(fmt.Sprintf("restore chat session: %v", err), exitCodeUsage)
	}
	// FK evidence is source-scoped. Non-SQLite sources remain usable for Chat,
	// but expose no inferred JOINs in this first discovery implementation.
	joinSource, err := dbcopy.Parse(sourceURL)
	if err != nil {
		return "", Exit(fmt.Sprintf("resolve chat source: %v", err), exitCodeUsage)
	}
	if joinSource.Scheme == "sqlite" {
		if err := dbcopy.CheckSourceFile(joinSource.Path); err != nil {
			return "", Exit(fmt.Sprintf("load JOIN metadata: %v", err), exitCodeUsage)
		}
		readOnlyURL := (&url.URL{Scheme: "file", Path: joinSource.Path, RawQuery: "mode=ro"}).String()
		metadataDB, openErr := sql.Open("sqlite", readOnlyURL)
		if openErr != nil {
			return "", Exit(fmt.Sprintf("open JOIN metadata: %v", openErr), exitCodeUsage)
		}
		defer func() { _ = metadataDB.Close() }()
		refresh := func(ctx context.Context) (chat.ForeignKeySnapshot, error) {
			return chat.LoadSQLiteForeignKeySnapshot(ctx, sourceURL, metadataDB)
		}
		snapshot, scanErr := refresh(ctx)
		if scanErr != nil {
			return "", Exit(fmt.Sprintf("load JOIN metadata: %v", scanErr), exitCodeUsage)
		}
		sessions.ConfigureJoinApplication(chat.ForeignKeyJoinApplication{
			Source: sourceURL, Snapshot: snapshot, Refresh: refresh,
			Executor: executor, Secure: !session.Unrestricted,
			CanReadTarget: func(ctx context.Context, target chat.RelationInstance) error {
				if target.Schema != "" && !strings.EqualFold(target.Schema, "main") {
					return fmt.Errorf("JOIN target schema is not supported by this policy preflight")
				}
				return executor.CanReadWholeCollection(ctx, target.Relation)
			},
		})
	}
	ui, err := chat.NewSessionUI(ctx, sessions, options.model)
	if err != nil {
		return "", Exit(fmt.Sprintf("render chat session: %v", err), exitCodeUsage)
	}
	bridge, err := chat.StartBrowserBridge(sessions)
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

func chatProjectChoices(current, currentDir string, catalog chat.ProjectCatalog) []chat.ProjectChoice {
	choices := []chat.ProjectChoice{{Key: current, Title: catalog.Title, Detail: currentDir}}
	settings, err := dtconfig.GetSettings()
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
		if urls[id] == "" {
			continue
		}
		label := database.Title
		if label == "" {
			label = id
		}
		catalog.Objects = append(catalog.Objects, chat.ProjectObject{Reference: chat.ContextReference{
			Kind: "source", ProjectID: project.ID, SourceID: id, ObjectID: id, Title: label,
		}})
		schema, schemaErr := api.GetCatalogSchema(projectDir, environment, id)
		if schemaErr != nil {
			return catalog, nil, schemaErr
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
			}, Columns: columns, ColumnTypes: columnTypes})
		}
	}
	queryIDs, err := api.QueryIDIndex(projectDir)
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
		}})
	}
	return catalog, urls, nil
}

func chatModelOptions(options chatOptions) []pimodels.Option {
	modelOptions := []pimodels.Option{pimodels.WithThinkingLevel(options.thinking)}
	if options.apiKey != "" {
		modelOptions = append(modelOptions, pimodels.WithAPIKey(options.apiKey))
	}
	if options.baseURL != "" {
		modelOptions = append(modelOptions, pimodels.WithBaseURL(options.baseURL))
	}
	return modelOptions
}
