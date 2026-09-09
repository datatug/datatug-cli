package httpsource

import (
	"time"

	"github.com/dal-go/dalgo2http"
	"github.com/datatug/datatug-core/pkg/datatug"
)

// defaultTimeout bounds one live request per collection when nothing else
// in the QueryDef says otherwise — the QueryDef JSON schema has no timeout
// field of its own.
const defaultTimeout = 10 * time.Second

// BuildCollection translates one HTTP QueryDef into a dalgo2http.Collection.
//
// urlTemplate is the sibling .query.http file's content (see
// LoadURLTemplate). sample, when non-nil, is the query's recorded fixture
// body (see readFixtureSample) decoded as JSON; it is used ONLY to infer the
// RowsPath and KeyField defaults documented on inferRowsPath/inferKeyField —
// never to shape any other field of the returned descriptor, and a nil
// sample degrades to those functions' no-sample answers rather than
// failing.
func BuildCollection(def *datatug.QueryDef, urlTemplate string, sample map[string]any) (dalgo2http.Collection, error) {
	params, err := classifyParams(def.ID, urlTemplate, def.Parameters)
	if err != nil {
		return dalgo2http.Collection{}, err
	}

	var columns []string
	if len(def.Recordsets) > 0 {
		for _, c := range def.Recordsets[0].Columns {
			columns = append(columns, c.Name)
		}
	}

	rowsPath := inferRowsPath(sample, columns)
	row := sample
	if rowsPath != "" {
		if nested, ok := sample[rowsPath].(map[string]any); ok {
			row = nested
		}
	}

	var paramID string
	if len(def.Parameters) > 0 {
		paramID = def.Parameters[0].ID
	}
	keyField := inferKeyField(row, paramID, columns)

	return dalgo2http.Collection{
		Name:        def.ID,
		URLTemplate: urlTemplate,
		KeyField:    keyField,
		RowsPath:    rowsPath,
		Params:      params,
		Timeout:     defaultTimeout,
	}, nil
}
