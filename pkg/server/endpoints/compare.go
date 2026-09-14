package endpoints

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/comparecache"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/recordsetcompare"
)

const maxCompareBodyBytes = 1 << 20

type compareSideData struct {
	recordset apicontract.Recordset
	receipt   apicontract.CompareSideReceipt
	truncated bool
}

type compareDependencies struct {
	executeSide func(context.Context, apicontract.CompareRequest, apicontract.CompareSideSpec) (compareSideData, error)
	resolveKey  func(context.Context, apicontract.CompareRequest) ([]string, error)
	preflight   func(context.Context, apicontract.CompareRequest) (*incidents.ComparisonRef, error)
	appendRun   func(context.Context, apicontract.CompareRequest, incidents.ComparisonRef) error
}

type compareComputationError struct {
	cause      error
	left       *apicontract.CompareSideReceipt
	right      *apicontract.CompareSideReceipt
	comparison *incidents.ComparisonRef
}

func (e *compareComputationError) Error() string { return e.cause.Error() }
func (e *compareComputationError) Unwrap() error { return e.cause }

func compareHandler(caps Capabilities) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeCompareError(w, r, newInvalidRequest("", "only POST is supported for compare"))
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCompareBodyBytes))
		if err != nil {
			writeCompareError(w, r, newInvalidRequest("body", "request body is too large or unreadable"))
			return
		}
		var req apicontract.CompareRequest
		if err := decodeContractBody(body, &req); err != nil {
			writeCompareError(w, r, newInvalidRequest("", err.Error()))
			return
		}
		if err := req.Validate(); err != nil {
			writeCompareError(w, r, requestValidationError(err))
			return
		}
		if !api.ValidateSecurityContext(req.SecurityContextID) {
			writeCompareError(w, r, newStaleContext("securityContextId does not match the agent's current session; call agent-info again"))
			return
		}
		if req.Incident != nil && !caps.AllowWrites {
			writeCompareError(w, r, newAccessDenied("this agent was started without incident write capability"))
			return
		}
		result, err := computeCompareWith(r.Context(), req, compareDependencies{
			executeSide: executeCompareSide,
			resolveKey:  resolveCompareKey,
			preflight:   preflightCompareIncident,
			appendRun:   appendCompareRun,
		})
		if err != nil {
			writeCompareError(w, r, err)
			return
		}
		writeContractResponse(w, r, nil, result)
	}
}

