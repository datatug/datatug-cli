package mcpserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const appsMaxResults = 10
const appsMaxRetained = 16 << 20
const appsIdleTTL = 10 * time.Minute
const appsAbsoluteTTL = 30 * time.Minute

var errAppsUnavailable = errors.New("DataTug private result unavailable; run the saved query again")

// appsPayload is private _meta.datatug only. Result retains the existing typed
// wire contract; UI parameter values never populate a model-facing envelope.
type appsPayload struct {
	Version    int               `json:"version"`
	Handle     string            `json:"handle"`
	QueryID    string            `json:"queryId"`
	Parameters []parameter       `json:"parameters"`
	Values     map[string]string `json:"values"`
	Result     json.RawMessage   `json:"result"`
}
type appsEntry struct {
	input             runInput
	fingerprint       string
	payload           appsPayload
	size              int
	created, accessed time.Time
	busy              bool
	cancel            context.CancelFunc
}
type appsRegistry struct {
	mu      sync.Mutex
	entries map[string]*appsEntry
	bytes   int
	now     func() time.Time
	ctx     context.Context
	cancel  context.CancelFunc
	closed  bool
}

func newAppsRegistry() *appsRegistry {
	ctx, cancel := context.WithCancel(context.Background())
	return &appsRegistry{entries: make(map[string]*appsEntry), now: time.Now, ctx: ctx, cancel: cancel}
}
func (r *appsRegistry) remove(handle string) {
	if e := r.entries[handle]; e != nil {
		if e.cancel != nil {
			e.cancel()
		}
		r.bytes -= e.size
		delete(r.entries, handle)
	}
}
func (r *appsRegistry) prune(now time.Time) {
	for h, e := range r.entries {
		if now.Sub(e.accessed) >= appsIdleTTL || now.Sub(e.created) >= appsAbsoluteTTL {
			r.remove(h)
		}
	}
}
func (r *appsRegistry) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	r.cancel()
	for h := range r.entries {
		r.remove(h)
	}
}
func (r *appsRegistry) operation(ctx context.Context) (context.Context, func()) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	stop := context.AfterFunc(r.ctx, cancel)
	return ctx, func() { stop(); cancel() }
}
func (r *appsRegistry) acquire(handle string, rerun bool) (*appsEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune(r.now())
	e := r.entries[handle]
	if r.closed || e == nil || (rerun && e.busy) {
		return nil, errAppsUnavailable
	}
	if rerun {
		e.busy = true
	}
	return e, nil
}
func (r *appsRegistry) release(handle string, e *appsEntry, success bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries[handle] == e {
		e.busy = false
		e.cancel = nil
		if success {
			e.accessed = r.now()
		}
	}
}
func (r *appsRegistry) revoke(handle string) { r.mu.Lock(); defer r.mu.Unlock(); r.remove(handle) }
func appsFingerprint(q *datatug.QueryDefWithFolderPath, info apicontract.AgentInfo) string {
	// Hash the actual full saved definition including SQL and all parameter
	// declarations, not the redacted publicQuery metadata projection.
	body, _ := json.Marshal(struct {
		Definition datatug.QueryDef
		Authority  apicontract.AgentInfo
	}{q.QueryDef, info})
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
func privateEnvelope(payload appsPayload) (*mcp.CallToolResult, int, error) {
	var counts struct {
		Recordset struct {
			Rows    []json.RawMessage `json:"rows"`
			Columns []json.RawMessage `json:"columns"`
		} `json:"recordset"`
		Truncated bool `json:"truncated"`
	}
	if json.Unmarshal(payload.Result, &counts) != nil {
		return nil, 0, errAppsUnavailable
	}
	summary := map[string]any{"status": "complete", "rows": len(counts.Recordset.Rows), "columns": len(counts.Recordset.Columns), "truncated": counts.Truncated}
	result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("DataTug returned %d captured rows; private results are available only to the App.", len(counts.Recordset.Rows))}}, StructuredContent: summary, Meta: mcp.Meta{"datatug": payload}}
	body, err := json.Marshal(result)
	if err != nil || len(body) > maxBody {
		return nil, 0, errAppsUnavailable
	}
	return result, len(body), nil
}
func (r *appsRegistry) retain(ctx context.Context, input runInput, fingerprint string, q *datatug.QueryDefWithFolderPath, out any, parentHandle string, parent *appsEntry) (*mcp.CallToolResult, error) {
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, errAppsUnavailable
	}
	random := make([]byte, 32)
	// crypto/rand.Read fills the slice and never returns an error; an entropy
	// failure terminates the process (Go 1.27 API contract).
	_, _ = rand.Read(random)
	handle := hex.EncodeToString(random)
	public, err := publicQuery(&q.QueryDef, input.QueryID, false)
	if err != nil {
		return nil, errAppsUnavailable
	}
	// Copy caller maps: registry entries are immutable snapshots, never SDK-owned.
	values := make(map[string]string, len(input.Parameters))
	for k, v := range input.Parameters {
		values[k] = v
	}
	// The current editor values include authoritative applied defaults as well
	// as supplied overrides; numeric wire strings remain exact.
	var applied struct {
		Bindings []struct {
			ID    string                      `json:"parameterId"`
			Value apicontract.TypedValueOrSet `json:"value"`
		} `json:"bindingsApplied"`
	}
	if json.Unmarshal(raw, &applied) != nil {
		return nil, errAppsUnavailable
	}
	declared := make(map[string]bool)
	for _, p := range public.Parameters {
		declared[p.ID] = true
	}
	for _, b := range applied.Bindings {
		if !declared[b.ID] || b.Value.Scalar == nil {
			continue
		}
		v := b.Value.Scalar
		switch v.Type {
		case apicontract.ValueTypeString, apicontract.ValueTypeInteger, apicontract.ValueTypeDecimal, apicontract.ValueTypeDate, apicontract.ValueTypeDatetime:
			values[b.ID] = v.Str
		case apicontract.ValueTypeBoolean:
			values[b.ID] = strconv.FormatBool(v.Bool)
		case apicontract.ValueTypeNumber:
			values[b.ID] = strconv.FormatFloat(v.Num, 'g', -1, 64)
		}
	}
	input.Parameters = values
	payload := appsPayload{Version: 1, Handle: handle, QueryID: input.QueryID, Parameters: public.Parameters, Values: values, Result: raw}
	envelope, size, err := privateEnvelope(payload)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	r.prune(now)
	if ctx.Err() != nil || r.closed || (parent != nil && r.entries[parentHandle] != parent) {
		return nil, errAppsUnavailable
	}
	for len(r.entries) >= appsMaxResults || r.bytes+size > appsMaxRetained {
		oldest := ""
		var at time.Time
		for h, e := range r.entries {
			if !e.busy && (oldest == "" || e.accessed.Before(at)) {
				oldest = h
				at = e.accessed
			}
		}
		if oldest == "" {
			return nil, errAppsUnavailable
		}
		r.remove(oldest)
	}
	if ctx.Err() != nil {
		return nil, errAppsUnavailable
	}
	r.entries[handle] = &appsEntry{input: input, fingerprint: fingerprint, payload: payload, size: size, created: now, accessed: now}
	r.bytes += size
	return envelope, nil
}
func (c *Client) appsInitial(ctx context.Context, r *appsRegistry, input runInput) (*mcp.CallToolResult, error) {
	ctx, done := r.operation(ctx)
	defer done()
	q, info, err := c.prepareQuery(ctx, input)
	if err != nil {
		return nil, errAppsUnavailable
	}
	fingerprint := appsFingerprint(q, info)
	out, err := c.executeQuery(ctx, input, q, info)
	if err != nil {
		return nil, errAppsUnavailable
	}
	current, authority, err := c.prepareQuery(ctx, input)
	if err != nil || ctx.Err() != nil || appsFingerprint(current, authority) != fingerprint {
		return nil, errAppsUnavailable
	}
	return r.retain(ctx, input, fingerprint, q, out, "", nil)
}

