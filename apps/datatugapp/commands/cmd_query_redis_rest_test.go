package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dal-go/dalgo2redis"
	"github.com/spf13/cobra"
)

// Exercise the public REST transport with verified owned TLS. The command has
// no insecure TLS flag; only this constructor seam admits the fixture CA.
func redisRESTCommandForTLS(t *testing.T, server *httptest.Server) *cobra.Command {
	t.Helper()
	return queryRedisCommandWithRESTOpener(func(endpoint string, options dalgo2redis.RESTOptions) (redisQueryClient, error) {
		options.Transport = server.Client().Transport.(*http.Transport)
		return dalgo2redis.NewRESTClient(endpoint, options)
	})
}

func executeRedisREST(cmd *cobra.Command, ctx context.Context, args ...string) (string, error) {
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetContext(ctx)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestQueryRedisRESTNativeAndLegacyWireContract(t *testing.T) {
	for _, tc := range []struct {
		name, reply, want string
		command           []string
		lossless          bool
	}{
		{"legacy", "$6\r\n<tag>&\r\n", "\"\\u003ctag\\u003e\\u0026\"\n", []string{"GET", "owned:key"}, false},
		{"bytes", "$5\r\n\x00\xff\x80\r\n\r\n", `{"kind":"bytes","base64":"AP+ADQo="}` + "\n", []string{"GET", "owned:key"}, true},
		{"int64", ":9223372036854775807\r\n", `{"kind":"integer","value":"9223372036854775807"}` + "\n", []string{"PTTL", "owned:key"}, true},
		{"type", "+string\r\n", "\"string\"\n", []string{"TYPE", "owned:key"}, true},
		{"null-empty", "*2\r\n$-1\r\n$0\r\n\r\n", `{"kind":"array","values":[null,{"kind":"bytes","base64":""}]}` + "\n", []string{"MGET", "owned:key", "owned:absent"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLI_UPSTASH_READ", "synthetic-reader-secret")
			t.Setenv("CLI_UPSTASH_SCAN", "synthetic-scan-secret")
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				defer func() { _ = r.Body.Close() }()
				var command []string
				if err := json.NewDecoder(io.LimitReader(r.Body, 65537)).Decode(&command); err != nil {
					t.Error("invalid command body")
				}
				if r.Method != http.MethodPost || r.URL.RequestURI() != "/" || !reflect.DeepEqual(command, tc.command) {
					t.Errorf("wrong command transport: method=%s path=%s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer synthetic-reader-secret" || r.Header.Get("Upstash-Response-Format") != "resp2" || r.Header.Get("Upstash-Encoding") != "" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("wrong scoped authentication or RESP2 headers")
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = io.WriteString(w, tc.reply)
			}))
			defer server.Close()
			t.Setenv("CLI_UPSTASH_SOURCE", "upstash+"+server.URL)
			args := []string{"--db", "env:CLI_UPSTASH_SOURCE", "--rest-token-env", "CLI_UPSTASH_READ", "--scan-token-env", "CLI_UPSTASH_SCAN"}
			if tc.lossless {
				args = append(args, "--lossless")
			}
			args = append(args, "--")
			out, err := executeRedisREST(redisRESTCommandForTLS(t, server), context.Background(), append(args, tc.command...)...)
			if err != nil || out != tc.want || calls.Load() != 1 {
				t.Fatalf("REST result %q error=%v calls=%d", out, err, calls.Load())
			}
		})
	}
}

