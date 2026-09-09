package apicontract_local

// Scope identifies one authorized request context: api-contract.md "Scope
// and identity". Project and Environment are required, nonempty project-
// local IDs; SecurityContextID is the opaque staleness token agent-info
// issues (never authentication/authority on its own).
type Scope struct {
	Project           string `json:"project"`
	Environment       string `json:"environment"`
	SecurityContextID string `json:"securityContextId"`
}

// SourceRef identifies one collection within one registered project source:
// api-contract.md "Scope and identity". Source is a stable project-local ID
// resolved through the project's source registry for Scope.Environment —
// see pkg/api's resolver — never a filesystem path, URL or credential.
type SourceRef struct {
	Source     string `json:"source"`
	Collection string `json:"collection"`
}

// PhysicalRef is one physical column an EntityField maps to.
type PhysicalRef struct {
	Source     string `json:"source"`
	Collection string `json:"collection"`
	Column     string `json:"column"`
}

// FactOrigin is Fact.Origin's closed set.
type FactOrigin string

const (
	OriginSelection FactOrigin = "selection"
	OriginContext   FactOrigin = "context"
	OriginManual    FactOrigin = "manual"
)

// Fact is one typed semantic value the browser holds (a grid selection, an
// Investigation Context item): api-contract.md "Shared JSON types".
type Fact struct {
	ID       string       `json:"id"`
	Entity   string       `json:"entity"`
	Field    string       `json:"field"`
	Value    TypedValue   `json:"value"`
	Origin   FactOrigin   `json:"origin"`
	Physical *PhysicalRef `json:"physical,omitempty"`
	Mapping  string       `json:"mapping,omitempty"` // "declared" | "inferred"
	Enabled  bool         `json:"enabled"`
}

// Limitation reports one applied restriction — never the number or values
// of the rows/columns it restricted (api-contract.md "Shared JSON types").
type Limitation struct {
	Policy        string   `json:"policy"`
	RowsFiltered  bool     `json:"rowsFiltered"`
	HiddenColumns []string `json:"hiddenColumns"`
}

// BindingOrigin is Binding.Origin's closed set — one more member than
// FactOrigin ("default", a declared query default).
type BindingOrigin string

const (
	BindingOriginSelection BindingOrigin = "selection"
	BindingOriginContext   BindingOrigin = "context"
	BindingOriginManual    BindingOrigin = "manual"
	BindingOriginDefault   BindingOrigin = "default"
)

// OriginEvidence distinguishes a client-reported binding origin from an
// actual server-attested default (api-contract.md "Endpoint table" /
// "Binding and context behavior": "The UI must not present client origins
// as server-attested provenance").
type OriginEvidence string

const (
	EvidenceServerDefault  OriginEvidence = "server-default"
	EvidenceClientReported OriginEvidence = "client-reported"
)

// Binding is one query parameter's resolved value and its provenance.
type Binding struct {
	ParameterID    string         `json:"parameterId"`
	Value          TypedValue     `json:"value"`
	Origin         BindingOrigin  `json:"origin"`
	OriginEvidence OriginEvidence `json:"originEvidence"`
	FactID         string         `json:"factId,omitempty"`
}

// ExecutionProfile is Result.Provenance.ExecutionProfile's closed set
// (REQ:opaque-sql-limitation).
type ExecutionProfile string

const (
	ProfileProtected        ExecutionProfile = "protected"
	ProfileOpaquePrivileged ExecutionProfile = "opaque-privileged"
)

// ExecutionMode is Result.Provenance.Mode / ExecutionRequest.Mode's closed
// set.
type ExecutionMode string

const (
	ModeLive     ExecutionMode = "live"
	ModeSnapshot ExecutionMode = "snapshot"
)

// Column is one Result.Recordset column.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Recordset is Result.Recordset: ordered columns, and rows with exactly one
// TypedValue per returned column, in column order.
type Recordset struct {
	Columns []Column       `json:"columns"`
	Rows    [][]TypedValue `json:"rows"`
}

// Provenance is Result.Provenance.
type Provenance struct {
	Source           string           `json:"source"`
	Collection       string           `json:"collection,omitempty"`
	QueryID          string           `json:"queryId,omitempty"`
	Mode             ExecutionMode    `json:"mode"`
	SnapshotID       string           `json:"snapshotId,omitempty"`
	ObservedAt       string           `json:"observedAt"`
	ExecutionProfile ExecutionProfile `json:"executionProfile"`
}

// Result is the appendix's one execution/read response shape, returned by
// exec/run_query and semantic/related/rows.
type Result struct {
	Recordset       Recordset    `json:"recordset"`
	Limitations     []Limitation `json:"limitations"`
	BindingsApplied []Binding    `json:"bindingsApplied"`
	Provenance      Provenance   `json:"provenance"`
	Truncated       bool         `json:"truncated"`
}

// AgentInfoPrincipal is agent-info's principal object.
type AgentInfoPrincipal struct {
	ID     string   `json:"id"`
	Roles  []string `json:"roles"`
	Groups []string `json:"groups"`
}

// AgentInfoProject is one entry of agent-info's projects array.
type AgentInfoProject struct {
	ID string `json:"id"`
}

// AgentInfoCapabilities is agent-info's capabilities object.
type AgentInfoCapabilities struct {
	ProtectedQueries bool `json:"protectedQueries"`
	OpaqueReadOnly   bool `json:"opaqueReadOnly"`
}

