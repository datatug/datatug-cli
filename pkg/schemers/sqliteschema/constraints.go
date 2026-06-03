package sqliteschema

import (
	"context"
	"io"

	"github.com/datatug/datatug-cli/pkg/datatug-core/schemer"
)

var _ schemer.ConstraintsProvider = (*constraintsProvider)(nil)

// constraintsProvider does not yet extract SQLite constraints.
//
// TODO: implement native SQLite constraint extraction — primary keys via
// PRAGMA table_info, foreign keys via PRAGMA foreign_key_list, and unique
// constraints via PRAGMA index_list. Until then it returns no constraints so
// a scan completes with tables and columns rather than failing.
type constraintsProvider struct{}

func (constraintsProvider) GetConstraints(_ context.Context, _, _, _ string) (schemer.ConstraintsReader, error) {
	return emptyConstraintsReader{}, nil
}

type emptyConstraintsReader struct{}

func (emptyConstraintsReader) NextConstraint() (*schemer.Constraint, error) { return nil, io.EOF }
