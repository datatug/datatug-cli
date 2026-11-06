package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/dal-go/dalgo2dynamodb"
)

func TestDynamoSignalContextCleanupAndCommandReuse(t *testing.T) {
	previous := interruptContext
	defer func() { interruptContext = previous }()
	parent := context.Background()
	stopped := false
	var child context.Context
	interruptContext = func(given context.Context) (context.Context, func() os.Signal, func()) {
		if given != parent {
			t.Error("command inherited a stale per-invocation context")
		}
		var cancel context.CancelFunc
		child, cancel = context.WithCancel(given)
		return child, func() os.Signal { return nil }, func() { stopped = true; cancel() }
	}
	cmd := queryDynamoDBCommand()
	cmd.SetContext(parent)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--config", "missing", "--request", "-"})
	for range 2 {
		stopped = false
		if err := cmd.Execute(); err == nil {
			t.Fatal("missing input unexpectedly succeeded")
		}
		if !stopped || child.Err() == nil || cmd.Context() != parent {
			t.Fatal("signal scope leaked across command reuse")
		}
	}
	// A direct RunE call can have nil Context, unlike Execute's default.
	bare := queryDynamoDBCommand()
	parent = context.Background()
	stopped = false
	if err := bare.RunE(bare, nil); err == nil || !stopped || child.Err() == nil || bare.Context() != nil {
		t.Fatal("nil caller context was not restored")
	}
}

func TestDynamoConfigurationRefusesUnsafeTransportAndBadFiles(t *testing.T) {
	for _, endpoint := range []string{"http://127.0.0.1:1234", "https://user:secret@example.test", "https://example.test?secret=token", "https://example.test?", "https://example.test#secret", "https://example.test/api", "https://example.test/%2F", "https:opaque", "https://", "https://bad host"} {
		if _, err := dynamoTransport(context.Background(), dynamoNativeConfig{Endpoint: endpoint, Region: "us-east-1"}); !errors.Is(err, errDynamoInput) {
			t.Fatal("unsafe endpoint accepted")
		}
	}
	if _, err := dynamoTransport(context.Background(), dynamoNativeConfig{Endpoint: "https://example.test", Region: "bad/region"}); !errors.Is(err, errDynamoInput) {
		t.Fatal("bad region accepted")
	}
	for _, path := range []string{"/no-such-owned-fixture-file", t.TempDir(), dynamoTestFile(t, "not-ca.pem", []byte("not a certificate"))} {
		if _, err := dynamoTransport(context.Background(), dynamoNativeConfig{Endpoint: "https://example.test", Region: "us-east-1", CAFile: path}); err == nil {
			t.Fatal("bad CA input accepted")
		}
	}
	transport, err := dynamoTransport(context.Background(), dynamoNativeConfig{Endpoint: "https://example.test", Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	if transport.Proxy != nil || transport.TLSClientConfig.InsecureSkipVerify || transport.TLSClientConfig.MinVersion < 0x0303 {
		t.Fatal("TLS/proxy security changed")
	}
	for _, args := range [][]string{{}, {"--config", "-", "--request", "-"}, {"--config", "missing", "--request", "-", "--timeout", "0s"}, {"--config", "missing", "--request", "-", "--timeout", "3m"}, {"--config", "missing", "--request", "-"}, {"--config", dynamoTestFile(t, "bad.json", []byte(`{"endpoint":"secret","unknown":true}`)), "--request", "-"}} {
		cmd := queryDynamoDBCommand()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("bad config accepted or leaked")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readDynamoFile(ctx, "not-opened"); !errors.Is(err, context.Canceled) {
		t.Fatal("file preabort lost")
	}
	if _, err := newDynamoNativeClient(dynamoNativeConfig{Endpoint: "https://example.test", Region: "us-east-1"}, transport, time.Second); err == nil {
		t.Fatal("empty table mapping accepted")
	}
}

func TestDynamoCredentialsExplicitEnvironmentOnly(t *testing.T) {
	dynamoTestCredentials(t)
	provider := dynamoEnvCredentials{}
	if credentials, err := provider.Retrieve(context.Background()); err != nil || credentials.SessionToken != "owned-fixture-session" {
		t.Fatal("explicit credentials missing")
	}
	t.Setenv("AWS_SESSION_TOKEN", "")
	if _, err := provider.Retrieve(context.Background()); err != nil {
		t.Fatal("optional session token refused")
	}
	t.Setenv("AWS_SESSION_TOKEN", "secret\nheader")
	if _, err := provider.Retrieve(context.Background()); !errors.Is(err, errDynamoInput) {
		t.Fatal("unsafe credential header accepted")
	}
	for _, value := range []string{"", strings.Repeat("x", 8193), "secret token", "secret\n", "secret🌍"} {
		if dynamoCredentialText(value) {
			t.Fatal("unsendable credential accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.Retrieve(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("credential preabort lost")
	}
}

func TestQueryDynamoRedirectAndOversizeRepliesFailClosed(t *testing.T) {
	dynamoTestCredentials(t)
	var followed atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Add(1) }))
	t.Cleanup(target.Close)
	for _, mode := range []string{"redirect", "oversize", "foreign", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			handlerDone := make(chan struct{})
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(handlerDone)
				// Read the complete bounded POST before observing peer cancellation.
				// This distinguishes a real admitted TLS response from a failed input.
				if r.TLS == nil || r.ContentLength < 1 || r.ContentLength > dynamoDocumentBytes {
					t.Error("owned TLS request contract changed")
					return
				}
				if _, err := io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, dynamoDocumentBytes)); err != nil {
					t.Error("owned bounded request could not be drained")
					return
				}
				w.Header().Set("Content-Type", "application/x-amz-json-1.0")
				switch mode {
				case "redirect":
					w.Header().Set("Location", target.URL)
					w.WriteHeader(302)
				case "oversize":
					if err := json.NewEncoder(w).Encode(map[string]any{"Item": map[string]any{"account": map[string]any{"S": "one"}, "large": map[string]any{"S": strings.Repeat("x", 1<<20)}}}); err != nil {
						// A client that refuses the oversized reply can close while
						// the server is still writing it. Accept only a concrete
						// peer-close error with the request cancellation witness.
						if !errors.Is(err, syscall.EPIPE) && !errors.Is(err, syscall.ECONNRESET) {
							t.Error("unexpected oversized fixture response write failure")
						} else if !errors.Is(r.Context().Err(), context.Canceled) {
							t.Error("peer-close write error lacked request cancellation")
						}
					}
				case "foreign":
					_, _ = w.Write([]byte(`{"Item":{"account":{"S":"other-secret"}}}`))
				default:
					_, _ = w.Write([]byte(`{"Item":{"account":{"S":"one"}}`))
				}
			}))
			t.Cleanup(server.Close)
			config := dynamoTestConfig(t, server)
			completedBy := time.Now().Add(2 * time.Second)
			output, diagnostic, err := dynamoTestCommand(t, context.Background(), config, `{"operation":"get","collection":"items","key":{"account":{"S":"one"}}}`, "2s")
			if err == nil || output != "" || strings.Contains(err.Error()+diagnostic, "secret") {
				t.Fatal("unsafe reply delivered or leaked")
			}
			// A positive handler acknowledgement is required within the original
			// two-second budget, including any expected peer-close write error.
			select {
			case <-handlerDone:
			default:
				select {
				case <-handlerDone:
				case <-time.After(time.Until(completedBy)):
					t.Fatal("owned negative-response handler did not complete")
				}
			}
		})
	}
	if followed.Load() != 0 {
		t.Fatal("redirect reached another endpoint")
	}
}

