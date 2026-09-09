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

// relatedRequest is POST semantic/related's body (api-contract.md "Endpoint
// table": Scope + {fact:Fact,limit?}). Core's pkg/apicontract (the schema
// authority, v0.26.0) defines RelatedResponse/RelatedItem but not a request
// envelope for this endpoint — Scope and Fact are core's own shared schema
// types, reused here unchanged; only the enclosing envelope has no
// pkg/apicontract type of its own. See applicableRequest's doc comment
// (semantic_applicable.go) for the same gap, named once for the lead there.
type relatedRequest struct {
	apicontract.Scope
	Fact  apicontract.Fact `json:"fact"`
	Limit int              `json:"limit,omitempty"`
}

// relatedRowsRequest is POST semantic/related/rows's body. Same gap as
// relatedRequest: no pkg/apicontract request envelope, composed here from
// core's own Scope/TypedValue.
type relatedRowsRequest struct {
	apicontract.Scope
	LookupID string                 `json:"lookupId"`
	Value    apicontract.TypedValue `json:"value"`
	Limit    int                    `json:"limit,omitempty"`
}

// semanticRelatedHandler is POST /datatug/semantic/related, rewritten
// (Task 12) to the appendix's exact envelope: Scope + {fact:Fact,limit?} in
// the JSON body (POST, so a semantic value is never copied into a URL —
// api-contract.md "Endpoint table"), response
// {related:[{lookupId,label,source,collection,count}],truncated}.
func semanticRelatedHandler(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeContractError(w, r, newInvalidRequest("", "failed to read request body: "+err.Error()))
		return
	}
	var req relatedRequest
	if err := decodeContractBody(body, &req); err != nil {
		writeContractError(w, r, newInvalidRequest("", err.Error()))
		return
	}
	resp, err := computeSemanticRelated(r.Context(), req)
	writeContractResponse(w, r, err, resp)
}

func computeSemanticRelated(ctx context.Context, req relatedRequest) (apicontract.RelatedResponse, error) {
	if err := validateScope(req.Scope); err != nil {
		return apicontract.RelatedResponse{}, err
	}
	if err := validateFact(req.Fact); err != nil {
		return apicontract.RelatedResponse{}, err
	}
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

	limit := boundRelatedLimit(req.Limit)
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

// validateFact checks the subset of Fact fields every semantic endpoint
// that accepts one needs populated: id/entity/field/value are always
// required; origin must be one of the closed set.
func validateFact(f apicontract.Fact) error {
	if f.Entity == "" {
		return newMissingParameter("fact.entity")
	}
	if f.Field == "" {
		return newMissingParameter("fact.field")
	}
	if f.Value.Type == "" {
		return newMissingParameter("fact.value")
	}
	switch f.Origin {
	case apicontract.FactOriginSelection, apicontract.FactOriginContext, apicontract.FactOriginManual:
	default:
		return newInvalidRequest("fact.origin", fmt.Sprintf("unknown origin %q", f.Origin))
	}
	return nil
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
// rewritten (Task 12) to the appendix's exact envelope: Scope +
// {lookupId,value:TypedValue,limit?}, response Result (the same shape
// exec/run_query returns).
func semanticRelatedRowsHandler(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeContractError(w, r, newInvalidRequest("", "failed to read request body: "+err.Error()))
		return
	}
	var req relatedRowsRequest
	if err := decodeContractBody(body, &req); err != nil {
		writeContractError(w, r, newInvalidRequest("", err.Error()))
		return
	}
	resp, err := computeSemanticRelatedRows(r.Context(), req)
	writeContractResponse(w, r, err, resp)
}

func computeSemanticRelatedRows(ctx context.Context, req relatedRowsRequest) (apicontract.Result, error) {
	if err := validateScope(req.Scope); err != nil {
		return apicontract.Result{}, err
	}
	if req.LookupID == "" {
		return apicontract.Result{}, newMissingParameter("lookupId")
	}
	if req.Value.Type == "" {
		return apicontract.Result{}, newMissingParameter("value")
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
	limit := boundLimit(req.Limit)
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
