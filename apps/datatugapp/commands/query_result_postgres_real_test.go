package commands

import (
	"context"
	"testing"

	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chinookKeysDDL is the part of Chinook the result metadata walks: the same
// tables and foreign keys as the SQLite fixture, in PostgreSQL.
// The scan fixture's tables of schema public go first: their lowercase
// customer and invoice would match these names, which compare without case.
var chinookKeysDDL = []string{
	`DROP TABLE IF EXISTS order_line, invoice, audit_log, type_matrix, "MixedCase", customer CASCADE`,
	`CREATE TABLE "Employee" ("EmployeeId" integer PRIMARY KEY, "FirstName" text NOT NULL)`,
	`CREATE TABLE "Customer" ("CustomerId" integer PRIMARY KEY, "FirstName" text NOT NULL, "SupportRepId" integer REFERENCES "Employee"("EmployeeId"))`,
	`CREATE TABLE "Invoice" ("InvoiceId" integer PRIMARY KEY, "CustomerId" integer NOT NULL REFERENCES "Customer"("CustomerId"), "Total" numeric(10,2) NOT NULL)`,
	`CREATE TABLE "InvoiceLine" ("InvoiceLineId" integer PRIMARY KEY, "InvoiceId" integer NOT NULL REFERENCES "Invoice"("InvoiceId"), "Quantity" integer NOT NULL)`,
	`INSERT INTO "Employee" VALUES (1, 'E')`,
	`INSERT INTO "Customer" VALUES (1, 'C', 1)`,
	`INSERT INTO "Invoice" VALUES (1, 1, 9.99)`,
	`INSERT INTO "InvoiceLine" VALUES (1, 1, 2)`,
}

// The participants and related tables of a result on the real PostgreSQL
// server of the CI job are the ones of the SQLite fixture, through the same
// dalgo schema calls; and the chat offers its joins there too.
func TestPostgresResultMetaMatchesSQLite(t *testing.T) {
	server := newRealPgServer(t)
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	execAll(t, server.scanURL, chinookKeysDDL...)
	root := resultsHome(t)
	sqlite := "sqlite://" + chinookFixture(t)
	for _, tc := range []struct {
		id, stdin string
		argv      []string
	}{
		{"from", "", []string{"--from", "Invoice"}},
		{"join", invoiceCustomerTugQL, []string{"-f", "-"}},
	} {
		_, stderr, code := runQuery(t, tc.stdin, append([]string{"--db", "env:" + realPgScanVar, "--no-policies", "--result-id", "pg-" + tc.id}, tc.argv...)...)
		require.Zero(t, code, stderr)
		_, stderr, code = runQuery(t, tc.stdin, append([]string{"--db", sqlite, "--no-policies", "--result-id", "lite-" + tc.id}, tc.argv...)...)
		require.Zero(t, code, stderr)
		pg, text := readDescribed(t, root, "pg-"+tc.id)
		lite, _ := readDescribed(t, root, "lite-"+tc.id)
		assert.NotContains(t, text, server.password)
		assert.NotContains(t, text, server.user)
		assert.NotContains(t, text, server.host+":")
		assert.NotContains(t, text, realPgScanVar)
		require.Len(t, pg.Participants, len(lite.Participants), tc.id)
		for i := range pg.Participants {
			assert.Equal(t, lite.Participants[i].Relation, pg.Participants[i].Relation)
			assert.Equal(t, lite.Participants[i].Role, pg.Participants[i].Role)
			assert.Equal(t, "public", pg.Participants[i].Schema)
		}
		want := relatedLines(lite)
		var got []string
		for _, related := range pg.Related {
			var fields string
			for _, field := range related.Fields {
				fields = field.SourceField + "=" + field.TargetField
			}
			got = append(got, related.FromInstance+" "+related.Direction+" main."+related.Target.Relation+" "+related.Cardinality+" "+fields)
		}
		assert.Equal(t, want, got, tc.id)
	}

	app, closeFn, err := loadChatJoinApplication(context.Background(), "env:"+realPgScanVar, secureread.NewExecutor(secureread.Session{Unrestricted: true}), true)
	require.NoError(t, err)
	defer closeFn()
	candidates, err := chat.DiscoverJoinCandidates([]byte("from: {name: Invoice}\n"), app.Snapshot)
	require.NoError(t, err)
	var targets []string
	for _, candidate := range candidates {
		targets = append(targets, candidate.Direction+" "+candidate.Target.Relation)
	}
	assert.ElementsMatch(t, []string{"outgoing Customer", "incoming InvoiceLine"}, targets)
}
