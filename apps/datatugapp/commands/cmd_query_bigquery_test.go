package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bigquery "github.com/dal-go/dalgo2bigquery"
	"github.com/datatug/datatug-cli/pkg/bigqueryread"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
)

type cliBQTransport func(*http.Request) (*http.Response, error)

func (f cliBQTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type cliBQProvider struct {
	principal bigquery.Principal
	calls     int
	clock     *cliBQClock
}

func (p *cliBQProvider) Authorize(_ context.Context, t http.RoundTripper) (bigquery.Identity, http.RoundTripper, error) {
	p.calls++
	return bigquery.Identity{Principal: p.principal, ExpiresAt: p.clock.Now().Add(time.Hour), Read: true, Cancel: true}, t, nil
}

type cliBQClock struct{ now time.Time }

func (c *cliBQClock) Now() time.Time { return c.now }
func (c *cliBQClock) Sleep(ctx context.Context, d time.Duration) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	c.now = c.now.Add(d)
	return nil
}
func cliBQResponse(s string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(s))}
}

type cliBQHarness struct {
	t                                 *testing.T
	dir, file, policy                 string
	clock                             *cliBQClock
	provider                          *cliBQProvider
	paid, requests, pageGets, cancels int
	failure                           string
	lastSQL                           string
}

func newCliBQHarness(t *testing.T) *cliBQHarness {
	t.Helper()
	h := &cliBQHarness{t: t, dir: t.TempDir(), clock: &cliBQClock{time.Now()}}
	h.provider = &cliBQProvider{principal: bigquery.Principal{Kind: "google-user", Subject: "verified-google-sub", Generation: "verified-grant-1"}, clock: h.clock}
	input := bigqueryread.Input{Profile: bigquery.SourceProfile{Version: 1, SourceID: "technical-fixture", DescriptorDigest: "reviewed-fixture", LogicalCollection: "sample", SourceProject: "source-project", DatasetID: "ds", TableID: "tbl", Location: "EU", Schema: []bigquery.Field{{Name: "n", Type: "INTEGER", Mode: "NULLABLE"}, {Name: "ownerID", Type: "STRING", Mode: "NULLABLE"}}, PublisherReviewRef: "fixture-review", RightsReviewRef: "connection-test-only", Use: "connection-test"}, Query: bigqueryread.QueryShape{From: bigqueryread.From{Name: "sample"}, Columns: []bigqueryread.Column{{Field: "n"}}, Limit: 3, Where: &bigqueryread.Condition{Op: ">=", Left: &bigqueryread.Expression{Field: "n"}, Right: &bigqueryread.Expression{Param: "min"}}}}
	h.file = h.save("input.json", input)
	if e := os.Mkdir(filepath.Join(h.dir, "policies"), 0700); e != nil {
		t.Fatal(e)
	}
	h.policy = filepath.Join(h.dir, "policy.yaml")
	h.writePolicy("allow")
	ledger := bigquery.NewMemoryLedger()
	saved := bigQueryDeps
	t.Cleanup(func() { bigQueryDeps = saved })
	bigQueryDeps = bigQueryDependencies{ledger: func(string) (bigquery.Ledger, error) { return ledger, nil }, provider: func(string, bool, string) (bigquery.Provider, error) { return h.provider, nil }, transport: cliBQTransport(h.transport), clock: h.clock}
	return h
}
func (h *cliBQHarness) save(name string, value any) string {
	h.t.Helper()
	raw, e := json.Marshal(value)
	if e != nil {
		h.t.Fatal(e)
	}
	p := filepath.Join(h.dir, name)
	if e = os.WriteFile(p, raw, 0600); e != nil {
		h.t.Fatal(e)
	}
	return p
}
func (h *cliBQHarness) writePolicy(effect string) {
	h.t.Helper()
	raw := `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata: {name: fixture}
default: deny
scopes:
- path: /sample
  rules:
  - id: own
    effect: ` + effect + `
    operations: [query]
    where:
      op: "=="
      left: {field: ownerID}
      right: {param: currentUser}
    fields: [n]
`
	if e := os.WriteFile(h.policy, []byte(raw), 0600); e != nil {
		h.t.Fatal(e)
	}
}
func (h *cliBQHarness) execute(op string, args ...string) ([]byte, error) {
	h.t.Helper()
	root := &cobra.Command{Use: "datatug", SilenceErrors: true, SilenceUsage: true}
	root.AddCommand(queryCommand())
	all := []string{"query", "bigquery", op, "--file", h.file, "--ledger", h.dir, "--auth", "adc", "--policy", h.policy, "--policies-dir", h.dir + "/policies", "--as", "alice", "--var", "min=\"10\""}
	all = append(all, args...)
	root.SetArgs(all)
	var out, stderr bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&stderr)
	root.SetContext(context.Background())
	e := root.Execute()
	return out.Bytes(), e
}
func (h *cliBQHarness) transport(r *http.Request) (*http.Response, error) {
	h.requests++
	if r.URL.Host != "bigquery.googleapis.com" {
		h.t.Fatal(r.URL)
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/datasets/ds"):
		return cliBQResponse(`{"datasetReference":{"projectId":"source-project","datasetId":"ds"},"location":"EU"}`), nil
	case strings.HasSuffix(r.URL.Path, "/tables/tbl"):
		return cliBQResponse(`{"tableReference":{"projectId":"source-project","datasetId":"ds","tableId":"tbl"},"type":"TABLE","schema":{"fields":[{"name":"n","type":"INTEGER","mode":"NULLABLE"},{"name":"ownerID","type":"STRING","mode":"NULLABLE"}]}}`), nil
	case strings.HasSuffix(r.URL.Path, "/cancel"):
		h.cancels++
		if h.failure == "cancel-malformed" {
			return cliBQResponse(`{"job":{}}`), nil
		}
		return cliBQResponse(`{"job":{"jobReference":{"projectId":"job-project","jobId":"j","location":"EU"},"status":{"state":"DONE"}}}`), nil
	case r.Method == "POST":
		var m map[string]any
		if e := json.NewDecoder(r.Body).Decode(&m); e != nil {
			h.t.Fatal(e)
		}
		h.lastSQL = m["query"].(string)
		if !strings.Contains(h.lastSQL, "`ownerID`") || !strings.Contains(h.lastSQL, "@p1") {
			h.t.Fatal("lost protected policy residual", m)
		}
		if m["dryRun"] == true {
			return cliBQResponse(`{"totalBytesProcessed":"100"}`), nil
		}
		h.paid++
		if m["maximumBytesBilled"] != "1000" || !strings.Contains(r.URL.Path, "/projects/job-project/") {
			h.t.Fatal(m, r.URL)
		}
		if h.failure == "ambiguous" {
			return nil, io.ErrUnexpectedEOF
		}
		if h.failure == "malformed" {
			return cliBQResponse(`{"jobReference":{"projectId":"job-project","jobId":"j","location":"EU"},"jobComplete":true,"schema":{"fields":[{"name":"n","type":"INTEGER","mode":"NULLABLE"}]},"rows":[{"f":[{}]}]}`), nil
		}
		return cliBQResponse(`{"jobReference":{"projectId":"job-project","jobId":"j","location":"EU"},"jobComplete":true,"schema":{"fields":[{"name":"n","type":"INTEGER","mode":"NULLABLE"}]},"rows":[{"f":[{"v":"9223372036854775807"}]}],"pageToken":"next"}`), nil
	case strings.Contains(r.URL.Path, "/queries/j"):
		h.pageGets++
		return cliBQResponse(`{"jobReference":{"projectId":"job-project","jobId":"j","location":"EU"},"jobComplete":true,"rows":[{"f":[{"v":"9223372036854775807"}]}]}`), nil
	case strings.Contains(r.URL.Path, "/jobs/j"):
		return cliBQResponse(`{"jobReference":{"projectId":"job-project","jobId":"j","location":"EU"},"status":{"state":"DONE"},"statistics":{"query":{"totalBytesProcessed":"100","totalBytesBilled":"100","cacheHit":false}}}`), nil
	default:
		h.t.Fatal("unexpected HTTP", r.Method, r.URL)
		return nil, errors.New("unexpected")
	}
}
func (h *cliBQHarness) preview() (bigquery.Preview, string) {
	h.t.Helper()
	raw, e := h.execute("preview", "--execution-project", "job-project", "--maximum-bytes-billed", "1000", "--page-size", "1")
	if e != nil {
		h.t.Fatal(e, string(raw))
	}
	var p bigquery.Preview
	if e = json.Unmarshal(raw, &p); e != nil {
		h.t.Fatal(e, string(raw))
	}
	return p, h.save("preview.json", p)
}
func (h *cliBQHarness) run(p bigquery.Preview, path string) (bigquery.Page, error) {
	h.t.Helper()
	raw, e := h.execute("run", "--preview", path, "--approve-digest", p.ApprovalDigest)
	var page bigquery.Page
	if len(raw) > 0 && json.Unmarshal(raw, &page) != nil {
		h.t.Fatal(string(raw))
	}
	return page, e
}
func TestBigQueryCobraProtectedJourney(t *testing.T) {
	h := newCliBQHarness(t)
	preview, path := h.preview()
	if h.paid != 0 || preview.Execution.Principal.Subject == "alice" || preview.Plan.Parameters[1].Value != "alice" {
		t.Fatal(preview, h.paid)
	}
	page, e := h.run(preview, path)
	if e != nil {
		t.Fatal(e)
	}
	if len(page.Rows) != 1 || page.Rows[0][0].Value != "9223372036854775807" || page.Receipt.Job.ProjectID != "job-project" || page.Cursor == "" {
		t.Fatal(page)
	}
	first := page.Receipt
	raw, e := h.execute("page", "--receipt", h.save("first.json", page))
	if e != nil {
		t.Fatal(e, string(raw))
	}
	if e = json.Unmarshal(raw, &page); e != nil {
		t.Fatal(e)
	}
	if h.paid != 1 || h.pageGets != 1 || page.Receipt.Job.JobID != first.Job.JobID || !page.Receipt.ExecutionDeadline.Equal(first.ExecutionDeadline) || page.Receipt.Counters.Rows != 2 {
		t.Fatal(page, h.paid, h.pageGets)
	}
	resultFile := h.save("second.json", page)
	raw, e = h.execute("status", "--receipt", resultFile)
	if e != nil {
		t.Fatal(e, string(raw))
	}
	var control bigQueryControl
	if json.Unmarshal(raw, &control) != nil || control.Status == nil || control.Status.State != "completed" {
		t.Fatal(string(raw))
	}
	_, e = h.execute("cancel", "--receipt", resultFile)
	if e == nil || h.cancels != 0 {
		t.Fatal(e, h.cancels)
	}
	_, e = h.execute("cancel", "--receipt", resultFile, "--enable-cancellation")
	if e != nil || h.cancels != 1 {
		t.Fatal(e, h.cancels)
	}
	_, e = h.run(preview, path)
	if e == nil || h.paid != 1 {
		t.Fatal("approval replay", e, h.paid)
	}
}
func TestBigQueryCobraPolicyChangeAndDeadlineNoDispatch(t *testing.T) {
	h := newCliBQHarness(t)
	preview, path := h.preview()
	h.writePolicy("deny")
	before := h.requests
	_, e := h.run(preview, path)
	if e == nil || h.requests != before || h.paid != 0 {
		t.Fatal(e, h.requests, before)
	}
	h.writePolicy("allow")
	page, e := h.run(preview, path)
	if e != nil {
		t.Fatal(e)
	}
	before = h.requests
	h.clock.now = h.clock.now.Add(121 * time.Second)
	raw, e := h.execute("page", "--receipt", h.save("deadline.json", page))
	if e == nil || h.requests != before {
		t.Fatal("expired resumed dispatch", e, h.requests, before)
	}
	var stopped bigquery.Page
	if json.Unmarshal(raw, &stopped) != nil || stopped.Receipt.RunID != page.Receipt.RunID || !stopped.Receipt.ExecutionDeadline.Equal(page.Receipt.ExecutionDeadline) {
		t.Fatal(string(raw))
	}
}
func TestBigQueryCobraUnknownAndMalformedSubmitReceipts(t *testing.T) {
	for _, failure := range []string{"ambiguous", "malformed"} {
		t.Run(failure, func(t *testing.T) {
			h := newCliBQHarness(t)
			p, path := h.preview()
			h.failure = failure
			page, e := h.run(p, path)
			if e == nil || h.paid != 1 || page.Receipt.RunID == "" {
				t.Fatal(e, h.paid, page)
			}
			if failure == "ambiguous" && page.Receipt.State != "submission_unknown" {
				t.Fatal(page)
			}
			if failure == "malformed" && page.Receipt.Job == nil {
				t.Fatal(page)
			}
			_, e = h.run(p, path)
			if e == nil || h.paid != 1 {
				t.Fatal("replayed uncertain submit")
			}
		})
	}
}
func TestBigQueryCobraRejectsUnsupportedBeforeAuth(t *testing.T) {
	h := newCliBQHarness(t)
	for _, raw := range []string{`{"profile":{},"query":{"from":{"name":"x","joins":[]},"columns":[{"field":"n"}],"limit":2}}`, `{"profile":{},"query":{"from":{"name":"x"},"columns":[{"field":"n"}],"limit":2,"offset":1}}`, strings.Repeat(" ", bigqueryread.MaxInputBytes+1)} {
		if e := os.WriteFile(h.file, []byte(raw), 0600); e != nil {
			t.Fatal(e)
		}
		_, e := h.execute("preview", "--execution-project", "job-project", "--maximum-bytes-billed", "1000")
		if e == nil || h.requests != 0 || h.provider.calls != 0 {
			t.Fatal(e, h.requests, h.provider.calls)
		}
	}
}

