package endpoints

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/incidentstore"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
)

func recordQueryExecution(ctx context.Context, req apicontract.ExecutionRequest, queryDef *datatug.QueryDef, queryRevision string, result apicontract.Result, started time.Time) (apicontract.ExecutionRef, error) {
	measurements, err := measureRecordset(result.Recordset, req.MeasurementProjections, result.Limitations, result.Truncated)
	if err != nil {
		return apicontract.ExecutionRef{}, newInvalidRequest("measurementProjections", err.Error())
	}
	return persistExecution(ctx, executionInput{
		Project: req.Project, Environment: req.Environment, SourceStoreID: req.StoreID,
		QueryID: req.QueryID, QueryRevision: queryRevision, DTQL: req.DTQL,
		Parameters: req.Parameters, Bindings: result.BindingsApplied, Incident: req.Incident,
		Snapshot: req.Snapshot, Measurements: measurements, SemanticFields: querySemanticFields(queryDef),
	}, result, started)
}

func recordRelatedRowsExecution(ctx context.Context, req apicontract.RelatedRowsRequest, result apicontract.Result, started time.Time) (apicontract.ExecutionRef, error) {
	canonical, err := json.Marshal(struct {
		LookupID string                 `json:"lookupId"`
		Value    apicontract.TypedValue `json:"value"`
	}{LookupID: req.LookupID, Value: req.Value})
	if err != nil {
		return apicontract.ExecutionRef{}, fmt.Errorf("encode related rows identity: %w", err)
	}
	hash := sha256.Sum256(canonical)
	value := apicontract.ScalarValue(req.Value)
	return persistExecution(ctx, executionInput{
		Project: req.Project, Environment: req.Environment, SourceStoreID: req.StoreID,
		DTQLHash: hex.EncodeToString(hash[:]), Parameters: map[string]apicontract.TypedValueOrSet{"value": value},
		Bindings: []apicontract.Binding{{
			ParameterID: "value", Value: value, Origin: apicontract.BindingOriginManual,
			OriginEvidence: apicontract.BindingOriginEvidenceClientReported,
		}}, Snapshot: req.Snapshot, Measurements: []apicontract.ScalarMeasurement{},
	}, result, started)
}

type executionInput struct {
	Project, Environment, SourceStoreID    string
	QueryID, QueryRevision, DTQL, DTQLHash string
	Parameters                             map[string]apicontract.TypedValueOrSet
	Bindings                               []apicontract.Binding
	Incident                               *apicontract.IncidentRef
	Snapshot                               bool
	Measurements                           []apicontract.ScalarMeasurement
	SemanticFields                         map[string]datatug.EntityFieldRef
}

