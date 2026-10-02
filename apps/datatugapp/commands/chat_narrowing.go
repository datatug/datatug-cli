package commands

import (
	"fmt"
	"os"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-cli/pkg/chat/narrowing"
	"github.com/strongo/aichat/ai/aiconfig"
	"github.com/strongo/aichat/ai/cloud"
	"github.com/strongo/aichat/ai/decision"
)

// chatDecisionEnvPrefix namespaces the aiconfig environment overrides for the
// chat's decision step, for example DATATUG_AI_DECISION_PROVIDER=auto.
const chatDecisionEnvPrefix = "DATATUG_"

// chatDecisionTimeoutEnv sets how long the decision engine may take per turn (a Go
// duration such as 800ms); the default is 1.5s.
const chatDecisionTimeoutEnv = "DATATUG_AI_DECISION_TIMEOUT"

// Seams over chatTableNarrower's environment, its one error that no real cloud
// client can produce (the cloud decider is always a scorer) and narrowing.New.
// Always os.Getenv, narrowing.ScorerOf and narrowing.New in production.
var (
	chatGetenv       = os.Getenv
	chatScorerOf     = narrowing.ScorerOf
	chatNarrowingNew = narrowing.New
)

// chatTableNarrower builds the decision that narrows a chat turn's schema context
// to the tables a question needs, or returns nil when the chat should keep
// sending its full schema. Anything wrong with the configuration is returned as a
// warning for the user and never stops the chat.
//
// Two rungs can narrow. The project's own deterministic rules
// (ai/table-rules.yaml) are local and always apply; they never call out. The
// cloud decider (TypeSafe AI's Jev behind the DataTug AI cloud) is OFF unless the
// user opts in, because it forwards the question text and the table and column
// names to a third party: set DATATUG_AI_DECISION_PROVIDER or the project's
// "decision:" setting to "auto" (use it when signed in with --model cloud) or
// "cloud" (same, and warn when it cannot be used). Anything else, or an
// unsupported value, means disabled.
func chatTableNarrower(projectDir string, cloudClient *cloud.Client, relations []api.CatalogRelation, links func() []narrowing.Link) (chat.TableNarrower, []string) {
	settings := narrowing.LoadSettings(projectDir)
	warnings := settings.Warnings
	cfg := aiconfig.Config{Decision: aiconfig.Decision{Provider: "disabled"}}
	if settings.Decision != "" {
		cfg.Decision.Provider = settings.Decision
	}
	cfg.ApplyEnv(chatGetenv, chatDecisionEnvPrefix)
	var engine decision.ScoredProvider
	switch cfg.Decision.Provider {
	case "disabled":
	case "auto", "cloud":
		switch {
		case cloudClient != nil:
			scorer, err := chatScorerOf(cloudClient.Decider())
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("the cloud decision engine is unavailable: %v", err))
			} else {
				engine = scorer
			}
		case cfg.Decision.Provider == "cloud":
			warnings = append(warnings, "the decision provider is \"cloud\" but the chat is not using --model cloud (run datatug auth login first); table narrowing by the cloud decider is off")
		}
	default:
		warnings = append(warnings, fmt.Sprintf("unsupported decision provider %q (use disabled, auto or cloud); table narrowing by the cloud decider is off", cfg.Decision.Provider))
	}
	timeout := time.Duration(0)
	if raw := chatGetenv(chatDecisionTimeoutEnv); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			warnings = append(warnings, fmt.Sprintf("%s=%q is not a positive duration; the default is used", chatDecisionTimeoutEnv, raw))
		} else {
			timeout = parsed
		}
	}
	if engine == nil && len(settings.Rules) == 0 {
		return nil, warnings
	}
	narrower, err := chatNarrowingNew(narrowing.Config{
		Relations: relations, Rules: settings.Rules, Links: links, Engine: engine, Timeout: timeout,
		Policy: decision.NarrowingPolicy(), Format: chat.FormatSchemaContext,
	})
	if err != nil {
		return nil, append(warnings, fmt.Sprintf("table narrowing is off: %v", err))
	}
	return narrower, append(warnings, narrower.Warnings()...)
}

// foreignKeyLinks turns the chat's foreign-key snapshot into narrowing links.
func foreignKeyLinks(keys []chat.ForeignKey) []narrowing.Link {
	links := make([]narrowing.Link, 0, len(keys))
	for _, key := range keys {
		links = append(links, narrowing.Link{FromSchema: key.Schema, From: key.FromRelation, ToSchema: key.ToSchema, To: key.ToRelation})
	}
	return links
}
