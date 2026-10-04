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

func TestSourceScopeIdentity_NonEnvSourcesAreUnchanged(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"sqlite:///tmp/shop.db", "ingitdb://./crm", "unavailable://shop", "postgres://alice:xxxxx@h/db", ""} {
		assert.Equal(t, source, sourceScopeIdentity(source, fakeEnv(nil)), source)
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