func persistExecution(ctx context.Context, input executionInput, result apicontract.Result, started time.Time) (apicontract.ExecutionRef, error) {
	resolvedSourceStoreID, err := api.ResolveStoreID(input.SourceStoreID, input.Project)
	if err != nil || input.SourceStoreID != "" && input.SourceStoreID != resolvedSourceStoreID {
		return apicontract.ExecutionRef{}, newInvalidRequest("storeId", "does not identify the configured project store")
	}
	input.SourceStoreID = resolvedSourceStoreID
	evidenceStoreID := input.Project
	if input.Incident != nil {
		evidenceStoreID = input.Incident.StoreID
	}
	store, err := api.ExecutionEvidenceStore(input.Project, input.Incident)
	if err != nil {
		return apicontract.ExecutionRef{}, fmt.Errorf("open execution evidence store: %w", err)
	}
	if input.Incident != nil {
		if err := store.RequireIncident(ctx, *input.Incident); err != nil {
			if errors.Is(err, incidentstore.ErrIncidentNotFound) {
				return apicontract.ExecutionRef{}, newInvalidRequest("incident", "incident not found")
			}
			return apicontract.ExecutionRef{}, fmt.Errorf("resolve execution incident: %w", err)
		}
	}
	executionID, err := newExecutionID()
	if err != nil {
		return apicontract.ExecutionRef{}, newContractError(codeInternal, "create execution id", "")
	}
	ref := apicontract.ExecutionRef{StoreID: evidenceStoreID, ProjectID: input.Project, ExecutionID: executionID}
	fingerprint, err := apicontract.FingerprintRecordset(result.Recordset)
	if err != nil {
		return apicontract.ExecutionRef{}, fmt.Errorf("fingerprint execution result: %w", err)
	}
	principalID := api.SecurePrincipalID()
	if principalID == "" {
		principalID = "local-owner"
	}
	roles, groups := api.SecurePrincipalRolesGroups()
	executedAt := started.UTC().Format(time.RFC3339Nano)
	dtqlHash := input.DTQLHash
	if input.DTQL != "" {
		hash := sha256.Sum256([]byte(input.DTQL))
		dtqlHash = hex.EncodeToString(hash[:])
	}
	record := apicontract.ExecutionRecord{
		Ref:     ref,
		Scope:   apicontract.ExecutionRecordScope{StoreID: input.SourceStoreID, Project: input.Project, Environment: input.Environment},
		QueryID: input.QueryID, QueryRevision: input.QueryRevision, DTQLHash: dtqlHash,
		Parameters: normalizeParameters(input.Parameters), BindingsApplied: normalizeBindings(input.Bindings),
		Principal:         apicontract.ExecutionPrincipal{ID: principalID, Roles: roles, Groups: groups},
		PolicyFingerprint: api.SecurePolicyFingerprint(), ExecutedAt: executedAt,
		DurationMS: max(time.Since(started).Milliseconds(), 0), Limitations: normalizeLimitations(result.Limitations),
		Provenance:       result.Provenance,
		AuthorizedFields: authorizedFields(input.SourceStoreID, input.Project, input.Environment, result, input.SemanticFields),
		RowCount:         len(result.Recordset.Rows), ResultFingerprint: fingerprint,
		Incident: input.Incident, Measurements: normalizeMeasurements(input.Measurements),
	}
	if input.Snapshot {
		snapshotRecordset, allowed, policyErr := snapshotRecordsetForStorage(input.Project, result.Provenance.Source, result.Recordset)
		if policyErr != nil {
			return apicontract.ExecutionRef{}, policyErr
		}
		if allowed {
			snapshotRef, stored, snapshotErr := store.PutSnapshot(ctx, ref, snapshotRecordset, started)
			if snapshotErr != nil {
				return apicontract.ExecutionRef{}, fmt.Errorf("store execution snapshot: %w", snapshotErr)
			}
			if stored {
				record.SnapshotRef = snapshotRef
			}
		}
	}
	if err := record.Validate(); err != nil {
		if record.SnapshotRef != "" {
			_ = store.RollbackSnapshot(ctx, record.SnapshotRef)
		}
		return apicontract.ExecutionRef{}, fmt.Errorf("validate execution record: %w", err)
	}
	if err := store.PutExecution(ctx, record); err != nil {
		if record.SnapshotRef != "" {
			_ = store.RollbackSnapshot(ctx, record.SnapshotRef)
		}
		return apicontract.ExecutionRef{}, fmt.Errorf("write execution record: %w", err)
	}
	return ref, nil
}

func snapshotRecordsetForStorage(projectID, sourceID string, recordset apicontract.Recordset) (apicontract.Recordset, bool, error) {
	policy, allowed := api.SnapshotPolicy(projectID, sourceID)
	if !allowed {
		return apicontract.Recordset{}, false, nil
	}
	masked := make(map[string]struct{}, len(policy.MaskedColumns))
	for _, column := range policy.MaskedColumns {
		masked[column] = struct{}{}
	}
	indices := make([]int, 0, len(recordset.Columns))
	filtered := apicontract.Recordset{Columns: []apicontract.Column{}, Rows: make([][]apicontract.TypedValue, len(recordset.Rows))}
	for index, column := range recordset.Columns {
		if _, hidden := masked[column.Name]; hidden {
			continue
		}
		indices = append(indices, index)
		filtered.Columns = append(filtered.Columns, column)
	}
	for rowIndex, row := range recordset.Rows {
		filtered.Rows[rowIndex] = make([]apicontract.TypedValue, len(indices))
		for filteredIndex, originalIndex := range indices {
			filtered.Rows[rowIndex][filteredIndex] = row[originalIndex]
		}
	}
	if err := filtered.Validate(); err != nil {
		return apicontract.Recordset{}, false, fmt.Errorf("validate masked snapshot: %w", err)
	}
	return filtered, true, nil
}