func computeCompareWith(ctx context.Context, req apicontract.CompareRequest, deps compareDependencies) (apicontract.CompareResult, error) {
	if err := req.Validate(); err != nil {
		return apicontract.CompareResult{}, requestValidationError(err)
	}
	if len(req.Key) == 0 {
		if req.Left.Kind == apicontract.CompareSideRecord || req.Right.Kind == apicontract.CompareSideRecord {
			return apicontract.CompareResult{}, newInvalidRequest("key", "an explicit key is required when comparing an execution record")
		}
		if deps.resolveKey == nil {
			return apicontract.CompareResult{}, newInvalidRequest("key", "the live query has no resolvable mapped key")
		}
		key, err := deps.resolveKey(ctx, req)
		if err != nil {
			return apicontract.CompareResult{}, err
		}
		req.Key = key
	}
	if deps.executeSide == nil {
		return apicontract.CompareResult{}, newContractError(codeInternal, "compare side executor is not configured", "")
	}
	if req.Incident != nil && deps.preflight != nil {
		prior, err := deps.preflight(ctx, req)
		if err != nil {
			return apicontract.CompareResult{}, &compareComputationError{cause: err}
		}
		if prior != nil {
			return apicontract.CompareResult{}, &compareComputationError{
				cause:      newRevisionConflict("mutationId", "the incident mutation was already applied; no compare result was cached"),
				comparison: prior,
			}
		}
	}
	left, err := deps.executeSide(ctx, req, req.Left)
	if err != nil {
		return apicontract.CompareResult{}, &compareComputationError{cause: err}
	}
	if left.truncated {
		leftReceipt := left.receipt
		return apicontract.CompareResult{}, &compareComputationError{
			cause: newSourceUnavailable("the left comparison execution could not prove a complete bounded recordset"), left: &leftReceipt,
		}
	}
	right, err := deps.executeSide(ctx, req, req.Right)
	if err != nil {
		leftReceipt := left.receipt
		return apicontract.CompareResult{}, &compareComputationError{cause: err, left: &leftReceipt}
	}
	if right.truncated {
		leftReceipt, rightReceipt := left.receipt, right.receipt
		return apicontract.CompareResult{}, &compareComputationError{
			cause: newSourceUnavailable("the right comparison execution could not prove a complete bounded recordset"), left: &leftReceipt, right: &rightReceipt,
		}
	}
	limit := apicontract.CompareDefaultLimit
	if req.Limit != nil {
		limit = *req.Limit
	}
	options := recordsetcompare.Options{
		Key: append([]string(nil), req.Key...), DistributionColumn: req.DistributionColumn, Limit: limit,
	}
	session, cacheErr := api.BeginCompareCache(ctx, comparecache.BeginRequest{
		QueryID: req.QueryID, Left: left.receipt.Execution, Right: right.receipt.Execution,
	})
	if cacheErr != nil {
		api.LogCompareCacheSkip("begin", cacheErr)
	} else if session != nil {
		options.Observer = session.Observer()
		defer session.Abort()
	}
	result, err := recordsetcompare.Compare(left.recordset, right.recordset, left.receipt, right.receipt, options)
	if err != nil {
		leftReceipt, rightReceipt := left.receipt, right.receipt
		return apicontract.CompareResult{}, &compareComputationError{cause: newInvalidRequest("compare", err.Error()), left: &leftReceipt, right: &rightReceipt}
	}
	if session != nil {
		if commitErr := session.Commit(result); commitErr != nil {
			api.LogCompareCacheSkip("commit", commitErr)
		}
	}
	if req.Incident != nil {
		comparison := incidents.ComparisonRef{Left: incidents.ExecutionRef(result.Left.Execution), Right: incidents.ExecutionRef(result.Right.Execution)}
		if deps.appendRun == nil {
			leftReceipt, rightReceipt := left.receipt, right.receipt
			return apicontract.CompareResult{}, &compareComputationError{cause: newContractError(codeInternal, "incident append is not configured", ""), left: &leftReceipt, right: &rightReceipt}
		}
		if err := deps.appendRun(ctx, req, comparison); err != nil {
			leftReceipt, rightReceipt := left.receipt, right.receipt
			return apicontract.CompareResult{}, &compareComputationError{cause: err, left: &leftReceipt, right: &rightReceipt}
		}
	}
	if err := result.Validate(); err != nil {
		return apicontract.CompareResult{}, fmt.Errorf("validate compare result: %w", err)
	}
	return result, nil
}

func writeCompareError(w http.ResponseWriter, r *http.Request, err error) {
	writeCORSOrigin(w, r)
	w.Header().Set("Content-Type", "application/json")
	var computation *compareComputationError
	if errors.As(err, &computation) {
		err = computation.cause
	}
	var ce *contractError
	switch {
	case errors.As(err, &ce):
	case errors.Is(err, secureread.ErrAccessDenied):
		ce = contractErrAccessDenied(err.Error())
	default:
		ce = &contractError{Code: codeInternal, Message: unclassifiedErrorMessage, RequestID: newRequestID()}
		log.Printf("%s: request %s: answered 500 INTERNAL: %v", r.URL.Path, ce.RequestID, err)
	}
	response := apicontract.CompareErrorResponse{Error: ce.envelope().Error}
	if computation != nil {
		response.Left = computation.left
		response.Right = computation.right
		response.Comparison = computation.comparison
	}
	status := httpStatusFor(ce.Code)
	if ce.Code == codeInternal {
		status = http.StatusInternalServerError
	}
	w.WriteHeader(status)
	if encErr := json.NewEncoder(w).Encode(response); encErr != nil {
		log.Printf("compare endpoint: failed to encode error response: %v", encErr)
	}
}
