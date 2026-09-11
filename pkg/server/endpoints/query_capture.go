package endpoints

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/querywrite"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
)

// POST queries/capture - "Save as project query" (hub datatug/datatug
// executable-knowledge-library REQ:capture-from-exploration, Phase 2 plan
// task 2). The browser saves a lookup it already ran as an ordinary project
// query: a "<id>.query.json" / "<id>.query.dtql" pair, left as an
// uncommitted Git change, with its purpose, typed parameters and
// source/binding provenance. No Git commit is made and no hosted team grant
// is implied.
//
// The wire types below mirror datatug-core's pkg/apicontract
// CaptureQueryRequest, CapturedQuery, CapturedParameter, EntityFieldRef,
// CaptureBindingOrigin, CaptureQueryResponse and CaptureProvenance field
// for field (frozen fixtures capture_query_*.json). They are declared here
// because no tagged datatug-core has them yet; the build-tagged
// query_capture_fixtures_test.go pins them to core's fixtures. Replace them
// with the apicontract types once go.mod moves to a release that has them.

type captureQueryRequest struct {
	Project           string        `json:"project"`
	Environment       string        `json:"environment"`
	SecurityContextID string        `json:"securityContextId"`
	IfNoneMatch       bool          `json:"ifNoneMatch,omitempty"`
	IfMatch           string        `json:"ifMatch,omitempty"`
	Query             capturedQuery `json:"query"`
}

type capturedQuery struct {
	FolderPath     string                 `json:"folderPath"`
	ID             string                 `json:"id"`
	Title          string                 `json:"title"`
	Purpose        string                 `json:"purpose"`
	Source         string                 `json:"source"`
	DTQL           string                 `json:"dtql"`
	Parameters     []capturedParameter    `json:"parameters"`
	BindingOrigins []captureBindingOrigin `json:"bindingOrigins"`
}

type capturedParameter struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Title      string          `json:"title,omitempty"`
	IsRequired bool            `json:"isRequired"`
	Meta       *entityFieldRef `json:"meta,omitempty"`
}

type entityFieldRef struct {
	Entity string `json:"entity"`
	Field  string `json:"field"`
}

type captureBindingOrigin struct {
	ParameterID string `json:"parameterId"`
	Origin      string `json:"origin"`
}

type captureQueryResponse struct {
	QueryID    string            `json:"queryId"`
	Revision   string            `json:"revision"`
	Query      capturedQuery     `json:"query"`
	Provenance captureProvenance `json:"provenance"`
}

type captureProvenance struct {
	Author      string `json:"author,omitempty"`
	Environment string `json:"environment"`
	Collection  string `json:"collection"`
}

// maxCaptureBodyBytes bounds a capture request body - the same 1 MiB the
// legacy project-item writes accept.
const maxCaptureBodyBytes = 1 << 20

// captureWriteTimeout bounds the store write (querywrite.Timeout); a
// variable only so a test can shorten it.
var captureWriteTimeout = querywrite.Timeout

// captureQueryHandler is POST queries/capture. The route is registered
// behind requireWriteCapability; computeCaptureQuery then authorizes the
// serving principal itself.
func captureQueryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeContractError(w, r, newInvalidRequest("", "only POST is supported for queries/capture"))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCaptureBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeContractError(w, r, newInvalidRequest("", fmt.Sprintf("the request body exceeds %d bytes", maxCaptureBodyBytes)))
			return
		}
		writeContractError(w, r, newInvalidRequest("", "failed to read the request body: "+err.Error()))
		return
	}
	var req captureQueryRequest
	if err := decodeCaptureBody(body, &req); err != nil {
		writeContractError(w, r, newInvalidRequest("", err.Error()))
		return
	}
	resp, status, err := computeCaptureQuery(r.Context(), req)
	if err != nil {
		writeContractError(w, r, err)
		return
	}
	writeContractResponseStatus(w, r, status, resp)
}

// decodeCaptureBody is decodeContractBody - unknown fields and duplicate
// keys rejected, so result rows, default values, fact IDs and any
// client-claimed author or principal never decode - plus a refusal of
// trailing data after the JSON value.
func decodeCaptureBody(body []byte, v any) error {
	if err := decodeContractBody(body, v); err != nil {
		return err
	}
	return rejectTrailingJSON(body)
}

