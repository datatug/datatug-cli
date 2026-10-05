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
		{"short alias dburl registers itself", "my://alice:s3cret@localhost/shop", "my://localhost/shop"},
		{"scheme with a transport", "MySQL+Unix://alice:s3cret@/var/run/mysqld.sock/shop", "mysql+unix://"},
		{"scheme with a transport and a host", "postgres+tcp://alice:s3cret@db.example.com:5432/shop", "postgres+tcp://db.example.com:5432/shop"},
		{"transport on a scheme nothing knows", "nosuch+unix://alice:s3cret@h/shop", UnparsableSource},
		{"transport with nothing before it", "+unix://alice:s3cret@h/shop", UnparsableSource},
		{"transport on a scheme that is not a database", "ingitdb+tcp://alice:s3cret@h/shop", UnparsableSource},
		// Only the transports dburl knows are shown after a "+": tcp, udp and unix, in any case.
		{"upper-case transport", "MySQL+TCP://alice:s3cret@db.example.com:3306/shop", "mysql+tcp://db.example.com:3306/shop"},
		{"udp transport", "mysql+udp://alice:s3cret@db.example.com:3306/shop", "mysql+udp://db.example.com:3306/shop"},
		{"transport that is not one of the three", "mysql+tok_Zk39xq://carol:pw-Zk39x@db.example.com/shop", UnparsableSource},
		{"transport that is a word", "postgres+secretword://db.example.com/shop", UnparsableSource},
		{"transport that holds a second plus sign", "mysql+unix+x://db.example.com/shop", UnparsableSource},
		{"transport that is empty", "mysql+://db.example.com/shop", UnparsableSource},
		// A short alias dburl registers itself is local when the scheme it resolves to is.
		{"sqlite short alias with a relative file", "sq:./chinook.db", "sq:./chinook.db"},
		{"sqlite short alias with an absolute file", "SQ:///tmp/chinook.db?mode=ro", "sq:///tmp/chinook.db"},
		{"sqlite short alias with userinfo", "sq://carol:pw-Zk39x@host.example/chinook.db", "sq://host.example/chinook.db"},
		{"sqlite short alias and a second URL", "sq:///https://tok_Zk39xq@git.example/x.db", "sq://git.example/x.db"},
		{"a short alias of a database is still a host", "pg://carol:pw-Zk39x@db.example.com:5432/shop", "pg://db.example.com:5432/shop"},
		{"a short alias of a database is not a file", "my:./shop", "my:/shop"},
		{"a short alias with a transport is a host", "sq+unix://carol:pw-Zk39x@host.example/x", "sq+unix://host.example/x"},

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
		// The stand-in for a source that could not be resolved shows only the plain ID it stands for.
		{"internal unavailable source", "unavailable://shop", "unavailable://shop"},
		{"internal unavailable source of a non-ASCII ID", "unavailable://Caf%C3%A9-local", "unavailable://Caf%C3%A9-local"},
		{"internal unavailable source of an ID with a space", "unavailable://Chinook%20db", "unavailable://"},
		{"internal unavailable source of an escaped source URL", "unavailable://postgres:%2F%2Falice:s3cret@db.example.com%2Fshop%3Fpassword=s3cret", "unavailable://"},
		{"internal unavailable source of an escaped query", "unavailable://postgres:%2F%2Fdb.example.com%2Fshop%3Fpassword=s3cret", "unavailable://"},
		{"internal unavailable source of an escaped fragment", "unavailable://sqlite:%2F%2F%2Fx.db%23s3cret", "unavailable://"},
		{"internal unavailable source with a raw query", "unavailable://shop?password=s3cret", "unavailable://"},
		{"internal unavailable source with a raw fragment", "unavailable://shop#s3cret", "unavailable://"},
		{"internal unavailable source that is not an escaped segment", "unavailable://shop%zz", "unavailable://"},
		{"internal unavailable source with nothing after it", "unavailable://", "unavailable://"},
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
		// A UNC start counts as a path only when it does not read as "user:password@host".
		{"http userinfo after a UNC start", `http://\\alice:s3cret@host/x`, "http://host/x"},
		{"ingitdb token after a UNC start", `ingitdb://\\tok:x-oauth-basic@github.com/org/repo`, "ingitdb://github.com/org/repo"},
		{"sqlite userinfo after a UNC start", `sqlite://\\alice:s3cret@host/x.db`, "sqlite://host/x.db"},
		{"openvaultdb userinfo after a UNC start", `openvaultdb://\\alice:s3cret@host/c.json`, "openvaultdb://host/c.json"},
		// No server name holds an "@": a token written as the user name straight after a UNC start is credentials.
		{"http token as the user name after a UNC start", `http://\\tok_Zk39xq@host.example/x`, "http://host.example/x"},
		{"https token as the user name after a UNC start", `https://\\tok_Zk39xq@host.example/team/repo`, "https://host.example/team/repo"},
		{"sqlite token as the user name after a UNC start", `sqlite://\\tok_Zk39xq@host.example/x.db`, "sqlite://host.example/x.db"},
		{"ingitdb token as the user name after a UNC start", `ingitdb://\\tok_Zk39xq@git.example/team/repo`, "ingitdb://git.example/team/repo"},
		{"openvaultdb token as the user name after a UNC start", `openvaultdb://\\tok_Zk39xq@host.example/c.json`, "openvaultdb://host.example/c.json"},
		{"UNC path with a colon and no at sign", `sqlite://\\server:445\share\db.sqlite`, `sqlite://\\server:445\share\db.sqlite`},
		// A Windows WebDAV path writes the server as host@SSL, host@port or host@SSL@port: those server names
		// are paths and are shown as typed, in any case of "SSL". Any other text in front of a second "@" in the
		// server name is a token as the user name.
		{"sqlite WebDAV UNC path over SSL", `sqlite://\\host.example@SSL\share\db.sqlite`, `sqlite://\\host.example@SSL\share\db.sqlite`},
		{"http WebDAV UNC path with a port", `http://\\host.example@8080\share\proj`, `http://\\host.example@8080\share\proj`},
		{"ingitdb WebDAV UNC path over SSL with a port", `ingitdb://\\host.example@SSL@8443\share\proj`, `ingitdb://\\host.example@SSL@8443\share\proj`},
		{"openvaultdb WebDAV UNC path over SSL in lower case", `openvaultdb://\\host.example@ssl\share\c.json`, `openvaultdb://\\host.example@ssl\share\c.json`},
		{"https WebDAV UNC path with slashes", `https://\\host.example@SSL/share/proj`, `https://\\host.example@SSL/share/proj`},
		{"WebDAV UNC path with an at sign in the share", `sqlite://\\host.example@SSL\share\a@b\db.sqlite`, `sqlite://\\host.example@SSL\share\a@b\db.sqlite`},
		{"token and SSL and a host in the server name", `http://\\tok_Zk39xq@SSL@host.example/x`, "http://host.example/x"},
		{"token and a port that is out of range", `ingitdb://\\tok_Zk39xq@99999/team/repo`, "ingitdb://99999/team/repo"},
		{"token and three at signs in the server name", `sqlite://\\tok_Zk39xq@SSL@8443@host.example/x.db`, "sqlite://host.example/x.db"},
		{"a server name that is not a host in front of SSL", `https://\\tok_Zk39xq!@SSL/x`, "https://SSL/x"},
		{"no server name in front of SSL", `openvaultdb://\\@SSL/c.json`, "openvaultdb://SSL/c.json"},
		// A UNC path has a server name: with a separator straight after the two backslashes the first segment is
		// empty, and what follows up to the "@" is a token, not a share.
		{"http token after a UNC start and a backslash", `http://\\\tok_Zk39xq@host.example/x`, "http://host.example/x"},
		{"https token after a UNC start and a slash", `https://\\/tok_Zk39xq@host.example/team/repo`, "https://host.example/team/repo"},
		{"sqlite token after a UNC start and a backslash", `sqlite://\\\tok_Zk39xq@host.example/x.db`, "sqlite://host.example/x.db"},
		{"sqlite token after a UNC start and a slash", `sqlite://\\/tok_Zk39xq@host.example/x.db`, "sqlite://host.example/x.db"},
		{"ingitdb token after a UNC start and a backslash", `ingitdb://\\\tok_Zk39xq@git.example/team/repo`, "ingitdb://git.example/team/repo"},
		{"openvaultdb token after a UNC start and a slash", `openvaultdb://\\/tok_Zk39xq@host.example/c.json`, "openvaultdb://host.example/c.json"},
		{"UNC path with an empty server name and no at sign", `sqlite://\\\share\db.sqlite`, `sqlite://\\\share\db.sqlite`},
		{"relative directory without an at sign", "ingitdb://dir/project", "ingitdb://dir/project"},
		// A second "scheme://" in front of the last "@" is not a path, whatever the text starts with: only what follows
		// the "@" is shown. A user name with a slash after a UNC start is userinfo for the same reason.
		{"ingitdb absolute start and a second URL", "ingitdb:///https://carol:pw-Zk39x@git.example/team/repo", "ingitdb://git.example/team/repo"},
		{"ingitdb dot start and a second URL with a token", "ingitdb://./https://tok_Zk39xq@git.example/team/repo", "ingitdb://git.example/team/repo"},
		{"ingitdb dot start and a second URL whose password holds a slash", "ingitdb://./https://carol:42/pw-Zk39x@git.example/team/repo", "ingitdb://git.example/team/repo"},
		{"ingitdb parent start and a second URL", "ingitdb://../postgres://carol:pw-Zk39x@git.example/team/repo", "ingitdb://git.example/team/repo"},
		{"ingitdb home start and a second URL", "ingitdb://~/http://tok_Zk39xq@git.example/team/repo", "ingitdb://git.example/team/repo"},
		{"ingitdb drive start and a second URL", `ingitdb://C:/https://tok_Zk39xq@git.example/team/repo`, "ingitdb://git.example/team/repo"},
		{"ingitdb drive start with a backslash and a second URL", `ingitdb://C:\https://tok_Zk39xq@git.example/team/repo`, "ingitdb://git.example/team/repo"},
		{"ingitdb UNC start and a second URL", `ingitdb://\\host\https://tok_Zk39xq@git.example/team/repo`, "ingitdb://git.example/team/repo"},
		{"sqlite absolute start and a second URL", "sqlite:///postgres://carol:pw-Zk39x@db.example/shop", "sqlite://db.example/shop"},
		{"sqlite without slashes and a second URL", "sqlite:/https://tok_Zk39xq@git.example/x", "sqlite:git.example/x"},
		{"http absolute start and a second URL", "http:///https://carol:pw-Zk39x@git.example/team/repo", "http://git.example/team/repo"},
		{"https dot start and a second URL", "https://./http://tok_Zk39xq@git.example/team/repo", "https://git.example/team/repo"},
		{"openvaultdb absolute start and a second URL", "openvaultdb:///https://carol:pw-Zk39x@git.example/c.json", "openvaultdb://git.example/c.json"},
		{"file scheme and a second URL", "file:///https://tok_Zk39xq@git.example/x.db", "file://git.example/x.db"},
		{"upper-case schemes and a second URL", "SQLITE:///HTTPS://tok_Zk39xq@git.example/x", "sqlite://git.example/x"},
		{"second URL whose host cannot be shown", "sqlite:///https://carol:pw-Zk39x@git_host!/x", "sqlite:///x"},
		{"UNC start and a user name that holds a slash", `http://\\corp/carol:pw-Zk39x@host.example/x`, "http://host.example/x"},
		{"ingitdb UNC start and a user name that holds a slash", `ingitdb://\\corp/carol:pw-Zk39x@git.example/team/repo`, "ingitdb://git.example/team/repo"},
		{"sqlite UNC start and a token that holds a slash and no colon", `sqlite://\\ab/tok_Zk39xq@host.example/x.db`, `sqlite://\\ab/tok_Zk39xq@host.example/x.db`},
		// Only text in front of the last "@" counts: a "scheme://" after it, or a path that merely holds a colon, stays a path.
		{"a second URL after the at sign stays a path", "sqlite:///data/team@work/https://x", "sqlite:///data/team@work/https://x"},
		{"a single slash after a scheme name is a directory", "ingitdb:///srv/https:/x@work/project", "ingitdb:///srv/https:/x@work/project"},
		{"explicit path with a colon in front of the at sign", "ingitdb://./a:b/c@d", "ingitdb://./a:b/c@d"},
		{"UNC path with a slash in front of the at sign and no colon", `sqlite://\\server\share\a@b\db.sqlite`, `sqlite://\\server\share\a@b\db.sqlite`},
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
		// A drive path written with a doubled slash ("C://work/a@b") is a path, not a
		// second URL, and Parse accepts it. "C://" also reads as a wrapped URL whose
		// scheme is not known, so nothing of it is shown: it is never a leak.
		{"drive path with a doubled slash and an at sign under https", `https://C://work/a@b/proj`, UnparsableSource},
		{"drive path with a doubled slash and an at sign under sqlite", `sqlite://C://work/a@b.db`, UnparsableSource},
		{"https drive start and a second URL", `https://C:/https://tok_Zk39xq@git.example/team/repo`, "https://git.example/team/repo"},
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

