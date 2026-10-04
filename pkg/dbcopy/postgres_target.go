package dbcopy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// PostgresTarget is the part of a PostgreSQL connection URL that says where it
// points. It never holds the password.
type PostgresTarget struct {
	Host string
	// Port is 0 when the URL names none.
	Port     int
	Database string
	User     string
}

// defaultPostgresPort is the port a PostgreSQL URL without one connects to.
const defaultPostgresPort = 5432

var errUnreadablePostgresURL = errors.New("the PostgreSQL URL cannot be read as host, port, database and user")

// ParsePostgresTarget reads the destination of a postgres:// or postgresql://
// URL the way pgx connects to it: the host, port, user and database of the URL,
// each replaced by the query parameter of the same name (host, port, user,
// dbname or database) when there is one. No error quotes the URL, which holds
// the password.
func ParsePostgresTarget(rawURL string) (PostgresTarget, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return PostgresTarget{}, errUnreadablePostgresURL
	}
	target := PostgresTarget{
		Host:     parsed.Hostname(),
		Database: strings.TrimPrefix(parsed.Path, "/"),
		User:     parsed.User.Username(),
	}
	query := parsed.Query()
	for _, override := range []struct {
		key  string
		into *string
	}{{"host", &target.Host}, {"user", &target.User}, {"dbname", &target.Database}, {"database", &target.Database}} {
		if value := query.Get(override.key); value != "" {
			*override.into = value
		}
	}
	port := parsed.Port()
	if value := query.Get("port"); value != "" {
		port = value
	}
	if port != "" {
		if target.Port, err = strconv.Atoi(port); err != nil {
			return PostgresTarget{}, errUnreadablePostgresURL
		}
	}
	return target, nil
}

// SourceScopeIdentity returns the text that stands for source in a persisted
// scope identity, such as the key of a chat session store.
//
// An "env:NAME" source names a variable, and the variable can be repointed at
// another database while its name stays: under the name alone, record sets read
// from the old database would stay reachable in the new one. So the identity of
// an env source is the name plus a hash of where the variable points now: for a
// PostgreSQL URL its host, port, database and user, never the password. The
// hash is the same for the same destination, whatever the password or the
// connection options, and differs for any other. A variable that cannot be
// resolved is its own scope. Every other source is returned unchanged.
func SourceScopeIdentity(source string) string {
	return sourceScopeIdentity(source, os.LookupEnv)
}

func sourceScopeIdentity(source string, lookupEnv func(string) (string, bool)) string {
	if !strings.HasPrefix(source, envPrefix) {
		return source
	}
	ref, err := parseSource(source, lookupEnv)
	if err != nil {
		return source + "#unresolved"
	}
	sum := sha256.Sum256([]byte(strings.Join(destinationParts(ref), "\x00")))
	return source + "#" + hex.EncodeToString(sum[:12])
}

// destinationParts lists what a resolved env source points at, with no secret.
func destinationParts(ref BackendRef) []string {
	if ref.Scheme == "postgres" {
		if target, err := ParsePostgresTarget(ref.Path); err == nil {
			port := target.Port
			if port == 0 {
				port = defaultPostgresPort
			}
			return []string{ref.Scheme, strings.ToLower(target.Host), strconv.Itoa(port), target.Database, target.User}
		}
	}
	// Whatever else the variable holds, bind to its URL with the secrets redacted.
	return []string{ref.Scheme, RedactSourceURL(ref.Path)}
}
