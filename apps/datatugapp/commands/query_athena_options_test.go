package commands

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	athenasdk "github.com/dal-go/dalgo2athena"
)

func athenaOptionsTestConfig(t *testing.T) athenaNativeConfig {
	t.Helper()
	cfg, err := decodeAthenaConfig(context.Background(), []byte(athenaConfigTestJSON))
	if err != nil {
		t.Fatal("failed to decode golden configuration")
	}
	return cfg
}

func TestAthenaOptionsOwnedMapping(t *testing.T) {
	cfg := athenaOptionsTestConfig(t)
	cfg.CAFile = "/nonexistent/athena-owned-ca.pem"
	mapped, err := athenaClientOptions(context.Background(), cfg, 10*time.Second)
	if err != nil {
		t.Fatal("failed to map the golden configuration")
	}
	if mapped.Identity == nil || mapped.Transport != nil || mapped.ImmediatePolling {
		t.Fatal("mapped identity, transport or polling is unexpected")
	}
	if mapped.Partition != "aws" || mapped.Region != "us-east-1" || mapped.AccountID != "111111111111" ||
		mapped.SourceID != "owned" || mapped.PolicyRevision != "v1" || mapped.WorkGroup != "readers" ||
		mapped.Catalog != "AwsDataCatalog" || mapped.Database != "native" ||
		mapped.OutputLocation != "s3://owned-results/athena//" || mapped.ExpectedBucketOwner != "111111111111" ||
		mapped.MaxScanBytes != 10000000 || mapped.Encryption.Option != "SSE_S3" || mapped.Encryption.KMSKey != "" {
		t.Fatal("mapped connection fields do not match the golden configuration")
	}
	wantColumns := []athenasdk.Column{{Name: "id", Type: "varchar"}, {Name: "amount", Type: "decimal(38,10)"}}
	table, ok := mapped.Tables["items"]
	if !ok || len(mapped.Tables) != 1 || table.Name != "actual" || table.IDColumn != "" ||
		!reflect.DeepEqual(table.Columns, wantColumns) {
		t.Fatal("mapped table does not match the golden configuration")
	}
	original := cfg.Tables["items"]
	original.Columns[0].Name, original.Columns[0].Type = "mutated", "int"
	delete(cfg.Tables, "items")
	cfg.Tables["replacement"] = athenaTableConfig{Name: "replacement", Columns: []athenaColumnConfig{{Name: "x", Type: "int"}}}
	table, ok = mapped.Tables["items"]
	_, replacement := mapped.Tables["replacement"]
	if !ok || replacement || len(mapped.Tables) != 1 || table.Name != "actual" || table.IDColumn != "" ||
		!reflect.DeepEqual(table.Columns, wantColumns) {
		t.Fatal("mapped options changed after mutating the input configuration")
	}
	client, err := athenasdk.New(mapped)
	if client != nil {
		t.Cleanup(func() {
			if client != nil {
				_ = client.Close()
			}
		})
	}
	if err != nil || client == nil {
		t.Fatal("real SDK constructor rejected the mapped options")
	}
	if err := client.Close(); err != nil {
		t.Fatal("real SDK client failed to close")
	}
	client = nil
}

func TestAthenaOptionsRealSDKConstructorBoundaries(t *testing.T) {
	t.Setenv("ATHENA_TEST_ACCESS", "")
	t.Setenv("ATHENA_TEST_SECRET", "")
	for _, row := range []struct {
		timeout time.Duration
		poll    time.Duration
	}{
		{time.Millisecond, time.Millisecond},
		{10 * time.Second, 100 * time.Millisecond},
		{120 * time.Second, 100 * time.Millisecond},
	} {
		t.Run(row.timeout.String(), func(t *testing.T) {
			mapped, err := athenaClientOptions(context.Background(), athenaOptionsTestConfig(t), row.timeout)
			if err != nil {
				t.Fatal("failed to map the golden configuration")
			}
			if mapped.Identity == nil || mapped.Transport != nil {
				t.Fatal("mapped identity or transport is unexpected")
			}
			if mapped.Timeout != row.timeout || mapped.CursorTTL != row.timeout || mapped.PollInterval != row.poll {
				t.Fatal("mapped durations do not match the expected profile")
			}
			if mapped.MaxConcurrent != 1 || mapped.MaxRows != 1000 || mapped.MaxColumns != 128 ||
				mapped.MaxPageNodes != 8192 || mapped.MaxCursorNodes != 65536 {
				t.Fatal("mapped limits do not match the hard policy")
			}
			mappedIdentity := mapped.Identity
			var calls atomic.Int64
			mapped.Identity = func(ctx context.Context) (athenasdk.Identity, error) {
				calls.Add(1)
				return mappedIdentity(ctx)
			}
			var client *athenasdk.Client
			t.Cleanup(func() {
				if client != nil {
					_ = client.Close()
				}
			})
			client, err = athenasdk.New(mapped)
			if err != nil || client == nil {
				t.Fatal("real SDK constructor rejected the mapped options")
			}
			if calls.Load() != 0 {
				t.Fatal("real SDK constructor invoked the identity provider")
			}
			if err := client.Close(); err != nil {
				t.Fatal("real SDK client failed to close")
			}
			client = nil
			if calls.Load() != 0 {
				t.Fatal("real SDK client close invoked the identity provider")
			}
		})
	}
}

func TestAthenaOptionsTimeoutAndCallerRefusals(t *testing.T) {
	cfg := athenaOptionsTestConfig(t)
	for _, timeout := range []time.Duration{
		0,
		-time.Millisecond,
		time.Millisecond - time.Nanosecond,
		120*time.Second + time.Nanosecond,
	} {
		t.Run("timeout/"+timeout.String(), func(t *testing.T) {
			got, err := athenaClientOptions(context.Background(), cfg, timeout)
			if err != errAthenaConfig {
				t.Fatal("invalid timeout did not return the exact configuration error")
			}
			if !reflect.DeepEqual(got, athenasdk.Options{}) {
				t.Fatal("invalid timeout returned non-zero options")
			}
		})
	}
	canceled, cancel := context.WithCancelCause(context.Background())
	defer func() { cancel(nil) }()
	cancel(errors.New("PRIVATE_CAUSE"))
	expired, stop := context.WithDeadlineCause(context.Background(), time.Now().Add(-time.Second), errors.New("PRIVATE_DEADLINE_CAUSE"))
	defer stop()
	var nilContext context.Context
	for _, test := range []struct {
		name    string
		ctx     context.Context
		timeout time.Duration
		want    error
	}{
		{"canceled", canceled, time.Second, context.Canceled},
		{"deadline", expired, time.Second, context.DeadlineExceeded},
		{"wrapped_invalid", athenaCancelContext{Context: canceled}, 0, context.Canceled},
		{"nil", nilContext, time.Second, errAthenaConfig},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := athenaClientOptions(test.ctx, cfg, test.timeout)
			if err != test.want {
				t.Fatal("caller context did not return the exact expected error")
			}
			if !reflect.DeepEqual(got, athenasdk.Options{}) {
				t.Fatal("caller context returned non-zero options")
			}
		})
	}
}
