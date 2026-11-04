package watchdaemon

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/datatug/datatug-cli/pkg/watch"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/dataaccess"
)

const (
	ServiceReadContract        = "watch-service-read/1"
	ServiceReadCatalog         = "catalog"
	ServiceReadResolve         = "resolve"
	ServiceReadMaxRows  uint32 = 100
	ServiceReadMaxBytes uint32 = 262144
	ServiceReadLease           = 10 * time.Second
)

// ServiceReadRequest is an additive runner/bundle payload, not an identity or
// permission assertion. Catalog has only contract/phase. Resolve adds operation
// intent; the server independently admits the runner, claim and accepted query.
// No endpoint consumes this foundation until its separate integration lands.
type ServiceReadRequest struct {
	Contract   string                                 `json:"contract"`
	Phase      string                                 `json:"phase"`
	QueryID    string                                 `json:"queryId,omitempty"`
	Parameters map[string]apicontract.TypedValueOrSet `json:"parameters,omitempty"`
	Request    *dataaccess.ResultRequest              `json:"request,omitempty"`
}

func (r ServiceReadRequest) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if r.Phase == ServiceReadCatalog {
		return json.Marshal(struct {
			Contract string `json:"contract"`
			Phase    string `json:"phase"`
		}{r.Contract, r.Phase})
	}
	return json.Marshal(struct {
		Contract   string                                 `json:"contract"`
		Phase      string                                 `json:"phase"`
		QueryID    string                                 `json:"queryId"`
		Parameters map[string]apicontract.TypedValueOrSet `json:"parameters"`
		Request    *dataaccess.ResultRequest              `json:"request"`
	}{r.Contract, r.Phase, r.QueryID, r.Parameters, r.Request})
}

func (r ServiceReadRequest) Validate() error {
	if r.Contract != ServiceReadContract {
		return watch.ErrInvalid
	}
	switch r.Phase {
	case ServiceReadCatalog:
		if r.QueryID != "" || r.Parameters != nil || r.Request != nil {
			return watch.ErrInvalid
		}
	case ServiceReadResolve:
		if !serviceReadText(r.QueryID, 512) || r.Parameters == nil || r.Request == nil || !serviceReadHash(r.Request.RequestSHA256) {
			return watch.ErrInvalid
		}
		if _, err := (dataaccess.StructuredRead{TugQL: "intent", MaxRows: ServiceReadMaxRows, MaxBytes: ServiceReadMaxBytes}).RequestFor(r.Request.Nonce); err != nil {
			return watch.ErrInvalid
		}
		if _, err := serviceReadParametersJSON(r.Parameters); err != nil {
			return err
		}
	default:
		return watch.ErrInvalid
	}
	return nil
}

func (r *ServiceReadRequest) UnmarshalJSON(data []byte) error {
	*r = ServiceReadRequest{}
	type wire ServiceReadRequest
	var v wire
	if err := serviceReadDecode(data, &v); err != nil {
		return err
	}
	keys := []string{"contract", "phase"}
	if v.Phase == ServiceReadResolve {
		keys = append(keys, "queryId", "parameters", "request")
	}
	if !serviceReadKeys(data, keys) {
		return watch.ErrInvalid
	}
	parsed := ServiceReadRequest(v)
	if err := parsed.Validate(); err != nil {
		return err
	}
	*r = parsed
	return nil
}

// ServiceReadResponse contains either catalog classification (no authority) or
// an existing Core resolution. Decoding checks shape only, not authenticity,
// current admission, expected Service identity, native identity or continuity.
// A consumer must independently match retained intent and enter Core's boundary.
type ServiceReadResponse struct {
	Contract           string                 `json:"contract"`
	Phase              string                 `json:"phase"`
	Mode               string                 `json:"mode,omitempty"`
	ProtectedSources   []string               `json:"protectedSources,omitempty"`
	QueryID            string                 `json:"queryId,omitempty"`
	QueryRevision      string                 `json:"queryRevision,omitempty"`
	BundleDigest       string                 `json:"bundleDigest,omitempty"`
	SourceID           string                 `json:"sourceId,omitempty"`
	EnrollmentRevision int64                  `json:"enrollmentRevision,omitempty"`
	RunCeiling         time.Time              `json:"runCeiling,omitempty"`
	Nonce              string                 `json:"nonce,omitempty"`
	WatchBindingSHA256 string                 `json:"watchBindingSHA256,omitempty"`
	Resolution         *dataaccess.Resolution `json:"resolution,omitempty"`
}

func (r ServiceReadResponse) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if r.Phase == ServiceReadCatalog {
		return json.Marshal(struct {
			Contract         string   `json:"contract"`
			Phase            string   `json:"phase"`
			Mode             string   `json:"mode"`
			ProtectedSources []string `json:"protectedSources"`
		}{r.Contract, r.Phase, r.Mode, r.ProtectedSources})
	}
	type wire ServiceReadResponse
	return json.Marshal(wire(r))
}

