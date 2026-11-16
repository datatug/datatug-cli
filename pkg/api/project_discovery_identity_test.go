package api

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestOrdinaryServerDiscoveryRejectsMergedForeignIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"valid", `{"id":"mysql:active:3306","server":{"driver":"mysql","host":"active","port":3306}}`, true},
		{"case-variant", `{"id":"mysql:active:3306","server":{"driver":"mysql","host":"active","port":3306},"Server":{"driver":"postgres","host":"foreign","port":5432}}`, false},
		{"duplicate-path", `{"id":"mysql:active:3306","server":{"driver":"mysql","host":"active","port":3306,"path":"foreign-path"},"server":{"driver":"mysql","host":"active","port":3306}}`, false},
		{"duplicate-port", `{"id":"mysql:active","server":{"driver":"mysql","host":"active","port":3306},"server":{"driver":"mysql","host":"active"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, branch, ref := ordinaryDiscoveryFixture(t)
			id := "mysql:active:3306"
			if tc.name == "duplicate-port" {
				id = "mysql:active"
			}
			writeDiscoveryFixture(t, dir, "dbs/mysql/"+id+".dbserver.json", []byte(tc.body))
			rows, err := LocalProjectDbServerDiscovery(t.Context(), ref, branch)
			if tc.valid {
				require.NoError(t, err)
				require.Len(t, rows, 1)
				require.Equal(t, "active", rows[0].Server.Host)
			} else {
				require.Error(t, err)
				require.Nil(t, rows)
			}
		})
	}
}

func TestOrdinaryServerTagsKeepOpaqueNonIdentityFields(t *testing.T) {
	dir, branch, ref := ordinaryDiscoveryFixture(t)
	writeDiscoveryFixture(t, dir, "dbs/mysql/mysql:active:3306.dbserver.json", []byte(`{"id":"mysql:active:3306","server":{"driver":"mysql","host":"active","port":3306},"title":5,"catalogs":{"future":true},"tags":["retained"]}`))
	tags, err := LocalProjectItemTags(t.Context(), ItemTagsSelector{ProjectRef: ref, Branch: branch, Kind: "dbserver", Driver: "mysql", ID: "mysql:active:3306"})
	require.NoError(t, err)
	require.Equal(t, []string{"retained"}, tags.Tags)
	rows, err := LocalProjectDbServerDiscovery(t.Context(), ref, branch)
	require.Error(t, err, "a full native DTO must still be valid for discovery")
	require.Nil(t, rows)
}
