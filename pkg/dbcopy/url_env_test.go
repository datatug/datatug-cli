package dbcopy

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// fakeEnv returns a lookup function over a fixed map, so these tests never
// touch the process environment and can run in parallel.
func fakeEnv(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func TestParse_Env_ResolvesSupportedURLs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		value      string
		wantScheme string
		wantPath   string
	}{
		{"postgres", "postgres://alice:s3cret@db.example.com:5432/shop", "postgres", "postgres://alice:s3cret@db.example.com:5432/shop"},
		{"postgresql alias", "postgresql://alice:s3cret@db.example.com/shop", "postgres", "postgresql://alice:s3cret@db.example.com/shop"},
		{"sqlite", "sqlite:///tmp/shop.db", "sqlite", "/tmp/shop.db"},
		{"ingitdb", "ingitdb://./crm", "ingitdb", "./crm"},
		{"openvaultdb", "openvaultdb:///etc/ovdb.json", "openvaultdb", "/etc/ovdb.json"},
		{"http project", "http://./demo-project", "http", "./demo-project"},
		{"surrounding whitespace is trimmed", "  postgres://alice:s3cret@h/db\n", "postgres", "postgres://alice:s3cret@h/db"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ref, err := parseSource("env:SHOP_PG_URL", fakeEnv(map[string]string{"SHOP_PG_URL": tc.value}))
			assert.NoError(t, err)
			assert.Equal(t, tc.wantScheme, ref.Scheme)
			assert.Equal(t, tc.wantPath, ref.Path)
			assert.Equal(t, "env:SHOP_PG_URL", ref.Raw, "Raw must name the variable, never hold its value")
			assert.NotContains(t, ref.String(), "s3cret")
		})
	}
}

func TestParse_Env_ReadsTheProcessEnvironment(t *testing.T) {
	t.Setenv("DT01_TEST_PG_URL", "postgres://alice:s3cret@h/shop")
	ref, err := Parse("env:DT01_TEST_PG_URL")
	assert.NoError(t, err)
	assert.Equal(t, "postgres", ref.Scheme)
	assert.Equal(t, "postgres://alice:s3cret@h/shop", ref.Path)
	assert.Equal(t, "env:DT01_TEST_PG_URL", ref.Raw)
}

func TestValidEnvName(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"A", "SHOP_PG_URL", "A1", "A_1_B", "PG2"} {
		assert.True(t, ValidEnvName(name), name)
	}
	for _, name := range []string{"", "a", "shop_pg_url", "1ABC", "_ABC", "A-B", "A B", "A=B", "A\x00B", "ÄB", "A\n", "postgres://u:p@h/db"} {
		assert.False(t, ValidEnvName(name), "%q", name)
	}
}

func TestParse_Env_RejectsBadNamesWithoutEchoingInput(t *testing.T) {
	t.Parallel()
	inputs := []string{
		"env:",
		"env:shop_pg_url",
		"env:1SHOP",
		"env:SHOP-PG",
		"env:SHOP PG",
		"env:SHOP\x00PG",
		"env:postgres://alice:s3cret@h/db", // a URL typed after env: by mistake
		"env:host=db password=s3cret",
	}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			_, err := parseSource(input, fakeEnv(map[string]string{"SHOP_PG_URL": "postgres://alice:s3cret@h/db"}))
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "^[A-Z][A-Z0-9_]*$")
			assert.NotContains(t, err.Error(), "s3cret")
			assert.NotContains(t, err.Error(), "alice")
		})
	}
}

func TestParse_Env_ErrorsNameTheVariableAndNeverEchoTheValue(t *testing.T) {
	t.Parallel()
	const secretURL = "mysql://alice:s3cret@db.example.com/shop"
	tests := []struct {
		name    string
		env     map[string]string
		wantMsg string
	}{
		{"unset", map[string]string{}, "environment variable SHOP_PG_URL is not set"},
		{"empty", map[string]string{"SHOP_PG_URL": ""}, "environment variable SHOP_PG_URL is empty"},
		{"blank", map[string]string{"SHOP_PG_URL": " \t\n"}, "environment variable SHOP_PG_URL is empty"},
		{"nested reference", map[string]string{"SHOP_PG_URL": "env:OTHER_URL"}, "must hold a URL, not another env: reference"},
		{"unsupported scheme", map[string]string{"SHOP_PG_URL": secretURL}, "does not hold a supported source URL"},
		{"not a URL at all", map[string]string{"SHOP_PG_URL": "alice s3cret"}, "does not hold a supported source URL"},
		{"malformed postgres", map[string]string{"SHOP_PG_URL": "postgres://alice:s3cret@h:notaport/db"}, "does not hold a supported source URL"},
		{"empty ingitdb", map[string]string{"SHOP_PG_URL": "ingitdb://"}, "does not hold a supported source URL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ref, err := parseSource("env:SHOP_PG_URL", fakeEnv(tc.env))
			assert.Error(t, err)
			assert.Equal(t, BackendRef{}, ref)
			assert.Contains(t, err.Error(), tc.wantMsg)
			assert.Contains(t, err.Error(), "SHOP_PG_URL")
			for _, secret := range []string{"s3cret", "alice", "db.example.com", "mysql://", "notaport"} {
				assert.NotContains(t, err.Error(), secret)
			}
		})
	}
}

