package chat

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

// This file keeps the chat store's identity of a source apart from the text it
// stores for it. A chat store stores the display form of a source
// (dbcopy.SourceDisplay): it never holds a password. The display form cuts a path
// at a "?" or a "#" (which Parse keeps in the path of a project directory) and
// drops the user name and the query of a URL, so it is not an identity: a source
// is found by its ID, and the display form is only compared with what is stored.

// previousIdentity is how the store identified a scope before it identified each
// source by where it points, the way main computed it (5778d72): the sources passed
// through dbcopy.RedactSourceURL, then (an env:NAME source only) given the
// destination hash of its variable, hashed with the environment, the database and
// the access fingerprint. It is what an existing chat store holds.
//
// It calls the live RedactSourceURL, which this change altered for two shapes of
// a path-scheme source: an empty user name before the colon, and a wrapped URL
// whose userinfo reads as a host. Parse refuses both now, so no chat opens a
// source of either shape, and a store written under one of them is not looked
// for. Every other shape is held by the goldens in source_identity_test.go.
type previousIdentity struct {
	// scope is the hash a session of that store was stored under.
	scope string
	// selected is the selected source as that store stored it in a saved query.
	selected string
}

func previousScopeIdentity(scope ChatScope) previousIdentity {
	redacted := make(map[string]string, len(scope.Sources))
	identities := make(map[string]string, len(scope.Sources))
	for id, source := range scope.Sources {
		redacted[id] = dbcopy.RedactSourceURL(source)
		identities[id] = previousSourceIdentity(redacted[id])
	}
	encoded, _ := json.Marshal(struct {
		Environment       string
		Database          string
		AccessFingerprint string
		Sources           map[string]string
	}{scope.Environment, scope.Database, scope.AccessFingerprint, identities})
	sum := sha256.Sum256(encoded)
	return previousIdentity{scope: hex.EncodeToString(sum[:]), selected: redacted[scope.Database]}
}

// previousSourceIdentity is what dbcopy.SourceScopeIdentity returned for the
// redacted source when main wrote the store: the source unchanged, except an
// env:NAME source, which carries a hash of where the variable points. The function
// has since been extended to every other source, and a store written before is
// found under what it was.
func previousSourceIdentity(redacted string) string {
	if strings.HasPrefix(redacted, "env:") {
		return dbcopy.SourceScopeIdentity(redacted)
	}
	return redacted
}

// previousScope is the scope hash main wrote for scope.
func previousScope(scope ChatScope) string { return previousScopeIdentity(scope).scope }

// moveChatScope moves everything the store kept under oldScope to newScope, in
// one transaction: the sessions, their bookmarks and the scope's preferences. A
// preference the new scope already has is kept, and the old one is left where it
// was.
func moveChatScope(db *sql.DB, oldScope, newScope string) error {
	if oldScope == newScope {
		return nil
	}
	tx, err := dbBeginFn(db)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		`UPDATE sessions SET scope = ? WHERE scope = ?`,
		`UPDATE bookmarks SET scope = ? WHERE scope = ?`,
		`UPDATE OR IGNORE chat_preferences SET scope = ? WHERE scope = ?`,
	} {
		if _, err := txExecFn(tx, statement, newScope, oldScope); err != nil {
			return fmt.Errorf("move chat scope: %w", err)
		}
	}
	return tx.Commit()
}

// storedSourceNames reports whether stored, the Source of a stored result (a
// source is stored as its display form), names live, a source string of this
// scope: the string itself, or its display form. A source that cannot be shown
// (dbcopy.UnparsableSource) names nothing.
func storedSourceNames(stored, live string) bool {
	if stored == live {
		return true
	}
	shown := dbcopy.SourceDisplay(live)
	return shown != dbcopy.UnparsableSource && stored == shown
}
