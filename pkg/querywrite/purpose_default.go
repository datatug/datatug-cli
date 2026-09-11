//go:build !datatug_query_capture

package querywrite

import "github.com/datatug/datatug-core/pkg/datatug"

// queryPurpose is "" in this build: the datatug-core release go.mod pins
// has no QueryDef.Purpose, so a legacy write cannot persist one.
func queryPurpose(*datatug.QueryDef) string {
	return ""
}
