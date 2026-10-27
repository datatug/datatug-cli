package dbcopy

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2couchdb"
)

func TestParseCouchDBSource(t *testing.T) {
	t.Parallel()
	accepted := []struct {
		source   string
		scheme   string
		host     string
		database string
	}{
		{"couchdb://couch.example.com:6984/orders", CouchDBScheme, "couch.example.com:6984", "orders"},
		{"couchdb+https://couch.example.com/orders", CouchDBHTTPSScheme, "couch.example.com", "orders"},
		{"couchdb+http://127.0.0.1:5984/orders", CouchDBHTTPScheme, "127.0.0.1:5984", "orders"},
		{"couchdb+http://localhost:5984/orders", CouchDBHTTPScheme, "localhost:5984", "orders"},
		{"couchdb+http://[::1]:5984/orders", CouchDBHTTPScheme, "[::1]:5984", "orders"},
		{"COUCHDB://couch.example.com:6984/orders", CouchDBScheme, "couch.example.com:6984", "orders"},
	}
	for _, tc := range accepted {
		t.Run(tc.source, func(t *testing.T) {
			ref, err := Parse(tc.source)
			if err != nil {
				t.Fatal(err)
			}
			if ref.Scheme != tc.scheme || ref.CouchDBHost != tc.host || ref.CouchDBDatabase != tc.database {
				t.Fatalf("ref = %#v", ref)
			}
			if ref.Path != tc.host+"/"+tc.database {
				t.Fatalf("Path = %q", ref.Path)
			}
			if got := ref.CouchDBSourceURL(); got != tc.scheme+"://"+tc.host+"/"+tc.database {
				t.Fatalf("CouchDBSourceURL = %q", got)
			}
			if shown := ref.Display(); strings.Contains(shown, "@") || !strings.HasPrefix(shown, tc.scheme+"://") {
				t.Fatalf("display = %q", shown)
			}
		})
	}
}

func TestParseCouchDBSourceRefusals(t *testing.T) {
	t.Parallel()
	secret := "s3cr3t-CouchDB"
	for _, tc := range []struct {
		source string
		want   string
	}{
		{"couchdb://user:" + secret + "@couch.example.com/orders", "credentials are not supported"},
		{"couchdb://token@couch.example.com/orders", "credentials are not supported"},
		{"couchdb://couch.example.com/orders?password=" + secret, "takes no query or fragment"},
		{"couchdb://couch.example.com/orders#" + secret, "takes no query or fragment"},
		{"couchdb://couch.example.com/", "exactly one database"},
		{"couchdb://couch.example.com", "exactly one database"},
		{"couchdb://couch.example.com/a/b", "database name must be one CouchDB database name"},
		{"couchdb://couch.example.com/Orders", "database name must be one CouchDB database name"},
		{"couchdb://couch.example.com/or%64ers", "must not be percent-encoded"},
		{"couchdb+http://couch.example.com/orders", "allowed only for a loopback endpoint"},
		{"couchdb+http://10.0.0.5/orders", "allowed only for a loopback endpoint"},
		{"couchdb:///orders", "expected couchdb://HOST/DATABASE"},
		{"couchdb://couch.example.com:notaport/orders", "invalid CouchDB URL"},
	} {
		_, err := Parse(tc.source)
		if err == nil {
			t.Errorf("%s was accepted", tc.source)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q lacks %q", tc.source, err, tc.want)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: error leaks the secret: %q", tc.source, err)
		}
	}
}

