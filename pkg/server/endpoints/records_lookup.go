package endpoints

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
)

// recordsLookupMaxBody bounds the body of POST /datatug/records/lookup: a project, an
// environment, a source, a table and a primary key are a few hundred bytes.
const recordsLookupMaxBody = 64 * 1024

// recordsLookupMaxText bounds a name of the request and a string key value.
const recordsLookupMaxText = 256

// bad-key answers: each says what is wrong with the key and never quotes a column name or a value
// of the request.
const (
	badKeyShape   = "key must be an object of the primary-key columns of the table and their values"
	badKeyNoPK    = "the table has no declared primary key to look a record up by"
	badKeyColumns = "key must name exactly the primary-key columns of the table"
	badKeyValue   = "a key value must be a string or an integer of the declared type of its column"
	badKeyType    = "a primary-key column of this type cannot be looked up"
)

// recordsLookupRequest is the body of POST /datatug/records/lookup. No field is a connection
// string, a path or SQL: every name is checked against the registry and the live schema of the
// registered source before any query is built, and the key values are bound as parameters.
type recordsLookupRequest struct {
	recordsTableRequest
	Key json.RawMessage `json:"key"`
}

// recordsTableRequest names a table of a registered source: the body of POST
// /datatug/records/schema and the part of a lookup before its key.
type recordsTableRequest struct {
	Project string `json:"project"`
	Env     string `json:"env"`
	Source  string `json:"source"`
	Schema  string `json:"schema"`
	Table   string `json:"table"`
}

// recordsLookupHandler is POST /datatug/records/lookup: a read of at most one record of a table
// of a registered source by the typed values of the declared primary-key columns of the table.
// It answers the contract Result of its siblings semantic/related/rows and exec/run_query
// (one recordset of at most one row, limitations, provenance) and the contract error envelope:
//
//   - INVALID_REQUEST (400), field project/env/source/schema/table/body, for a request that is
//     not this shape or names something that is not a plain name (a connection string, a path
//     or a "://" is refused before anything is looked up);
//   - INVALID_REQUEST (400), field "key", message starting "bad-key", for a key that is not
//     exactly the primary-key columns with values of their types; no query was issued;
//   - NOT_FOUND (404), field "project", "source", "table" or "key" (the last is
//     record-not-found);
//   - ACCESS_DENIED (403) when the access decision of the principal refuses the read.
//
// The body, the key and the row are never logged.
func recordsLookupHandler(w http.ResponseWriter, r *http.Request) {
	body, ok := readRecordsBody(w, r)
	if !ok {
		return
	}
	var req recordsLookupRequest
	if err := decodeRecordsBody(body, &req, &req.recordsTableRequest); err != nil {
		writeContractError(w, r, err)
		return
	}
	resp, err := computeRecordsLookup(r.Context(), req)
	writeContractResponse(w, r, err, resp)
}

// readRecordsBody reads the bounded body of a records route.
func readRecordsBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, recordsLookupMaxBody))
	if err != nil {
		writeContractError(w, r, newInvalidRequest("body", "the request body could not be read or is too large"))
		return nil, false
	}
	return body, true
}

// decodeRecordsBody decodes body strictly (one JSON object, no unknown field) into into and
// checks the names of table.
func decodeRecordsBody(body []byte, into any, table *recordsTableRequest) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	// The error of the decoder quotes the offending field or value: say only that the shape is wrong.
	if err := dec.Decode(into); err != nil || dec.More() {
		return newInvalidRequest("body", "the request body must be one JSON object of the documented fields")
	}
	for _, f := range []struct{ field, value string }{
		{"project", table.Project}, {"env", table.Env}, {"source", table.Source}, {"table", table.Table},
	} {
		if f.value == "" {
			return newMissingParameter(f.field)
		}
	}
	for _, f := range []struct{ field, value string }{
		{"project", table.Project}, {"env", table.Env}, {"source", table.Source},
	} {
		if api.ValidateIdentifier(f.field, f.value) != nil {
			return newInvalidRequest(f.field, api.PlainNameRule)
		}
	}
	for _, f := range []struct{ field, value string }{{"schema", table.Schema}, {"table", table.Table}} {
		if !plainSchemaObjectName(f.value) {
			return newInvalidRequest(f.field, "must be a name of the database: no '://', path separator, control character or more than 256 characters")
		}
	}
	return nil
}

