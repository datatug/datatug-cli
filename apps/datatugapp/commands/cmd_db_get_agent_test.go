package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/auth/device"
	"github.com/datatug/datatug-core/pkg/dataaccess"
	"github.com/stretchr/testify/require"
)

func protectedAgentResolution() dataaccess.Resolution {
	resolution := protectedResolution()
	resolution.Subject.Kind = dataaccess.Agent
	resolution.Grants = []dataaccess.Grant{}
	return resolution
}

// The port-free transport fixture uses the real Core capture/envelope fence.
// Native/source and User-to-Agent reduction proofs belong to the server lane.
func protectedWireForResolution(t *testing.T, request *http.Request, resolution dataaccess.Resolution) []byte {
	t.Helper()
	body, err := io.ReadAll(request.Body)
	require.NoError(t, err)
	var selectors map[string]string
	require.NoError(t, json.Unmarshal(body, &selectors))
	require.Len(t, selectors, 5, "mode/subject/authority are never request fields")
	require.Equal(t, "space", selectors["spaceID"])
	require.Equal(t, "project", selectors["projectId"])
	require.Equal(t, "catalog", selectors["connectionId"])
	require.Equal(t, "Customer", selectors["collection"])
	require.Equal(t, "1", selectors["id"])
	nonce := request.Header.Get("X-DataTug-Result-Nonce")
	require.Len(t, nonce, 32)
	require.Equal(t, strings.ToLower(nonce), nonce)
	_, err = hex.DecodeString(nonce)
	require.NoError(t, err)
	digest := sha256.Sum256(body)
	authority, err := dataaccess.NewAuthority(resolution.Subject, resolution.Connection, func(context.Context, dataaccess.ResolveRequest) (dataaccess.Resolution, error) {
		return resolution, nil
	})
	require.NoError(t, err)
	result, err := authority.CaptureResult(request.Context(), dataaccess.ResultRequest{Nonce: nonce, RequestSHA256: hex.EncodeToString(digest[:])}, func(context.Context) ([]byte, error) {
		return []byte(`{"record":{"id":9007199254740993,"name":"agent baseline"}}`), nil
	})
	require.NoError(t, err)
	defer result.Close()
	var wire bytes.Buffer
	require.NoError(t, result.WriteEnvelope(request.Context(), &wire))
	return append([]byte(nil), wire.Bytes()...)
}

type protectedAgentBody struct {
	io.Reader
	closes atomic.Int32
	close  func() error
}

func (b *protectedAgentBody) Close() error {
	b.closes.Add(1)
	if b.close != nil {
		return b.close()
	}
	return nil
}

func executeProtectedAgent(t *testing.T, dependencies protectedReadDependencies, ctx context.Context) (string, error) {
	t.Helper()
	cmd := protectedReadCommand(dependencies)
	cmd.SetContext(ctx)
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--agent", "--space", "space", "--project", "project", "--connection", "catalog", "--collection", "Customer", "--id", "1"})
	err := cmd.Execute()
	return output.String(), err
}

func TestProtectedAgentCommandSelectsExactRoutesAndKeepsNormalUserMode(t *testing.T) {
	mounted, _, err := dbCommand().Find([]string{"get"})
	require.NoError(t, err)
	require.NotNil(t, mounted.Flags().Lookup("agent"))
	require.Equal(t, "false", mounted.Flags().Lookup("agent").DefValue)
	for _, agent := range []bool{false, true} {
		t.Run(map[bool]string{false: "user", true: "agent"}[agent], func(t *testing.T) {
			_, dependencies, token := protectedReadFixture(t)
			readPath, resolvePath := "/v0/datatug/access/read", "/v0/datatug/access/resolve"
			resolution := protectedResolution
			if agent {
				readPath, resolvePath, resolution = "/v0/datatug/access/agent/read", "/v0/datatug/access/agent/resolve", protectedAgentResolution
			}
			reads, resolves := 0, 0
			var bodies []*protectedAgentBody
			dependencies.client.Transport = protectedReadRoundTrip(func(r *http.Request) (*http.Response, error) {
				require.Equal(t, http.MethodPost, r.Method)
				require.Equal(t, "Bearer "+token, r.Header.Get("Authorization"))
				var raw []byte
				switch r.URL.Path {
				case readPath:
					reads++
					raw = protectedWireForResolution(t, r, resolution())
				case resolvePath:
					resolves++
					var scope map[string]string
					require.NoError(t, json.NewDecoder(r.Body).Decode(&scope))
					require.Len(t, scope, 3)
					require.Equal(t, "project", scope["projectId"])
					require.Empty(t, r.Header.Get("X-DataTug-Result-Nonce"), "only the actual read consumes a nonce")
					var err error
					raw, err = json.Marshal(resolution())
					require.NoError(t, err)
				default:
					t.Fatalf("unexpected route %s", r.URL.Path)
				}
				body := &protectedAgentBody{Reader: bytes.NewReader(raw)}
				bodies = append(bodies, body)
				return &http.Response{StatusCode: 200, Body: body}, nil
			})
			cmd := protectedReadCommand(dependencies)
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(io.Discard)
			args := []string{"--space", "space", "--project", "project", "--connection", "catalog", "--collection", "Customer", "--id", "1"}
			if agent {
				args = append(args, "--agent")
			}
			cmd.SetArgs(args)
			require.NoError(t, cmd.Execute())
			require.Contains(t, output.String(), "9007199254740993")
			require.Contains(t, output.String(), "agent baseline")
			require.NotContains(t, output.String(), token)
			require.Equal(t, 1, reads)
			require.GreaterOrEqual(t, resolves, 2, "fresh import and final release authority use the same fixed route")
			for _, body := range bodies {
				require.EqualValues(t, 1, body.closes.Load())
			}
		})
	}
}

