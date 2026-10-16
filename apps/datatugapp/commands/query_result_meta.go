package commands

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/dtql"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-cli/pkg/joinmeta"
	"github.com/datatug/datatug-cli/pkg/queryresults"
	"github.com/datatug/datatug-core/pkg/apicontract"
)

// resultDescription is what the schema reads add to meta.json: the declared
// facts of the columns, the participants and the related tables. It never
// holds a row value: only the query's tree and the database's schema are read.
type resultDescription struct {
	declared     map[string]string
	facts        map[string]queryresults.ColumnFacts
	participants []queryresults.Participant
	related      []queryresults.Related
	unavailable  queryresults.Unavailable
}

// dtqlSources opens the database a participant of a DTQL query is in ("" is
// the query's own database; a federated query names others). ref is the
// datatug:// reference of a named database, nil when it has none; a nil ref
// gives no participant a reference.
type dtqlSources struct {
	open func(ctx context.Context, database string) (db dal.DB, driver string, release func(), err error)
	ref  func(database string) *string
}

// snapshotState is the foreign-key snapshot of one database, or why there is none.
type snapshotState struct {
	snapshot joinmeta.ForeignKeySnapshot
	driver   string
	reason   string
}

// snapshotReason is the meta.json reason code of a failed snapshot.
func snapshotReason(err error) string {
	var notSupported *dbschema.NotSupportedError
	switch {
	case errors.As(err, &notSupported):
		return queryresults.UnavailableNoRelMetadata
	case errors.Is(err, joinmeta.ErrTooManyCollections):
		return queryresults.UnavailableTooMany
	default:
		return queryresults.UnavailableUnreadable
	}
}

// schemaOr is schema, or the default schema of the driver when it is empty.
func schemaOr(schema, driver string) string {
	if schema != "" {
		return schema
	}
	return api.DefaultSchemaForDriver(driver)
}

// describeDTQL describes a DTQL result: the participants from the query's
// tree, the related tables from DiscoverJoinCandidates over each database's
// foreign keys (never filtered by permission), and the declared facts of the
// columns that read one source's field directly.
func describeDTQL(ctx context.Context, text string, columns []string, sources dtqlSources) resultDescription {
	description := resultDescription{declared: map[string]string{}, facts: map[string]queryresults.ColumnFacts{}}
	query, err := dtql.Deserialize([]byte(text))
	if err != nil {
		description.unavailable = queryresults.Unavailable{Participants: queryresults.UnavailableUnreadable, Related: queryresults.UnavailableUnreadable, DeclaredTypes: queryresults.UnavailableUnreadable}
		return description
	}
	instances := joinmeta.RelationInstances(query.From())
	states := map[string]*snapshotState{}
	load := func(database string) *snapshotState {
		if state := states[database]; state != nil {
			return state
		}
		state := &snapshotState{}
		states[database] = state
		db, driver, release, err := sources.open(ctx, database)
		state.driver = driver
		if err == nil {
			state.snapshot, err = joinmeta.LoadForeignKeySnapshot(ctx, db, "result")
			release()
		}
		if err != nil {
			state.reason = snapshotReason(err)
		}
		return state
	}
	joins := joinsOf(query.From())
	for _, instance := range instances {
		state := load(instance.Database)
		participant := queryresults.Participant{
			Instance: string(instance.ID), Schema: schemaOr(instance.Schema, state.driver), Relation: instance.Relation, Alias: instance.Alias,
			Kind: state.snapshot.Kind(instance.Schema, instance.Relation), Role: "join", Join: joins[instance.ID],
		}
		if instance.ID == "root" {
			participant.Role = "from"
		}
		if instance.Database != "" && sources.ref != nil {
			participant.DB = sources.ref(instance.Database)
		}
		description.participants = append(description.participants, participant)
	}
	description.related, description.unavailable.Related = relatedOf(text, states)
	description.unavailable.DeclaredTypes = describeColumns(query, instances, states, columns, description)
	return description
}

