package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestQueryRedisRejectsUnsafeCommandsBeforeOpening(t *testing.T) {
	for _, args := range [][]string{{"SET", "key", "value"}, {"EVAL", "return 1", "0"}, {"KEYS", "*"}, {"GET", "a", "b"}} {
		cmd := queryRedisCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(append([]string{"--db", "redis://secret:password@invalid.invalid/0", "--"}, args...))
		err := cmd.Execute()
		if err == nil {
			t.Fatalf("accepted %v", args)
		}
		if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "password") || strings.Contains(err.Error(), "connection failed") {
			t.Fatal(err)
		}
	}
}
func TestQueryRedisMissingEnvironment(t *testing.T) {
	cmd := queryRedisCommand()
	cmd.SetArgs([]string{"--db", "env:DATATUG_REDIS_TEST_UNSET", "--", "GET", "key"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unset or empty") {
		t.Fatal(err)
	}
}
func TestQueryRedisIntegration(t *testing.T) {
	raw := os.Getenv("REDIS_TEST_URL")
	if raw == "" {
		t.Skip("set REDIS_TEST_URL for real Redis CLI tests")
	}
	ctx := context.Background()
	opts, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	seed := redis.NewClient(opts)
	defer func() { _ = seed.Close() }()
	key := "datatug-cli-test:" + time.Now().Format("150405.000000000")
	if err = seed.Set(ctx, key, "hello", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	defer seed.Del(ctx, key)
	t.Setenv("DATATUG_REDIS_TEST_URL", raw)
	root := queryCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"redis", "--db", "env:DATATUG_REDIS_TEST_URL", "--", "MGET", key, key + ":missing"})
	if err = root.Execute(); err != nil {
		t.Fatal(err)
	}
	var got []any
	if err = json.Unmarshal(out.Bytes(), &got); err != nil || len(got) != 2 || got[0] != "hello" || got[1] != nil {
		t.Fatalf("%q %v", out.String(), err)
	}
	refusal := queryCommand()
	refusal.SetOut(&out)
	refusal.SetErr(&out)
	refusal.SetArgs([]string{"redis", "--db", "env:DATATUG_REDIS_TEST_URL", "--", "SET", key, "changed"})
	if err = refusal.Execute(); err == nil {
		t.Fatal("wired command accepted mutation")
	}
	if got := seed.Get(ctx, key).Val(); got != "hello" {
		t.Fatalf("mutation changed stored value: %q", got)
	}

}