// plainSchemaObjectName reports whether s may be the name of a schema or a table: empty is a
// schema left out. It refuses what a connection string, a DSN or a path holds; the name is then
// matched against the live schema of the source, so it never reaches SQL as text anyway.
func plainSchemaObjectName(s string) bool {
	if len(s) > recordsLookupMaxText || strings.Contains(s, "://") || strings.ContainsAny(s, "/\\") {
		return false
	}
	for _, c := range s {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

// resolveRecordsTable validates the project and the source against the registry (there is no
// default source) and reads the live schema of the table from the source itself, so a source that
// was never scanned works. It returns the collection name the table is read through.
func resolveRecordsTable(ctx context.Context, req recordsTableRequest) (resolvedSource, string, error) {
	projectDir, ok := relatedProjectDir(req.Project)
	if !ok {
		return resolvedSource{}, "", newContractError(apicontract.ErrCodeNotFound, "unknown project", "project")
	}
	projStore, err := relatedProjectStoreFor(req.Project)
	if err != nil {
		return resolvedSource{}, "", newInvalidRequest("project", "the project store cannot be opened")
	}
	registered, err := apiResolveSource(ctx, projStore, projectDir, req.Env, req.Source)
	if err != nil {
		if errors.Is(err, api.ErrSourceUnavailable) {
			return resolvedSource{}, "", newContractError(apicontract.ErrCodeNotFound, "unknown source in this environment", "source")
		}
		return resolvedSource{}, "", newSourceUnavailable("the source cannot be read")
	}
	collection := lookupCollectionName(req.Schema, req.Table, driverOfURL(registered.URL))
	resolved, err := resolveSource(ctx, projStore, projectDir, req.Env, req.Source, collection)
	if err != nil {
		return resolvedSource{}, "", recordsLookupSchemaError(err)
	}
	return resolved, collection, nil
}

func computeRecordsLookup(ctx context.Context, req recordsLookupRequest) (apicontract.Result, error) {
	resolved, collection, err := resolveRecordsTable(ctx, req.recordsTableRequest)
	if err != nil {
		return apicontract.Result{}, err
	}
	pk := resolved.Schema.PrimaryKey
	if pk == nil || len(pk.Columns) == 0 {
		return apicontract.Result{}, newInvalidRequest("key", badKeyNoPK)
	}
	types := map[string]string{}
	for _, c := range resolved.Columns {
		types[c.Name] = c.Type
	}
	values, err := bindRecordKey(req.Key, pk.Columns, types)
	if err != nil {
		return apicontract.Result{}, err
	}

	executor, ok := secureExecutorHook()
	if !ok {
		return apicontract.Result{}, newContractError(codeInternal, "server has no policy-enforced session configured", "")
	}
	var builder dal.IQueryBuilder = dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef(collection, "")))
	for i, column := range pk.Columns {
		builder = builder.WhereField(column, dal.Equal, values[i])
	}
	// SQLite compares a number through a REAL, so an integer above 2^53 - 1 also matches the
	// integers that round to the same float: the engine returns a bucket of candidates, and the
	// one whose key is exactly the key asked for is the record. A smaller key is exact already.
	limit := 1
	exact := keyNeedsExactMatch(values)
	if exact {
		limit = recordsLookupExactCandidates
	}
	query := builder.Limit(limit).SelectIntoRecord(nil)

	result, err := runStructuredRelatedHook(executor, ctx, resolved.URL, query, nil)
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return apicontract.Result{}, newTimeout("the record lookup timed out")
		case isAccessDenied(err):
			return apicontract.Result{}, contractErrAccessDenied("access denied by policy")
		}
		if unavailable := api.SourceUnavailable(req.Source, err); unavailable != nil {
			return apicontract.Result{}, newSourceUnavailable(unavailable.Error())
		}
		// The text of the error may quote the query or a value: it is not logged or answered.
		log.Printf("records/lookup: the read failed")
		return apicontract.Result{}, newContractError(codeInternal, unclassifiedErrorMessage, "")
	}
	candidates := len(result.Rows)
	if exact {
		result.Rows = exactKeyRows(result.Rows, pk.Columns, values)
	}
	if len(result.Rows) == 0 {
		if candidates >= limit && exact {
			// The bucket is wider than the candidates read: not "no such record", and never a neighbour.
			return apicontract.Result{}, newContractError(codeInternal, "the key cannot be resolved exactly on this source", "")
		}
		return apicontract.Result{}, newContractError(apicontract.ErrCodeNotFound, "no record has this key", "key")
	}
	result.Rows = result.Rows[:1]
	recordset, err := toContractRecordsetHook(result)
	if err != nil {
		return apicontract.Result{}, newContractError(codeInternal, "the record could not be encoded", "")
	}
	response := apicontract.Result{
		Recordset:   recordset,
		Limitations: toContractLimitations(result.Limitations),
		Provenance: apicontract.Provenance{
			Source: req.Source, Collection: collection, Mode: apicontract.ProvenanceModeLive,
			ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), ExecutionProfile: apicontract.ExecutionProfileProtected,
		},
	}
	if err := relatedResultValidateHook(&response); err != nil {
		return apicontract.Result{}, newContractError(codeInternal, "the record could not be encoded", "")
	}
	return response, nil
}

