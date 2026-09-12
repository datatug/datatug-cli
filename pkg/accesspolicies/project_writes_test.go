package accesspolicies

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/access"
)

// The corpus below writes the same two rules - allow everything, deny
// writes to the query "Revenue" - in every document form DALgo accepts, so
// a form that hides a rule from the project-write rewrite shows up as a
// missing rule, not as a policy that happens to behave.

const corpusAllowAndDeny = `
    - path: /**
      rules:
        - id: allow-all
          effect: allow
          operations: [readwrite]
    - path: /datatug_projects/*/queries/Revenue
      rules:
        - id: protect-revenue
          effect: deny
          operations: [write]
`

const corpusHeader = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: corpus
default: deny
`

const corpusBindings = `bindings:
  roles:
    admin: [admin]
  everyone: [admin]
`

type corpusPolicy struct {
	name  string
	codec access.Codec
	doc   string
}

func policyCorpus() []corpusPolicy {
	yaml := func(name, doc string) corpusPolicy {
		return corpusPolicy{name: name, codec: access.YAMLCodec{}, doc: doc}
	}
	jsonDoc := func(name, doc string) corpusPolicy {
		return corpusPolicy{name: name, codec: access.JSONCodec{}, doc: doc}
	}
	const jsonAllow = `{"path":"/**","rules":[{"id":"allow-all","effect":"allow","operations":["readwrite"]}]}`
	const jsonDeny = `{"path":"/datatug_projects/*/queries/Revenue","rules":[{"id":"protect-revenue","effect":"deny","operations":["write"]}]}`
	const jsonHeader = `"apiVersion":"dalgo.io/access/v1","kind":"AccessPolicy","metadata":{"name":"corpus"},"default":"deny"`
	return []corpusPolicy{
		yaml("plain scopes", corpusHeader+"scopes:"+corpusAllowAndDeny),
		yaml("document markers", "---\n"+corpusHeader+"scopes:"+corpusAllowAndDeny+"...\n"),
		yaml("top-level merge key", corpusHeader+"<<:\n  scopes:"+corpusAllowAndDeny),
		yaml("rule sets merge key", corpusHeader+"ruleSets:\n  <<:\n    admin:"+corpusAllowAndDeny+corpusBindings),
		yaml("alias key", `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: &k scopes
default: deny
*k :`+corpusAllowAndDeny),
		yaml("aliased scope list", corpusHeader+"ruleSets:\n  admin: &shared"+corpusAllowAndDeny+"  support: *shared\n"+`bindings:
  roles:
    admin: [admin]
    support: [support]
`),
		yaml("merge key inside a scope", corpusHeader+`scopes:
  - path: /**
    rules:
      - id: allow-all
        effect: allow
        operations: [readwrite]
  - <<:
      path: /datatug_projects/*/queries/Revenue
    rules:
      - id: protect-revenue
        effect: deny
        operations: [write]
`),
		yaml("merge key inside a rule", corpusHeader+`scopes:
  - path: /**
    rules:
      - id: allow-all
        effect: allow
        operations: [readwrite]
  - path: /datatug_projects/*/queries/Revenue
    rules:
      - <<:
          effect: deny
          operations: [write]
        id: protect-revenue
`),
		yaml("explicit key", corpusHeader+"? scopes\n:"+corpusAllowAndDeny),
		yaml("quoted keys", corpusHeader+`"scopes":
  - "path": /**
    "rules":
      - "id": allow-all
        "effect": allow
        "operations": [readwrite]
  - 'path': '/datatug_projects/*/queries/Revenue'
    'rules':
      - 'id': protect-revenue
        'effect': deny
        'operations': [write]
`),
		yaml("tagged and anchored paths", corpusHeader+`scopes:
  - path: !!str /**
    rules:
      - id: allow-all
        effect: allow
        operations: [readwrite]
  - path: &protected !!str /datatug_projects/*/queries/Revenue
    rules:
      - id: protect-revenue
        effect: deny
        operations: [write]
  - path: *protected
    rules:
      - id: protect-revenue-again
        effect: deny
        operations: [delete]
`),
		yaml("binary path", corpusHeader+`scopes:
  - path: /**
    rules:
      - id: allow-all
        effect: allow
        operations: [readwrite]
  - path: !!binary L2RhdGF0dWdfcHJvamVjdHMvKi9xdWVyaWVzL1JldmVudWU=
    rules:
      - id: protect-revenue
        effect: deny
        operations: [write]
`),
		yaml("flow style, block scalar and padding", corpusHeader+`scopes:
  - {path: /**, rules: [{id: allow-all, effect: allow, operations: [readwrite]}]}
  - path: |-
      /datatug_projects/*/queries/Revenue
    rules:
      - id: protect-revenue
        effect: deny
        operations: [write]
  - path: "  /datatug_projects/*/queries/Revenue  "
    rules:
      - id: protect-revenue-padded
        effect: deny
        operations: [delete]
