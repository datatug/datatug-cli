package dtlog

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/posthog/posthog-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ensure context is used (deadline exceeded test uses context.DeadlineExceeded)
var _ = context.DeadlineExceeded

// --- helpers ---

func withTempConfig(t *testing.T, content string) (path string, restore func()) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, ".posthog.yaml")
	if content != "" {
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	}
	old := getPosthogConfigFilePath
	getPosthogConfigFilePath = func() string { return p }
	return p, func() { getPosthogConfigFilePath = old }
}

// --- DistinctID ---

func TestDistinctID(t *testing.T) {
	old := posthogDistinctID
	posthogDistinctID = "my-id"
	defer func() { posthogDistinctID = old }()
	assert.Equal(t, "my-id", DistinctID())
}

// --- postInitFlush drain loop ---

func TestPostInitFlush_DrainQueue(t *testing.T) {
	mock := &mockPosthogClient{}

	mu.Lock()
	oldPh := ph
	oldInitialized := initialized
	oldQueue := queue
	oldDistinctID := posthogDistinctID

	ph = nil
	initialized = false
	posthogDistinctID = "flush-test-id"
	queue = []posthog.Message{
		posthog.Capture{Event: "queued-1"},
		posthog.Capture{Event: "queued-2"},
	}
	mu.Unlock()

	defer func() {
		mu.Lock()
		ph = oldPh
		initialized = oldInitialized
		queue = oldQueue
		posthogDistinctID = oldDistinctID
		mu.Unlock()
	}()

	postInitFlush(mock)

	assert.True(t, initialized)
	assert.Nil(t, queue)
	assert.Len(t, mock.enqueued, 2)
}

// --- enqueue: ph == nil ---

func TestEnqueue_NilClient(t *testing.T) {
	mu.Lock()
	oldPh := ph
	oldInitialized := initialized
	ph = nil
	initialized = true
	mu.Unlock()
	defer func() {
		mu.Lock()
		ph = oldPh
		initialized = oldInitialized
		mu.Unlock()
	}()

	// must not panic
	enqueue(posthog.Capture{Event: "noop"})
}

// --- enqueue: Enqueue error ---

type errPosthogClient struct {
	posthog.Client
}

func (e *errPosthogClient) Enqueue(_ posthog.Message) error {
	return errors.New("enqueue error")
}
func (e *errPosthogClient) Close() error { return nil }

func TestEnqueue_ClientError(t *testing.T) {
	errClient := &errPosthogClient{}

	mu.Lock()
	oldPh := ph
	oldInitialized := initialized
	oldDistinctID := posthogDistinctID
	ph = errClient
	initialized = true
	posthogDistinctID = "err-test-id"
	mu.Unlock()
	defer func() {
		mu.Lock()
		ph = oldPh
		initialized = oldInitialized
		posthogDistinctID = oldDistinctID
		mu.Unlock()
	}()

	// must not panic even when Enqueue returns error
	enqueue(posthog.Capture{Event: "fail"})
}

// --- getPostHogClient: empty DistinctID triggers random ID + save ---

func TestGetPostHogClient_EmptyDistinctID(t *testing.T) {
	_, restore := withTempConfig(t, "api_key: test-key\n")
	defer restore()

	oldNew := posthogNewWithConfig
	posthogNewWithConfig = func(apiKey string, config posthog.Config) (posthog.Client, error) {
		return &mockPosthogClient{}, nil
	}
	defer func() { posthogNewWithConfig = oldNew }()

	t.Setenv("DATATUG_POSTHOG_API_KEY", "test-key")

	client := getPostHogClient()
	assert.NotNil(t, client)
	assert.NotEmpty(t, posthogDistinctID)
}

// --- getPostHogClient: posthogNewWithConfig returns error ---

func TestGetPostHogClient_NewClientError(t *testing.T) {
	_, restore := withTempConfig(t, "api_key: test-key\ndistinct_id: some-id\n")
	defer restore()

	oldNew := posthogNewWithConfig
	posthogNewWithConfig = func(apiKey string, config posthog.Config) (posthog.Client, error) {
		return nil, errors.New("bad config")
	}
	defer func() { posthogNewWithConfig = oldNew }()

	t.Setenv("DATATUG_POSTHOG_API_KEY", "test-key")

	client := getPostHogClient()
	assert.Nil(t, client)
}

