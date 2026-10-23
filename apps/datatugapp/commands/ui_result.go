package commands

import (
	"context"
	"path/filepath"
	"strconv"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-cli/pkg/queryresults"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/spf13/cobra"
)

// resultOpen is a stored query result, opened in the terminal workspace as an
// immutable chat RecordSet: its rows come from data.json and nothing is run.
type resultOpen struct {
	projectDir, env, database string
	query                     chat.QueryResult
}

// resultPrompt is the user line the opened result hangs under in the chat.
// It names no value, path or id.
const resultPrompt = "Opened a saved query result in the terminal (loaded from its files; no query was run)"

// runResultApp is a seam over the workspace a result opens in, and
// seedResult over putting the result in the project's chat store.
var (
	runResultApp = runUnifiedResultApp
	seedResult   = seedResultSession
)

// runUIResult is `datatug ui --result <id>`.
func runUIResult(cmd *cobra.Command, id string, principal chatOptions) error {
	result, err := loadResultForUI(cmd.Context(), id)
	if err != nil {
		return err
	}
	modules := uiModules()
	return runResultApp(modules, datatugui.Options{Start: datatugui.StartScreen(modules, "")}, result, principal)
}

// loadResultForUI reads the result's two files with the store's checks. A
// missing, expired, incomplete or unknown-version result exits 3 with one
// path-free line. The project comes from the result's own reference, through
// the registry, never from the current directory.
func loadResultForUI(ctx context.Context, id string) (resultOpen, error) {
	if !queryresults.ValidID(id) {
		return resultOpen{}, storeExit(&queryresults.Error{Reason: queryresults.ReasonBadID})
	}
	store, err := openResultsStore()
	if err != nil {
		return resultOpen{}, storeExit(err)
	}
	loaded, err := store.Load(id)
	switch queryresults.ReasonOf(err) {
	case "":
	case queryresults.ReasonNotFound, queryresults.ReasonExpired, queryresults.ReasonIncomplete, queryresults.ReasonVersion:
		return resultOpen{}, Exit(err.Error(), exitCodeNotFound)
	default:
		return resultOpen{}, storeExit(err)
	}
	meta := loaded.Meta
	if meta.DB.Ref == nil || meta.DB.Project == nil || meta.DB.Env == nil || meta.DB.Source == nil {
		return resultOpen{}, Exit("query result: its database is not a source of a registered project; add the database to a project to open the result in the terminal UI", exitCodeUsage)
	}
	projectDir, projectStore, err := resolveRegisteredProject(*meta.DB.Project)
	if err != nil {
		return resultOpen{}, Exit("query result: the project of this result is not registered on this machine", exitCodeNotFound)
	}
	query := chat.QueryResult{Title: "Query result", TugQL: meta.Query.TugQL, SourceID: *meta.DB.Source, Result: resultRows(loaded.Recordset), Parameters: nativeParameters(meta.Query.Parameters)}
	if meta.Query.Kind == queryresults.QueryKindSaved {
		query.QueryID = meta.Query.SavedQueryID
		if definition, loadErr := projectStore.LoadQuery(ctx, meta.Query.SavedQueryID); loadErr == nil && meta.Query.SavedQueryType == queryresults.QueryKindTugQL {
			query.TugQL = definition.Text
		}
	}
	return resultOpen{projectDir: projectDir, env: *meta.DB.Env, database: *meta.DB.Source, query: query}, nil
}

// resolveRegisteredProject resolves a result's project only as a project id
// registered in ~/.datatug.yaml, to the absolute directory registered for it:
// never as a directory of that name in the current folder (which a result
// would otherwise write its rows into), and never as a relative or remote
// location.
func resolveRegisteredProject(id string) (string, datatug.ProjectStore, error) {
	v := projectBaseCommand{ProjectName: id}
	if err := v.initProjectCommand(projectCommandOptions{projNameRequired: true}); err != nil {
		return "", nil, err
	}
	if !filepath.IsAbs(v.ProjectDir) {
		return "", nil, ErrUnknownProjectName
	}
	return filepath.Clean(v.ProjectDir), v.store.GetProjectStore(v.projectID), nil
}

