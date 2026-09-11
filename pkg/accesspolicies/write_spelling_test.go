package accesspolicies

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/access"
)

// denyQueryDoc allows everything and denies writes to the resources the
// path pattern names.
func denyQueryDoc(pattern string) string {
	return `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: deny-test
default: deny
scopes:
  - path: /**
    rules:
      - id: allow-all
        effect: allow
        operations: [readwrite]
  - path: ` + strconv.Quote(pattern) + `
    rules:
      - id: protect-query
        effect: deny
        operations: [write]
`
}

var writeOperations = []access.Operations{access.Insert, access.Set, access.Update, access.Delete}

// TestAuthorizeWrite_EquivalentSpellingsShareOneDecision is the matrix of
// deny-rule spellings against request spellings: whatever spelling the
// policy author used and whatever spelling a request uses, one query gets
// one decision.
func TestAuthorizeWrite_EquivalentSpellingsShareOneDecision(t *testing.T) {
	const nfc, nfd = "café", "café"
	families := []struct {
		name     string
		patterns []string
		requests []string
	}{
		{"case", []string{"revenue", "Revenue", "REVENUE"}, []string{"revenue", "Revenue", "REVENUE", "rEvEnUe"}},
		{"normalization and case", []string{nfc, nfd, "CAFÉ"}, []string{nfc, nfd, "CAFÉ", "CAFÉ"}},
		{"folder-qualified", []string{"reports%2Frevenue", "Reports%2FRevenue", "REPORTS%2FREVENUE"}, []string{"reports/revenue", "REPORTS/Revenue"}},
	}
	for _, family := range families {
		for _, pattern := range family.patterns {
			policies := []Loaded{decodeLoaded(t, "deny.yaml", denyQueryDoc("/datatug_projects/*/queries/"+pattern))}
			for _, id := range family.requests {
				for _, op := range writeOperations {
					err := AuthorizeWrite(context.Background(), WriteOptions{Principal: principalWithRoles("alice"), Policies: policies},
						op, ProjectQueryResource("demo", id))
					var denied *WriteDeniedError
					if !errors.As(err, &denied) || !errors.Is(err, access.ErrAccessDenied) {
						t.Errorf("%s: deny %q, %s %q: expected an access denial, got %v", family.name, pattern, op, id, err)
						continue
					}
					if !strings.Contains(denied.Reason, "protect-query") {
						t.Errorf("%s: deny %q, %s %q: Reason = %q, want the deny rule", family.name, pattern, op, id, denied.Reason)
					}
				}
			}
			if err := AuthorizeWrite(context.Background(), WriteOptions{Principal: principalWithRoles("alice"), Policies: policies},
				access.Insert, ProjectQueryResource("demo", "expenses")); err != nil {
				t.Errorf("%s: deny %q: an unrelated query must stay writable, got %v", family.name, pattern, err)
			}
		}
	}
}

// A literal grant is canonicalized the same way as a deny: it covers every
// spelling of its query, and only that query.
func TestAuthorizeWrite_LiteralGrantCoversEverySpelling(t *testing.T) {
	const grant = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: one-query
default: deny
scopes:
  - path: /datatug_projects/demo/queries/Reports%2FRevenue
    rules:
      - id: curate-revenue
        effect: allow
        operations: [write]
`
	policies := []Loaded{decodeLoaded(t, "grant.yaml", grant)}
	for _, id := range []string{"reports/revenue", "REPORTS/REVENUE", "Reports/Revenue"} {
		if err := AuthorizeWrite(context.Background(), WriteOptions{Policies: policies}, access.Update, ProjectQueryResource("demo", id)); err != nil {
			t.Errorf("update %q: expected the grant to cover it, got %v", id, err)
		}
	}
	for _, id := range []string{"reports/other", "revenue", "other/revenue"} {
		if err := AuthorizeWrite(context.Background(), WriteOptions{Policies: policies}, access.Update, ProjectQueryResource("demo", id)); err == nil {
			t.Errorf("update %q: the grant names another query and must not cover it", id)
		}
	}
}

// Every nesting depth and every rule set is rewritten.
func TestAuthorizeWrite_CanonicalizesNestedScopesAndRuleSets(t *testing.T) {
	const doc = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: nested
default: deny
ruleSets:
  editors:
    - path: /**
      rules:
        - id: allow-all
          effect: allow
          operations: [readwrite]
    - path: /datatug_projects/{project}
      scopes:
        - path: /queries/Revenue
          rules:
            - id: protect-revenue
              effect: deny
              operations: [write]
bindings:
  roles:
    editor: [editors]
`
	policies := []Loaded{decodeLoaded(t, "nested.yaml", doc)}
	err := AuthorizeWrite(context.Background(), WriteOptions{Principal: principalWithRoles("ed", "editor"), Policies: policies},
		access.Delete, ProjectQueryResource("demo", "REVENUE"))
	if !errors.Is(err, access.ErrAccessDenied) {
		t.Fatalf("expected the nested deny to apply, got %v", err)
	}
}

