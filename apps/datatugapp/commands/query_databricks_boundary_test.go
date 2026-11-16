package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	dbrsdk "github.com/dal-go/dalgo2databricks"
)

func TestDatabricksCommandTokenAndCAFailuresOwnResources(t *testing.T) {
	t.Setenv("DALGO_DATABRICKS_READ_TEST", "")
	for _, test := range []struct {
		name, config string
		timeout      time.Duration
		want         string
	}{
		{"missing-token", databricksTestConfigJSON, time.Second, "read failed"},
		{"short-valid-timeout", databricksTestConfigJSON, 100 * time.Millisecond, "read failed"},
		{"invalid-ca", strings.TrimSuffix(databricksTestConfigJSON, "}") + `,"caFile":"` + databricksTestFile(t, "NOT A CERTIFICATE") + `"}`, time.Second, "invalid or unreadable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd := queryDatabricksCommand()
			cmd.SetContext(context.Background())
			cmd.SetIn(strings.NewReader(`{"operation":"get","collection":"users","id":"a"}`))
			var out, diagnostic bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&diagnostic)
			err := runDatabricksCommand(cmd, databricksTestFile(t, test.config), "-", test.timeout)
			if err == nil || !strings.Contains(err.Error(), test.want) || out.Len() != 0 {
				t.Fatal("resource construction/token failure contract", err)
			}
		})
	}
	// Invoke RunE directly to cover a genuinely absent original Cobra context;
	// Execute normally substitutes Background before calling RunE.
	cmd := queryDatabricksCommand()
	cmd.SetIn(strings.NewReader(`{"operation":"metadata"}`))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Flags().Set("config", databricksTestFile(t, databricksTestConfigJSON)); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("request", "-"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.RunE(cmd, nil); err != nil || cmd.Context() != nil {
		t.Fatal("nil original context not restored", err)
	}
}

func TestDatabricksOpenAdmissionUsesRealSDKValidation(t *testing.T) {
	config := databricksTestConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if client, closeTransport, err := openDatabricksClient(ctx, config, time.Second); !errors.Is(err, context.Canceled) || client != nil || closeTransport != nil {
		t.Fatal("preabort opened resources", err)
	}
	config.Tables = nil
	if client, closeTransport, err := openDatabricksClient(context.Background(), config, time.Second); err == nil || client != nil || closeTransport != nil {
		t.Fatal("invalid native mapping opened client", err)
	}
	config = databricksTestConfig(t)
	config.CAFile = "/PRIVATE_UNREADABLE_CA"
	if client, closeTransport, err := openDatabricksClient(context.Background(), config, time.Second); err == nil || client != nil || closeTransport != nil {
		t.Fatal("failed CA opened client", err)
	}
}

func TestDatabricksShortDeadlineConstructsAndCancelsActualSDKRead(t *testing.T) {
	config := databricksTestConfig(t)
	t.Setenv(config.TokenEnv, "owned-short-timeout")
	options, err := databricksClientOptions(context.Background(), config, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if options.PollInterval != 100*time.Millisecond {
		t.Fatal("short timeout rejected by default polling interval")
	}
	dispatch, disconnect, cleanup := make(chan struct{}), make(chan struct{}), make(chan struct{})
	transport := databricksOwnedTLS(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/2.0/sql/statements" {
			t.Error("unexpected short-deadline route")
		}
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error("short-deadline request body", err)
		}
		close(dispatch)
		select {
		case <-r.Context().Done():
			close(disconnect)
		case <-cleanup:
		}
	})
	defer close(cleanup)
	client, err := newDatabricksNativeClient(config.Endpoint, options, transport)
	if err != nil {
		t.Fatal("valid short timeout failed constructor", err)
	}
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	done := make(chan error, 1)
	go func() { _, err := client.Get(ctx, "users", "a"); done <- err }()
	select {
	case <-dispatch:
	case <-time.After(time.Second):
		t.Fatal("short timeout never dispatched")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("short deadline classification", err)
		}
	case <-time.After(time.Second):
		t.Fatal("short deadline not prompt")
	}
	drain, cancelDrain := context.WithTimeout(context.Background(), time.Second)
	defer cancelDrain()
	if err = client.Shutdown(drain); err != nil {
		t.Fatal("short deadline did not drain", err)
	}
	select {
	case <-disconnect:
	case <-time.After(time.Second):
		t.Fatal("short deadline server disconnect missing")
	}
}

