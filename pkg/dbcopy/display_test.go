package dbcopy

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSourceDisplay(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		// env: names a variable, never its value.
		{"env reference", "env:SHOP_PG_URL", "env:SHOP_PG_URL"},
		{"env reference with a lower-case name", "env:shop", UnparsableSource},
		{"env reference with no name", "env:", UnparsableSource},
		{"env reference that holds a URL", "env:postgres://alice:s3cret@h/db", UnparsableSource},

		// A database URL: scheme, host, port and a one-segment path. Never userinfo, query or fragment.
		{"full postgres URL", "postgres://alice:s3cret@db.example.com:5432/shop?sslmode=require#frag", "postgres://db.example.com:5432/shop"},
		{"upper-case scheme", "POSTGRES://alice:s3cret@Db.Example.com/shop", "postgres://Db.Example.com/shop"},
		{"mixed-case scheme and the postgresql alias", "Postgresql://u:p@h/db", "postgresql://h/db"},
		{"user without a password", "postgres://u@h/db", "postgres://h/db"},
		{"no userinfo", "postgres://h:5432/db", "postgres://h:5432/db"},
		{"at sign in the password", "postgres://u:p@ss@h/db", "postgres://h/db"},
		{"slash in the password", "postgres://u:p/ss@h/db", "postgres://h/db"},
		{"password that starts with digits and holds a slash", "postgres://alice:42/s3cret@db.example.com/shop", "postgres://db.example.com/shop"},
		{"password that is all digits before a slash", "postgresql://alice:5432/s3cret@db.example.com/shop", "postgresql://db.example.com/shop"},
		{"empty user name", "postgres://:s3cret@h/db", "postgres://h/db"},
		{"one-letter user and a password that starts with a slash", "postgres://C:/s3cret@db.example.com/shop", "postgres://db.example.com/shop"},
		{"token as the user name", "postgres://s3cret@h/db", "postgres://h/db"},
		{"password query parameter", "postgres://h/db?password=s3cret&sslmode=require", "postgres://h/db"},
		{"password in the fragment", "postgres://h/db#s3cret", "postgres://h/db"},
		{"no host", "postgres://alice:s3cret@/db", "postgres:///db"},
		{"ipv6 host", "postgres://[::1]:5432/db", "postgres://[::1]:5432/db"},
		{"ipv6 host without a port", "postgres://[2001:db8::1]/db", "postgres://[2001:db8::1]/db"},
		{"unclosed ipv6 host", "postgres://[::1/db", "postgres:///db"},
		{"text after an ipv6 host that is not a port", "postgres://[::1]x/db", "postgres:///db"},
		{"port that is not a number", "postgres://h:notaport/db", "postgres://h/db"},
		{"port out of range", "postgres://h:99999/db", "postgres://h/db"},
		{"list of hosts", "postgres://h1:1,h2:2/db", "postgres:///db"},
		{"path with more than one segment", "postgres://h/db/extra", "postgres://h"},
		{"path with a character outside the allowed set", "postgres://h/d b", "postgres://h"},
		{"path that is only a slash", "postgres://h/", "postgres://h/"},
		{"host with a character outside the allowed set", "postgres://h!/db", "postgres:///db"},
		{"no path", "postgres://h", "postgres://h"},
		// An "@" after a "?" or "#" cannot be told from the end of the userinfo, so nothing after the scheme is shown.
		{"at sign after a question mark", "postgres://u:pa?ss@h/db", "postgres://"},
		{"at sign in the query of a URL without userinfo", "postgres://h/db?email=a@b.example", "postgres://"},
		{"at sign after a hash", "postgres://h/db#a@b", "postgres://"},
		// Other schemes the viewer command accepts through dburl.
		{"mysql through dburl", "mysql://alice:s3cret@db.example.com:3306/shop?password=x", "mysql://db.example.com:3306/shop"},
		{"mysql alias through dburl", "MariaDB://alice:s3cret@db.example.com/shop", "mariadb://db.example.com/shop"},

		// A local path: scheme and path as they are, minus query and fragment.
		{"sqlite absolute path", "sqlite:///tmp/foo.db", "sqlite:///tmp/foo.db"},
		{"sqlite relative path", "sqlite://./rel/foo.db", "sqlite://./rel/foo.db"},
		{"sqlite upper-case scheme", "SQLITE:///tmp/x.db", "sqlite:///tmp/x.db"},
		{"sqlite without slashes", "sqlite:foo.db", "sqlite:foo.db"},
		{"sqlite in memory", "sqlite://:memory:", "sqlite://:memory:"},
		{"sqlite key in the query", "sqlite:///x.db?_pragma_key=s3cret&mode=ro", "sqlite:///x.db"},
		{"sqlite path with a space", "sqlite:///tmp/my data/x.db", "sqlite:///tmp/my data/x.db"},
		{"sqlite path shaped like userinfo", "sqlite://alice:s3cret@host/x.db", "sqlite://host/x.db"},
		{"sqlite token as the user name", "sqlite://s3cret@host/x.db", "sqlite://host/x.db"},
		{"ingitdb relative project", "ingitdb://./project", "ingitdb://./project"},
		{"ingitdb path shaped like userinfo", "ingitdb://alice:s3cret@github.com/org/repo", "ingitdb://github.com/org/repo"},
		{"ingitdb token as the user name", "ingitdb://s3cret@github.com/org/repo", "ingitdb://github.com/org/repo"},
		{"ingitdb empty user name and a slash in the password", "ingitdb://:s3/cret@github.com/org/repo", "ingitdb://github.com/org/repo"},
		{"ingitdb with nothing after the scheme", "ingitdb://", "ingitdb://"},
		{"openvaultdb path", "openvaultdb:///tmp/c.json", "openvaultdb:///tmp/c.json"},
		{"internal unavailable source", "unavailable://Chinook%20db", "unavailable://Chinook%20db"},
		{"path that holds an at sign in a later segment", "https:///Users/alex/team@work/project", "https:///Users/alex/team@work/project"},
		// What stands in front of the last "@" is userinfo unless the text is certainly a path (it starts with "/", "./",
		// "../", "~/", "\\" or a drive letter): a user name or a token may hold a slash.
		{"ingitdb token that holds a slash", "ingitdb://ab/cdefghij@github.com/org/repo", "ingitdb://github.com/org/repo"},
		{"http user name that holds a slash", "http://team/alice:s3cret@host/x", "http://host/x"},
		{"https user name that holds a slash and no password", "https://team/s3cret@host/x", "https://host/x"},
		{"openvaultdb user name that holds a slash", "openvaultdb://alice/s3cret@host/c.json", "openvaultdb://host/c.json"},
		{"sqlite token that holds a slash", "sqlite://ab/s3cret@host/x.db", "sqlite://host/x.db"},
		{"sqlite without slashes and a token that holds a slash", "sqlite:ab/s3cret@host/x.db", "sqlite:host/x.db"},
		{"relative directory with an at sign in a later segment reads as userinfo", "ingitdb://dir/team@work/project", "ingitdb://work/project"},
		{"explicit relative directory keeps its at sign", "ingitdb://./dir/team@work/project", "ingitdb://./dir/team@work/project"},
		{"parent-relative directory keeps its at sign", "http://../team@work/project", "http://../team@work/project"},
		{"home directory keeps its at sign", "ingitdb://~/team@work/project", "ingitdb://~/team@work/project"},
		{"UNC path keeps its at sign", `sqlite://\\server\share@x\db.sqlite`, `sqlite://\\server\share@x\db.sqlite`},
		{"relative directory without an at sign", "ingitdb://dir/project", "ingitdb://dir/project"},
		// The file schemes dburl reads are paths, not hosts.
		{"sqlite3 relative file", "sqlite3:./x.db", "sqlite3:./x.db"},
		{"sqlite3 absolute file", "sqlite3:///tmp/a/x.db", "sqlite3:///tmp/a/x.db"},
		{"sqlite3 key in the query", "SQLITE3:///tmp/a/x.db?_pragma_key=s3cret", "sqlite3:///tmp/a/x.db"},
		{"file scheme", "file:./x.db?mode=ro", "file:./x.db"},
		{"duckdb file", "duckdb:///tmp/x.duckdb", "duckdb:///tmp/x.duckdb"},
		{"modernc sqlite alias", "mq:///tmp/x.db", "mq:///tmp/x.db"},
		{"file scheme with a token that holds a slash", "file://ab/s3cret@host/x.db", "file://host/x.db"},
		{"windows drive path under http", `http://C:\work\a@b`, `http://C:\work\a@b`},
		{"windows drive path with slashes under https", `https://C:/work/a@b/proj`, `https://C:/work/a@b/proj`},
		{"windows drive path under sqlite", `sqlite://C:\data\x.db`, `sqlite://C:\data\x.db`},
		{"http project directory", "http://./demo-project-1", "http://./demo-project-1"},
		{"http credentials", "https://alice:s3cret@api.example.com/x", "https://api.example.com/x"},
		{"http password that reads as host and port", "HTTP://alice:42/s3cret@host/x", "http://host/x"},
		{"http empty user name and a slash in the password", "http://:s3/cret@host/x", "http://host/x"},
		{"http token as the user name", "https://s3cret@host/x", "https://host/x"},
		{"path with a control character", "sqlite:///tmp/a\x00b.db", "sqlite://"},
		{"path that is not UTF-8", "sqlite:///tmp/\xff.db", "sqlite://"},
		{"path longer than any real path", "sqlite://" + strings.Repeat("a", 5000), "sqlite://"},

		// A URL wrapped in a path scheme is shown without anything in front of the host.
		{"wrapped URL with a password", "ingitdb://https://alice:s3cret@github.com/org/repo", "ingitdb://https://github.com/org/repo"},
		{"wrapped URL whose token is the user name before a colon", "ingitdb://https://TOKEN:x-oauth-basic@github.com/org/repo", "ingitdb://https://github.com/org/repo"},
		{"wrapped URL whose password starts with digits and a slash", "ingitdb://https://alice:42/s3cret@github.com/org/repo", "ingitdb://https://github.com/org/repo"},
		{"wrapped URL whose token is a digits-and-slash user name", "ingitdb://https://s3cret:42/x@github.com/org/repo", "ingitdb://https://github.com/org/repo"},
		{"wrapped URL whose token holds a slash and no colon", "ingitdb://https://s3/cret@github.com/org/repo", "ingitdb://https://github.com/org/repo"},
		{"wrapped URL with an empty user name", "ingitdb://https://:s3/cret@github.com/org/repo", "ingitdb://https://github.com/org/repo"},
		{"wrapped URL with an upper-case scheme", "INGITDB://HTTPS://tok@github.com/x", "ingitdb://https://github.com/x"},
		{"wrapped URL with a query", "ingitdb://https://github.com/org/repo?token=s3cret", "ingitdb://https://github.com/org/repo"},
		{"wrapped scheme and nothing else", "ingitdb://https://", "ingitdb://https://"},
		{"wrapped URL of a scheme nothing knows", "ingitdb://nosuch://x", UnparsableSource},

		// Anything that is not scheme-led, or whose scheme nothing knows, shows nothing from the input.
		{"unknown scheme", "nosuch://alice:s3cret@host/x", UnparsableSource},
		{"no scheme", "alice:s3cret@host/shop", UnparsableSource},
		{"a bare token", "s3cret", UnparsableSource},
		{"empty", "", UnparsableSource},
		{"libpq keywords", "host=db password=s3cret", UnparsableSource},
		{"scheme longer than any scheme", strings.Repeat("a", 40) + "://h/x", UnparsableSource},
		{"windows drive path with no scheme", `C:\data\x.db`, UnparsableSource},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, SourceDisplay(tc.in))
			assert.NotContains(t, SourceDisplay(tc.in), "s3cret")
		})
	}
}

