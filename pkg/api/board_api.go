package api

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"sync"

	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/secureread"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/strongo/validation"
)

// A board ID is a folder or file name under the project's boards, so every entry that
// takes one checks it (and the project) before the store is asked, and none says what
// the store said when it fails (see itemNotFound).

// boardIDField is the field the refusal of a board ID names.
const boardIDField = "boardID"

// createBoardMu keeps a create's existence check and write together with all
// legacy board saves in this process. Native writes also own the project lock.
var createBoardMu sync.Mutex

func localBoardWriter() (generation, actor string, allowed bool) {
	secureMu.RLock()
	defer secureMu.RUnlock()
	if securityContextID == "" || !capabilities.AllowWrites || !secureSession.Unrestricted || secureSession.Principal == nil || secureSession.Principal.ID == nil {
		return "", "", false
	}
	return securityContextID, fmt.Sprint(secureSession.Principal.ID), true
}

// CreateLocalProjectBoard is the native endpoint boundary. Only a current
// identified unrestricted operator can author a board without a board policy
// contract; browser sign-in is independent of this server-owned principal.
func CreateLocalProjectBoard(ctx context.Context, ref dto.ProjectRef, branch string, board datatug.Board) (result *datatug.Board, err error) {
	// Keep malformed identifiers a bad request before a provider is resolved.
	// Registered HTTP callers still pass the outer session/authentication guard first.
	if err := validateProjectItem(ref.ProjectID, boardIDField, board.ID); err != nil {
		return nil, err
	}
	generation, actor, allowed := localBoardWriter()
	if !allowed {
		return nil, secureread.ErrAccessDenied
	}
	err = withLocalProjectFiles(ctx, ref, branch, true, func(root *os.Root, state gitState) error {
		authority := func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			currentGeneration, currentActor, allowed := localBoardWriter()
			if !allowed || currentGeneration != generation || currentActor != actor {
				return secureread.ErrAccessDenied
			}
			return nil
		}
		authorize := func() error {
			if err := authority(); err != nil {
				return err
			}
			current, err := localGitStateSeam(ctx, root.Name())
			if err != nil {
				if canceled := ctx.Err(); canceled != nil {
					return canceled
				}
				return err
			}
			if current.branch != state.branch || current.head != state.head {
				return dto.ErrBranchHeadConflict
			}
			// Git is an I/O boundary too: no stale authority or cancellation
			// may survive its return into the actual write decision.
			return authority()
		}
		result, err = createBoard(ctx, ref, board, authorize)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// CreateBoard creates a board without replacing an existing definition.
func CreateBoard(ctx context.Context, ref dto.ProjectRef, board datatug.Board) (*datatug.Board, error) {
	return createBoard(ctx, ref, board, nil)
}

func createBoard(ctx context.Context, ref dto.ProjectRef, board datatug.Board, authorize func() error) (*datatug.Board, error) {
	if err := validateProjectItem(ref.ProjectID, boardIDField, board.ID); err != nil {
		return nil, err
	}
	if err := board.Validate(); err != nil {
		return nil, validation.NewBadRequestError(err)
	}
	log.Printf("api.CreateBoard(ref=%+v)", ref)
	store, err := projectStoreForID(ref.StoreID, ref.ProjectID)
	if err != nil {
		return nil, err
	}
	creator, supported := guardedBoardCreator(store)
	if !supported {
		return nil, dto.ErrUnsupportedCapability
	}
	createBoardMu.Lock()
	defer createBoardMu.Unlock()
	if authorize != nil {
		if err = authorize(); err != nil {
			return nil, err
		}
	}
	// Unreadable/corrupt content is never treated as an absent definition.
	switch _, err = store.LoadBoard(ctx, board.ID); {
	case err == nil:
		return nil, itemExists("board", dbcopy.SourceIDDisplay(board.ID))
	case !errors.Is(err, fs.ErrNotExist) && !record.IsNotFound(err):
		return nil, itemWriteFailed("save", "board", dbcopy.SourceIDDisplay(board.ID), err)
	}
	if authorize != nil {
		if err = authorize(); err != nil {
			return nil, err
		}
	}
	if err = creator.CreateBoardGuarded(ctx, &board, authorize); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, itemExists("board", dbcopy.SourceIDDisplay(board.ID))
		}
		return nil, itemWriteFailed("create", "board", dbcopy.SourceIDDisplay(board.ID), err)
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
	createBoardMu.Lock()
	defer createBoardMu.Unlock()
	if err = store.SaveBoard(ctx, &board); err != nil {
		return nil, itemWriteFailed("save", "board", dbcopy.SourceIDDisplay(board.ID), err)
	}
	return &board, nil
}