// A query ID is folders and a name joined with "/": it is shown only when every part
// is a plain name, and as the placeholder otherwise, a whole source string included.
func TestQueryIDDisplay(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"customer-invoices", "customers/customer-invoices", "a/b/c", "Café/données-1.v2"} {
		assert.Equal(t, id, QueryIDDisplay(id))
	}
	for _, id := range []string{
		"", "/", "a//b", "/a", "a/", "../x", "a/./b", "a/../b", "postgres://alice:s3cret@h/db", "alice:s3cret@h/x",
		"a b/c", `a\b/c`, "a\x00b/c", strings.Repeat("a", 129) + "/b", SourceIDNotShown,
	} {
		assert.Equal(t, SourceIDNotShown, QueryIDDisplay(id), id)
	}
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

// The path field of a catalog names a file or a directory. One that is a URL, or
// holds a URL in front of its last "@", is neither, and what follows the scheme may
// be credentials: PathHoldsURL says so, so that no message shows it as a path.
func TestPathHoldsURL(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]bool{
		"https://tok_Zk39xq@git.example/x":      true,
		"HTTPS://carol:pw@git.example/x":        true,
		"https://git.example/x":                 true,
		"postgres://carol:pw@db.example/shop":   true,
		"./https://tok_Zk39xq@git.example/x":    true,
		"/srv/https://tok_Zk39xq@git.example/x": true,
		`C:/https://tok_Zk39xq@git.example/x`:   true,
		`C://https://tok_Zk39xq@git.example/x`:  true,
		"dbs/chinook.sqlite":                    false,
		"~/datatug/dbs/chinook.sqlite":          false,
		"/data/team@work/https://x":             false,
		"./team@work/a.db":                      false,
		`C:\work\a@b.db`:                        false,
		"C://work/a@b.db":                       false,
		"c://work/a.db":                         false,
		`\\server\share@x\a.db`:                 false,
		"":                                      false,
	} {
		assert.Equal(t, want, PathHoldsURL(path), "%q", path)
	}
}
