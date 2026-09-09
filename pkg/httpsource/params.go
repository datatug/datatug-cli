package httpsource

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/dal-go/dalgo2http"
	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
)

var placeholderRe = regexp.MustCompile(`\{([a-zA-Z0-9_]+)\}`)

// classifyParams determines, for each of a QueryDef's declared Parameters,
// whether its {id} placeholder sits in the path or the query-string portion
// of urlTemplate, and builds the dalgo2http.Param map from that — dalgo2http
// itself only needs to know each declared parameter's Location (see
// dalgo2http.Collection.URLTemplate's doc comment: the escaping it applies
// depends on the declared Location, not the placeholder's textual position),
// so this is the one piece of information this translation must supply that
// isn't already explicit in either file.
//
// It fails closed rather than silently dropping a mismatch: a declared
// Parameter whose {id} placeholder never appears in the template, or a
// template placeholder no declared Parameter names, is a genuine
// configuration error in the query's own files, and dalgo2http.NewDB would
// reject the resulting Collection anyway (see its own validate()) — this
// just names the query id in the error instead of only the collection name.
func classifyParams(id, urlTemplate string, params datatug.Parameters) (map[string]dalgo2http.Param, error) {
	pathPart, queryPart := urlTemplate, ""
	if i := strings.IndexByte(urlTemplate, '?'); i >= 0 {
		pathPart, queryPart = urlTemplate[:i], urlTemplate[i:]
	}

	declared := make(map[string]bool, len(params))
	result := make(map[string]dalgo2http.Param, len(params))
	for _, p := range params {
		declared[p.ID] = true
		placeholder := "{" + p.ID + "}"
		switch {
		case strings.Contains(pathPart, placeholder):
			result[p.ID] = dalgo2http.Param{Location: dalgo2http.ParamPath}
		case strings.Contains(queryPart, placeholder):
			result[p.ID] = dalgo2http.Param{Location: dalgo2http.ParamQuery}
		default:
			return nil, fmt.Errorf("httpsource: query %q: parameter %q has no %s placeholder in urlTemplate %q", id, p.ID, placeholder, urlTemplate)
		}
	}

	for _, m := range placeholderRe.FindAllStringSubmatch(urlTemplate, -1) {
		if !declared[m[1]] {
			return nil, fmt.Errorf("httpsource: query %q: urlTemplate %q references parameter %q, which is not declared in parameters[]", id, urlTemplate, m[1])
		}
	}
	return result, nil
}
