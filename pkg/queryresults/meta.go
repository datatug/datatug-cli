package queryresults

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
)

// Query kinds, policy modes and unavailable reasons of meta.json.
const (
	QueryKindFrom  = "from"
	QueryKindDTQL  = "dtql"
	QueryKindSaved = "saved"

	// Languages of query.text: what the query editor reads it as.
	LanguageDTQL = "dtql"
	LanguageSQL  = "sql"
	LanguageHTTP = "http"

	PolicyNone       = "none"
	PolicyDiscovered = "discovered"
	PolicyDir        = "dir"
	PolicyExplicit   = "explicit"
	PolicyProject    = "project"

	UnavailableTooLarge      = "too-large"
	UnavailableTooMany       = "too-many"
	UnavailableNativeSQL     = "native-sql"
	UnavailableNoRelMetadata = "no-relationship-metadata"
	UnavailableUnreadable    = "schema-unreadable"

	// MaxParticipants and MaxRelated bound those lists of meta.json.
	MaxParticipants = 32
	MaxRelated      = 200
)

// Meta is meta.json: everything needed to draw a result except its rows. It
// holds no row value, connection string, file path, host, user, password,
// environment variable or policy path.
type Meta struct {
	Format     string      `json:"format"`
	Version    int         `json:"version"`
	ID         string      `json:"id"`
	CreatedAt  time.Time   `json:"createdAt"`
	ExpiresAt  time.Time   `json:"expiresAt"`
	CLIVersion string      `json:"cliVersion"`
	DataBytes  int64       `json:"dataBytes"`
	DB         DB          `json:"db"`
	Query      Query       `json:"query"`
	Rows       Rows        `json:"rows"`
	Provenance *Provenance `json:"provenance"`
	Columns    []Column    `json:"columns"`
	// Participants are the query's source occurrences and Related the tables
	// that can be joined to them.
	Participants []Participant `json:"participants"`
	Related      []Related     `json:"related"`
	// Unavailable names, per part, why it is missing or cut short.
	Unavailable Unavailable `json:"unavailable"`
	// Prompt is the request that produced the result, set by `query results
	// annotate`; absent until then and in results written before it existed.
	Prompt *Prompt `json:"prompt,omitempty"`
}

// Prompt is the text a person or an agent asked for the result with.
type Prompt struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
	// At is when it was recorded, RFC 3339 in UTC.
	At string `json:"at"`
}

// DB says which database the query ran on, by its project reference only.
type DB struct {
	// Ref is datatug://<project>/<db>?env=<env>, or nil when the database is
	// not a source of a registered project.
	Ref     *string `json:"ref"`
	Project *string `json:"project"`
	Env     *string `json:"env"`
	Source  *string `json:"source"`
	Driver  string  `json:"driver"`
}

// Query is what ran: enough to run it again.
type Query struct {
	Kind           string                                 `json:"kind"`
	DTQL           string                                 `json:"dtql,omitempty"`
	From           string                                 `json:"from,omitempty"`
	SavedQueryID   string                                 `json:"savedQueryId,omitempty"`
	SavedQueryType string                                 `json:"savedQueryType,omitempty"`
	Parameters     map[string]apicontract.TypedValueOrSet `json:"parameters,omitempty"`
	Policy         string                                 `json:"policy"`
	// Text, Language, DBRef and Params make the query openable in the query
	// editor: the canonical text the editor accepts, its language ("dtql",
	// "sql" or "http"), the datatug:// reference of the database (absent when
	// the run was not against a project source) and the run's parameters as
	// name/value pairs. They are additive: readers that predate them ignore
	// them and results written before them lack them.
	Text     string       `json:"text,omitempty"`
	Language string       `json:"language,omitempty"`
	DBRef    string       `json:"dbRef,omitempty"`
	Params   []QueryParam `json:"params,omitempty"`
}

// QueryParam is one parameter of a run: its name and typed value (a scalar or
// a set), as in query.parameters.
type QueryParam struct {
	Name  string                      `json:"name"`
	Value apicontract.TypedValueOrSet `json:"value"`
}

