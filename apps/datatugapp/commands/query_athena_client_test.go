package commands

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	athenasdk "github.com/dal-go/dalgo2athena"
)

func athenaClientTestServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
		t.Error("unexpected Athena HTTP invocation")
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func athenaClientTestCA(t *testing.T, server *httptest.Server) string {
	t.Helper()
	return dynamoTestFile(t, "athena-client-ca.pem",
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
}

func TestAthenaClientLazyOwnedConstruction(t *testing.T) {
	server, calls := athenaClientTestServer(t)
	ca := athenaClientTestCA(t, server)
	for _, test := range []struct {
		name   string
		caFile string
	}{
		{"system_roots", ""},
		{"replacement_roots", ca},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := athenaOptionsTestConfig(t)
			athenaBoundaryUnsetEnv(t, cfg.Identity.AccessKeyIDEnv)
			athenaBoundaryUnsetEnv(t, cfg.Identity.SecretAccessKeyEnv)
			cfg.CAFile = test.caFile
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			client, cleanup, err := openAthenaClient(ctx, cfg, 5*time.Second)
			if err != nil || client == nil || cleanup == nil {
				t.Fatal("real Athena client construction failed")
			}
			t.Cleanup(func() {
				if client != nil {
					shutdown, stop := context.WithTimeout(context.Background(), 2*time.Second)
					_ = client.Shutdown(shutdown)
					stop()
				}
				if cleanup != nil {
					cleanup()
				}
			})
			metadata := client.ConfiguredMetadata()
			table, ok := metadata.Tables["items"]
			configured := cfg.Tables["items"]
			if metadata.DynamicDiscovery ||
				metadata.Provenance != "trusted configuration, not live catalog/IAM or spend proof" ||
				!ok || len(metadata.Tables) != len(cfg.Tables) ||
				table.Name != configured.Name || len(table.Columns) != len(configured.Columns) {
				t.Fatal("configured metadata mapping or provenance changed")
			}
			for i, column := range table.Columns {
				if column.Name != configured.Columns[i].Name || column.Type != configured.Columns[i].Type {
					t.Fatal("configured metadata column mapping changed")
				}
			}
			shutdown, stop := context.WithTimeout(context.Background(), 2*time.Second)
			defer stop()
			if err := client.Shutdown(shutdown); err != nil {
				t.Fatal("real Athena client shutdown failed")
			}
			client = nil
			cleanup()
			cleanup = nil
			if calls.Load() != 0 {
				t.Fatal("client construction or shutdown dispatched HTTP")
			}
		})
	}
}

func TestAthenaClientRefusesCAAndInvalidConstructorOptions(t *testing.T) {
	_, calls := athenaClientTestServer(t)
	missing := filepath.Join(t.TempDir(), "missing-ca.pem")
	invalidPEM := dynamoTestFile(t, "athena-invalid-ca.pem", []byte("PRIVATE_CA_BYTES"))
	oversized := dynamoTestFile(t, "athena-oversized-ca.pem", []byte(strings.Repeat("A", (1<<20)+1)))
	for _, test := range []struct {
		name    string
		caFile  string
		private string
	}{
		{"missing_path", missing, missing},
		{"invalid_pem", invalidPEM, "PRIVATE_CA_BYTES"},
		{"oversized_regular", oversized, ""},
		{"directory", t.TempDir(), ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := athenaOptionsTestConfig(t)
			cfg.CAFile = test.caFile
			client, cleanup, err := openAthenaClient(context.Background(), cfg, 5*time.Second)
			if err == nil || client != nil || cleanup != nil {
				t.Fatal("invalid CA was not refused without resources")
			}
			if test.private != "" && strings.Contains(err.Error(), test.private) {
				t.Fatal("CA path or PEM contents leaked into the error")
			}
		})
	}
	for _, timeout := range []time.Duration{0, 120*time.Second + time.Nanosecond} {
		t.Run("timeout/"+timeout.String(), func(t *testing.T) {
			cfg := athenaOptionsTestConfig(t)
			cfg.CAFile = missing
			client, cleanup, err := openAthenaClient(context.Background(), cfg, timeout)
			if err != errAthenaConfig || client != nil || cleanup != nil {
				t.Fatal("invalid timeout did not refuse before CA read")
			}
		})
	}
	t.Run("sdk_constructor", func(t *testing.T) {
		cfg := athenaOptionsTestConfig(t)
		cfg.Tables = nil
		if _, err := athenaClientOptions(context.Background(), cfg, 5*time.Second); err != nil {
			t.Fatal("valid options-helper input was refused before the SDK constructor")
		}
		client, cleanup, err := openAthenaClient(context.Background(), cfg, 5*time.Second)
		var typed *athenasdk.Error
		if err == nil || !errors.As(err, &typed) || typed.Code == "" || client != nil || cleanup != nil {
			t.Fatal("real SDK constructor did not reject empty tables with a typed error")
		}
	})
	if calls.Load() != 0 {
		t.Fatal("refused Athena client configuration dispatched HTTP")
	}
}

func TestAthenaClientCallerPriorityAndZeroOwnership(t *testing.T) {
	_, calls := athenaClientTestServer(t)
	canceled, cancel := context.WithCancelCause(context.Background())
	defer func() { cancel(nil) }()
	cancel(errors.New("PRIVATE_CALLER_CAUSE"))
	expired, stop := context.WithDeadlineCause(context.Background(),
		time.Now().Add(-time.Second), errors.New("PRIVATE_DEADLINE_CAUSE"))
	defer stop()
	var dynamicNil context.Context
	missing := filepath.Join(t.TempDir(), "missing-ca.pem")
	for _, test := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"canceled", canceled, context.Canceled},
		{"expired", expired, context.DeadlineExceeded},
		{"nil", dynamicNil, errAthenaConfig},
	} {
		for _, shape := range []struct {
			name    string
			caFile  string
			timeout time.Duration
		}{
			{"valid", "", 5 * time.Second},
			{"invalid", missing, 0},
		} {
			t.Run(test.name+"/"+shape.name, func(t *testing.T) {
				cfg := athenaOptionsTestConfig(t)
				cfg.CAFile = shape.caFile
				client, cleanup, err := openAthenaClient(test.ctx, cfg, shape.timeout)
				if err != test.want || client != nil || cleanup != nil {
					t.Fatal("caller context did not override Athena configuration exactly")
				}
				if strings.Contains(err.Error(), "PRIVATE") {
					t.Fatal("caller cause leaked into the error")
				}
			})
		}
	}
	if calls.Load() != 0 {
		t.Fatal("caller-priority refusal dispatched HTTP")
	}
}
