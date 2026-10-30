package dbcopy

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The one rule for the host of a db server that a project records: the host is part of the
// ID of the server, which is a file name, and of the connection string that is built to reach
// the server, so it is a host name or an address and nothing else.
func TestIsRecordableHost(t *testing.T) {
	for _, host := range []string{"localhost", "db.example.com", "10.0.0.1", "::1", "fe80::1", "my_host-1", "DB1", "a", "1", ":", strings.Repeat("a", MaxHostLength)} {
		assert.True(t, IsRecordableHost(host), host)
	}
	for _, host := range []string{
		"", " ", "a b", "a;b", "a=b", "key=value;other=value", "user@host", "host/x", `a\b`, "a%2Fb", "fe80::1%eth0",
		"host,host2", "[::1]", "-host", ".host", "_host", "host\x00", "host\n", "db.example.com:1433@x", "../x", "hôte",
		strings.Repeat("a", MaxHostLength+1),
	} {
		assert.False(t, IsRecordableHost(host), host)
	}
}
