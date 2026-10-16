package queryresults

import (
	"encoding/json"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
)

// Query kinds, policy modes and unavailable reasons of meta.json.
const (
	QueryKindFrom  = "from"
	QueryKindDTQL  = "dtql"
	QueryKindSaved = "saved"

	PolicyNone       = "none"
	PolicyDiscovered = "discovered"
	PolicyDir        = "dir"
	PolicyExplicit   = "explicit"
	PolicyProject    = "project"

	UnavailableTooLarge = "too-large"
	UnavailableTooMany  = "too-many"
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
	// Unavailable names, per part, why it is missing or cut short.
	Unavailable Unavailable `json:"unavailable"`
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

// Column describes one column of data.json.
type Column struct {
	Name          string   `json:"name"`
	Type          string   `json:"type"`
	Encoding      string   `json:"encoding,omitempty"`
	CutCells      int      `json:"cutCells,omitempty"`
	ObservedTypes []string `json:"observedTypes,omitempty"`
}

// Unavailable holds a reason code per part of the metadata that is missing.
type Unavailable struct {
	Query   string `json:"query,omitempty"`
	Columns string `json:"columns,omitempty"`
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
	parameters, _ := json.Marshal(m.Query.Parameters)
	if len(m.Query.DTQL)+len(parameters) > MaxQueryBytes {
		m.Query.DTQL, m.Query.Parameters = "", nil
		m.Unavailable.Query = UnavailableTooLarge
	}
	if len(m.Columns) > MaxMetaColumns {
		m.Columns = m.Columns[:MaxMetaColumns]
		m.Unavailable.Columns = UnavailableTooMany
	}
	encoded, _ := json.Marshal(m)
	if len(encoded) > MaxMetaBytes {
		m.Columns = []Column{}
		m.Unavailable.Columns = UnavailableTooMany
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
