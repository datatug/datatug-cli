package dbcopy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// defaultSession is what a read of a source whose URL sets none of the session parameters gets, in
// the order postgresConnectionString appends them.
const defaultSession = "default_transaction_read_only=on&statement_timeout=30000&timezone=UTC&application_name=datatug&connect_timeout=10"

// writeSession is the same without the read-only default: only a copy target is opened for writing.
const writeSession = "statement_timeout=30000&timezone=UTC&application_name=datatug&connect_timeout=10"

// The one function that reads the parameters of a URL and decides the session of a connection, as
// a table: what each URL becomes, for a read and for the copy target that is written.
func TestPostgresConnectionString_Table(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		url   string
		write bool
		want  string
	}{
		// A read gets every default the URL does not set, appended after what was typed.
		{"a bare URL", "postgres://h/db", false, "postgres://h/db?" + defaultSession},
		{"a URL with a query", "postgres://h/db?sslmode=disable", false, "postgres://h/db?sslmode=disable&" + defaultSession},
		{"an empty query", "postgres://h/db?", false, "postgres://h/db?" + defaultSession},
		{"a query that ends with an ampersand", "postgres://h/db?sslmode=disable&", false, "postgres://h/db?sslmode=disable&" + defaultSession},
		{"a fragment stays a fragment", "postgres://h/db#frag", false, "postgres://h/db?" + defaultSession + "#frag"},
		{"a query and a fragment", "postgres://h/db?sslmode=disable#frag", false, "postgres://h/db?sslmode=disable&" + defaultSession + "#frag"},
		{"the postgresql alias is kept as it is", "postgresql://h/db", false, "postgresql://h/db?" + defaultSession},
		{"the userinfo is not rewritten", "postgres://alice:p%40ss%2Fw@h:5433/db", false, "postgres://alice:p%40ss%2Fw@h:5433/db?" + defaultSession},
		{"an empty password and a bracketed address", "postgres://alice:@[::1]:5433/db", false, "postgres://alice:@[::1]:5433/db?" + defaultSession},

		// A parameter the URL sets is not replaced, whatever the case of its name.
		{"the person sets the timeout", "postgres://h/db?statement_timeout=0", false,
			"postgres://h/db?statement_timeout=0&default_transaction_read_only=on&timezone=UTC&application_name=datatug&connect_timeout=10"},
		{"the person sets the time zone, in other case", "postgres://h/db?TimeZone=Europe/Kyiv", false,
			"postgres://h/db?TimeZone=Europe/Kyiv&default_transaction_read_only=on&statement_timeout=30000&application_name=datatug&connect_timeout=10"},
		{"the person names the application", "postgres://h/db?application_name=mine", false,
			"postgres://h/db?application_name=mine&default_transaction_read_only=on&statement_timeout=30000&timezone=UTC&connect_timeout=10"},
		{"the person sets the connect timeout", "postgres://h/db?connect_timeout=3", false,
			"postgres://h/db?connect_timeout=3&default_transaction_read_only=on&statement_timeout=30000&timezone=UTC&application_name=datatug"},
		{"read-only is already on", "postgres://h/db?default_transaction_read_only=on", false,
			"postgres://h/db?default_transaction_read_only=on&statement_timeout=30000&timezone=UTC&application_name=datatug&connect_timeout=10"},
		{"read-only is on, spelled otherwise", "postgres://h/db?Default_Transaction_Read_Only=TRUE", false,
			"postgres://h/db?Default_Transaction_Read_Only=TRUE&statement_timeout=30000&timezone=UTC&application_name=datatug&connect_timeout=10"},
		{"every parameter is set", "postgres://h/db?default_transaction_read_only=yes&statement_timeout=1&timezone=UTC&application_name=a&connect_timeout=1", false,
			"postgres://h/db?default_transaction_read_only=yes&statement_timeout=1&timezone=UTC&application_name=a&connect_timeout=1"},
		{"an options string that sets something else", "postgres://h/db?options=-c%20search_path%3Dsales", false,
			"postgres://h/db?options=-c%20search_path%3Dsales&" + defaultSession},

		// Names are compared as ASCII, as the server compares them: a key that only lower-cases to the
		// name of a parameter, because Unicode folds a letter of it to an ASCII one, is not that parameter,
		// so the default is still appended (the adapter refuses a setting whose name is not plain ASCII).
		{"a key that Unicode folds to the read-only parameter", "postgres://h/db?default_transact%C4%B0on_read_only=on", false,
			"postgres://h/db?default_transact%C4%B0on_read_only=on&" + defaultSession},
		{"a key that Unicode folds to the timeout", "postgres://h/db?statement_t%C4%B0meout=0", false,
			"postgres://h/db?statement_t%C4%B0meout=0&" + defaultSession},
		{"an options string whose Unicode folds to the read-only parameter is not the parameter", "postgres://h/db?options=-c%20default_transact%C4%B0on_read_only%3Doff", false,
			"postgres://h/db?options=-c%20default_transact%C4%B0on_read_only%3Doff&" + defaultSession},

		// A service or a service file in the URL of a write is kept as typed.
		{"a target that names a service", "postgres://h/db?service=mine", true, "postgres://h/db?service=mine&" + writeSession},

		// The copy target is the one connection that is written through: no read-only default.
		{"a target", "postgres://h/db", true, "postgres://h/db?" + writeSession},
		{"a target whose URL turns read-only off", "postgres://h/db?default_transaction_read_only=off", true,
			"postgres://h/db?default_transaction_read_only=off&" + writeSession},
		{"a target whose URL keeps read-only on", "postgres://h/db?default_transaction_read_only=on", true,
			"postgres://h/db?default_transaction_read_only=on&" + writeSession},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := postgresConnectionString(tc.url, tc.write)
			assert.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// A URL that turns the read-only session off is refused on a read, whatever it writes for "off",
// and it is refused again when the same name is set twice and one of the values is not on.
func TestPostgresConnectionString_RefusesAReadThatTurnsReadOnlyOff(t *testing.T) {
	t.Parallel()
	for _, query := range []string{
		"default_transaction_read_only=off",
		"default_transaction_read_only=OFF",
		"default_transaction_read_only=false",
		"default_transaction_read_only=0",
		"default_transaction_read_only=no",
		"default_transaction_read_only=f",
		"default_transaction_read_only=n",
		"default_transaction_read_only=",
		"default_transaction_read_only=perhaps",
		"DEFAULT_TRANSACTION_READ_ONLY=off",
		"default_transaction_read_only=on&default_transaction_read_only=off",
		"default_transaction_read_only=off&default_transaction_read_only=on",
		"options=-c%20default_transaction_read_only%3Doff",
		"options=-c%20DEFAULT_TRANSACTION_READ_ONLY%3dOFF",
		"options=--default_transaction_read_only%3Doff",
	} {
		got, err := postgresConnectionString("postgres://"+markerUser+":"+markerPassword+"@h/db?x="+markerQuery+"&"+query, false)
		assert.Empty(t, got, query)
		assert.Same(t, errPostgresReadOnlyOff, err, query)
		assertNoMarkers(t, query, err)
	}
}

// A URL that names a service or a service file is refused on a read, whatever the case of the name: a service
// file can set the session, in a spelling the URL does not show, beside the read-only default. Only the name of
// the setting is said, and nothing of the URL.
func TestPostgresConnectionString_RefusesAReadThatNamesAServiceFile(t *testing.T) {
	t.Parallel()
	for _, query := range []string{"service=mine", "servicefile=%2Ftmp%2Fpg_service.conf", "SERVICE=mine", "ServiceFile=x", "sslmode=disable&service="} {
		got, err := postgresConnectionString("postgres://"+markerUser+":"+markerPassword+"@h/db?x="+markerQuery+"&"+query, false)
		assert.Empty(t, got, query)
		assert.Same(t, errPostgresServiceFile, err, query)
		assertNoMarkers(t, query, err)
	}
	assert.Contains(t, errPostgresServiceFile.Error(), "service")
	assert.Contains(t, errPostgresServiceFile.Error(), "servicefile")
}

// The refusal says what is wrong and where writing is allowed, and quotes nothing of the URL.
func TestErrPostgresReadOnlyOff_NamesTheParameterAndTheOnlyWayToWrite(t *testing.T) {
	t.Parallel()
	assert.Contains(t, errPostgresReadOnlyOff.Error(), "default_transaction_read_only")
	assert.Contains(t, errPostgresReadOnlyOff.Error(), "datatug db copy --to")
}

// A URL that cannot be read gets the one sentence of the URL parser, with nothing of the URL.
func TestPostgresConnectionString_RefusesAURLItCannotRead(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"postgres://h:notaport/db", "postgres://%zz@h/db", "://", "postgres://" + markerUser + ":" + markerPassword + "@h:" + markerQuery + "/db"} {
		got, err := postgresConnectionString(raw, false)
		assert.Empty(t, got, raw)
		assert.Same(t, errUnreadablePostgresURL, err, raw)
		assertNoMarkers(t, raw, err)
	}
}
