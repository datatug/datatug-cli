package commands

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// A tiny RESP2 peer tests the command's actual transport/JSON boundary without
// depending on optional Redis services. Real Redis/ACL acceptance lives in the
// Redis journey; this peer gives deterministic protocol and output failures.
func redisCommandPeer(t *testing.T, reply string) string {
	return redisCommandPeerWithCheck(t, reply, nil)
}

func redisCommandPeerWithCheck(t *testing.T, reply string, check func([]string)) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var workers sync.WaitGroup
	connections := make(map[net.Conn]bool)
	done := make(chan struct{})
	serve := func(conn net.Conn) {
		defer workers.Done()
		defer func() {
			_ = conn.Close()
			mu.Lock()
			delete(connections, conn)
			mu.Unlock()
		}()
		_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
		reader := bufio.NewReader(conn)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			count, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "*")))
			if err != nil || count < 1 || count > 130 {
				return
			}
			args := make([]string, count)
			for i := range args {
				header, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				size, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(header, "$")))
				if err != nil || size < 0 || size > 65536 {
					return
				}
				value := make([]byte, size+2)
				if _, err = io.ReadFull(reader, value); err != nil {
					return
				}
				args[i] = string(value[:size])
			}
			response := "+OK\r\n"
			switch strings.ToUpper(args[0]) {
			case "HELLO":
				response = "-ERR unknown command HELLO\r\n"
			case "PING":
				response = "+PONG\r\n"
			case "GET", "MGET", "HMGET", "PTTL", "TYPE", "ZSCORE", "SCAN":
				if check != nil {
					check(args)
				}
				response = reply
			}
			if _, err = io.WriteString(conn, response); err != nil {
				return
			}
		}
	}
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			connections[conn] = true
			mu.Unlock()
			workers.Add(1)
			go serve(conn)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
		mu.Lock()
		for conn := range connections {
			_ = conn.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	return "redis://" + listener.Addr().String() + "/0"
}
func TestQueryRedisHermeticValidation(t *testing.T) {
	for _, tc := range []struct{ source, want string }{{"env:", "environment variable name"}, {"", "--db is required"}, {"redis://%", "invalid Redis connection URL"}} {
		t.Run(fmt.Sprintf("source-%s", tc.want), func(t *testing.T) {
			cmd := queryRedisCommand()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs([]string{"--db", tc.source, "--", "GET", "key"})
			if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v want %s", err, tc.want)
			}
		})
	}
}
func TestQueryRedisHermeticTransportAndOutput(t *testing.T) {
	for _, tc := range []struct {
		name, reply, want string
		env, writeFailure bool
	}{
		{name: "JSON", reply: "$5\r\nhello\r\n", want: "\"hello\"\n"},
		{name: "environment", reply: "$-1\r\n", want: "null\n", env: true},
		{name: "server-denial", reply: "-NOPERM private-server-detail\r\n", want: "Redis read failed"},
		{name: "output-failure", reply: "$5\r\nhello\r\n", writeFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := redisCommandPeer(t, tc.reply)
			if tc.env {
				t.Setenv("REDIS_HERMETIC_URL", source)
				source = "env:REDIS_HERMETIC_URL"
			}
			cmd := queryCommand()
			var out, stderr bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&stderr)
			if tc.writeFailure {
				cmd.SetOut(failWriter{})
			}
			cmd.SetArgs([]string{"redis", "--db", source, "--", "GET", "key"})
			err := cmd.Execute()
			if tc.name == "server-denial" {
				if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "private-server-detail") {
					t.Fatalf("unsafe denial: %v", err)
				}
				return
			}
			if tc.writeFailure {
				if err == nil {
					t.Fatal("output failure ignored")
				}
				return
			}
			if err != nil || out.String() != tc.want {
				t.Fatalf("output %q error %v", out.String(), err)
			}
		})
	}
}
func TestQueryRedisLosslessTransportAndOutput(t *testing.T) {
	for _, tc := range []struct {
		name, reply, want string
		args              []string
	}{
		{"binary", "$5\r\n\x00\xff\x80\r\n\r\n", `{"kind":"bytes","base64":"AP+ADQo="}` + "\n", []string{"GET", "key"}},
		{"empty", "$0\r\n\r\n", `{"kind":"bytes","base64":""}` + "\n", []string{"GET", "key"}},
		{"missing", "$-1\r\n", "null\n", []string{"GET", "key"}},
		{"int64", ":9223372036854775807\r\n", `{"kind":"integer","value":"9223372036854775807"}` + "\n", []string{"PTTL", "key"}},
		{"type-status", "+string\r\n", "\"string\"\n", []string{"TYPE", "key"}},
		{"zscore", "$3\r\n1.5\r\n", `{"kind":"bytes","base64":"MS41"}` + "\n", []string{"ZSCORE", "key", "member"}},
		{"scan-empty-nonterminal", "*2\r\n$2\r\n17\r\n*0\r\n", `{"kind":"array","values":[{"kind":"bytes","base64":"MTc="},{"kind":"array","values":[]}]}` + "\n", []string{"SCAN", "0", "COUNT", "1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := redisCommandPeerWithCheck(t, tc.reply, func(args []string) {
				if args[0] == "SCAN" {
					if got := strings.Join(args, "|"); got != "SCAN|0|COUNT|1|MATCH|tenant\\[\\*\\?\\\\\\]:*" {
						t.Errorf("incorrect literal scope on wire: %q", got)
					}
				}
			})
			cmd := queryCommand()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(io.Discard)
			args := []string{"redis", "--db", source, "--lossless", "--scan-prefix", "tenant[*?\\]:", "--"}
			cmd.SetArgs(append(args, tc.args...))
			if err := cmd.Execute(); err != nil || out.String() != tc.want {
				t.Fatalf("lossless output %q, error %v; want %q", out.String(), err, tc.want)
			}
		})
	}
}

