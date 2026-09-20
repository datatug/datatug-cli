package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/dimetron/pi-go/pimodels"
	"github.com/spf13/cobra"
)

const defaultChatModel = "gpt-5.6-luna"

type chatOptions struct {
	project  string
	env      string
	database string
	model    string
	baseURL  string
	thinking string
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
			return runChat(cmd, options)
		},
	}
	flags := cmd.Flags()
	flags.StringVarP(&options.project, "project", "p", ".", "DataTug project directory or registered project ID")
	flags.StringVar(&options.env, "env", "local", "Project environment ID")
	flags.StringVar(&options.database, "database", "", "Database catalog ID (auto-selected when the environment has one)")
	flags.StringVar(&options.model, "model", defaultChatModel, "Model name (for example gpt-5.6-luna, claude-haiku, or ollama/qwen3:4b)")
	flags.StringVar(&options.baseURL, "base-url", "", "OpenAI-compatible model API base URL")
	flags.StringVar(&options.thinking, "thinking", "low", "Model reasoning effort: low, medium, or high (provider support varies)")
	flags.StringVar(&options.as, "as", "", "Principal ID used for access policies")
	flags.StringSliceVar(&options.roles, "role", nil, "Principal role (repeatable)")
	flags.StringSliceVar(&options.groups, "group", nil, "Principal group (repeatable)")
	return cmd
}

func runChat(cmd *cobra.Command, options chatOptions) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	projectDir, projectStore, err := resolveQueryProject(options.project)
	if err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}

	database := options.database
	if database == "" {
		database, err = resolveQueryDatabase(ctx, projectStore, options.env, &datatug.QueryDef{ID: "chat"})
		if err != nil {
			return Exit(err.Error(), exitCodeUsage)
		}
	}
	sourceURL, err := resolveQuerySourceURL(ctx, projectStore, projectDir, options.env, database)
	if err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}
	storedSchema, err := api.GetCatalogSchema(projectDir, options.env, database)
	if err != nil {
		return Exit(fmt.Sprintf("load project schema: %v", err), exitCodeUsage)
	}
	if len(storedSchema.Relations) == 0 {
		return Exit("the selected database has no scanned schema; run datatug scan first", exitCodeUsage)
	}

	session, err := resolveServeSession(projectDir, serveFlags{
		projectDir: projectDir,
		as:         options.as,
		roles:      options.roles,
		groups:     options.groups,
	})
	if err != nil {
		return Exit("chat: "+strings.TrimPrefix(err.Error(), "serve: "), exitCodeUsage)
	}
	executor := secureread.NewExecutor(session)
	llm, err := pimodels.New(ctx, options.model, chatModelOptions(options)...)
	if err != nil {
		return Exit(fmt.Sprintf("configure chat model %q: %v", options.model, err), exitCodeUsage)
	}
	conversation, err := chat.NewADKConversation(llm, executor, sourceURL, chat.FormatSchemaContext(storedSchema), chat.WithThinkingLevel(options.thinking))
	if err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}
	return chat.NewUI(ctx, conversation, options.model).Run()
}

func chatModelOptions(options chatOptions) []pimodels.Option {
	modelOptions := []pimodels.Option{pimodels.WithThinkingLevel(options.thinking)}
	if options.baseURL != "" {
		modelOptions = append(modelOptions, pimodels.WithBaseURL(options.baseURL))
	}
	return modelOptions
}