func TestDecodeLoaded_JSONDocument(t *testing.T) {
	const doc = `{"apiVersion":"dalgo.io/access/v1","kind":"AccessPolicy","metadata":{"name":"deny-json"},"default":"deny",
"scopes":[{"path":"/**","rules":[{"id":"all","effect":"allow","operations":["readwrite"]}]},
{"path":"/datatug_projects/*/queries/Revenue","rules":[{"id":"protect","effect":"deny","operations":["write"]}]}]}`
	loaded, err := DecodeLoaded([]byte(doc), access.JSONCodec{}, "/srv/p.json")
	if err != nil {
		t.Fatal(err)
	}
	err = AuthorizeWrite(context.Background(), WriteOptions{Policies: []Loaded{loaded}}, access.Set, ProjectQueryResource("demo", "revenue"))
	if !errors.Is(err, access.ErrAccessDenied) {
		t.Fatalf("expected the JSON deny to apply to another spelling, got %v", err)
	}
}

func TestDecodeLoaded_RejectsAnInvalidDocument(t *testing.T) {
	if _, err := DecodeLoaded([]byte("kind: nothing\n"), access.YAMLCodec{}, "x.yaml"); err == nil {
		t.Fatal("expected an invalid document to be rejected")
	}
}

func TestAuthorizeWrite_RefusesAPolicyWithoutItsDocument(t *testing.T) {
	policy, err := access.DecodePolicy(strings.NewReader(denyQueryDoc("/datatug_projects/*/queries/other")), access.YAMLCodec{})
	if err != nil {
		t.Fatal(err)
	}
	err = AuthorizeWrite(context.Background(), WriteOptions{Policies: []Loaded{{Policy: policy}}}, access.Insert, ProjectQueryResource("p", "q"))
	var denied *WriteDeniedError
	if !errors.As(err, &denied) || !strings.Contains(denied.Reason, "not loaded from its document") {
		t.Fatalf("expected a refusal for a policy with no document, got %v", err)
	}
	if err := AuthorizeWrite(context.Background(), WriteOptions{Policies: []Loaded{{}}}, access.Insert, ProjectQueryResource("p", "q")); err == nil {
		t.Fatal("expected an empty Loaded to be refused")
	}
}

// A scope tree that cannot be rewritten in place fails closed.
func TestAuthorizeWrite_RefusesWhatItCannotCanonicalize(t *testing.T) {
	const aliased = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: aliased
default: deny
scopes:
  - path: &protected /datatug_projects/*/queries/Revenue
    rules:
      - id: protect
        effect: deny
        operations: [write]
  - path: *protected
    rules:
      - id: protect-again
        effect: deny
        operations: [delete]
  - path: /**
    rules:
      - id: all
        effect: allow
        operations: [readwrite]
`
	policies := []Loaded{decodeLoaded(t, "aliased.yaml", aliased)}
	err := AuthorizeWrite(context.Background(), WriteOptions{Policies: policies}, access.Insert, ProjectQueryResource("p", "other"))
	var denied *WriteDeniedError
	if !errors.As(err, &denied) || !strings.Contains(denied.Reason, "alias") {
		t.Fatalf("expected a refusal naming the alias, got %v", err)
	}
	for _, data := range []string{"{", "- [unbalanced"} {
		if w := prepareProjectWrites([]byte(data)); w.refusal == "" {
			t.Errorf("prepareProjectWrites(%q) must refuse", data)
		}
	}
}
