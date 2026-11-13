package commands

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"errors"
	"fmt"
	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/labelguard"
	"github.com/datatug/datatug-core/pkg/meaninglabels"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-cli/pkg/chat/narrowing"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/joinmeta"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/spf13/cobra"
	"github.com/strongo/aichat/ai"
	"github.com/strongo/aichat/ai/cloud"
)

const defaultChatModel = "gpt-5.6-luna"

type chatOptions struct {
	workspace       *workspaceStart
	notice          string // a line the workspace shows first in the chat of this project
	openAtStart     string // a table the workspace opens as it starts, by its title
	project         string
	env             string
	database        string
	ai              string
	model           string
	baseURL         string
	thinking        string
	apiKey          string
	insecureStorage bool
	as              string
	cloudDecision   string
	cloudModel      string
	cloudProject    string
	cloudIdentity   string // saved preference's login scope, verified at cloud preflight
	cloudScope      string // saved preference's local project, verified at cloud preflight
	cloudAPI        string // saved preference's API base, verified at cloud preflight
	roles           []string
	groups          []string
	// result is a stored query result the workspace opens on (`ui --result`).
	result *resultOpen
	// modelExplicit: the AI was named on this command line, so the AI chosen
	// for the project in the workspace does not replace it.
	modelExplicit bool
	aiFromChoice  bool // the AI is the one chosen for this project in the workspace
	aiExact       bool // the endpoint and key were typed in the workspace: the environment adds nothing
	// modelRemembered: the model is the one remembered from an earlier
	// command line, not named on this one.
	modelRemembered bool
	aiState         *aiRunState // shared by the reopenings of one run
}

