package dbcopy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2clickhouse"
)

const (
	ClickHouseScheme           = "clickhouse"
	ClickHouseHTTPSScheme      = "clickhouse+https"
	ClickHouseHTTPScheme       = "clickhouse+http"
	ClickHouseUsernameEnv      = "DATATUG_CLICKHOUSE_USERNAME"
	ClickHousePasswordEnv      = "DATATUG_CLICKHOUSE_PASSWORD"
	ClickHouseRelationsEnv     = "DATATUG_CLICKHOUSE_RELATIONS"
	ClickHouseNativeSQLEnv     = "DATATUG_CLICKHOUSE_NATIVE_SQL"
	ClickHouseNativeSourcesEnv = "DATATUG_CLICKHOUSE_NATIVE_SQL_SOURCES"
)

var clickHouseIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func IsClickHouseScheme(s string) bool {
	return s == ClickHouseScheme || s == ClickHouseHTTPSScheme || s == ClickHouseHTTPScheme
}

// ClickHouse uses one database, an explicit port, and no executable URL options.
// Credentials and relation grants belong exclusively to the operator environment.
func parseClickHouseSource(scheme, rest, _ string) (BackendRef, error) {
	bad := func() (BackendRef, error) {
		return BackendRef{}, errors.New("invalid ClickHouse source: require HOST:PORT/DATABASE, no URL credentials, encoding, query or fragment; plaintext requires a literal loopback IP")
	}
	u, err := url.Parse(scheme + "://" + rest)
	if err != nil || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.EscapedPath() != u.Path || strings.ContainsAny(u.Host, " \t\r\n%") {
		return bad()
	}
	host, portText, err := net.SplitHostPort(u.Host)
	port, portErr := strconv.Atoi(portText)
	if err != nil || portErr != nil || port < 1 || port > 65535 || host == "" || strings.ContainsAny(host, "/\\?#@") || !clickHouseIdentifier.MatchString(strings.TrimPrefix(u.Path, "/")) {
		return bad()
	}
	if scheme == ClickHouseHTTPScheme {
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return bad()
		}
	}
	return BackendRef{Scheme: scheme, Path: u.Host + u.Path, Raw: scheme + "://" + u.Host + u.Path, ClickHouseAddress: u.Host, ClickHouseDatabase: strings.TrimPrefix(u.Path, "/")}, nil
}