func TestQueryRedisScopedScanRefusesBeforeOpening(t *testing.T) {
	for _, args := range [][]string{
		{"SCAN", "0"}, {"--scan-prefix", "tenant:", "--", "SCAN", "0", "MATCH", "*"},
		{"--scan-prefix", "tenant:", "--", "SCAN", "0", "COUNT", "1001"},
	} {
		cmd := queryRedisCommand()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(append([]string{"--db", "redis://%"}, args...))
		if err := cmd.Execute(); err == nil || strings.Contains(err.Error(), "invalid Redis connection URL") {
			t.Fatalf("invalid scan reached open: %v", err)
		}
	}
}

func TestQueryRedisForeignScanPageReturnsNoOutput(t *testing.T) {
	for _, lossless := range []bool{false, true} {
		source := redisCommandPeer(t, "*2\r\n$1\r\n0\r\n*2\r\n$8\r\ntenant:a\r\n$14\r\nforeign-secret\r\n")
		cmd := queryCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(io.Discard)
		args := []string{"redis", "--db", source, "--scan-prefix", "tenant:"}
		if lossless {
			args = append(args, "--lossless")
		}
		cmd.SetArgs(append(args, "--", "SCAN", "0"))
		err := cmd.Execute()
		if err == nil || out.Len() != 0 || strings.Contains(err.Error(), "foreign-secret") {
			t.Fatalf("foreign page leaked (lossless=%v): %q, %v", lossless, out.String(), err)
		}
	}
}

func TestQueryRedisCancellationAfterDispatch(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprintf("deadline=%v", deadline), func(t *testing.T) {
			dispatched := make(chan struct{})
			source := redisCommandPeerWithCheck(t, "", func([]string) { close(dispatched) })
			ctx, cancel := context.WithCancel(context.Background())
			want := "context canceled"
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 500*time.Millisecond)
				want = "context deadline exceeded"
			}
			defer cancel()
			cmd := queryCommand()
			cmd.SetContext(ctx)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"redis", "--db", source, "--lossless", "--", "GET", "key"})
			result := make(chan error, 1)
			go func() { result <- cmd.Execute() }()
			select {
			case <-dispatched:
			case <-time.After(2 * time.Second):
				t.Fatal("CLI read never dispatched")
			}
			if !deadline {
				cancel()
			}
			select {
			case err := <-result:
				if err == nil || !strings.Contains(err.Error(), want) || out.Len() != 0 {
					t.Fatalf("cancelled CLI output %q, error %v", out.String(), err)
				}
			case <-time.After(time.Second):
				t.Fatal("CLI did not promptly cancel the dispatched read")
			}
		})
	}
}