func chatCommand() *cobra.Command {
	options := chatOptions{}
	cmd := &cobra.Command{
		Use:   "chat",
		Short: "Ask questions about project data using TugQL",
		Long: `Ask questions about project data using TugQL.

Table narrowing: before each AI turn the chat can decide which of the project's
tables the model needs and show it only those definitions. The model is told
which tables were left out and can read up to two of them per turn with
describe_relation, and the chat prints one line naming the tables it was given.
When it cannot decide, or would save little, the full schema is sent as before.
Narrowing is a trade-off: it saves schema bytes (about 40% of the schema context
on the 11-table Chinook sample, more on larger schemas) but a table the model is
not shown can cost a describe_relation round trip.

  Project rules (always on, local): <project>/ai/table-rules.yaml maps an exact
  question to the tables it needs; a match never calls out.

  Cloud decider (OFF until YOU turn it on): with --model cloud it asks the
  DataTug AI cloud, which relays to TypeSafe AI's Jev decision model. That sends
  your question, up to three earlier questions of the session, and every table
  name with its column names (no types, no rows) to the DataTug cloud and to
  TypeSafe AI. Only you can turn it on:
    DATATUG_AI_DECISION_PROVIDER=auto     for this session (=disabled always wins)
    datatug chat --cloud-decision allow   for this project, remembered in your
                                          user config directory (refuse|forget)
  A project's "decision: auto" in ai/table-rules.yaml only REQUESTS it: it is
  ignored, with a notice, until you allow it. Whenever it is on, the chat says so
  at start: as a system line at the top of the chat (the terminal UI hides what
  is printed before it starts), and on stderr for non-interactive runs. After a failure it is not asked again for five minutes;
  DATATUG_AI_DECISION_TIMEOUT sets how long it may take per turn (default 1.5s).
  Schemas above 255 tables are not sent to it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags := cmd.Flags()
			options.modelExplicit = flags.Changed("model") || flags.Changed("ai") || flags.Changed("base-url")
			options.aiState = &aiRunState{}
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
	flags.StringVar(&options.model, "model", defaultChatModel, "Model name (for example gpt-5.6-luna, claude-haiku, cloud, or none for local queries)")
	flags.StringVar(&options.baseURL, "base-url", "", "model API base URL (for cloud, the shared API /v0/ URL; comma-separated list = failover hosts, primary first)")
	flags.BoolVar(&options.insecureStorage, "insecure-storage", false, "use the plaintext DataTug auth session created with auth login --insecure-storage (cloud only)")
	flags.StringVar(&options.thinking, "thinking", "low", "Model reasoning effort: low, medium, or high (provider support varies)")
	flags.StringVar(&options.cloudDecision, "cloud-decision", "", "Record your choice about the cloud decision engine for this project: allow, refuse or forget (it is off until you allow it)")
	flags.StringVar(&options.cloudModel, "cloud-model", "", "choose a hosted model listed by datatug plan (cloud only)")
	flags.StringVar(&options.cloudProject, "cloud-project", "", "opaque cloud-registered project ID hint, verified by the server (cloud only)")
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
	explicit := options.modelExplicit
	for {
		nextProject, err := runChatProjectFunc(cmd, options)
		if err == nil && options.aiState != nil && options.aiState.reopen {
			// The AI was set up in the workspace: open it again, in place.
			// What was just chosen for this project wins over the command line.
			options.aiState.reopen = false
			options.modelExplicit = false
			continue
		}
		if err != nil || nextProject == "" || nextProject == options.project {
			return err
		}
		options.modelExplicit = explicit // the command line holds for another project
		options.project = nextProject
		options.database = ""      // resolve the new project's source independently
		options.cloudDecision = "" // a consent choice is for the project it was given for
		options.cloudModel, options.cloudProject = "", ""
		options.cloudIdentity, options.cloudScope = "", ""
		options.cloudAPI = ""
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
	setSessionNotices = func(ui *chat.ChatUI, lines []string) { ui.SetSessionNotices(lines) }
)

// chatDatabaseRefusal is the one answer to --database when the value is not an ID
// of the environment. It names nothing that was typed.
const chatDatabaseRefusal = "--database takes the ID of a catalog of the environment (a name such as chinook-local), not a source URL or a path"

// isSourceString reports whether value reads as a source string: a URL or a path
// after a scheme the CLI knows, an env:NAME, or a datatug:// reference (see
// dbcopy.SourceDisplay). A plain name is never one. Neither is an unknown nonplain
// name such as "Chinook db": that may be a catalog ID of an existing project, so it
// is still looked up (and refused later only if the environment has no such
// catalog). This is the cheap check that keeps a source string from being read as
// an ID, before the project and catalog are loaded; it adds no parser of its own.
func isSourceString(value string) bool {
	return !dbcopy.IsPlainSourceID(value) && dbcopy.SourceDisplay(value) != dbcopy.UnparsableSource
}

func runChatProject(cmd *cobra.Command, options chatOptions) (string, error) {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	projectDir, projectStore, err := resolveQueryProject(options.project)
	if err != nil {
		return "", Exit(err.Error()+projectNotFoundAdvice, exitCodeUsage)
	}

	if options.cloudDecision != "" {
		path, consentErr := setCloudDecisionConsent(projectDir, options.cloudDecision)
		if consentErr != nil {
			return "", Exit(consentErr.Error(), exitCodeUsage)
		}
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "cloud decision engine for this project: %s (stored in %s)\n", options.cloudDecision, path)
		if advice := consentAdvice(projectDir, options.cloudDecision); advice != "" {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "note: %s\n", advice)
		}
	}

	if options.aiState == nil {
		options.aiState = &aiRunState{}
	}
	// What is remembered for the next chat is what was asked for on the
	// command line, never the AI chosen for this one project.
	lastOptions := options
	choiceNotice := ""
	if !options.modelExplicit {
		choiceNotice = applyProjectAIChoice(&options, projectDir)
		if options.model == "cloud" && options.modelRemembered && !options.aiFromChoice {
			// DataTug AI remembered from an earlier command line is not a
			// consent for this project: it is offered, not switched on.
			options.model = "none"
			choiceNotice = "DataTug AI was used before on this machine, but it is now chosen per project. Type " + aiCommand + " to choose it for this one."
		}
	}

	database := options.database
	// A source string typed into --database is not an ID of the environment: it is
	// refused before the project and its catalogs are read, naming nothing of it.
	// A plain name and a configured catalog ID that is not a plain name (such as
	// "Chinook db") are not source strings: they still reach the catalog, so the
	// degraded session of a known unresolved catalog opens exactly as before.
	if database != "" && isSourceString(database) {
		return "", Exit(chatDatabaseRefusal, exitCodeUsage)
	}
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
	// --database takes the ID of a catalog of the environment, and what is typed
	// there can be a whole source string with its password. A typed value that is
	// neither a source of the project, a catalog of the environment (whether or not
	// its source resolves: it opens the degraded session that shows the issue) nor a
	// plain name is refused here, naming nothing of it, so that no later step holds
	// it: not the connect screen, the settings the browser reads, the scope the store
	// hashes or the stand-in source of a failed lookup.
	_, known := sourceURLs[database]
	if options.database != "" && !known && !projectCatalogHasSource(projectCatalog, database) && !dbcopy.IsPlainSourceID(database) {
		return "", Exit(chatDatabaseRefusal, exitCodeUsage)
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
	var healthy api.CatalogSchema
	if storedSchema != nil {
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
	// Saved queries and the model's own TugQL read through this session, so
	// it checks the project's sensitivity labels, for every principal, and
	// strictly: what it reads goes to an AI model.
	installProjectLabels(&session, projectDir, chatSourceCatalogs(sourceURLs, sourceURL, database), cmd.ErrOrStderr(), true)
	executor := secureread.NewExecutor(session)
	var conversation chat.ContextualConversation
	var foreignKeys []narrowing.Link // set once the source's foreign keys are read
	var sessionNotices []string      // shown inside the chat as well as on stderr
	var cloudClient *cloud.Client
	var cloudContext ai.ClientContext
	var hostedSession cloudSession
	isSample := projectCatalog.ID == sampleProjectID
	aiMissing := false // nothing answers a question in plain words
	hostedDown := false
	if choiceNotice != "" {
		sessionNotices = append(sessionNotices, choiceNotice)
	}
	if options.model == "cloud" {
		cloudSession, loadErr := loadCloudSession(ctx, options)
		var plan cloudPlan
		planErr := loadErr
		if loadErr == nil {
			plan, planErr = resolveCloudSelection(ctx, cmd, &options, cloudSession)
		}
		switch {
		case planErr != nil && options.aiFromChoice:
			// DataTug AI was chosen in the workspace: a sign-in that expired
			// or a service that is away never closes the project. It opens
			// without AI and says how to get it back.
			hostedDown = true
			sessionNotices = append(sessionNotices, "DataTug AI is not available right now: "+aiPublicError(planErr)+". Type "+aiCommand+" to sign in again or choose another AI. Tables still open from the Explorer.")
		case planErr != nil:
			return "", Exit(fmt.Sprintf("configure cloud chat: %v", planErr), exitCodeUsage)
		}
		if planErr == nil && plan.V == 1 {
			notice := fmt.Sprintf("Hosted AI payer: %s (%s), %s; %d of %d questions left until %s", plan.AccountTitle, plan.AccountID, plan.EffectivePlan, *plan.AI.Left, *plan.AI.Limit, plan.AI.ResetsAt)
			if !plan.AI.Enforced {
				notice += "; AI allowance is currently observed, not enforced"
			}
			if plan.AI.Blocked != nil {
				notice += "; AI temporarily blocked: " + *plan.AI.Blocked
			}
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), notice)
			sessionNotices = append(sessionNotices, notice)
		}
		if planErr == nil {
			cloudClient, cloudContext, err = cloudChatClientFromSession(options, cloudSession)
			if err != nil {
				return "", Exit(fmt.Sprintf("configure cloud chat: %v", err), exitCodeUsage)
			}
			hostedSession = cloudSession
		}
	}
	if !hasQueryableProjectTables(projectCatalog, sourceURLs) {
		conversation = unavailableSchemaConversation{database: database}
	} else {
		var provider ai.LLMProvider
		if cloudClient != nil {
			provider = cloudUserProvider{cloudClient}
		} else {
			var providerErr error
			switch {
			case options.model == "none" || hostedDown:
			case options.aiExact:
				provider, providerErr = chat.NewLLMProviderExact(options.model, options.baseURL, options.apiKey)
			default:
				provider, providerErr = chat.NewLLMProvider(options.model, options.baseURL, options.apiKey)
			}
			if hostedDown || providerErr != nil || options.model == "none" || (!options.aiExact && chat.MissingAPIKey(options.model, options.baseURL, options.apiKey)) {
				provider = nil
				aiMissing = true
				conversation = providerUnavailableConversation{}
				notice := providerUnavailableNotice
				if isSample {
					conversation = sampleConversation{source: sourceURL, sourceID: database, run: func(ctx context.Context, tugql []byte) (secureread.Result, error) {
						return executor.RunTugQL(ctx, sourceURL, tugql, nil)
					}}
					notice = sampleQuestionsNotice()
				}
				if !hostedDown {
					sessionNotices = append(sessionNotices, notice)
				}
			}
		}
		if provider != nil {
			conversationOptions := []chat.Option{chat.WithThinkingLevel(options.thinking), chat.WithSources(sourceURLs)}
			if cloudClient != nil {
				conversationOptions = append(conversationOptions, chat.WithHostedModel(options.cloudModel))
			}
			// Narrowing applies to the stored-schema context only: the degraded
			// multi-source context is left exactly as it is.
			if healthyRelations > 0 && sourceErr == nil {
				choice, consentPath, consentWarning := readDecisionConsent(projectDir)
				if consentWarning != "" {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: table narrowing: %s\n", consentWarning)
					sessionNotices = append(sessionNotices, "Warning: table narrowing: "+consentWarning)
				}
				narrower, warnings, notices := chatTableNarrower(projectDir, cloudClient, healthy.Relations, func() []narrowing.Link { return foreignKeys }, consentState{choice, consentPath})
				for _, notice := range notices {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "table narrowing: %s\n", notice)
					sessionNotices = append(sessionNotices, "Table narrowing: "+notice)
				}
				for _, warning := range warnings {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: table narrowing: %s\n", warning)
					sessionNotices = append(sessionNotices, "Warning: table narrowing: "+warning)
				}
				if narrower != nil {
					conversationOptions = append(conversationOptions, chat.WithTableNarrowing(narrower))
				}
			}
			conversation, err = chat.NewAIConversation(provider, executor, sourceURL, schemaContext, conversationOptions...)
			if err != nil {
				return "", Exit(err.Error(), exitCodeUsage)
			}
		}
	}
	storePath, err := chat.DefaultChatStorePath(projectDir)
	if err != nil {
		return "", Exit(fmt.Sprintf("resolve chat storage: %v", err), exitCodeUsage)
	}
	store, err := chat.OpenSessionStore(storePath, chat.ChatScope{
		ProjectID:   projectCatalog.ID,
		Environment: options.env, Database: database,
		AccessFingerprint: accesspolicies.Fingerprint(session.Policies, session.DatabaseProfile.PolicyFreeReads(), session.Principal),
		Sources:           sourceURLs,
	})
	if err != nil {
		return "", Exit(fmt.Sprintf("open chat sessions: %v", err), exitCodeUsage)
	}
	defer func() { _ = store.Close() }()
	joinApplication, closeJoin, joinErr := loadChatJoinApplication(ctx, sourceURL, executor, session)
	if closeJoin != nil {
		defer closeJoin()
	}
	if joinErr != nil {
		setCatalogSourceIssue(&projectCatalog, database, "JOIN metadata unavailable: "+joinErr.Error())
	}
	if joinApplication != nil {
		foreignKeys = foreignKeyLinks(joinApplication.Snapshot.Keys)
	}
	if options.result != nil {
		if err := seedResult(ctx, store, sourceURL, options.result); err != nil {
			return "", Exit(fmt.Sprintf("open the query result: %v", err), exitCodeUsage)
		}
	}
	sessions, err := newSessionChat(ctx, store, conversation, sourceURL, projectCatalog)
	if err != nil {
		return "", Exit(fmt.Sprintf("restore chat session: %v", err), exitCodeUsage)
	}
	if cloudClient != nil {
		enableChatReports(sessions, cloudClient, cloudContext)
		defer sessions.WaitForTelemetry()
	}
	sessions.ConfigureQueryExecutor(executor)
	if joinApplication != nil {
		sessions.ConfigureJoinApplication(*joinApplication)
	}
	ui, err := newSessionChatUI(ctx, sessions, options.model)
	if err != nil {
		return "", Exit(fmt.Sprintf("render chat session: %v", err), exitCodeUsage)
	}
	if cloudClient != nil {
		ui.SetHostedPlanLookup(func(ctx context.Context) (string, error) {
			var output strings.Builder
			err := writeHostedPlan(ctx, hostedSession, options.cloudProject, &output)
			return output.String(), err
		})
	}
	if options.notice != "" {
		sessionNotices = append([]string{options.notice}, sessionNotices...)
	}
	if options.aiState.chosen && !aiMissing {
		ready := "AI is set up: " + currentAI(options, false) + " answers your questions in this project. Change it any time with " + aiCommand + "."
		if options.aiState.pending != "" {
			ready += " Your question is in the composer: press Enter to ask it."
		}
		if options.aiState.keyNote != "" {
			ready += " " + options.aiState.keyNote
		}
		sessionNotices = append([]string{ready}, sessionNotices...)
	}
	if len(sessionNotices) > 0 {
		// The terminal UI uses the alternate screen: what was printed to stderr
		// above is not visible until the user quits, so show it in the chat too.
		setSessionNotices(ui, sessionNotices)
	}
	if err := setSavedQueryService(ui, chatSavedQueries{projectDir: projectDir, store: projectStore, executor: executor, env: options.env, projectID: projectCatalog.ID, session: session, warn: cmd.ErrOrStderr()}); err != nil {
		return "", Exit(fmt.Sprintf("list saved project queries: %v", err), exitCodeUsage)
	}
	bridge, err := startBrowserBridge(sessions)
	if err != nil {
		return "", Exit(fmt.Sprintf("start browser chat: %v", err), exitCodeUsage)
	}
	defer func() { _ = bridge.Close() }()
	ui.SetBrowserURL(bridge.URL)
	// Opening a stored result is not a choice of project, environment or
	// database for the next chat, so it leaves the saved choices as they were.
	if options.result == nil {
		toSave := options
		if options.aiFromChoice {
			toSave = lastOptions
		}
		if err := saveLastChatOptionsFunc(cmd, toSave); err != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: save chat options: %v\n", err)
		}
	}
	ui.SetProjectChoices(chatProjectChoices(options.project, projectDir, projectCatalog))
	if options.workspace != nil {
		mountExplore(ui, *options.workspace)
	}
	ui.OpenTableAtStart(options.openAtStart)
	noneChosen := options.aiFromChoice && options.model == "none"
	ui.SetAISetup(chatAISetup(ctx, options, projectDir, projectCatalog.Title, aiMissing, noneChosen, isSample))
	if options.aiState.chosen {
		// Reopened after the AI setup: continue in the chat, with the
		// question that opened the setup back in the composer.
		ui.ShowChat()
		ui.SetComposerText(options.aiState.pending)
		options.aiState.chosen, options.aiState.pending, options.aiState.keyNote = false, "", ""
	}
	var programOptions []tea.ProgramOption
	if options.workspace != nil {
		programOptions = options.workspace.programOptions
	}
	if err := ui.Run(programOptions...); err != nil {
		return "", err
	}
	options.aiState.reopen, options.aiState.pending = ui.Reopened()
	options.aiState.chosen = options.aiState.reopen
	return ui.SelectedProject(), nil
}

// projectNotFoundAdvice is for the command line; the workspace leaves it out.
const projectNotFoundAdvice = "\nChoose an existing project: datatug projects, then datatug chat --project <ID-or-directory>\nStart a new project: datatug init <ID> <directory>"

// saveLastChatOptionsFunc is saveLastChatOptions; a seam so a test sees what
// is remembered for the next chat.
var saveLastChatOptionsFunc = saveLastChatOptions

// currentAI says in words what answers questions in this chat.
func currentAI(options chatOptions, missing bool) string {
	switch {
	case missing:
		return "no AI"
	case options.model == "cloud":
		return "DataTug AI"
	case options.baseURL != "":
		return options.model + " at your own endpoint"
	}
	return options.model + " with your own key"
}

func writeHostedPlan(ctx context.Context, session cloudSession, project string, w io.Writer) error {
	plan, err := session.getPlan(ctx, project)
	if errors.Is(err, planHTTPError{status: http.StatusUnauthorized}) {
		return fmt.Errorf("%w: type %s", err, aiCommand)
	}
	if err != nil {
		return err
	}
	if err := plan.requirePersonalFreeOrPro(); err != nil {
		return err
	}
	return writePlan(w, plan)
}

type unavailableSchemaConversation struct{ database string }

func (c unavailableSchemaConversation) AskWithContext(context.Context, string, string) (chat.Turn, error) {
	// c.database is what --database was given, and a source string can be typed
	// where a catalog ID belongs: only a plain name is echoed.
	return chat.Turn{Text: "I can't query " + dbcopy.SourceIDDisplay(c.database) + " because its source or schema is unavailable. Open Project explorer for details."}, nil
}

// JOIN discovery is optional: a failed metadata read should disable JOIN
// suggestions for this source, not prevent chat or other sources from loading.
// The source is opened as `query run` opens it (read-only for SQLite and
// PostgreSQL) and its foreign keys are read through DALgo's engine-neutral
// schema calls, so every engine whose provider reports them gets JOINs.
// openJoinMetadataDB is that opener; a package var so tests can make it fail.
var openJoinMetadataDB = func(ctx context.Context, backend dbcopy.BackendRef) (dal.DB, error) {
	return openBackend(ctx, backend)
}

func loadChatJoinApplication(ctx context.Context, sourceURL string, executor *secureread.Executor, session secureread.Session) (*chat.ForeignKeyJoinApplication, func(), error) {
	session, err := secureread.NormalizeSession(session)
	if err != nil {
		return nil, nil, err
	}
	if strings.HasPrefix(sourceURL, "unavailable://") {
		return nil, nil, nil
	}
	joinSource, err := dbcopy.Parse(sourceURL)
	if err != nil {
		return nil, nil, err
	}
	metadataDB, err := openJoinMetadataDB(ctx, joinSource)
	if err != nil {
		return nil, nil, err
	}
	closeDB := dbcopy.SourceCloser(metadataDB)
	refresh := func(ctx context.Context) (chat.ForeignKeySnapshot, error) {
		return joinmeta.LoadForeignKeySnapshot(ctx, metadataDB, sourceURL)
	}
	snapshot, err := refresh(ctx)
	if err != nil {
		closeDB()
		return nil, nil, err
	}
	// CanReadWholeCollection checks a relation by name only, so a target in
	// another schema than the source's default one is refused.
	defaultSchema := api.DefaultSchemaForDriver(joinSource.Scheme)
	application := &chat.ForeignKeyJoinApplication{
		Source: sourceURL, Snapshot: snapshot, Refresh: refresh,
		Executor: executor, Secure: !session.DatabaseProfile.PolicyFreeReads() || len(session.Policies) > 0,
		CanReadTarget: func(ctx context.Context, target chat.RelationInstance) error {
			if target.Schema != "" && !strings.EqualFold(target.Schema, defaultSchema) {
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
	// selectedSource is what --database was given (see unavailableSchemaConversation)
	// and this text goes to the model provider: only a plain name is sent.
	fmt.Fprintf(&b, "Selected source %q has unavailable schema or connection. Do not query it. For another listed source, always set sourceId in run_tugql.\n", dbcopy.SourceIDDisplay(selectedSource))
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
	labels, labelsErr := loadLabels(ctx, projectDir)
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
			primaryKey := []string{}
			keyPositions := map[string]int{}
			for i, column := range relation.Columns {
				columns[i] = column.Name
				columnTypes[column.Name] = column.DbType
				if column.PrimaryKeyPosition > 0 {
					primaryKey = append(primaryKey, column.Name)
					keyPositions[column.Name] = column.PrimaryKeyPosition
				}
			}
			sort.SliceStable(primaryKey, func(i, j int) bool { return keyPositions[primaryKey[i]] < keyPositions[primaryKey[j]] })
			catalog.Objects = append(catalog.Objects, chat.ProjectObject{Reference: chat.ContextReference{
				Kind: kind, ProjectID: project.ID, SourceID: id, ObjectID: name, Title: relation.Name,
			}, Columns: columns, ColumnTypes: columnTypes, PrimaryKey: primaryKey, Issue: relation.Issue,
				ProtectedColumns: chatProtectedColumns(labels, labelsErr, id, relation.Name, kind, columns)})
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

// projectCatalogHasSource reports whether the environment has a catalog with this
// ID: buildChatProjectCatalog adds a "source" object for every catalog of the
// environment, whether or not its source resolves.
func projectCatalogHasSource(catalog chat.ProjectCatalog, id string) bool {
	for _, object := range catalog.Objects {
		if object.Reference.Kind == "source" && object.Reference.SourceID == id {
			return true
		}
	}
	return false
}

// providerUnavailableConversation keeps the project/session and secure manual
// executor available while provider credentials are absent or offline is chosen.
type providerUnavailableConversation struct{}

// Without an AI provider the workspace still opens tables and runs queries;
// only questions in plain words need one. Both texts say where to set it up.
const (
	providerUnavailableNotice = "No AI is set up yet, so questions in plain words cannot be answered. Opening tables from the Explorer works without it. " + providerSetupAdvice
	providerUnavailableReply  = "I can't answer yet: no AI is set up. Tables still open from the Explorer. " + providerSetupAdvice + " No query was executed."
	// providerSetupAdvice is the way AI is set up inside the workspace.
	providerSetupAdvice = aiSetupAdvice
)

func (providerUnavailableConversation) AskWithContext(context.Context, string, string) (chat.Turn, error) {
	return chat.Turn{Text: providerUnavailableReply}, nil
}

// chatSourceCatalogs names every project source URL of a chat by the catalog
// ids that resolve to it: the explorer's sources and the chat's own database.
// Labels are keyed by catalog id, so a URL two catalogs share keeps both, and
// the labels of each apply.
func chatSourceCatalogs(sourceURLs map[string]string, sourceURL, database string) map[string][]string {
	catalogs := map[string][]string{}
	add := func(url, id string) {
		if !slices.Contains(catalogs[url], id) {
			catalogs[url] = append(catalogs[url], id)
			slices.Sort(catalogs[url])
		}
	}
	for id, url := range sourceURLs {
		add(url, id)
	}
	add(sourceURL, database)
	return catalogs
}

// chatProtectedColumns lists the columns of a table that a chat read may not
// name: chat is always strict, so a protected label refuses whether it is
// stored or a read-time suggestion (see installProjectLabels). The workspace
// opens the table without them instead of refusing the whole table.
//
// It lists nothing when the labels cannot be read, for a view, or when the
// table as a whole is protected: those reads keep refusing at the guard.
func chatProtectedColumns(doc meaninglabels.Document, labelsErr error, source, table, kind string, columns []string) []string {
	if labelsErr != nil || kind != "table" {
		return nil
	}
	for _, l := range doc.Labels {
		// A label on the table itself protects all of it: nothing to open.
		if l.Target.Column == "" && l.Class.Protected() && strings.EqualFold(l.Target.Source, source) && meaninglabels.TableMatches(l.Target.Table, table) {
			return nil
		}
	}
	var protected []string
	for _, column := range columns {
		if refused, _ := labelguard.CheckColumns(doc, source, table, false, []string{column}, true); refused != nil {
			protected = append(protected, column)
		}
	}
	return protected
}
