package dbcopy

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParsePostgresTarget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		url  string
		want PostgresTarget
	}{
		{"full URL", "postgres://alice:s3cret@db.example.com:5433/shop", PostgresTarget{"db.example.com", 5433, "shop", "alice"}},
		{"postgresql alias without port", "postgresql://alice:s3cret@db.example.com/shop", PostgresTarget{"db.example.com", 0, "shop", "alice"}},
		{"no user", "postgres://db.example.com/shop", PostgresTarget{"db.example.com", 0, "shop", ""}},
		{"IPv6 host", "postgres://alice:s3cret@[::1]:5433/shop", PostgresTarget{"::1", 5433, "shop", "alice"}},
		{"password with escaped delimiters", "postgres://alice:p%40ss%2Fw%3Ard@h/shop", PostgresTarget{"h", 0, "shop", "alice"}},
		{"query parameters override the destination, as pgx applies them", "postgres://alice:s3cret@h/shop?host=other&port=6543&dbname=reports&user=bob&sslmode=require", PostgresTarget{"other", 6543, "reports", "bob"}},
		{"database query parameter", "postgres://alice:s3cret@h/shop?database=reports", PostgresTarget{"h", 0, "reports", "alice"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParsePostgresTarget(tc.url)
			assert.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParsePostgresTarget_RefusesWhatItCannotRead(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"postgres://alice:s3cret@h:notaport/shop",
		"postgres://alice:s3cret@h/shop?port=notaport",
		"postgres://alice:s3cret@h/%zz",
	} {
		got, err := ParsePostgresTarget(raw)
		assert.Equal(t, PostgresTarget{}, got, raw)
		if assert.Error(t, err, raw) {
			assert.NotContains(t, err.Error(), "s3cret", raw)
			assert.Contains(t, err.Error(), "host, port, database and user", raw)
		}
	}
}

// A source whose display form is the whole of it is its own identity, so a store
// that holds such a source keeps its scope.
func TestSourceScopeIdentity_ASourceWithNothingToHideIsItsOwnIdentity(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"sqlite:///tmp/shop.db", "ingitdb://./crm", "ingitdb:///tmp/a b/crm", "http://./proj", "openvaultdb:///tmp/c.json", "unavailable://shop", ""} {
		assert.Equal(t, source, sourceScopeIdentity(source, fakeEnv(nil)), source)
	}
	assert.Equal(t, "sqlite:///tmp/shop.db", sourceScopeIdentity("SQLITE:///tmp/shop.db", fakeEnv(nil)), "the scheme is read case-insensitively")
}

// A literal PostgreSQL URL is identified like an env source: by where it points
// and as whom (host, port, database and user), never by its password. Two roles on
// one host and database are two scopes, and the identity holds neither the user
// name nor the password.
func TestSourceScopeIdentity_ALiteralPostgresURLFollowsItsDestinationAndNeverItsPassword(t *testing.T) {
	t.Parallel()
	identity := func(url string, env map[string]string) string { return sourceScopeIdentity(url, fakeEnv(env)) }
	base := identity("postgres://reader:s3cret@db.example.com:5432/shop", nil)
	assert.True(t, strings.HasPrefix(base, "postgres#"), base)
	assert.NotContains(t, base, "db.example.com", "the identity is a digest of the destination")
	for _, secret := range []string{"reader", "s3cret"} {
		assert.NotContains(t, base, secret)
	}

	// The same destination is the same scope, whatever the password, the connection options or the spelling.
	assert.Equal(t, base, identity("postgres://reader:rotated@db.example.com:5432/shop", nil))
	assert.Equal(t, base, identity("postgresql://reader:s3cret@db.example.com:5432/shop?sslmode=require", nil))
	assert.Equal(t, base, identity("POSTGRES://reader:s3cret@DB.Example.COM/shop", nil), "host case and the default port do not make a new scope")

	// Another role, host, port or database is another scope.
	for name, other := range map[string]string{
		"user":     "postgres://admin:s3cret@db.example.com:5432/shop",
		"host":     "postgres://reader:s3cret@staging.example.com:5432/shop",
		"port":     "postgres://reader:s3cret@db.example.com:5433/shop",
		"database": "postgres://reader:s3cret@db.example.com:5432/warehouse",
	} {
		assert.NotEqual(t, base, identity(other, nil), name)
	}

	// What the URL leaves out is what libpq's environment says, as for an env source.
	assert.NotEqual(t,
		identity("postgres://db.example.com/shop", map[string]string{"PGUSER": "reader"}),
		identity("postgres://db.example.com/shop", map[string]string{"PGUSER": "admin"}))

	// A URL that cannot be read (a password that splits it) still binds to where it
	// points, and shows no part of the password.
	split := identity("postgres://alice:42/TOPSECRET@db.example.com/shop", nil)
	assert.NotContains(t, split, "TOPSECRET")
	assert.NotContains(t, split, "alice")
	assert.Equal(t, split, identity("postgres://alice:42/OTHERSECRET@db.example.com/shop", nil), "the split password is not part of the identity")
}

// A file or a directory is identified by its whole path, with its "#" and "?"
// (which the display form cuts off), so two directories whose names differ after
// a "#" are two scopes. The identity text itself shows only the display form and
// a digest of the whole path.
func TestSourceScopeIdentity_ALocalPathEntersWhole(t *testing.T) {
	t.Parallel()
	identity := func(source string) string { return sourceScopeIdentity(source, fakeEnv(nil)) }
	first, second := identity("ingitdb:///tmp/a#b/data/ingitdb"), identity("ingitdb:///tmp/a#c/data/ingitdb")
	assert.NotEqual(t, first, second)
	assert.True(t, strings.HasPrefix(first, "ingitdb:///tmp/a#"), first)
	assert.NotContains(t, first, "data/ingitdb", "the text shows the display form and a digest, not the whole path")
	assert.Equal(t, first, identity("INGITDB:///tmp/a#b/data/ingitdb"))

	assert.NotEqual(t, identity("ingitdb:///tmp/a?x=1"), identity("ingitdb:///tmp/a?x=2"), "a query is part of a directory name")
	assert.NotEqual(t, identity("http:///tmp/a#b"), identity("http:///tmp/a#c"))

	// The query of a sqlite URL holds driver options, which can include a key: it is
	// not part of the identity, and neither is anything else that is not the file.
	assert.Equal(t, identity("sqlite:///private.db"), identity("sqlite:///private.db?_pragma_key=s3cret"))
	assert.NotContains(t, identity("sqlite:///private.db?_pragma_key=s3cret"), "s3cret")
	assert.Equal(t, identity("sqlite:///private.db"), identity("sqlite:///private.db#frag"))
	assert.NotEqual(t, identity("sqlite:///private.db"), identity("sqlite:///other.db"))
	assert.NotEqual(t, "sqlite:file.db", identity("sqlite:file.db"), "the no-slash form is not the display form")
}

// A literal source that Parse refuses can hold credentials: its identity is the
// display form, which holds none.
func TestSourceScopeIdentity_ARefusedLiteralSourceIsItsDisplayForm(t *testing.T) {
	t.Parallel()
	for source, want := range map[string]string{
		"http://alice:s3cretpw@host/x":           "http://host/x",
		"ingitdb://s3cretpw@github.com/org/repo": "ingitdb://github.com/org/repo",
		"mongodb://alice:s3cretpw@host/db":       UnparsableSource,
		"alice:s3cretpw@host/shop":               UnparsableSource,
	} {
		assert.Equal(t, want, sourceScopeIdentity(source, fakeEnv(nil)), source)
	}
}

func TestSourceScopeIdentity_BindsEnvNameToTheDatabaseItPointsAt(t *testing.T) {
	t.Parallel()
	identity := func(url string) string {
		return sourceScopeIdentity("env:SHOP_PG_URL", fakeEnv(map[string]string{"SHOP_PG_URL": url}))
	}
	base := identity("postgres://alice:s3cret@db.example.com:5432/shop")
	assert.True(t, strings.HasPrefix(base, "env:SHOP_PG_URL#"), base)
	assert.NotContains(t, base, "db.example.com")
	assert.NotContains(t, base, "alice")
	assert.NotContains(t, base, "s3cret")

	// The same destination is the same scope, however the password or the
	// connection options change.
	assert.Equal(t, base, identity("postgres://alice:rotated@db.example.com:5432/shop"))
	assert.Equal(t, base, identity("postgresql://alice:s3cret@db.example.com:5432/shop?sslmode=require"))
	assert.Equal(t, base, identity("postgres://alice:s3cret@DB.Example.COM/shop"), "host case and the default port do not make a new scope")
	assert.Equal(t, base, identity("postgres://alice:s3cret@other.example.net/shop?host=db.example.com"))

	// Repointing the variable at another database is another scope.
	for name, repointed := range map[string]string{
		"host":     "postgres://alice:s3cret@staging.example.com:5432/shop",
		"port":     "postgres://alice:s3cret@db.example.com:5433/shop",
		"database": "postgres://alice:s3cret@db.example.com:5432/warehouse",
		"user":     "postgres://bob:s3cret@db.example.com:5432/shop",
	} {
		other := identity(repointed)
		assert.NotEqual(t, base, other, name)
		assert.True(t, strings.HasPrefix(other, "env:SHOP_PG_URL#"), name)
	}
}

func TestSourceScopeIdentity_OtherSchemesFollowTheirPath(t *testing.T) {
	t.Parallel()
	identity := func(url string) string {
		return sourceScopeIdentity("env:SHOP_URL", fakeEnv(map[string]string{"SHOP_URL": url}))
	}
	assert.Equal(t, identity("sqlite:///tmp/a.db"), identity("sqlite:///tmp/a.db"))
	assert.NotEqual(t, identity("sqlite:///tmp/a.db"), identity("sqlite:///tmp/b.db"))
	assert.NotEqual(t, identity("sqlite:///tmp/a.db"), identity("ingitdb:///tmp/a.db"))
}

func TestSourceScopeIdentity_AnUnreadablePostgresURLStillBindsToItsRedactedForm(t *testing.T) {
	t.Parallel()
	identity := func(url string) string {
		return sourceScopeIdentity("env:SHOP_PG_URL", fakeEnv(map[string]string{"SHOP_PG_URL": url}))
	}
	// pgx would refuse this port, but the URL is shaped like a PostgreSQL URL.
	base := identity("postgres://alice:s3cret@h/shop?port=notaport")
	assert.True(t, strings.HasPrefix(base, "env:SHOP_PG_URL#"), base)
	assert.NotEqual(t, "env:SHOP_PG_URL#unresolved", base)
	assert.NotContains(t, base, "s3cret")
	assert.Equal(t, base, identity("postgres://alice:rotated@h/shop?port=notaport"))
	assert.NotEqual(t, base, identity("postgres://alice:s3cret@other/shop?port=notaport"))
}

func TestSourceScopeIdentity_AVariableThatDoesNotResolveIsItsOwnScope(t *testing.T) {
	t.Parallel()
	unresolved := "env:SHOP_PG_URL#unresolved"
	assert.Equal(t, unresolved, sourceScopeIdentity("env:SHOP_PG_URL", fakeEnv(nil)))
	assert.Equal(t, unresolved, sourceScopeIdentity("env:SHOP_PG_URL", fakeEnv(map[string]string{"SHOP_PG_URL": ""})))
	assert.Equal(t, unresolved, sourceScopeIdentity("env:SHOP_PG_URL", fakeEnv(map[string]string{"SHOP_PG_URL": "mongodb://alice:s3cret@h/db"})))
	assert.Equal(t, "env:lower#unresolved", sourceScopeIdentity("env:lower", fakeEnv(nil)))
}

func TestSourceScopeIdentity_ReadsTheProcessEnvironment(t *testing.T) {
	t.Setenv("DT03_SCOPE_PG_URL", "postgres://alice:s3cret@h/shop")
	first := SourceScopeIdentity("env:DT03_SCOPE_PG_URL")
	t.Setenv("DT03_SCOPE_PG_URL", "postgres://alice:s3cret@h/warehouse")
	assert.NotEqual(t, first, SourceScopeIdentity("env:DT03_SCOPE_PG_URL"))
}

// splitPasswordURLs are PostgreSQL URLs whose password holds an unescaped "/",
// "?" or "#" right after digits. net/url and pgx end the authority there and
// read "alice:42" as host "alice" and port 42, so the rest of the password lands
// in the database name, the query or the fragment, and a driver error that
// quotes the database prints it. The marker SECRET stands for the part of the
// password that must never be printed.
var splitPasswordURLs = map[string]string{
	"slash after digits":         "postgres://alice:42/SECRET@db.example.com/shop",
	"question mark after digits": "postgres://alice:42?SECRET@db.example.com/shop",
	"hash after digits":          "postgres://alice:42#SECRET@db.example.com/shop",
	"postgresql alias":           "postgresql://alice:42/SECRET@db.example.com:5433/shop?sslmode=require",
}

// atSignAfterAuthorityURLs are the URLs ParsePostgresTarget refuses: the split
// passwords above, and a well-formed URL with a literal "@" in its path or query.
var atSignAfterAuthorityURLs = func() map[string]string {
	urls := map[string]string{
		"an at sign in the path":  "postgres://alice:pw@db.example.com/shop@SECRET",
		"an at sign in the query": "postgres://alice:pw@db.example.com/shop?application_name=SECRET@x",
	}
	for name, raw := range splitPasswordURLs {
		urls[name] = raw
	}
	return urls
}()

func TestParsePostgresTarget_RefusesAnAtSignAfterTheAuthority(t *testing.T) {
	t.Parallel()
	for name, raw := range atSignAfterAuthorityURLs {
		got, err := ParsePostgresTarget(raw)
		assert.Equal(t, PostgresTarget{}, got, name)
		if assert.Error(t, err, name) {
			for _, quoted := range []string{"SECRET", "alice", "db.example.com", "shop", "42"} {
				assert.NotContains(t, err.Error(), quoted, name+": the error quotes nothing of the URL")
			}
			assert.Contains(t, err.Error(), "percent-encode", name)
		}
	}
}

func TestParsePostgresTarget_AnAtSignInsideTheUserinfoIsStillAPassword(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]PostgresTarget{
		"postgres://alice:p@ss@h/shop":           {"h", 0, "shop", "alice"},
		"postgres://alice:p%40ss@h:5433/shop":    {"h", 5433, "shop", "alice"},
		"postgres://alice:p%2Fss%3Fx%23y@h/shop": {"h", 0, "shop", "alice"},
		"postgres://h/shop?sslmode=require":      {"h", 0, "shop", ""},
	} {
		got, err := ParsePostgresTarget(raw)
		assert.NoError(t, err, raw)
		assert.Equal(t, want, got, raw)
	}
}

