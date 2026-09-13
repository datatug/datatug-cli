package endpoints

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/incidentstore"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/julienschmidt/httprouter"
)

const (
	maxExecutionSeriesBodyBytes = 1 << 20
	maxExecutionSeriesRecords   = 1000
)

func executionsListHandler(w http.ResponseWriter, r *http.Request) {
	req, err := executionListRequest(r)
	if err != nil {
		writeContractError(w, r, err)
		return
	}
	response, computeErr := computeExecutionList(r, req)
	writeContractResponse(w, r, computeErr, response)
}

func executionListRequest(r *http.Request) (apicontract.ExecutionListRequest, error) {
	query := r.URL.Query()
	req := apicontract.ExecutionListRequest{Scope: apicontract.Scope{
		StoreID: query.Get("storeId"), Project: query.Get("project"), Environment: query.Get("environment"), SecurityContextID: query.Get("securityContextId"),
	}, QueryID: query.Get("queryId"), Since: query.Get("since"), Until: query.Get("until")}
	if incidentText := query.Get("incident"); incidentText != "" {
		incident, err := incidents.ParseIncidentRef(incidentText)
		if err != nil {
			return req, newInvalidRequest("incident", "invalid incident reference")
		}
		req.Incident = &incident
	}
	if limitText := query.Get("limit"); limitText != "" {
		limit, err := strconv.Atoi(limitText)
		if err != nil {
			return req, newInvalidRequest("limit", "must be an integer")
		}
		req.Limit = &limit
	}
	return req, nil
}

func computeExecutionList(r *http.Request, req apicontract.ExecutionListRequest) (apicontract.ExecutionListResponse, error) {
	if err := validateScope(req.Scope); err != nil {
		return apicontract.ExecutionListResponse{}, err
	}
	if err := req.Validate(); err != nil {
		return apicontract.ExecutionListResponse{}, requestValidationError(err)
	}
	store, err := api.ExecutionEvidenceStoreByID(req.Project, req.StoreID)
	if err != nil {
		return apicontract.ExecutionListResponse{}, newNotFound("execution evidence store not found")
	}
	records, err := store.Executions(r.Context())
	if err != nil {
		return apicontract.ExecutionListResponse{}, newContractError(codeInternal, "read execution records", "")
	}
	limit := defaultResultLimit
	if req.Limit != nil {
		limit = *req.Limit
	}
	var since, until time.Time
	if req.Since != "" {
		since, _ = time.Parse(time.RFC3339, req.Since)
	}
	if req.Until != "" {
		until, _ = time.Parse(time.RFC3339, req.Until)
	}
	response := apicontract.ExecutionListResponse{Executions: []apicontract.ExecutionRecordBrief{}}
	for _, record := range records {
		at, _ := time.Parse(time.RFC3339, record.ExecutedAt)
		if record.Scope.Project != req.Project || record.Scope.Environment != req.Environment || req.QueryID != "" && record.QueryID != req.QueryID || req.Incident != nil && (record.Incident == nil || *record.Incident != *req.Incident) || !since.IsZero() && at.Before(since) || !until.IsZero() && at.After(until) {
			continue
		}
		if err := authorizeExecutionRecord(r, record); err != nil {
			if isCurrentPolicyDenial(err) {
				continue
			}
			return apicontract.ExecutionListResponse{}, newContractError(codeInternal, "apply current policy to execution record", "")
		}
		brief, err := executionBrief(r, store, record)
		if err != nil {
			return apicontract.ExecutionListResponse{}, err
		}
		if len(response.Executions) == limit {
			response.Truncated = true
			break
		}
		response.Executions = append(response.Executions, brief)
	}
	if err := response.Validate(); err != nil {
		return apicontract.ExecutionListResponse{}, newContractError(codeInternal, "validate execution list", "")
	}
	return response, nil
}

func executionShowHandler(w http.ResponseWriter, r *http.Request) {
	record, err := computeExecutionShow(r)
	writeContractResponse(w, r, err, record)
}