// Config parsing is pure: no connection or read-only probe happens until a read.
// A relation map is mandatory for structured/schema operations even when the
// independently scoped native SQL capability is enabled.
func (r BackendRef) ClickHouseConfig(lookup func(string) (string, bool), native bool) (dalgo2clickhouse.Config, error) {
	fail := func() (dalgo2clickhouse.Config, error) {
		return dalgo2clickhouse.Config{}, errors.New("invalid ClickHouse environment configuration; check username/password, relations and native SQL opt-in")
	}
	if !IsClickHouseScheme(r.Scheme) || r.ClickHouseAddress == "" {
		return fail()
	}
	user, _ := lookup(ClickHouseUsernameEnv)
	password, _ := lookup(ClickHousePasswordEnv)
	if user == "" || len(user) > 256 || len(password) > 4096 || strings.ContainsAny(user+password, "\x00\r\n") {
		return fail()
	}
	opt, _ := lookup(ClickHouseNativeSQLEnv)
	if opt != "" && opt != "true" && opt != "false" {
		return fail()
	}
	if native && opt != "true" {
		return dalgo2clickhouse.Config{}, errors.New("ClickHouse native SQL requires operator opt-in via " + ClickHouseNativeSQLEnv)
	}
	if native {
		// An operator-wide enable switch cannot silently grant every saved DSN.
		// Only explicitly named environment sources may use opaque SQL.
		name := strings.TrimPrefix(r.Raw, "env:")
		allowed, _ := lookup(ClickHouseNativeSourcesEnv)
		if !strings.HasPrefix(r.Raw, "env:") || !ValidEnvName(name) || len(allowed) > 4096 {
			return dalgo2clickhouse.Config{}, errors.New("ClickHouse native SQL requires an env:NAME source listed in " + ClickHouseNativeSourcesEnv)
		}
		seen := map[string]bool{}
		found := false
		for _, entry := range strings.Split(allowed, ",") {
			entry = strings.TrimSpace(entry)
			if !ValidEnvName(entry) || len(entry) > 256 || seen[entry] {
				return fail()
			}
			seen[entry] = true
			if entry == name {
				found = true
			}
		}
		if !found {
			return dalgo2clickhouse.Config{}, errors.New("ClickHouse native SQL source is not opted in via " + ClickHouseNativeSourcesEnv)
		}
	}
	text, _ := lookup(ClickHouseRelationsEnv)
	tables := map[string][]string{}
	if text != "" {
		if len(text) > 64<<10 {
			return fail()
		}
		// Decode entries explicitly so duplicate relation names cannot silently
		// replace a restrictive grant with a different one.
		d := json.NewDecoder(strings.NewReader(text))
		first, err := d.Token()
		if err != nil || first != json.Delim('{') {
			return fail()
		}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return fail()
			}
			name, ok := key.(string)
			if !ok || !clickHouseIdentifier.MatchString(name) || len(tables) >= 256 {
				return fail()
			}
			if _, exists := tables[name]; exists {
				return fail()
			}
			var columns []string
			if d.Decode(&columns) != nil || columns == nil || len(columns) > 256 {
				return fail()
			}
			seen := map[string]bool{}
			for _, col := range columns {
				if !clickHouseIdentifier.MatchString(col) || seen[col] {
					return fail()
				}
				seen[col] = true
			}
			tables[name] = columns
		}
		if _, err = d.Token(); err != nil {
			return fail()
		}
		var trailing any
		if d.Decode(&trailing) != io.EOF {
			return fail()
		}
	}
	if !native && len(tables) == 0 {
		return dalgo2clickhouse.Config{}, errors.New("ClickHouse structured reads and schema scans require " + ClickHouseRelationsEnv + " (JSON table-to-columns map; [] permits all visible columns of that table)")
	}
	return dalgo2clickhouse.Config{Address: r.ClickHouseAddress, Database: r.ClickHouseDatabase, Username: user, Password: password, Tables: tables, NativeSQL: native, HTTP: r.Scheme != ClickHouseScheme, PlaintextLoopback: r.Scheme == ClickHouseHTTPScheme, Timeout: 10 * time.Second, MaxRows: 1000, MaxBytes: 4_000_000}, nil
}

var newClickHouseDatabase = dalgo2clickhouse.NewDB

func openClickHouse(ctx context.Context, r BackendRef, mode openMode) (dal.DB, error) {
	if mode.forWrite {
		return nil, &refusedError{"ClickHouse sources are read-only; copying into ClickHouse is unavailable"}
	}
	return r.OpenClickHouse(ctx, false)
}

// OpenClickHouse opens the requested capability. Native SQL never broadens a
// structured relation grant, and the adapter performs its locked read-only probe.
func (r BackendRef) OpenClickHouse(ctx context.Context, native bool) (dal.DB, error) {
	cfg, err := r.ClickHouseConfig(os.LookupEnv, native)
	if err != nil {
		return nil, err
	}
	db, err := newClickHouseDatabase(ctx, cfg)
	if err != nil {
		return nil, &openError{message: "ClickHouse open failed: " + ClickHouseFailureReason(err), cause: err}
	}
	return db, nil
}

func ClickHouseFailureReason(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "the read was cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "the read exceeded its deadline"
	case errors.Is(err, dalgo2clickhouse.ErrLimit):
		return "the read exceeded its finite result budget"
	case errors.Is(err, dal.ErrNotSupported):
		return "the ClickHouse adapter does not support this query or capability"
	default:
		return "the ClickHouse read was refused; check the environment configuration, permitted relations and locked read-only server profile"
	}
}