// SetEditor fills the editor-ready fields of the query from what the run
// already holds: db.ref and query.parameters. text is "" when the language
// has no editable text.
func (m *Meta) SetEditor(text, language string) {
	m.Query.Text, m.Query.Language, m.Query.DBRef, m.Query.Params = text, language, "", nil
	if m.DB.Ref != nil {
		m.Query.DBRef = *m.DB.Ref
	}
	names := make([]string, 0, len(m.Query.Parameters))
	for name := range m.Query.Parameters {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		m.Query.Params = append(m.Query.Params, QueryParam{Name: name, Value: m.Query.Parameters[name]})
	}
}

// Rows says how many rows data.json keeps and whether more exist.
type Rows struct {
	Kept        int    `json:"kept"`
	HasMore     bool   `json:"hasMore"`
	TruncatedBy string `json:"truncatedBy"`
}

// Provenance is whether an HTTP source answered live or from a snapshot.
type Provenance struct {
	Mode       string `json:"mode"`
	ObservedAt string `json:"observedAt"`
}

// Column describes one column of data.json. The declared facts come from the
// database's schema, never from the values; they are null where unknown.
type Column struct {
	Name          string        `json:"name"`
	Type          string        `json:"type"`
	DeclaredType  *string       `json:"declaredType"`
	Nullable      *bool         `json:"nullable"`
	PK            *int          `json:"pk"`
	Source        *ColumnSource `json:"source"`
	ForeignKeys   []ColumnFK    `json:"foreignKeys"`
	Encoding      string        `json:"encoding,omitempty"`
	CutCells      int           `json:"cutCells,omitempty"`
	ObservedTypes []string      `json:"observedTypes,omitempty"`
}

// ColumnFacts are the schema facts of a column that is a direct reference to
// one source's field.
type ColumnFacts struct {
	DeclaredType string
	// ContractType is the declared type in the contract's closed set, or "".
	ContractType string
	Nullable     bool
	PK           int // 1-based position in the primary key, 0 when not in it
	Source       ColumnSource
	ForeignKeys  []ColumnFK
}

// ColumnSource is the source field a column reads.
type ColumnSource struct {
	Instance string `json:"instance"`
	Schema   string `json:"schema"`
	Relation string `json:"relation"`
	Column   string `json:"column"`
}

// ColumnFK is a foreign key a column takes part in.
type ColumnFK struct {
	RefSchema   string `json:"refSchema"`
	RefRelation string `json:"refRelation"`
	RefColumn   string `json:"refColumn"`
}

// Participant is one source occurrence of the query.
type Participant struct {
	Instance string           `json:"instance"`
	Schema   string           `json:"schema"`
	Relation string           `json:"relation"`
	Alias    string           `json:"alias"`
	DB       *string          `json:"db,omitempty"`
	Kind     string           `json:"kind"`
	Role     string           `json:"role"`
	Join     *ParticipantJoin `json:"join,omitempty"`
}

// ParticipantJoin is how a participant is joined.
type ParticipantJoin struct {
	Type string     `json:"type"`
	On   []JoinPair `json:"on"`
}

// JoinPair is one equality of a join: alias.field on each side.
type JoinPair struct {
	Left  string `json:"left"`
	Right string `json:"right"`
}

// Related is one table that can be joined to a participant by a foreign key.
type Related struct {
	CandidateID  string         `json:"candidateId"`
	FromInstance string         `json:"fromInstance"`
	Target       RelatedTarget  `json:"target"`
	ConstraintID string         `json:"constraintId"`
	Direction    string         `json:"direction"`
	Cardinality  string         `json:"cardinality"`
	Fields       []RelatedField `json:"fields"`
	Evidence     string         `json:"evidence"`
}

// RelatedTarget is the table a related candidate joins.
type RelatedTarget struct {
	Schema   string `json:"schema"`
	Relation string `json:"relation"`
	Kind     string `json:"kind"`
}

// RelatedField pairs a field of the participant with one of the target.
type RelatedField struct {
	SourceField string `json:"sourceField"`
	TargetField string `json:"targetField"`
}

// Unavailable holds a reason code per part of the metadata that is missing.
type Unavailable struct {
	Query         string `json:"query,omitempty"`
	Columns       string `json:"columns,omitempty"`
	Participants  string `json:"participants,omitempty"`
	Related       string `json:"related,omitempty"`
	DeclaredTypes string `json:"declaredTypes,omitempty"`
}

