package dbcopy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo2typesense"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTypesenseSource(t *testing.T) {
	ref, err := Parse("typesense://search.example.com:8108/books")
	require.NoError(t, err)
	assert.Equal(t, TypesenseScheme, ref.Scheme)
	assert.Equal(t, "search.example.com:8108", ref.TypesenseHost)
	assert.Equal(t, "books", ref.TypesenseCollection)
	assert.False(t, ref.TypesenseAllowLoopbackHTTP, "typesense:// is HTTPS")
	assert.Equal(t, "search.example.com:8108/books", ref.Path)
	assert.Equal(t, "typesense://search.example.com:8108/books", ref.TypesenseSourceURL())
	assert.Equal(t, "typesense://search.example.com:8108/books", ref.String())

	ref, err = Parse("typesense+https://search.example.com/books")
	require.NoError(t, err)
	assert.Equal(t, TypesenseHTTPSScheme, ref.Scheme)
	assert.False(t, ref.TypesenseAllowLoopbackHTTP)

	for _, loopback := range []string{"127.0.0.1:8108", "localhost:8108", "[::1]:8108"} {
		ref, err = Parse("typesense+http://" + loopback + "/books")
		require.NoError(t, err, loopback)
		assert.True(t, ref.TypesenseAllowLoopbackHTTP, loopback)
	}

	ref, err = ParseWithEnv("env:DATATUG_TEST_TYPESENSE", func(name string) (string, bool) {
		require.Equal(t, "DATATUG_TEST_TYPESENSE", name)
		return "typesense+http://127.0.0.1:8108/books", true
	})
	require.NoError(t, err)
	assert.Equal(t, "env:DATATUG_TEST_TYPESENSE", ref.Raw)
	assert.Equal(t, "books", ref.TypesenseCollection)
}

func TestParseTypesenseSourceRefusesAnUnusableURL(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"typesense://search.example.com", "names no collection"},
		{"typesense://search.example.com/", "names no collection"},
		{"typesense://search.example.com/books/", "trailing slash"},
		{"typesense://search.example.com/team/books", "exactly one collection"},
		{"typesense://search.example.com/collections/books", "exactly one collection"},
		{"typesense://search.example.com/books?x=1", "query or fragment"},
		{"typesense://search.example.com/books#frag", "query or fragment"},
		{"typesense://alice:secret-key@search.example.com/books", "credentials are not supported"},
		{"typesense://search.example.com/bo%6fks", "percent-encoded"},
		{"typesense://search.example.com/-books", "plain Typesense collection name"},
		{"typesense://search.example.com/books%2Fmore", "percent-encoded"},
		{"typesense+http://search.example.com/books", "loopback"},
		{"typesense://", "expected typesense://HOST/COLLECTION"},
		{"typesense://:8108/books", "names no usable host"},
		{"typesense://search.example.com/books\\more", "one plain path segment"},
	} {
		_, err := Parse(tc.raw)
		require.Error(t, err, tc.raw)
		assert.Contains(t, err.Error(), tc.want, tc.raw)
		assert.NotContains(t, err.Error(), "secret-key", tc.raw)
	}
}

func TestTypesenseSourceURLIsEmptyForAnIncompleteRef(t *testing.T) {
	for name, ref := range map[string]BackendRef{
		"no scheme":     {TypesenseHost: "search.example.com:8108", TypesenseCollection: "books"},
		"no host":       {Scheme: TypesenseScheme, TypesenseCollection: "books"},
		"no collection": {Scheme: TypesenseScheme, TypesenseHost: "search.example.com:8108"},
	} {
		assert.Empty(t, ref.TypesenseSourceURL(), name)
	}
}

func TestOpenTypesenseRefusesAnIncompleteRef(t *testing.T) {
	db, err := openTypesense(context.Background(), BackendRef{Scheme: TypesenseScheme}, openMode{})
	require.Error(t, err)
	assert.Nil(t, db)
	assert.ErrorIs(t, err, errUnsupportedBackend)
}

