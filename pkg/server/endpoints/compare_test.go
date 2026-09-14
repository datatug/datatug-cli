package endpoints

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/stretchr/testify/require"
)

func TestCompareHandlerRejectsUnknownCheckID(t *testing.T) {
	body := []byte(`{"securityContextId":"ctx","queryId":"orders","left":{"kind":"scope","storeId":"project","project":"project","environment":"before"},"right":{"kind":"scope","storeId":"project","project":"project","environment":"after"},"checkId":"not-supported"}`)
	req := httptest.NewRequest(http.MethodPost, "/datatug/compare", bytes.NewReader(body))
	res := httptest.NewRecorder()

	compareHandler(Capabilities{})(res, req)

	require.Equal(t, http.StatusBadRequest, res.Code)
	var envelope apicontract.CompareErrorResponse
	require.NoError(t, apicontract.DecodeStrict(res.Body.Bytes(), &envelope))
	require.Equal(t, string(apicontract.ErrCodeInvalidRequest), envelope.Error.Code)
}

func TestWriteCompareErrorPreservesIncompleteReceipts(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	left := apicontract.CompareSideReceipt{
		Execution:  apicontract.ExecutionRef{StoreID: "ops", ProjectID: "project", ExecutionID: "left-run"},
		ExecutedAt: now,
		RowCount:   2,
	}
	req := httptest.NewRequest(http.MethodPost, "/datatug/compare", nil)
	res := httptest.NewRecorder()

	writeCompareError(res, req, &compareComputationError{cause: newSnapshotExpired("snapshot is unavailable"), left: &left})

	require.Equal(t, http.StatusGone, res.Code)
	var response apicontract.CompareErrorResponse
	require.NoError(t, apicontract.DecodeStrict(res.Body.Bytes(), &response))
	require.NoError(t, response.Validate())
	require.Equal(t, string(apicontract.ErrCodeSnapshotExpired), response.Error.Code)
	require.Equal(t, left, *response.Left)
	require.Nil(t, response.Right)
}

func TestComputeCompareExecutesScopeSidesIndependently(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	receipt := func(environment, execution string) apicontract.CompareSideReceipt {
		return apicontract.CompareSideReceipt{
			Execution:  apicontract.ExecutionRef{StoreID: "project", ProjectID: "project", ExecutionID: execution},
			ExecutedAt: now,
			RowCount:   1,
		}
	}
	recordset := func(value string) apicontract.Recordset {
		return apicontract.Recordset{
			Columns: []apicontract.Column{{Name: "id", Type: string(apicontract.ValueTypeString)}, {Name: "status", Type: string(apicontract.ValueTypeString)}},
			Rows:    [][]apicontract.TypedValue{{apicontract.NewStringValue("1"), apicontract.NewStringValue(value)}},
		}
	}
	var environments []string
	deps := compareDependencies{
		executeSide: func(_ context.Context, _ apicontract.CompareRequest, side apicontract.CompareSideSpec) (compareSideData, error) {
			environments = append(environments, side.Environment)
			if side.Environment == "before" {
				return compareSideData{recordset: recordset("pending"), receipt: receipt(side.Environment, "left-run")}, nil
			}
			return compareSideData{recordset: recordset("paid"), receipt: receipt(side.Environment, "right-run")}, nil
		},
	}
	req := apicontract.CompareRequest{
		SecurityContextID: "ctx",
		QueryID:           "orders",
		Left:              apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, StoreID: "project", Project: "project", Environment: "before"},
		Right:             apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, StoreID: "project", Project: "project", Environment: "after"},
		Key:               []string{"id"},
	}

	result, err := computeCompareWith(context.Background(), req, deps)

	require.NoError(t, err)
	require.Equal(t, []string{"before", "after"}, environments)
	require.Equal(t, "left-run", result.Left.Execution.ExecutionID)
	require.Equal(t, "right-run", result.Right.Execution.ExecutionID)
	require.Equal(t, 1, result.Summary.Changed)
	require.Len(t, result.Changed, 1)
}

