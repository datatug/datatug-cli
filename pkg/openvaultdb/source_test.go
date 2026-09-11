package openvaultdb

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
	"github.com/dal-go/dalgo/dtql"
)

func TestSourceDescriptorFailsClosed(t *testing.T) {
	t.Setenv("ACL24_TOKEN", "credential")
	for _, body := range []string{`{}`, `{"tokenEnv":"ACL24_TOKEN","principalId":"alice","baseUrl":"https://vault.test","databaseId":"crm","unknown":true}`, `{"baseUrl":"https://vault.test","baseUrl":"https://other.test"}`, strings.Repeat(" ", 65537), `{"tokenEnv":"MISSING_ACL24_TOKEN","principalId":"alice","baseUrl":"https://vault.test","databaseId":"crm"}`} {
		path := filepath.Join(t.TempDir(), "connection.json")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenSource(path); err == nil {
			t.Fatal("invalid descriptor accepted")
		}
	}
}
func TestSourceRejectsMalformedRecordsAndPreservesKeys(t *testing.T) {
	query, err := dtql.Deserialize([]byte("from: {name: customers}\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		body string
		ok   bool
	}{{`{"records":[{"key":"customers/a%2Fb","data":{"name":"A"}}]}`, true}, {`{"records":[{"key":"other/1","data":{}}]}`, false}, {`{"records":null}`, false}, {`{"other":[]}`, false}, {`{"records":[{"key":"customers/%broken"}]}`, false}} {
		t.Run(tc.body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			source := &source{target: Target{BaseURL: server.URL, DatabaseID: "crm", Token: "credential"}, principalID: "alice"}
			if _, err := source.ExecuteQueryToRecordsReader(context.Background(), query); !errors.Is(err, access.ErrAccessDenied) {
				t.Fatalf("missing principal=%v", err)
			}
			reader, err := source.ExecuteQueryToRecordsReader(access.WithPrincipal(context.Background(), access.Principal{ID: "alice"}), query)
			if (err == nil) != tc.ok {
				t.Fatalf("result=%v", err)
			}
			if tc.ok {
				rec, err := reader.Next()
				if err != nil || rec.Key().ID != "a/b" {
					t.Fatalf("key=%v err=%v", rec, err)
				}
				if err = reader.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err = reader.Next(); !errors.Is(err, dal.ErrNoMoreRecords) {
					t.Fatal(err)
				}
			}
		})
	}
}
func TestSourceConfigDoesNotContainToken(t *testing.T) {
	data, err := json.Marshal(SourceConfig{TokenEnv: "ACL24_TOKEN", PrincipalID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "credential") {
		t.Fatal("credential serialized")
	}
}

func TestSourceCredentialBindings(t *testing.T) {
	t.Setenv("ACL24_TOKEN", "credential")
	t.Setenv("ACL24_TOKEN_BASE_URL", "https://vault.test")
	t.Setenv("ACL24_TOKEN_PRINCIPAL_ID", "alice")
	for _, tc := range []struct {
		url, principal string
		ok             bool
	}{
		{"https://vault.test", "alice", true},
		{"https://attacker.test", "alice", false},
		{"https://vault.test", "bob", false},
	} {
		body, err := json.Marshal(SourceConfig{BaseURL: tc.url, DatabaseID: "crm", TokenEnv: "ACL24_TOKEN", PrincipalID: tc.principal})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "connection.json")
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		db, err := OpenSource(path)
		if (err == nil) != tc.ok {
			t.Fatalf("binding %s/%s: %v", tc.url, tc.principal, err)
		}
		_ = db
	}
}
