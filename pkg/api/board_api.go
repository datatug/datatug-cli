package api

import (
	"context"
	"log"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
)

// A board ID is a folder or file name under the project's boards, so every entry that
// takes one checks it (and the project) before the store is asked, and none says what
// the store said when it fails (see itemNotFound).

// boardIDField is the field the refusal of a board ID names.
const boardIDField = "boardID"

// CreateBoard creates board
func CreateBoard(ctx context.Context, ref dto.ProjectRef, board datatug.Board) (*datatug.Board, error) {
	if err := validateProjectItem(ref.ProjectID, boardIDField, board.ID); err != nil {
		return nil, err
	}
	log.Printf("api.CreateBoard(ref=%+v)", ref)
	store, err := projectStoreForID(ref.StoreID, ref.ProjectID)
	if err != nil {
		return nil, err
	}
	if err = store.SaveBoard(ctx, &board); err != nil {
		return nil, itemWriteFailed("save", "board", dbcopy.SourceIDDisplay(board.ID), err)
	}
	return &board, nil
}

// GetBoard returns board by ID
func GetBoard(ctx context.Context, ref dto.ProjectItemRef) (*datatug.Board, error) {
	if err := validateProjectItem(ref.ProjectID, boardIDField, ref.ID); err != nil {
		return nil, err
	}
	store, err := projectStoreForID(ref.StoreID, ref.ProjectID)
	if err != nil {
		return nil, err
	}
	board, err := store.LoadBoard(ctx, ref.ID)
	if err != nil {
		return nil, itemNotFound("board", dbcopy.SourceIDDisplay(ref.ID), err)
	}
	return board, nil
}

// DeleteBoard deletes board
func DeleteBoard(ctx context.Context, ref dto.ProjectItemRef) error {
	if err := validateProjectItem(ref.ProjectID, boardIDField, ref.ID); err != nil {
		return err
	}
	store, err := projectStoreForID(ref.StoreID, ref.ProjectID)
	if err != nil {
		return err
	}
	return deleteItem("board", dbcopy.SourceIDDisplay(ref.ID),
		func() error { _, err := store.LoadBoard(ctx, ref.ID); return err },
		func() error { return store.DeleteBoard(ctx, ref.ID) })
}

// SaveBoard saves board
func SaveBoard(ctx context.Context, ref dto.ProjectRef, board datatug.Board) (*datatug.Board, error) {
	if err := validateProjectItem(ref.ProjectID, boardIDField, board.ID); err != nil {
		return nil, err
	}
	store, err := projectStoreForID(ref.StoreID, ref.ProjectID)
	if err != nil {
		return nil, err
	}
	if err = store.SaveBoard(ctx, &board); err != nil {
		return nil, itemWriteFailed("save", "board", dbcopy.SourceIDDisplay(board.ID), err)
	}
	return &board, nil
}