func TestComputeCompareRejectsTruncatedSideAndPreservesReceipts(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	recordset := apicontract.Recordset{
		Columns: []apicontract.Column{{Name: "id", Type: string(apicontract.ValueTypeString)}},
		Rows:    [][]apicontract.TypedValue{{apicontract.NewStringValue("1")}},
	}
	receipt := func(id string) apicontract.CompareSideReceipt {
		return apicontract.CompareSideReceipt{
			Execution:  apicontract.ExecutionRef{StoreID: "project", ProjectID: "project", ExecutionID: id},
			ExecutedAt: now, RowCount: 1,
		}
	}
	call := 0
	deps := compareDependencies{executeSide: func(context.Context, apicontract.CompareRequest, apicontract.CompareSideSpec) (compareSideData, error) {
		call++
		return compareSideData{recordset: recordset, receipt: receipt([]string{"left", "right"}[call-1]), truncated: call == 2}, nil
	}}
	req := apicontract.CompareRequest{
		SecurityContextID: "ctx", QueryID: "orders", Key: []string{"id"},
		Left:  apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, StoreID: "local", Project: "project", Environment: "before"},
		Right: apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, StoreID: "local", Project: "project", Environment: "after"},
	}

	_, err := computeCompareWith(context.Background(), req, deps)

	var incomplete *compareComputationError
	require.ErrorAs(t, err, &incomplete)
	require.Equal(t, apicontract.ErrCodeSourceUnavailable, incomplete.cause.(*contractError).Code)
	require.Equal(t, "left", incomplete.left.Execution.ExecutionID)
	require.Equal(t, "right", incomplete.right.Execution.ExecutionID)
}

func TestComputeCompareDuplicateIncidentMutationDoesNotExecute(t *testing.T) {
	prior := incidents.ComparisonRef{
		Left:  incidents.ExecutionRef{StoreID: "ops", ProjectID: "project", ExecutionID: "left-prior"},
		Right: incidents.ExecutionRef{StoreID: "ops", ProjectID: "project", ExecutionID: "right-prior"},
	}
	executed := 0
	req := validIncidentCompareRequest()
	_, err := computeCompareWith(context.Background(), req, compareDependencies{
		preflight: func(context.Context, apicontract.CompareRequest) (*incidents.ComparisonRef, error) {
			return &prior, nil
		},
		executeSide: func(context.Context, apicontract.CompareRequest, apicontract.CompareSideSpec) (compareSideData, error) {
			executed++
			return compareSideData{}, nil
		},
	})
	var incomplete *compareComputationError
	require.ErrorAs(t, err, &incomplete)
	require.Zero(t, executed)
	require.Equal(t, prior, *incomplete.comparison)
	var conflict *contractError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, codeRevisionConflict, conflict.Code)
}

func TestComputeCompareAppendFailurePreservesBothReceipts(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	recordset := apicontract.Recordset{
		Columns: []apicontract.Column{{Name: "id", Type: string(apicontract.ValueTypeString)}},
		Rows:    [][]apicontract.TypedValue{{apicontract.NewStringValue("1")}},
	}
	call := 0
	req := validIncidentCompareRequest()
	appendFailure := newSourceUnavailable("incident store unavailable")
	_, err := computeCompareWith(context.Background(), req, compareDependencies{
		preflight: func(context.Context, apicontract.CompareRequest) (*incidents.ComparisonRef, error) { return nil, nil },
		executeSide: func(context.Context, apicontract.CompareRequest, apicontract.CompareSideSpec) (compareSideData, error) {
			call++
			return compareSideData{recordset: recordset, receipt: apicontract.CompareSideReceipt{
				Execution:  apicontract.ExecutionRef{StoreID: "ops", ProjectID: "project", ExecutionID: fmt.Sprintf("run-%d", call)},
				ExecutedAt: now, RowCount: 1,
			}}, nil
		},
		appendRun: func(context.Context, apicontract.CompareRequest, incidents.ComparisonRef) error { return appendFailure },
	})
	var incomplete *compareComputationError
	require.ErrorAs(t, err, &incomplete)
	require.True(t, errors.Is(err, appendFailure))
	require.Equal(t, "run-1", incomplete.left.Execution.ExecutionID)
	require.Equal(t, "run-2", incomplete.right.Execution.ExecutionID)
}

func TestComputeCompareIncidentPreflightFailureDoesNotExecute(t *testing.T) {
	executed := 0
	req := validIncidentCompareRequest()
	denial := newAccessDenied("current policy does not allow incident writes")
	_, err := computeCompareWith(context.Background(), req, compareDependencies{
		preflight: func(context.Context, apicontract.CompareRequest) (*incidents.ComparisonRef, error) {
			return nil, denial
		},
		executeSide: func(context.Context, apicontract.CompareRequest, apicontract.CompareSideSpec) (compareSideData, error) {
			executed++
			return compareSideData{}, nil
		},
	})
	require.Error(t, err)
	require.Zero(t, executed)
	require.True(t, errors.Is(err, denial))
}

