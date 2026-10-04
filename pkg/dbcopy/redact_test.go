package dbcopy

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRedactSourceURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"password in userinfo", "postgres://u:s3cret@h:5432/db?sslmode=require", "postgres://u:xxxxx@h:5432/db?sslmode=require"},
		{"postgresql alias", "postgresql://u:s3cret@h/db", "postgresql://u:xxxxx@h/db"},
		{"user without password is kept", "postgres://u@h/db", "postgres://u@h/db"},
		{"no userinfo", "postgres://h:5432/db", "postgres://h:5432/db"},
		{"at sign inside the password", "postgres://u:p@ss@h/db", "postgres://u:xxxxx@h/db"},
		{"slash inside the password", "postgres://u:p/ss@h/db", "postgres://u:xxxxx@h/db"},
		{"question mark inside the password", "postgres://u:pa?ss@h/db", "postgres://u:xxxxx@h/db"},
		{"password query parameter", "postgres://h/db?password=s3cret&sslmode=require", "postgres://h/db?password=xxxxx&sslmode=require"},
		{"mixed case query key", "postgres://h/db?PassWord=s3cret", "postgres://h/db?PassWord=xxxxx"},
		{"escaped query key", "postgres://h/db?pass%77ord=s3cret", "postgres://h/db?pass%77ord=xxxxx"},
		{"invalid escape in query key", "postgres://h/db?%zzpass=s3cret&a=b", "postgres://h/db?%zzpass=xxxxx&a=b"},
		// A "#" may be part of an unquoted password, so the keyword pass takes the rest of the value; a database URL has no use for a fragment.
		{"sslpassword with fragment", "postgres://h/db?sslpassword=s3cret#frag", "postgres://h/db?sslpassword=xxxxx"},
		{"fragment after a harmless parameter", "postgres://h/db?sslmode=require#frag", "postgres://h/db?sslmode=require#frag"},
		{"query pair without value", "postgres://h/db?password&a=b", "postgres://h/db?password&a=b"},
		{"harmless query", "postgres://h/db?sslmode=require&connect_timeout=5", "postgres://h/db?sslmode=require&connect_timeout=5"},
		{"unknown scheme with credentials", "mysql://u:s3cret@h/db", "mysql://u:xxxxx@h/db"},
		{"http scheme with credentials", "https://u:s3cret@host/x", "https://u:xxxxx@host/x"},
		{"sqlite path is never userinfo", "sqlite:///tmp/a@b:c.db", "sqlite:///tmp/a@b:c.db"},
		{"ingitdb path is never userinfo", "ingitdb://./a@b:c", "ingitdb://./a@b:c"},
		{"openvaultdb path is never userinfo", "openvaultdb:///a@b:c.json", "openvaultdb:///a@b:c.json"},
		{"sqlite key parameter", "sqlite:///x.db?_pragma_key=s3cret&mode=ro", "sqlite:///x.db?_pragma_key=xxxxx&mode=ro"},
		{"env reference", "env:SHOP_PG_URL", "env:SHOP_PG_URL"},
		{"ingitdb wrapping a URL with a password", "ingitdb://https://alice:s3cret@github.com/org/repo", "ingitdb://https://xxxxx@github.com/org/repo"},
		{"ingitdb wrapping a URL whose token is the user name before a colon", "ingitdb://https://TOKEN:x-oauth-basic@github.com/org/repo", "ingitdb://https://xxxxx@github.com/org/repo"},
		{"ingitdb path shaped like userinfo", "ingitdb://alice:s3cret@github.com/org/repo", "ingitdb://alice:xxxxx@github.com/org/repo"},
		{"sqlite path shaped like userinfo", "sqlite://alice:s3cret@host/x.db", "sqlite://alice:xxxxx@host/x.db"},
		{"windows drive path is a path", `ingitdb://C:\work\a@b`, `ingitdb://C:\work\a@b`},
		{"windows drive path under http is a path", `http://C:\work\a@b`, `http://C:\work\a@b`},
		{"windows drive path under https is a path", `https://C:/work/a@b/proj`, `https://C:/work/a@b/proj`},
		{"path that holds an at sign after a port", "http://localhost:8080/users/@me", "http://localhost:8080/users/@me"},
		{"scoped package path", "https://registry.example.com/@scope/pkg", "https://registry.example.com/@scope/pkg"},
		// A database URL has no host-only exemption: its userinfo ends at the last "@", so an unescaped "/" in a password cannot hide it behind a port-like prefix.
		{"postgres host with an at sign in the query is over-redacted", "postgres://h:5432/db?email=a@b.example", "postgres://h:xxxxx@b.example"},
		{"postgres password that starts with digits and holds a slash", "postgres://alice:42/s3cret@db.example.com/shop", "postgres://alice:xxxxx@db.example.com/shop"},
		{"postgres password that is all digits before a slash", "postgresql://alice:5432/s3cret@db.example.com/shop", "postgresql://alice:xxxxx@db.example.com/shop"},
		{"ipv6 host and path that holds an at sign", "http://[::1]:8080/users/@me", "http://[::1]:8080/users/@me"},
		{"ipv6 host without a port and path that holds an at sign", "https://[2001:db8::1]/users/@me", "https://[2001:db8::1]/users/@me"},
		{"http host with an at sign in the query", "http://localhost:8080?email=a@b.com", "http://localhost:8080?email=a@b.com"},
		{"http host with an at sign in the fragment", "http://localhost:8080#a@b.com", "http://localhost:8080#a@b.com"},
		{"http host and path with an at sign in the query", "http://localhost:8080/x?email=a@b.com", "http://localhost:8080/x?email=a@b.com"},
		{"http credentials still hidden next to a port-like password", "https://alice:s3cret@host:8080/x", "https://alice:xxxxx@host:8080/x"},
		{"ingitdb wrapping a URL with a token as the user name", "ingitdb://https://s3cret@github.com/org/repo", "ingitdb://https://xxxxx@github.com/org/repo"},
		// Finding 1 of the review of #320: inside a wrapped URL the userinfo is masked whole, even when it reads as host or host:port.
		{"wrapped URL whose password starts with digits and a slash", "ingitdb://https://alice:42/s3cret@github.com/org/repo", "ingitdb://https://xxxxx@github.com/org/repo"},
		{"wrapped URL whose token is a user name with digits and a slash", "ingitdb://https://s3cret:42/x@github.com/org/repo", "ingitdb://https://xxxxx@github.com/org/repo"},
		{"wrapped URL whose token holds a slash and no colon", "ingitdb://https://s3/cret@github.com/org/repo", "ingitdb://https://xxxxx@github.com/org/repo"},
		{"wrapped URL with an empty user name", "ingitdb://https://:s3/cret@github.com/org/repo", "ingitdb://https://xxxxx@github.com/org/repo"},
		// Finding 3: an empty user name is userinfo too.
		{"path scheme with an empty user name and a slash in the password", "ingitdb://:s3/cret@github.com/org/repo", "ingitdb://:xxxxx@github.com/org/repo"},
		// Finding 4: the drive-path exemption is scoped to http and https; it must not hide a PostgreSQL password that starts with a slash.
		{"one-letter postgres user with a password that starts with a slash", "postgres://C:/s3cret@db.example.com/shop", "postgres://C:xxxxx@db.example.com/shop"},
		{"ingitdb wrapping a URL with no userinfo", "ingitdb://https://github.com/org/repo", "ingitdb://https://github.com/org/repo"},
		{"password made of a port-like number is still userinfo", "postgres://u:123@h/db", "postgres://u:xxxxx@h/db"},
		{"keyword DSN", "host=db user=u password=s3cret dbname=x", "host=db user=u password=xxxxx dbname=x"},
		{"keyword DSN with spaces around equals", "host=db PASSWORD = s3cret dbname=x", "host=db PASSWORD = xxxxx dbname=x"},
		{"keyword DSN quoted value", `password='it\'s a secret' host=x`, `password=xxxxx host=x`},
		{"keyword DSN sslpassword", "sslpassword=s3cret", "sslpassword=xxxxx"},
		{"PGPASSWORD", "PGPASSWORD=s3cret psql", "PGPASSWORD=xxxxx psql"},
		{"prefixed name", "db_password=s3cret host=x", "db_password=xxxxx host=x"},
		{"double-quoted value with a space", `host=x password="it is s3cret" dbname=y`, `host=x password=xxxxx dbname=y`},
		{"json member", `{"user":"u","password":"s3cret"}`, `{"user":"u","password":"xxxxx"}`},
		{"json member with spaces and an escaped quote", `{"db_password" : "s3\"cret"}`, `{"db_password" : "xxxxx"}`},
		{"json member holding a number", `{"password":12345}`, `{"password":"xxxxx"}`},
		{"colon form", "Password:s3cret", "Password:xxxxx"},
		{"colon form is prose when a space follows", "Password: required", "Password: required"},
		{"word that only ends in pwd is still a keyword", "cwd=/tmp and pwd=/home", "cwd=/tmp and pwd=xxxxx"},
		{"plain text", "just some words", "just some words"},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, RedactSourceURL(tc.in))
			assert.NotContains(t, RedactSourceURL(tc.in), "s3cret")
		})
	}
}