`),
		yaml("percent-encoded id", corpusHeader+`scopes:
  - path: /**
    rules:
      - id: allow-all
        effect: allow
        operations: [readwrite]
  - path: /datatug_projects/*/queries/%52evenue
    rules:
      - id: protect-revenue
        effect: deny
        operations: [write]
`),
		yaml("nested scopes", corpusHeader+`scopes:
  - path: /**
    rules:
      - id: allow-all
        effect: allow
        operations: [readwrite]
  - path: /datatug_projects/demo
    scopes:
      - path: /queries/Revenue
        rules:
          - id: protect-revenue
            effect: deny
            operations: [write]
`),
		jsonDoc("plain json", `{`+jsonHeader+`,"scopes":[`+jsonAllow+`,`+jsonDeny+`]}`),
		jsonDoc("escaped slashes", `{`+jsonHeader+`,"scopes":[`+jsonAllow+`,
 {"path":"\/datatug_projects\/*\/queries\/Revenue","rules":[{"id":"protect-revenue","effect":"deny","operations":["write"]}]}]}`),
		jsonDoc("unicode escapes", `{`+jsonHeader+`,"scopes":[`+jsonAllow+`,
 {"path":"/datatug_projects/*/queries/Revenue","rules":[{"id":"protect-revenue","effect":"deny","operations":["write"]}]}]}`),
		jsonDoc("case-insensitive keys", `{`+jsonHeader+`,"Scopes":[`+jsonAllow+`,
 {"Path":"/datatug_projects/*/queries/Revenue","RULES":[{"ID":"protect-revenue","Effect":"deny","Operations":["write"]}]}]}`),
		jsonDoc("duplicate key", `{`+jsonHeader+`,"scopes":[`+jsonAllow+`,
 {"path":"/datatug_projects/*/queries/Other","path":"/datatug_projects/*/queries/Revenue","rules":[{"id":"protect-revenue","effect":"deny","operations":["write"]}]}]}`),
		jsonDoc("json rule sets", `{`+jsonHeader+`,"ruleSets":{"admin":[`+jsonAllow+`,`+jsonDeny+`]},"bindings":{"everyone":["admin"]}}`),
	}
}

// TestPrepareProjectWrites_RewritesEveryRuleDALgoEvaluates is the
// completeness proof: for every document form in the corpus, the rules the
// project-write policy is compiled from are exactly the rules DALgo
// compiles from the document as loaded, one for one, with every query id
// canonicalized - so no document form can hide a rule from the rewrite.
func TestPrepareProjectWrites_RewritesEveryRuleDALgoEvaluates(t *testing.T) {
	for _, policy := range policyCorpus() {
		t.Run(policy.name, func(t *testing.T) {
			loaded, err := DecodeLoaded([]byte(policy.doc), policy.codec, serverPolicyDir+"corpus")
			if err != nil {
				t.Fatalf("the corpus document must load: %v", err)
			}
			if loaded.writes.refusal != "" {
				t.Fatalf("project writes are refused: %s", loaded.writes.refusal)
			}
			loadedRules := flattenPolicy(t, loaded.Policy)
			if len(loadedRules) == 0 {
				t.Fatal("the document declares no rules; the corpus entry is broken")
			}
			writeRules := flattenPolicy(t, loaded.writes.policy)
			assertSameRules(t, "the rules DALgo evaluates for writes", writeRules, canonicalizedRules(loadedRules))
			assertSameRules(t, "the rules the rewrite recorded", loaded.writes.rules, canonicalizedRules(loadedRules))
		})
	}
}

