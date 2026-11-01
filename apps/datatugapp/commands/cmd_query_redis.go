package commands

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/dal-go/dalgo2redis"
	"github.com/spf13/cobra"
)

// queryRedisCommand runs explicit native Redis reads. This separate command does
// not advertise SQL/TugQL or application policy enforcement for Redis keys.
func queryRedisCommand() *cobra.Command {
	var source string
	var scanPrefix string
	var lossless bool
	cmd := &cobra.Command{
		Use:   "redis --db <URL|env:NAME> -- COMMAND key|cursor [arguments ...]",
		Short: "Read Redis keys using native read-only commands",
		Long: `Read Redis strings, hash fields and metadata using native commands.
Allowed: GET, MGET, HGET, HMGET, HEXISTS, EXISTS, TYPE, TTL, PTTL, STRLEN,
HLEN, LLEN, SCARD, ZCARD and ZSCORE. SCAN requires --scan-prefix and returns one
page under that literal prefix; COUNT is a hint, not a result limit.
Writes, scripts, KEYS and blocking reads are rejected.
Default output is unchanged JSON text/numbers. --lossless encodes bulk bytes as
base64 and integers as decimal strings in tagged objects; missing values are null.
Redis ACLs control access; DataTug application policies are not applied here.
Use a dedicated read-only ACL user and rediss:// for verified TLS.
Credentials can be supplied via env:NAME. Calls time out after ten seconds.

Examples:
  datatug query redis --db env:REDIS_URL -- GET customer:42
  datatug query redis --db env:REDIS_URL -- HMGET customer:42 name email
  datatug query redis --db env:REDIS_URL --lossless --scan-prefix customer: -- SCAN 0 COUNT 100`,
		SilenceUsage: true,
		Args:         cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			options := dalgo2redis.Options{ScanPrefix: scanPrefix}
			if err := dalgo2redis.ValidateCommandWithOptions(args, options); err != nil {
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
			client, err := dalgo2redis.OpenWithOptions(ctx, raw, options)
			if err != nil {
				return Exit(err.Error(), exitCodeDatabase)
			}
			defer func() { _ = client.Close() }()
			var value any
			if lossless {
				value, err = client.QueryNative(ctx, args...)
			} else {
				value, err = client.Query(ctx, args...)
			}
			if err != nil {
				return Exit(err.Error(), exitCodeDatabase)
			}
			if err := ctx.Err(); err != nil {
				return Exit(err.Error(), exitCodeDatabase)
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(value)
		},
	}
	cmd.Flags().StringVar(&source, "db", "", "Redis URL (redis:// or rediss://), or env:NAME containing the URL")
	cmd.Flags().BoolVar(&lossless, "lossless", false, "Preserve Redis bulk bytes and int64 values with tagged JSON")
	cmd.Flags().StringVar(&scanPrefix, "scan-prefix", "", "Trusted literal prefix enabling one SCAN page; Redis ACLs still govern key reads")
	return cmd
}
