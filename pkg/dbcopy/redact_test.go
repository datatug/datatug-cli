package dbcopy

import (
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
		{"keyword DSN", "host=db user=u password=s3cret dbname=x", "host=db user=u password=xxxxx dbname=x"},
		{"keyword DSN with spaces around equals", "host=db PASSWORD = s3cret dbname=x", "host=db PASSWORD = xxxxx dbname=x"},
		{"keyword DSN quoted value", `password='it\'s a secret' host=x`, `password=xxxxx host=x`},
		{"keyword DSN sslpassword", "sslpassword=s3cret", "sslpassword=xxxxx"},
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
		assert.Contains(t, rendered, "postgres://alice:xxxxx@db.example.com:5432/shop")
	}
}
