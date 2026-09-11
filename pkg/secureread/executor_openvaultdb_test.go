package secureread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/openvaultdb"
	"github.com/openvaultdb/openvaultdb-go/pkg/auth"
	"github.com/openvaultdb/openvaultdb-go/pkg/core"
	"github.com/openvaultdb/openvaultdb-go/pkg/mount"
	ovserver "github.com/openvaultdb/openvaultdb-go/pkg/server"
)

// Runs the actual DataTug executor over HTTP to the real OVDB server and both
// storage engines. The existing fixed local session remains the upper ACL layer.
func TestOpenVaultDBLayeredDTQL(t *testing.T) {
	for _, engine := range []string{"sqlite", "ingitdb"} {
		t.Run(engine, func(t *testing.T) {
			dir := t.TempDir()
			write := func(path, text string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			storage := "data"
			if engine == "sqlite" {
				storage = "data.sqlite"
			}
			manifest := fmt.Sprintf("database: {id: crm, schema_mode: strict}\nstorage: {engine: %s, path: %s}\nschemas:\n  collections:\n    customers:\n      fields:\n        name: {type: string}\n        tenant: {type: string}\n        country: {type: string}\n", engine, storage)
			path := filepath.Join(dir, "db.yaml")
			write(path, manifest)
			db, err := mount.File(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range []struct{ id, tenant, country string }{{"1", "B", "IE"}, {"2", "A", "US"}, {"3", "A", "IE"}} {
				_, err = db.Apply(context.Background(), []core.Op{{Op: "insert", Key: record.NewKeyWithID("customers", row.id), Data: map[string]any{"name": "Customer " + row.id, "tenant": row.tenant, "country": row.country}}}, "seed")
				if err != nil {
					t.Fatal(err)
				}
			}
			policy := func(name, field, value string) string {
				return fmt.Sprintf(`apiVersion: dtql.org/access/v1
kind: AccessPolicy
metadata: {name: %s}
target: {database: crm}
composition: dalgo-hierarchical-v1
default: deny
ruleSets:
  read:
    - path: /customers
      rules:
        - id: visible
          effect: allow
          operations: [query, get]
          where: {op: '==', left: {field: %s}, right: {value: %s}}
          fields: [id, name]
bindings: {roles: {reader: [read]}}
`, name, field, value)
			}
			write(filepath.Join(dir, "upper.yaml"), policy("ovdb", "country", "IE"))
			write(path, manifest+"acl: {enabled: true, policies: [upper.yaml]}\n")
			if engine == "ingitdb" {
				root := filepath.Join(dir, "data", ".ingitdb", "access")
				write(filepath.Join(root, "lower.yaml"), policy("ingitdb", "tenant", "A"))
				write(filepath.Join(root, "manifest.yaml"), "enabled: true\ndatabase: crm\npolicies: [lower.yaml]\n")
			}
			db, err = mount.File(path)
			if err != nil {
				t.Fatal(err)
			}
			store, err := auth.OpenStore(filepath.Join(dir, "auth.json"))
			if err != nil {
				t.Fatal(err)
			}
			token := "fixture-scoped-token"
			err = store.CreateGrant(&auth.Grant{PrincipalID: "alice", DatabaseID: "crm", Capabilities: []auth.Capability{{Action: auth.CapRecordsRead, Collection: "customers"}}}, token)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(ovserver.New("test", map[string]*core.Database{"crm": db}, ovserver.WithAuth(&auth.Config{OwnerToken: "fixture-owner", Store: store}), ovserver.WithPrincipalResolver(func(context.Context, *auth.Principal) (access.Principal, error) {
				return access.Principal{ID: "alice", Roles: []string{"reader"}}, nil
			})).Handler())
			defer server.Close()
			t.Setenv("ACL24_OVDB_TOKEN", token)
			t.Setenv("ACL24_OVDB_TOKEN_BASE_URL", server.URL)
			t.Setenv("ACL24_OVDB_TOKEN_PRINCIPAL_ID", "alice")
			descriptor, _ := json.Marshal(openvaultdb.SourceConfig{BaseURL: server.URL, DatabaseID: "crm", TokenEnv: "ACL24_OVDB_TOKEN", PrincipalID: "alice"})
			connection := filepath.Join(dir, "connection.json")
			write(connection, string(descriptor))
			sourceURL := "openvaultdb://" + connection
			executor := NewExecutor(aliceSession(t, permissivePolicy))
			query := "from: {name: customers}\ncolumns: [{field: name}]\norderBy: [{field: name}]\nlimit: 10\n"
			result, err := executor.RunDTQL(context.Background(), sourceURL, []byte(query), nil)
			if err != nil {
				t.Fatal(err)
			}
			want := 2
			if engine == "ingitdb" {
				want = 1
			}
			if len(result.Rows) != want {
				t.Fatalf("rows=%+v, want %d", result.Rows, want)
			}
			for _, row := range result.Rows {
				if _, ok := row.Data["tenant"]; ok {
					t.Fatal("owner predicate field leaked")
				}
				if row.Key == "2" {
					t.Fatal("OVDB restriction bypassed")
				}
			}
			denied := NewExecutor(aliceSession(t, productsOnlyPolicy))
			if _, err = denied.RunDTQL(context.Background(), sourceURL, []byte(query), nil); !errors.Is(err, access.ErrAccessDenied) {
				t.Fatalf("local denial=%v", err)
			}
			if _, err = executor.RunDTQL(context.Background(), sourceURL, []byte(strings.Replace(query, "field: name", "field: tenant", 1)), nil); !errors.Is(err, access.ErrAccessDenied) {
				t.Fatalf("lower column denial=%v", err)
			}
			wrong := aliceSession(t, permissivePolicy)
			wrong.Principal.ID = "bob"
			if _, err = NewExecutor(wrong).RunDTQL(context.Background(), sourceURL, []byte(query), nil); !errors.Is(err, access.ErrAccessDenied) {
				t.Fatalf("credential principal mismatch=%v", err)
			}
			// Revocation must take effect on the next operation; no client cached allow.
			t.Setenv("ACL24_OVDB_TOKEN", "revoked")
			if _, err = executor.RunDTQL(context.Background(), sourceURL, []byte(query), nil); !errors.Is(err, access.ErrAccessDenied) {
				t.Fatalf("revoked token=%v", err)
			}
		})
	}
}
