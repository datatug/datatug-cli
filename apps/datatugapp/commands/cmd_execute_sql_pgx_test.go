package commands

import (
	"database/sql"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The PostgreSQL adapter's import registers the database/sql drivers "pgx" and
// "pgx/v5" in the binary, and updateUrlConfig would open either by the name it is
// given, with a password from its command line.
func TestExecuteSQLCommandValidate_RefusesThePgxDrivers(t *testing.T) {
	registered := sql.Drivers()
	var messages []string
	for _, driver := range []string{"pgx", "pgx/v5"} {
		assert.Truef(t, slices.Contains(registered, driver), "%q is registered in the binary: this test is about a real driver name", driver)

		v := &executeSQLCommand{Driver: driver, Host: "db.example.com", User: "alice", Password: "hunter2-p4ss", Schema: "shop", CommandText: "SELECT 1"}
		err := v.Validate()
		require.Errorf(t, err, "driver %q", driver)
		assert.Contains(t, err.Error(), errExecuteSQLPostgresDriver.Error())
		assert.NotContains(t, err.Error(), "hunter2-p4ss")
		assert.NotContains(t, err.Error(), "db.example.com")
		messages = append(messages, err.Error())
	}
	assert.Equal(t, messages[0], messages[1], "a static message: nothing the caller typed, not even the driver name, is echoed")
	assert.NoError(t, (&executeSQLCommand{Driver: "sqlite3", Query: "a"}).Validate(), "other drivers are still validated as before")
}
