package chat

import (
	"strconv"
	"strings"

	"github.com/dal-go/dalgo/dtql"
	"gopkg.in/yaml.v3"
)

const savedQueryLookupLimit = 100

// SavedQueryLookupPlan is derived only from source-scoped SQLite FK evidence.
type SavedQueryLookupPlan struct {
	Relation string
	Key      string
	Columns  []string
	Multi    bool
}

// DiscoverSavedQueryLookup declines ambiguous and composite relationships;
// these cannot safely fill one scalar parameter.
func DiscoverSavedQueryLookup(snapshot ForeignKeySnapshot, entity, field string) *SavedQueryLookupPlan {
	if snapshot.Source == "" || entity == "" || field == "" {
		return nil
	}
	var match *ForeignKey
	for i := range snapshot.Keys {
		fk := &snapshot.Keys[i]
		if !sameLookupRelation(fk.Schema, fk.FromRelation, entity) || len(fk.FromFields) == 0 || !strings.EqualFold(fk.FromFields[0], field) {
			continue
		}
		if match != nil {
			return nil
		}
		match = fk
	}
	if match == nil || len(match.FromFields) != 1 || len(match.ToFields) != 1 || match.ToRelation == "" || match.ToFields[0] == "" {
		return nil
	}
	return savedQueryLookupPlan(snapshot, match)
}

// DiscoverSavedQueryParameterLookup uses the saved DTQL equality predicate to
// locate the parameter's source column, then checks Meta against the target
// of that exact FK. ID is accepted as a semantic shorthand only for a real
// target key named <Entity>Id in the FK snapshot.
func DiscoverSavedQueryParameterLookup(doc []byte, snapshot ForeignKeySnapshot, parameterID, entity, field string) *SavedQueryLookupPlan {
	return DiscoverSavedQueryParameterLookupWithMode(doc, snapshot, parameterID, entity, field, false)
}

func DiscoverSavedQueryParameterLookupWithMode(doc []byte, snapshot ForeignKeySnapshot, parameterID, entity, field string, multi bool) *SavedQueryLookupPlan {
	if snapshot.Source == "" || parameterID == "" || entity == "" || field == "" {
		return nil
	}
	var query struct {
		From struct {
			Schema string `yaml:"schema"`
			Name   string `yaml:"name"`
			Alias  string `yaml:"alias"`
		} `yaml:"from"`
		Where struct {
			Op   string `yaml:"op"`
			Left struct {
				Field  string `yaml:"field"`
				Param  string `yaml:"param"`
				Source string `yaml:"source"`
			} `yaml:"left"`
			Right struct {
				Field  string `yaml:"field"`
				Param  string `yaml:"param"`
				Source string `yaml:"source"`
			} `yaml:"right"`
		} `yaml:"where"`
	}
	if err := yaml.Unmarshal(doc, &query); err != nil {
		return nil
	}
	if multi {
		if query.Where.Op != "In" && query.Where.Op != "NotIn" {
			return nil
		}
	} else if query.Where.Op != "==" && query.Where.Op != "=" {
		return nil
	}
	if _, err := dtql.Deserialize(doc); err != nil {
		return nil
	}
	bound := query.Where.Left
	if query.Where.Right.Param == parameterID && query.Where.Left.Param == "" {
		bound = query.Where.Left
	} else if query.Where.Left.Param == parameterID && query.Where.Right.Param == "" {
		bound = query.Where.Right
	} else {
		return nil
	}
	if bound.Field == "" || (bound.Source != "" && !strings.EqualFold(bound.Source, query.From.Alias) && !strings.EqualFold(bound.Source, query.From.Name)) {
		return nil
	}
	var match *ForeignKey
	for i := range snapshot.Keys {
		fk := &snapshot.Keys[i]
		if !sameLookupRelation(fk.Schema, fk.FromRelation, qualifiedLookupRelation(query.From.Schema, query.From.Name)) || len(fk.FromFields) == 0 || !strings.EqualFold(fk.FromFields[0], bound.Field) || !sameLookupRelation(fk.ToSchema, fk.ToRelation, entity) || len(fk.ToFields) == 0 || !lookupTargetFieldMatches(fk.ToRelation, fk.ToFields[0], field) {
			continue
		}
		if match != nil {
			return nil
		}
		match = fk
	}
	if match == nil || len(match.FromFields) != 1 || len(match.ToFields) != 1 {
		return nil
	}
	plan := savedQueryLookupPlan(snapshot, match)
	if plan != nil {
		plan.Multi = multi
	}
	return plan
}

func qualifiedLookupRelation(schema, relation string) string {
	if schema == "" {
		return relation
	}
	return schema + "." + relation
}

func sameLookupRelation(schema, relation, reference string) bool {
	parts := strings.Split(reference, ".")
	if len(parts) == 1 {
		return strings.EqualFold(relation, parts[0])
	}
	return len(parts) == 2 && strings.EqualFold(schema, parts[0]) && strings.EqualFold(relation, parts[1])
}

func lookupTargetFieldMatches(relation, key, field string) bool {
	return strings.EqualFold(key, field) || (strings.EqualFold(field, "ID") && strings.EqualFold(key, relation+"Id"))
}

func savedQueryLookupPlan(snapshot ForeignKeySnapshot, match *ForeignKey) *SavedQueryLookupPlan {
	if match == nil || len(match.FromFields) != 1 || len(match.ToFields) != 1 || match.ToRelation == "" || match.ToFields[0] == "" {
		return nil
	}
	columns := snapshot.Columns[relationKey(match.ToSchema, match.ToRelation)]
	key := ""
	for _, column := range columns {
		if strings.EqualFold(column, match.ToFields[0]) {
			key = column
			break
		}
	}
	if key == "" {
		return nil
	}
	plan := &SavedQueryLookupPlan{Relation: match.ToRelation, Key: key, Columns: []string{key}}
	for _, column := range columns {
		if strings.EqualFold(column, key) {
			continue
		}
		plan.Columns = append(plan.Columns, column)
		if len(plan.Columns) == 4 {
			break
		}
	}
	return plan
}

func (p SavedQueryLookupPlan) Document() []byte {
	var doc strings.Builder
	doc.WriteString("from: {name: " + strconv.Quote(p.Relation) + "}\ncolumns:\n")
	for _, column := range p.Columns {
		doc.WriteString("  - field: " + strconv.Quote(column) + "\n")
	}
	doc.WriteString("orderBy:\n  - field: " + strconv.Quote(p.Key) + "\n")
	doc.WriteString("limit: " + strconv.Itoa(savedQueryLookupLimit) + "\n")
	return []byte(doc.String())
}
