package commands

import (
	"context"
	"encoding/json"
	"testing"
)

func athenaContractObject(t *testing.T) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal([]byte(athenaConfigTestJSON), &object); err != nil {
		t.Fatalf("decode golden fixture: %v", err)
	}
	return object
}

func athenaContractRaw(t *testing.T, object map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(object)
	if err != nil {
		t.Fatalf("encode mutant fixture: %v", err)
	}
	return raw
}

func TestAthenaConfigTrustedScalarContracts(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		field  string
		value  any
		absent bool
	}{
		{"partition boolean false", "partition", false, true},
		{"region boolean false", "region", false, false},
		{"accountId boolean false", "accountId", false, false},
		{"sourceId boolean false", "sourceId", false, false},
		{"policyRevision boolean false", "policyRevision", false, false},
		{"workGroup boolean false", "workGroup", false, false},
		{"catalog boolean false", "catalog", false, false},
		{"database boolean false", "database", false, false},
		{"outputLocation boolean false", "outputLocation", false, false},
		{"expectedBucketOwner boolean false", "expectedBucketOwner", false, false},
		{"partition unknown", "partition", "aws-other", true},
		{"region malformed", "region", "us-east-x", false},
		{"accountId not twelve digits", "accountId", "11111111111", false},
		{"sourceId empty", "sourceId", "", false},
		{"policyRevision empty", "policyRevision", "", false},
		{"catalog empty", "catalog", "", false},
		{"database empty", "database", "", false},
		{"workGroup wildcard", "workGroup", "read*ers", false},
		{"outputLocation https", "outputLocation", "https://owned-results/athena/", false},
		{"expectedBucketOwner not twelve digits", "expectedBucketOwner", "11111111111", false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			object := athenaContractObject(t)
			if _, present := object[test.field]; present == test.absent {
				t.Fatalf("mutation target %q present = %v, want absent = %v", test.field, present, test.absent)
			}
			object[test.field] = test.value
			cfg, err := decodeAthenaConfig(ctx, athenaContractRaw(t, object))
			if err != errAthenaConfig {
				t.Fatalf("err = %v, want errAthenaConfig", err)
			}
			athenaZeroConfig(t, cfg)
		})
	}
}

func TestAthenaConfigPartitionAndEncryptionContracts(t *testing.T) {
	ctx := context.Background()
	partitions := []struct{ name, partition, region string }{
		{"aws us-east-1", "aws", "us-east-1"},
		{"aws-cn cn-north-1", "aws-cn", "cn-north-1"},
		{"aws-us-gov us-gov-west-1", "aws-us-gov", "us-gov-west-1"},
	}
	for _, test := range partitions {
		t.Run(test.name, func(t *testing.T) {
			object := athenaContractObject(t)
			object["partition"] = test.partition
			object["region"] = test.region
			cfg, err := decodeAthenaConfig(ctx, athenaContractRaw(t, object))
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if cfg.Partition != test.partition || cfg.Region != test.region {
				t.Fatalf("partition/region = %q/%q, want %q/%q", cfg.Partition, cfg.Region, test.partition, test.region)
			}
		})
	}
	crossed := []struct{ name, partition, region string }{
		{"aws/cn", "aws", "cn-north-1"},
		{"aws/gov", "aws", "us-gov-west-1"},
		{"cn/us-east", "aws-cn", "us-east-1"},
		{"gov/us-east", "aws-us-gov", "us-east-1"},
	}
	for _, test := range crossed {
		t.Run(test.name, func(t *testing.T) {
			object := athenaContractObject(t)
			object["partition"] = test.partition
			object["region"] = test.region
			cfg, err := decodeAthenaConfig(ctx, athenaContractRaw(t, object))
			if err != errAthenaConfig {
				t.Fatalf("err = %v, want errAthenaConfig", err)
			}
			athenaZeroConfig(t, cfg)
		})
	}
	const key = "arn:aws:kms:us-east-1:111111111111:key/11111111-2222-3333-4444-555555555555"
	t.Run("SSE_KMS bounded key", func(t *testing.T) {
		object := athenaContractObject(t)
		object["encryption"] = map[string]any{"option": "SSE_KMS", "kmsKey": key}
		cfg, err := decodeAthenaConfig(ctx, athenaContractRaw(t, object))
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if cfg.Encryption != (athenaEncryptionConfig{Option: "SSE_KMS", KMSKey: key}) {
			t.Fatalf("encryption = %#v", cfg.Encryption)
		}
	})
	refused := []struct {
		name       string
		encryption any
	}{
		{"missing key", map[string]any{"option": "SSE_KMS"}},
		{"empty key", map[string]any{"option": "SSE_KMS", "kmsKey": ""}},
		{"numeric key", map[string]any{"option": "SSE_KMS", "kmsKey": 123}},
		{"option wrong type", map[string]any{"option": false, "kmsKey": key}},
		{"option missing", map[string]any{"kmsKey": key}},
		{"unknown option", map[string]any{"option": "CSE_KMS", "kmsKey": key}},
		{"SSE_S3 empty key", map[string]any{"option": "SSE_S3", "kmsKey": ""}},
		{"SSE_S3 key", map[string]any{"option": "SSE_S3", "kmsKey": key}},
	}
	for _, test := range refused {
		t.Run(test.name, func(t *testing.T) {
			object := athenaContractObject(t)
			object["encryption"] = test.encryption
			cfg, err := decodeAthenaConfig(ctx, athenaContractRaw(t, object))
			if err != errAthenaConfig {
				t.Fatalf("err = %v, want errAthenaConfig", err)
			}
			athenaZeroConfig(t, cfg)
		})
	}
}
