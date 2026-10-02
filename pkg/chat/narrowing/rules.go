package narrowing

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/strongo/aichat/ai/decision"
	"github.com/strongo/aichat/ai/decision/rules"
	"github.com/strongo/aichat/ai/session"
	"gopkg.in/yaml.v3"
)

// RulesFile is where a project keeps its table rules, relative to the project
// directory. The format is provisional until the DataTug decision layer is
// specified (plan task K-1); it holds exact-phrase knowledge only.
const RulesFile = "ai/table-rules.yaml"

// Rule is deterministic project knowledge: when the question is exactly Phrase
// (compared after rules.Normalize: case, spacing and trailing punctuation do not
// matter), the answer needs exactly Tables, and no decision engine is asked.
type Rule struct {
	Phrase string   `yaml:"phrase"`
	Tables []string `yaml:"tables"`
}

type rulesFile struct {
	Rules []Rule `yaml:"rules"`
}

// LoadRules reads the project's RulesFile. A project without one has no rules,
// which is not an error.
func LoadRules(projectDir string) ([]Rule, error) {
	path := filepath.Join(projectDir, filepath.FromSlash(RulesFile))
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read table rules: %w", err)
	}
	var file rulesFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse %s: %w", RulesFile, err)
	}
	return file.Rules, nil
}

// rulesProviderName names the project rules in a decision trace.
const rulesProviderName = "datatug-project-rules"

// moduleName and intentName are the one module and intent of the narrowing
// taxonomy: every table is a scope of the module, so a rule's RequiredScopes are
// the tables the answer needs and decision.Validate rejects an unknown table.
const (
	moduleName = "datatug"
	intentName = "query"
)

// rulesProvider turns the rules into a decision.Provider, or nil when there are
// none. A rule that names no phrase or no table is a configuration error,
// reported here and not at match time.
func rulesProvider(set []Rule) (decision.Provider, error) {
	if len(set) == 0 {
		return nil, nil
	}
	byPhrase := make(map[string][]string, len(set))
	built := make([]rules.Rule, 0, len(set))
	for i, rule := range set {
		phrase := rules.Normalize(rule.Phrase)
		if phrase == "" || len(rule.Tables) == 0 {
			return nil, fmt.Errorf("table rule %d needs a phrase and at least one table", i+1)
		}
		if _, dup := byPhrase[phrase]; dup {
			return nil, fmt.Errorf("table rule %d repeats the phrase of an earlier rule", i+1)
		}
		byPhrase[phrase] = rule.Tables
		built = append(built, rules.Rule{Name: phrase, Match: func(text string, _ session.State) (decision.Decision, bool) {
			if text != phrase {
				return decision.Decision{}, false
			}
			return decision.Decision{
				Module:         decision.Scored{Value: moduleName},
				Intent:         decision.Scored{Value: intentName},
				Interaction:    decision.InteractionQuestion,
				RequiredScopes: append([]string(nil), byPhrase[phrase]...),
			}, true
		}})
	}
	return rules.New(rulesProviderName, built...), nil
}