// Describe sets the declared facts of each column found in facts.
func (m *Meta) Describe(facts map[string]ColumnFacts) {
	for i := range m.Columns {
		fact, ok := facts[m.Columns[i].Name]
		if !ok {
			continue
		}
		column := &m.Columns[i]
		declared, nullable, source := fact.DeclaredType, fact.Nullable, fact.Source
		column.DeclaredType, column.Nullable, column.Source = &declared, &nullable, &source
		if fact.PK > 0 {
			pk := fact.PK
			column.PK = &pk
		}
		column.ForeignKeys = append([]ColumnFK{}, fact.ForeignKeys...)
	}
}

// NewMeta starts the meta.json of id written now.
func NewMeta(id, cliVersion string, data Data) Meta {
	created := now().UTC()
	return Meta{
		Format:     MetaFormat,
		Version:    Version,
		ID:         id,
		CreatedAt:  created,
		ExpiresAt:  created.Add(Retention),
		CLIVersion: cliVersion,
		DataBytes:  int64(len(data.Bytes)),
		Rows:       data.Rows,
		Columns:    data.Columns,
	}
}

// Encode applies the bounds of meta.json and encodes it: query.dtql and
// query.parameters together within MaxQueryBytes (else both are dropped,
// "too-large"), at most MaxMetaColumns columns, and the whole within
// MaxMetaBytes (else the column descriptions are dropped).
func (m Meta) Encode() []byte {
	// Both are measured as they are encoded, escapes included.
	text, _ := json.Marshal(m.Query.DTQL)
	parameters, _ := json.Marshal(m.Query.Parameters)
	editorText, _ := json.Marshal(m.Query.Text)
	params, _ := json.Marshal(m.Query.Params)
	if len(text)+len(parameters)+len(editorText)+len(params) > MaxQueryBytes {
		m.Query.DTQL, m.Query.Parameters, m.Query.Text, m.Query.Params = "", nil, "", nil
		m.Unavailable.Query = UnavailableTooLarge
	}
	if m.Participants == nil {
		m.Participants = []Participant{}
	}
	if m.Related == nil {
		m.Related = []Related{}
	}
	if len(m.Participants) > MaxParticipants {
		m.Participants = m.Participants[:MaxParticipants]
		m.Unavailable.Participants = UnavailableTooMany
	}
	if len(m.Related) > MaxRelated {
		m.Related = m.Related[:MaxRelated]
		m.Unavailable.Related = UnavailableTooMany
	}
	if len(m.Columns) > MaxMetaColumns {
		m.Columns = m.Columns[:MaxMetaColumns]
		m.Unavailable.Columns = UnavailableTooMany
	}
	encoded, _ := json.Marshal(m)
	if len(encoded) > MaxMetaBytes-MaxPromptJSONBytes {
		m.Columns, m.Participants, m.Related = []Column{}, []Participant{}, []Related{}
		m.Unavailable.Columns, m.Unavailable.Participants, m.Unavailable.Related = UnavailableTooMany, UnavailableTooMany, UnavailableTooMany
		encoded, _ = json.Marshal(m)
	}
	return encoded
}

// Locator is the path-free line `query run --result-id` prints: it holds no
// id, no path and no row.
func LocatorLine(data Data) []byte {
	encoded, _ := json.Marshal(struct {
		ResultFiles struct {
			Version int `json:"version"`
		} `json:"resultFiles"`
		RowsKept int  `json:"rowsKept"`
		HasMore  bool `json:"hasMore"`
		Columns  int  `json:"columns"`
	}{ResultFiles: struct {
		Version int `json:"version"`
	}{Version}, RowsKept: data.Rows.Kept, HasMore: data.Rows.HasMore, Columns: len(data.Columns)})
	return append(encoded, '\n')
}

// FailureLine is the path-free line printed when a --result-id run fails.
func FailureLine(reason string) []byte {
	encoded, _ := json.Marshal(struct {
		ResultFiles *struct{} `json:"resultFiles"`
		Reason      string    `json:"reason"`
	}{nil, reason})
	return append(encoded, '\n')
}

// LocateLine is the answer of `query results locate`: the paths of a complete
// result, or null with the reason.
func LocateLine(locator *Locator, reason string) []byte {
	var encoded []byte
	if locator != nil {
		encoded, _ = json.Marshal(struct {
			ResultFiles *Locator `json:"resultFiles"`
		}{locator})
	} else {
		encoded = FailureLine(reason)
		encoded = encoded[:len(encoded)-1]
	}
	return append(encoded, '\n')
}