func TestCouchDBHeadersFromEnvironment(t *testing.T) {
	t.Parallel()
	basic := func(user, password string) map[string]string {
		return map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password))}
	}
	env := func(values map[string]string) func(string) (string, bool) {
		return func(name string) (string, bool) {
			value, ok := values[name]
			return value, ok
		}
	}
	for _, tc := range []struct {
		name    string
		values  map[string]string
		want    map[string]string
		wantErr error
	}{
		{"no credentials", nil, nil, nil},
		{"basic", map[string]string{CouchDBUsernameEnv: "reader", CouchDBPasswordEnv: "pw-" + "only"}, basic("reader", "pw-only"), nil},
		{"bearer", map[string]string{CouchDBBearerEnv: "tok-" + "only"}, map[string]string{"Authorization": "Bearer tok-only"}, nil},
		{"empty variables are unset", map[string]string{CouchDBUsernameEnv: "", CouchDBPasswordEnv: "", CouchDBBearerEnv: ""}, nil, nil},
		{"user alone", map[string]string{CouchDBUsernameEnv: "reader"}, nil, errCouchDBIncompleteBasic},
		{"password alone", map[string]string{CouchDBPasswordEnv: "hidden"}, nil, errCouchDBIncompleteBasic},
		{"both forms", map[string]string{CouchDBUsernameEnv: "reader", CouchDBPasswordEnv: "hidden", CouchDBBearerEnv: "token"}, nil, errCouchDBMixedCredentials},
		{"bearer and half basic", map[string]string{CouchDBUsernameEnv: "reader", CouchDBBearerEnv: "token"}, nil, errCouchDBMixedCredentials},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := couchDBHeaders(env(tc.values))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("headers = %#v, want %#v", got, tc.want)
			}
			for name, want := range tc.want {
				if got[name] != want {
					t.Fatalf("headers = %#v, want %#v", got, tc.want)
				}
			}
		})
	}
}

func TestCouchDBHeadersReadTheEnvironmentPerRequest(t *testing.T) {
	// Not parallel: it reads and writes the process environment.
	unsetCouchDBEnv(t)
	ref, err := Parse("couchdb+http://127.0.0.1:5984/orders")
	if err != nil {
		t.Fatal(err)
	}
	original := newCouchDBDatabase
	t.Cleanup(func() { newCouchDBDatabase = original })
	var cfg dalgo2couchdb.Config
	newCouchDBDatabase = func(_ context.Context, got dalgo2couchdb.Config) (dal.DB, error) {
		cfg = got
		return nil, nil
	}
	t.Setenv(CouchDBUsernameEnv, "reader")
	t.Setenv(CouchDBPasswordEnv, "pw")
	if _, err = ref.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	first, err := cfg.Headers(context.Background())
	if err != nil || first["Authorization"] == "" {
		t.Fatalf("first headers = %#v, %v", first, err)
	}
	t.Setenv(CouchDBUsernameEnv, "reader2")
	t.Setenv(CouchDBPasswordEnv, "pw2")
	second, err := cfg.Headers(context.Background())
	if err != nil || second["Authorization"] == first["Authorization"] {
		t.Fatalf("the header did not follow the environment: %#v %#v %v", first, second, err)
	}
	t.Setenv(CouchDBBearerEnv, "token")
	if _, err = cfg.Headers(context.Background()); !errors.Is(err, errCouchDBMixedCredentials) {
		t.Fatalf("a mixed configuration was accepted: %v", err)
	}
}

func TestCouchDBOpenConfigurationAndReadOnly(t *testing.T) {
	unsetCouchDBEnv(t)
	ref, err := Parse("couchdb+http://127.0.0.1:5984/orders")
	if err != nil {
		t.Fatal(err)
	}
	original := newCouchDBDatabase
	t.Cleanup(func() { newCouchDBDatabase = original })
	var cfg dalgo2couchdb.Config
	opened := 0
	newCouchDBDatabase = func(_ context.Context, got dalgo2couchdb.Config) (dal.DB, error) {
		opened++
		cfg = got
		return nil, nil
	}
	if _, err = ref.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	if opened != 1 || cfg.DatabaseURL != "http://127.0.0.1:5984/orders" || cfg.Collection != "orders" || !cfg.AllowLoopbackHTTP {
		t.Fatalf("opened %d with %#v", opened, cfg)
	}
	if cfg.Headers == nil {
		t.Fatal("Headers must be set so credentials are read for every request")
	}
	if _, err = ref.OpenProtected(context.Background()); err != nil || opened != 2 {
		t.Fatalf("OpenProtected: opened %d, %v", opened, err)
	}
	if _, err = ref.OpenForWrite(context.Background()); err == nil || opened != 2 {
		t.Fatalf("OpenForWrite must be refused before any driver call: opened %d, err %v", opened, err)
	} else if !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("OpenForWrite error = %q", err)
	}
}