func TestTypesenseHeadersReadTheEnvironmentAndRefuseAnUnsendableKey(t *testing.T) {
	lookup := func(name string) (string, bool) {
		if name == TypesenseAPIKeyEnv {
			return "scoped-key", true
		}
		return "", false
	}
	headers, err := typesenseHeaders(lookup)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"X-TYPESENSE-API-KEY": "scoped-key"}, headers)

	for name, lookup := range map[string]func(string) (string, bool){
		"unset": func(string) (string, bool) { return "", false },
		"empty": func(string) (string, bool) { return "", true },
		"with CRLF": func(string) (string, bool) {
			return "scoped-key\r\nX-Injected: yes", true
		},
	} {
		_, err := typesenseHeaders(lookup)
		require.Error(t, err, name)
		assert.True(t, IsTypesenseCredentialError(err), name)
		assert.Contains(t, err.Error(), TypesenseAPIKeyEnv, name)
		assert.NotContains(t, err.Error(), "scoped-key", name)
		assert.NotContains(t, err.Error(), "X-Injected", name)
		assert.NotContains(t, err.Error(), "search.example.com", name)
	}

	assert.Error(t, CheckTypesenseCredentials(func(string) (string, bool) { return "", false }))
	assert.NoError(t, CheckTypesenseCredentials(lookup))
}

func TestOpenTypesenseRefusesAWriteBeforeAnyRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()
	t.Setenv(TypesenseAPIKeyEnv, "scoped-key")

	ref, err := Parse("typesense+http://" + server.Listener.Addr().String() + "/books")
	require.NoError(t, err)
	db, err := ref.open(context.Background(), openMode{forWrite: true})
	require.Error(t, err)
	assert.Nil(t, db)
	assert.Contains(t, err.Error(), "read-only")
	assert.Zero(t, requests, "a refused destination never reaches the server")
}

func TestOpenTypesenseRefusesAMissingKeyBeforeAnyRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()
	t.Setenv(TypesenseAPIKeyEnv, "")

	ref, err := Parse("typesense+http://" + server.Listener.Addr().String() + "/books")
	require.NoError(t, err)
	db, err := ref.open(context.Background(), openMode{})
	require.Error(t, err)
	assert.Nil(t, db)
	assert.Contains(t, err.Error(), TypesenseAPIKeyEnv)
	assert.True(t, IsTypesenseCredentialError(err))
	assert.Zero(t, requests, "a credential check is not a request")
}

// TestTypesenseReadsTheKeyForEveryRequest proves the credential is not captured
// at open time: the same handle sends the value the environment holds when each
// request is made. It is why a rotated scoped key needs no restart.
func TestTypesenseReadsTheKeyForEveryRequest(t *testing.T) {
	var (
		mu   sync.Mutex
		seen []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("X-TYPESENSE-API-KEY"))
		mu.Unlock()
		_, _ = w.Write([]byte(`[{"name":"books","fields":[{"name":"title","type":"string"}],"num_documents":1}]`))
	}))
	defer server.Close()

	t.Setenv(TypesenseAPIKeyEnv, "key-one")
	ref, err := Parse("typesense+http://" + server.Listener.Addr().String() + "/books")
	require.NoError(t, err)
	db, err := ref.OpenProtected(context.Background())
	require.NoError(t, err)
	defer CloseSource(db)

	reader, ok := dal.As[dbschema.SchemaReader](db)
	require.True(t, ok)
	_, err = reader.ListCollections(context.Background(), nil)
	require.NoError(t, err)

	t.Setenv(TypesenseAPIKeyEnv, "key-two")
	_, err = reader.ListCollections(context.Background(), nil)
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"key-one", "key-two"}, seen)
}

