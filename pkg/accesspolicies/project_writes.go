package accesspolicies

import (
	"bytes"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strings"

	"github.com/dal-go/dalgo/access"
)

// projectWrites is a loaded policy as AuthorizeWrite evaluates it.
type projectWrites struct {
	// policy is the loaded document with every query id its path patterns
	// name rewritten to its CanonicalQueryID, so a rule matches the query
	// whatever spelling the policy author used.
	policy access.Policy
	// rules is every rule the rewritten document compiles to, named and
	// pathed as DALgo compiles it (see writeRule). It is what the
	// package's own completeness test compares against the rules DALgo
	// evaluates, and it is never used to decide a write.
	rules []writeRule
	// refusal, when set, says why project query writes are refused while
	// this policy is loaded.
	refusal string
}

// writeRule is one rule of a prepared document, as DALgo compiles it: its
// name (qualified by its rule set, the way DALgo names a bound rule set's
// rules) and the absolute path pattern its scopes join to.
type writeRule struct {
	name string
	// path is the joined pattern, decoded, with DALgo's own segment kinds:
	// each scope's own path alternates collection, id, collection, id from
	// its own first segment, whatever depth the scope is nested at.
	path []pathSegment
	// trailingAll reports that the scope the rule sits in ended in "/**".
	// DALgo trims that suffix, so it selects nothing extra; it says only
	// what the author meant.
	trailingAll bool
}

// pathSegment is one segment of a policy path pattern, decoded.
type pathSegment struct {
	value string
	isID  bool
}

// prepareProjectWrites builds the project-write view of the access document
// held in data, which DecodeLoaded has already decoded through codec.
//
// It decodes the document a second time, through the same codec, into
// DALgo's own access.Document - so it sees exactly the scopes and rules
// DALgo compiled, with every YAML merge key, alias, anchor and tag and
// every JSON escape and case-insensitive key already resolved - rewrites
// each path pattern's query-id segment
// (/datatug_projects/{project}/queries/{queryID}) in those structs to its
// CanonicalQueryID, and compiles the result by encoding it through the same
// codec and decoding it as a policy again. Nothing else in the document
// changes: the re-encoded document is required to decode back to exactly
// the rewritten one, so the policy this returns is the loaded policy with
// the query ids of its paths canonicalized and nothing else.
//
// Anything it cannot rewrite with certainty becomes a refusal, so a query
// write is never decided by a rule whose spelling was not canonicalized; so
// does a rule that can never match a query at all (folderScopeRefusal),
// rather than let such a rule silently not apply.
func prepareProjectWrites(data []byte, codec access.Codec) projectWrites {
	var document access.Document
	if err := codec.Decode(bytes.NewReader(data), &document); err != nil {
		return projectWrites{refusal: "the policy document could not be re-read to decide project writes: " + err.Error()}
	}
	var w rewriter
	w.document(&document)
	if w.refusal == "" {
		w.checkRules()
	}
	if w.refusal != "" {
		return projectWrites{refusal: w.refusal}
	}
	var encoded bytes.Buffer
	if err := codec.Encode(&encoded, document); err != nil {
		return projectWrites{refusal: "the policy document could not be prepared to decide project writes: " + err.Error()}
	}
	var reread access.Document
	if err := codec.Decode(bytes.NewReader(encoded.Bytes()), &reread); err != nil {
		return projectWrites{refusal: "the policy document could not be prepared to decide project writes: " + err.Error()}
	}
	if !sameDocument(reread, document) {
		return projectWrites{refusal: "the policy document could not be prepared to decide project writes: " +
			"re-encoding it does not reproduce it exactly, so the rules a project write would be decided by are not the rules the policy declares"}
	}
	policy, err := access.DecodePolicy(bytes.NewReader(encoded.Bytes()), codec)
	if err != nil {
		return projectWrites{refusal: "the policy document could not be prepared to decide project writes: " + err.Error()}
	}
	return projectWrites{policy: policy, rules: w.rules}
}

// rewriter rewrites the query-id segments of a decoded policy document's
// scope paths in place, collects the rules the document declares, and
// records the first reason it cannot proceed.
type rewriter struct {
	refusal string
	rules   []writeRule
}

