package commands

import (
	"context"
	"strings"
	"testing"
)

func athenaReplaceOnce(t *testing.T, source, old, next string) string {
	t.Helper()
	if count := strings.Count(source, old); count != 1 {
		t.Fatalf("mutation target appears %d times, want once", count)
	}
	return strings.Replace(source, old, next, 1)
}

func TestAthenaConfigStrictJSON(t *testing.T) {
	replace := func(old, next string) string {
		return athenaReplaceOnce(t, athenaConfigTestJSON, old, next)
	}
	const identity = `"subject":"reader","generation":"one","accessKeyIdEnv":"ATHENA_TEST_ACCESS","secretAccessKeyEnv":"ATHENA_TEST_SECRET"`
	cases := []struct{ name, raw string }{
		{"duplicate top region", replace(`"region":"us-east-1",`, `"region":"us-east-1","region":"us-east-1",`)},
		{"wrong-case Region", replace(`"region":`, `"Region":`)},
		{"unknown private control", replace(`"region":`, `"PRIVATE_CONTROL":"x","region":`)},
		{"null identity", replace(`"identity":{`+identity+`}`, `"identity":null`)},
		{"wrong-case nested access key env", replace(`"accessKeyIdEnv"`, `"accessKeyIDEnv"`)},
		{"duplicate encryption option", replace(`"encryption":{"option":"SSE_S3"}`, `"encryption":{"option":"SSE_S3","option":"SSE_S3"}`)},
		{"null table value", replace(`"tables":{"items":{`, `"tables":{"items":null,"z":{`)},
		{"null column", replace(`{"name":"id","type":"varchar"}`, `null`)},
		{"duplicate column names", replace(`"name":"id"`, `"name":"amount"`)},
		{"trailing second object", athenaConfigTestJSON + "{}"},
		{"malformed object", strings.TrimSuffix(athenaConfigTestJSON, "}")},
		{"invalid utf8 source id", replace(`"sourceId":"owned"`, "\"sourceId\":\"\xff\"")},
		{"lone surrogate source id", replace(`"sourceId":"owned"`, `"sourceId":"\ud800"`)},
		{"non-object root", `[]`},
		{"empty root", `{}`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := decodeAthenaConfig(context.Background(), []byte(test.raw))
			if err != errAthenaConfig {
				t.Fatalf("err = %v, want errAthenaConfig", err)
			}
			athenaZeroConfig(t, cfg)
		})
	}
	t.Run("escaped astral unicode source id", func(t *testing.T) {
		raw := replace(`"sourceId":"owned"`, `"sourceId":"\ud83d\ude00"`)
		cfg, err := decodeAthenaConfig(context.Background(), []byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.SourceID != "\U0001F600" {
			t.Fatalf("source id = %q", cfg.SourceID)
		}
	})
}

func TestAthenaConfigS3Authority(t *testing.T) {
	replace := func(location string) string {
		return athenaReplaceOnce(t, athenaConfigTestJSON, `"outputLocation":"s3://owned-results/athena//"`, `"outputLocation":"`+location+`"`)
	}
	ctx := context.Background()
	for _, location := range []string{
		"s3://owned-results:/athena/",
		"s3://:/athena/",
		"s3://owned-results:443/athena/",
		"s3://[::1]/athena/",
	} {
		t.Run("reject "+location, func(t *testing.T) {
			cfg, err := decodeAthenaConfig(ctx, []byte(replace(location)))
			if err != errAthenaConfig {
				t.Fatalf("err = %v, want errAthenaConfig", err)
			}
			athenaZeroConfig(t, cfg)
		})
	}
	for _, location := range []string{
		"s3://owned-results/athena/",
		"s3://owned.results/athena/",
	} {
		t.Run("accept "+location, func(t *testing.T) {
			cfg, err := decodeAthenaConfig(ctx, []byte(replace(location)))
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if cfg.OutputLocation != location {
				t.Fatalf("output location = %q, want %q", cfg.OutputLocation, location)
			}
		})
	}
}

func TestAthenaConfigScanBytes(t *testing.T) {
	scan := func(literal string) string {
		return athenaReplaceOnce(t, athenaConfigTestJSON, `"maxScanBytes":"10000000"`, `"maxScanBytes":`+literal)
	}
	for _, test := range []struct {
		name    string
		literal string
		want    int64
		ok      bool
	}{
		{"threshold", `"10000000"`, 10000000, true},
		{"maximum int64", `"9223372036854775807"`, 9223372036854775807, true},
		{"below threshold", `"9999999"`, 0, false},
		{"int64 overflow", `"9223372036854775808"`, 0, false},
		{"zero", `"0"`, 0, false},
		{"leading zero", `"010000000"`, 0, false},
		{"plus sign", `"+10000000"`, 0, false},
		{"minus sign", `"-10000000"`, 0, false},
		{"exponent", `"1e7"`, 0, false},
		{"decimal point", `"10000000.0"`, 0, false},
		{"leading space", `" 10000000"`, 0, false},
		{"numeric literal", `10000000`, 0, false},
		{"null", `null`, 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := decodeAthenaConfig(context.Background(), []byte(scan(test.literal)))
			if test.ok {
				if err != nil || cfg.MaxScanBytes != test.want {
					t.Fatalf("scan = %d, err = %v, want %d", cfg.MaxScanBytes, err, test.want)
				}
				return
			}
			if err != errAthenaConfig {
				t.Fatalf("err = %v, want errAthenaConfig", err)
			}
			athenaZeroConfig(t, cfg)
		})
	}
}