func computeExecutionShow(r *http.Request) (apicontract.ExecutionRecord, error) {
	scope, ref, store, err := executionReadScope(r)
	if err != nil {
		return apicontract.ExecutionRecord{}, err
	}
	if err := validateScope(scope); err != nil {
		return apicontract.ExecutionRecord{}, err
	}
	record, err := store.Execution(r.Context(), ref)
	if err != nil {
		return apicontract.ExecutionRecord{}, executionReadError(err)
	}
	if record.Scope.Environment != scope.Environment {
		return apicontract.ExecutionRecord{}, newNotFound("execution not found")
	}
	if err := authorizeExecutionRecord(r, record); err != nil {
		if isCurrentPolicyDenial(err) {
			return apicontract.ExecutionRecord{}, newNotFound("execution not found")
		}
		return apicontract.ExecutionRecord{}, newContractError(codeInternal, "apply current policy to execution record", "")
	}
	// Grant references remain immutable in storage, but this server has no
	// incident/grant visibility resolver yet. Fail closed on the read surface:
	// never disclose a grant reference without proving that the current reader
	// can see its incident and grant scope.
	record.GrantUses = nil
	return record, nil
}

func executionSnapshotHandler(w http.ResponseWriter, r *http.Request) {
	response, err := computeExecutionSnapshot(r)
	writeContractResponse(w, r, err, response)
}

func computeExecutionSnapshot(r *http.Request) (apicontract.SnapshotReadResponse, error) {
	limit, err := executionSnapshotLimit(r)
	if err != nil {
		return apicontract.SnapshotReadResponse{}, err
	}
	scope, ref, store, err := executionReadScope(r)
	if err != nil {
		return apicontract.SnapshotReadResponse{}, err
	}
	if err := validateScope(scope); err != nil {
		return apicontract.SnapshotReadResponse{}, err
	}
	record, err := store.Execution(r.Context(), ref)
	if err != nil || record.Scope.Environment != scope.Environment || record.SnapshotRef == "" {
		return apicontract.SnapshotReadResponse{}, newNotFound("snapshot not found")
	}
	snapshot, err := store.Snapshot(r.Context(), ref, record.SnapshotRef)
	if err != nil {
		return apicontract.SnapshotReadResponse{}, executionReadError(err)
	}
	if snapshot.SnapshotState.Availability != apicontract.SnapshotAvailable {
		// Terminal lifecycle state has no rows left to replay. Recheck current
		// record visibility before disclosing even that state; metadata surfaces
		// remain fail-closed when a recorded column or row scope is no longer
		// provably visible.
		if err := authorizeExecutionRecord(r, record); err != nil {
			if isCurrentPolicyDenial(err) {
				return apicontract.SnapshotReadResponse{}, newAccessDenied("current access policy does not permit this recorded evidence")
			}
			return apicontract.SnapshotReadResponse{}, newContractError(codeInternal, "apply current policy to snapshot", "")
		}
		return snapshot, nil
	}
	// Available snapshots are authorized with their real recorded rows. A
	// metadata-only preflight would incorrectly deny a valid current policy
	// that projects away columns which RunSnapshot must instead remove.
	executor, ok := api.SecureExecutor()
	if !ok {
		return apicontract.SnapshotReadResponse{}, newContractError(codeInternal, "server has no policy-enforced session configured", "")
	}
	filtered, err := executor.RunSnapshot(r.Context(), record.Provenance.Collection, *snapshot.Recordset)
	if err != nil {
		if isCurrentPolicyDenial(err) {
			return apicontract.SnapshotReadResponse{}, newAccessDenied("current access policy does not permit this recorded evidence")
		}
		return apicontract.SnapshotReadResponse{}, newContractError(codeInternal, "apply current policy to snapshot", "")
	}
	if filtered.SnapshotRecordset == nil {
		return apicontract.SnapshotReadResponse{}, newContractError(codeInternal, "shape filtered snapshot", "")
	}
	snapshot.Recordset = filtered.SnapshotRecordset
	snapshot.Limitations = toContractLimitations(filtered.Limitations)
	if limit != nil && len(snapshot.Recordset.Rows) > *limit {
		snapshot.Recordset.Rows = snapshot.Recordset.Rows[:*limit]
		snapshot.Truncated = true
	}
	if err := snapshot.Validate(); err != nil {
		return apicontract.SnapshotReadResponse{}, newContractError(codeInternal, "validate filtered snapshot", "")
	}
	return snapshot, nil
}