// recordsLookupExactCandidates is how many rows an integer key above 2^53 - 1 reads to find the
// exact one among the rows the engine takes for equal.
const recordsLookupExactCandidates = 64

// maxExactFloatInteger is the largest integer a float64 (and a JavaScript number) holds exactly.
const maxExactFloatInteger = 1<<53 - 1

// keyNeedsExactMatch reports whether a bound value is an integer the engine may not compare exactly.
func keyNeedsExactMatch(values []any) bool {
	for _, v := range values {
		if n, ok := v.(int64); ok && (n > maxExactFloatInteger || n < -maxExactFloatInteger) {
			return true
		}
	}
	return false
}

// exactKeyRows keeps the rows whose primary-key columns are exactly the bound values. A row that
// does not hold a key column (a column the policy hides) or holds it as another type is dropped.
func exactKeyRows(rows []secureread.Row, columns []string, values []any) []secureread.Row {
	var kept []secureread.Row
	for _, row := range rows {
		match := true
		for i, column := range columns {
			if row.Data[column] != values[i] {
				match = false
			}
		}
		if match {
			kept = append(kept, row)
		}
	}
	return kept
}

// recordsLookupSchemaError turns a failure to read the live schema of the table into the answer:
// what resolveSource already classified (the source is unavailable) is kept, any other failure
// is a table that cannot be described, answered as an unknown table.
func recordsLookupSchemaError(err error) error {
	var ce *contractError
	if errors.As(err, &ce) {
		return ce
	}
	return newContractError(apicontract.ErrCodeNotFound, "unknown table", "table")
}

// driverOfURL names the driver a source URL is opened with, as far as the default schema of the
// driver goes (see api.PolicyCollectionName); "" is a driver with no default schema.
func driverOfURL(u string) string {
	switch {
	case strings.HasPrefix(u, "sqlite://"), strings.HasPrefix(u, "sqlite3://"):
		return "sqlite3"
	case strings.HasPrefix(u, "postgres://"), strings.HasPrefix(u, "postgresql://"):
		return "postgres"
	}
	return ""
}

// lookupCollectionName is the collection a lookup reads: the table, qualified by the schema of the
// request unless it is the default schema of the driver (the form the access policy names).
func lookupCollectionName(schema, table, driver string) string {
	if schema == "" {
		return table
	}
	return api.PolicyCollectionName(schema+"."+table, driver)
}

