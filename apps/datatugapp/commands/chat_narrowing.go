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

// consentState is the user's own recorded choice for this project and where it is stored.
type consentState struct{ choice, path string }

// sentToThirdParty says what the cloud decision engine sends, and to whom.
const sentToThirdParty = "your question, up to three earlier questions of the session, and every table name with its column names (no types, no rows) to the DataTug cloud and, through it, to TypeSafe AI's Jev decision model"

// chatTableNarrower builds the decision that narrows a chat turn's schema context
// to the tables a question needs, or returns nil when the chat should keep
// sending its full schema. A problem with the configuration is returned as a
// warning and never stops the chat; notices tell the user what is, or is not,
// being forwarded and how to change it.
//
// Two rungs can narrow. The project's own deterministic rules
// (ai/table-rules.yaml) are local and always apply; they never call out. The
// cloud decider (TypeSafe AI's Jev behind the DataTug AI cloud) is OFF unless the
// USER turns it on, because it forwards the question text and the schema names to
// a third party:
//
//   - DATATUG_AI_DECISION_PROVIDER=auto|cloud turns it on (for the session);
//     =disabled always wins over everything else.
//   - A project's "decision: auto|cloud" only REQUESTS it. It takes effect only
//     when the user has allowed it for this project (datatug chat --cloud-decision
//     allow, stored in the user's config directory); otherwise it is ignored and
//     a notice says so.
//
// "cloud" is "auto" plus a warning when the chat is not using --model cloud.
func chatTableNarrower(projectDir string, cloudClient *cloud.Client, relations []api.CatalogRelation, links func() []narrowing.Link, consent consentState) (chat.TableNarrower, []string, []string) {
	settings := narrowing.LoadSettings(projectDir)
	warnings := settings.Warnings
	var notices []string
	provider, source := "disabled", ""
	requested := settings.Decision
	if requested != "" && requested != "disabled" && requested != "auto" && requested != "cloud" {
		warnings = append(warnings, fmt.Sprintf("unsupported decision setting %q in the project (use disabled, auto or cloud); it is ignored", requested))
		requested = ""
	}
	envName := chatDecisionEnvPrefix + aiconfig.EnvDecision
	switch env := chatGetenv(envName); {
	case env != "":
		provider, source = env, "the "+envName+" environment variable"
		if env != "disabled" && env != "auto" && env != "cloud" {
			warnings = append(warnings, fmt.Sprintf("unsupported decision provider %q in %s (use disabled, auto or cloud); table narrowing by the cloud decider is off", env, envName))
			provider = "disabled"
		}
	case requested == "auto" || requested == "cloud":
		switch consent.choice {
		case consentAllow:
			provider, source = requested, fmt.Sprintf("your consent for this project, stored in %s", consent.path)
		case consentRefuse:
		default:
			if cloudClient != nil {
				notices = append(notices, fmt.Sprintf("this project's %s asks to use the cloud decision engine, which would send %s. It is NOT enabled. To allow it for this project run: datatug chat --cloud-decision allow (stored in %s). To allow it for one session only: %s=auto datatug chat. To refuse and stop this message: datatug chat --cloud-decision refuse.", narrowing.SettingsFile, sentToThirdParty, consentWhere(consent.path), envName))
			}
		}
	}
	var engine decision.ScoredProvider
	if provider == "auto" || provider == "cloud" {
		switch {
		case cloudClient != nil:
			scorer, err := chatScorerOf(cloudClient.Decider())
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("the cloud decision engine is unavailable: %v", err))
			} else {
				engine = scorer
				off := fmt.Sprintf("set %s=disabled", envName)
				if consent.choice == consentAllow && chatGetenv(envName) == "" {
					off = fmt.Sprintf("run datatug chat --cloud-decision refuse (or set %s=disabled)", envName)
				}
				notices = append(notices, fmt.Sprintf("the cloud decision engine is ON (enabled by %s): each turn sends %s. To turn it off: %s.", source, sentToThirdParty, off))
			}
		case provider == "cloud":
			warnings = append(warnings, "the decision provider is \"cloud\" but the chat is not using --model cloud (run datatug auth login first); table narrowing by the cloud decider is off")
		}
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
		return nil, warnings, notices
	}
	narrower, err := chatNarrowingNew(narrowing.Config{
		Relations: relations, Rules: settings.Rules, Links: links, Engine: engine, Timeout: timeout,
		Policy: decision.NarrowingPolicy(), Format: chat.FormatSchemaContext,
	})
	if err != nil {
		return nil, append(warnings, fmt.Sprintf("table narrowing is off: %v", err)), notices
	}
	return narrower, append(warnings, narrower.Warnings()...), notices
}

// consentWhere names the consent store for a message, even when it could not be located.
func consentWhere(path string) string {
	if path == "" {
		return "the datatug folder of your user config directory"
	}
	return path
}

// foreignKeyLinks turns the chat's foreign-key snapshot into narrowing links.
func foreignKeyLinks(keys []chat.ForeignKey) []narrowing.Link {
	links := make([]narrowing.Link, 0, len(keys))
	for _, key := range keys {
		links = append(links, narrowing.Link{FromSchema: key.Schema, From: key.FromRelation, ToSchema: key.ToSchema, To: key.ToRelation})
	}
	return links
}
