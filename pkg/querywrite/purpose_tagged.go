package querywrite

import "github.com/datatug/datatug-core/pkg/datatug"

// queryPurpose is q's purpose, which this build's datatug-core persists.
func queryPurpose(q *datatug.QueryDef) string {
	return q.Purpose
}
