package dbcopy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
)

func writeOpenVaultDBDescriptor(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "connection.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenVaultDBDescriptorFailsClosed(t *testing.T) {
	t.Setenv("OVDB_TOKEN", "s3cr3t-token")
	t.Setenv("OVDB_TOKEN_BASE_URL", "ftp://vault.test")
	t.Setenv("OVDB_TOKEN_PRINCIPAL_ID", "alice")
	for name, body := range map[string]string{
		"empty object":     `{}`,
		"unknown field":    `{"tokenEnv":"OVDB_TOKEN","principalId":"alice","baseUrl":"ftp://vault.test","databaseId":"crm","unknown":true}`,
		"duplicate key":    `{"baseUrl":"https://vault.test","baseUrl":"https://other.test"}`,
		"oversized":        strings.Repeat(" ", maxOpenVaultDBDescriptorBytes+1),
		"wrong type":       `{"baseUrl":5}`,
		"token env equals": `{"tokenEnv":"A=B","principalId":"alice"}`,
		"blank principal":  `{"tokenEnv":"OVDB_TOKEN","principalId":" "}`,
		"unbound token":    `{"tokenEnv":"MISSING_OVDB_TOKEN","principalId":"alice","baseUrl":"ftp://vault.test","databaseId":"crm"}`,
		"invalid target":   `{"tokenEnv":"OVDB_TOKEN","principalId":"alice","baseUrl":"ftp://vault.test","databaseId":"crm"}`,
	} {
		if _, err := OpenOpenVaultDBSource(writeOpenVaultDBDescriptor(t, body)); err == nil {
			t.Errorf("%s: invalid descriptor accepted", name)
		} else if strings.Contains(err.Error(), "s3cr3t-token") {
			t.Errorf("%s: error exposed the token: %v", name, err)
		}
	}
	if _, err := OpenOpenVaultDBSource(filepath.Join(t.TempDir(), "missing.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing descriptor: %v", err)
	}
	if _, err := OpenOpenVaultDBSource(t.TempDir()); err == nil {
		t.Fatal("directory accepted as a descriptor")
	}
}

func TestOpenVaultDBDescriptorDoesNotContainToken(t *testing.T) {
	data, err := json.Marshal(OpenVaultDBDescriptor{TokenEnv: "OVDB_TOKEN", PrincipalID: "alice"})
	if err != nil || strings.Contains(string(data), "s3cr3t-token") {
		t.Fatalf("descriptor=%s err=%v", data, err)
	}
}

func TestOpenVaultDBCredentialBindings(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"records":[{"key":"customers/a%2Fb","data":{"name":"A"}}]}`))
	}))
	defer server.Close()
	t.Setenv("OVDB_TOKEN", "s3cr3t-token")
	t.Setenv("OVDB_TOKEN_BASE_URL", server.URL)
	t.Setenv("OVDB_TOKEN_PRINCIPAL_ID", "alice")
	for _, tc := range []struct {
		url, principal string
		ok             bool
	}{
		{server.URL, "alice", true},
		{"https://attacker.test", "alice", false},
		{server.URL, "bob", false},
	} {
		body, _ := json.Marshal(OpenVaultDBDescriptor{BaseURL: tc.url, DatabaseID: "crm", TokenEnv: "OVDB_TOKEN", PrincipalID: tc.principal})
		db, err := OpenOpenVaultDBSource(writeOpenVaultDBDescriptor(t, string(body)))
		if (err == nil) != tc.ok {
			t.Fatalf("binding %s/%s: %v", tc.url, tc.principal, err)
		}
		if !tc.ok {
			continue
		}
		query := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("customers", ""))).SelectKeysOnly(0)
		if _, err = db.ExecuteQueryToRecordsReader(access.WithPrincipal(context.Background(), access.Principal{ID: "bob"}), query); !errors.Is(err, access.ErrAccessDenied) {
			t.Fatalf("other principal: %v", err)
		}
		reader, err := db.ExecuteQueryToRecordsReader(access.WithPrincipal(context.Background(), access.Principal{ID: "alice"}), query)
		if err != nil {
			t.Fatal(err)
		}
		rec, err := reader.Next()
		if err != nil || rec.Key().ID != "a/b" || authorization != "Bearer s3cr3t-token" {
			t.Fatalf("rec=%v err=%v authorization=%q", rec, err, authorization)
		}
	}
}
