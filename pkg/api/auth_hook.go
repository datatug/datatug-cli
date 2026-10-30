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
// A nil token is returned when no principal and no explicit anonymous local
// identity fallback were configured. The fallback preserves legacy local reads
// only; it grants neither project ownership nor database authority.
func AuthTokenFromHTTPRequest(_ *http.Request, _ bool) (*sneatauth.Token, error) {
	secureMu.RLock()
	principal := secureSession.Principal
	anonymousLocalIdentity := secureSession.AllowAnonymousLocalIdentity
	secureMu.RUnlock()

	if principal == nil {
		if anonymousLocalIdentity {
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
