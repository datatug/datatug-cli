package endpoints

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/semantic"
)

// semanticRelatedHandler is POST /datatug/semantic/related, decoding the
// appendix's exact envelope — Scope + {fact:Fact,limit?} in the JSON body
// (POST, so a semantic value is never copied into a URL — api-contract.md
// "Endpoint table") — as core's own apicontract.RelatedRequest (the schema
// authority for this body since PR datatug-core#313 / v0.27.0 of
// datatug-core; previously composed locally here, a gap S78's report named
// for the lead and #313 closed). Response:
// {related:[{lookupId,label,source,collection,count}],truncated}.
func semanticRelatedHandler(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeContractError(w, r, newInvalidRequest("", "failed to read request body: "+err.Error()))
		return
	}
	var req apicontract.RelatedRequest
	if err := apicontract.DecodeStrict(body, &req); err != nil {
		writeContractError(w, r, newInvalidRequest("", err.Error()))
		return
	}
	resp, err := computeSemanticRelated(r.Context(), req)
	writeContractResponse(w, r, err, resp)
}

func computeSemanticRelated(ctx context.Context, req apicontract.RelatedRequest) (apicontract.RelatedResponse, error) {
	scope := apicontract.Scope{Project: req.Project, Environment: req.Environment, SecurityContextID: req.SecurityContextID}
	if err := validateScope(scope); err != nil {
		return apicontract.RelatedResponse{}, err
	}
	// req.Validate() is core's own structural check: required Scope fields,
	// Fact validity (id/entity/field/value/origin/mapping — everything
	// validateFact used to hand-check here, now superseded), and — "an
	// explicit limit: 0 is INVALID_REQUEST" per the lead session's semantics
	// ruling for this stream — Limit, when the caller sent one, in (0,50].
	// validateScope above still runs first for the one thing core's
	// Validate() cannot check: STALE_CONTEXT (api.ValidateSecurityContext), a
	// live-session check.
	if err := req.Validate(); err != nil {
		return apicontract.RelatedResponse{}, requestValidationError(err)
	}
	// fact.physical is required by THIS endpoint specifically ("compute
	// related lookups" needs a physical column to pivot from), not by Fact's
	// own general schema (core's Fact.Validate() only validates Physical
	// when present, since some Fact-carrying endpoints — queries/applicable —
	// accept facts with no physical mapping at all).
	if req.Fact.Physical == nil {
		return apicontract.RelatedResponse{}, newInvalidRequest("fact.physical", "is required to compute related lookups")
	}
	projectDir, ok := api.ProjectDir(req.Project)
	if !ok {
		return apicontract.RelatedResponse{}, newNotFound("unknown project")
	}
	projStore, err := api.ProjectStoreFor(req.Project)
	if err != nil {
		return apicontract.RelatedResponse{}, newInvalidRequest("project", err.Error())
	}
	entities, err := loadModuleEntities(projectDir)
	if err != nil {
		return apicontract.RelatedResponse{}, err
	}
	physical := *req.Fact.Physical
	resolved, err := resolveSource(ctx, projStore, projectDir, req.Environment, physical.Source, physical.Collection)
	if err != nil {
		return apicontract.RelatedResponse{}, err
	}
	value, err := factValueString(req.Fact.Value)
	if err != nil {
		return apicontract.RelatedResponse{}, err
	}
	provenance := semantic.Inferred
	if req.Fact.Mapping == "declared" {
		provenance = semantic.Declared
	}
	selected := semantic.SemanticValue{
		Entity: req.Fact.Entity, Field: req.Fact.Field, Value: value,
		Source: physical.Source, Collection: physical.Collection, Column: physical.Column,
		Provenance: provenance,
	}
	schemaByCollection := map[semantic.SchemaKey]semantic.TableSchema{
		{Source: physical.Source, Collection: physical.Collection}: resolved.Schema,
	}
	lookups := semantic.RelatedLookups(entities, schemaByCollection, selected)

	// req.Limit is *int (core's own optional-limit shape): nil means the
	// caller sent no "limit" at all, so boundRelatedLimit's own "0 means
	// unset, default to the cap" rule applies unchanged; an explicit
	// "limit": 0 is no longer reachable here at all — req.Validate() above
	// already rejected it as INVALID_REQUEST, distinct from an absent limit
	// for the first time (previously both unmarshaled to the same `int`
	// zero value and were indistinguishable — this stream's limit-0
	// semantics change; see requestValidationError and exec_run_query.go's
	// identical requestedLimit idiom for ExecutionRequest.Limit).
	requestedLimit := 0
	if req.Limit != nil {
		requestedLimit = *req.Limit
	}
	limit := boundRelatedLimit(requestedLimit)
	truncated := len(lookups) > limit
	if truncated {
		lookups = lookups[:limit]
	}

	executor, ok := api.SecureExecutor()
	if !ok {
		return apicontract.RelatedResponse{}, newContractError(codeInternal, "server has no policy-enforced session configured", "")
	}
	resp := apicontract.RelatedResponse{Related: make([]apicontract.RelatedItem, 0, len(lookups)), Truncated: truncated}
	for _, lookup := range lookups {
		entry := apicontract.RelatedItem{
			Label: lookup.Collection, Source: lookup.Source, Collection: lookup.Collection,
			LookupID: encodeLookupID(lookup.Source, lookup.Collection, lookup.Column),
		}
		count, countErr := countRelated(ctx, executor, projStore, projectDir, req.Environment, lookup, value)
		if countErr == nil {
			entry.Count = count
		}
		resp.Related = append(resp.Related, entry)
	}
	// Candidates sort by queryId for deterministic tests
	// (api-contract.md) — the same determinism rule applies here: order by
	// (source, collection) so repeated calls against the same fact are
	// byte-stable for fixture tests.
	sort.Slice(resp.Related, func(i, j int) bool {
		if resp.Related[i].Source != resp.Related[j].Source {
			return resp.Related[i].Source < resp.Related[j].Source
		}
		return resp.Related[i].Collection < resp.Related[j].Collection
	})
	return resp, nil
}