// relatedOf lists the join candidates of every participant from the foreign
// keys of its own database, and the reason when a database could not say.
func relatedOf(text string, states map[string]*snapshotState) ([]queryresults.Related, string) {
	related := []queryresults.Related{}
	reason := ""
	databases := make([]string, 0, len(states))
	for database := range states {
		databases = append(databases, database)
	}
	sort.Strings(databases)
	for _, database := range databases {
		state := states[database]
		if state.reason == "" && len(state.snapshot.Keys) == 0 {
			state.reason = queryresults.UnavailableNoRelMetadata
		}
		if state.reason != "" {
			reason = state.reason
			continue
		}
		candidates, err := chat.DiscoverJoinCandidates([]byte(text), state.snapshot)
		if err != nil {
			reason = queryresults.UnavailableUnreadable
			continue
		}
		for _, candidate := range candidates {
			if candidate.Source.Database != database {
				continue
			}
			item := queryresults.Related{
				CandidateID: string(candidate.ID), FromInstance: string(candidate.Source.ID),
				Target:       queryresults.RelatedTarget{Schema: schemaOr(candidate.Target.Schema, state.driver), Relation: candidate.Target.Relation, Kind: state.snapshot.Kind(candidate.Target.Schema, candidate.Target.Relation)},
				ConstraintID: candidate.ConstraintID, Direction: candidate.Direction, Cardinality: candidate.Cardinality, Evidence: candidate.Evidence,
			}
			for _, pair := range candidate.Fields {
				item.Fields = append(item.Fields, queryresults.RelatedField{SourceField: pair.SourceField, TargetField: pair.TargetField})
			}
			related = append(related, item)
		}
	}
	sort.SliceStable(related, func(i, j int) bool {
		a, b := related[i], related[j]
		if a.FromInstance != b.FromInstance {
			return a.FromInstance < b.FromInstance
		}
		if a.Direction != b.Direction {
			return a.Direction > b.Direction // outgoing before incoming
		}
		if a.Target.Relation != b.Target.Relation {
			return a.Target.Relation < b.Target.Relation
		}
		return a.ConstraintID < b.ConstraintID
	})
	return related, reason
}

// joinsOf is how each joined participant is joined: its type and the
// field equalities of its ON clause, written alias.field.
func joinsOf(from dal.FromSource) map[joinmeta.RelationInstanceID]*queryresults.ParticipantJoin {
	joins := map[joinmeta.RelationInstanceID]*queryresults.ParticipantJoin{}
	var walk func(dal.FromSource, string)
	walk = func(node dal.FromSource, path string) {
		for i, join := range node.Joins() {
			childPath := fmt.Sprintf("%s/%d", path, i)
			described := &queryresults.ParticipantJoin{Type: strings.ToLower(string(join.JoinType())), On: []queryresults.JoinPair{}}
			for _, condition := range join.On() {
				comparison, ok := condition.(dal.Comparison)
				if !ok || comparison.Operator != dal.Equal {
					continue
				}
				left, leftOK := comparison.Left.(dal.FieldRef)
				right, rightOK := comparison.Right.(dal.FieldRef)
				if leftOK && rightOK {
					described.On = append(described.On, queryresults.JoinPair{Left: qualified(left), Right: qualified(right)})
				}
			}
			joins[joinmeta.RelationInstanceID(childPath)] = described
			child := join.From()
			if child == nil {
				child = dal.From(join.RecordsetSource)
			}
			walk(child, childPath)
		}
	}
	walk(from, "root")
	return joins
}

func qualified(field dal.FieldRef) string {
	if field.Source() == "" {
		return field.Name()
	}
	return field.Source() + "." + field.Name()
}