func TestSourceScopeIdentity_ASplitPasswordNeverReachesTheIdentity(t *testing.T) {
	t.Parallel()
	for name, raw := range splitPasswordURLs {
		identity := func(secret string) string {
			url := strings.ReplaceAll(raw, "SECRET", secret)
			return sourceScopeIdentity("env:SHOP_PG_URL", fakeEnv(map[string]string{"SHOP_PG_URL": url}))
		}
		first := identity("FIRST")
		assert.True(t, strings.HasPrefix(first, "env:SHOP_PG_URL#"), name)
		assert.NotEqual(t, "env:SHOP_PG_URL#unresolved", first, name)
		assert.Equal(t, first, identity("SECOND"), name+": the part of the password after the split is not an input of the identity")
	}
}

// libpq, and so pgx, takes a part the URL leaves out from an environment
// variable. A variable repointed through them must not keep the scope.
func TestSourceScopeIdentity_APartLeftOutOfTheURLIsWhatTheEnvironmentSays(t *testing.T) {
	t.Parallel()
	identity := func(url string, env map[string]string) string {
		values := map[string]string{"SHOP_PG_URL": url}
		for name, value := range env {
			values[name] = value
		}
		return sourceScopeIdentity("env:SHOP_PG_URL", fakeEnv(values))
	}
	const full = "postgres://alice:s3cret@db.example.com:5432/shop"

	// A part the URL names is not taken from the environment.
	base := identity(full, nil)
	assert.Equal(t, base, identity(full, map[string]string{"PGHOST": "elsewhere", "PGPORT": "6543", "PGDATABASE": "warehouse", "PGUSER": "bob", "PGPASSWORD": "other"}))

	// The port: PGPORT, else the default; a value that is not a port is no port.
	noPort := "postgres://alice:s3cret@db.example.com/shop"
	assert.Equal(t, base, identity(noPort, nil))
	assert.Equal(t, base, identity(noPort, map[string]string{"PGPORT": "5432"}))
	assert.Equal(t, base, identity(noPort, map[string]string{"PGPORT": "not-a-port"}))
	assert.NotEqual(t, base, identity(noPort, map[string]string{"PGPORT": "6543"}))

	// The host.
	noHost := "postgres://alice:s3cret@/shop"
	assert.NotEqual(t, identity(noHost, nil), identity(noHost, map[string]string{"PGHOST": "db.example.com"}))
	assert.Equal(t, base, identity(noHost, map[string]string{"PGHOST": " DB.example.com "}), "the host the variable names is the host, whatever its case")

	// The user.
	noUser := "postgres://:s3cret@db.example.com/shop"
	assert.NotEqual(t, identity(noUser, nil), identity(noUser, map[string]string{"PGUSER": "alice"}))
	assert.Equal(t, base, identity(noUser, map[string]string{"PGUSER": "alice"}))

	// The database: PGDATABASE, else the user name.
	noDatabase := "postgres://alice:s3cret@db.example.com"
	assert.Equal(t, identity("postgres://alice:s3cret@db.example.com/alice", nil), identity(noDatabase, nil), "an unnamed database is the user's own")
	assert.NotEqual(t, identity(noDatabase, nil), identity(noDatabase, map[string]string{"PGDATABASE": "shop"}))
	assert.Equal(t, base, identity(noDatabase, map[string]string{"PGDATABASE": "shop"}))

	// The service: the URL's, else PGSERVICE. Its file is not read.
	assert.NotEqual(t, base, identity(full+"?service=prod", nil))
	assert.NotEqual(t, identity(full+"?service=prod", nil), identity(full+"?service=staging", nil))
	assert.NotEqual(t, base, identity(full, map[string]string{"PGSERVICE": "prod"}))
	assert.Equal(t, identity(full+"?service=prod", nil), identity(full, map[string]string{"PGSERVICE": "prod"}))
	assert.Equal(t, identity(full+"?service=prod", nil), identity(full+"?service=prod", map[string]string{"PGSERVICE": "staging"}), "the URL's service wins")
}

