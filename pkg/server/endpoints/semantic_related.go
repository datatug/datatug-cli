package endpoints

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/semantic"
)

// RelatedResponse is GET /datatug/semantic/related's body: per REQ:related-
// lookup-model and the API contracts table, [{label, source, collection,
// count|null, lookupId}]. Column/Kind are this stream's own addition,
// directly available from semantic.Lookup and useful for a caller (the web
// context panel) that wants to explain a lookup rather than just list it.
type RelatedResponse struct {
	Related []RelatedEntry `json:"related"`
}

// RelatedEntry is one related-record path. Label defaults to Collection —
// semantic.Lookup carries no human title of its own, and no entity/board
// naming convention exists yet to derive a nicer one from; a caller free to
// override display text with something richer once one does.
//
// Count is nil ("count unavailable" in the hub's own wording) whenever
// running the count query through the access-policy path reported any
// Limitation — a restricted count is not the true count, so REQ:related-
// lookup-execution's "count or count unavailable" is honoured by refusing to
// report a number that would misrepresent what an unrestricted caller would
// see. It is also nil if the count query itself failed (e.g. access denied
// outright): the /related endpoint reports "nothing to show", it does not
// fail the whole list over one lookup a principal cannot see at all.
type RelatedEntry struct {
	Label      string `json:"label"`
	Source     string `json:"source"`
	Collection string `json:"collection"`
	Column     string `json:"column"`
	Kind       string `json:"kind"`
	Count      *int   `json:"count"`
	LookupID   string `json:"lookupId"`
}

func semanticRelatedHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	session, err := semanticSessionFromQuery(q)
	if err != nil {
		writeSemanticJSON(w, r, err, nil)
		return
	}
	resp, err := computeSemanticRelated(r.Context(), semanticRelatedRequest{
		ProjectID:  q.Get(urlParamProjectID),
		Entity:     q.Get("entity"),
		Field:      q.Get("field"),
		Value:      q.Get("value"),
		Source:     q.Get("source"),
		Collection: q.Get("collection"),
		Column:     q.Get("column"),
		Provenance: q.Get("provenance"),
	}, session)
	writeSemanticJSON(w, r, err, resp)
}

type semanticRelatedRequest struct {
	ProjectID                  string
	Entity, Field, Value       string
	Source, Collection, Column string
	Provenance                 string
}

func (req semanticRelatedRequest) validate() error {
	fields := []struct{ name, value string }{
		{"entity", req.Entity}, {"field", req.Field}, {"value", req.Value},
		{"source", req.Source}, {"collection", req.Collection}, {"column", req.Column},
	}
	for _, f := range fields {
		if f.value == "" {
			return newFieldError(f.name, "is required")
		}
	}
	return nil
}

func computeSemanticRelated(ctx context.Context, req semanticRelatedRequest, session secureread.Session) (RelatedResponse, error) {
	if err := req.validate(); err != nil {
		return RelatedResponse{}, err
	}
	projectDir, err := semanticProjectDir(req.ProjectID)
	if err != nil {
		return RelatedResponse{}, err
	}
	entities, err := loadModuleEntities(projectDir)
	if err != nil {
		return RelatedResponse{}, err
	}
	resolved, err := resolveSource(ctx, projectDir, req.Source, req.Collection)
	if err != nil {
		return RelatedResponse{}, err
	}
	provenance := semantic.Provenance(req.Provenance)
	if provenance == "" {
		provenance = semantic.Declared
	}
	selected := semantic.SemanticValue{
		Entity: req.Entity, Field: req.Field, Value: req.Value,
		Source: req.Source, Collection: req.Collection, Column: req.Column,
		Provenance: provenance,
	}
	schemaByCollection := map[semantic.SchemaKey]semantic.TableSchema{
		{Source: req.Source, Collection: req.Collection}: resolved.Schema,
	}
	lookups := semantic.RelatedLookups(entities, schemaByCollection, selected)

	executor := secureread.NewExecutor(session)
	resp := RelatedResponse{Related: make([]RelatedEntry, 0, len(lookups))}
	for _, lookup := range lookups {
		entry := RelatedEntry{
			Label: lookup.Collection, Source: lookup.Source, Collection: lookup.Collection,
			Column: lookup.Column, Kind: string(lookup.Kind),
			LookupID: encodeLookupID(lookup.Source, lookup.Collection, lookup.Column),
		}
		if count, countErr := countRelated(ctx, executor, projectDir, lookup, req.Value); countErr == nil {
			entry.Count = count
		}
		resp.Related = append(resp.Related, entry)
	}
	return resp, nil
}

