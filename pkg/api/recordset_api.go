package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/datatug-core/dto"
	"github.com/datatug/datatug-cli/pkg/datatug-core/storage"
	"github.com/strongo/validation"
)

// GetRecordsetsSummary returns board by ID
func GetRecordsetsSummary(ctx context.Context, ref dto.ProjectRef) (*dto.ProjRecordsetSummary, error) {
	if ref.ProjectID == "" {
		return nil, validation.NewErrRequestIsMissingRequiredField("project")
	}
	store, err := storage.GetProjectStore(ctx, ref.StoreID, ref.ProjectID)
	if err != nil {
		return nil, err
	}
	datasetDefinitions, err := store.LoadRecordsetDefinitions(ctx)
	if err != nil {
		return nil, err
	}
	root := dto.ProjRecordsetSummary{}
	root.ID = "/"
	for _, dsDef := range datasetDefinitions {
		dsPath := strings.Split(dsDef.ID, "/")

		var folder *dto.ProjRecordsetSummary
		if len(dsPath) > 1 {
			folder = getRecordsetFolder(&root, dsPath[1:])
		} else {
			folder = &root
		}

		ds := dto.ProjRecordsetSummary{
			ProjectItem: datatug.ProjectItem{
				ProjItemBrief: datatug.ProjItemBrief{
					ID:    dsDef.ID,
					Title: dsDef.Title,
					ListOfTags: datatug.ListOfTags{
						Tags: dsDef.Tags,
					},
				},
			},
		}
		for _, col := range dsDef.Columns {
			ds.Columns = append(ds.Columns, col.Name)
		}

		folder.Recordsets = append(folder.Recordsets, &ds)
	}
	return &root, err
}

func getRecordsetFolder(folder *dto.ProjRecordsetSummary, paths []string) *dto.ProjRecordsetSummary {
	if len(paths) == 0 {
		return folder
	}
	for _, p := range paths {
		for _, rs := range folder.Recordsets {
			if rs.ID == p {
				folder = rs
				continue
			}
		}
		newFolder := &dto.ProjRecordsetSummary{
			ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: p}},
		}
		folder.Recordsets = append(folder.Recordsets, newFolder)
		folder = newFolder
	}
	return folder
}

// GetDatasetDefinition returns definition of a dataset by ID
func GetDatasetDefinition(ctx context.Context, ref dto.ProjectItemRef) (dataset *datatug.RecordsetDefinition, err error) {
	store, err := storage.GetProjectStore(ctx, ref.StoreID, ref.ProjectID)
	if err != nil {
		return nil, err
	}
	return store.LoadRecordsetDefinition(ctx, ref.ID)
}

// GetRecordset saves board
func GetRecordset(_ context.Context, _ dto.ProjectItemRef) (recordset *datatug.Recordset, err error) {
	panic("not implemented yet")
}

// AddRecords adds record
//func AddRecords(projectID, datasetID, recordsetID string, _ []map[string]interface{}) error {
//	panic(fmt.Errorf("not implemented yet: %s, %s, %s", projectID, datasetID, recordsetID))
//}

// RecordsetRequestParams is a set of common request parameters
type RecordsetRequestParams struct {
	Project   string `json:"project"`
	Recordset string `json:"recordset"`
}

// RecordsetDataRequestParams is a set of common request parameters
type RecordsetDataRequestParams struct {
	RecordsetRequestParams
	Data string `json:"data"`
}

// Validate returns error if not valid
func (v RecordsetRequestParams) Validate() error {
	if v.Project == "" {
		return validation.NewErrRequestIsMissingRequiredField("project")
	}
	if v.Recordset == "" {
		return validation.NewErrRequestIsMissingRequiredField("recordset")
	}
	return nil
}

// Validate returns error if not valid
func (v RecordsetDataRequestParams) Validate() error {
	if err := v.RecordsetRequestParams.Validate(); err != nil {
		return err
	}
	if v.Data == "" {
		return validation.NewErrRequestIsMissingRequiredField("data")
	}
	return nil
}

// RowValues set of named values
type RowValues = map[string]interface{}

// RowWithIndex points to specific row with expected values
type RowWithIndex struct {
	Index  int                    `json:"index"`
	Values map[string]interface{} `json:"values"`
}

// Validate returns error if not valid
func (v RowWithIndex) Validate() error {
	if v.Index < 0 {
		return validation.NewErrBadRecordFieldValue("index", "should be > 0")
	}
	return nil
}

// RowWithIndexAndNewValues points to specific row with expected values and provides new set of named values
type RowWithIndexAndNewValues struct {
	RowWithIndex
	NewValues map[string]interface{} `json:"newValues"`
}

// AddRowsToRecordset adds rows to a recordset
func AddRowsToRecordset(params RecordsetDataRequestParams, _ []RowValues) (numberOfRecords int, err error) {
	if err = params.Validate(); err != nil {
		return 0, err
	}
	return 0, errNotImplementedYet
}

// RemoveRowsFromRecordset removes rows from a recordset
func RemoveRowsFromRecordset(params RecordsetDataRequestParams, rows []RowWithIndex) (numberOfRecordsAffected int, err error) {
	if err = params.Validate(); err != nil {
		return 0, err
	}
	for i, row := range rows {
		if err = row.Validate(); err != nil {
			return 0, fmt.Errorf("invalid row at index=%v: %w", i, err)
		}
	}
	return numberOfRecordsAffected, errNotImplementedYet
}

// UpdateRowsInRecordset updates rows in a recordset
func UpdateRowsInRecordset(params RecordsetDataRequestParams, rows []RowWithIndexAndNewValues) (numberOfRecordsAffected int, err error) {
	if err = params.Validate(); err != nil {
		return 0, err
	}
	for i, row := range rows {
		if err = row.Validate(); err != nil {
			return 0, fmt.Errorf("invalid row at index=%v: %w", i, err)
		}
	}
	return numberOfRecordsAffected, errNotImplementedYet
}
