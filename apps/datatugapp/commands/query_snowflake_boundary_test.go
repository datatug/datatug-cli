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
	snowsdk "github.com/dal-go/dalgo2snowflake"
)

func TestSnowflakeCommandTokenAndCAFailuresOwnResources(t *testing.T) {
	t.Setenv("DALGO_SNOWFLAKE_READ_TEST", "")
	for _, test := range []struct {
		name, config string
		timeout      time.Duration
		want         string
	}{
		{"missing-token", snowflakeTestConfigJSON, time.Second, "read failed"},
		{"short-valid-timeout", snowflakeTestConfigJSON, 100 * time.Millisecond, "read failed"},
		{"invalid-ca", strings.TrimSuffix(snowflakeTestConfigJSON, "}") + `,"caFile":"` + snowflakeTestFile(t, "NOT A CERTIFICATE") + `"}`, time.Second, "invalid or unreadable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd := querySnowflakeCommand()
			cmd.SetContext(context.Background())
			cmd.SetIn(strings.NewReader(`{"operation":"get","collection":"users","id":"a"}`))
			var out, diagnostic bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&diagnostic)
			err := runSnowflakeCommand(cmd, snowflakeTestFile(t, test.config), "-", test.timeout)
			if err == nil || !strings.Contains(err.Error(), test.want) || out.Len() != 0 {
				t.Fatal("resource construction/token failure contract", err)
			}
		})
	}
	// Invoke RunE directly to cover a genuinely absent original Cobra context;
	// Execute normally substitutes Background before calling RunE.
	cmd := querySnowflakeCommand()
	cmd.SetIn(strings.NewReader(`{"operation":"metadata"}`))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Flags().Set("config", snowflakeTestFile(t, snowflakeTestConfigJSON)); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("request", "-"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.RunE(cmd, nil); err != nil || cmd.Context() != nil {
		t.Fatal("nil original context not restored", err)
	}
}
func TestSnowflakeOpenAdmissionUsesRealSDKValidation(t *testing.T) {
	config := snowflakeTestConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if client, closeTransport, err := openSnowflakeClient(ctx, config, time.Second); !errors.Is(err, context.Canceled) || client != nil || closeTransport != nil {
		t.Fatal("preabort opened resources", err)
	}
	// The SDK boundary must refuse invalid trusted options even when invoked
	// without the outer JSON validator. Its constructor is lazy and performs no I/O.
	config.Tables = nil
	if client, closeTransport, err := openSnowflakeClient(context.Background(), config, time.Second); err == nil || client != nil || closeTransport != nil {
		t.Fatal("invalid native mapping opened client", err)
	}
	config = snowflakeTestConfig(t)
	config.CAFile = "/PRIVATE_UNREADABLE_CA"
	if client, closeTransport, err := openSnowflakeClient(context.Background(), config, time.Second); err == nil || client != nil || closeTransport != nil {
		t.Fatal("failed CA opened client", err)
	}
}
func TestSnowflakeShortDeadlineConstructsAndCancelsActualSDKRead(t *testing.T) {
	config := snowflakeTestConfig(t)
	t.Setenv(config.TokenEnv, "owned-short-timeout")
	options, err := snowflakeClientOptions(context.Background(), config, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if options.PollInterval != 100*time.Millisecond {
		t.Fatal("short timeout rejected by default polling interval")
	}
	// Read headers/body have genuinely dispatched before the SDK deadline.
	dispatch, disconnect, cleanup := make(chan struct{}), make(chan struct{}), make(chan struct{})
	transport := snowflakeOwnedTLS(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v2/statements" {
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
	client, err := newSnowflakeNativeClient(config.Endpoint, options, transport)
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
func TestSnowflakeProviderFailureAndRevocationEveryReadRoute(t *testing.T) {
	config := snowflakeTestConfig(t)
	for _, operation := range []string{"exists", "testConnection", "getMany", "query"} {
		for _, revoke := range []bool{false, true} {
			request := `{"operation":"` + operation + `","collection":"users","id":"a"}`
			if operation == "getMany" {
				request = `{"operation":"getMany","collection":"users","ids":["a"]}`
			}
			if operation == "query" {
				request = `{"operation":"query","collection":"users","limit":2}`
			}
			r := snowflakeTestRead(t, config, request)
			ctx, cancel := context.WithCancel(context.Background())
			client := snowflakeSuccessfulClient()
			failure := func() error {
				if revoke {
					cancel()
					return nil
				}
				return errors.New("PRIVATE_PROVIDER_CAUSE")
			}
			client.exists = func(context.Context, string, string) (bool, error) { return true, failure() }
			client.many = func(context.Context, string, []string) ([]*snowsdk.Document, error) {
				return []*snowsdk.Document{snowflakeTestDocument("a")}, failure()
			}
			client.query = func(context.Context, dal.Query) (*snowsdk.Result, error) {
				d := snowflakeTestDocument("a")
				return &snowsdk.Result{Rows: []snowsdk.Row{d.Row}, Columns: d.Columns}, failure()
			}
			var drains, closes int
			client.shutdown = func(context.Context) error { drains++; return nil }
			var out bytes.Buffer
			err := deliverSnowflakeRead(ctx, &out, client, func() { closes++ }, r)
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
func TestSnowflakeMalformedBatchAndQueryCellsFailWhole(t *testing.T) {
	config := snowflakeTestConfig(t)
	for _, mode := range []string{"batch-id", "batch-nil-id", "query-columns", "query-width", "query-missing-cell", "query-invalid-text"} {
		raw := `{"operation":"query","collection":"users","limit":2}`
		if strings.HasPrefix(mode, "batch") {
			raw = `{"operation":"getMany","collection":"users","ids":["a","b"]}`
		}
		r := snowflakeTestRead(t, config, raw)
		client := snowflakeSuccessfulClient()
		d := snowflakeTestDocument("a")
		switch mode {
		case "batch-id":
			d.ID = "substituted"
		case "batch-nil-id":
			d.Row["ID"] = nil
		case "query-columns":
			d.Columns = nil
		case "query-width":
			delete(d.Row, "NIL")
		case "query-missing-cell":
			delete(d.Row, "NIL")
			d.Row["OTHER"] = nil
		case "query-invalid-text":
			v := string([]byte{0xff})
			d.Row["VALUE"] = &v
		}
		client.many = func(context.Context, string, []string) ([]*snowsdk.Document, error) {
			return []*snowsdk.Document{snowflakeTestDocument("a"), d}, nil
		}
		client.query = func(context.Context, dal.Query) (*snowsdk.Result, error) {
			return &snowsdk.Result{Rows: []snowsdk.Row{d.Row}, Columns: d.Columns}, nil
		}
		var out bytes.Buffer
		if err := deliverSnowflakeRead(context.Background(), &out, client, func() {}, r); err == nil || out.Len() != 0 {
			t.Fatal("malformed native response committed", mode, err)
		}
	}
}

func TestSnowflakeThreeFieldSelectorAmbiguityIsRefused(t *testing.T) {
	config := snowflakeTestConfig(t)
	// Exactly three closed fields reach the selector check; both selectors named
	// with a decoded operator must still be refused before any read is admitted.
	raw := `{"operation":"query","collection":"users","limit":1,"where":{"field":"ID","documentId":true,"operator":"="}}`
	admitted, err := admitSnowflakeRequest(context.Background(), config, []byte(raw))
	if !errors.Is(err, errDynamoInput) || admitted.query != nil || admitted.ids != nil {
		t.Fatal("three-field ambiguous selector admitted", err)
	}
}

// The writer records only the actual final Write contract; it neither seeds
// accounting counters nor observes implementation call counts.
type snowflakeCountWriter struct{ writes, bytes int }

func (w *snowflakeCountWriter) Write(data []byte) (int, error) {
	w.writes++
	w.bytes += len(data)
	if len(data) == 0 || data[0] != '{' || data[len(data)-1] != '\n' {
		return 0, errors.New("invalid complete output")
	}
	return len(data), nil
}
func TestSnowflakeMixedNativeRowsNearEscapedWireLimit(t *testing.T) {
	config := snowflakeTestConfig(t)
	r := snowflakeTestRead(t, config, `{"operation":"getMany","collection":"users","ids":["a","b","c"]}`)
	// Each cell is within the native per-page limit. Their shared, HTML-escaped
	// wire occurrences straddle the public8MiB envelope limit. Varying real
	// payload sizes exercises whole-document/header/descriptor/cell boundaries.
	first := strings.Repeat("<", 600000)
	second := strings.Repeat("<", (8<<20)/6-600000)
	accepted, refused := 0, 0
	for padding := 0; padding < 1536; padding++ {
		a, b, c := snowflakeTestDocument("a"), snowflakeTestDocument("b"), snowflakeTestDocument("c")
		tail := second[:len(second)-padding]
		a.Row["VALUE"] = &first
		b.Row["VALUE"] = &tail
		client := snowflakeSuccessfulClient()
		client.many = func(context.Context, string, []string) ([]*snowsdk.Document, error) {
			return []*snowsdk.Document{a, b, c}, nil
		}
		var drains, closes int
		client.shutdown = func(context.Context) error { drains++; return nil }
		writer := &snowflakeCountWriter{}
		err := deliverSnowflakeRead(context.Background(), writer, client, func() { closes++ }, r)
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
func TestSnowflakeMetadataAndNativeValueAdmissionFailures(t *testing.T) {
	config := snowflakeTestConfig(t)
	for _, mode := range []string{"database", "schema", "alias", "table", "id-column", "warehouse", "role"} {
		c := config
		c.Tables = map[string]snowsdk.Table{"users": {Name: "T", IDColumn: "ID"}}
		// Direct typed boundary tests retain valid JSON/no custom callbacks; malformed
		// native text and excessive metadata must fail even without JSON admission.
		invalid := string([]byte{0xff})
		switch mode {
		case "database":
			c.Database = invalid
		case "schema":
			c.Schema = invalid
		case "alias":
			c.Tables = map[string]snowsdk.Table{strings.Repeat("x", 4097): {Name: "T", IDColumn: "ID"}}
		case "table":
			c.Tables["users"] = snowsdk.Table{Name: invalid, IDColumn: "ID"}
		case "id-column":
			c.Tables["users"] = snowsdk.Table{Name: "T", IDColumn: invalid}
		case "warehouse":
			c.Warehouse = invalid
		case "role":
			c.Role = invalid
		}
		var out, diagnostic bytes.Buffer
		if err := deliverSnowflakeMetadata(context.Background(), &out, &diagnostic, c); err == nil || out.Len() != 0 || diagnostic.Len() != 0 {
			t.Fatal("unsafe metadata committed", mode, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if value, err := snowflakeWireResult(ctx, map[string]any{"native": "staged"}); !errors.Is(err, context.Canceled) || value != nil {
		t.Fatal("final conversion ignored real cancellation", err)
	}
	if _, err := snowflakeScalar(json.RawMessage(`{`)); err == nil {
		t.Fatal("malformed numeric/native parameter admitted")
	}
	for _, raw := range []string{
		`{"operation":"query","collection":"users","limit":1,"order":[{"field":true,"direction":"asc"}]}`,
		`{"operation":"query","collection":"users","limit":1,"where":{"field":true,"operator":"=","value":"a"}}`,
		`{"operation":"query","collection":"users","limit":1,"where":{"field":"N","operator":true,"value":"a"}}`,
	} {
		if _, err := admitSnowflakeRequest(context.Background(), config, []byte(raw)); err == nil {
			t.Fatal("wrong native control type admitted")
		}
	}
	bad := strings.Replace(snowflakeTestConfigJSON, `"database":"DB"`, `"database":true`, 1)
	var c snowflakeNativeConfig
	if err := decodeSnowflakeConfig([]byte(bad), &c); err == nil {
		t.Fatal("wrong native config type admitted")
	}
}

func TestSnowflakeMissingRowsAndShortColumnPagesRetainWholeLimits(t *testing.T) {
	// Missing rows count toward the same row budget; they cannot create an
	// unbounded logical result while consuming little actual cell storage.
	budget := snowflakeWireBudget{}
	for i := 0; i < 1000; i++ {
		value, err := budget.document(context.Background(), nil, "ID", "missing")
		if err != nil || value != nil {
			t.Fatal("valid missing row refused", i, err)
		}
	}
	if _, err := budget.document(context.Background(), nil, "ID", "missing"); !errors.Is(err, errDynamoResponse) {
		t.Fatal("missing rows bypassed row cap", err)
	}
	native := snowflakeTestDocument("a")
	if err := budget.admitRow(context.Background(), native.Row, native.Columns); !errors.Is(err, errDynamoResponse) {
		t.Fatal("mixed native row bypassed existing missing-row quota", err)
	}
	// Short distinct names keep byte size below8MiB, so this real page tests
	// aggregate node admission rather than only escaped-byte exhaustion.
	config := snowflakeTestConfig(t)
	r := snowflakeTestRead(t, config, `{"operation":"query","collection":"users","limit":1000}`)
	columns := make([]snowsdk.Column, 128)
	row := make(snowsdk.Row, 128)
	for i := range columns {
		name := string(rune(0x100 + i))
		columns[i] = snowsdk.Column{Name: name, Type: "text"}
		row[name] = nil
	}
	rows := make([]snowsdk.Row, 1000)
	for i := range rows {
		rows[i] = row
	}
	client := snowflakeSuccessfulClient()
	client.query = func(context.Context, dal.Query) (*snowsdk.Result, error) {
		return &snowsdk.Result{Rows: rows, Columns: columns}, nil
	}
	var out bytes.Buffer
	if err := deliverSnowflakeRead(context.Background(), &out, client, func() {}, r); err == nil || out.Len() != 0 {
		t.Fatal("whole page bypassed node budget", err)
	}
}