func TestProtectedAgentRejectsCompatibilityAndAmbiguousAuthorityWithoutFallback(t *testing.T) {
	for _, scenario := range []string{"read-old-server", "resolve-old-server", "read-redirect", "resolve-redirect", "human-envelope", "human-resolution", "wrong-uid", "wrong-realm", "null-grants", "human-grants", "duplicate-grants", "duplicate-subject", "duplicate-subject-kind", "unknown-version", "wrong-nonce", "wrong-digest", "read-oversize", "resolve-oversize"} {
		t.Run(scenario, func(t *testing.T) {
			_, dependencies, _ := protectedReadFixture(t)
			humanCalls := 0
			var bodies []*protectedAgentBody
			dependencies.client.Transport = protectedReadRoundTrip(func(r *http.Request) (*http.Response, error) {
				status := 200
				var raw []byte
				headers := http.Header{}
				switch r.URL.Path {
				case "/v0/datatug/access/agent/read":
					if scenario == "read-old-server" {
						status = 404
						break
					}
					if scenario == "read-redirect" {
						status = 307
						headers.Set("Location", "https://api.fixture.test/v0/datatug/access/read")
						break
					}
					if scenario == "read-oversize" {
						raw = bytes.Repeat([]byte{'x'}, (1<<20)+1)
						break
					}
					resolution := protectedAgentResolution()
					if scenario == "human-envelope" {
						resolution = protectedResolution()
					}
					raw = protectedWireForResolution(t, r, resolution)
					var envelope dataaccess.ResultEnvelope
					require.NoError(t, json.Unmarshal(raw, &envelope))
					switch scenario {
					case "unknown-version":
						envelope.Lease.Version = 2
					case "wrong-nonce":
						envelope.Lease.Nonce = "other-unpredictable-nonce"
					case "wrong-digest":
						envelope.Lease.RequestSHA256 = strings.Repeat("f", 64)
					}
					var err error
					raw, err = json.Marshal(envelope)
					require.NoError(t, err)
				case "/v0/datatug/access/agent/resolve":
					if scenario == "resolve-old-server" {
						status = 404
						break
					}
					if scenario == "resolve-redirect" {
						status = 302
						headers.Set("Location", "https://api.fixture.test/v0/datatug/access/resolve")
						break
					}
					if scenario == "resolve-oversize" {
						raw = bytes.Repeat([]byte{'x'}, (1<<20)+1)
						break
					}
					resolution := protectedAgentResolution()
					switch scenario {
					case "human-resolution":
						resolution.Subject.Kind = dataaccess.User
					case "wrong-uid":
						resolution.Subject.ID = "other-uid"
					case "wrong-realm":
						resolution.Subject.Realm = "other-realm"
					case "null-grants":
						resolution.Grants = nil
					case "human-grants":
						resolution.Grants = []dataaccess.Grant{{ID: "human-grant"}}
					}
					var err error
					raw, err = json.Marshal(resolution)
					require.NoError(t, err)
					switch scenario {
					case "duplicate-grants":
						raw = []byte(strings.Replace(string(raw), `"grants":[]`, `"grants":null,"grants":[]`, 1))
					case "duplicate-subject":
						subject, _ := json.Marshal(resolution.Subject)
						raw = append(append([]byte(`{"subject":`), subject...), append([]byte(","), raw[1:]...)...)
					case "duplicate-subject-kind":
						raw = []byte(strings.Replace(string(raw), `"kind":"agent"`, `"kind":"user","Kind":"agent"`, 1))
					}
				default:
					humanCalls++
					status = 500
				}
				body := &protectedAgentBody{Reader: bytes.NewReader(raw)}
				bodies = append(bodies, body)
				return &http.Response{StatusCode: status, Body: body, Header: headers}, nil
			})
			out, err := executeProtectedAgent(t, dependencies, context.Background())
			require.Error(t, err)
			require.Empty(t, out)
			require.Zero(t, humanCalls, "there is no human compatibility retry")
			for _, body := range bodies {
				require.EqualValues(t, 1, body.closes.Load())
			}
		})
	}
}

