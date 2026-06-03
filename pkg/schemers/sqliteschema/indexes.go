package sqliteschema

import (
	"context"
	"io"

	"github.com/datatug/datatug-cli/pkg/datatug-core/schemer"
)

var _ schemer.IndexesProvider = (*indexesProvider)(nil)

// indexesProvider does not yet extract SQLite indexes.
//
// TODO: implement native SQLite index extraction via PRAGMA index_list(table).
// Until then it returns no indexes so a scan completes with tables and columns.
type indexesProvider struct{}

func (indexesProvider) GetIndexes(_ context.Context, _, _, _ string) (schemer.IndexesReader, error) {
	return emptyIndexesReader{}, nil
}

type emptyIndexesReader struct{}

func (emptyIndexesReader) NextIndex() (*schemer.Index, error) { return nil, io.EOF }
