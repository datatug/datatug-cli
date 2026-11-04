package watchdaemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/queryparams"
	"github.com/datatug/datatug-cli/pkg/watch"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/dataaccess"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/detective"
)

// ServiceReadBinding is retained/admitted metadata supplied independently by
// each peer. It is never decoded from an operation request. Building a digest
// authenticates nothing and does not attest that a worker executed a Check.
type ServiceReadBinding struct {
	Claim      watch.Claim
	Bundle     CloudExecutionBundle
	QueryID    string
	SourceID   string
	Connection dataaccess.ConnectionRef
}

type ServiceReadIntent struct {
	Operation          dataaccess.StructuredRead
	Request            dataaccess.ResultRequest
	WatchBindingSHA256 string
}

// ServiceConnectionSource makes the existing immutable descriptor shape for a
// catalog connection. Its hash is a descriptor ID, never access authority.
func ServiceConnectionSource(connectionID string) (CloudBundleSource, error) {
	if !serviceReadText(connectionID, 512) {
		return CloudBundleSource{}, watch.ErrInvalid
	}
	s := CloudBundleSource{Kind: "connection", SourceID: connectionID}
	raw, _ := json.Marshal(s)
	s.ID = serviceReadSHA(raw)
	return s, nil
}

// BuildServiceReadIntent preserves Core's operation-only RequestFor digest and
// separately binds the complete accepted Watch intent. Query/default codecs are
// shared by client/server. Bounds are FIXED, not inferred from Detective's500
// row limit. Core still owns query admission, policy compilation and output.
func BuildServiceReadIntent(ctx context.Context, b ServiceReadBinding, parameters map[string]apicontract.TypedValueOrSet, nonce string) (ServiceReadIntent, error) {
	var zero ServiceReadIntent
	d := b.Claim.Definition
	if ctx == nil || ctx.Err() != nil || b.Claim.ClaimID == "" || b.Claim.ClaimID != b.Claim.RunID || b.Claim.Fence < 1 || b.Claim.Deadline.IsZero() || b.Claim.LeaseUntil.IsZero() || b.Claim.LeaseUntil.After(b.Claim.Deadline) || d.ID == "" || d.Version < 1 || !serviceReadConnection(b.Connection) || b.Connection.Space != d.Namespace || !serviceReadHash(b.SourceID) {
		return zero, watch.ErrInvalid
	}
	catalog, err := b.Bundle.VerifiedCatalog(ctx, b.Claim)
	if err != nil {
		return zero, err
	}
	accepted := false
	for _, check := range catalog.Checks {
		for _, step := range check.Steps {
			accepted = accepted || step.Kind == "query" && step.QueryID == b.QueryID
		}
	}
	pin, exists := b.Bundle.Queries[b.QueryID]
	if !accepted || !exists || !serviceReadText(pin.Revision, 512) {
		return zero, watch.ErrInvalid
	}
	var query datatug.QueryDefWithFolderPath
	if serviceReadDecode(pin.JSON, &query) != nil || query.QueryDef.Validate() != nil {
		return zero, watch.ErrInvalid
	}
	expectedSource, err := ServiceConnectionSource(query.ConnectionID)
	if err != nil || expectedSource.ID != b.SourceID {
		return zero, watch.ErrInvalid
	}
	matched := 0
	for _, source := range b.Bundle.Sources {
		if source == expectedSource {
			matched++
		}
	}
	if matched != 1 {
		return zero, watch.ErrInvalid
	}
	normalized, err := normalizeServiceReadParameters(query.QueryDef, parameters)
	if err != nil {
		return zero, err
	}
	encoded, err := serviceReadParametersJSON(normalized)
	if err != nil {
		return zero, err
	}
	op := dataaccess.StructuredRead{TugQL: query.Text, Parameters: string(encoded), MaxRows: ServiceReadMaxRows, MaxBytes: ServiceReadMaxBytes}
	request, err := op.RequestFor(nonce)
	if err != nil {
		return zero, watch.ErrInvalid
	}
	for name, value := range d.Inputs {
		if !serviceReadText(name, 128) || !serviceReadInput(value) {
			return zero, watch.ErrInvalid
		}
	}
	f := b.Connection.Fingerprint
	// This fixed array is the watch-service-read/1 canonical Watch binding.
	// Core requestSHA256 above MUST remain only the StructuredRead digest.
	intent := []any{ServiceReadContract, b.Claim.ClaimID, b.Claim.RunID, b.Claim.Fence,
		[3]string{d.Namespace, d.Project, d.Environment}, d.ID, d.Version,
		b.Bundle.CheckID, b.Bundle.CheckVersion, d.SourceRevision, d.SourceFingerprint,
		b.Bundle.BundleDigest, b.QueryID, pin.Revision, serviceReadSHA(pin.JSON), b.SourceID,
		[6]string{b.Connection.Space, b.Connection.ID, f.Engine, f.Database, f.EngineIdentity, f.Address},
		d.Inputs, normalized, request.Nonce, request.RequestSHA256}
	raw, err := json.Marshal(intent)
	if err != nil || len(raw) > 1<<20 || ctx.Err() != nil {
		return zero, watch.ErrInvalid
	}
	return ServiceReadIntent{Operation: op, Request: request, WatchBindingSHA256: serviceReadSHA(raw)}, nil
}

