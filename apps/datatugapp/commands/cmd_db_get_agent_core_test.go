package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/adapters/dalgo2memory"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/dal-go/record/update"
	"github.com/datatug/datatug-cli/pkg/auth/device"
	"github.com/datatug/datatug-core/pkg/dataaccess"
	"github.com/stretchr/testify/require"
)

type protectedAgentMemory struct {
	dal.DB
	dal.WriteSession
	reads, writes atomic.Int32
}

func (m *protectedAgentMemory) GetMulti(ctx context.Context, records []record.Record) error {
	m.reads.Add(1)
	return m.DB.GetMulti(ctx, records)
}
func (m *protectedAgentMemory) Set(ctx context.Context, rec record.Record) error {
	m.writes.Add(1)
	return m.WriteSession.Set(ctx, rec)
}
func (m *protectedAgentMemory) Insert(ctx context.Context, rec record.Record, options ...dal.InsertOption) error {
	m.writes.Add(1)
	return m.WriteSession.Insert(ctx, rec, options...)
}
func (m *protectedAgentMemory) Update(ctx context.Context, key *record.Key, changes []update.Update, conditions ...dal.Precondition) error {
	m.writes.Add(1)
	return m.WriteSession.Update(ctx, key, changes, conditions...)
}
func (m *protectedAgentMemory) Delete(ctx context.Context, key *record.Key) error {
	m.writes.Add(1)
	return m.WriteSession.Delete(ctx, key)
}

func TestProtectedAgentCoreCaptureImportAndTypedMutationConformance(t *testing.T) {
	for _, profile := range []string{"all", "none", "agent-none", "human-grant"} {
		t.Run(profile, func(t *testing.T) {
			ctx := context.Background()
			raw := dalgo2memory.New(dalgo2memory.FirestoreProfile())
			writer := raw.(dal.WriteSession)
			key := record.NewKeyWithID("Customer", "1")
			require.NoError(t, writer.Insert(ctx, record.NewRecordWithData(key, map[string]any{"name": "original"})))
			memory := &protectedAgentMemory{DB: raw, WriteSession: writer}
			resolution := protectedAgentResolution()
			resolution.Connection.Fingerprint.Engine, resolution.Protection.Fingerprint.Engine = "memory", "memory"
			switch profile {
			case "none":
				resolution.Protection.ReadPosture = dataaccess.ReadNone
			case "agent-none":
				resolution.Protection.AgentRead = dataaccess.AgentNone
			case "human-grant":
				resolution.Protection.ReadPosture = dataaccess.ReadNone
				human := resolution.Subject
				human.Kind = dataaccess.User
				resolution.Grants = []dataaccess.Grant{{ID: "human-grant", Subject: human, RequestedBy: human, Permissions: []dataaccess.Permission{dataaccess.DataRead}, Scope: dataaccess.GrantScope{Connection: resolution.Connection.ID, Requires: []string{}}, Reason: "investigate", ExpiresAt: resolution.ServerTime.Add(time.Minute), State: dataaccess.Approved, Decision: &dataaccess.Decision{By: dataaccess.Subject{Realm: human.Realm, Kind: dataaccess.User, ID: "reviewer"}, At: resolution.ServerTime}, Revision: 1}}
			}
			authority, err := dataaccess.NewAuthority(resolution.Subject, resolution.Connection, func(context.Context, dataaccess.ResolveRequest) (dataaccess.Resolution, error) {
				return resolution, nil
			})
			require.NoError(t, err)
			secured, err := authority.SecureDB(memory)
			require.NoError(t, err)
			request := dataaccess.ResultRequest{Nonce: "agent-result-fresh-nonce", RequestSHA256: hashProtectedAgentRequest()}
			started := time.Now()
			callback := false
			result, err := authority.CaptureResult(ctx, request, func(ctx context.Context) ([]byte, error) {
				callback = true
				data := map[string]any{}
				rec := record.NewRecordWithData(key, &data)
				if err := secured.Get(ctx, rec); err != nil {
					return nil, err
				}
				return json.Marshal(map[string]any{"record": data})
			})
			if profile == "all" {
				require.NoError(t, err)
				defer result.Close()
				require.True(t, callback)
				require.EqualValues(t, 1, memory.reads.Load())
				var wire bytes.Buffer
				require.NoError(t, result.WriteEnvelope(ctx, &wire))
				envelope, err := dataaccess.DecodeResultEnvelope(wire.Bytes())
				require.NoError(t, err)
				require.Equal(t, dataaccess.Agent, envelope.Lease.Subject.Kind)
				imported, err := authority.ImportResult(ctx, request, started, envelope)
				require.NoError(t, err)
				defer imported.Close()
				var output bytes.Buffer
				require.NoError(t, imported.WritePayload(ctx, &output))
				require.Contains(t, output.String(), "original")
			} else {
				require.Error(t, err)
				require.Nil(t, result)
				require.False(t, callback)
				require.Zero(t, memory.reads.Load())
			}
			// These are the same writable memory source and Agent-bound secured
			// database used above, not a fake adapter that refuses mutations.
			changed := record.NewRecordWithData(key, map[string]any{"name": "changed"})
			inserted := record.NewRecordWithData(record.NewKeyWithID("Customer", "2"), map[string]any{"name": "new"})
			for _, mutate := range []func() error{
				func() error { return secured.Set(ctx, changed) },
				func() error { return secured.Insert(ctx, inserted) },
				func() error { return secured.Update(ctx, key, []update.Update{update.ByFieldName("name", "changed")}) },
				func() error { return secured.Delete(ctx, key) },
			} {
				require.ErrorIs(t, mutate(), access.ErrAccessDenied)
			}
			require.Zero(t, memory.writes.Load())
			unchanged := map[string]any{}
			require.NoError(t, raw.Get(ctx, record.NewRecordWithData(key, &unchanged)))
			require.Equal(t, map[string]any{"name": "original"}, unchanged)
			exists, err := raw.Exists(ctx, inserted.Key())
			require.NoError(t, err)
			require.False(t, exists)
		})
	}
}