// colonInUserURLs are PostgreSQL URLs whose user name holds a colon. A generic
// URL encoder turns "alice:SECRET" into "alice%3ASECRET", and net/url reads a
// userinfo with no literal colon as a user name and no password: the password
// becomes the user, which a scan logs ("user=...") and pgx prints in its errors,
// and which no redactor knows as a secret. The marker SECRET stands for the part
// that must never be printed.
var colonInUserURLs = map[string]string{
	"an upper-case escape":              "postgres://alice%3ASECRET@db.example.com/shop",
	"a lower-case escape":               "postgres://alice%3aSECRET@db.example.com/shop",
	"the postgresql alias, port, query": "postgresql://alice%3ASECRET@db.example.com:5433/shop?sslmode=require",
	"an escape and a literal colon":     "postgres://alice%3ASECRET:pw@db.example.com/shop",
	"a colon in the user query":         "postgres://db.example.com/shop?user=alice:SECRET",
}

func TestParsePostgresTarget_RefusesAUserNameThatHoldsAColon(t *testing.T) {
	t.Parallel()
	for name, raw := range colonInUserURLs {
		got, err := ParsePostgresTarget(raw)
		assert.Equal(t, PostgresTarget{}, got, name)
		if assert.Error(t, err, name) {
			for _, quoted := range []string{"SECRET", "alice", "db.example.com", "shop", "pw"} {
				assert.NotContains(t, err.Error(), quoted, name+": the error quotes nothing of the URL")
			}
			assert.Contains(t, err.Error(), "literal colon", name)
		}
	}
}