// countRelated runs lookup's filter through the access-policy path and
// returns the admitted row count, or nil when any policy Limitation applied
// (see RelatedEntry's doc for why a restricted count is never reported as
// the count).
func countRelated(ctx context.Context, executor *secureread.Executor, projectDir string, lookup semantic.Lookup, value string) (*int, error) {
	lookupSource, err := resolveSource(ctx, projectDir, lookup.Source, lookup.Collection)
	if err != nil {
		return nil, err
	}
	q := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef(lookup.Collection, ""))).
		Where(dal.WhereField(lookup.Column, dal.Equal, value)).
		SelectIntoRecord(nil)
	result, err := executor.RunStructured(ctx, lookupSource.URL, q, nil)
	if err != nil {
		return nil, err
	}
	if len(result.Limitations) > 0 {
		return nil, nil
	}
	n := len(result.Rows)
	return &n, nil
}

// RelatedRowsResponse is GET /datatug/semantic/related/rows's body: per the
// API contracts table, "recordset + limitations".
type RelatedRowsResponse struct {
	Columns     []string                `json:"columns"`
	Rows        []RelatedRow            `json:"rows"`
	Limitations []secureread.Limitation `json:"limitations"`
}

// RelatedRow mirrors secureread.Row (Key/Data) under its own JSON tags —
// secureread.Row has none of its own (see PR body: modifying pkg/secureread,
// already merged by a concurrent lane, is outside this stream's scope).
type RelatedRow struct {
	Key  string         `json:"key"`
	Data map[string]any `json:"data"`
}

func semanticRelatedRowsHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	session, err := semanticSessionFromQuery(q)
	if err != nil {
		writeSemanticJSON(w, r, err, nil)
		return
	}
	limit := 0
	if v := q.Get("limit"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil {
			writeSemanticJSON(w, r, newFieldError("limit", "must be an integer"), nil)
			return
		}
		limit = n
	}
	resp, err := computeSemanticRelatedRows(r.Context(), q.Get(urlParamProjectID), q.Get("lookupId"), q.Get("value"), limit, session)
	writeSemanticJSON(w, r, err, resp)
}

func computeSemanticRelatedRows(ctx context.Context, projectID, lookupID, value string, limit int, session secureread.Session) (RelatedRowsResponse, error) {
	if lookupID == "" {
		return RelatedRowsResponse{}, newFieldError("lookupId", "is required")
	}
	if value == "" {
		return RelatedRowsResponse{}, newFieldError("value", "is required")
	}
	source, collection, column, err := decodeLookupID(lookupID)
	if err != nil {
		return RelatedRowsResponse{}, newFieldError("lookupId", err.Error())
	}
	projectDir, err := semanticProjectDir(projectID)
	if err != nil {
		return RelatedRowsResponse{}, err
	}
	resolved, err := resolveSource(ctx, projectDir, source, collection)
	if err != nil {
		return RelatedRowsResponse{}, err
	}
	builder := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef(collection, ""))).
		Where(dal.WhereField(column, dal.Equal, value))
	if limit > 0 {
		builder = builder.Limit(limit)
	}
	query := builder.SelectIntoRecord(nil)

	executor := secureread.NewExecutor(session)
	result, err := executor.RunStructured(ctx, resolved.URL, query, nil)
	if err != nil {
		if errors.Is(err, secureread.ErrAccessDenied) {
			return RelatedRowsResponse{}, newAccessDeniedError(err.Error())
		}
		return RelatedRowsResponse{}, err
	}
	resp := RelatedRowsResponse{Columns: result.Columns, Limitations: result.Limitations}
	for _, row := range result.Rows {
		resp.Rows = append(resp.Rows, RelatedRow{Key: row.Key, Data: row.Data})
	}
	return resp, nil
}

// encodeLookupID/decodeLookupID give /semantic/related's LookupID a stable,
// opaque, self-contained encoding of exactly what /semantic/related/rows
// needs to re-run the same lookup (source, collection, column) — no server
// side state to keep between the two calls.
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