// factValueString renders a TypedValue as the plain string
// semantic.SemanticValue/RelatedLookups' equality-filter machinery expects
// (that package is untyped — see pkg/semantic.SemanticValue.Value
// interface{} — so the exact TypedValue type distinction the appendix
// requires at the transport boundary is preserved up to here, then
// collapsed to the query-parameter string form countRelated/dal.WhereField
// need).
func factValueString(v apicontract.TypedValue) (string, error) {
	native, err := nativeValue(v)
	if err != nil {
		return "", newTypeMismatch("fact.value", err.Error())
	}
	return fmt.Sprint(native), nil
}

// countRelated runs lookup's filter through the access-policy path (bounded
// by the appendix's 2-second count budget: api-contract.md "Bounded lookups
// and HTTP" — "return null if an exact authorized count cannot be obtained
// within a 2-second budget") and returns the admitted row count, or nil
// when any policy Limitation applied, the count query itself failed, or the
// budget was exceeded — every one of these means "count unavailable", never
// a number that would misrepresent what an unrestricted caller would see.
func countRelated(ctx context.Context, executor *secureread.Executor, projStore datatug.ProjectStore, projectDir, environment string, lookup semantic.Lookup, value string) (*int64, error) {
	lookupSource, err := resolveSource(ctx, projStore, projectDir, environment, lookup.Source, lookup.Collection)
	if err != nil {
		return nil, err
	}
	countCtx, cancel := context.WithTimeout(ctx, countBudget)
	defer cancel()
	q := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef(lookup.Collection, ""))).
		Where(dal.WhereField(lookup.Column, dal.Equal, value)).
		SelectIntoRecord(nil)
	result, err := executor.RunStructured(countCtx, lookupSource.URL, q, nil)
	if err != nil {
		return nil, err
	}
	if len(result.Limitations) > 0 {
		return nil, nil
	}
	n := int64(len(result.Rows))
	return &n, nil
}

// semanticRelatedRowsHandler is POST /datatug/semantic/related/rows,
// decoding the appendix's exact envelope — Scope +
// {lookupId,value:TypedValue,limit?}, response Result (the same shape
// exec/run_query returns) — as core's own apicontract.RelatedRowsRequest
// (the schema authority for this body since PR datatug-core#313 / v0.27.0 of
// datatug-core; previously composed locally here, a gap S78's report named
// for the lead and #313 closed).
func semanticRelatedRowsHandler(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeContractError(w, r, newInvalidRequest("", "failed to read request body: "+err.Error()))
		return
	}
	var req apicontract.RelatedRowsRequest
	if err := apicontract.DecodeStrict(body, &req); err != nil {
		writeContractError(w, r, newInvalidRequest("", err.Error()))
		return
	}
	resp, err := computeSemanticRelatedRows(r.Context(), req)
	writeContractResponse(w, r, err, resp)
}