// TestPrepareProjectWrites_EveryCorpusFormDeniesEverySpelling is the same
// corpus end to end through AuthorizeWrite: whatever form the document is
// written in, its deny rule holds for every spelling of the query it
// names, and an unrelated query stays writable.
func TestPrepareProjectWrites_EveryCorpusFormDeniesEverySpelling(t *testing.T) {
	for _, policy := range policyCorpus() {
		t.Run(policy.name, func(t *testing.T) {
			loaded, err := DecodeLoaded([]byte(policy.doc), policy.codec, serverPolicyDir+"corpus")
			if err != nil {
				t.Fatalf("the corpus document must load: %v", err)
			}
			options := WriteOptions{Principal: principalWithRoles("alice", "admin", "support"), Policies: []Loaded{loaded}}
			for _, id := range []string{"Revenue", "revenue", "REVENUE", "rEvEnUe"} {
				err := AuthorizeWrite(context.Background(), options, access.Set, ProjectQueryResource("demo", id))
				var denied *WriteDeniedError
				if !errors.As(err, &denied) || !strings.Contains(denied.Reason, "protect-revenue") {
					t.Errorf("write %q: expected the deny rule to refuse it, got %v", id, err)
				}
			}
			if err := AuthorizeWrite(context.Background(), options, access.Set, ProjectQueryResource("demo", "expenses")); err != nil {
				t.Errorf("an unrelated query must stay writable, got %v", err)
			}
		})
	}
}

// A stream with a second document is refused at load, by DALgo itself, so
// no half-read document can reach the project-write rewrite.
func TestDecodeLoaded_RefusesASecondDocument(t *testing.T) {
	doc := corpusHeader + "scopes:" + corpusAllowAndDeny + "---\n" + corpusHeader + "scopes:" + corpusAllowAndDeny
	if _, err := DecodeLoaded([]byte(doc), access.YAMLCodec{}, "two.yaml"); err == nil {
		t.Fatal("a two-document stream must not load")
	}
}

// A document the rewrite cannot reproduce through its own codec refuses
// every project query write rather than decide one from rules that are not
// the policy's own.
func TestPrepareProjectWrites_RefusesWhatItCannotReproduce(t *testing.T) {
	// An empty fields list on an allow rule means "no field", which
	// omitempty cannot re-encode; the write view refuses instead of
	// silently widening the rule to every field.
	doc := corpusHeader + `scopes:
  - path: /datatug_projects/*/queries/*
    rules:
      - id: curate
        effect: allow
        operations: [write]
        fields: []
`
	loaded, err := DecodeLoaded([]byte(doc), access.YAMLCodec{}, "fields.yaml")
	if err != nil {
		t.Fatalf("the document must load: %v", err)
	}
	err = AuthorizeWrite(context.Background(), WriteOptions{Principal: principalWithRoles("alice"), Policies: []Loaded{loaded}},
		access.Set, ProjectQueryResource("demo", "revenue"))
	var denied *WriteDeniedError
	if !errors.As(err, &denied) || !strings.Contains(denied.Reason, "re-encoding it does not reproduce it exactly") {
		t.Fatalf("expected a refusal naming the reproduction check, got %v", err)
	}
}

// A rule naming a query id no query write could ever use - a mistyped glob,
// a trailing dot, an invisible character, a Windows 8.3 short name - can
// never apply. Rather than let it silently not apply, the policy refuses
// every project query write and names the rule.
func TestPrepareProjectWrites_RefusesAQueryIDNoWriteCanUse(t *testing.T) {
	refused := map[string]string{
		"/datatug_projects/*/queries/Rev*":             "must not contain any of",
		"/datatug_projects/demo/queries/reports.":      `must not end with "." or a space`,
		"/datatug_projects/*/queries/rev%E2%80%8Cenue": "invisible",
		"/datatug_projects/*/queries/LONGFO~1":         "short name",
		"/datatug_projects/*/queries/reports%2F":       "must not be empty",
		"/datatug_projects/*/queries/CON":              "device name",
		"/datatug_projects/*/queries/%7E":              "queries root",
	}
	for pattern, want := range refused {
		t.Run(pattern, func(t *testing.T) {
			loaded := decodeLoaded(t, "deny.yaml", denyQueryDoc(pattern))
			err := AuthorizeWrite(context.Background(), WriteOptions{Principal: principalWithRoles("alice"), Policies: []Loaded{loaded}},
				access.Set, ProjectQueryResource("demo", "anything"))
			var denied *WriteDeniedError
			if !errors.As(err, &denied) {
				t.Fatalf("expected every project query write to be refused, got %v", err)
			}
			for _, part := range []string{`rule "protect-query"`, "no query write can use", want} {
				if !strings.Contains(denied.Reason, part) {
					t.Errorf("Reason = %q, want it to mention %q", denied.Reason, part)
				}
			}
		})
	}
	// A canonicalizable id, a wildcard and a capture are not refused.
	for _, pattern := range []string{
		"/datatug_projects/*/queries/Revenue",
		"/datatug_projects/*/queries/Reports%2FRevenue",
		"/datatug_projects/*/queries/caf%C3%A9",
		"/datatug_projects/*/queries/*",
		"/datatug_projects/{project}/queries/{query}",
	} {
		loaded := decodeLoaded(t, "deny.yaml", denyQueryDoc(pattern))
		if loaded.writes.refusal != "" {
			t.Errorf("%s: project writes are refused: %s", pattern, loaded.writes.refusal)
		}
	}
}