func TestTypesenseFailureReasonIsFixedAndRedacted(t *testing.T) {
	ref, err := Parse("typesense://search.example.com:8108/books")
	require.NoError(t, err)

	missing := &typesenseCredentialError{name: TypesenseAPIKeyEnv, reason: typesenseCredentialMissing}
	assert.Equal(t, "Typesense credentials: "+TypesenseAPIKeyEnv+" is not set: it must hold a Typesense API key with a scoped read grant", TypesenseFailureReason(missing))
	assert.NotContains(t, TypesenseFailureReason(missing), "search.example.com")

	permission := &dalgo2typesense.HTTPError{StatusCode: http.StatusUnauthorized}
	reason := TypesenseFailureReason(permission)
	assert.Contains(t, reason, "refused the credential")
	assert.Contains(t, reason, TypesenseAPIKeyEnv)
	assert.NotContains(t, reason, "search.example.com")
	assert.NotContains(t, reason, "books")

	metadata := TypesenseFailureReason(dalgo2typesense.ErrMetadataForbidden)
	assert.Contains(t, metadata, "collections:get")
	assert.True(t, IsTypesenseMetadataForbidden(dalgo2typesense.ErrMetadataForbidden))

	assert.Equal(t, "the read was cancelled", TypesenseFailureReason(context.Canceled))
	assert.Contains(t, TypesenseFailureReason(dal.ErrNotSupported), "does not support")
	assert.Contains(t, TypesenseFailureReason(dalgo2typesense.ErrSemanticMismatch), "narrow the filter")
	assert.Contains(t, TypesenseFailureReason(dalgo2typesense.ErrTooManyPages), "budget")
	assert.Equal(t, "", TypesenseFailureReason(nil))

	// A transport error with no adapter sentinel falls through to the package's
	// fixed cause list, and the safe sentence still lets errors.Is see the cause.
	refused := ref.OpenFailure(fmt.Errorf("dial tcp search.example.com:8108: %w", syscall.ECONNREFUSED))
	assert.Contains(t, refused.Error(), "connection was refused")
	assert.True(t, errors.Is(refused, syscall.ECONNREFUSED))
	assert.NotContains(t, refused.Error(), "secret-key")

	// The adapter replaces a transport failure with the detail-free
	// ErrRequestFailed, so a real Typesense read reports that fixed
	// classification rather than guessing at the cause. A wrapped secret never
	// reaches the sentence.
	failed := ref.OpenFailure(fmt.Errorf("dial tcp search.example.com:8108: %w", dalgo2typesense.ErrRequestFailed))
	assert.Contains(t, failed.Error(), "could not be reached or the connection failed")
	assert.True(t, errors.Is(failed, dalgo2typesense.ErrRequestFailed))
	assert.NotContains(t, failed.Error(), "secret")
	assert.NotContains(t, failed.Error(), "secret-key")
}

func TestTypesenseFailureReasonCoversEveryAdapterClassification(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"semantic mismatch":  {dalgo2typesense.ErrSemanticMismatch, "narrow the filter"},
		"permission denied":  {dalgo2typesense.ErrPermissionDenied, "refused the credential"},
		"request too large":  {dalgo2typesense.ErrRequestTooLarge, "bounded request"},
		"response too large": {dalgo2typesense.ErrResponseTooLarge, "bounded request"},
		"row too large":      {dalgo2typesense.ErrRowTooLarge, "bounded request"},
		"too many rows":      {dalgo2typesense.ErrTooManyRows, "bounded request"},
		"too many pages":     {dalgo2typesense.ErrTooManyPages, "bounded request"},
		"partial result":     {dalgo2typesense.ErrPartialResult, "fewer hits than its own found count"},
		"duplicate id":       {dalgo2typesense.ErrDuplicateID, "repeated a document id"},
		"redirect refused":   {dalgo2typesense.ErrRedirectRefused, "attempted a redirect"},
		"malformed response": {dalgo2typesense.ErrMalformedResponse, "does not recognise"},
		"closed handle":      {dalgo2typesense.ErrClosed, "handle is closed"},
		"no collection":      {dalgo2typesense.ErrCollectionNotConfigured, "names no collection"},
		"request failed":     {dalgo2typesense.ErrRequestFailed, "could not be reached"},
		"unsafe value":       {dalgo2typesense.ErrUnsafeValue, "refused a value"},
		"deadline exceeded":  {context.DeadlineExceeded, "exceeded its deadline"},
	} {
		reason := TypesenseFailureReason(tc.err)
		assert.Contains(t, reason, tc.want, name)
		assert.NotContains(t, reason, "127.0.0.1", name)
	}
}