// bindRecordKey checks key against the primary-key columns pkColumns of the table and their
// declared types, and returns the values to bind, in the order of pkColumns. The key must be a
// JSON object that names exactly the columns of the primary key; a value is a string or, for an
// integer column, an exact integer literal (as a JSON number or a string). A number is read as
// its literal text (json.Number), never through a float, so an integer above 2^53 is not
// rounded to a neighbour. Nothing of the request is echoed. No query has been issued when it
// fails.
func bindRecordKey(key json.RawMessage, pkColumns []string, types map[string]string) ([]any, error) {
	dec := json.NewDecoder(bytes.NewReader(key))
	dec.UseNumber()
	var given map[string]any
	if err := dec.Decode(&given); err != nil || given == nil {
		return nil, newInvalidRequest("key", badKeyShape)
	}
	if len(given) != len(pkColumns) {
		return nil, newInvalidRequest("key", badKeyColumns)
	}
	values := make([]any, len(pkColumns))
	for i, column := range pkColumns {
		raw, ok := given[column]
		if !ok {
			return nil, newInvalidRequest("key", badKeyColumns)
		}
		v, err := bindKeyValue(raw, types[column])
		if err != nil {
			return nil, err
		}
		values[i] = v
	}
	return values, nil
}

func bindKeyValue(raw any, declared string) (any, error) {
	var literal string
	var isString bool
	switch v := raw.(type) {
	case string:
		if len(v) > recordsLookupMaxText {
			return nil, newInvalidRequest("key", badKeyValue)
		}
		literal, isString = v, true
	case json.Number:
		literal = v.String()
	default: // null, boolean, object, array
		return nil, newInvalidRequest("key", badKeyValue)
	}
	switch strings.ToLower(declared) {
	case "int", "integer", "int64", "bigint":
		n, ok := exactInteger(literal)
		if !ok {
			return nil, newInvalidRequest("key", badKeyValue)
		}
		return n, nil
	case "string", "text":
		if !isString {
			return nil, newInvalidRequest("key", badKeyValue)
		}
		return literal, nil
	}
	return nil, newInvalidRequest("key", badKeyType)
}

// exactInteger parses s only when it is a plain integer literal that fits int64: an optional '-'
// and digits with no leading zero, no '+', no space, no fraction or exponent.
func exactInteger(s string) (int64, bool) {
	digits := strings.TrimPrefix(s, "-")
	if digits == "" || (len(digits) > 1 && digits[0] == '0') || s == "-0" {
		return 0, false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// recordsSchemaColumn is a column of the live schema of a table.
type recordsSchemaColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// recordsSchemaResponse is the answer of POST /datatug/records/schema: what the lookup validates a
// key against, read from the registered source now (not from a scan).
type recordsSchemaResponse struct {
	Source       string                `json:"source"`
	Schema       string                `json:"schema"`
	Table        string                `json:"table"`
	Columns      []recordsSchemaColumn `json:"columns"`
	PrimaryKey   []string              `json:"primaryKey"`
	ReferencedBy []string              `json:"referencedBy"`
}

// recordsSchemaHandler is POST /datatug/records/schema: the live schema (columns with their types,
// the declared primary key, the tables that reference it) of one table of a registered source, for
// a source that was never scanned. It validates exactly as records/lookup does, reads no row, and
// answers the same contract errors.
func recordsSchemaHandler(w http.ResponseWriter, r *http.Request) {
	body, ok := readRecordsBody(w, r)
	if !ok {
		return
	}
	var req recordsTableRequest
	if err := decodeRecordsBody(body, &req, &req); err != nil {
		writeContractError(w, r, err)
		return
	}
	resolved, _, err := resolveRecordsTable(r.Context(), req)
	if err != nil {
		writeContractError(w, r, err)
		return
	}
	resp := recordsSchemaResponse{
		Source: req.Source, Schema: req.Schema, Table: req.Table,
		Columns: []recordsSchemaColumn{}, PrimaryKey: []string{}, ReferencedBy: []string{},
	}
	for _, c := range resolved.Columns {
		resp.Columns = append(resp.Columns, recordsSchemaColumn{Name: c.Name, Type: c.Type})
	}
	if pk := resolved.Schema.PrimaryKey; pk != nil {
		resp.PrimaryKey = append(resp.PrimaryKey, pk.Columns...)
	}
	for _, ref := range resolved.Schema.ReferencedBy {
		resp.ReferencedBy = append(resp.ReferencedBy, ref.Name())
	}
	writeContractResponse(w, r, nil, resp)
}
