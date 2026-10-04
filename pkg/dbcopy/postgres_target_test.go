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
