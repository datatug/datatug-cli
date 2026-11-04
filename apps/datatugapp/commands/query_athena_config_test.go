package commands

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

const athenaConfigTestJSON = `{"region":"us-east-1","accountId":"111111111111","sourceId":"owned","policyRevision":"v1","workGroup":"readers","catalog":"AwsDataCatalog","database":"native","outputLocation":"s3://owned-results/athena//","expectedBucketOwner":"111111111111","encryption":{"option":"SSE_S3"},"maxScanBytes":"10000000","tables":{"items":{"name":"actual","columns":[{"name":"id","type":"varchar"},{"name":"amount","type":"decimal(38,10)"}]}},"identity":{"subject":"reader","generation":"one","accessKeyIdEnv":"ATHENA_TEST_ACCESS","secretAccessKeyEnv":"ATHENA_TEST_SECRET"}}`

type athenaCancelContext struct {
	context.Context
}

func (c athenaCancelContext) Err() error {
	err := c.Context.Err()
	if err == nil {
		return nil
	}
	return fmt.Errorf("PRIVATE_WRAPPER: %w", err)
}

func athenaZeroConfig(t *testing.T, cfg athenaNativeConfig) {
	t.Helper()
	if !reflect.DeepEqual(cfg, athenaNativeConfig{}) {
		t.Fatalf("config = %#v, want zero value", cfg)
	}
}

func TestAthenaConfigGolden(t *testing.T) {
	data := []byte(athenaConfigTestJSON)
	cfg, err := decodeAthenaConfig(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Partition != "aws" || cfg.Region != "us-east-1" || cfg.AccountID != "111111111111" {
		t.Fatalf("partition/region/account = %q/%q/%q", cfg.Partition, cfg.Region, cfg.AccountID)
	}
	if cfg.SourceID != "owned" || cfg.PolicyRevision != "v1" || cfg.WorkGroup != "readers" {
		t.Fatalf("source/policy/workgroup = %q/%q/%q", cfg.SourceID, cfg.PolicyRevision, cfg.WorkGroup)
	}
	if cfg.Catalog != "AwsDataCatalog" || cfg.Database != "native" || cfg.OutputLocation != "s3://owned-results/athena//" {
		t.Fatalf("catalog/database/output = %q/%q/%q", cfg.Catalog, cfg.Database, cfg.OutputLocation)
	}
	if cfg.ExpectedBucketOwner != "111111111111" || cfg.MaxScanBytes != int64(10000000) {
		t.Fatalf("bucket/scan = %q/%d", cfg.ExpectedBucketOwner, cfg.MaxScanBytes)
	}
	if cfg.Encryption != (athenaEncryptionConfig{Option: "SSE_S3"}) {
		t.Fatalf("encryption = %#v", cfg.Encryption)
	}
	table, ok := cfg.Tables["items"]
	wantColumns := []athenaColumnConfig{{Name: "id", Type: "varchar"}, {Name: "amount", Type: "decimal(38,10)"}}
	if !ok || len(cfg.Tables) != 1 || table.Name != "actual" || table.IDColumn != "" || !reflect.DeepEqual(table.Columns, wantColumns) {
		t.Fatalf("table = %#v", table)
	}
	wantIdentity := athenaIdentityConfig{Subject: "reader", Generation: "one", AccessKeyIDEnv: "ATHENA_TEST_ACCESS", SecretAccessKeyEnv: "ATHENA_TEST_SECRET"}
	if cfg.Identity != wantIdentity || cfg.CAFile != "" || cfg.Identity.SessionTokenEnv != "" {
		t.Fatalf("identity/ca = %#v/%q", cfg.Identity, cfg.CAFile)
	}
	snapshot := fmt.Sprintf("%#v", cfg)
	for i := range data {
		data[i] = 'x'
	}
	if fmt.Sprintf("%#v", cfg) != snapshot {
		t.Fatal("config changed after mutating input buffer")
	}
}

func TestAthenaConfigCanceled(t *testing.T) {
	parent, cancel := context.WithCancelCause(context.Background())
	defer func() { cancel(nil) }()
	cancel(errors.New("PRIVATE_CAUSE"))
	wrapped := athenaCancelContext{Context: parent}
	for _, test := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"native", parent, context.Canceled},
		{"wrapped", wrapped, context.Canceled},
		{"nil", nil, errAthenaConfig},
	} {
		for _, raw := range []string{athenaConfigTestJSON, "{"} {
			t.Run(test.name+"/"+raw[:1], func(t *testing.T) {
				cfg, err := decodeAthenaConfig(test.ctx, []byte(raw))
				if err != test.want {
					t.Fatalf("err = %v, want exact %v", err, test.want)
				}
				athenaZeroConfig(t, cfg)
			})
		}
	}
}

func TestAthenaConfigDeadline(t *testing.T) {
	for _, name := range []string{"native", "wrapped"} {
		for _, raw := range []string{athenaConfigTestJSON, "{"} {
			t.Run(name+"/"+raw[:1], func(t *testing.T) {
				parent, cancel := context.WithDeadlineCause(context.Background(), time.Now().Add(-time.Second), errors.New("PRIVATE_DEADLINE_CAUSE"))
				defer cancel()
				ctx := parent
				if name == "wrapped" {
					ctx = athenaCancelContext{Context: parent}
				}
				cfg, err := decodeAthenaConfig(ctx, []byte(raw))
				if err != context.DeadlineExceeded {
					t.Fatalf("err = %v, want exact context.DeadlineExceeded", err)
				}
				athenaZeroConfig(t, cfg)
			})
		}
	}
}
