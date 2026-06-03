package sqliteschema

import (
	"context"
	"io"

	"github.com/datatug/datatug-cli/pkg/datatug-core/schemer"
)

var _ schemer.IndexColumnsProvider = (*indexColumnsProvider)(nil)

// indexColumnsProvider does not yet extract SQLite index columns.
//
// TODO: implement via PRAGMA index_info(index). Until then it returns no index
// columns (it is only reached once index extraction is implemented).
type indexColumnsProvider struct{}

func (indexColumnsProvider) GetIndexColumns(_ context.Context, _, _, _, _ string) (schemer.IndexColumnsReader, error) {
	return emptyIndexColumnsReader{}, nil
}

type emptyIndexColumnsReader struct{}

func (emptyIndexColumnsReader) NextIndexColumn() (*schemer.IndexColumn, error) { return nil, io.EOF }
