// Package querybody adapts a saved query's explicit body representation to the
// structured document accepted by DataTug's policy-bound executor.
package querybody

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dal-go/dalgo/tugql"
	"github.com/datatug/datatug-core/pkg/datatug"
)

// Structured keeps structured bodies byte-for-byte. Text bodies are parsed by
// DALgo, never detected from their content. Authoring definitions and parameter
// declarations require an authorized ResolveTugQL context; this adapter refuses
// them instead of discarding them or manufacturing schema authority.
func Structured(query *datatug.QueryDef) (string, error) {
	if query.Type != datatug.QueryTypeTugQL || query.StructuredBody {
		return query.Text, nil
	}
	document, diagnostics := tugql.ParseTugQL(query.Text)
	if len(diagnostics) != 0 {
		return "", fmt.Errorf("invalid TugQL text: %s", diagnostics[0].Message)
	}
	if len(document.Tree.Definitions) != 0 || len(document.Tree.Parameters) != 0 {
		return "", fmt.Errorf("TugQL authoring definitions and parameter declarations require an authorized resolver context")
	}
	// ParseTugQL validates every tree value against the JSON-compatible profile,
	// so encoding this query map cannot fail. The executor subsequently validates
	// its executable query shape and applies normal source access policies.
	body, _ := json.Marshal(document.Tree.Query)
	return string(body), nil
}

// Extension follows Core's filename contract; content is never inspected.
func Extension(queryType datatug.QueryType, structured bool) string {
	extension := strings.ToLower(string(queryType))
	if queryType == datatug.QueryTypeTugQL && structured {
		extension += ".yaml"
	}
	return extension
}
