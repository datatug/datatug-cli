package narrowing

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/strongo/aichat/ai/decision"
	"github.com/strongo/aichat/ai/decision/rules"
	"github.com/strongo/aichat/ai/session"
	"gopkg.in/yaml.v3"
)

// SettingsFile is where a project keeps its table-narrowing settings and rules,
// relative to the project directory. The format is provisional until the
// DataTug decision layer is specified (plan task K-1):
//
//	decision: auto          # disabled (default) | auto | cloud
//	rules:
//	  - phrase: Sales by country
//	    tables: [Invoice, Customer]
//
// "decision" opts the project in to the cloud decider (which forwards the
// question text and the table and column names to TypeSafe AI's Jev model,
// through the DataTug AI cloud); the environment variable
// DATATUG_AI_DECISION_PROVIDER overrides it. Rules are local and deterministic.
const SettingsFile = "ai/table-rules.yaml"

// maxSettingsBytes bounds the settings file that is read.
const maxSettingsBytes = 64 << 10

// Rule is deterministic project knowledge: when the question is exactly Phrase
// (compared after rules.Normalize: case, spacing and trailing punctuation do not
// matter), the answer needs exactly Tables, and no decision engine is asked.
type Rule struct {
	Phrase string   `yaml:"phrase"`
	Tables []string `yaml:"tables"`
}

// Settings are a project's narrowing settings as read from SettingsFile. Reading
// never fails: a problem becomes a Warning and the offending part is ignored, so
// a bad file can never stop a chat from starting.
type Settings struct {
	// Decision is the project's choice of decision provider ("" when unset).
	Decision string
	Rules    []Rule
	Warnings []string
}

// LoadSettings reads the project's SettingsFile. A project without one has no
// settings. The file is read only when it is a regular file inside the project
// (a symbolic link is never followed), is at most 64 KiB, and no warning echoes
// its content.
func LoadSettings(projectDir string) Settings {
	var s Settings
	warn := func(format string, args ...any) { s.Warnings = append(s.Warnings, fmt.Sprintf(format, args...)) }
	dir := filepath.Join(projectDir, filepath.Dir(filepath.FromSlash(SettingsFile)))
	path := filepath.Join(projectDir, filepath.FromSlash(SettingsFile))
	for _, entry := range []struct{ path, what string }{{dir, "its directory"}, {path, "it"}} {
		info, err := os.Lstat(entry.path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			return s
		case err != nil:
			warn("%s was not read: %s cannot be inspected", SettingsFile, entry.what)
			return s
		case info.Mode()&os.ModeSymlink != 0:
			warn("%s was not read: %s is a symbolic link, which is never followed", SettingsFile, entry.what)
			return s
		}
		if entry.path == path && !info.Mode().IsRegular() {
			warn("%s was not read: it is not a regular file", SettingsFile)
			return s
		}
		if entry.path == path && info.Size() > maxSettingsBytes {
			warn("%s was not read: it is larger than %d KiB", SettingsFile, maxSettingsBytes>>10)
			return s
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		warn("%s was not read: it cannot be opened", SettingsFile)
		return s
	}
	var top map[string]any
	if err := yaml.Unmarshal(raw, &top); err != nil {
		warn("%s is not a valid YAML mapping; its settings and rules are ignored", SettingsFile)
		return s
	}
	for _, key := range sortedKeys(top) {
		switch key {
		case "decision":
			value, ok := top[key].(string)
			if !ok {
				warn("%s: \"decision\" must be a word (disabled, auto or cloud); ignored", SettingsFile)
				continue
			}
			s.Decision = value
		case "rules":
			s.Rules = parseRules(top[key], warn)
		default:
			warn("%s: unknown key %s ignored", SettingsFile, quoted(key))
		}
	}
	return s
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// quoted renders a user-supplied word safely for a message: escaped and short.
func quoted(s string) string {
	if len(s) > 40 {
		s = s[:40] + "..."
	}
	return strconv.Quote(s)
}

// parseRules reads the rules list, skipping (with a warning that names the rule
// by its number and phrase) every rule that cannot work.
func parseRules(value any, warn func(string, ...any)) []Rule {
	list, ok := value.([]any)
	if !ok {
		warn("%s: \"rules\" must be a list; ignored", SettingsFile)
		return nil
	}
	var out []Rule
	seen := map[string]bool{}
	for i, item := range list {
		n := i + 1
		entry, ok := item.(map[string]any)
		if !ok {
			warn("%s: rule %d is not a mapping; skipped", SettingsFile, n)
			continue
		}
		rule, problem := parseRule(entry)
		if problem == "" {
			phrase := rules.Normalize(rule.Phrase)
			if seen[phrase] {
				problem = "repeats the phrase of an earlier rule"
			}
			seen[phrase] = true
		}
		if problem != "" {
			warn("%s: rule %d %s; skipped", SettingsFile, n, problem)
			continue
		}
		out = append(out, rule)
	}
	return out
}

func parseRule(entry map[string]any) (Rule, string) {
	var rule Rule
	for _, key := range sortedKeys(entry) {
		switch key {
		case "phrase":
			rule.Phrase, _ = entry[key].(string)
		case "tables":
			list, _ := entry[key].([]any)
			for _, item := range list {
				if name, ok := item.(string); ok && name != "" {
					rule.Tables = append(rule.Tables, name)
				}
			}
		default:
			return Rule{}, "has the unknown key " + quoted(key) + " (did you mean phrase or tables?)"
		}
	}
	if rules.Normalize(rule.Phrase) == "" {
		return Rule{}, "has no phrase"
	}
	if len(rule.Tables) == 0 {
		return Rule{}, fmt.Sprintf("(%s) names no tables", quoted(rule.Phrase))
	}
	return rule, ""
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
