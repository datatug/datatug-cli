package api

import (
	"context"
	"log"

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/datatug-core/dto"
	"github.com/datatug/datatug-cli/pkg/datatug-core/storage"
)

// CreateBoard creates board
func CreateBoard(ctx context.Context, ref dto.ProjectRef, board datatug.Board) (*datatug.Board, error) {
	log.Printf("api.CreateBoard(ref=%+v)", ref)
	store, err := storage.GetProjectStore(ctx, ref.StoreID, ref.ProjectID)
	if err != nil {
		return nil, err
	}
	return &board, store.SaveBoard(ctx, &board)
}

// GetBoard returns board by ID
func GetBoard(ctx context.Context, ref dto.ProjectItemRef) (*datatug.Board, error) {
	store, err := storage.GetProjectStore(ctx, ref.StoreID, ref.ProjectID)
	if err != nil {
		return nil, err
	}
	return store.LoadBoard(ctx, ref.ID)
}

// DeleteBoard deletes board
func DeleteBoard(ctx context.Context, ref dto.ProjectItemRef) error {
	store, err := storage.GetProjectStore(ctx, ref.StoreID, ref.ProjectID)
	if err != nil {
		return err
	}
	return store.DeleteBoard(ctx, ref.ID)
}

// SaveBoard saves board
func SaveBoard(ctx context.Context, ref dto.ProjectRef, board datatug.Board) (*datatug.Board, error) {
	store, err := storage.GetProjectStore(ctx, ref.StoreID, ref.ProjectID)
	if err != nil {
		return nil, err
	}
	return &board, store.SaveBoard(ctx, &board)
}