type redisMarshalFunc func() ([]byte, error)

func (f redisMarshalFunc) MarshalJSON() ([]byte, error) { return f() }

type redisWriterFunc func([]byte) (int, error)

func (f redisWriterFunc) Write(data []byte) (int, error) { return f(data) }

func TestRedisResultCancellationDuringSerializationWritesNothing(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprintf("deadline=%v", deadline), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			want := "context canceled"
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
				want = "context deadline exceeded"
			}
			defer cancel()
			marshaled := false
			value := redisMarshalFunc(func() ([]byte, error) {
				marshaled = true
				if deadline {
					// Wait for the real context's notification; no scheduling
					// assumption or sleep determines the interleaving.
					<-ctx.Done()
				} else {
					cancel()
				}
				return []byte(`{"secret":"synthetic-private-payload"}`), nil
			})
			var out bytes.Buffer
			err := writeRedisResult(ctx, &out, value)
			if !marshaled || err == nil || !strings.Contains(err.Error(), want) || out.Len() != 0 {
				t.Fatalf("late serialization result leaked: marshaled=%v output=%q error=%v", marshaled, out.String(), err)
			}
			if strings.Contains(err.Error(), "synthetic-private-payload") {
				t.Fatal("cancellation disclosed payload")
			}
		})
	}
}

func TestRedisResultEncoderAndWriterOutcomes(t *testing.T) {
	t.Run("unchanged newline and escaping", func(t *testing.T) {
		var out bytes.Buffer
		err := writeRedisResult(context.Background(), &out, map[string]any{"html": "<tag>&"})
		if err != nil || out.String() != `{"html":"\u003ctag\u003e\u0026"}`+"\n" {
			t.Fatalf("encoded output changed: %q %v", out.String(), err)
		}
	})
	t.Run("serialization failure never reaches writer", func(t *testing.T) {
		want := errors.New("synthetic encoding failure")
		var out bytes.Buffer
		value := redisMarshalFunc(func() ([]byte, error) { return nil, want })
		err := writeRedisResult(context.Background(), &out, value)
		if !errors.Is(err, want) || out.Len() != 0 {
			t.Fatalf("encoding failure output=%q error=%v", out.String(), err)
		}
	})
	t.Run("partial write retains original error", func(t *testing.T) {
		want := errors.New("synthetic output failure")
		written := 0
		output := redisWriterFunc(func(data []byte) (int, error) {
			written = len(data) / 2
			return written, want
		})
		err := writeRedisResult(context.Background(), output, "payload")
		if written == 0 || !errors.Is(err, want) {
			t.Fatalf("partial write outcome changed: bytes=%d error=%v", written, err)
		}
	})
	t.Run("short write without an error is refused", func(t *testing.T) {
		output := redisWriterFunc(func(data []byte) (int, error) { return len(data) - 1, nil })
		if err := writeRedisResult(context.Background(), output, "payload"); !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("short write accepted: %v", err)
		}
	})
	t.Run("cancellation after committed write keeps success", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var out bytes.Buffer
		output := redisWriterFunc(func(data []byte) (int, error) {
			n, err := out.Write(data)
			cancel()
			return n, err
		})
		if err := writeRedisResult(ctx, output, "payload"); err != nil || out.String() != "\"payload\"\n" || ctx.Err() == nil {
			t.Fatalf("committed outcome changed: output=%q error=%v", out.String(), err)
		}
	})
}

func TestQueryRedisMalformedScanRepliesWriteNothing(t *testing.T) {
	for _, reply := range []string{
		"*1\r\n$1\r\n0\r\n",
		"*2\r\n$20\r\n18446744073709551616\r\n*0\r\n",
		"*2\r\n$1\r\n0\r\n*1\r\n:1\r\n",
	} {
		for _, lossless := range []bool{false, true} {
			source := redisCommandPeer(t, reply)
			cmd := queryCommand()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(io.Discard)
			args := []string{"redis", "--db", source, "--scan-prefix", "tenant:"}
			if lossless {
				args = append(args, "--lossless")
			}
			cmd.SetArgs(append(args, "--", "SCAN", "0"))
			if err := cmd.Execute(); err == nil || out.Len() != 0 {
				t.Fatalf("malformed SCAN success (lossless=%v): %q %v", lossless, out.String(), err)
			}
		}
	}
}
