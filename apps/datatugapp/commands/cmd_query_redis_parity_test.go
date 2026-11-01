package commands

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

type redisNativeJSON struct {
	Kind   string            `json:"kind"`
	Value  string            `json:"value"`
	Base64 string            `json:"base64"`
	Values []redisNativeJSON `json:"values"`
}

func redisNativeBytes(t *testing.T, value redisNativeJSON) []byte {
	t.Helper()
	if value.Kind != "bytes" {
		t.Fatalf("expected bulk bytes, got %#v", value)
	}
	data, err := base64.StdEncoding.Strict().DecodeString(value.Base64)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestQueryRedisNativeParityIntegration(t *testing.T) {
	raw := os.Getenv("REDIS_TEST_URL")
	if raw == "" {
		t.Skip("set REDIS_TEST_URL to run against a disposable Redis fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	options, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatal("invalid fixture URL")
	}
	admin := redis.NewClient(options)
	defer admin.Close()
	prefix := fmt.Sprintf("cli-parity-%d:", time.Now().UnixNano())
	keys := []string{prefix + "binary", prefix + "integer", prefix + "empty"}
	for i := 0; i < 31; i++ {
		keys = append(keys, fmt.Sprintf("%sitem:%d", prefix, i))
	}
	foreign := "foreign-" + prefix
	defer admin.Del(context.Background(), append(append([]string(nil), keys...), foreign)...)
	for _, key := range append(append([]string(nil), keys...), foreign) {
		if err := admin.Set(ctx, key, "owned", 0).Err(); err != nil {
			t.Fatal(err)
		}
	}
	binary := []byte{0, 255, 128, 13, 10}
	if err := admin.Set(ctx, keys[0], binary, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := admin.Set(ctx, keys[1], "9223372036854775000", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := admin.Do(ctx, "PEXPIREAT", keys[1], "9223372036854775000").Err(); err != nil {
		t.Fatal(err)
	}
	if err := admin.Set(ctx, keys[2], "", 0).Err(); err != nil {
		t.Fatal(err)
	}
	username, password := prefix+"reader", "fixture-only-password"
	if err := admin.Do(ctx, "ACL", "SETUSER", username, "reset", "on", ">"+password, "~"+prefix+"*",
		"+@connection", "+get", "+mget", "+pttl", "+scan").Err(); err != nil {
		t.Fatal(err)
	}
	defer admin.Do(context.Background(), "ACL", "DELUSER", username)
	endpoint, err := url.Parse(raw)
	if err != nil {
		t.Fatal("invalid fixture URL")
	}
	endpoint.User = url.UserPassword(username, password)
	t.Setenv("REDIS_CLI_PARITY_URL", endpoint.String())
	run := func(args ...string) (string, error) {
		cmd := queryCommand()
		cmd.SetContext(ctx)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(io.Discard)
		flags := []string{"redis", "--db", "env:REDIS_CLI_PARITY_URL", "--lossless", "--scan-prefix", prefix, "--"}
		cmd.SetArgs(append(flags, args...))
		err := cmd.Execute()
		return out.String(), err
	}
	decode := func(args ...string) redisNativeJSON {
		t.Helper()
		text, err := run(args...)
		if err != nil {
			t.Fatal(err)
		}
		var value redisNativeJSON
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			t.Fatal("CLI output contains trailing JSON")
		}
		return value
	}
	if got := redisNativeBytes(t, decode("GET", keys[0])); !bytes.Equal(got, binary) {
		t.Fatalf("actual CLI binary bytes changed: %v", got)
	}
	if got := redisNativeBytes(t, decode("GET", keys[2])); len(got) != 0 {
		t.Fatal("empty bulk changed")
	}
	if got, err := run("GET", prefix+"missing"); err != nil || got != "null\n" {
		t.Fatalf("missing value: %q %v", got, err)
	}
	value := decode("PTTL", keys[1])
	integer, err := strconv.ParseInt(value.Value, 10, 64)
	if err != nil || value.Kind != "integer" || integer <= 9007199254740991 {
		t.Fatalf("actual CLI int64 lost: %#v %v", value, err)
	}
	cursor, pages := "0", 0
	seen := make(map[string]bool)
	for {
		pages++
		if pages > 256 {
			t.Fatal("fixture whole-scan work budget exceeded")
		}
		page := decode("SCAN", cursor, "COUNT", "1")
		if page.Kind != "array" || len(page.Values) != 2 || page.Values[1].Kind != "array" {
			t.Fatalf("invalid CLI scan page: %#v", page)
		}
		cursor = string(redisNativeBytes(t, page.Values[0]))
		if _, err := strconv.ParseUint(cursor, 10, 64); err != nil {
			t.Fatal("invalid CLI scan cursor")
		}
		for _, key := range page.Values[1].Values {
			name := string(redisNativeBytes(t, key))
			if !strings.HasPrefix(name, prefix) {
				t.Fatal("out-of-scope key returned")
			}
			seen[name] = true
		}
		if cursor == "0" {
			break
		}
	}
	if len(seen) != len(keys) || seen[foreign] {
		t.Fatalf("actual CLI SCAN got %d keys, want %d", len(seen), len(keys))
	}
	for _, key := range keys {
		if !seen[key] {
			t.Fatal("owned key absent from complete fixture scan")
		}
	}
	for _, args := range [][]string{
		{"GET", foreign}, {"SET", keys[0], "mutated"}, {"KEYS", "*"}, {"SCAN", "0", "MATCH", "*"},
	} {
		if output, err := run(args...); err == nil || output != "" || strings.Contains(err.Error(), password) {
			t.Fatalf("CLI denial leaked data: %q %v", output, err)
		}
	}
	if got := admin.Get(ctx, foreign).Val(); got != "owned" {
		t.Fatal("foreign fixture key changed")
	}
}