func TestRedactText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"quoted URL in an error", `open "postgres://u:s3cret@h/db": dial tcp: refused`, `open "postgres://u:xxxxx@h/db": dial tcp: refused`},
		{"two URLs", "from postgres://a:s3cret@h/x to mysql://b:s3cret@h/y done", "from postgres://a:xxxxx@h/x to mysql://b:xxxxx@h/y done"},
		{"keyword in prose", "failed with password=s3cret and more", "failed with password=xxxxx and more"},
		{"url-escaped quote form", `parse "postgres://u:s3\"cret@h/db": bad`, `parse "postgres://u:xxxxx@h/db": bad`},
		{"nothing to redact", "sqlite:///tmp/x.db is missing", "sqlite:///tmp/x.db is missing"},
		{"quoted URL whose password holds a space", `parse "postgres://alice:p ass s3cret@h/db": bad`, `parse "postgres://alice:xxxxx@h/db": bad`},
		{"single-quoted URL whose password holds a space", `parse 'postgres://alice:p ass s3cret@h/db': bad`, `parse 'postgres://alice:xxxxx@h/db': bad`},
		{"ingitdb wrapping a URL", `ingitdb URL "ingitdb://https://alice:s3cret@github.com/org/repo" looks remote`, `ingitdb URL "ingitdb://https://xxxxx@github.com/org/repo" looks remote`},
		{"harmless URL with an at sign in the path", "GET http://localhost:8080/users/@me failed", "GET http://localhost:8080/users/@me failed"},
		{"harmless URL with an ipv6 host and an at sign in the path", "GET http://[::1]:8080/users/@me failed", "GET http://[::1]:8080/users/@me failed"},
		{"postgres URL whose password starts with digits and holds a slash", "dial postgres://alice:42/s3cret@db.example.com/shop failed", "dial postgres://alice:xxxxx@db.example.com/shop failed"},
		{"harmless prose with an address", "mail bob@example.com about http://localhost:8080 today", "mail bob@example.com about http://localhost:8080 today"},
		{"PGPASSWORD in an environment dump", "PGPASSWORD=s3cret PGUSER=u", "PGPASSWORD=xxxxx PGUSER=u"},
		{"json body", `response {"password":"s3cret"} rejected`, `response {"password":"xxxxx"} rejected`},
		{"prose that names the word", "the password must not be empty", "the password must not be empty"},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, RedactText(tc.in))
			assert.NotContains(t, RedactText(tc.in), "s3cret")
		})
	}
}