func TestCouchDBOpenRefusesAnAmbiguousCredentialConfiguration(t *testing.T) {
	unsetCouchDBEnv(t)
	t.Setenv(CouchDBUsernameEnv, "reader")
	t.Setenv(CouchDBBearerEnv, "token")
	ref, err := Parse("couchdb+http://127.0.0.1:5984/orders")
	if err != nil {
		t.Fatal(err)
	}
	original := newCouchDBDatabase
	t.Cleanup(func() { newCouchDBDatabase = original })
	newCouchDBDatabase = func(context.Context, dalgo2couchdb.Config) (dal.DB, error) {
		t.Fatal("the driver must not be called with an ambiguous credential configuration")
		return nil, nil
	}
	_, err = ref.Open(context.Background())
	if !errors.Is(err, errCouchDBMixedCredentials) {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "token") {
		t.Fatalf("the error shows a credential: %q", err)
	}
}

func TestOpenSchemaScanCouchDBUsesTheGenericReader(t *testing.T) {
	unsetCouchDBEnv(t)
	ref, err := Parse("couchdb+http://127.0.0.1:1/orders")
	if err != nil {
		t.Fatal(err)
	}
	scan, err := ref.OpenSchemaScan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	collections, err := scan.ListCollections(context.Background(), nil)
	if err != nil || len(collections) != 1 || collections[0].Name() != "orders" {
		t.Fatalf("collections = %#v, %v", collections, err)
	}
	if err = scan.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCouchDBOpenFailureTextIsSafe(t *testing.T) {
	t.Parallel()
	ref := BackendRef{Scheme: CouchDBScheme, Path: "couch.example.com/orders", CouchDBHost: "couch.example.com", CouchDBDatabase: "orders", Raw: "couchdb://couch.example.com/orders"}
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"unauthorized", &dalgo2couchdb.HTTPError{StatusCode: 401}, "refused the request (HTTP 401)"},
		{"forbidden", &dalgo2couchdb.HTTPError{StatusCode: 403}, "refused the request (HTTP 403)"},
		{"not found", &dalgo2couchdb.HTTPError{StatusCode: 404}, "database was not found"},
		{"index", dalgo2couchdb.ErrIndexRequired, "usable CouchDB Mango index"},
		{"header provider", dalgo2couchdb.ErrHeaderProvider, "credential environment variables"},
		{"response", dalgo2couchdb.ErrResponseTooLarge, "configured bound"},
		{"request", dalgo2couchdb.ErrRequestFailed, "CouchDB request failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shown := ref.OpenFailure(tc.err).Error()
			if !strings.Contains(shown, tc.want) {
				t.Fatalf("OpenFailure = %q, want %q", shown, tc.want)
			}
			if strings.Contains(shown, "Authorization") || strings.Contains(shown, "token") || strings.Contains(shown, "password") {
				t.Fatalf("OpenFailure shows a credential: %q", shown)
			}
		})
	}
}

// unsetCouchDBEnv clears the three credential variables for the test and puts
// back whatever was there, so a developer's own environment cannot change the
// result.
func unsetCouchDBEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{CouchDBUsernameEnv, CouchDBPasswordEnv, CouchDBBearerEnv} {
		old, ok := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
		name := name
		t.Cleanup(func() {
			if ok {
				_ = os.Setenv(name, old)
			}
		})
	}
}

func TestOpenSchemaScanCouchDBReportsAnOpenFailure(t *testing.T) {
	unsetCouchDBEnv(t)
	t.Setenv(CouchDBUsernameEnv, "reader")
	t.Setenv(CouchDBPasswordEnv, "pw")
	t.Setenv(CouchDBBearerEnv, "token")
	ref, err := Parse("couchdb+http://127.0.0.1:5984/orders")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ref.OpenSchemaScan(context.Background()); !errors.Is(err, errCouchDBMixedCredentials) {
		t.Fatalf("OpenSchemaScan = %v, want the mixed-credential refusal", err)
	}
}