func TestReadTypesenseDescriptorReadsOnlyTheAllowedVariableName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "books.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"dsnEnv":"DATATUG_TYPESENSE_BOOKS"}`), 0o600))
	lookup := func(name string) (string, bool) {
		if name == DescriptorEnvAllowList {
			return "DATATUG_TYPESENSE_BOOKS", true
		}
		return "", false
	}
	descriptor, err := readTypesenseDescriptor(path, lookup)
	require.NoError(t, err)
	assert.Equal(t, ConnectionDescriptor{DSNEnv: "DATATUG_TYPESENSE_BOOKS"}, descriptor)

	t.Setenv(DescriptorEnvAllowList, "DATATUG_TYPESENSE_BOOKS")
	descriptor, err = ReadTypesenseDescriptor(path)
	require.NoError(t, err)
	assert.Equal(t, "DATATUG_TYPESENSE_BOOKS", descriptor.DSNEnv)

	// A descriptor that names a variable the operator did not set aside is
	// refused, and the variable is named but no value is.
	foreign := filepath.Join(dir, "foreign.json")
	require.NoError(t, os.WriteFile(foreign, []byte(`{"dsnEnv":"PROD_TYPESENSE_URL"}`), 0o600))
	t.Setenv(DescriptorEnvAllowList, "")
	_, err = ReadTypesenseDescriptor(foreign)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PROD_TYPESENSE_URL")
	assert.Contains(t, err.Error(), DescriptorEnvAllowList)

	// A descriptor that is not there fails with the path the server-owned reader
	// would have opened, never with a value.
	_, err = ReadTypesenseDescriptor(filepath.Join(dir, "missing.json"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open Typesense connection descriptor")
	assert.NotContains(t, err.Error(), "secret")
}

func TestOpenSchemaScanTypesenseUsesTheAdapterSchemaReader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/collections":
			_, _ = w.Write([]byte(`[{"name":"books","fields":[{"name":"title","type":"string"}],"num_documents":1}]`))
		case "/collections/books":
			_, _ = w.Write([]byte(`{"name":"books","fields":[{"name":"title","type":"string"},{"name":"tags","type":"string[]"}],"num_documents":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv(TypesenseAPIKeyEnv, "scoped-key")

	ref, err := Parse("typesense+http://" + server.Listener.Addr().String() + "/books")
	require.NoError(t, err)
	scanDB, err := ref.OpenSchemaScan(context.Background())
	require.NoError(t, err)
	defer func() { _ = scanDB.Close() }()

	collections, err := scanDB.ListCollections(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, collections, 1)
	assert.Equal(t, "books", collections[0].Name())
	definition, err := scanDB.DescribeCollection(context.Background(), &collections[0])
	require.NoError(t, err)
	assert.Equal(t, []dal.FieldName{"id"}, definition.PrimaryKey)
	types := map[string]string{}
	for _, column := range definition.SourceDefinition.Columns {
		types[column.Name] = column.DeclaredType
	}
	assert.Equal(t, "string", types["id"])
	assert.Equal(t, "string[]", types["tags"])
}

func TestOpenSchemaScanTypesenseReportsAMissingKeyBeforeAnyRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()
	t.Setenv(TypesenseAPIKeyEnv, "")
	ref, err := Parse("typesense+http://" + server.Listener.Addr().String() + "/books")
	require.NoError(t, err)
	scanDB, err := ref.OpenSchemaScan(context.Background())
	require.Error(t, err)
	assert.Nil(t, scanDB)
	assert.True(t, IsTypesenseCredentialError(err))
	assert.Zero(t, requests)
}

func TestTypesenseDescriptorRoundTripsTheVariableNameOnly(t *testing.T) {
	lookup := func(name string) (string, bool) {
		if name == DescriptorEnvAllowList {
			return "MY_TYPESENSE_URL", true
		}
		return "", false
	}
	descriptor, err := decodeConnectionDescriptor([]byte(`{"dsnEnv":"MY_TYPESENSE_URL"}`), "Typesense")
	require.NoError(t, err)
	assert.Equal(t, ConnectionDescriptor{DSNEnv: "MY_TYPESENSE_URL"}, descriptor)
	require.NoError(t, CheckDescriptorEnvName(descriptor.DSNEnv, lookup))

	_, err = DecodeTypesenseDescriptor([]byte(`{"dsnEnv":"MY_TYPESENSE_URL","apiKey":"secret"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown Typesense connection field")
	assert.NotContains(t, err.Error(), "secret")

	_, err = DecodeTypesenseDescriptor([]byte(`{"dsnEnv":"lower-case"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "environment variable name")
}

func TestTypesenseSchemeIsADisplayableSource(t *testing.T) {
	shown := SourceDisplay("typesense://search.example.com:8108/books")
	assert.Equal(t, "typesense://search.example.com:8108/books", shown)
	ref, err := Parse("typesense://search.example.com:8108/books")
	require.NoError(t, err)
	assert.Equal(t, "typesense://search.example.com:8108/books", ref.Display())
	assert.True(t, IsTypesenseScheme(ref.Scheme))
	assert.True(t, IsTypesenseCollectionName("books_2026"))
	assert.False(t, IsTypesenseCollectionName("../books"))
	assert.Contains(t, strings.Join(SupportedSchemes(), ","), "typesense")
}