func TestDatabricksProviderFailureAndRevocationEveryReadRoute(t *testing.T) {
	config := databricksTestConfig(t)
	for _, operation := range []string{"exists", "testConnection", "getMany", "query"} {
		for _, revoke := range []bool{false, true} {
			request := `{"operation":"` + operation + `","collection":"users","id":"a"}`
			if operation == "getMany" {
				request = `{"operation":"getMany","collection":"users","ids":["a"]}`
			}
			if operation == "query" {
				request = `{"operation":"query","collection":"users","limit":2}`
			}
			r := databricksTestRead(t, config, request)
			ctx, cancel := context.WithCancel(context.Background())
			client := databricksSuccessfulClient()
			failure := func() error {
				if revoke {
					cancel()
					return nil
				}
				return errors.New("PRIVATE_PROVIDER_CAUSE")
			}
			client.exists = func(context.Context, string, string) (bool, error) { return true, failure() }
			client.many = func(context.Context, string, []string) ([]*dbrsdk.Document, error) {
				return []*dbrsdk.Document{databricksTestDocument("a")}, failure()
			}
			client.query = func(context.Context, dal.Query) (*dbrsdk.Result, error) {
				d := databricksTestDocument("a")
				return &dbrsdk.Result{Rows: []dbrsdk.Row{d.Row}, Columns: d.Columns}, failure()
			}
			var drains, closes int
			client.shutdown = func(context.Context) error { drains++; return nil }
			var out bytes.Buffer
			err := deliverDatabricksRead(ctx, &out, client, func() { closes++ }, r)
			cancel()
			if err == nil || out.Len() != 0 || drains != 1 || closes != 1 || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("failed/revoked native route committed or leaked", operation, revoke, err)
			}
			if revoke && !strings.Contains(err.Error(), "cancelled") {
				t.Fatal("provider callback revocation lost", err)
			}
		}
	}
}

func TestDatabricksMalformedBatchAndQueryCellsFailWhole(t *testing.T) {
	config := databricksTestConfig(t)
	for _, mode := range []string{"batch-id", "batch-nil-id", "query-columns", "query-width", "query-missing-cell", "query-invalid-text"} {
		raw := `{"operation":"query","collection":"users","limit":2}`
		if strings.HasPrefix(mode, "batch") {
			raw = `{"operation":"getMany","collection":"users","ids":["a","b"]}`
		}
		r := databricksTestRead(t, config, raw)
		client := databricksSuccessfulClient()
		d := databricksTestDocument("a")
		switch mode {
		case "batch-id":
			d.ID = "substituted"
		case "batch-nil-id":
			d.Row["id"] = nil
		case "query-columns":
			d.Columns = nil
		case "query-width":
			delete(d.Row, "nil")
		case "query-missing-cell":
			delete(d.Row, "nil")
			d.Row["other"] = nil
		case "query-invalid-text":
			v := string([]byte{0xff})
			d.Row["value"] = &v
		}
		client.many = func(context.Context, string, []string) ([]*dbrsdk.Document, error) {
			return []*dbrsdk.Document{databricksTestDocument("a"), d}, nil
		}
		client.query = func(context.Context, dal.Query) (*dbrsdk.Result, error) {
			return &dbrsdk.Result{Rows: []dbrsdk.Row{d.Row}, Columns: d.Columns}, nil
		}
		var out bytes.Buffer
		if err := deliverDatabricksRead(context.Background(), &out, client, func() {}, r); err == nil || out.Len() != 0 {
			t.Fatal("malformed native response committed", mode, err)
		}
	}
}

func TestDatabricksThreeFieldSelectorAmbiguityIsRefused(t *testing.T) {
	config := databricksTestConfig(t)
	raw := `{"operation":"query","collection":"users","limit":1,"where":{"field":"id","documentId":true,"operator":"="}}`
	admitted, err := admitDatabricksRequest(context.Background(), config, []byte(raw))
	if !errors.Is(err, errDynamoInput) || admitted.query != nil || admitted.ids != nil {
		t.Fatal("three-field ambiguous selector admitted", err)
	}
}

type databricksCountWriter struct{ writes, bytes int }

func (w *databricksCountWriter) Write(data []byte) (int, error) {
	w.writes++
	w.bytes += len(data)
	if len(data) == 0 || data[0] != '{' || data[len(data)-1] != '\n' {
		return 0, errors.New("invalid complete output")
	}
	return len(data), nil
}

func TestDatabricksMixedNativeRowsNearEscapedWireLimit(t *testing.T) {
	config := databricksTestConfig(t)
	r := databricksTestRead(t, config, `{"operation":"getMany","collection":"users","ids":["a","b","c"]}`)
	first := strings.Repeat("<", 600000)
	second := strings.Repeat("<", (8<<20)/6-600000)
	accepted, refused := 0, 0
	for padding := 0; padding < 1536; padding++ {
		a, b, c := databricksTestDocument("a"), databricksTestDocument("b"), databricksTestDocument("c")
		tail := second[:len(second)-padding]
		a.Row["value"] = &first
		b.Row["value"] = &tail
		client := databricksSuccessfulClient()
		client.many = func(context.Context, string, []string) ([]*dbrsdk.Document, error) {
			return []*dbrsdk.Document{a, b, c}, nil
		}
		var drains, closes int
		client.shutdown = func(context.Context) error { drains++; return nil }
		writer := &databricksCountWriter{}
		err := deliverDatabricksRead(context.Background(), writer, client, func() { closes++ }, r)
		if drains != 1 || closes != 1 || first[0] != '<' || tail[0] != '<' {
			t.Fatal("budget refusal skipped cleanup or changed source")
		}
		if err != nil {
			refused++
			if writer.writes != 0 || strings.Contains(err.Error(), "<") {
				t.Fatal("budget failure committed prefix or data")
			}
		} else {
			accepted++
			if writer.writes != 1 || writer.bytes > 8<<20 {
				t.Fatal("successful bounded output contract")
			}
		}
	}
	if accepted == 0 || refused == 0 {
		t.Fatal("real boundary fixture must straddle acceptance and whole refusal", accepted, refused)
	}
}