func TestQueryRedisRESTScopedScanUsesOnlyExplicitACLToken(t *testing.T) {
	t.Setenv("CLI_UPSTASH_READ", "reader-must-not-be-used")
	t.Setenv("CLI_UPSTASH_SCAN", "synthetic-scan-secret")
	const prefix = "owned[*?\\]:"
	key := prefix + "\x00\xff"
	reply := fmt.Sprintf("*2\r\n$20\r\n18446744073709551615\r\n*2\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n", len(key), key, len(key), key)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { _ = r.Body.Close() }()
		var command []string
		if err := json.NewDecoder(r.Body).Decode(&command); err != nil {
			t.Error(err)
		}
		want := []string{"SCAN", "0", "COUNT", "1", "MATCH", "owned\\[\\*\\?\\\\\\]:*"}
		if !reflect.DeepEqual(command, want) || r.Header.Get("Authorization") != "Bearer synthetic-scan-secret" {
			t.Error("SCAN scope or separate ACL token was not applied")
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(w, reply)
	}))
	defer server.Close()
	for _, includeReader := range []bool{false, true} {
		args := []string{"--db", "upstash+" + server.URL, "--scan-token-env", "CLI_UPSTASH_SCAN", "--scan-prefix", prefix, "--lossless"}
		if includeReader {
			args = append(args, "--rest-token-env", "CLI_UPSTASH_READ")
		}
		out, err := executeRedisREST(redisRESTCommandForTLS(t, server), context.Background(), append(args, "--", "scan", "0", "COUNT", "1")...)
		var value struct {
			Kind   string            `json:"kind"`
			Values []json.RawMessage `json:"values"`
		}
		if err != nil || json.Unmarshal([]byte(out), &value) != nil || value.Kind != "array" || len(value.Values) != 2 {
			t.Fatalf("SCAN result %q error=%v", out, err)
		}
		if string(value.Values[0]) != `{"kind":"bytes","base64":"MTg0NDY3NDQwNzM3MDk1NTE2MTU="}` || string(value.Values[1]) != `{"kind":"array","values":[{"kind":"bytes","base64":"b3duZWRbKj9cXToA/w=="},{"kind":"bytes","base64":"b3duZWRbKj9cXToA/w=="}]}` {
			t.Fatalf("cursor or duplicate binary suffix lost: %q", out)
		}
	}
}

func TestQueryRedisRESTFlagsAndPolicyRefuseBeforeOpening(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"reader-required", []string{"--db", "upstash+https://owned.invalid", "GET", "key"}},
		{"scan-token-required", []string{"--db", "upstash+https://owned.invalid", "--rest-token-env", "READ", "--scan-prefix", "owned:", "SCAN", "0"}},
		{"prefix-required", []string{"--db", "upstash+https://owned.invalid", "--scan-token-env", "SCAN", "--", "SCAN", "0"}},
		{"reader-not-scan-fallback", []string{"--db", "upstash+https://owned.invalid", "--scan-token-env", "SCAN", "GET", "key"}},
		{"bad-env-name", []string{"--db", "upstash+https://owned.invalid", "--rest-token-env", "secret=must-not-print", "GET", "key"}},
		{"bad-unused-env-name", []string{"--db", "upstash+https://owned.invalid", "--rest-token-env", "READ", "--scan-token-env", "secret=must-not-print", "GET", "key"}},
		{"tcp-auth-flag", []string{"--db", "redis://%", "--rest-token-env", "READ", "GET", "key"}},
		{"tcp-empty-auth-flag", []string{"--db", "redis://%", "--scan-token-env=", "GET", "key"}},
		{"http-no-inference", []string{"--db", "https://owned.invalid", "--rest-token-env", "READ", "GET", "key"}},
		{"http-rest-refused", []string{"--db", "upstash+http://owned.invalid", "--rest-token-env", "READ", "GET", "key"}},
		{"write", []string{"--db", "upstash+https://owned.invalid", "--rest-token-env", "READ", "SET", "key", "value"}},
		{"keys", []string{"--db", "upstash+https://owned.invalid", "--rest-token-env", "READ", "KEYS", "*"}},
		{"scan-conflict", []string{"--db", "upstash+https://owned.invalid", "--scan-token-env", "SCAN", "--scan-prefix", "owned:", "SCAN", "0", "MATCH", "*"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opened := false
			cmd := queryRedisCommandWithRESTOpener(func(string, dalgo2redis.RESTOptions) (redisQueryClient, error) {
				opened = true
				return nil, fmt.Errorf("unexpected open")
			})
			out, err := executeRedisREST(cmd, context.Background(), tc.args...)
			if err == nil || out != "" || opened || strings.Contains(err.Error(), "must-not-print") {
				t.Fatalf("misconfigured request: opened=%v bytes=%d error=%v", opened, len(out), err)
			}
		})
	}
}

func TestQueryRedisRESTEndpointAndTLSRefusals(t *testing.T) {
	t.Setenv("CLI_UPSTASH_READ", "synthetic-secret-not-for-logs")
	for _, source := range []string{
		"upstash+https://user:source-secret@owned.invalid", "upstash+https://owned.invalid?token=source-secret",
		"upstash+https://owned.invalid/GET/key", "upstash+https://owned.invalid#source-secret", "upstash+https://%",
	} {
		out, err := executeRedisREST(queryRedisCommand(), context.Background(), "--db", source, "--rest-token-env", "CLI_UPSTASH_READ", "GET", "key")
		if err == nil || out != "" || strings.Contains(err.Error(), "source-secret") || strings.Contains(err.Error(), "synthetic-secret") {
			t.Fatalf("invalid endpoint leaked: bytes=%d error=%v", len(out), err)
		}
	}
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	out, err := executeRedisREST(queryRedisCommand(), context.Background(), "--db", "upstash+"+server.URL, "--rest-token-env", "CLI_UPSTASH_READ", "GET", "key")
	if err == nil || out != "" || calls.Load() != 0 || strings.Contains(err.Error(), server.URL) {
		t.Fatalf("untrusted TLS accepted or leaked: bytes=%d error=%v", len(out), err)
	}
}