func TestSourceIDDisplay(t *testing.T) {
	t.Parallel()
	// A plain name: letters and digits of any script, "." "_" and "-".
	for _, id := range []string{"chinook", "Chinook-local_2.db", "a", "Café", "データ-1", "Straße.v2"} {
		assert.Equal(t, id, SourceIDDisplay(id))
	}
	for _, id := range []string{"", "postgres://alice:s3cret@h/db", "alice:s3cret@h", "s3 cret", "-lead", strings.Repeat("a", 129), strings.Repeat("é", 129), "a/b", "a\x00b", "a\u202eb"} {
		assert.Equal(t, SourceIDNotShown, SourceIDDisplay(id), id)
	}
	assert.Equal(t, "<source id not shown>", SourceIDNotShown, "the placeholder says what it is: the ID is not hidden because it is wrong")
	assert.Equal(t, strings.Repeat("é", 128), SourceIDDisplay(strings.Repeat("é", 128)))
}

func TestPathDisplay(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		// A path with no scheme (what `datatug db ./chinook.sqlite` takes) is shown as it is, minus query and fragment.
		"./chinook.sqlite":          "./chinook.sqlite",
		"chinook.sqlite?key=s3cret": "chinook.sqlite",
		`C:\data\x.db`:              `C:\data\x.db`,
		"/tmp/a/x.db#s3cret":        "/tmp/a/x.db",
		"sqlite3:./x.db":            "sqlite3:./x.db",
		// A text that starts like userinfo is shown without it.
		"s3cret@host/db":    "host/db",
		"ab/s3cret@host/db": "host/db",
		// A URL is shown as SourceDisplay shows it.
		"postgres://alice:s3cret@db.example.com/shop": "postgres://db.example.com/shop",
		// What cannot be shown is named as such.
		"gone\x00.db": unprintablePath,
		"":            unprintablePath,
	} {
		assert.Equal(t, want, PathDisplay(in), in)
		assert.NotContains(t, PathDisplay(in), "s3cret", in)
	}
}

