package api

import (
	"context"
	"fmt"
	"log"

	"github.com/strongo/validation"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
)

// A card ID is a folder or file name under the project's cards, so every entry that
// takes one checks it (and the project) before the store is asked, and none says what
// the store said when it fails (see itemNotFound).

// cardIDField is the field the refusal of a card ID names.
const cardIDField = "cardID"

// itemExists is the answer for a create of an item that is already there.
func itemExists(kind, shown string) error {
	return validation.NewBadRequestError(fmt.Errorf("%s %q already exists", kind, shown))
}

// CreateCard creates a project card under the ID it carries. A card that is already
// there is not replaced: the answer says so.
func CreateCard(ctx context.Context, ref dto.ProjectRef, card datatug.Card) (*datatug.Card, error) {
	if err := validateProjectItem(ref.ProjectID, cardIDField, card.ID); err != nil {
		return nil, err
	}
	if err := card.Validate(); err != nil {
		return nil, validation.NewBadRequestError(err)
	}
	log.Printf("api.CreateCard(ref=%+v)", ref)
	store, err := projectStoreForID(ref.StoreID, ref.ProjectID)
	if err != nil {
		return nil, err
	}
	if existing, _ := store.LoadCard(ctx, card.ID); existing != nil {
		return nil, itemExists("card", dbcopy.SourceIDDisplay(card.ID))
	}
	if err = store.SaveCard(ctx, &card); err != nil {
		return nil, itemWriteFailed("create", "card", dbcopy.SourceIDDisplay(card.ID), err)
	}
	return &card, nil
}

// GetCard returns a project card by ID.
func GetCard(ctx context.Context, ref dto.ProjectItemRef) (*datatug.Card, error) {
	if err := validateProjectItem(ref.ProjectID, cardIDField, ref.ID); err != nil {
		return nil, err
	}
	store, err := projectStoreForID(ref.StoreID, ref.ProjectID)
	if err != nil {
		return nil, err
	}
	card, err := store.LoadCard(ctx, ref.ID)
	if err != nil {
		return nil, itemNotFound("card", dbcopy.SourceIDDisplay(ref.ID), err)
	}
	return card, nil
}

// ListCards returns the briefs of the project's cards.
func ListCards(ctx context.Context, ref dto.ProjectRef) ([]datatug.CardBrief, error) {
	if err := ValidateProjectIdentifier("project", ref.ProjectID); err != nil {
		return nil, err
	}
	store, err := projectStoreForID(ref.StoreID, ref.ProjectID)
	if err != nil {
		return nil, err
	}
	cards, err := store.LoadCards(ctx)
	if err != nil {
		return nil, itemsNotLoaded("cards", ref.ProjectID, err)
	}
	return cards.Briefs(), nil
}

// DeleteCard deletes a project card.
func DeleteCard(ctx context.Context, ref dto.ProjectItemRef) error {
	if err := validateProjectItem(ref.ProjectID, cardIDField, ref.ID); err != nil {
		return err
	}
	store, err := projectStoreForID(ref.StoreID, ref.ProjectID)
	if err != nil {
		return err
	}
	return deleteItem("card", dbcopy.SourceIDDisplay(ref.ID),
		func() error { _, err := store.LoadCard(ctx, ref.ID); return err },
		func() error { return store.DeleteCard(ctx, ref.ID) })
}

// SaveCard saves a project card, replacing the one under the same ID.
func SaveCard(ctx context.Context, ref dto.ProjectRef, card datatug.Card) (*datatug.Card, error) {
	if err := validateProjectItem(ref.ProjectID, cardIDField, card.ID); err != nil {
		return nil, err
	}
	if err := card.Validate(); err != nil {
		return nil, validation.NewBadRequestError(err)
	}
	store, err := projectStoreForID(ref.StoreID, ref.ProjectID)
	if err != nil {
		return nil, err
	}
	if err = store.SaveCard(ctx, &card); err != nil {
		return nil, itemWriteFailed("save", "card", dbcopy.SourceIDDisplay(card.ID), err)
	}
	return &card, nil
}