func TestProtectedAgentRetainedReleaseKeepsIdentityProtectionAndCredentialCeiling(t *testing.T) {
	for _, scenario := range []string{"user", "revoke", "revision", "source", "caller-cancel", "credential-expiry"} {
		t.Run(scenario, func(t *testing.T) {
			options, dependencies, _ := protectedReadFixture(t)
			options.agent = true
			credential, err := dependencies.credential(context.Background(), options.auth)
			require.NoError(t, err)
			if scenario == "credential-expiry" {
				credential.ExpiresAt = time.Now().Add(500 * time.Millisecond)
			}
			dependencies.credential = func(context.Context, device.ProductOptions) (device.ProductReadCredential, error) {
				return credential, nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			changed := false
			reads, resolves := 0, 0
			dependencies.client.Transport = protectedReadRoundTrip(func(r *http.Request) (*http.Response, error) {
				deadline, ok := r.Context().Deadline()
				require.True(t, ok)
				require.False(t, deadline.After(credential.ExpiresAt))
				var raw []byte
				if r.URL.Path == "/v0/datatug/access/agent/read" {
					reads++
					raw = protectedWireForResolution(t, r, protectedAgentResolution())
				} else {
					require.Equal(t, "/v0/datatug/access/agent/resolve", r.URL.Path)
					resolves++
					resolution := protectedAgentResolution()
					if changed {
						switch scenario {
						case "user":
							resolution.Subject.Kind = dataaccess.User
						case "revoke":
							resolution.Protection.ReadPosture = dataaccess.ReadNone
						case "revision":
							resolution.Protection.Revision++
						case "source":
							resolution.Connection.Fingerprint.EngineIdentity = "other-source"
						}
					}
					var err error
					raw, err = json.Marshal(resolution)
					require.NoError(t, err)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(raw))}, nil
			})
			retained, err := readProtectedRecord(ctx, options, dependencies)
			require.NoError(t, err)
			defer retained.cancel()
			defer retained.result.Close()
			changed = true
			if scenario == "caller-cancel" {
				cancel()
			}
			if scenario == "credential-expiry" {
				select {
				case <-retained.ctx.Done():
				case <-time.After(time.Second):
					t.Fatal("original credential ceiling was extended")
				}
			}
			var output bytes.Buffer
			require.Error(t, retained.result.WritePayload(retained.ctx, &output))
			require.Empty(t, output.Bytes())
			require.Error(t, retained.result.WritePayload(context.Background(), &output), "failed release purges owned result rather than falling back")
			require.Empty(t, output.Bytes())
			require.Equal(t, 1, reads)
			if scenario == "caller-cancel" || scenario == "credential-expiry" {
				require.Equal(t, 1, resolves, "local expiry/cancellation refuses before more authority I/O")
			} else {
				require.GreaterOrEqual(t, resolves, 2)
			}
		})
	}
}

func TestProtectedAgentMidReceiveCancellationAndConstructionFailures(t *testing.T) {
	for _, boundary := range []string{"EOF", "Close", "close-error", "entropy", "construction"} {
		t.Run(boundary, func(t *testing.T) {
			options, deps, _ := protectedReadFixture(t)
			options.agent = true
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			deps.client.Transport = protectedReadRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				var raw []byte
				if r.URL.Path == "/v0/datatug/access/agent/read" {
					raw = protectedWireForResolution(t, r, protectedAgentResolution())
					return &http.Response{StatusCode: 200, Body: protectedBoundaryBody{Reader: strings.NewReader(string(raw)), cancel: cancel, atClose: boundary != "EOF", closeError: boundary == "close-error"}}, nil
				}
				require.Equal(t, "/v0/datatug/access/agent/resolve", r.URL.Path)
				raw, _ = json.Marshal(protectedAgentResolution())
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(raw))}, nil
			})
			if boundary == "entropy" {
				deps.entropy = strings.NewReader("")
			}
			if boundary == "construction" {
				deps.request = func(_ context.Context, method, url string, _ io.Reader) (*http.Request, error) {
					require.Equal(t, http.MethodPost, method)
					require.Equal(t, "https://api.fixture.test/v0/datatug/access/agent/read", url)
					return nil, errors.New("request refused")
				}
			}
			retained, err := readProtectedRecord(ctx, options, deps)
			require.Error(t, err)
			require.Nil(t, retained)
			if boundary == "entropy" || boundary == "construction" {
				require.Zero(t, calls)
			}
		})
	}
}