func TestQueryRedisRESTTokenResolvedInsideOperation(t *testing.T) {
	t.Setenv("CLI_UPSTASH_LATE", "stale-secret")
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer rotated-secret" {
			t.Error("token resolved before operation")
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(w, "$-1\r\n")
	}))
	defer server.Close()
	cmd := queryRedisCommandWithRESTOpener(func(endpoint string, options dalgo2redis.RESTOptions) (redisQueryClient, error) {
		options.Transport = server.Client().Transport.(*http.Transport)
		client, err := dalgo2redis.NewRESTClient(endpoint, options)
		// Constructor has completed; only the operation may resolve the value.
		t.Setenv("CLI_UPSTASH_LATE", "rotated-secret")
		return client, err
	})
	out, err := executeRedisREST(cmd, context.Background(), "--db", "upstash+"+server.URL, "--rest-token-env", "CLI_UPSTASH_LATE", "GET", "key")
	if err != nil || out != "null\n" || calls.Load() != 1 {
		t.Fatalf("late token result %q error=%v", out, err)
	}
	for _, name := range []string{"CLI_UPSTASH_EMPTY", "CLI_UPSTASH_SCAN_EMPTY"} {
		t.Setenv(name, "")
		out, err = executeRedisREST(redisRESTCommandForTLS(t, server), context.Background(), "--db", "upstash+"+server.URL, "--rest-token-env", name, "GET", "key")
		if err == nil || out != "" || calls.Load() != 1 {
			t.Fatalf("empty token made a request: error=%v calls=%d", err, calls.Load())
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if token, err := redisRESTTokenFromEnv(ctx, "CLI_UPSTASH_LATE"); err != context.Canceled || token != "" {
		t.Fatal("pre-abort exposed a token")
	}
	out, err = executeRedisREST(redisRESTCommandForTLS(t, server), ctx, "--db", "upstash+"+server.URL, "--rest-token-env", "CLI_UPSTASH_LATE", "GET", "key")
	if err == nil || out != "" || calls.Load() != 1 {
		t.Fatal("pre-aborted REST command made a request")
	}
}

type redisRESTCloseTracker struct {
	redisQueryClient
	closes *atomic.Int32
}

func (c *redisRESTCloseTracker) Close() error {
	c.closes.Add(1)
	return c.redisQueryClient.Close()
}

func TestQueryRedisRESTClosesClientOnQueryAndOutputOutcomes(t *testing.T) {
	t.Setenv("CLI_UPSTASH_READ", "synthetic-token")
	for _, outcome := range []string{"success", "query-failure", "output-failure"} {
		t.Run(outcome, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/octet-stream")
				if outcome == "query-failure" {
					w.WriteHeader(http.StatusForbidden)
				}
				_, _ = io.WriteString(w, "$-1\r\n")
			}))
			defer server.Close()
			var closes atomic.Int32
			cmd := queryRedisCommandWithRESTOpener(func(endpoint string, options dalgo2redis.RESTOptions) (redisQueryClient, error) {
				options.Transport = server.Client().Transport.(*http.Transport)
				client, err := dalgo2redis.NewRESTClient(endpoint, options)
				if err != nil {
					return nil, err
				}
				return &redisRESTCloseTracker{redisQueryClient: client, closes: &closes}, nil
			})
			var out bytes.Buffer
			cmd.SetOut(&out)
			if outcome == "output-failure" {
				cmd.SetOut(failWriter{})
			}
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"--db", "upstash+" + server.URL, "--rest-token-env", "CLI_UPSTASH_READ", "GET", "key"})
			err := cmd.Execute()
			if closes.Load() != 1 || (err == nil) != (outcome == "success") {
				t.Fatalf("client lifecycle outcome=%s closes=%d error=%v", outcome, closes.Load(), err)
			}
			if outcome != "success" && out.Len() != 0 {
				t.Fatal("failed read emitted output")
			}
		})
	}
}

