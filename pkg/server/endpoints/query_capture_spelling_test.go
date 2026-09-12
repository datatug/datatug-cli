package endpoints

import (
	"net/http"
	"strconv"
	"testing"
)

// captureDenyPolicy allows the admin role everything except writes to the
// resources the path pattern names.
func captureDenyPolicy(pattern string) string {
	return `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: deny-test
default: deny
ruleSets:
  admin:
    - path: /**
      rules:
        - id: admin-full-access
          effect: allow
          operations: [readwrite]
    - path: ` + strconv.Quote(pattern) + `
      rules:
        - id: protect-query
          effect: deny
          operations: [write]
bindings:
  roles:
    admin: [admin]
`
}

// TestCaptureQuery_EquivalentSpellingsAreDenied is the capture route's row
// of the spelling matrix: a deny rule, in any spelling, holds for a create
// (ifNoneMatch) and an update (ifMatch) in any spelling of its query, and
// nothing reaches the store.
func TestCaptureQuery_EquivalentSpellingsAreDenied(t *testing.T) {
	const nfc, nfd = "café", "café"
	families := []struct {
		name     string
		patterns []string
		requests []string
		status   int
	}{
		{"case", []string{"revenue", "Revenue", "REVENUE"}, []string{"revenue", "Revenue", "REVENUE", "rEvEnUe"}, http.StatusForbidden},
		// Capture ids are ASCII only, so a non-ASCII spelling is refused as
		// an invalid location before authorization - still nothing written.
		{"normalization", []string{nfc, nfd, "CAFÉ"}, []string{nfc, nfd, "CAFÉ", "CAFÉ"}, http.StatusBadRequest},
	}
	for _, family := range families {
		for pi, pattern := range family.patterns {
			t.Run(family.name+"-pattern"+string(rune('A'+pi)), func(t *testing.T) {
				scope, fake := captureTestSetupWithPolicies(t, "admin", []string{"admin"}, true,
					map[string]string{"deny.yaml": captureDenyPolicy("/datatug_projects/*/queries/" + pattern)})
				for _, id := range family.requests {
					for _, ifMatch := range []string{"", "rev-1"} {
						req := validCaptureRequest(scope)
						req.Query.FolderPath, req.Query.ID = "", id
						if ifMatch != "" {
							req.IfNoneMatch, req.IfMatch = false, ifMatch
						}
						if w := postCapture(t, req); w.Code != family.status {
							t.Errorf("deny %q, capture %q (ifMatch %q): status %d, want %d; body %s", pattern, id, ifMatch, w.Code, family.status, w.Body.String())
						}
					}
				}
				if len(fake.puts) != 0 {
					t.Errorf("a refused capture reached the store %d times", len(fake.puts))
				}
				control := validCaptureRequest(scope)
				control.Query.FolderPath, control.Query.ID = "", "expenses"
				if w := postCapture(t, control); w.Code != http.StatusCreated {
					t.Errorf("an unrelated query must stay writable: status %d; body %s", w.Code, w.Body.String())
				}
			})
		}
	}
}
