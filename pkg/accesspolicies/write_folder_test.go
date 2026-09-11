package accesspolicies

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/record"
)

// TestAuthorizeWrite_FolderScopedRulesFailClosed: a folder-qualified query
// id is one path segment, so a rule scoped below a query id can never match
// a query - the intuitive folder deny .../queries/reports/** used to fail
// open. Such a policy still loads and serves reads, but every project
// query write is refused under it, naming the rule.
func TestAuthorizeWrite_FolderScopedRulesFailClosed(t *testing.T) {
	const header = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: folder-deny
default: deny
`
	const allowAll = `  - path: /**
    rules:
      - id: all
        effect: allow
        operations: [readwrite]
`
	refused := []struct {
		name, doc, rule string
	}{
		{"a folder deny", header + "scopes:\n" + allowAll + `  - path: /datatug_projects/demo/queries/reports/**
    rules:
      - id: protect-reports-folder
        effect: deny
        operations: [write]
`, `rule "protect-reports-folder"`},
		{"a path below a query id", header + "scopes:\n" + allowAll + `  - path: /datatug_projects/*/queries/reports/revenue
    rules:
      - id: protect-nested
        effect: deny
        operations: [write]
`, `rule "protect-nested"`},
		{"a nested scope below a query id", header + "scopes:\n" + allowAll + `  - path: /datatug_projects/demo/queries/reports
    scopes:
      - path: /versions/*
        rules:
          - id: protect-versions
            effect: deny
            operations: [write]
`, `rule "protect-versions"`},
		{"a rule set", header + `ruleSets:
  admin:
    - path: /**
      rules:
        - id: all
          effect: allow
          operations: [readwrite]
    - path: /datatug_projects/*/queries/Reports/**
      rules:
        - id: protect-reports
          effect: deny
          operations: [delete]
bindings:
  roles:
    admin: [admin]
`, `rule "admin/protect-reports"`},
	}
	for _, tt := range refused {
		t.Run(tt.name, func(t *testing.T) {
			loaded := decodeLoaded(t, "folder.yaml", tt.doc)
			read := loaded.Policy.Decide(access.WithPrincipal(context.Background(), access.Principal{ID: "a", Roles: []string{"admin"}}),
				access.Request{Operation: access.Get, Resources: []access.Resource{access.RecordResourceForKey(record.NewKeyWithID("Customer", 1))}})
			if !read.Allowed {
				t.Errorf("reads must keep working under the policy, got %+v", read)
			}
			for _, id := range []string{"reports/revenue", "reports", "other"} {
				err := AuthorizeWrite(context.Background(), WriteOptions{Principal: principalWithRoles("a", "admin"), Policies: []Loaded{loaded}},
					access.Insert, ProjectQueryResource("demo", id))
				var denied *WriteDeniedError
				if !errors.As(err, &denied) {
					t.Fatalf("write %q: expected every query write to be refused, got %v", id, err)
				}
				for _, want := range []string{tt.rule, "folder-scoped", "%2F"} {
					if !strings.Contains(denied.Reason, want) {
						t.Errorf("write %q: Reason = %q, want it to mention %s", id, denied.Reason, want)
					}
				}
			}
		})
	}
}

// The path shapes that do name queries are not refused: each one's deny
// applies to reports/revenue through the rule itself.
func TestAuthorizeWrite_QueryPathShapesThatWork(t *testing.T) {
	for _, pattern := range []string{
		"/datatug_projects/**",
		"/datatug_projects/demo/queries",
		"/datatug_projects/*/queries/**",
		"/datatug_projects/*/queries/*/**",
		"/datatug_projects/{project}/queries/{query}/**",
		"/datatug_projects/demo/queries/reports%2Frevenue",
	} {
		policies := []Loaded{decodeLoaded(t, "deny.yaml", denyQueryDoc(pattern))}
		err := AuthorizeWrite(context.Background(), WriteOptions{Policies: policies}, access.Update, ProjectQueryResource("demo", "reports/revenue"))
		var denied *WriteDeniedError
		if !errors.As(err, &denied) || !strings.Contains(denied.Reason, "protect-query") || strings.Contains(denied.Reason, "folder-scoped") {
			t.Errorf("deny %s: expected the deny rule itself to refuse reports/revenue, got %v", pattern, err)
		}
	}
}

func TestDisplayPathAndRuleNames(t *testing.T) {
	segments := []pathSegment{{value: ProjectsCollection}, {value: "demo", isID: true}, {value: ProjectQueriesCollection}, {value: "a/b", isID: true}}
	if got := displayPath(segments, true); got != "/datatug_projects/demo/queries/a%2Fb/**" {
		t.Errorf("displayPath = %q", got)
	}
	if got := displayPath(nil, true); got != "/**" {
		t.Errorf("displayPath(root) = %q", got)
	}
	if got := scopeRuleName(nil, nil, "admin"); got != "a scope" {
		t.Errorf("scopeRuleName with no rule = %q", got)
	}
}
