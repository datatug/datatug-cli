package api

import (
	"context"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
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
	// The store joins the ID into a path of its own, so the ID is checked first.
	if err := ValidateProjectIdentifier("project", ref.ProjectID); err != nil {
		return nil, err
	}
	if err := ValidateIdentifier("envID", ref.ID); err != nil {
		return nil, err
	}
	store, err := projectStoreForID(ref.StoreID, ref.ProjectID)
	if err != nil {
		return nil, err
	}
	summary, err := store.LoadEnvironmentSummary(ctx, ref.ID)
	if err != nil {
		return nil, itemNotFound("environment", dbcopy.SourceIDDisplay(ref.ID), err)
	}
	return summary, nil
}