// rejectTrailingJSON fails when body holds anything but whitespace after
// its first JSON value.
func rejectTrailingJSON(body []byte) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	var first json.RawMessage
	if err := dec.Decode(&first); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	if dec.More() {
		return errors.New("invalid JSON body: trailing data after the JSON value")
	}
	return nil
}

// computeCaptureQuery is captureQueryHandler's testable core. It returns
// the success envelope with 201 Created for a create or 200 OK for an
// update, and only after the store has returned. Its order, each step
// refusing before anything is written:
//
//  1. Scope: project, environment and a current securityContextId (400,
//     409 STALE_CONTEXT).
//  2. Exactly one condition, ifNoneMatch or ifMatch (400).
//  3. The query itself (validateCapturedQuery; 400): safe folder/id,
//     title, purpose, a registered-source-shaped id, DTQL that parses,
//     reads one collection and uses exactly its declared typed
//     parameters, binding origins, no credential anywhere.
//  4. The project is served here (404).
//  5. The serving principal may write project queries
//     (api.AuthorizeProjectQueryWrite; 403 ACCESS_DENIED).
//  6. The source resolves in the environment as a target a saved query
//     can bind to (400).
//  7. One atomic, conditional write through the revisioned store, bounded
//     by captureWriteTimeout; its typed refusals map to 400 or 409
//     REVISION_CONFLICT, and a store still locked by another writer when
//     the bound runs out maps to 504 TIMEOUT with nothing written.
func computeCaptureQuery(ctx context.Context, req captureQueryRequest) (captureQueryResponse, int, error) {
	if err := validateScope(apicontract.Scope{Project: req.Project, Environment: req.Environment, SecurityContextID: req.SecurityContextID}); err != nil {
		return captureQueryResponse{}, 0, err
	}
	if req.IfNoneMatch == (req.IfMatch != "") {
		return captureQueryResponse{}, 0, newInvalidRequest("ifNoneMatch/ifMatch", "exactly one of ifNoneMatch or ifMatch is required")
	}
	condition := captureWriteCondition{IfNoneMatch: req.IfNoneMatch, IfMatch: req.IfMatch}
	collection, err := validateCapturedQuery(req.Query)
	if err != nil {
		return captureQueryResponse{}, 0, err
	}
	queryID := captureQueryID(req.Query.FolderPath, req.Query.ID)

	projectDir, ok := api.ProjectDir(req.Project)
	if !ok {
		return captureQueryResponse{}, 0, newNotFound(fmt.Sprintf("unknown project %q", req.Project))
	}
	operation := access.Update
	if condition.IfNoneMatch {
		operation = access.Insert
	}
	if err := api.AuthorizeProjectQueryWrite(ctx, req.Project, queryID, operation); err != nil {
		if errors.Is(err, secureread.ErrAccessDenied) {
			return captureQueryResponse{}, 0, captureAccessDenied(err)
		}
		return captureQueryResponse{}, 0, captureInternal(err)
	}
	if err := checkCaptureSource(ctx, req.Project, projectDir, req.Environment, req.Query.Source); err != nil {
		return captureQueryResponse{}, 0, err
	}

	store, err := captureStoreFor(req.Project)
	if err != nil {
		return captureQueryResponse{}, 0, captureInternal(err)
	}
	record := capturedRecord{Query: req.Query, Author: api.SecurePrincipalID(), Environment: req.Environment, Collection: collection}
	writeCtx, cancel := context.WithTimeout(ctx, captureWriteTimeout)
	defer cancel()
	stored, err := store.PutQuery(writeCtx, record, condition)
	if err != nil {
		return captureQueryResponse{}, 0, captureStoreFailure(err, condition)
	}

	status := http.StatusOK
	if condition.IfNoneMatch {
		status = http.StatusCreated
	}
	query := stored.Record.Query
	// The contract's arrays are always arrays on the wire, never null.
	if query.Parameters == nil {
		query.Parameters = []capturedParameter{}
	}
	if query.BindingOrigins == nil {
		query.BindingOrigins = []captureBindingOrigin{}
	}
	return captureQueryResponse{
		QueryID:  captureQueryID(query.FolderPath, query.ID),
		Revision: stored.Revision,
		Query:    query,
		Provenance: captureProvenance{
			Author:      stored.Record.Author,
			Environment: stored.Record.Environment,
			Collection:  stored.Record.Collection,
		},
	}, status, nil
}

