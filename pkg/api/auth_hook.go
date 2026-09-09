package api

import (
	"fmt"
	"net/http"

	"github.com/dal-go/dalgo/access"
	"github.com/sneat-co/sneat-go-core/sneatauth"
)

// AuthTokenFromHTTPRequest implements sneat-go-core/apicore's
// GetAuthTokenFromHttpRequest hook for `datatug serve`
// (REQ:principal-selection). Chosen over routing local-agent endpoints
// around apicore.Execute (option (b) in the brief) because the fix is a
// two-line wire-up at serve startup and every endpoint keeps using the same
// apicore.Execute/VerifyRequest path the rest of the server already relies
// on for its 400/403/500 conventions (util_error_handling.go).
//
// `datatug serve` has no per-request bearer token: the whole process
// authenticates once, at startup, into a fixed secureread.Session via
// --as/--role/--group (see resolveServeSession in
// apps/datatugapp/commands/cmd_serve.go and api.ConfigureSecureSession).
// So this hook does not look at r at all — it reports the session's
// already-resolved principal for every request alike, exactly as if that
// principal had presented a bearer token on each one.
//
// A nil token — which apicore.VerifyRequest already turns into
// facade.ErrUnauthorized (401) whenever the endpoint is AuthRequired,
// without this hook needing to duplicate that check — is returned only when
// the session both names no principal AND is not Unrestricted: a policy set
// was loaded (Session.Policies is non-empty, or would be were policies
// findable) but nobody was identified for it to run as. That is exactly the
// "anonymous request when a policy set exists" case REQ:server-acl-all-reads
// requires refused, never treated as an implicit admin.
//
// A Session with Principal == nil AND Unrestricted == true (no policies were
// found at all, and the caller ran `serve` with none of --as/--role/--group)
// still yields a token: Unrestricted already means every read this process
// serves is unenforced regardless of who asks, so there is no principal left
// to gate on — refusing here would only turn "no policies configured" into a
// different, spurious 401 with no security benefit. resolveServeSession's
// own production path never actually produces this combination (it defaults
// --as to "admin" whenever no principal was named and no policies exist),
// but secureread.NewSession(SessionOptions{NoPolicies: true}) can build one
// directly (as test helpers across this package already do), so this hook
// handles it explicitly rather than assuming that default always ran.
func AuthTokenFromHTTPRequest(_ *http.Request, _ bool) (*sneatauth.Token, error) {
	secureMu.RLock()
	principal := secureSession.Principal
	unrestricted := secureSession.Unrestricted
	secureMu.RUnlock()

	if principal == nil {
		if unrestricted {
			return &sneatauth.Token{}, nil
		}
		return nil, nil
	}
	return &sneatauth.Token{UID: principalUID(principal)}, nil
}

// principalUID stringifies access.Principal.ID, which is typed `any` to
// allow non-string IDs elsewhere in dalgo/access; `datatug serve` only ever
// sets it from the string --as flag (see secureread.NewSession), but this
// stays defensive rather than panicking on an unexpected concrete type.
func principalUID(principal *access.Principal) string {
	if principal.ID == nil {
		return ""
	}
	if id, ok := principal.ID.(string); ok {
		return id
	}
	return fmt.Sprint(principal.ID)
}