func computeSemanticRelatedRows(ctx context.Context, req apicontract.RelatedRowsRequest) (apicontract.Result, error) {
	scope := apicontract.Scope{Project: req.Project, Environment: req.Environment, SecurityContextID: req.SecurityContextID}
	if err := validateScope(scope); err != nil {
		return apicontract.Result{}, err
	}
	// req.Validate() is core's own structural check: required Scope fields
	// and lookupId, Value validity, and — "an explicit limit: 0 is
	// INVALID_REQUEST" per the lead session's semantics ruling for this
	// stream — Limit, when the caller sent one, in (0,500]. This supersedes
	// the two hand-written req.LookupID=="" / req.Value.Type=="" checks that
	// used to live here. validateScope above still runs first for the one
	// thing core's Validate() cannot check: STALE_CONTEXT
	// (api.ValidateSecurityContext), a live-session check.
	if err := req.Validate(); err != nil {
		return apicontract.Result{}, requestValidationError(err)
	}
	source, collection, column, err := decodeLookupID(req.LookupID)
	if err != nil {
		return apicontract.Result{}, newInvalidRequest("lookupId", err.Error())
	}
	projectDir, ok := api.ProjectDir(req.Project)
	if !ok {
		return apicontract.Result{}, newNotFound("unknown project")
	}
	projStore, err := api.ProjectStoreFor(req.Project)
	if err != nil {
		return apicontract.Result{}, newInvalidRequest("project", err.Error())
	}
	resolved, err := resolveSource(ctx, projStore, projectDir, req.Environment, source, collection)
	if err != nil {
		return apicontract.Result{}, err
	}
	value, err := factValueString(req.Value)
	if err != nil {
		return apicontract.Result{}, err
	}
	// req.Limit is *int (core's own optional-limit shape): nil means the
	// caller sent no "limit" at all, so boundLimit's own "0 means unset,
	// default to 100" rule applies unchanged; an explicit "limit": 0 is no
	// longer reachable here — req.Validate() above already rejected it as
	// INVALID_REQUEST (this stream's limit-0 semantics change; see
	// requestValidationError and exec_run_query.go's identical
	// requestedLimit idiom for ExecutionRequest.Limit).
	requestedLimit := 0
	if req.Limit != nil {
		requestedLimit = *req.Limit
	}
	limit := boundLimit(requestedLimit)
	builder := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef(collection, ""))).
		Where(dal.WhereField(column, dal.Equal, value)).
		Limit(limit + 1) // +1 so a full page can be distinguished from an exact-limit result, for Truncated.
	query := builder.SelectIntoRecord(nil)

	executor, ok := api.SecureExecutor()
	if !ok {
		return apicontract.Result{}, newContractError(codeInternal, "server has no policy-enforced session configured", "")
	}
	result, err := executor.RunStructured(ctx, resolved.URL, query, nil)
	if err != nil {
		if err == context.DeadlineExceeded {
			return apicontract.Result{}, newTimeout("related rows lookup timed out")
		}
		if isAccessDenied(err) {
			return apicontract.Result{}, contractErrAccessDenied(err.Error())
		}
		return apicontract.Result{}, newInvalidRequest("", err.Error())
	}
	truncated := len(result.Rows) > limit
	if truncated {
		result.Rows = result.Rows[:limit]
	}
	recordset, err := toContractRecordset(result)
	if err != nil {
		return apicontract.Result{}, newContractError(codeInternal, err.Error(), "")
	}
	return apicontract.Result{
		Recordset:   recordset,
		Limitations: toContractLimitations(result.Limitations),
		Provenance: apicontract.Provenance{
			Source: source, Collection: collection, Mode: apicontract.ProvenanceModeLive,
			ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), ExecutionProfile: apicontract.ExecutionProfileProtected,
		},
		Truncated: truncated,
	}, nil
}

func isAccessDenied(err error) bool {
	return err == secureread.ErrAccessDenied || (err != nil && strings.Contains(err.Error(), "access denied"))
}

// encodeLookupID/decodeLookupID give /semantic/related's LookupID a stable,
// opaque, self-contained encoding of exactly what /semantic/related/rows
// needs to re-run the same lookup (source, collection, column) — no server
// side state to keep between the two calls. Scoped to project/environment/
// securityContextId only insofar as every caller of decodeLookupID re-runs
// full Scope validation and source resolution against the CURRENT request's
// own scope (api-contract.md: "Each rows request revalidates the
// relationship, value type, current source policy and limit").
func encodeLookupID(source, collection, column string) string {
	raw := source + "\x00" + collection + "\x00" + column
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeLookupID(id string) (source, collection, column string, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return "", "", "", fmt.Errorf("invalid lookupId: %w", err)
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("invalid lookupId: malformed")
	}
	return parts[0], parts[1], parts[2], nil
}