// resultRows is data.json's recordset as a chat result: $key is the record
// key and every other column a field with its native value.
func resultRows(recordset apicontract.Recordset) secureread.Result {
	var result secureread.Result
	for _, column := range recordset.Columns[1:] {
		result.Columns = append(result.Columns, column.Name)
	}
	for _, row := range recordset.Rows {
		item := secureread.Row{Key: row[0].Str, Data: make(map[string]any, len(row)-1)}
		for i, column := range result.Columns {
			item.Data[column] = nativeOf(row[i+1])
		}
		result.Rows = append(result.Rows, item)
	}
	return result
}

// nativeOf is a typed value as the driver value a chat result holds: an
// integer that does not fit int64 stays its exact text.
func nativeOf(value apicontract.TypedValue) any {
	switch value.Type {
	case apicontract.ValueTypeNull:
		return nil
	case apicontract.ValueTypeInteger:
		if n, err := strconv.ParseInt(value.Str, 10, 64); err == nil {
			return n
		}
		return value.Str
	case apicontract.ValueTypeNumber:
		return value.Num
	case apicontract.ValueTypeBoolean:
		return value.Bool
	default:
		return value.Str
	}
}

func nativeParameters(parameters map[string]apicontract.TypedValueOrSet) map[string]any {
	if len(parameters) == 0 {
		return nil
	}
	native := make(map[string]any, len(parameters))
	for name, value := range parameters {
		if value.Set != nil {
			items := make([]any, len(value.Set.Values))
			for i, item := range value.Set.Values {
				items[i] = nativeOf(item)
			}
			native[name] = items
			continue
		}
		native[name] = nativeOf(*value.Scalar)
	}
	return native
}

// resultSessionStore is the part of the chat store a result is put in.
type resultSessionStore interface {
	Create(ctx context.Context, title string) (chat.ChatSession, error)
	AppendUser(ctx context.Context, sessionID, prompt string) (chat.ChatMessage, error)
	AppendQuery(ctx context.Context, sessionID, originID, source string, query chat.QueryResult) (chat.QueryResult, error)
}

// seedResultSession puts the result in a new chat session of the project's
// store, under one user line, so that the workspace opens on it.
func seedResultSession(ctx context.Context, store resultSessionStore, source string, result *resultOpen) error {
	session, err := store.Create(ctx, "Query result")
	if err != nil {
		return err
	}
	origin, err := store.AppendUser(ctx, session.ID, resultPrompt)
	if err != nil {
		return err
	}
	query := result.query
	query.Source = source
	_, err = store.AppendQuery(ctx, session.ID, origin.ID, source, query)
	return err
}

// runUnifiedResultApp opens the workspace of the result's project on the
// result. A failure to open it is returned, not replaced by the setup screen,
// so the person reads why. Switching to another project afterwards continues
// as the ordinary workspace does.
func runUnifiedResultApp(modules []datatugui.Module, opts datatugui.Options, result resultOpen, principal chatOptions, programOptions ...tea.ProgramOption) error {
	cmd, options := workspaceOptions(modules, opts, programOptions)
	options.project, options.env, options.database, options.result = result.projectDir, result.env, result.database, &result
	if principal.as != "" || len(principal.roles) > 0 || len(principal.groups) > 0 {
		options.as, options.roles, options.groups = principal.as, principal.roles, principal.groups
	}
	if options.ai != "" {
		if err := resolveChatAIProfile(&options, cmd); err != nil {
			options.ai = ""
		}
	}
	next, err := runChatProjectFunc(cmd, options)
	if err != nil || next == "" || next == options.project {
		return err
	}
	options.result = nil
	switchChatProject(&options, next)
	return runWorkspaceLoop(cmd, options)
}
