package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/dal-go/dalgo2redis"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/spf13/cobra"
)

type redisQueryClient interface {
	Query(context.Context, ...string) (any, error)
	QueryNative(context.Context, ...string) (dalgo2redis.NativeValue, error)
	Close() error
}

type redisRESTOpener func(string, dalgo2redis.RESTOptions) (redisQueryClient, error)

// queryRedisCommand runs explicit native Redis reads. This separate command does
// not advertise SQL/TugQL or application policy enforcement for Redis keys.
func queryRedisCommand() *cobra.Command {
	return queryRedisCommandWithRESTOpener(func(endpoint string, options dalgo2redis.RESTOptions) (redisQueryClient, error) {
		return dalgo2redis.NewRESTClient(endpoint, options)
	})
}

// The opener seam lets transport fixtures use an owned TLS trust root without
// giving the product a TLS verification bypass or an HTTP endpoint mode.
func queryRedisCommandWithRESTOpener(openREST redisRESTOpener) *cobra.Command {
	var source string
	var scanPrefix string
	var restTokenEnv, scanTokenEnv string
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
Upstash REST requires an explicit upstash+https://host source and
--rest-token-env NAME. SCAN instead requires --scan-token-env NAME, naming a
separately scoped ACL token that permits SCAN, plus --scan-prefix. The default
Upstash read-only token forbids SCAN. No credential or KEYS fallback is used.

Examples:
  datatug query redis --db env:REDIS_URL -- GET customer:42
  datatug query redis --db env:REDIS_URL -- HMGET customer:42 name email
  datatug query redis --db env:REDIS_URL --lossless --scan-prefix customer: -- SCAN 0 COUNT 100
  datatug query redis --db upstash+https://example.upstash.io --rest-token-env UPSTASH_READ_TOKEN --lossless -- GET customer:42`,
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
			var client redisQueryClient
			var err error
			if strings.HasPrefix(raw, "upstash+") {
				if !strings.HasPrefix(raw, "upstash+https://") {
					return Exit("Upstash REST source must use upstash+https://", exitCodeUsage)
				}
				for _, name := range []string{restTokenEnv, scanTokenEnv} {
					if name != "" && !dbcopy.ValidEnvName(name) {
						return Exit("REST token flags require an environment variable name", exitCodeUsage)
					}
				}
				name := restTokenEnv
				if strings.EqualFold(args[0], "SCAN") {
					name = scanTokenEnv
					if name == "" {
						return Exit("Upstash REST SCAN requires --scan-token-env and --scan-prefix; no reader-token fallback", exitCodeUsage)
					}
				} else if name == "" {
					return Exit("Upstash REST reads require --rest-token-env", exitCodeUsage)
				}
				client, err = openREST(strings.TrimPrefix(raw, "upstash+"), dalgo2redis.RESTOptions{
					Options: options,
					TokenProvider: func(ctx context.Context) (string, error) {
						return redisRESTTokenFromEnv(ctx, name)
					},
				})
			} else {
				if cmd.Flags().Changed("rest-token-env") || cmd.Flags().Changed("scan-token-env") {
					return Exit("REST token flags require an upstash+https:// source", exitCodeUsage)
				}
				client, err = dalgo2redis.OpenWithOptions(ctx, raw, options)
			}
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
			return writeRedisResult(ctx, cmd.OutOrStdout(), value)
		},
	}
	cmd.Flags().StringVar(&source, "db", "", "Redis URL (redis://, rediss:// or upstash+https://), or env:NAME containing the URL")
	cmd.Flags().StringVar(&restTokenEnv, "rest-token-env", "", "Environment variable holding the scoped Upstash REST reader token (not a token value)")
	cmd.Flags().StringVar(&scanTokenEnv, "scan-token-env", "", "Separate environment variable holding an explicitly SCAN-permitted Upstash ACL token; no reader fallback")
	cmd.Flags().BoolVar(&lossless, "lossless", false, "Preserve Redis bulk bytes and int64 values with tagged JSON")
	cmd.Flags().StringVar(&scanPrefix, "scan-prefix", "", "Trusted literal prefix enabling one SCAN page; Redis ACLs still govern key reads")
	return cmd
}

// Resolve credentials inside the provider's bounded operation, never at command
// construction. Values and environment variable contents are absent from errors.
// The sole product caller is REST TokenProvider; acquireToken owns the fresh
// cancellation check after this callback returns, before using the token.
func redisRESTTokenFromEnv(ctx context.Context, name string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	value, ok := os.LookupEnv(name)
	if !ok || value == "" {
		return "", errors.New("upstash REST token environment variable is unset or empty")
	}
	return value, nil
}

// Serialize into bounded read-derived memory before the final delivery check.
// A cancellation during JSON marshaling must not emit a success payload. Once
// Write commits bytes, preserve its outcome rather than reporting a late abort.
func writeRedisResult(ctx context.Context, output io.Writer, value any) error {
	var encoded bytes.Buffer
	if err := json.NewEncoder(&encoded).Encode(value); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return Exit(err.Error(), exitCodeDatabase)
	}
	n, err := output.Write(encoded.Bytes())
	if err != nil {
		return err
	}
	if n < encoded.Len() {
		return io.ErrShortWrite
	}
	return nil
}