// describeColumns sets the declared facts of each column that reads one
// source's field directly, and answers the reason when such a column's
// definition could not be read.
func describeColumns(query dal.StructuredQuery, instances []joinmeta.RelationInstance, states map[string]*snapshotState, columns []string, description resultDescription) string {
	byAlias := map[string]joinmeta.RelationInstance{}
	for _, instance := range instances {
		alias := instance.Alias
		if alias == "" {
			alias = instance.Relation
		}
		byAlias[strings.ToLower(alias)] = instance
	}
	single := len(instances) == 1
	instanceOf := func(source string) (joinmeta.RelationInstance, bool) {
		if source == "" {
			return instances[0], single
		}
		instance, ok := byAlias[strings.ToLower(source)]
		return instance, ok
	}
	type reference struct {
		instance joinmeta.RelationInstance
		field    string
	}
	direct := map[string]reference{}
	var wildcards []joinmeta.RelationInstance
	if len(query.Columns()) == 0 && single {
		wildcards = append(wildcards, instances[0])
	}
	for _, column := range query.Columns() {
		if column.Wildcard != nil {
			if instance, ok := instanceOf(column.Wildcard.Source); ok {
				wildcards = append(wildcards, instance)
			}
			continue
		}
		field, ok := column.Expression.(dal.FieldRef)
		if !ok {
			continue
		}
		name := column.Alias
		if name == "" {
			name = field.Name()
		}
		if instance, found := instanceOf(field.Source()); found {
			direct[name] = reference{instance: instance, field: field.Name()}
		}
	}
	reason := ""
	for _, name := range columns {
		state, def, ref := (*snapshotState)(nil), (*dbschema.CollectionDef)(nil), reference{}
		if found, ok := direct[name]; ok {
			ref = found
			state = states[ref.instance.Database]
			def = state.snapshot.Definitions[joinmeta.RelationKey(ref.instance.Schema, ref.instance.Relation)]
			if def == nil {
				reason = state.reason
				if reason == "" {
					reason = queryresults.UnavailableUnreadable
				}
				continue
			}
		} else {
			for _, instance := range wildcards {
				candidate := states[instance.Database]
				if candidateDef := candidate.snapshot.Definitions[joinmeta.RelationKey(instance.Schema, instance.Relation)]; candidateDef != nil && fieldOf(candidateDef, name) != nil {
					state, def, ref = candidate, candidateDef, reference{instance: instance, field: name}
					break
				} else if candidateDef == nil {
					reason = candidate.reason
					if reason == "" {
						reason = queryresults.UnavailableUnreadable
					}
				}
			}
		}
		if def == nil {
			continue
		}
		field := fieldOf(def, ref.field)
		if field == nil {
			continue
		}
		facts := queryresults.ColumnFacts{
			DeclaredType: declaredTypeOf(*field), ContractType: contractTypeOf(field.Type), Nullable: field.Nullable,
			Source:      queryresults.ColumnSource{Instance: string(ref.instance.ID), Schema: schemaOr(ref.instance.Schema, state.driver), Relation: ref.instance.Relation, Column: string(field.Name)},
			ForeignKeys: []queryresults.ColumnFK{},
		}
		for i, key := range def.PrimaryKey {
			if strings.EqualFold(string(key), string(field.Name)) {
				facts.PK = i + 1
			}
		}
		for _, key := range state.snapshot.Keys {
			if !strings.EqualFold(key.FromRelation, def.Name) || joinmeta.RelationKey(key.Schema, "") != joinmeta.RelationKey(ref.instance.Schema, "") {
				continue
			}
			for i, from := range key.FromFields {
				if strings.EqualFold(from, string(field.Name)) {
					facts.ForeignKeys = append(facts.ForeignKeys, queryresults.ColumnFK{RefSchema: schemaOr(key.ToSchema, state.driver), RefRelation: key.ToRelation, RefColumn: key.ToFields[i]})
				}
			}
		}
		description.facts[name] = facts
		if facts.ContractType != "" {
			description.declared[name] = facts.ContractType
		}
	}
	return reason
}

func fieldOf(def *dbschema.CollectionDef, name string) *dbschema.FieldDef {
	for i := range def.Fields {
		if strings.EqualFold(string(def.Fields[i].Name), name) {
			return &def.Fields[i]
		}
	}
	return nil
}

// declaredTypeOf is the portable type of a field with its length or precision.
func declaredTypeOf(field dbschema.FieldDef) string {
	text := field.Type.String()
	switch {
	case field.Precision != nil:
		text += fmt.Sprintf("(%d,%d)", field.Precision.Total, field.Precision.Scale)
	case field.Length != nil:
		text += fmt.Sprintf("(%d)", *field.Length)
	}
	return text
}

// contractTypeOf maps a portable type to the contract's closed type set; ""
// when it has no counterpart (bytes, null).
func contractTypeOf(t dbschema.Type) string {
	switch t {
	case dbschema.Bool:
		return string(apicontract.ValueTypeBoolean)
	case dbschema.Int:
		return string(apicontract.ValueTypeInteger)
	case dbschema.Float:
		return string(apicontract.ValueTypeNumber)
	case dbschema.String:
		return string(apicontract.ValueTypeString)
	case dbschema.Time:
		return string(apicontract.ValueTypeDatetime)
	case dbschema.Decimal:
		return string(apicontract.ValueTypeDecimal)
	default:
		return ""
	}
}

// describeNativeSQL describes a saved native SQL result: nothing parses SQL,
// so it has no participants and no related tables; column types are the
// executor's when it reports them.
func describeNativeSQL(columnTypes map[string]string) resultDescription {
	description := resultDescription{declared: map[string]string{}, unavailable: queryresults.Unavailable{Participants: queryresults.UnavailableNativeSQL, Related: queryresults.UnavailableNativeSQL}}
	for name, typ := range columnTypes {
		if (apicontract.Column{Name: name, Type: typ}).Validate() == nil {
			description.declared[name] = typ
		}
	}
	if len(description.declared) == 0 {
		description.unavailable.DeclaredTypes = queryresults.UnavailableNativeSQL
	}
	return description
}

// describeHTTP describes a saved HTTP result: one participant, the collection
// it read; an HTTP source reports no schema.
func describeHTTP(collection string) resultDescription {
	return resultDescription{
		participants: []queryresults.Participant{{Instance: "root", Relation: collection, Kind: "unknown", Role: "from"}},
		unavailable:  queryresults.Unavailable{Related: queryresults.UnavailableNoRelMetadata, DeclaredTypes: queryresults.UnavailableNoRelMetadata},
	}
}