func TestBigQueryCobraConnectExplicitAndGrantChoice(t *testing.T) {
	h := newCliBQHarness(t)
	calls := 0
	bigQueryDeps.consentIdentity = func(_ context.Context, _ string, _ bool, token *oauth2.Token) (bigquery.Identity, error) {
		if token.AccessToken != "fixture-never-output" {
			t.Fatal("wrong consent token")
		}
		return bigquery.Identity{Principal: h.provider.principal, ExpiresAt: h.clock.Now().Add(time.Hour), Read: true, Cancel: true}, nil
	}
	var scopeList []string
	bigQueryDeps.connect = func(ctx context.Context, scopes []string) (*oauth2.Token, error) {
		calls++
		scopeList = scopes
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded connect")
		}
		return &oauth2.Token{AccessToken: "fixture-never-output"}, nil
	}
	run := func(args ...string) ([]byte, error) {
		root := &cobra.Command{Use: "datatug", SilenceErrors: true, SilenceUsage: true}
		root.AddCommand(queryCommand())
		all := []string{"query", "bigquery", "connect", "--ledger", h.dir}
		all = append(all, args...)
		root.SetArgs(all)
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(io.Discard)
		return out.Bytes(), root.Execute()
	}
	_, e := run("--auth", "adc")
	if e == nil || calls != 0 {
		t.Fatal("ADC opened consent", e, calls)
	}
	// Keep stdout after execution, since an evaluation-order snapshot is empty.
	root := &cobra.Command{Use: "datatug", SilenceErrors: true, SilenceUsage: true}
	root.AddCommand(queryCommand())
	root.SetArgs([]string{"query", "bigquery", "connect", "--ledger", h.dir, "--auth", "google"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(io.Discard)
	if e = root.Execute(); e != nil || calls != 1 || strings.Join(scopeList, " ") != "https://www.googleapis.com/auth/bigquery.readonly openid" || strings.Contains(out.String(), "fixture-never-output") || !strings.Contains(out.String(), "verified-google-sub") {
		t.Fatal(e, calls, out.String(), scopeList)
	}
	_, e = run("--auth", "google", "--enable-cancellation")
	if e != nil || calls != 2 || strings.Join(scopeList, " ") != "https://www.googleapis.com/auth/bigquery openid" {
		t.Fatal(e, calls, scopeList)
	}
	bigQueryDeps.consentIdentity = func(context.Context, string, bool, *oauth2.Token) (bigquery.Identity, error) {
		return bigquery.Identity{Principal: bigquery.Principal{Kind: "google-user", Subject: "different-consent-account", Generation: "different-grants"}, Read: true, Cancel: true}, nil
	}
	if _, e = run("--auth", "google"); e == nil {
		t.Fatal("reported stale stored account as newly connected")
	}
	if h.requests != 0 || h.paid != 0 {
		t.Fatal("connect queried warehouse", h.requests, h.paid)
	}
}
func TestBigQueryCobraPrivateExportsAndPermissionDenials(t *testing.T) {
	h := newCliBQHarness(t)
	previewFile := filepath.Join(h.dir, "preview-fixture.json")
	raw, e := h.execute("preview", "--execution-project", "job-project", "--maximum-bytes-billed", "1000", "--preview-out", previewFile)
	if e != nil {
		t.Fatal(e)
	}
	var preview bigquery.Preview
	if json.Unmarshal(raw, &preview) != nil {
		t.Fatal(string(raw))
	}
	receiptFile := filepath.Join(h.dir, "receipt-fixture.json")
	_, e = h.execute("run", "--preview", previewFile, "--approve-digest", preview.ApprovalDigest, "--receipt-out", receiptFile)
	if e != nil {
		t.Fatal(e)
	}
	exported, e := os.ReadFile(receiptFile)
	if e != nil {
		t.Fatal(e)
	}
	var continuation bigquery.Page
	if json.Unmarshal(exported, &continuation) != nil || len(continuation.Rows) != 0 || continuation.Cursor == "" || continuation.Receipt.Job == nil {
		t.Fatal(string(exported))
	}
	info, e := os.Stat(receiptFile)
	if e != nil || info.Mode().Perm()&0077 != 0 {
		t.Fatal(info, e)
	}
	_, e = h.execute("page", "--receipt", receiptFile)
	if e != nil {
		t.Fatal(e)
	}
	h.writePolicy("deny")
	before := h.requests
	raw, e = h.execute("page", "--receipt", receiptFile)
	if e == nil || h.requests != before || !bytes.Contains(raw, []byte(continuation.Receipt.RunID)) {
		t.Fatal("denial discarded receipt", e, h.requests, before, string(raw))
	}
}
func TestBigQueryCobraHiddenReferencesDeniedBeforeAuth(t *testing.T) {
	for _, shape := range []string{"where", "order", "alias"} {
		t.Run(shape, func(t *testing.T) {
			h := newCliBQHarness(t)
			raw, e := os.ReadFile(h.file)
			if e != nil {
				t.Fatal(e)
			}
			var doc map[string]any
			if json.Unmarshal(raw, &doc) != nil {
				t.Fatal("input")
			}
			q := doc["query"].(map[string]any)
			switch shape {
			case "where":
				q["where"] = map[string]any{"op": "==", "left": map[string]any{"field": "ownerID"}, "right": map[string]any{"value": "someone"}}
			case "order":
				q["orderBy"] = []any{map[string]any{"field": "ownerID"}}
			case "alias":
				q["columns"] = []any{map[string]any{"field": "ownerID", "as": "n"}}
			}
			h.file = h.save("hidden.json", doc)
			_, e = h.execute("preview", "--execution-project", "job-project", "--maximum-bytes-billed", "1000")
			if e == nil || h.requests != 0 || h.provider.calls != 0 {
				t.Fatal(e, h.requests, h.provider.calls)
			}
		})
	}
}

func TestBigQueryCobraReconnectionPreservesOriginalJob(t *testing.T) {
	h := newCliBQHarness(t)
	p, path := h.preview()
	page, e := h.run(p, path)
	if e != nil {
		t.Fatal(e)
	}
	original := page.Receipt
	receiptFile := h.save("reconnect.json", page)
	h.provider.principal.Generation = "verified-grant-2"
	before := h.requests
	_, e = h.execute("page", "--receipt", receiptFile)
	if e == nil || h.requests != before {
		t.Fatal("silent reconnect", e, h.requests, before)
	}
	raw, e := h.execute("page", "--receipt", receiptFile, "--reconnect")
	if e != nil {
		t.Fatal(e, string(raw))
	}
	if json.Unmarshal(raw, &page) != nil || page.Receipt.Principal != original.Principal || page.Receipt.Job.JobID != original.Job.JobID || !page.Receipt.ExecutionDeadline.Equal(original.ExecutionDeadline) || h.paid != 1 || h.pageGets != 1 {
		t.Fatal(string(raw), h.paid, h.pageGets)
	}
	receiptFile = h.save("rebound.json", page)
	h.provider.principal.Subject = "different-google-sub"
	before = h.requests
	raw, e = h.execute("status", "--receipt", receiptFile, "--reconnect")
	if e == nil || h.requests != before || !bytes.Contains(raw, []byte(original.RunID)) {
		t.Fatal("different-subject recovery", e, h.requests, before, string(raw))
	}
}
func TestBigQueryCobraPolicyBindingChangeRefusesSubmit(t *testing.T) {
	h := newCliBQHarness(t)
	p, path := h.preview()
	raw, e := os.ReadFile(h.policy)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(h.policy, bytes.Replace(raw, []byte("name: fixture"), []byte("name: revised"), 1), 0600); e != nil {
		t.Fatal(e)
	}
	before := h.requests
	_, e = h.run(p, path)
	if e == nil || h.paid != 0 || h.requests != before {
		t.Fatal("policy fingerprint not rebound", e, h.paid, h.requests, before)
	}
}

func TestBigQueryCobraBoundedINParameterBeforeAndAfterPolicy(t *testing.T) {
	h := newCliBQHarness(t)
	var input bigqueryread.Input
	raw, err := os.ReadFile(h.file)
	if err != nil || json.Unmarshal(raw, &input) != nil {
		t.Fatal(err)
	}
	input.Query.Where.Op = "In"
	h.file = h.save("in-input.json", input)
	for _, n := range []int{0, 1, 1000} {
		values := ""
		if n > 0 {
			values = strings.Repeat(`"10",`, n-1) + `"20"`
		}
		raw, err = h.execute("preview", "--execution-project", "job-project", "--maximum-bytes-billed", "1000", "--var", "min=["+values+"]")
		if err != nil || h.paid != 0 {
			t.Fatal(n, err, string(raw))
		}
		var preview bigquery.Preview
		if json.Unmarshal(raw, &preview) != nil || preview.Plan.Parameters[0].Type != "ARRAY<INT64>" || len(preview.Plan.Parameters[0].Value.([]any)) != n {
			t.Fatal(n, string(raw))
		}
		if n > 0 && !strings.Contains(h.lastSQL, "IN UNNEST(@p0)") {
			t.Fatal(h.lastSQL)
		}
	}
	before, authorizations := h.requests, h.provider.calls
	invalid := []string{"[" + strings.Repeat(`"10",`, 1000) + `"20"]`, `["10",true]`, `["10",null]`, `[["10"]]`}
	for _, values := range invalid {
		_, err = h.execute("preview", "--execution-project", "job-project", "--maximum-bytes-billed", "1000", "--var", "min="+values)
		if err == nil || h.requests != before || h.provider.calls != authorizations {
			t.Fatal("unsafe substituted IN reached authentication or HTTP", err, h.requests, before)
		}
	}
}

func TestBigQueryCobraInvalidSourceRetainsKnownReceipt(t *testing.T) {
	h := newCliBQHarness(t)
	preview, path := h.preview()
	page, err := h.run(preview, path)
	if err != nil {
		t.Fatal(err)
	}
	receiptPath := h.save("known-page.json", page)
	if err = os.WriteFile(h.file, []byte(`{"unknown":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	before := h.requests
	raw, err := h.execute("page", "--receipt", receiptPath)
	var recovered bigquery.Page
	if err == nil || json.Unmarshal(raw, &recovered) != nil || recovered.Receipt.Job == nil || recovered.Receipt.Job.JobID != page.Receipt.Job.JobID || recovered.Receipt.RunID != page.Receipt.RunID || h.requests != before {
		t.Fatal(err, string(raw), h.requests, before)
	}
}

func TestBigQueryCobraDiagnosticsRedactPolicyVariablesAndLiterals(t *testing.T) {
	h := newCliBQHarness(t)
	raw, err := os.ReadFile(h.policy)
	if err != nil {
		t.Fatal(err)
	}
	original := `where:
      op: "=="
      left: {field: ownerID}
      right: {param: currentUser}`
	replacement := `where:
      and:
      - {op: ==, left: {field: ownerID}, right: {param: currentUser}}
      - {op: ==, left: {field: ownerID}, right: {param: policyvar}}
      - {op: ==, left: {field: ownerID}, right: {value: private-policy-literal-fixture}}`
	if !strings.Contains(string(raw), original) {
		t.Fatal("policy fixture changed")
	}
	if err = os.WriteFile(h.policy, []byte(strings.Replace(string(raw), original, replacement, 1)), 0600); err != nil {
		t.Fatal(err)
	}
	root := &cobra.Command{Use: "datatug", SilenceErrors: true, SilenceUsage: true}
	root.AddCommand(queryCommand())
	root.SetArgs([]string{"query", "bigquery", "preview", "--file", h.file, "--ledger", h.dir, "--auth", "adc", "--policy", h.policy, "--policies-dir", h.dir + "/policies", "--as", "private-policy-binding-fixture", "--var", `min="10"`, "--var", `policyvar="private-policy-variable-fixture"`, "--execution-project", "job-project", "--maximum-bytes-billed", "1000"})
	var out, diag bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&diag)
	if err = root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"private-policy-binding-fixture", "private-policy-variable-fixture", "private-policy-literal-fixture"} {
		if strings.Contains(diag.String(), marker) || !strings.Contains(out.String(), marker) {
			t.Fatal("diagnostic leaked or preview omitted deliberate parameter", marker, diag.String())
		}
	}
	if !strings.Contains(diag.String(), `policy "fixture" rule "own" allows`) || !strings.Contains(diag.String(), "fields") {
		t.Fatal("useful policy metadata lost", diag.String())
	}
}

type bigQueryBrokenOutput struct{}

func (bigQueryBrokenOutput) Write([]byte) (int, error) { return 0, os.ErrClosed }
func TestBigQueryCobraRecoveryBeforeBrokenOutput(t *testing.T) {
	for _, failure := range []string{"", "ambiguous", "malformed"} {
		t.Run(failure, func(t *testing.T) {
			h := newCliBQHarness(t)
			preview, previewPath := h.preview()
			h.failure = failure
			receiptPath := filepath.Join(h.dir, "receipt-recovery.json")
			root := &cobra.Command{Use: "datatug", SilenceErrors: true, SilenceUsage: true}
			root.AddCommand(queryCommand())
			root.SetArgs([]string{"query", "bigquery", "run", "--file", h.file, "--ledger", h.dir, "--auth", "adc", "--policy", h.policy, "--policies-dir", h.dir + "/policies", "--as", "alice", "--var", `min="10"`, "--preview", previewPath, "--approve-digest", preview.ApprovalDigest, "--receipt-out", receiptPath})
			root.SetOut(bigQueryBrokenOutput{})
			root.SetErr(io.Discard)
			err := root.Execute()
			if !errors.Is(err, os.ErrClosed) || !strings.Contains(err.Error(), "result output failed") || h.paid != 1 {
				t.Fatal(err, h.paid)
			}
			raw, readError := os.ReadFile(receiptPath)
			var result bigquery.Page
			if readError != nil || json.Unmarshal(raw, &result) != nil || result.Receipt.RunID == "" || len(result.Rows) != 0 {
				t.Fatal(readError, string(raw))
			}
			if failure == "ambiguous" && (!strings.Contains(err.Error(), "submission_unknown") || result.Receipt.State != "submission_unknown") {
				t.Fatal(err, result)
			}
			if failure == "malformed" && (!strings.Contains(err.Error(), "malformed_wire") || result.Receipt.Job == nil) {
				t.Fatal(err, result)
			}
			if failure == "" && (result.Receipt.Job == nil || result.Cursor == "") {
				t.Fatal(result)
			}
		})
	}
}
func TestBigQueryResultRetainsPersistenceAndOutputErrors(t *testing.T) {
	h := newCliBQHarness(t)
	obstructed := filepath.Join(h.dir, "receipt-blocked.json")
	if err := os.Mkdir(obstructed, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	cmd.SetOut(bigQueryBrokenOutput{})
	err := writeBigQueryResult(cmd, bigquery.Page{}, "", obstructed, h.dir)
	if !errors.Is(err, os.ErrClosed) || !errors.Is(err, bigqueryread.ErrInput) || !strings.Contains(err.Error(), "private recovery artifact failed") || !strings.Contains(err.Error(), "result output failed") {
		t.Fatal(err)
	}
}

func TestBigQueryCobraCurrentControlRecoveryAfterExpiry(t *testing.T) {
	for _, operation := range []string{"status", "cancel", "partial-cancel"} {
		t.Run(operation, func(t *testing.T) {
			h := newCliBQHarness(t)
			preview, path := h.preview()
			page, err := h.run(preview, path)
			if err != nil {
				t.Fatal(err)
			}
			h.clock.now = page.Receipt.ExecutionDeadline.Add(time.Second)
			priorFile := h.save("prior-control.json", page)
			export := filepath.Join(h.dir, "receipt-control.json")
			op := operation
			args := []string{"--receipt", priorFile, "--receipt-out", export}
			if op != "status" {
				op = "cancel"
				args = append(args, "--enable-cancellation")
			}
			if operation == "partial-cancel" {
				h.failure = "cancel-malformed"
			}
			beforeAuth, beforeRequests := h.provider.calls, h.requests
			raw, err := h.execute(op, args...)
			if (err != nil) != (operation == "partial-cancel") {
				t.Fatal(operation, err, string(raw))
			}
			var control bigQueryControl
			if json.Unmarshal(raw, &control) != nil {
				t.Fatal(string(raw))
			}
			if control.Receipt.Counters.Bytes <= page.Receipt.Counters.Bytes || control.Receipt.Counters.Rows != page.Receipt.Counters.Rows || control.Cursor != page.Cursor || !control.Receipt.ExecutionDeadline.Equal(page.Receipt.ExecutionDeadline) || control.Receipt.Job == nil || *control.Receipt.Job != *page.Receipt.Job {
				t.Fatal("stale or renewed recovery authority", control, page)
			}
			if op == "status" && (control.Status == nil || control.Status.State != "completed" || control.Receipt.BilledBytes == nil || *control.Receipt.BilledBytes != "100") {
				t.Fatal(control)
			}
			if op == "cancel" && (control.Cancel == nil || control.Cancel.State == "") {
				t.Fatal(control)
			}
			if operation == "partial-cancel" && (control.Cancel.State != "unknown" || control.Receipt.State != page.Receipt.State || !strings.Contains(err.Error(), "cancellation_unknown")) {
				t.Fatal(err, control)
			}
			exported, readErr := os.ReadFile(export)
			var recovery bigQueryControl
			if readErr != nil || json.Unmarshal(exported, &recovery) != nil || recovery.Receipt.Counters != control.Receipt.Counters || recovery.Cursor != control.Cursor || len(recovery.Rows) != 0 || len(recovery.Schema) != 0 || (recovery.Status == nil) != (control.Status == nil) || (recovery.Cancel == nil) != (control.Cancel == nil) {
				t.Fatal(readErr, string(exported), control)
			}
			if h.provider.calls != beforeAuth+1 || h.requests != beforeRequests+1 || h.pageGets != 0 || h.paid != 1 {
				t.Fatal("snapshot authorized, read rows or replayed", h.provider.calls, h.requests, h.pageGets, h.paid)
			}
		})
	}
}
func TestBigQueryCobraSnapshotFailurePreservesKnownArtifactAndControl(t *testing.T) {
	h := newCliBQHarness(t)
	preview, path := h.preview()
	page, err := h.run(preview, path)
	if err != nil {
		t.Fatal(err)
	}
	// Status may truthfully observe a known trusted job, but this edited receipt
	// must never be promoted into current ledger authority by Snapshot.
	page.Receipt.ApprovalDigest = "edited-receipt-authority"
	h.failure = "cancel-malformed"
	export := filepath.Join(h.dir, "receipt-snapshot-failure.json")
	raw, err := h.execute("cancel", "--enable-cancellation", "--receipt", h.save("edited-control.json", page), "--receipt-out", export)
	var control bigQueryControl
	if err == nil || !strings.Contains(err.Error(), "cancellation_unknown") || !strings.Contains(err.Error(), "cursor_invalid") || json.Unmarshal(raw, &control) != nil || control.Cancel == nil || control.Cancel.State != "unknown" {
		t.Fatal(err, string(raw))
	}
	if control.Receipt.ApprovalDigest != page.Receipt.ApprovalDigest || control.Receipt.Counters != page.Receipt.Counters || control.Cursor != page.Cursor || control.Receipt.Job == nil || *control.Receipt.Job != *page.Receipt.Job {
		t.Fatal("discarded or falsely refreshed prior recovery artifact", control, page)
	}
	exported, readErr := os.ReadFile(export)
	var recovery bigQueryControl
	if readErr != nil || json.Unmarshal(exported, &recovery) != nil || recovery.Cancel == nil || recovery.Cancel.State != "unknown" || recovery.Receipt.ApprovalDigest != page.Receipt.ApprovalDigest || recovery.Cursor != page.Cursor {
		t.Fatal(readErr, string(exported))
	}
	if h.paid != 1 || h.pageGets != 0 || h.cancels != 1 {
		t.Fatal("replayed recovery", h.paid, h.pageGets, h.cancels)
	}
}