func (r ServiceReadResponse) Validate() error {
	if r.Contract != ServiceReadContract {
		return watch.ErrInvalid
	}
	if r.Phase == ServiceReadCatalog {
		if r.Mode != "protected" || len(r.ProtectedSources) == 0 || len(r.ProtectedSources) > 50 || r.QueryID != "" || r.QueryRevision != "" || r.BundleDigest != "" || r.SourceID != "" || r.EnrollmentRevision != 0 || !r.RunCeiling.IsZero() || r.Nonce != "" || r.WatchBindingSHA256 != "" || r.Resolution != nil {
			return watch.ErrInvalid
		}
		seen := map[string]bool{}
		for _, id := range r.ProtectedSources {
			if !serviceReadHash(id) || seen[id] {
				return watch.ErrInvalid
			}
			seen[id] = true
		}
		return nil
	}
	if r.Phase != ServiceReadResolve || r.Mode != "" || r.ProtectedSources != nil || !serviceReadText(r.QueryID, 512) || !serviceReadText(r.QueryRevision, 512) || !serviceReadHash(r.BundleDigest) || !serviceReadHash(r.SourceID) || !serviceReadHash(r.WatchBindingSHA256) || r.EnrollmentRevision < 1 || r.RunCeiling.IsZero() || r.Resolution == nil {
		return watch.ErrInvalid
	}
	if _, err := (dataaccess.StructuredRead{TugQL: "intent", MaxRows: ServiceReadMaxRows, MaxBytes: ServiceReadMaxBytes}).RequestFor(r.Nonce); err != nil {
		return watch.ErrInvalid
	}
	v := r.Resolution
	if v.Subject.Validate() != nil || v.Subject.Kind != dataaccess.Service || !serviceReadConnection(v.Connection) || v.Grants == nil || len(v.Grants) != 0 || !v.Protection.ReadOnly || v.Protection.ReadPosture != dataaccess.ReadExact || v.Protection.ID != v.Connection.ID || v.Protection.Fingerprint != v.Connection.Fingerprint || v.Protection.Revision < 1 || (v.Protection.AgentRead != dataaccess.AgentInherit && v.Protection.AgentRead != dataaccess.AgentNone) || v.Protection.MandatoryFineGrained || v.ExactRead == nil || v.ExactRead.Version != 1 || v.ExactRead.Subject != v.Subject || v.ExactRead.Connection != v.Connection || v.ExactRead.Baseline == nil || len(v.ExactRead.Baseline) > 1000 || v.ServerTime.IsZero() || !v.ValidUntil.After(v.ServerTime) || v.ValidUntil.After(v.ServerTime.Add(ServiceReadLease)) || v.ValidUntil.After(r.RunCeiling) {
		return watch.ErrInvalid
	}
	return nil
}

func (r *ServiceReadResponse) UnmarshalJSON(data []byte) error {
	*r = ServiceReadResponse{}
	type wire ServiceReadResponse
	var v wire
	if err := serviceReadDecode(data, &v); err != nil {
		return err
	}
	keys := []string{"contract", "phase", "mode", "protectedSources"}
	if v.Phase == ServiceReadResolve {
		keys = []string{"contract", "phase", "queryId", "queryRevision", "bundleDigest", "sourceId", "enrollmentRevision", "runCeiling", "nonce", "watchBindingSHA256", "resolution"}
	}
	if !serviceReadKeys(data, keys) {
		return watch.ErrInvalid
	}
	parsed := ServiceReadResponse(v)
	if err := parsed.Validate(); err != nil {
		return err
	}
	*r = parsed
	return nil
}

func serviceReadDecode(data []byte, v any) error {
	if len(data) == 0 || len(data) > 1<<20 || !utf8.Valid(data) || apicontract.DecodeStrict(data, v) != nil {
		return watch.ErrInvalid
	}
	return nil
}

// Require exact variant fields, including rejecting present-null fields from
// the other variant. Core's strict decoder already rejected duplicates/trailing.
func serviceReadKeys(data []byte, keys []string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || len(fields) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := fields[key]; !ok {
			return false
		}
	}
	return true
}

func serviceReadHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == value
}

func serviceReadText(value string, limit int) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}

func serviceReadConnection(c dataaccess.ConnectionRef) bool {
	f := c.Fingerprint
	return c.LocalAuthorityID == "" && serviceReadText(c.Space, 512) && serviceReadText(c.ID, 512) && serviceReadText(f.Engine, 128) && serviceReadText(f.Database, 512) && (f.EngineIdentity == "" || serviceReadText(f.EngineIdentity, 4096)) && (f.Address == "" || serviceReadText(f.Address, 4096)) && (f.EngineIdentity != "" || f.Address != "")
}
