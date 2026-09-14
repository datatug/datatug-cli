package endpoints

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/incidentstore"
	"github.com/datatug/datatug-core/pkg/apicontract"
)

func executeCompareSide(ctx context.Context, req apicontract.CompareRequest, side apicontract.CompareSideSpec) (compareSideData, error) {
	switch side.Kind {
	case apicontract.CompareSideScope:
		parameters := make(map[string]apicontract.TypedValueOrSet, len(side.Parameters))
		origins := make([]apicontract.BindingOriginEntry, 0, len(side.Parameters))
		names := make([]string, 0, len(side.Parameters))
		for name := range side.Parameters {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			parameters[name] = apicontract.ScalarValue(side.Parameters[name])
			origins = append(origins, apicontract.BindingOriginEntry{ParameterID: name, Origin: apicontract.BindingOriginManual})
		}
		return executeCompareLiveSide(ctx, req, side, parameters, origins)
	case apicontract.CompareSideFacts:
		return executeCompareFactsSide(ctx, req, side)
	case apicontract.CompareSideRecord:
		return loadCompareRecordSide(ctx, req, side)
	default:
		return compareSideData{}, newInvalidRequest("kind", "unsupported compare side kind")
	}
}

func executeCompareLiveSide(
	ctx context.Context,
	request apicontract.CompareRequest,
	side apicontract.CompareSideSpec,
	parameters map[string]apicontract.TypedValueOrSet,
	origins []apicontract.BindingOriginEntry,
) (compareSideData, error) {
	maxRows := apicontract.CompareMaximumLimit
	execRequest := apicontract.ExecutionRequest{
		StoreID: side.StoreID, Project: side.Project, Environment: side.Environment,
		SecurityContextID: request.SecurityContextID, QueryID: request.QueryID,
		Parameters: parameters, BindingOrigins: origins,
		Mode: apicontract.ProvenanceModeLive, Limit: &maxRows,
		Incident: request.Incident, Record: true, Snapshot: true,
	}
	if err := execRequest.Normalize(); err != nil {
		return compareSideData{}, requestValidationError(err)
	}
	result, err := computeRunQuery(ctx, execRequest)
	if err != nil {
		return compareSideData{}, err
	}
	if result.Execution == nil {
		return compareSideData{}, newContractError(codeInternal, "compare execution produced no receipt", "")
	}
	store, err := api.ExecutionEvidenceStoreByID(side.Project, result.Execution.StoreID)
	if err != nil {
		return compareSideData{}, newContractError(codeInternal, "open persisted compare receipt", "")
	}
	record, err := store.Execution(ctx, *result.Execution)
	if err != nil {
		return compareSideData{}, newContractError(codeInternal, "read persisted compare receipt", "")
	}
	reproducible := false
	if record.SnapshotRef != "" {
		if snapshot, snapshotErr := store.Snapshot(ctx, record.Ref, record.SnapshotRef); snapshotErr == nil {
			reproducible = snapshot.SnapshotState.Availability == apicontract.SnapshotAvailable && snapshot.Recordset != nil
		}
	}
	receipt := apicontract.CompareSideReceipt{
		Execution: record.Ref, ExecutedAt: record.ExecutedAt, RowCount: len(result.Recordset.Rows),
		Limitations: normalizeLimitations(result.Limitations), Reproducible: reproducible,
	}
	if err := receipt.Validate(); err != nil {
		return compareSideData{}, newContractError(codeInternal, "validate persisted compare receipt", "")
	}
	return compareSideData{recordset: result.Recordset, receipt: receipt, truncated: result.Truncated}, nil
}

func loadCompareRecordSide(ctx context.Context, request apicontract.CompareRequest, side apicontract.CompareSideSpec) (compareSideData, error) {
	if side.Execution == nil {
		return compareSideData{}, newInvalidRequest("execution", "record side requires an execution")
	}
	ref := *side.Execution
	store, err := api.ExecutionEvidenceStoreByID(ref.ProjectID, ref.StoreID)
	if err != nil {
		return compareSideData{}, newNotFound("qualified execution store not found")
	}
	record, err := store.Execution(ctx, ref)
	if err != nil {
		if errors.Is(err, incidentstore.ErrExecutionNotFound) {
			return compareSideData{}, newNotFound("qualified execution not found")
		}
		return compareSideData{}, newContractError(codeInternal, "read qualified execution receipt", "")
	}
	if record.QueryID == "" || record.QueryID != request.QueryID {
		return compareSideData{}, newInvalidRequest("queryId", "record side was not produced by the requested query")
	}
	if record.RowCount >= apicontract.CompareMaximumLimit {
		return compareSideData{}, newSourceUnavailable("the historical execution cannot prove that its retained rows are complete")
	}
	if record.SnapshotRef == "" {
		return compareSideData{}, newSnapshotExpired("the execution has no retained row snapshot")
	}
	snapshot, err := store.Snapshot(ctx, ref, record.SnapshotRef)
	retained, err := readableCompareSnapshot(snapshot, err)
	if err != nil {
		return compareSideData{}, err
	}
	executor, ok := api.SecureExecutor()
	if !ok {
		return compareSideData{}, newContractError(codeInternal, "server has no policy-enforced session configured", "")
	}
	filtered, err := executor.RunSnapshot(ctx, record.Provenance.Collection, retained)
	if err != nil {
		if isCurrentPolicyDenial(err) {
			return compareSideData{}, newAccessDenied("current access policy does not permit this recorded evidence")
		}
		return compareSideData{}, newContractError(codeInternal, "apply current policy to recorded comparison side", "")
	}
	if filtered.SnapshotRecordset == nil {
		return compareSideData{}, newContractError(codeInternal, "shape filtered comparison snapshot", "")
	}
	limitations := append([]apicontract.Limitation(nil), record.Limitations...)
	limitations = append(limitations, toContractLimitations(filtered.Limitations)...)
	limitations = normalizeLimitations(limitations)
	receipt := apicontract.CompareSideReceipt{
		Execution: ref, ExecutedAt: record.ExecutedAt, RowCount: len(filtered.SnapshotRecordset.Rows),
		Limitations: limitations, Reproducible: true,
	}
	if err := receipt.Validate(); err != nil {
		return compareSideData{}, fmt.Errorf("validate record compare receipt: %w", err)
	}
	return compareSideData{recordset: *filtered.SnapshotRecordset, receipt: receipt}, nil
}

func readableCompareSnapshot(snapshot apicontract.SnapshotReadResponse, err error) (apicontract.Recordset, error) {
	if err != nil {
		if errors.Is(err, executionstore.ErrSnapshotNotFound) {
			return apicontract.Recordset{}, newSnapshotExpired("the execution row snapshot is unavailable")
		}
		return apicontract.Recordset{}, newSnapshotExpired("the execution row snapshot cannot be read")
	}
	if snapshot.SnapshotState.Availability != apicontract.SnapshotAvailable || snapshot.Recordset == nil {
		return apicontract.Recordset{}, newSnapshotExpired("the execution row snapshot is no longer retained")
	}
	return *snapshot.Recordset, nil
}