type appsResultInput struct {
	Handle string `json:"handle"`
}
type appsRerunInput struct {
	Handle     string            `json:"handle"`
	Parameters map[string]string `json:"parameters"`
}

func (c *Client) appsContinue(ctx context.Context, r *appsRegistry, handle string, parameters map[string]string, rerun bool) (*mcp.CallToolResult, error) {
	e, err := r.acquire(handle, rerun)
	if err != nil {
		return nil, err
	}
	success := false
	if rerun {
		defer func() { r.release(handle, e, success) }()
	}
	ctx, done := r.operation(ctx)
	defer done()
	q, info, err := c.prepareQuery(ctx, e.input)
	if err != nil || appsFingerprint(q, info) != e.fingerprint {
		r.revoke(handle)
		return nil, errAppsUnavailable
	}
	if !rerun {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.prune(r.now())
		if ctx.Err() != nil || r.closed || r.entries[handle] != e {
			return nil, errAppsUnavailable
		}
		e.accessed = r.now()
		result, _, err := privateEnvelope(e.payload)
		return result, err
	}
	input := e.input
	input.Parameters = parameters
	// Cancel in-flight work when the source handle is revoked/expired/evicted.
	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	r.mu.Lock()
	if r.closed || r.entries[handle] != e {
		r.mu.Unlock()
		return nil, errAppsUnavailable
	}
	e.cancel = cancel
	r.mu.Unlock()
	out, err := c.executeQuery(cancelCtx, input, q, info)
	if err != nil {
		return nil, errAppsUnavailable
	}
	current, authority, err := c.prepareQuery(cancelCtx, input)
	if err != nil || cancelCtx.Err() != nil || appsFingerprint(current, authority) != e.fingerprint {
		r.revoke(handle)
		return nil, errAppsUnavailable
	}
	result, err := r.retain(cancelCtx, input, e.fingerprint, q, out, handle, e)
	success = err == nil
	return result, err
}