func (w *rewriter) refuse(reason string) {
	if w.refusal == "" {
		w.refusal = reason
	}
}

// document walks the top-level scopes and every rule set's scopes.
func (w *rewriter) document(document *access.Document) {
	w.scopes(document.Scopes, "", nil, false)
	setNames := make([]string, 0, len(document.RuleSets))
	for name := range document.RuleSets {
		setNames = append(setNames, name)
	}
	sort.Strings(setNames)
	for _, name := range setNames {
		w.scopes(document.RuleSets[name], name, nil, false)
	}
}

// scopes walks the scopes of rule set ruleSet ("" for the top-level
// scopes), nested under the parent path.
func (w *rewriter) scopes(scopes []access.DocumentScope, ruleSet string, parent []pathSegment, parentTrailingAll bool) {
	for i := range scopes {
		w.scope(&scopes[i], ruleSet, parent, parentTrailingAll)
	}
}

// scope rewrites one scope's path, records its rules and walks its nested
// scopes.
func (w *rewriter) scope(scope *access.DocumentScope, ruleSet string, parent []pathSegment, parentTrailingAll bool) {
	if scope.Path == "" {
		// A collectionGroup or opaqueQuery scope: DALgo compiles no path
		// scope below one, so nothing under it can name a project file.
		return
	}
	segments, trailingAll, ok := w.rewritePath(scope, parent)
	if !ok {
		return
	}
	if len(segments) == len(parent) {
		// A "/" or "/**" scope adds no segment; what the author meant by a
		// trailing "/**" further up still stands.
		trailingAll = trailingAll || parentTrailingAll
	}
	for _, rule := range scope.Rules {
		w.rules = append(w.rules, writeRule{name: qualifiedRuleName(ruleSet, rule.ID), path: segments, trailingAll: trailingAll})
	}
	w.scopes(scope.Scopes, ruleSet, segments, trailingAll)
}

// rewritePath parses scope's path the way DALgo does, appended to parent,
// and rewrites it so the query id it names, if any, is in canonical form.
// It returns the scope's absolute segments and whether the path ends in
// "/**"; ok is false for a path DALgo would reject (DecodeLoaded already
// has).
func (w *rewriter) rewritePath(scope *access.DocumentScope, parent []pathSegment) (segments []pathSegment, trailingAll, ok bool) {
	raw := strings.TrimSpace(scope.Path)
	if !strings.HasPrefix(raw, "/") {
		return nil, false, false
	}
	segments = append([]pathSegment(nil), parent...)
	if raw == "/" || raw == "/**" {
		return segments, raw == "/**", true
	}
	body := strings.TrimSuffix(raw, "/**")
	suffix := raw[len(body):]
	parts := strings.Split(strings.TrimPrefix(body, "/"), "/")
	changed := false
	for i, part := range parts {
		decoded, err := url.PathUnescape(part)
		if part == "" || err != nil {
			return nil, false, false
		}
		segment := pathSegment{value: decoded, isID: i%2 == 1}
		if segment.isID && isQueryIDPosition(segments) && !isIDWildcard(decoded) {
			canonical := CanonicalQueryID(decoded)
			if isIDWildcard(canonical) {
				w.refuse(fmt.Sprintf("the policy names the query id %q, whose canonical form %q is a wildcard, "+
					"so project query writes are refused while this policy is loaded", decoded, canonical))
				return nil, false, false
			}
			if canonical != decoded {
				parts[i] = url.PathEscape(canonical)
				segment.value = canonical
				changed = true
			}
		}
		segments = append(segments, segment)
	}
	if changed {
		scope.Path = "/" + strings.Join(parts, "/") + suffix
	}
	return segments, suffix != "", true
}

// checkRules refuses the whole document when any rule it declares is one no
// query can ever match, rather than let it silently not apply.
func (w *rewriter) checkRules() {
	for _, rule := range w.rules {
		if isBelowQueryID(rule.path, rule.trailingAll) {
			w.refuse(folderScopeRefusal(fmt.Sprintf("rule %q", rule.name), displayPath(rule.path, rule.trailingAll)))
			return
		}
	}
}

