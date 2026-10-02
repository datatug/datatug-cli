package endpoints

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/comparecache"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/recordsetcompare"
)

func compareRowsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeContractResponse(w, r, newInvalidRequest("", "only GET is supported for compare rows"), nil)
		return
	}
	query := r.URL.Query()
	if !api.ValidateSecurityContext(query.Get("securityContextId")) {
		writeContractResponse(w, r, newStaleContext("securityContextId does not match the agent's current session; call agent-info again"), nil)
		return
	}
	comparisonID := query.Get("comparisonId")
	if comparisonID == "" {
		writeContractResponse(w, r, newInvalidRequest("comparisonId", "is required"), nil)
		return
	}
	state := recordsetcompare.RecordState(query.Get("state"))
	switch state {
	case recordsetcompare.RecordMatched, recordsetcompare.RecordAdded, recordsetcompare.RecordRemoved, recordsetcompare.RecordChanged:
	default:
		writeContractResponse(w, r, newInvalidRequest("state", "must be matched, added, removed, or changed"), nil)
		return
	}
	limit := comparecache.DefaultLimit
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > apicontract.CompareMaximumLimit {
			writeContractResponse(w, r, newInvalidRequest("limit", "must be between 1 and 500"), nil)
			return
		}
		limit = parsed
	}
	meta, err := api.LookupCompareCache(r.Context(), comparisonID)
	if err != nil {
		writeContractResponse(w, r, mapCompareCacheError(err), nil)
		return
	}
	if err := authorizeCachedComparison(r.Context(), meta); err != nil {
		writeContractResponse(w, r, err, nil)
		return
	}
	page, err := api.PageCompareCache(r.Context(), comparecache.PageRequest{
		ComparisonID: comparisonID,
		State:        state,
		After:        query.Get("after"),
		Limit:        limit,
	})
	if err != nil {
		writeContractResponse(w, r, mapCompareCacheError(err), nil)
		return
	}
	writeContractResponse(w, r, nil, page)
}

func authorizeCachedComparison(ctx context.Context, meta comparecache.Meta) error {
	request := apicontract.CompareRequest{QueryID: meta.QueryID}
	left := apicontract.CompareSideSpec{Kind: apicontract.CompareSideRecord, Execution: &meta.Left}
	if _, err := loadCompareRecordSide(ctx, request, left); err != nil {
		return err
	}
	right := apicontract.CompareSideSpec{Kind: apicontract.CompareSideRecord, Execution: &meta.Right}
	if _, err := loadCompareRecordSide(ctx, request, right); err != nil {
		return err
	}
	return nil
}

func mapCompareCacheError(err error) error {
	switch {
	case errors.Is(err, comparecache.ErrNotConfigured), errors.Is(err, comparecache.ErrNotFound):
		return newSourceUnavailable("no cached comparison rows are available")
	default:
		return err
	}
}
