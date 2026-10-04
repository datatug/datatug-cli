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

var (
	errUnreadablePostgresURL = errors.New("the PostgreSQL URL cannot be read as host, port, database and user")

	// errSplitPostgresPassword names the shape and the fix and quotes none of the
	// URL: the text after the split is part of the password.
	errSplitPostgresPassword = errors.New("the PostgreSQL URL holds a literal '@' after its host, so it is read differently from how it was written (a password that holds '/', '?' or '#' ends the host early): percent-encode '@', '/', '?' and '#' in the user name and the password, for example '/' as %2F")
)

// atSignAfterAuthority reports whether rawURL holds an "@" after the end of its
// authority, the part up to the first "/", "?" or "#". In a well-formed URL the
// only "@" is the one that ends the userinfo, inside the authority. Another one
// means a password whose "/", "?" or "#" ended the authority early: net/url and
// pgx then read the front of the password as the host and a port, and the rest of
// it as the database, the query or the fragment, which a driver error quotes.
func atSignAfterAuthority(rawURL string) bool {
	rest := rawURL
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+len("://"):]
	}
	end := strings.IndexAny(rest, "/?#")
	return end >= 0 && strings.Contains(rest[end:], "@")
}

// ParsePostgresTarget reads the destination a postgres:// or postgresql:// URL
// states: the host, port, user and database of the URL, each replaced by the
// query parameter of the same name (host, port, user, dbname or database) when
// there is one, as pgx applies them. It reads the URL only: what pgx takes from
// the PG* environment variables or a service file for a part the URL leaves out
// is not here.
//
// It refuses a URL with a literal "@" after the authority (see
// atSignAfterAuthority): such a URL is not read the way it was written, and
// refusing it is the only way to keep the rest of a password out of the host,
// database and scope that are printed or hashed. No error quotes the URL, which
// holds the password.
func ParsePostgresTarget(rawURL string) (PostgresTarget, error) {
	target, _, err := parsePostgresURL(rawURL)
	return target, err
}

// parsePostgresURL is ParsePostgresTarget that also returns the query of the URL.
func parsePostgresURL(rawURL string) (PostgresTarget, url.Values, error) {
	if atSignAfterAuthority(rawURL) {
		return PostgresTarget{}, nil, errSplitPostgresPassword
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return PostgresTarget{}, nil, errUnreadablePostgresURL
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
			return PostgresTarget{}, nil, errUnreadablePostgresURL
		}
	}
	return target, query, nil
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
// connection options, and differs for any other. A part the URL leaves out is
// what libpq's environment variables give it (PGHOST, PGPORT, PGDATABASE,
// PGUSER and PGSERVICE), and the database is the user when nothing names one, as
// pgx resolves them; the contents of a connection service file and the operating
// system's user name are not part of the identity. A variable that cannot be
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
	sum := sha256.Sum256([]byte(strings.Join(destinationParts(ref, lookupEnv), "\x00")))
	return source + "#" + hex.EncodeToString(sum[:12])
}

// destinationParts lists what a resolved env source points at, with no secret.
func destinationParts(ref BackendRef, lookupEnv func(string) (string, bool)) []string {
	if ref.Scheme == "postgres" {
		if target, query, err := parsePostgresURL(ref.Path); err == nil {
			return append([]string{ref.Scheme}, resolvedPostgresParts(target, query, lookupEnv)...)
		}
	}
	// Whatever else the variable holds, bind to its URL with the secrets redacted.
	return []string{ref.Scheme, RedactSourceURL(ref.Path)}
}

// resolvedPostgresParts lists the host, port, database, user and service a
// PostgreSQL URL connects to: the URL's own, or else what libpq's environment
// variable of that part says, or else libpq's default.
func resolvedPostgresParts(target PostgresTarget, query url.Values, lookupEnv func(string) (string, bool)) []string {
	fromEnv := func(name string) string {
		value, _ := lookupEnv(name)
		return strings.TrimSpace(value)
	}
	host := target.Host
	if host == "" {
		host = fromEnv("PGHOST")
	}
	port := target.Port
	if port == 0 {
		if fromEnvPort, err := strconv.Atoi(fromEnv("PGPORT")); err == nil && fromEnvPort > 0 {
			port = fromEnvPort
		} else {
			port = defaultPostgresPort
		}
	}
	user := target.User
	if user == "" {
		user = fromEnv("PGUSER")
	}
	database := target.Database
	if database == "" {
		if database = fromEnv("PGDATABASE"); database == "" {
			database = user
		}
	}
	service := query.Get("service")
	if service == "" {
		service = fromEnv("PGSERVICE")
	}
	return []string{strings.ToLower(host), strconv.Itoa(port), database, user, service}
}