// AgentInfoResponse is GET agent-info's exact success envelope
// (api-contract.md "Endpoint table").
type AgentInfoResponse struct {
	Version           string                `json:"version"`
	Principal         AgentInfoPrincipal    `json:"principal"`
	SecurityContextID string                `json:"securityContextId"`
	Projects          []AgentInfoProject    `json:"projects"`
	Capabilities      AgentInfoCapabilities `json:"capabilities"`
}

// ColumnProvenance is ColumnEntry.Provenance's closed set.
type ColumnProvenance string

const (
	ColumnDeclared ColumnProvenance = "declared"
	ColumnInferred ColumnProvenance = "inferred"
)

// ColumnEntry is one GET semantic/columns response entry. Unmapped columns
// are omitted entirely (never present with empty entity/field).
type ColumnEntry struct {
	Column     string           `json:"column"`
	Entity     string           `json:"entity"`
	Field      string           `json:"field"`
	Provenance ColumnProvenance `json:"provenance"`
}

// ColumnsResponse is GET semantic/columns's exact success envelope.
type ColumnsResponse struct {
	Columns []ColumnEntry `json:"columns"`
}

// RelatedRequest is POST semantic/related's body (beyond the embedded
// Scope, sent as query params per api-contract.md's GET/POST placement
// rule... except semantic/related is itself POST — see api-contract.md
// "Endpoint table": "POST semantic/related | Scope + {fact:Fact,limit?}").
// Scope travels in the JSON body here (unlike semantic/columns' GET, which
// carries it in the URL query) because the whole request is a POST body.
type RelatedRequest struct {
	Scope
	Fact  Fact `json:"fact"`
	Limit int  `json:"limit,omitempty"`
}

// RelatedEntry is one POST semantic/related response entry. Count is
// omitted (null) when an exact authorized count could not be obtained.
type RelatedEntry struct {
	LookupID   string `json:"lookupId"`
	Label      string `json:"label"`
	Source     string `json:"source"`
	Collection string `json:"collection"`
	Count      *int   `json:"count"`
}

// RelatedResponse is POST semantic/related's exact success envelope.
type RelatedResponse struct {
	Related   []RelatedEntry `json:"related"`
	Truncated bool           `json:"truncated"`
}

// RelatedRowsRequest is POST semantic/related/rows's body.
type RelatedRowsRequest struct {
	Scope
	LookupID string     `json:"lookupId"`
	Value    TypedValue `json:"value"`
	Limit    int        `json:"limit,omitempty"`
}

// ApplicableRequest is POST queries/applicable's body.
type ApplicableRequest struct {
	Scope
	Values []Fact `json:"values"`
}

// CandidateState is Candidate.State's closed set.
type CandidateState string

const (
	StateRunnable          CandidateState = "runnable"
	StateNeedsInput        CandidateState = "needs-input"
	StateNeedsTarget       CandidateState = "needs-target"
	StateSourceUnavailable CandidateState = "source-unavailable"
)

// CandidateTarget is one authorized eligible target option on a Candidate.
type CandidateTarget struct {
	Source string `json:"source"`
	Label  string `json:"label"`
}

// CandidateChainEntry explains how one parameter resolved (or why it is
// still missing).
type CandidateChainEntry struct {
	ParameterID string `json:"parameterId"`
	FactID      string `json:"factId,omitempty"`
	Explanation string `json:"explanation"`
}

// CandidateAmbiguous names one parameter more than one distinct automatic
// value could bind, with the conflicting fact IDs.
type CandidateAmbiguous struct {
	ParameterID string   `json:"parameterId"`
	FactIDs     []string `json:"factIds"`
}

// Candidate is one library query's resolution state against the caller's
// available facts (api-contract.md "Endpoint table" / the Candidate type).
type Candidate struct {
	QueryID        string                `json:"queryId"`
	Targets        []CandidateTarget     `json:"targets"`
	SelectedSource string                `json:"selectedSource,omitempty"`
	Bindings       []Binding             `json:"bindings"`
	Chain          []CandidateChainEntry `json:"chain"`
	Missing        []string              `json:"missing"`
	Ambiguous      []CandidateAmbiguous  `json:"ambiguous"`
	State          CandidateState        `json:"state"`
}

// ApplicableResponse is POST queries/applicable's exact success envelope.
type ApplicableResponse struct {
	Applicable []Candidate `json:"applicable"`
	NotYet     []Candidate `json:"notYet"`
}

// BindingOriginInput is one ExecutionRequest.BindingOrigins entry.
type BindingOriginInput struct {
	ParameterID string        `json:"parameterId"`
	Origin      BindingOrigin `json:"origin"`
	FactID      string        `json:"factId,omitempty"`
}

// ExecutionRequest is POST exec/run_query's body (api-contract.md "Endpoint
// table" / the ExecutionRequest type). Exactly one of QueryID/DTQL is
// required; Source is required for ad-hoc DTQL and optional for a saved
// query obeying target resolution.
type ExecutionRequest struct {
	Project           string                `json:"project"`
	Environment       string                `json:"environment"`
	SecurityContextID string                `json:"securityContextId"`
	Source            string                `json:"source,omitempty"`
	QueryID           string                `json:"queryId,omitempty"`
	DTQL              string                `json:"dtql,omitempty"`
	Parameters        map[string]TypedValue `json:"parameters"`
	BindingOrigins    []BindingOriginInput  `json:"bindingOrigins"`
	Mode              ExecutionMode         `json:"mode"`
	SnapshotID        string                `json:"snapshotId,omitempty"`
	Limit             int                   `json:"limit,omitempty"`
}
