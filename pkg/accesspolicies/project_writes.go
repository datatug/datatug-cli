package accesspolicies

import (
	"bytes"
	"fmt"
	"net/url"
	"strings"

	"github.com/dal-go/dalgo/access"
	"gopkg.in/yaml.v3"
)

// projectWrites is a loaded policy as AuthorizeWrite evaluates it.
type projectWrites struct {
	// policy is the loaded document with every query id its path patterns
	// name rewritten to its CanonicalQueryID, so a rule matches the query
	// whatever spelling the policy author used.
	policy access.Policy
	// refusal, when set, says why project query writes are refused while
	// this policy is loaded.
	refusal string
}

// aliasRefusal is prepareProjectWrites' refusal of a scope tree it cannot
// rewrite in place.
const aliasRefusal = "the policy uses a YAML alias or merge key in its scope tree, which project-write authorization cannot rewrite; write those scopes out in full"

// prepareProjectWrites builds the project-write view of the access
// document in data, which DecodeLoaded has already decoded. It walks every
// scope - top-level scopes and every rule set, at every nesting depth -
// and rewrites each path pattern's query-id segment
// (/datatug_projects/{project}/queries/{queryID}) to its CanonicalQueryID,
// then decodes the rewritten document. Nothing else in the document
// changes. Anything it cannot rewrite with certainty becomes a refusal, so
// a query write is never decided by a rule whose spelling was not
// canonicalized.
func prepareProjectWrites(data []byte) projectWrites {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return projectWrites{refusal: "the policy document could not be re-read to decide project writes: " + err.Error()}
	}
	var w scopeWalker
	w.document(&root)
	if w.refusal != "" {
		return projectWrites{refusal: w.refusal}
	}
	rewritten, err := yaml.Marshal(&root)
	if err == nil {
		var policy access.Policy
		if policy, err = access.DecodePolicy(bytes.NewReader(rewritten), access.YAMLCodec{}); err == nil {
			return projectWrites{policy: policy}
		}
	}
	return projectWrites{refusal: "the policy document could not be prepared to decide project writes: " + err.Error()}
}

// pathSegment is one segment of a policy path pattern, decoded.
type pathSegment struct {
	value string
	isID  bool
}

// scopeWalker rewrites the query-id segments of a policy document's scope
// paths in place and records the first reason it cannot.
type scopeWalker struct {
	refusal string
}

func (w *scopeWalker) refuse(reason string) {
	if w.refusal == "" {
		w.refusal = reason
	}
}

// document walks the top-level scopes and every rule set's scopes.
func (w *scopeWalker) document(root *yaml.Node) {
	node := root
	if node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
		node = node.Content[0]
	}
	if node.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		value := node.Content[i+1]
		switch node.Content[i].Value {
		case "scopes":
			w.scopes(value, nil)
		case "ruleSets":
			switch value.Kind {
			case yaml.AliasNode:
				w.refuse(aliasRefusal)
			case yaml.MappingNode:
				for j := 0; j+1 < len(value.Content); j += 2 {
					w.scopes(value.Content[j+1], nil)
				}
			}
		}
	}
}

// scopes walks a sequence of scopes nested under the parent path.
func (w *scopeWalker) scopes(node *yaml.Node, parent []pathSegment) {
	switch node.Kind {
	case yaml.AliasNode:
		w.refuse(aliasRefusal)
	case yaml.SequenceNode:
		for _, scope := range node.Content {
			w.scope(scope, parent)
		}
	}
}

// scope rewrites one scope's path and walks its nested scopes.
func (w *scopeWalker) scope(node *yaml.Node, parent []pathSegment) {
	if node.Kind == yaml.AliasNode {
		w.refuse(aliasRefusal)
		return
	}
	if node.Kind != yaml.MappingNode {
		return
	}
	var pathNode, children *yaml.Node
	for i := 0; i+1 < len(node.Content); i += 2 {
		switch node.Content[i].Value {
		case "path":
			pathNode = node.Content[i+1]
		case "scopes":
			children = node.Content[i+1]
		case "<<":
			w.refuse(aliasRefusal)
		}
	}
	if pathNode == nil {
		// A collectionGroup or opaqueQuery scope: DALgo compiles no path
		// scope below one, so nothing under it can name a project file.
		return
	}
	if pathNode.Kind != yaml.ScalarNode {
		w.refuse(aliasRefusal)
		return
	}
	segments, ok := foldScopePath(pathNode, parent)
	if ok && children != nil {
		w.scopes(children, segments)
	}
}

// foldScopePath parses pathNode's path the way DALgo does, appended to
// parent, and rewrites pathNode so the query id it names, if any, is in
// canonical form. It returns the scope's absolute segments; ok is false
// for a path DALgo would reject (DecodeLoaded already has).
func foldScopePath(pathNode *yaml.Node, parent []pathSegment) (segments []pathSegment, ok bool) {
	raw := strings.TrimSpace(pathNode.Value)
	if !strings.HasPrefix(raw, "/") {
		return nil, false
	}
	segments = append([]pathSegment(nil), parent...)
	if raw == "/" || raw == "/**" {
		return segments, true
	}
	body := strings.TrimSuffix(raw, "/**")
	suffix := raw[len(body):]
	parts := strings.Split(strings.TrimPrefix(body, "/"), "/")
	changed := false
	for i, part := range parts {
		decoded, err := url.PathUnescape(part)
		if part == "" || err != nil {
			return nil, false
		}
		segment := pathSegment{value: decoded, isID: i%2 == 1}
		if segment.isID && isQueryIDPosition(segments) && !isIDWildcard(decoded) {
			if canonical := CanonicalQueryID(decoded); canonical != decoded {
				parts[i] = url.PathEscape(canonical)
				segment.value = canonical
				changed = true
			}
		}
		segments = append(segments, segment)
	}
	if changed {
		pathNode.Value = "/" + strings.Join(parts, "/") + suffix
		pathNode.Style = yaml.DoubleQuotedStyle
		pathNode.Tag = "!!str"
	}
	return segments, true
}

// isQueryIDPosition reports whether the next segment after prefix is the
// query-id segment of /datatug_projects/{project}/queries/{queryID}.
func isQueryIDPosition(prefix []pathSegment) bool {
	return len(prefix) == 3 &&
		!prefix[0].isID && prefix[0].value == ProjectsCollection &&
		!prefix[2].isID && prefix[2].value == ProjectQueriesCollection
}

// isIDWildcard reports whether an id segment is "*" or a {capture}, which
// match any id and have no spelling to canonicalize.
func isIDWildcard(segment string) bool {
	return segment == "*" || strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}")
}

// String makes a pathSegment readable in test failures.
func (s pathSegment) String() string {
	return fmt.Sprintf("%q", s.value)
}