// --- getPostHogClient: getPostHogApiKeyFromServerFunc error branch ---

func TestGetPostHogClient_ApiKeyServerError(t *testing.T) {
	// Config with stale timestamp (zero value), no lock, no key → hits server fetch
	_, restore := withTempConfig(t, "distinct_id: some-id\n")
	defer restore()

	oldFetch := getPostHogApiKeyFromServerFunc
	getPostHogApiKeyFromServerFunc = func() (string, error) {
		return "", errors.New("server unavailable")
	}
	defer func() { getPostHogApiKeyFromServerFunc = oldFetch }()

	oldNew := posthogNewWithConfig
	posthogNewWithConfig = func(apiKey string, config posthog.Config) (posthog.Client, error) {
		return &mockPosthogClient{}, nil
	}
	defer func() { posthogNewWithConfig = oldNew }()

	t.Setenv("DATATUG_POSTHOG_API_KEY", "")

	client := getPostHogClient()
	// falls back to hardcoded key, still returns a client
	assert.NotNil(t, client)
}

// --- getPostHogClient: getPostHogApiKeyFromServerFunc success branch ---

func TestGetPostHogClient_ApiKeyServerSuccess(t *testing.T) {
	_, restore := withTempConfig(t, "distinct_id: some-id\n")
	defer restore()

	oldFetch := getPostHogApiKeyFromServerFunc
	getPostHogApiKeyFromServerFunc = func() (string, error) {
		return "fetched-key", nil
	}
	defer func() { getPostHogApiKeyFromServerFunc = oldFetch }()

	oldNew := posthogNewWithConfig
	posthogNewWithConfig = func(apiKey string, config posthog.Config) (posthog.Client, error) {
		assert.Equal(t, "fetched-key", apiKey)
		return &mockPosthogClient{}, nil
	}
	defer func() { posthogNewWithConfig = oldNew }()

	t.Setenv("DATATUG_POSTHOG_API_KEY", "")

	client := getPostHogClient()
	assert.NotNil(t, client)
}

// --- writePostHogConfigToFile: osCreate failure ---

func TestWritePostHogConfigToFile_CreateError(t *testing.T) {
	oldCreate := osCreate
	osCreate = func(name string) (*os.File, error) {
		return nil, errors.New("cannot create file")
	}
	defer func() { osCreate = oldCreate }()

	_, restore := withTempConfig(t, "")
	defer restore()

	err := writePostHogConfigToFile(nil, posthogConfig{ApiKey: "k", DistinctID: "d"}) //nolint:staticcheck
	assert.NoError(t, err)                                                            // function always returns nil
}

// --- readPostHogConfig: file not found ---

func TestReadPostHogConfig_FileNotFound(t *testing.T) {
	old := getPosthogConfigFilePath
	getPosthogConfigFilePath = func() string { return "/nonexistent/path/.posthog.yaml" }
	defer func() { getPosthogConfigFilePath = old }()

	cfg := readPostHogConfig()
	assert.Empty(t, cfg.ApiKey)
	assert.Empty(t, cfg.DistinctID)
}

// --- readPostHogConfig: malformed YAML ---

func TestReadPostHogConfig_BadYAML(t *testing.T) {
	_, restore := withTempConfig(t, ":\tinvalid: yaml: content\n")
	defer restore()

	cfg := readPostHogConfig()
	// Malformed YAML should yield a zero-value config.
	assert.Empty(t, cfg.ApiKey)
	assert.Empty(t, cfg.DistinctID)
	assert.Empty(t, cfg.LockApiKey)
	assert.True(t, cfg.ApiKeyTimestamp.IsZero())
}

// --- getPostHogApiKeyFromServer via httpDoRequest seam ---

func TestGetPostHogApiKeyFromServer_Success(t *testing.T) {
	oldDo := httpDoRequest
	httpDoRequest = func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader("my-api-key")),
		}, nil
	}
	defer func() { httpDoRequest = oldDo }()

	key, err := getPostHogApiKeyFromServer()
	assert.NoError(t, err)
	assert.Equal(t, "my-api-key", key)
}

