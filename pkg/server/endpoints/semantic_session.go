package endpoints

import (
	"errors"

	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

// semanticSessionFromQuery builds a secureread.Session for one request from
// query parameters mirroring `datatug query run`'s own flags (as/role/group/
// policiesDir/policy/noPolicies — see apps/datatugapp/commands/cmd_query.go).
//
// `datatug serve --as/--role/--group` are parsed by cmd_serve.go but, as its
// own comment records, "not yet enforced" (task 6 wires them into a
// persistent session) — there is no serve-wide Session this handler can
// reach yet. Building one per request from the SAME query the caller is
// already making is a deliberate, narrowly-scoped stand-in: it reuses
// secureread.NewSession exactly as designed (a cheap, stateless
// constructor), needs no change to cmd_serve.go/http_server.go, and every
// test in this package can exercise both a full-access and a restricted
// principal without a global server-session concept existing yet. See the
// PR body for why this, rather than waiting on task 6.
func semanticSessionFromQuery(q queryValues) (secureread.Session, error) {
	o := secureread.SessionOptions{
		As:          q.Get("as"),
		Roles:       q["role"],
		Groups:      q["group"],
		PoliciesDir: q.Get("policiesDir"),
		PolicyFiles: q["policy"],
		NoPolicies:  q.Get("noPolicies") == "true",
	}
	session, err := secureread.NewSession(o)
	if err != nil {
		switch {
		case errors.Is(err, secureread.ErrNoPrincipal):
			return secureread.Session{}, newFieldError("as", "a principal is required: pass as=, role= or group=, or noPolicies=true to run unrestricted")
		case errors.Is(err, accesspolicies.ErrNoPolicies):
			return secureread.Session{}, newFieldError("policiesDir", "no access policies found: pass policiesDir=/policy=, or noPolicies=true to run unrestricted")
		default:
			return secureread.Session{}, err
		}
	}
	return session, nil
}
