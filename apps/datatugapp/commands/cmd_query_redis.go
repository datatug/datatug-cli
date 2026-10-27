package commands

import (
	"context"
	"encoding/json"
	"github.com/dal-go/dalgo2redis"
	"github.com/spf13/cobra"
	"os"
	"strings"
	"time"
)

// queryRedisCommand runs explicit native Redis reads. This separate command does
// not advertise SQL/TugQL or application policy enforcement for Redis keys.
func queryRedisCommand() *cobra.Command {
	var source string
	cmd := &cobra.Command{
		Use:   "redis --db <URL|env:NAME> -- COMMAND key [key|field ...]",
		Short: "Read Redis keys using native read-only commands",
		Long: `Read Redis strings, hash fields and metadata using native commands.
Allowed: GET, MGET, HGET, HMGET, HEXISTS, EXISTS, TYPE, TTL, PTTL, STRLEN,
HLEN, LLEN, SCARD and ZCARD. Writes, scripts, scans and blocking reads are rejected.
Output is one JSON value. Missing values are null; arrays preserve input order.
Redis ACLs control access; DataTug application policies are not applied here.
Use a dedicated read-only ACL user and rediss:// for verified TLS.
Credentials can be supplied via env:NAME. Calls time out after ten seconds.

Examples:
  datatug query redis --db env:REDIS_URL -- GET customer:42
  datatug query redis --db env:REDIS_URL -- HMGET customer:42 name email`,
		SilenceUsage: true,
		Args:         cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := dalgo2redis.ValidateCommand(args); err != nil {
				return Exit(err.Error(), exitCodeUsage)
			}
			raw := source
			if strings.HasPrefix(raw, "env:") {
				name := strings.TrimPrefix(raw, "env:")
				if name == "" {
					return Exit("--db env: requires an environment variable name", exitCodeUsage)
				}
				var ok bool
				raw, ok = os.LookupEnv(name)
				if !ok || raw == "" {
					return Exit("Redis connection environment variable is unset or empty", exitCodeUsage)
				}
			}
			if raw == "" {
				return Exit("--db is required", exitCodeUsage)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			client, err := dalgo2redis.Open(ctx, raw)
			if err != nil {
				return Exit(err.Error(), exitCodeDatabase)
			}
			defer client.Close()
			value, err := client.Query(ctx, args...)
			if err != nil {
				return Exit(err.Error(), exitCodeDatabase)
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(value)
		},
	}
	cmd.Flags().StringVar(&source, "db", "", "Redis URL (redis:// or rediss://), or env:NAME containing the URL")
	return cmd
}