func TestLocalPathDisplay(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "./x.db", localPathDisplay("./x.db?_pragma_key=s3cret"))
	assert.Equal(t, "./x.db", localPathDisplay("./x.db#frag"))
	assert.Equal(t, `C:\work\x.db`, localPathDisplay(`C:\work\x.db`))
	assert.Equal(t, "", localPathDisplay("a\nb"))
	assert.Equal(t, "", localPathDisplay("a\x7fb"))
	assert.Equal(t, "", localPathDisplay("\xff"))
	assert.Equal(t, "", localPathDisplay(strings.Repeat("a", maxLocalPath+1)))
	assert.Equal(t, strings.Repeat("a", maxLocalPath), localPathDisplay(strings.Repeat("a", maxLocalPath)))
}

func TestBackendRef_DisplayBuildsFromTheParts(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "env:SHOP_PG_URL", BackendRef{Scheme: "postgres", Path: "postgres://u:s3cret@h/db", Raw: "env:SHOP_PG_URL"}.Display())
	assert.Equal(t, "postgres://h/db", BackendRef{Scheme: "postgres", Path: "postgres://u:s3cret@h/db"}.Display())
	assert.Equal(t, "sqlite:///tmp/x.db", BackendRef{Scheme: "sqlite", Path: "/tmp/x.db"}.Display())
	assert.Equal(t, "sqlite://./x.db", BackendRef{Scheme: "sqlite", Path: "./x.db?_pragma_key=s3cret"}.Display())
	assert.Equal(t, "http://./proj", BackendRef{Scheme: "http", Path: "./proj"}.Display())
	assert.Equal(t, "ingitdb://./proj", BackendRef{Scheme: "ingitdb", Path: "./proj"}.Display())
	assert.Equal(t, "openvaultdb:///c.json", BackendRef{Scheme: "openvaultdb", Path: "/c.json"}.Display())
	assert.Equal(t, UnparsableSource, BackendRef{Scheme: "nosuch", Path: "s3cret"}.Display())
	assert.Equal(t, UnparsableSource, BackendRef{}.Display())
}