func TestParse_Env_UnsupportedValueErrorListsSchemes(t *testing.T) {
	t.Parallel()
	_, err := parseSource("env:SHOP_PG_URL", fakeEnv(map[string]string{"SHOP_PG_URL": "mysql://u:p@h/db"}))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), strings.Join(supportedSchemes, ", "))
}

func TestParse_LiteralCredentialsNeverEchoedByErrors(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"mysql://alice:s3cret@db.example.com/shop",      // unsupported scheme
		"postgres://alice:s3cret@db.example.com:bad/db", // malformed postgres URL
		"postgres://alice:s3%zzcret@db.example.com/db",  // bad percent-escape
	} {
		_, err := Parse(input)
		assert.Error(t, err, input)
		assert.NotContains(t, err.Error(), "s3cret", input)
		assert.NotContains(t, err.Error(), "s3%zzcret", input)
		assert.NotContains(t, err.Error(), "%zz", input)
	}
}

func TestParse_LiteralPostgresRawIsRedacted(t *testing.T) {
	t.Parallel()
	ref, err := Parse("postgres://alice:s3cret@db.example.com:5432/shop?sslmode=require&password=other")
	assert.NoError(t, err)
	assert.Equal(t, "postgres", ref.Scheme)
	assert.Equal(t, "postgres://alice:xxxxx@db.example.com:5432/shop?sslmode=require&password=xxxxx", ref.Raw)
	assert.Contains(t, ref.Path, "s3cret", "Path stays the real URL: Open needs it")
}

func TestParse_EveryNonEnvSchemeKeepsRawVerbatim(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"sqlite:///tmp/foo.db", "sqlite://./rel/foo.db", "ingitdb://./project", "http://./demo-project-1", "openvaultdb:///tmp/c.json"} {
		ref, err := Parse(input)
		assert.NoError(t, err)
		assert.Equal(t, input, ref.Raw)
	}
}

func TestBackendRef_StringFallsBackToPath(t *testing.T) {
	t.Parallel()
	ref := BackendRef{Scheme: "postgres", Path: "postgres://alice:s3cret@h/db"}
	assert.Equal(t, "postgres://alice:xxxxx@h/db", ref.String())
	assert.Equal(t, "postgres://alice:xxxxx@h/db", fmt.Sprintf("%v", ref))
}

func TestParse_InputThatIsNotAURLIsNeverEchoed(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"alice:s3cret@db.example.com/shop", "s3cret", "host=db password=s3cret", ""} {
		_, err := Parse(input)
		assert.Error(t, err, input)
		assert.NotContains(t, err.Error(), "s3cret", input)
		assert.Contains(t, err.Error(), "env:NAME", input)
	}
}

func TestParse_UnknownSchemeErrorStillNamesSchemeAndShowsRedactedURL(t *testing.T) {
	t.Parallel()
	_, err := Parse("mysql://alice:s3cret@db.example.com/shop")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), `unsupported scheme "mysql"`)
	assert.Contains(t, err.Error(), "mysql://alice:xxxxx@db.example.com/shop")
	assert.Contains(t, err.Error(), "env:NAME")
}

func TestParse_HTTPCredentialsAreRefusedWithoutEcho(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"https://alice:s3cret@api.example.com/x", "http://alice:s3cret@host:8080", "https://s3cret@host/x"} {
		_, err := Parse(input)
		assert.Error(t, err, input)
		assert.Contains(t, err.Error(), "credentials are not supported")
		assert.NotContains(t, err.Error(), "s3cret")
	}
	// A project directory that merely contains an "@" later in its path is fine.
	ref, err := Parse("https:///Users/alex/team@work/project")
	assert.NoError(t, err)
	assert.Equal(t, "/Users/alex/team@work/project", ref.Path)
}

func TestRedactError(t *testing.T) {
	t.Parallel()
	assert.NoError(t, RedactError(nil))
	original := fmt.Errorf("dial postgres://alice:s3cret@h/db: %w", ErrPostgresNotWired)
	redacted := RedactError(original)
	assert.NotContains(t, redacted.Error(), "s3cret")
	assert.Contains(t, redacted.Error(), "postgres://alice:xxxxx@h/db")
	assert.ErrorIs(t, redacted, ErrPostgresNotWired, "the wrapped chain must survive redaction")
	assert.ErrorIs(t, fmt.Errorf("open: %w", redacted), ErrPostgresNotWired)
}