func TestQueryDynamoReadFailureModesDoNotPartiallyWrite(t *testing.T) {
	dynamoTestCredentials(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"__type":"AccessDeniedException","message":"secret"}`))
	}))
	t.Cleanup(server.Close)
	config := dynamoTestConfig(t, server)
	for _, request := range []string{
		`{"operation":"exists","collection":"items","key":{"account":{"S":"one"}}}`,
		`{"operation":"getMany","collection":"items","keys":[{"account":{"S":"one"}}]}`,
		`{"operation":"query","collection":"events","partition":{"S":"tenant"},"limit":1}`,
	} {
		if output, _, err := dynamoTestCommand(t, context.Background(), config, request, "2s"); err == nil || output != "" {
			t.Fatal("provider failure returned success/partial stdout")
		}
	}
	requestPath := dynamoTestFile(t, "request.json", []byte(`{"operation":"get","collection":"items","key":{"account":{"S":"one"}}}`))
	badConfig := dynamoTestJSON(t, "config.json", dynamoNativeConfig{Endpoint: server.URL, Region: "us-east-1", Tables: map[string]dalgo2dynamodb.Table{"items": {Name: "", PartitionKey: "account"}}})
	cmd := queryDynamoDBCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--config", badConfig, "--request", requestPath})
	if err := cmd.Execute(); err == nil || out.Len() != 0 {
		t.Fatal("bad mapping accepted")
	}
}

type dynamoCommittedWriter struct {
	cancel context.CancelFunc
	output bytes.Buffer
}

func (w *dynamoCommittedWriter) Write(p []byte) (int, error) {
	n, err := w.output.Write(p)
	w.cancel()
	return n, err
}
func TestDynamoCommittedOutputIsNotReclassified(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	writer := &dynamoCommittedWriter{cancel: cancel}
	if err := writeDynamoResult(ctx, writer, map[string]any{"ok": true}); err != nil || writer.output.Len() == 0 || ctx.Err() == nil {
		t.Fatal("already committed stdout was converted into cancellation")
	}
}