func executionSnapshotLimit(r *http.Request) (*int, error) {
	text := r.URL.Query().Get("limit")
	if text == "" {
		return nil, nil
	}
	limit, err := strconv.Atoi(text)
	if err != nil || limit <= 0 || limit > maxResultLimit {
		return nil, newInvalidRequest("limit", fmt.Sprintf("must be between 1 and %d", maxResultLimit))
	}
	return &limit, nil
}

func executionSeriesHandler(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxExecutionSeriesBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeContractError(w, r, newInvalidRequest("", fmt.Sprintf("the request body exceeds %d bytes", maxExecutionSeriesBodyBytes)))
			return
		}
		writeContractError(w, r, newInvalidRequest("", "failed to read request body"))
		return
	}
	var req apicontract.ExecutionSeriesRequest
	if err := apicontract.DecodeStrict(body, &req); err != nil {
		writeContractError(w, r, newInvalidRequest("", err.Error()))
		return
	}
	response, computeErr := computeExecutionSeries(r, req)
	writeContractResponse(w, r, computeErr, response)
}

func computeExecutionSeries(r *http.Request, req apicontract.ExecutionSeriesRequest) (apicontract.ExecutionSeriesResponse, error) {
	return computeExecutionSeriesBounded(r, req, maxExecutionSeriesRecords)
}

func computeExecutionSeriesBounded(r *http.Request, req apicontract.ExecutionSeriesRequest, recordLimit int) (apicontract.ExecutionSeriesResponse, error) {
	if err := validateScope(req.Scope); err != nil {
		return apicontract.ExecutionSeriesResponse{}, err
	}
	if err := req.Validate(); err != nil {
		return apicontract.ExecutionSeriesResponse{}, requestValidationError(err)
	}
	store, err := api.ExecutionEvidenceStoreByID(req.Project, req.StoreID)
	if err != nil {
		return apicontract.ExecutionSeriesResponse{}, newNotFound("execution evidence store not found")
	}
	records, boundedOmission, err := store.ExecutionsBounded(r.Context(), recordLimit)
	if err != nil {
		return apicontract.ExecutionSeriesResponse{}, newContractError(codeInternal, "read execution records", "")
	}
	response := apicontract.ExecutionSeriesResponse{Points: []apicontract.ExecutionSeriesPoint{}, Omitted: boundedOmission}
	for _, record := range records {
		if !recordMatchesPartition(record, req.Partition) {
			continue
		}
		if err := authorizeExecutionRecord(r, record); err != nil {
			if isCurrentPolicyDenial(err) {
				response.Omitted = true
				continue
			}
			return apicontract.ExecutionSeriesResponse{}, newContractError(codeInternal, "apply current policy to execution series", "")
		}
		found := false
		for _, measurement := range record.Measurements {
			if measurement.Projection == req.Partition.Projection {
				response.Points = append(response.Points, apicontract.ExecutionSeriesPoint{
					ExecutedAt: record.ExecutedAt, Execution: record.Ref, Completeness: measurement.Completeness,
					Value: measurement.Value, Reason: measurement.Reason,
				})
				found = true
				break
			}
		}
		if !found {
			response.Omitted = true
		}
	}
	sort.Slice(response.Points, func(i, j int) bool { return response.Points[i].ExecutedAt < response.Points[j].ExecutedAt })
	if err := response.Validate(); err != nil {
		return apicontract.ExecutionSeriesResponse{}, newContractError(codeInternal, "validate execution series", "")
	}
	return response, nil
}

func isCurrentPolicyDenial(err error) bool {
	return isAccessDenied(err) || errors.Is(err, dal.ErrNotSupported) || errors.Is(err, secureread.ErrSnapshotPolicyUnexpressible)
}