func TestComputeCompareIncidentWithoutKeyDoesNotPreflightOrExecute(t *testing.T) {
	req := validIncidentCompareRequest()
	req.Key = nil
	preflighted, executed := 0, 0

	_, err := computeCompareWith(context.Background(), req, compareDependencies{
		preflight: func(context.Context, apicontract.CompareRequest) (*incidents.ComparisonRef, error) {
			preflighted++
			return nil, nil
		},
		executeSide: func(context.Context, apicontract.CompareRequest, apicontract.CompareSideSpec) (compareSideData, error) {
			executed++
			return compareSideData{}, nil
		},
	})

	var contractErr *contractError
	require.ErrorAs(t, err, &contractErr)
	require.Equal(t, apicontract.ErrCodeInvalidRequest, contractErr.Code)
	require.Equal(t, "key", contractErr.Field)
	require.Zero(t, preflighted)
	require.Zero(t, executed)
}

func TestComputeCompareRightFailurePreservesLeftReceipt(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	req := validIncidentCompareRequest()
	rightFailure := newSourceUnavailable("right source unavailable")
	call := 0
	_, err := computeCompareWith(context.Background(), req, compareDependencies{
		preflight: func(context.Context, apicontract.CompareRequest) (*incidents.ComparisonRef, error) { return nil, nil },
		executeSide: func(context.Context, apicontract.CompareRequest, apicontract.CompareSideSpec) (compareSideData, error) {
			call++
			if call == 2 {
				return compareSideData{}, rightFailure
			}
			return compareSideData{receipt: apicontract.CompareSideReceipt{
				Execution:  apicontract.ExecutionRef{StoreID: "ops", ProjectID: "project", ExecutionID: "left-run"},
				ExecutedAt: now,
			}}, nil
		},
	})
	var incomplete *compareComputationError
	require.ErrorAs(t, err, &incomplete)
	require.True(t, errors.Is(err, rightFailure))
	require.NotNil(t, incomplete.left)
	require.Equal(t, "left-run", incomplete.left.Execution.ExecutionID)
	require.Nil(t, incomplete.right)
}

func TestReadableCompareSnapshotMapsEveryUnavailableStateToSnapshotExpired(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, test := range []struct {
		name     string
		response apicontract.SnapshotReadResponse
		err      error
	}{
		{name: "unretained", err: executionstore.ErrSnapshotNotFound},
		{name: "unreadable", err: errors.New("storage read failed")},
		{name: "expired", response: apicontract.SnapshotReadResponse{SnapshotState: apicontract.SnapshotState{Availability: apicontract.SnapshotExpired, ChangedAt: now, Reason: "retention"}}},
		{name: "deleted", response: apicontract.SnapshotReadResponse{SnapshotState: apicontract.SnapshotState{Availability: apicontract.SnapshotDeleted, ChangedAt: now, Reason: "deleted"}}},
		{name: "available without rows", response: apicontract.SnapshotReadResponse{SnapshotState: apicontract.SnapshotState{Availability: apicontract.SnapshotAvailable, ChangedAt: now}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := readableCompareSnapshot(test.response, test.err)
			var contractErr *contractError
			require.ErrorAs(t, err, &contractErr)
			require.Equal(t, apicontract.ErrCodeSnapshotExpired, contractErr.Code)
		})
	}
}

func TestReadableCompareSnapshotAccepts847RowsAndRejectsInputCapOverflow(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	recordset := apicontract.Recordset{
		Columns: []apicontract.Column{{Name: "id", Type: string(apicontract.ValueTypeInteger)}},
		Rows:    make([][]apicontract.TypedValue, 847),
	}
	for index := range recordset.Rows {
		recordset.Rows[index] = []apicontract.TypedValue{apicontract.NewIntegerValue(fmt.Sprint(index + 1))}
	}
	response := apicontract.SnapshotReadResponse{
		SnapshotState: apicontract.SnapshotState{Availability: apicontract.SnapshotAvailable, ChangedAt: now},
		Recordset:     &recordset,
	}

	retained, err := readableCompareSnapshot(response, nil)
	require.NoError(t, err)
	require.Len(t, retained.Rows, 847)

	response.Recordset.Rows = make([][]apicontract.TypedValue, compareInputMaximumRows+1)
	for index := range response.Recordset.Rows {
		response.Recordset.Rows[index] = []apicontract.TypedValue{apicontract.NewIntegerValue(fmt.Sprint(index + 1))}
	}
	_, err = readableCompareSnapshot(response, nil)
	var contractErr *contractError
	require.ErrorAs(t, err, &contractErr)
	require.Equal(t, apicontract.ErrCodeSourceUnavailable, contractErr.Code)
}

func validIncidentCompareRequest() apicontract.CompareRequest {
	incident := incidents.IncidentRef{StoreID: "ops", IncidentID: "INC-1"}
	return apicontract.CompareRequest{
		SecurityContextID: "ctx", QueryID: "orders", Key: []string{"id"}, Incident: &incident, MutationID: "compare-1",
		Left:  apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, StoreID: "local", Project: "project", Environment: "before"},
		Right: apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, StoreID: "local", Project: "project", Environment: "after"},
	}
}