func TestBackendRef_FormattingNeverPrintsTheSecret(t *testing.T) {
	t.Parallel()
	ref, err := Parse("postgres://alice:s3cret@db.example.com:5432/shop")
	assert.NoError(t, err)
	assert.Equal(t, "postgres://alice:s3cret@db.example.com:5432/shop", ref.Path, "Open needs the real URL")
	for _, rendered := range []string{
		fmt.Sprint(ref), fmt.Sprintf("%v", ref), fmt.Sprintf("%+v", ref), fmt.Sprintf("%#v", ref), fmt.Sprintf("%v", &ref),
		ref.String(), ref.GoString(),
	} {
		assert.NotContains(t, rendered, "s3cret")
		assert.Contains(t, rendered, "postgres://db.example.com:5432/shop")
	}
}

func TestRedactError_KeepsAnErrorThatHoldsNoSecret(t *testing.T) {
	t.Parallel()
	original := fmt.Errorf("open sqlite: %w", ErrSourceFileMissing)
	assert.Same(t, original, RedactError(original), "nothing to hide: the error itself comes back")
	assert.NoError(t, RedactErrorWithSecrets(nil, "postgres://u:p@h/db"))
	assert.NoError(t, RedactErrorWithLiterals(nil, "p"))
}

func TestSourceSecrets(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want []string
		// subset: want is a subset of the secrets, in any order and spelling count.
		subset bool
	}{
		{"password", "postgres://alice:s3cret@h/db", []string{"s3cret"}, false},
		{"percent-encoded password lists both spellings", "postgres://alice:p%40ss%2Fw@h/db", []string{"p%40ss%2Fw", "p@ss/w"}, true},
		{"query parameters", "postgres://h/db?sslmode=require&password=s3cret&api_key=k%20k#frag", []string{"s3cret", "k%20k", "k k", "k+k"}, true},
		{"keyword", "host=db password='it is s3cret'", []string{"it is s3cret", "it+is+s3cret", "it%20is%20s3cret"}, true},
		{"user without password", "postgres://alice@h/db", nil, false},
		{"no secret", "postgres://h:5432/db?sslmode=require", nil, false},
		{"http host and port only, at sign in the path", "http://h:5432/a@b", nil, false},
		{"postgres host and port only, at sign in the path reads as userinfo", "postgres://h:5432/a@b", []string{"5432/a"}, true},
		{"sqlite path is never userinfo", "sqlite://a:b@c.db", nil, false},
		{"bare path with colon and at sign", "/tmp/x:y@z.db", nil, false},
		{"bare project path with a secret-named parameter", "api.example.com/v1?api_key=K", []string{"K"}, false},
		{"empty password", "postgres://alice:@h/db", nil, false},
		{"password that starts with digits and holds a slash", "postgres://alice:42/s3cret@h/db", []string{"42/s3cret", "42%2Fs3cret"}, true},
		{"http host and path with an at sign", "http://localhost:8080/users/@me", nil, false},
		{"http ipv6 host and path with an at sign", "http://[::1]:8080/users/@me", nil, false},
		{"http host with an at sign in the query", "http://localhost:8080?email=a@b.com", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := sourceSecrets(tc.in)
			if tc.subset {
				for _, want := range tc.want {
					assert.Contains(t, got, want)
				}
				return
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRedactErrorWithSecrets_RemovesTheLiteralPasswordInAnyShape(t *testing.T) {
	t.Parallel()
	const source = "postgres://alice:p%40ss%2Fw0rd@db.example.com:5432/shop?password=q%3Dz"
	for _, message := range []string{
		`dial "postgres://alice:p%40ss%2Fw0rd@db.example.com:5432/shop": refused`,
		"failed to connect to `host=db.example.com user=alice password=p@ss/w0rd`: refused",
		"pq: password authentication failed for user alice (p@ss/w0rd)",
		"decoded q=z and encoded q%3Dz and plus q%3Dz",
		"pgx: dsn p%40ss%2Fw0rd",
	} {
		err := RedactErrorWithSecrets(fmt.Errorf("open: %w", ErrPostgresNotWired), source) // chain check below
		assert.ErrorIs(t, err, ErrPostgresNotWired)

		redacted := RedactErrorWithSecrets(errors.New(message), source)
		for _, secret := range []string{"p%40ss%2Fw0rd", "p@ss/w0rd", "w0rd", "q%3Dz", "q=z"} {
			assert.NotContains(t, redacted.Error(), secret, message)
		}
	}
	original := fmt.Errorf("dial p@ss/w0rd: %w", ErrPostgresNotWired)
	redacted := RedactErrorWithSecrets(original, source)
	assert.ErrorIs(t, redacted, ErrPostgresNotWired, "the chain survives")
	assert.Equal(t, "dial xxxxx: PostgreSQL backend not yet wired", redacted.Error())
	// A source that holds no secret leaves the error alone.
	plain := errors.New("boom")
	assert.Same(t, plain, RedactErrorWithSecrets(plain, "sqlite:///tmp/x.db"))
}

func TestRedactErrorWithLiteralsAndTextWithSecrets(t *testing.T) {
	t.Parallel()
	err := RedactErrorWithLiterals(errors.New("login failed for p ss;w0rd on h"), "p ss;w0rd")
	assert.Equal(t, "login failed for xxxxx on h", err.Error())
	assert.Equal(t, "server=h;password=xxxxx;database=d", RedactTextWithSecrets("server=h;password=p ss;w0rd;database=d", "p ss;w0rd"))
	assert.Equal(t, "nothing here", RedactTextWithSecrets("nothing here", ""))
	assert.Equal(t, "nothing here", RedactTextWithSecrets("nothing here"))
}