func TestGetPostHogApiKeyFromServer_NetworkError(t *testing.T) {
	oldDo := httpDoRequest
	httpDoRequest = func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("network failure")
	}
	defer func() { httpDoRequest = oldDo }()

	key, err := getPostHogApiKeyFromServer()
	assert.Error(t, err)
	assert.Empty(t, key)
}

func TestGetPostHogApiKeyFromServer_TimeoutError(t *testing.T) {
	oldDo := httpDoRequest
	httpDoRequest = func(req *http.Request) (*http.Response, error) {
		// simulate deadline exceeded wrapped in a net error
		return nil, errors.Join(errors.New("timeout"), context_deadline_exceeded_sentinel{})
	}
	defer func() { httpDoRequest = oldDo }()

	key, err := getPostHogApiKeyFromServer()
	assert.Error(t, err)
	assert.Empty(t, key)
}

// sentinel that satisfies errors.Is(err, context.DeadlineExceeded)
type context_deadline_exceeded_sentinel struct{}

func (context_deadline_exceeded_sentinel) Error() string   { return "context deadline exceeded" }
func (context_deadline_exceeded_sentinel) Timeout() bool   { return true }
func (context_deadline_exceeded_sentinel) Temporary() bool { return false }
func (context_deadline_exceeded_sentinel) Is(target error) bool {
	_, ok := target.(interface{ Timeout() bool })
	return ok
}

// --- cover default seam bodies ---

// TestSeamBodies_PosthogNewWithConfig exercises the default posthogNewWithConfig seam body
// (which calls the real posthog.NewWithConfig). A valid API key must produce either a non-nil
// client with no error, or an error with a nil client — never both nil.
func TestSeamBodies_PosthogNewWithConfig(t *testing.T) {
	client, err := posthogNewWithConfig("phc_test", posthog.Config{Endpoint: "https://eu.i.posthog.com"})
	// posthog.NewWithConfig either succeeds or fails; it must not return (nil, nil).
	assert.False(t, client == nil && err == nil, "expected either a client or an error, got neither")
	if err != nil {
		assert.Nil(t, client)
	} else {
		assert.NotNil(t, client)
		assert.NoError(t, client.Close())
	}
}

// TestSeamBodies_HttpDoRequest exercises the default httpDoRequest seam body using a local
// test server so no real network call is needed.
func TestSeamBodies_HttpDoRequest(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer ts.Close()

	req, err := http.NewRequestWithContext(context.Background(), "GET", ts.URL, nil)
	require.NoError(t, err)

	resp, err := httpDoRequest(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NoError(t, resp.Body.Close())
}

// --- writePostHogConfigToFile: yaml encode error ---

type errYamlEncoder struct{}

func (e *errYamlEncoder) Encode(_ interface{}) error { return errors.New("encode error") }

func TestWritePostHogConfigToFile_EncodeError(t *testing.T) {
	oldEnc := newYamlEncoder
	newYamlEncoder = func(w io.Writer) yamlEncoder { return &errYamlEncoder{} }
	defer func() { newYamlEncoder = oldEnc }()

	_, restore := withTempConfig(t, "")
	defer restore()

	err := writePostHogConfigToFile(context.Background(), posthogConfig{ApiKey: "k", DistinctID: "d"})
	assert.NoError(t, err) // function always returns nil
}

// --- getPostHogApiKeyFromServer: body read error ---

type errReader struct{}

func (errReader) Read(_ []byte) (int, error) { return 0, errors.New("read error") }

func TestGetPostHogApiKeyFromServer_ReadBodyError(t *testing.T) {
	oldDo := httpDoRequest
	httpDoRequest = func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(errReader{}),
		}, nil
	}
	defer func() { httpDoRequest = oldDo }()

	key, err := getPostHogApiKeyFromServer()
	assert.Error(t, err)
	assert.Empty(t, key)
}

// --- getPostHogApiKeyFromServer: bad URL triggers http.NewRequestWithContext error ---

func TestGetPostHogApiKeyFromServer_BadURL(t *testing.T) {
	oldURL := posthogAPIKeyURL
	// A URL with a control character causes http.NewRequestWithContext to fail.
	posthogAPIKeyURL = "http://invalid\x00url"
	defer func() { posthogAPIKeyURL = oldURL }()

	key, err := getPostHogApiKeyFromServer()
	assert.Error(t, err)
	assert.Empty(t, key)
}