func newExecutionID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func authorizedFields(storeID, project, environment string, result apicontract.Result, semanticFields map[string]datatug.EntityFieldRef) []apicontract.FieldAccessRef {
	fields := make([]apicontract.FieldAccessRef, len(result.Recordset.Columns))
	for i, column := range result.Recordset.Columns {
		fields[i] = apicontract.FieldAccessRef{
			StoreID: storeID, Project: project, Environment: environment,
			Source: result.Provenance.Source, Collection: result.Provenance.Collection, Column: column.Name,
		}
		if semantic := semanticFields[column.Name]; semantic.Entity != "" && semantic.Field != "" {
			fields[i].Entity = semantic.Entity
			fields[i].Field = semantic.Field
		}
	}
	return fields
}

func querySemanticFields(queryDef *datatug.QueryDef) map[string]datatug.EntityFieldRef {
	fields := map[string]datatug.EntityFieldRef{}
	if queryDef == nil || len(queryDef.Recordsets) == 0 {
		return fields
	}
	for _, column := range queryDef.Recordsets[0].Columns {
		if column.Meta != nil {
			fields[column.Name] = *column.Meta
		}
	}
	return fields
}

func normalizeParameters(values map[string]apicontract.TypedValueOrSet) map[string]apicontract.TypedValueOrSet {
	if values == nil {
		return map[string]apicontract.TypedValueOrSet{}
	}
	return values
}

func normalizeBindings(values []apicontract.Binding) []apicontract.Binding {
	if values == nil {
		return []apicontract.Binding{}
	}
	return values
}

func normalizeLimitations(values []apicontract.Limitation) []apicontract.Limitation {
	if values == nil {
		return []apicontract.Limitation{}
	}
	return values
}

func normalizeMeasurements(values []apicontract.ScalarMeasurement) []apicontract.ScalarMeasurement {
	if values == nil {
		return []apicontract.ScalarMeasurement{}
	}
	return values
}

func measureRecordset(recordset apicontract.Recordset, projections []apicontract.MeasurementProjection, limitations []apicontract.Limitation, truncated bool) ([]apicontract.ScalarMeasurement, error) {
	measurements := make([]apicontract.ScalarMeasurement, 0, len(projections))
	columns := make(map[string]int, len(recordset.Columns))
	for i, column := range recordset.Columns {
		columns[column.Name] = i
	}
	for _, projection := range projections {
		if projection.Aggregate == apicontract.MeasurementAggregateRowCount {
			value := apicontract.NewIntegerValue(fmt.Sprint(len(recordset.Rows)))
			measurements = append(measurements, apicontract.ScalarMeasurement{Projection: projection, Completeness: apicontract.MeasurementComplete, Value: &value})
			continue
		}
		reason := ""
		switch {
		case truncated:
			reason = apicontract.MeasurementReasonTruncated
		case len(limitations) > 0:
			reason = apicontract.MeasurementReasonPolicyLimited
		}
		column, ok := columns[projection.Column]
		if !ok {
			if reason == "" {
				reason = apicontract.MeasurementReasonSourceRefused
			}
			measurements = append(measurements, unavailableMeasurement(projection, reason))
			continue
		}
		if reason != "" {
			measurements = append(measurements, unavailableMeasurement(projection, reason))
			continue
		}
		measurement := aggregateMeasurement(projection, column, recordset.Rows)
		measurements = append(measurements, measurement)
	}
	return measurements, nil
}

