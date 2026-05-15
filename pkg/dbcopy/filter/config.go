package filter

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// configFile mirrors the YAML schema documented in
// spec/features/cli/db/copy/filtering/README.md#req:config-file-schema.
// All fields are optional. Two reserved keys (`columns:` and nested
// `or:`) are NOT modeled here — they're rejected via the generic-map
// scan in ParseConfigFile before this struct decodes.
type configFile struct {
	Include []string                      `yaml:"include"`
	Exclude []string                      `yaml:"exclude"`
	Where   map[string][]configWhereEntry `yaml:"where"`
	Limit   map[string]int                `yaml:"limit"`
}

type configWhereEntry struct {
	Field string `yaml:"field,omitempty"`
	Op    string `yaml:"op,omitempty"`
	Value string `yaml:"value,omitempty"`
}

// ParseConfigFile reads and decodes the YAML at path into a Directives.
// Rejects unrecognized top-level keys (REQ:config-file-schema). The
// `columns:` (top-level) and nested `or:` (inside where:<table>:) keys
// are recognized but reserved for deferred follow-up Features and MUST
// produce errors naming the deferral.
func ParseConfigFile(path string) (*Directives, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	// Decode into a generic map first to detect unknown top-level keys
	// AND the two reserved keys (`columns:`, nested `or:`).
	var generic map[string]any
	if err := yaml.Unmarshal(data, &generic); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}
	if _, hasColumns := generic["columns"]; hasColumns {
		return nil, fmt.Errorf(
			"top-level `columns:` key is reserved for a deferred follow-up Feature " +
				"(`cli/db/copy/filtering/columns/`); column subsetting is not in MVP",
		)
	}
	known := map[string]bool{"include": true, "exclude": true, "where": true, "limit": true}
	for k := range generic {
		if !known[k] {
			return nil, fmt.Errorf("unrecognized top-level key %q (allowed: include, exclude, where, limit)", k)
		}
	}

	// Walk where: entries and reject any reserved `or:` subkey.
	if whereRaw, ok := generic["where"].(map[string]any); ok {
		for table, entries := range whereRaw {
			list, ok := entries.([]any)
			if !ok {
				continue
			}
			for _, entry := range list {
				m, ok := entry.(map[string]any)
				if !ok {
					continue
				}
				if _, hasOr := m["or"]; hasOr {
					return nil, fmt.Errorf(
						"nested `or:` key under `where:%s:` is reserved for a deferred follow-up Feature "+
							"(OR-groups in row predicates); MVP supports only AND-composed conditions per table",
						table,
					)
				}
			}
		}
	}

	var cf configFile
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&cf); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	d := &Directives{
		IncludeTables: cf.Include,
		ExcludeTables: cf.Exclude,
		LimitsByTable: cf.Limit,
	}

	if len(cf.Where) > 0 {
		d.Where = map[string]*PredicateGroup{}
		for table, entries := range cf.Where {
			grp := &PredicateGroup{Operator: And}
			for _, e := range entries {
				grp.Conditions = append(grp.Conditions, Predicate{
					Field:    e.Field,
					Operator: OperatorToken(e.Op),
					Value:    e.Value,
				})
			}
			d.Where[table] = grp
		}
	}

	return d, nil
}