func normalizeServiceReadParameters(query datatug.QueryDef, supplied map[string]apicontract.TypedValueOrSet) (map[string]apicontract.TypedValueOrSet, error) {
	if query.Type != datatug.QueryTypeTugQL || !query.StructuredBody || query.Federation != nil || len(query.Targets) != 0 || len(query.Parameters) > 100 {
		return nil, watch.ErrInvalid
	}
	declared := make(map[string]datatug.ParameterDef, len(query.Parameters))
	result := make(map[string]apicontract.TypedValueOrSet, len(supplied))
	for _, p := range query.Parameters {
		if p.Validate() != nil || p.IsCredential || !dal.ValidParamName(p.ID) || strings.Contains(p.ID, ".") || !serviceReadValueType(apicontract.ValueType(p.Type)) {
			return nil, watch.ErrInvalid
		}
		if _, exists := declared[p.ID]; exists {
			return nil, watch.ErrInvalid
		}
		declared[p.ID] = p
	}
	for name, value := range supplied {
		p, exists := declared[name]
		if !exists || value.Validate() != nil || value.Set != nil && !p.IsMultiValue {
			return nil, watch.ErrInvalid
		}
		typeName := apicontract.ValueType(p.Type)
		if value.Scalar != nil && value.Scalar.Type != typeName || value.Set != nil && value.Set.Values[0].Type != typeName {
			return nil, watch.ErrInvalid
		}
		result[name] = value
	}
	for name, p := range declared {
		if _, exists := result[name]; exists {
			continue
		}
		if p.DefaultValue != nil {
			v, err := queryparams.ScalarDefault(p)
			if err != nil {
				return nil, watch.ErrInvalid
			}
			result[name] = apicontract.ScalarValue(v)
		} else if p.IsRequired {
			return nil, watch.ErrInvalid
		}
	}
	return result, nil
}

func serviceReadParametersJSON(values map[string]apicontract.TypedValueOrSet) ([]byte, error) {
	if values == nil || len(values) > 100 {
		return nil, watch.ErrInvalid
	}
	for name, value := range values {
		if !dal.ValidParamName(name) || strings.Contains(name, ".") || value.Validate() != nil {
			return nil, watch.ErrInvalid
		}
		items := []apicontract.TypedValue{}
		if value.Scalar != nil {
			items = append(items, *value.Scalar)
		} else {
			items = value.Set.Values
		}
		for _, item := range items {
			if !utf8.ValidString(item.Str) {
				return nil, watch.ErrInvalid
			}
		}
	}
	raw, err := json.Marshal(values)
	if err != nil || len(raw) > 65536 {
		return nil, watch.ErrInvalid
	}
	return raw, nil
}

func serviceReadSHA(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func serviceReadInput(value detective.InputValue) bool {
	if value.Validate() != nil {
		return false
	}
	if value.Scalar != nil {
		return utf8.ValidString(value.Scalar.Str)
	}
	if value.Set != nil {
		for _, item := range value.Set.Values {
			if !utf8.ValidString(item.Str) {
				return false
			}
		}
		return true
	}
	return utf8.ValidString(value.Range.Start.Str) && utf8.ValidString(value.Range.End.Str)
}

func serviceReadValueType(kind apicontract.ValueType) bool {
	switch kind {
	case apicontract.ValueTypeString, apicontract.ValueTypeNumber, apicontract.ValueTypeInteger, apicontract.ValueTypeDecimal, apicontract.ValueTypeBoolean, apicontract.ValueTypeDate, apicontract.ValueTypeDatetime, apicontract.ValueTypeNull:
		return true
	default:
		return false
	}
}