// captureQueryID is the canonical, folder-qualified query ID
// (apicontract.CanonicalQueryID on the capture contract).
func captureQueryID(folderPath, id string) string {
	if folderPath == "" {
		return id
	}
	return folderPath + "/" + id
}

// revisionConflictMessage is a failed ifMatch's message, word for word
// datatug-core's frozen fixture error_revision_conflict.json.
const revisionConflictMessage = "the query changed since that revision was read; reload it and retry"

// captureStoreFailure maps a failed store write onto the error envelope.
//
// The store's own typed refusals become a 400 or a 409; a wait that ran out
// becomes a 504 with nothing written. Everything else is a 500 with a fixed
// message (captureInternal), including a stored query file that does not
// parse: the store reports that as an ordinary error, and nothing tells it
// apart from an I/O failure, so the client is told only that the query
// could not be saved and the agent log carries which file and why. A typed
// 4xx for it ("the stored query is corrupt; replace it with ifMatch") needs
// a typed error from datatug-core's filestore, which is a change in that
// repo.
func captureStoreFailure(err error, condition captureWriteCondition) error {
	var refused *captureStoreError
	switch {
	case errors.As(err, &refused):
		switch refused.Kind {
		case captureErrConflict:
			if condition.IfNoneMatch {
				return newRevisionConflict("query.id", "a query already exists at this location; choose another id, or load it and update it with ifMatch")
			}
			return newRevisionConflict("ifMatch", revisionConflictMessage)
		case captureErrIncomplete:
			return newInvalidRequest("query.id", "the stored query at this location is an incomplete pair; replace it with ifMatch set to its current revision")
		case captureErrLocation:
			return newInvalidRequest(fieldOr(refused.Field, "query.folderPath"), refused.Reason)
		default:
			return newInvalidRequest(fieldOr(refused.Field, "query"), refused.Reason)
		}
	case errors.Is(err, context.DeadlineExceeded):
		return newTimeout("the query store stayed busy with another write, so nothing was saved; reload the query before retrying")
	default:
		return captureInternal(err)
	}
}

func fieldOr(field, fallback string) string {
	if field == "" {
		return fallback
	}
	return field
}

// captureAccessDeniedMessage is the fixed 403 message of a refused
// capture, with the agent's own reason appended when there is one a client
// may have (see captureAccessDenied).
const captureAccessDeniedMessage = "the serving principal may not save project queries"

// captureAccessDenied answers a refused capture with a 403 whose message
// names nothing the request did not: not the policy that refused it, not
// the rule or the role, and not the resource path it protects - a client
// that may not write a query may not learn which policy says so, nor that
// the query it named exists. The denial's own text, which carries all of
// that, goes to the agent log under the request ID the client is given.
//
// The exception is a denial no policy decided (WriteDeniedError.Policy is
// empty): this agent has no serve session, was started without
// --allow-writes, or has no policy loaded at all. Those reasons are the
// agent's own mode, which agent-info publishes anyway, and they name
// nothing else - no query, policy, rule or role - so the client is told
// which one it is, and can stop asking.
func captureAccessDenied(err error) *contractError {
	var denied *accesspolicies.WriteDeniedError
	if errors.As(err, &denied) && denied.Policy == "" {
		return newAccessDenied(captureAccessDeniedMessage + ": " + denied.Reason)
	}
	ce := newAccessDenied(captureAccessDeniedMessage)
	log.Printf("queries/capture: request %s: answered 403 ACCESS_DENIED: %v", ce.RequestID, err)
	return ce
}

// captureInternal answers an unexpected failure with a 500 whose message
// names no server path or other detail; the detail goes to the agent log
// under the same request ID. errCaptureStoreUnavailable is the exception:
// its message is the operator-facing reason capture is off in this build.
func captureInternal(err error) *contractError {
	if errors.Is(err, errCaptureStoreUnavailable) {
		return newContractError(codeInternal, errCaptureStoreUnavailable.Error(), "")
	}
	ce := newContractError(codeInternal, "the query could not be saved; the agent log has the details", "")
	log.Printf("queries/capture: request %s: %v", ce.RequestID, err)
	return ce
}