func TestQueryRedisRESTFailuresHaveNoPartialOutput(t *testing.T) {
	t.Setenv("CLI_UPSTASH_READ", "synthetic-token-secret")
	for _, tc := range []struct {
		name, reply, contentType string
		status                   int
		command                  []string
	}{
		{"denied", "synthetic-server-secret", "application/json", 403, []string{"GET", "key"}},
		{"redirect", "synthetic-server-secret", "application/octet-stream", 302, []string{"GET", "key"}},
		{"malformed", "$4\r\nx\r\n", "application/octet-stream", 200, []string{"GET", "key"}},
		{"wrong-content", "$-1\r\n", "application/json", 200, []string{"GET", "key"}},
		{"extra-frame", "$-1\r\n+OK\r\n", "application/octet-stream", 200, []string{"GET", "key"}},
		{"oversize", "$1048577\r\n", "application/octet-stream", 200, []string{"GET", "key"}},
		{"foreign-scan", "*2\r\n$1\r\n0\r\n*1\r\n$23\r\nsynthetic-server-secret\r\n", "application/octet-stream", 200, []string{"SCAN", "0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", tc.contentType)
				w.Header().Set("Location", "/must-not-follow")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.reply)
			}))
			defer server.Close()
			args := []string{"--db", "upstash+" + server.URL, "--rest-token-env", "CLI_UPSTASH_READ", "--scan-token-env", "CLI_UPSTASH_READ", "--scan-prefix", "owned:", "--lossless", "--"}
			out, err := executeRedisREST(redisRESTCommandForTLS(t, server), context.Background(), append(args, tc.command...)...)
			if err == nil || out != "" || calls.Load() != 1 || strings.Contains(err.Error(), "synthetic-") || strings.Contains(err.Error(), server.URL) {
				t.Fatalf("unsafe failure: bytes=%d calls=%d error=%v", len(out), calls.Load(), err)
			}
		})
	}
}

func TestQueryRedisRESTRequestBudgetsBeforeHTTP(t *testing.T) {
	t.Setenv("CLI_UPSTASH_READ", "synthetic-token")
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	for i, key := range []string{strings.Repeat("x", 65537), strings.Repeat("\n", 40000), "invalid-utf8-\xff"} {
		out, err := executeRedisREST(redisRESTCommandForTLS(t, server), context.Background(), "--db", "upstash+"+server.URL, "--rest-token-env", "CLI_UPSTASH_READ", "--", "GET", key)
		if err == nil || out != "" || calls.Load() != 0 {
			t.Fatalf("budget case %d made HTTP or output: bytes=%d error=%v", i, len(out), err)
		}
	}
}

func TestQueryRedisRESTCancellationAfterDispatchAndReopen(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprintf("deadline=%v", deadline), func(t *testing.T) {
			t.Setenv("CLI_UPSTASH_READ", "synthetic-token")
			started, disconnected, escape := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer func() { _ = r.Body.Close() }()
				if _, err := io.Copy(io.Discard, io.LimitReader(r.Body, 65537)); err != nil {
					t.Error(err)
					return
				}
				if calls.Add(1) != 1 {
					w.Header().Set("Content-Type", "application/octet-stream")
					_, _ = io.WriteString(w, "$-1\r\n")
					return
				}
				close(started)
				select {
				case <-r.Context().Done():
					close(disconnected)
				case <-escape:
				case <-time.After(3 * time.Second):
				}
			}))
			defer server.Close()
			defer close(escape)
			ctx, cancel := context.WithCancel(context.Background())
			want := "context canceled"
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 500*time.Millisecond)
				want = "context deadline exceeded"
			}
			defer cancel()
			cmd := redisRESTCommandForTLS(t, server)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(io.Discard)
			cmd.SetContext(ctx)
			args := []string{"--db", "upstash+" + server.URL, "--rest-token-env", "CLI_UPSTASH_READ", "--lossless", "GET", "key"}
			cmd.SetArgs(args)
			done := make(chan error, 1)
			go func() { done <- cmd.Execute() }()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("REST command never dispatched")
			}
			if !deadline {
				cancel()
			}
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), want) || out.Len() != 0 {
					t.Fatalf("cancelled output bytes=%d error=%v", out.Len(), err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("REST command did not promptly cancel")
			}
			select {
			case <-disconnected:
			case <-time.After(2 * time.Second):
				t.Fatal("owned HTTP read was not closed")
			}
			value, err := executeRedisREST(redisRESTCommandForTLS(t, server), context.Background(), args...)
			if err != nil || value != "null\n" || calls.Load() != 2 {
				t.Fatalf("reopen failed: %q error=%v", value, err)
			}
		})
	}
}
