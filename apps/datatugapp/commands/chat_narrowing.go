package commands

import (
	"fmt"
	"os"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-cli/pkg/chat/narrowing"
	"github.com/strongo/aichat/ai/aiconfig"
	"github.com/strongo/aichat/ai/cloud"
	"github.com/strongo/aichat/ai/decision"
)

// chatDecisionEnvPrefix namespaces the aiconfig environment overrides for the
// chat's decision step, for example DATATUG_AI_DECISION_PROVIDER=disabled.
const chatDecisionEnvPrefix = "DATATUG_"

// Seams over chatTableNarrower's environment and its one error that no real cloud
// client can produce (the cloud decider is always a scorer). Always os.Getenv and
// narrowing.ScorerOf in production.
var (
	chatGetenv   = os.Getenv
	chatScorerOf = narrowing.ScorerOf
)

// chatTableNarrower builds the decision that narrows a chat turn's schema context
// to the tables a question needs, or returns nil when the chat should keep
// sending its full schema.
//
// Two rungs can narrow: the project's own deterministic rules (ai/table-rules.yaml,
// always read, never calling out), then the cloud decider (Jev behind the AI
// cloud), which exists only for --model cloud. The aiconfig decision provider
// governs the second: "auto" (the default) uses the cloud decider when signed in
// to the cloud, "cloud" requires it, and "disabled" turns it off. With no rules
// and no cloud decider the chat is unchanged.
func chatTableNarrower(projectDir string, cloudClient *cloud.Client, relations []api.CatalogRelation) (chat.TableNarrower, error) {
	cfg := aiconfig.Config{Decision: aiconfig.Decision{Provider: "auto"}}
	cfg.ApplyEnv(chatGetenv, chatDecisionEnvPrefix)
	var engine decision.ScoredProvider
	switch cfg.Decision.Provider {
	case "disabled":
	case "auto", "cloud":
		if cloudClient != nil {
			scorer, err := chatScorerOf(cloudClient.Decider())
			if err != nil {
				return nil, err
			}
			engine = scorer
		} else if cfg.Decision.Provider == "cloud" {
			return nil, fmt.Errorf("%sAI_DECISION_PROVIDER=cloud needs --model cloud (run datatug auth login first)", chatDecisionEnvPrefix)
		}
	default:
		return nil, fmt.Errorf("%sAI_DECISION_PROVIDER: unknown value %q (use auto, cloud or disabled)", chatDecisionEnvPrefix, cfg.Decision.Provider)
	}
	rules, err := narrowing.LoadRules(projectDir)
	if err != nil {
		return nil, err
	}
	if engine == nil && len(rules) == 0 {
		return nil, nil
	}
	return narrowing.New(narrowing.Config{
		Relations: relations, Rules: rules, Engine: engine,
		Policy: decision.NarrowingPolicy(), Format: chat.FormatSchemaContext,
	})
}