func TestParsePostgresTarget_AColonInThePasswordIsStillAPassword(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]PostgresTarget{
		"postgres://alice:pa:ss@h/shop":        {"h", 0, "shop", "alice"},
		"postgres://alice:pa%3Ass@h:5433/shop": {"h", 5433, "shop", "alice"},
		"postgres://alice:@h/shop":             {"h", 0, "shop", "alice"},
	} {
		got, err := ParsePostgresTarget(raw)
		assert.NoError(t, err, raw)
		assert.Equal(t, want, got, raw)
	}
}

func TestSourceScopeIdentity_AColonInTheUserNeverReachesTheIdentity(t *testing.T) {
	t.Parallel()
	for name, raw := range colonInUserURLs {
		if strings.Contains(raw, "?user=") {
			continue // the user is a query parameter here: not a userinfo to mask
		}
		identity := func(url, secret string) string {
			url = strings.ReplaceAll(url, "SECRET", secret)
			return sourceScopeIdentity("env:SHOP_PG_URL", fakeEnv(map[string]string{"SHOP_PG_URL": url}))
		}
		first := identity(raw, "FIRST")
		assert.True(t, strings.HasPrefix(first, "env:SHOP_PG_URL#"), name)
		assert.NotEqual(t, "env:SHOP_PG_URL#unresolved", first, name)
		assert.Equal(t, first, identity(raw, "SECOND"), name+": a password rotation is not a new scope")
		assert.NotEqual(t, first, identity(strings.ReplaceAll(raw, "db.example.com", "other.example.com"), "FIRST"), name+": another host is another scope")
	}
}

func TestMaskPostgresUserinfo(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]string{
		"postgres://alice:pw@h/shop":       "postgres://xxxxx@h/shop",
		"postgres://alice%3Apw@h:5433/db":  "postgres://xxxxx@h:5433/db",
		"postgres://alice:p@ss@h/shop":     "postgres://xxxxx@h/shop",
		"postgres://alice:42/pw@h/shop":    "postgres://xxxxx@h/shop",
		"postgres://h/shop":                "postgres://h/shop",
		"postgres://h/shop?sslmode=prefer": "postgres://h/shop?sslmode=prefer",
		"not a url, alice:pw@h":            "not a url, alice:pw@h",
	} {
		assert.Equal(t, want, maskPostgresUserinfo(raw), raw)
	}
}