// qualifiedRuleName is the name DALgo compiles a rule under: its id, or
// "<rule set>/<id>" for a rule in a bound rule set.
func qualifiedRuleName(ruleSet, id string) string {
	id = strings.TrimSpace(id)
	if ruleSet == "" {
		return id
	}
	return ruleSet + "/" + id
}

// isBelowQueryID reports whether a rule whose absolute path is segments
// (with a trailing "/**" when trailingAll) is scoped below the query-id
// segment of /datatug_projects/{project}/queries/{queryID}: deeper than a
// query id, or a literal query id followed by "/**". A query resource has
// no segment after its id, so DALgo's prefix match can never apply such a
// rule to a query - .../queries/reports/** matches only a query whose
// whole id is "reports", never the queries in a folder "reports".
func isBelowQueryID(segments []pathSegment, trailingAll bool) bool {
	if len(segments) < 4 || !isQueryIDPosition(segments[:3]) || !segments[3].isID {
		return false
	}
	return len(segments) > 4 || trailingAll && !isIDWildcard(segments[3].value)
}

// folderScopeRefusal is prepareProjectWrites' refusal of a rule scoped
// below a query id.
func folderScopeRefusal(rule, path string) string {
	return fmt.Sprintf("%s (path %q) is scoped below a single query id, where no query can match it: "+
		"folder-scoped query rules are not supported, so project query writes are refused while this policy is loaded; "+
		"a folder-qualified query id is one path segment, written /%s/<project>/%s/<folder>%%2F<id>",
		rule, path, ProjectsCollection, ProjectQueriesCollection)
}

// displayPath renders absolute segments as a policy path.
func displayPath(segments []pathSegment, trailingAll bool) string {
	parts := make([]string, len(segments))
	for i, segment := range segments {
		parts[i] = url.PathEscape(segment.value)
	}
	path := "/" + strings.Join(parts, "/")
	if trailingAll {
		path = strings.TrimSuffix(path, "/") + "/**"
	}
	return path
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

// sameDocument reports whether two decoded documents declare the same
// policy. It is reflect.DeepEqual over both documents with an empty list or
// map read as an absent one everywhere the distinction cannot change a
// decision - never on a rule's Fields, where an empty allow-list means "no
// field" and an absent one means "every field".
func sameDocument(a, b access.Document) bool {
	return reflect.DeepEqual(normalizedDocument(a), normalizedDocument(b))
}

func normalizedDocument(d access.Document) access.Document {
	d.Scopes = normalizedScopes(d.Scopes)
	if len(d.RuleSets) == 0 {
		d.RuleSets = nil
	} else {
		sets := make(map[string][]access.DocumentScope, len(d.RuleSets))
		for name, scopes := range d.RuleSets {
			sets[name] = normalizedScopes(scopes)
		}
		d.RuleSets = sets
	}
	if d.Bindings != nil {
		bindings := *d.Bindings
		bindings.Roles = normalizedStringMap(bindings.Roles)
		bindings.Groups = normalizedStringMap(bindings.Groups)
		bindings.Users = normalizedStringMap(bindings.Users)
		bindings.Everyone = normalizedStrings(bindings.Everyone)
		d.Bindings = &bindings
	}
	return d
}

func normalizedScopes(scopes []access.DocumentScope) []access.DocumentScope {
	if len(scopes) == 0 {
		return nil
	}
	out := make([]access.DocumentScope, len(scopes))
	for i, scope := range scopes {
		scope.Path = strings.TrimSpace(scope.Path)
		scope.Rules = normalizedRules(scope.Rules)
		scope.Scopes = normalizedScopes(scope.Scopes)
		out[i] = scope
	}
	return out
}

func normalizedRules(rules []access.DocumentRule) []access.DocumentRule {
	if len(rules) == 0 {
		return nil
	}
	out := make([]access.DocumentRule, len(rules))
	for i, rule := range rules {
		rule.Operations = normalizedStrings(rule.Operations)
		out[i] = rule
	}
	return out
}

func normalizedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return values
}

func normalizedStringMap(m map[string][]string) map[string][]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string][]string, len(m))
	for key, values := range m {
		out[key] = normalizedStrings(values)
	}
	return out
}