func hashProtectedAgentRequest() string {
	// An agreed fixed canonical-request digest; HTTP tests compute it from the
	// actual five-field command bytes independently on both ends.
	return "9d6fc6c80b79961a72ad8673cc70b2a0b2da7f94f0f506903d0de9295323f703"
}

func TestProtectedAgentStalledFreshResolverCannotBlockPurgeOrRelease(t *testing.T) {
	for _, boundary := range []string{"close", "cancel", "credential-expiry"} {
		t.Run(boundary, func(t *testing.T) {
			options, deps, _ := protectedReadFixture(t)
			options.agent = true
			credential, err := deps.credential(context.Background(), options.auth)
			require.NoError(t, err)
			if boundary == "credential-expiry" {
				credential.ExpiresAt = time.Now().Add(500 * time.Millisecond)
			}
			deps.credential = func(context.Context, device.ProductOptions) (device.ProductReadCredential, error) {
				return credential, nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered, unblock := make(chan context.Context, 1), make(chan struct{})
			var once sync.Once
			release := func() { once.Do(func() { close(unblock) }) }
			t.Cleanup(release)
			var armed atomic.Bool
			deps.client.Transport = protectedReadRoundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/v0/datatug/access/agent/read" {
					return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(protectedWireForResolution(t, r, protectedAgentResolution())))}, nil
				}
				require.Equal(t, "/v0/datatug/access/agent/resolve", r.URL.Path)
				if armed.Load() {
					entered <- r.Context()
					<-unblock
				}
				raw, err := json.Marshal(protectedAgentResolution())
				require.NoError(t, err)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(raw))}, nil
			})
			retained, err := readProtectedRecord(ctx, options, deps)
			require.NoError(t, err)
			defer retained.cancel()
			defer retained.result.Close()
			armed.Store(true)
			done := make(chan error, 1)
			var output bytes.Buffer
			go func() { done <- retained.result.WritePayload(retained.ctx, &output) }()
			var resolving context.Context
			select {
			case resolving = <-entered:
			case <-time.After(time.Second):
				t.Fatal("fresh agent resolver did not enter")
			}
			deadline, ok := resolving.Deadline()
			require.True(t, ok)
			require.False(t, deadline.After(credential.ExpiresAt))
			if boundary == "cancel" {
				cancel()
			}
			if boundary == "credential-expiry" {
				select {
				case <-resolving.Done():
				case <-time.After(time.Second):
					t.Fatal("credential ceiling was renewed")
				}
			}
			purged := make(chan error, 1)
			go func() {
				if boundary == "close" {
					retained.result.Close()
					purged <- nil
					return
				}
				var denied bytes.Buffer
				err := retained.result.WritePayload(context.Background(), &denied)
				if denied.Len() != 0 {
					purged <- io.ErrShortWrite
					return
				}
				purged <- err
			}()
			select {
			case err := <-purged:
				if boundary != "close" {
					require.Error(t, err)
				}
			case <-time.After(time.Second):
				t.Fatal("blocked resolver held result ownership lock")
			}
			release()
			select {
			case err := <-done:
				require.Error(t, err)
			case <-time.After(time.Second):
				t.Fatal("late resolver response did not refuse")
			}
			require.Empty(t, output.Bytes())
		})
	}
}
