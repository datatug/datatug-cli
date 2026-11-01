package commands

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

// Retirement cannot start before the release carrying this notice is published.
// These notices preserve the legacy behavior throughout its warning period.
const legacySQLRetirementNotice = "Compatibility notice: updateUrlConfig is legacy SQL execution and will be retired only after at least two weeks of published warning. Use query run for read-only queries. This command is outside unified access grants; a reachable write credential remains its limit."

const remoteCopyCompatibilityNotice = "Compatibility notice: remote db copy targets use reachable write credentials without an access grant or deadline (accident prevention only). After at least two weeks of published warning, a future release will require --confirm-write-target=<database> and refuse Space-recorded targets. That flag is not available in this release; local files and loopback targets remain unchanged."

// This is a notice classifier, never an authorization or endpoint validator.
// Ambiguous non-file destinations get the notice; no target is refused here.
func warnRemoteCopyTarget(w io.Writer, target dbcopy.BackendRef) {
	switch target.Scheme {
	case "sqlite", "duckdb", "ingitdb", "http", "https":
		return // These source forms name files or a local project descriptor.
	}
	u, _ := url.Parse(target.Path)
	host := ""
	if u != nil {
		host = u.Hostname()
	}
	for _, address := range []string{target.CouchDBHost, target.PostgRESTHost, target.ClickHouseAddress, target.LibSQLHost, target.TypesenseHost} {
		if address != "" {
			host = (&url.URL{Host: address}).Hostname()
		}
	}
	if strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback() {
		return
	}
	_, _ = fmt.Fprintln(w, remoteCopyCompatibilityNotice)
}
