package detective

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	core "github.com/datatug/datatug-core/pkg/detective"
)

var _ core.CatalogReader = (*LocalStore)(nil)

// catalogSnapshotReader is used only while LocalStore's flock is held. The
// ordinary exported GetCheck retains its own lock for standalone callers.
type catalogSnapshotReader struct{ *LocalStore }

func (r catalogSnapshotReader) GetCheck(_ context.Context, id string) (Check, error) {
	return r.getCheck(id)
}

func (s *LocalStore) loadCatalog(ctx context.Context, checkID, queryID, caseID string) (core.Catalog, error) {
	var catalog core.Catalog
	err := s.withLock(ctx, func() error {
		if caseID != "" {
			c, err := s.getCase(caseID)
			if err != nil {
				return err
			}
			if c.Status != "active" {
				return ErrInvalid
			}
		}
		var err error
		catalog, err = core.LoadCatalog(ctx, checkID, queryID, catalogSnapshotReader{s})
		return err
	})
	return catalog, err
}

func (s *LocalStore) GetPublishedValidator(_ context.Context, id string) (PublishedValidator, error) {
	data, err := safeReadFile(s.definitionDir("validators"), id)
	if errors.Is(err, os.ErrNotExist) {
		return PublishedValidator{}, ErrNotFound
	}
	if err != nil {
		return PublishedValidator{}, err
	}
	var item PublishedValidator
	if err := json.Unmarshal(data, &item); err != nil {
		return PublishedValidator{}, err
	}
	if item.ID != id {
		return PublishedValidator{}, ErrInvalid
	}
	return item, nil
}

func (s *LocalStore) GetQueryValidators(_ context.Context, id string) (QueryValidators, bool, error) {
	if !core.ValidQueryRef(id) {
		return QueryValidators{}, false, ErrInvalid
	}
	data, err := safeReadFile(s.definitionDir("query-validators"), queryValidatorFileID(id))
	if errors.Is(err, os.ErrNotExist) {
		return QueryValidators{}, false, nil
	}
	if err != nil {
		return QueryValidators{}, false, err
	}
	var item QueryValidators
	if err := json.Unmarshal(data, &item); err != nil {
		return QueryValidators{}, false, err
	}
	if item.QueryID != id {
		return QueryValidators{}, false, ErrInvalid
	}
	return item, true, nil
}

func (s *LocalStore) QueryExists(_ context.Context, id string) (bool, error) {
	_, err := s.resolveQueryRef(id)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}