func unavailableMeasurement(projection apicontract.MeasurementProjection, reason string) apicontract.ScalarMeasurement {
	return apicontract.ScalarMeasurement{Projection: projection, Completeness: apicontract.MeasurementUnavailable, Reason: reason}
}

func aggregateMeasurement(projection apicontract.MeasurementProjection, column int, rows [][]apicontract.TypedValue) apicontract.ScalarMeasurement {
	if projection.Aggregate == apicontract.MeasurementAggregateCount {
		count := 0
		for _, row := range rows {
			if row[column].Type != apicontract.ValueTypeNull {
				count++
			}
		}
		value := apicontract.NewIntegerValue(fmt.Sprint(count))
		return apicontract.ScalarMeasurement{Projection: projection, Completeness: apicontract.MeasurementComplete, Value: &value}
	}
	if len(rows) == 0 {
		return unavailableMeasurement(projection, apicontract.MeasurementReasonNoRows)
	}
	values := make([]*big.Rat, 0, len(rows))
	for _, row := range rows {
		value, ok := numericValue(row[column])
		if !ok {
			return unavailableMeasurement(projection, apicontract.MeasurementReasonNonNumeric)
		}
		values = append(values, value)
	}
	if projection.Aggregate == apicontract.MeasurementAggregateFirst {
		first := rows[0][column]
		return apicontract.ScalarMeasurement{Projection: projection, Completeness: apicontract.MeasurementComplete, Value: &first}
	}
	result := new(big.Rat).Set(values[0])
	switch projection.Aggregate {
	case apicontract.MeasurementAggregateSum, apicontract.MeasurementAggregateAvg:
		result.SetInt64(0)
		for _, value := range values {
			result.Add(result, value)
		}
		if projection.Aggregate == apicontract.MeasurementAggregateAvg {
			result.Quo(result, big.NewRat(int64(len(values)), 1))
		}
	case apicontract.MeasurementAggregateMin:
		for _, value := range values[1:] {
			if value.Cmp(result) < 0 {
				result.Set(value)
			}
		}
	case apicontract.MeasurementAggregateMax:
		for _, value := range values[1:] {
			if value.Cmp(result) > 0 {
				result.Set(value)
			}
		}
	}
	decimal, ok := finiteDecimal(result)
	if !ok {
		return unavailableMeasurement(projection, apicontract.MeasurementReasonSourceRefused)
	}
	value := apicontract.NewDecimalValue(decimal)
	return apicontract.ScalarMeasurement{Projection: projection, Completeness: apicontract.MeasurementComplete, Value: &value}
}

func finiteDecimal(value *big.Rat) (string, bool) {
	denominator := new(big.Int).Set(value.Denom())
	twos, fives := 0, 0
	for new(big.Int).Mod(denominator, big.NewInt(2)).Sign() == 0 {
		denominator.Quo(denominator, big.NewInt(2))
		twos++
	}
	for new(big.Int).Mod(denominator, big.NewInt(5)).Sign() == 0 {
		denominator.Quo(denominator, big.NewInt(5))
		fives++
	}
	if denominator.Cmp(big.NewInt(1)) != 0 {
		return "", false
	}
	scale := max(twos, fives)
	decimal := value.FloatString(scale)
	if scale > 0 {
		decimal = strings.TrimRight(strings.TrimRight(decimal, "0"), ".")
	}
	if decimal == "-0" || decimal == "" {
		decimal = "0"
	}
	return decimal, true
}

func numericValue(value apicontract.TypedValue) (*big.Rat, bool) {
	switch value.Type {
	case apicontract.ValueTypeInteger, apicontract.ValueTypeDecimal:
		v, ok := new(big.Rat).SetString(value.Str)
		return v, ok
	case apicontract.ValueTypeNumber:
		return new(big.Rat).SetFloat64(value.Num), true
	default:
		return nil, false
	}
}