// flattenPolicy returns the rules DALgo itself compiles for policy: DALgo
// encodes its own rule tree back into a document, and this flattens that
// document the way DALgo joins nested scopes. It shares no code with the
// rewrite it is used to check.
func flattenPolicy(t *testing.T, policy access.Policy) []writeRule {
	t.Helper()
	var encoded bytes.Buffer
	switch p := policy.(type) {
	case *access.AccessPolicy:
		if err := access.EncodeAccessPolicy(&encoded, access.JSONCodec{}, p); err != nil {
			t.Fatalf("encode the compiled policy: %v", err)
		}
	case *access.PrincipalPolicySet:
		if err := access.EncodePrincipalPolicySet(&encoded, access.JSONCodec{}, p); err != nil {
			t.Fatalf("encode the compiled policy set: %v", err)
		}
	default:
		t.Fatalf("unexpected policy type %T", policy)
	}
	var document access.Document
	if err := json.Unmarshal(encoded.Bytes(), &document); err != nil {
		t.Fatalf("decode the encoded policy: %v", err)
	}
	var rules []writeRule
	var walk func(scopes []access.DocumentScope, set string, parent []pathSegment)
	walk = func(scopes []access.DocumentScope, set string, parent []pathSegment) {
		for _, scope := range scopes {
			if scope.Path == "" {
				continue
			}
			joined := testJoinPath(t, parent, scope.Path)
			for _, rule := range scope.Rules {
				rules = append(rules, writeRule{name: qualifiedRuleName(set, rule.ID), path: joined})
			}
			walk(scope.Scopes, set, joined)
		}
	}
	walk(document.Scopes, "", nil)
	setNames := make([]string, 0, len(document.RuleSets))
	for name := range document.RuleSets {
		setNames = append(setNames, name)
	}
	sort.Strings(setNames)
	for _, name := range setNames {
		walk(document.RuleSets[name], name, nil)
	}
	return rules
}

// testJoinPath appends one scope path to parent the way DALgo does: each
// scope's own segments alternate collection, id from its own first one.
func testJoinPath(t *testing.T, parent []pathSegment, path string) []pathSegment {
	t.Helper()
	joined := append([]pathSegment(nil), parent...)
	path = strings.TrimSuffix(strings.TrimSpace(path), "/**")
	if path == "" || path == "/" {
		return joined
	}
	for i, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		value, err := url.PathUnescape(part)
		if err != nil {
			t.Fatalf("path %q: %v", path, err)
		}
		joined = append(joined, pathSegment{value: value, isID: i%2 == 1})
	}
	return joined
}

// canonicalizedRules is what the rewrite must produce from the rules DALgo
// compiles: the same rules, with a literal query id replaced by its
// canonical spelling.
func canonicalizedRules(rules []writeRule) []writeRule {
	out := make([]writeRule, len(rules))
	for i, rule := range rules {
		path := append([]pathSegment(nil), rule.path...)
		if len(path) >= 4 && path[0].value == ProjectsCollection && !path[0].isID &&
			path[1].isID && path[2].value == ProjectQueriesCollection && !path[2].isID &&
			path[3].isID && !isIDWildcard(path[3].value) {
			path[3].value = CanonicalQueryID(path[3].value)
		}
		out[i] = writeRule{name: rule.name, path: path}
	}
	return out
}

func assertSameRules(t *testing.T, what string, got, want []writeRule) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d rules, want %d\n got: %s\nwant: %s", what, len(got), len(want), ruleLines(got), ruleLines(want))
	}
	for i := range want {
		if got[i].name != want[i].name || displayPath(got[i].path, false) != displayPath(want[i].path, false) {
			t.Errorf("%s: rule %d is %s, want %s", what, i, ruleLine(got[i]), ruleLine(want[i]))
		}
	}
}

func ruleLines(rules []writeRule) string {
	lines := make([]string, len(rules))
	for i, rule := range rules {
		lines[i] = ruleLine(rule)
	}
	return strings.Join(lines, ", ")
}

func ruleLine(rule writeRule) string {
	return rule.name + " at " + displayPath(rule.path, false)
}