func TestDatabricksMetadataAndNativeValueAdmissionFailures(t *testing.T) {
	config := databricksTestConfig(t)
	for _, mode := range []string{"catalog", "schema", "alias", "table", "id-column", "warehouse"} {
		c := config
		c.Tables = map[string]dbrsdk.Table{"users": {Name: "t", IDColumn: "id"}}
		invalid := string([]byte{0xff})
		switch mode {
		case "catalog":
			c.Catalog = invalid
		case "schema":
			c.Schema = invalid
		case "alias":
			c.Tables = map[string]dbrsdk.Table{strings.Repeat("x", 4097): {Name: "t", IDColumn: "id"}}
		case "table":
			c.Tables["users"] = dbrsdk.Table{Name: invalid, IDColumn: "id"}
		case "id-column":
			c.Tables["users"] = dbrsdk.Table{Name: "t", IDColumn: invalid}
		case "warehouse":
			c.WarehouseID = invalid
		}
		var out, diagnostic bytes.Buffer
		if err := deliverDatabricksMetadata(context.Background(), &out, &diagnostic, c); err == nil || out.Len() != 0 || diagnostic.Len() != 0 {
			t.Fatal("unsafe metadata committed", mode, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if value, err := databricksWireResult(ctx, map[string]any{"native": "staged"}); !errors.Is(err, context.Canceled) || value != nil {
		t.Fatal("final conversion ignored real cancellation", err)
	}
	if _, err := databricksScalar(json.RawMessage(`{`)); err == nil {
		t.Fatal("malformed numeric/native parameter admitted")
	}
	for _, raw := range []string{
		`{"operation":"query","collection":"users","limit":1,"order":[{"field":true,"direction":"asc"}]}`,
		`{"operation":"query","collection":"users","limit":1,"where":{"field":true,"operator":"=","value":"a"}}`,
		`{"operation":"query","collection":"users","limit":1,"where":{"field":"n","operator":true,"value":"a"}}`,
	} {
		if _, err := admitDatabricksRequest(context.Background(), config, []byte(raw)); err == nil {
			t.Fatal("wrong native control type admitted")
		}
	}
	bad := strings.Replace(databricksTestConfigJSON, `"catalog":"main"`, `"catalog":true`, 1)
	var c databricksNativeConfig
	if err := decodeDatabricksConfig([]byte(bad), &c); err == nil {
		t.Fatal("wrong native config type admitted")
	}
}

func TestDatabricksMissingRowsAndShortColumnPagesRetainWholeLimits(t *testing.T) {
	budget := databricksWireBudget{}
	for i := 0; i < 1000; i++ {
		value, err := budget.document(context.Background(), nil, "id", "missing")
		if err != nil || value != nil {
			t.Fatal("valid missing row refused", i, err)
		}
	}
	if _, err := budget.document(context.Background(), nil, "id", "missing"); !errors.Is(err, errDynamoResponse) {
		t.Fatal("missing rows bypassed row cap", err)
	}
	native := databricksTestDocument("a")
	if err := budget.admitRow(context.Background(), native.Row, native.Columns); !errors.Is(err, errDynamoResponse) {
		t.Fatal("mixed native row bypassed existing missing-row quota", err)
	}
	config := databricksTestConfig(t)
	r := databricksTestRead(t, config, `{"operation":"query","collection":"users","limit":1000}`)
	columns := make([]dbrsdk.Column, 128)
	row := make(dbrsdk.Row, 128)
	for i := range columns {
		name := string(rune(0x100 + i))
		columns[i] = dbrsdk.Column{Name: name, Type: "STRING"}
		row[name] = nil
	}
	rows := make([]dbrsdk.Row, 1000)
	for i := range rows {
		rows[i] = row
	}
	client := databricksSuccessfulClient()
	client.query = func(context.Context, dal.Query) (*dbrsdk.Result, error) {
		return &dbrsdk.Result{Rows: rows, Columns: columns}, nil
	}
	var out bytes.Buffer
	if err := deliverDatabricksRead(context.Background(), &out, client, func() {}, r); err == nil || out.Len() != 0 {
		t.Fatal("whole page bypassed node budget", err)
	}
}
