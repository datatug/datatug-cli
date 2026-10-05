package endpoints

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/sneat-co/sneat-go-core/apicore"
	"github.com/strongo/validation"
)

func getRecordsetRequestParams(r *http.Request) (params api.RecordsetRequestParams, err error) {
	query := r.URL.Query()
	if params.Project = query.Get(urlParamProjectID); params.Project == "" {
		err = validation.NewErrRequestIsMissingRequiredField(urlParamProjectID)
		return
	}
	if params.Recordset = query.Get(urlParamRecordsetID); params.Recordset == "" {
		err = validation.NewErrRequestIsMissingRequiredField(urlParamRecordsetID)
		return
	}
	return
}

func getRecordsetDataParams(r *http.Request) (params api.RecordsetDataRequestParams, err error) {
	query := r.URL.Query()
	params.RecordsetRequestParams, err = getRecordsetRequestParams(r)

	if params.Data = query.Get(urlParamDataID); params.Data == "" {
		err = validation.NewErrRequestIsMissingRequiredField(urlParamDataID)
		return
	}
	return
}

// getRecordsetsSummary returns list of dataset definitions
func getRecordsetsSummary(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	ref, err := newProjectRef(query)
	if err != nil {
		handleError(err, w, r)
		return
	}
	ctx, err := getContextFromRequest(r)
	if err != nil {
		handleError(err, w, r)
	}
	datasets, err := api.GetRecordsetsSummary(ctx, ref)
	returnJSON(w, r, http.StatusOK, err, datasets)
}

// getRecordsetDefinition returns list of dataset definitions
func getRecordsetDefinition(w http.ResponseWriter, r *http.Request) {
	var ref dto.ProjectItemRef
	getProjectItem(w, r, &ref, func(ctx context.Context) (responseDTO apicore.ResponseDTO, err error) {
		return api.GetDatasetDefinition(ctx, ref)
	})
}

// recordsetDataNotImplementedSentence is the answer of recordsets/recordset_data, which this agent
// does not implement.
const recordsetDataNotImplementedSentence = "reading the data of a recordset is not implemented by this agent yet"

// getRecordsetData is not implemented: it answers 501 with a built sentence.
func getRecordsetData(w http.ResponseWriter, r *http.Request) {
	writeNotImplemented(w, r, recordsetDataNotImplementedSentence)
}

// maxRecordsetRowsCount is the largest number of rows that a client may announce with the `count` of
// recordset_add_rows: the number only sizes the list that the rows are read into.
const maxRecordsetRowsCount = 1000

// recordsetCountSentence is the answer for a `count` that is not a whole number from 0 to
// maxRecordsetRowsCount: it names nothing the client wrote.
var recordsetCountSentence = fmt.Sprintf("must be a whole number from 0 to %d", maxRecordsetRowsCount)

// parseRecordsetRowsCount reads the `count` of recordset_add_rows: absent is 0, and anything that is not a
// whole number from 0 to maxRecordsetRowsCount is a bad request that says what a count is and nothing
// of the text that was sent (nor does the log: the parse error of the number quotes it).
func parseRecordsetRowsCount(query url.Values) (int, error) {
	text := query.Get("count")
	if text == "" {
		return 0, nil
	}
	count, err := strconv.Atoi(text)
	if err != nil || count < 0 || count > maxRecordsetRowsCount {
		return 0, validation.NewErrBadRequestFieldValue("count", recordsetCountSentence)
	}
	return count, nil
}

// addRowsToRecordset adds rows to a recordset
func addRowsToRecordset(w http.ResponseWriter, r *http.Request) {
	var err error
	params, err := getRecordsetDataParams(r)
	if err != nil {
		handleError(err, w, r)
		return
	}

	count, err := parseRecordsetRowsCount(r.URL.Query())
	if err != nil {
		handleError(err, w, r)
		return
	}
	rows := make([]api.RowValues, 0, count)

	decoder := json.NewDecoder(r.Body)
	if err = decoder.Decode(&rows); err != nil {
		// The decoder's text can quote what the client wrote.
		handleError(validation.NewErrBadRequestFieldValue("body", "must be a JSON list of rows"), w, r)
		return
	}
	numberOfRecords, err := api.AddRowsToRecordset(params, nil)
	returnJSON(w, r, http.StatusCreated, err, numberOfRecords)
}

// deleteRowsFromRecordset deletes rows from a recordset
func deleteRowsFromRecordset(w http.ResponseWriter, r *http.Request) {
	executeRecordsetCommand(w, r, func(params api.RecordsetDataRequestParams, count int) (numberOfRecords int, err error) {
		rows := make([]api.RowWithIndex, 0, count)
		return api.RemoveRowsFromRecordset(params, rows)
	})
}

// updateRowsInRecordset updates rows in a recordset
func updateRowsInRecordset(w http.ResponseWriter, r *http.Request) {
	executeRecordsetCommand(w, r, func(params api.RecordsetDataRequestParams, count int) (numberOfRecords int, err error) {
		rows := make([]api.RowWithIndexAndNewValues, 0, count)
		return api.UpdateRowsInRecordset(params, rows)
	})
}

func executeRecordsetCommand(w http.ResponseWriter, r *http.Request, f func(params api.RecordsetDataRequestParams, count int) (numberOfRecords int, err error)) {
	params, err := getRecordsetDataParams(r)
	if err != nil {
		handleError(err, w, r)
		return
	}
	var count int
	if countStr := r.URL.Query().Get("count"); countStr != "" {
		if count, err = strconv.Atoi(countStr); err == nil {
			handleError(fmt.Errorf("invalid count: %v", count), w, r)
			return
		}
	}
	var numberOfRecordsAffected int
	numberOfRecordsAffected, err = f(params, count)
	if err != nil {
		handleError(err, w, r)
	}
	returnJSON(w, r, http.StatusOK, err, numberOfRecordsAffected)
}
