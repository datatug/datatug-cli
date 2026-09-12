package endpoints

import (
	"net/http"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/apicontract"
)

// TestCaptureQuery_AccessDeniedNamesNoPolicyOrResource: the 403 a refused
// capture answers carries a fixed message. It used to append the denial's
// own text, which names the policy that refused the write, the rule and the
// role that decided it, and the resource path of the query - telling a
// client that may not write queries which policy stands in its way, and
// that the query it named exists. The detail belongs in the agent log,
// under the request ID the body carries.
func TestCaptureQuery_AccessDeniedNamesNoPolicyOrResource(t *testing.T) {
	scope, fake := captureTestSetupWithPolicies(t, "admin", []string{"admin"}, true,
		map[string]string{"deny.yaml": captureDenyPolicy("/datatug_projects/*/queries/*")})
	w := postCapture(t, validCaptureRequest(scope))
	env := assertCaptureError(t, w, fake, http.StatusForbidden, apicontract.ErrCodeAccessDenied, "")
	if env.Error.Message != "the serving principal may not save project queries" {
		t.Errorf("message = %q, want the fixed message", env.Error.Message)
	}
	if env.Error.RequestID == "" {
		t.Error("the body carries no request ID to find the denial in the log by")
	}
	for _, leak := range []string{"datatug_projects", "protect-query", "deny-test", "policy", "rule", "role", "dalgo"} {
		if strings.Contains(w.Body.String(), leak) {
			t.Errorf("body %q leaks %q", w.Body.String(), leak)
		}
	}
}