func executionReadScope(r *http.Request) (apicontract.Scope, apicontract.ExecutionRef, *executionstore.Store, error) {
	query := r.URL.Query()
	scope := apicontract.Scope{StoreID: query.Get("storeId"), Project: query.Get("project"), Environment: query.Get("environment"), SecurityContextID: query.Get("securityContextId")}
	if scope.StoreID == "" {
		scope.StoreID = scope.Project
	}
	id := httprouter.ParamsFromContext(r.Context()).ByName("id")
	ref := apicontract.ExecutionRef{StoreID: scope.StoreID, ProjectID: scope.Project, ExecutionID: id}
	if err := ref.Validate(); err != nil {
		return scope, ref, nil, newInvalidRequest("id", "invalid execution id")
	}
	store, err := api.ExecutionEvidenceStoreByID(scope.Project, scope.StoreID)
	if err != nil {
		return scope, ref, nil, newNotFound("execution evidence store not found")
	}
	return scope, ref, store, nil
}

func executionReadError(err error) error {
	if errors.Is(err, executionstore.ErrSnapshotNotFound) || errors.Is(err, incidentstore.ErrExecutionNotFound) {
		return newNotFound("execution evidence not found")
	}
	return newContractError(codeInternal, "read execution evidence", "")
}

func authorizeExecutionRecord(r *http.Request, record apicontract.ExecutionRecord) error {
	executor, ok := api.SecureExecutor()
	if !ok {
		return fmt.Errorf("server has no policy-enforced session configured")
	}
	columns := make([]apicontract.Column, len(record.AuthorizedFields))
	probeRow := make([]apicontract.TypedValue, len(record.AuthorizedFields))
	for i, field := range record.AuthorizedFields {
		columns[i] = apicontract.Column{Name: field.Column, Type: string(apicontract.ValueTypeString)}
		probeRow[i] = apicontract.NewStringValue("datatug-policy-probe")
	}
	result, err := executor.RunSnapshot(r.Context(), record.Provenance.Collection, apicontract.Recordset{Columns: columns, Rows: [][]apicontract.TypedValue{probeRow}})
	if err != nil {
		return fmt.Errorf("%w: metadata policy probe: %v", secureread.ErrSnapshotPolicyUnexpressible, err)
	}
	if result.SnapshotRecordset == nil || len(result.SnapshotRecordset.Columns) != len(columns) {
		return secureread.ErrSnapshotPolicyUnexpressible
	}
	visible := make(map[string]struct{}, len(result.SnapshotRecordset.Columns))
	for _, column := range result.SnapshotRecordset.Columns {
		visible[column.Name] = struct{}{}
	}
	for _, column := range columns {
		if _, ok := visible[column.Name]; !ok {
			return secureread.ErrSnapshotPolicyUnexpressible
		}
	}
	for _, limitation := range result.Limitations {
		if limitation.Kind == secureread.LimitationRowsFiltered {
			return secureread.ErrSnapshotPolicyUnexpressible
		}
	}
	return nil
}

func executionBrief(r *http.Request, store *executionstore.Store, record apicontract.ExecutionRecord) (apicontract.ExecutionRecordBrief, error) {
	brief := apicontract.ExecutionRecordBrief{
		Ref: record.Ref, Scope: record.Scope, QueryID: record.QueryID, QueryRevision: record.QueryRevision, DTQLHash: record.DTQLHash,
		ExecutedAt: record.ExecutedAt, DurationMS: record.DurationMS, RowCount: record.RowCount,
		ResultFingerprint: record.ResultFingerprint, SnapshotRef: record.SnapshotRef, Incident: record.Incident,
	}
	if record.SnapshotRef != "" {
		state, err := store.State(r.Context(), record.SnapshotRef)
		if err != nil {
			return brief, newContractError(codeInternal, "read snapshot state", "")
		}
		brief.SnapshotState = state
	}
	return brief, nil
}

func recordMatchesPartition(record apicontract.ExecutionRecord, partition apicontract.ExecutionSeriesPartition) bool {
	return record.Ref.StoreID == partition.EvidenceStoreID && record.Scope == partition.SourceScope &&
		record.Provenance.Source == partition.Source && record.PolicyFingerprint == partition.PolicyFingerprint &&
		record.QueryID == partition.QueryID && record.QueryRevision == partition.QueryRevision && record.DTQLHash == partition.DTQLHash &&
		reflect.DeepEqual(record.BindingsApplied, partition.BindingsApplied)
}
