package commands

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A tiny RESP2 peer tests the command's actual transport/JSON boundary without
// depending on optional Redis services. Real Redis/ACL acceptance lives in the
// Redis journey; this peer gives deterministic protocol and output failures.
func redisCommandPeer(t *testing.T, reply string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
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
			case "GET":
				response = reply
			}
			if _, err = io.WriteString(conn, response); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
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
