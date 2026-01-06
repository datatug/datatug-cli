package api

import (
	"context"

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/datatug-core/dto"
	"github.com/datatug/datatug-cli/pkg/datatug-core/storage"
	"github.com/strongo/validation"
)

// GetEnvironmentSummary returns environment summary
func GetEnvironmentSummary(ctx context.Context, ref dto.ProjectItemRef) (*datatug.EnvironmentSummary, error) {
	if ref.ProjectID == "" {
		return nil, validation.NewErrRequestIsMissingRequiredField("projID")
	}
	if ref.ID == "" {
		return nil, validation.NewErrRequestIsMissingRequiredField("envID")
	}
	store, err := storage.GetProjectStore(ctx, ref.StoreID, ref.ProjectID)
	if err != nil {
		return nil, err
	}
	return store.LoadEnvironmentSummary(ctx, ref.ID)
}
