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
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/recordset"
	"github.com/datatug/datatug-cli/pkg/auth/device"
	"github.com/datatug/datatug-core/pkg/dataaccess"
	"github.com/stretchr/testify/require"
)

func protectedTableFixture(t *testing.T) (protectedTableOptions, protectedReadDependencies, string) {
	t.Helper()
	o, d, token := protectedReadFixture(t)
	return protectedTableOptions{request: protectedTableRequest{Space: o.request.Space, Project: o.request.Project, Connection: o.request.Connection, Operation: dataaccess.TableRead{Object: dataaccess.ObjectRef{Namespace: "main", Name: "Customer"}, MaxRows: 2, MaxBytes: 4096}}, baseURL: o.baseURL, auth: o.auth}, d, token
}

type tableTransportSession struct {
	dal.ReadSession
	reader dal.RecordsetReader
}

func (s tableTransportSession) ExecuteQueryToRecordsetReader(context.Context, dal.Query, ...recordset.Option) (dal.RecordsetReader, error) {
	return s.reader, nil
}

type tableTransportReader struct {
	rs recordset.Recordset
	i  int
}

func (r *tableTransportReader) Recordset() recordset.Recordset { return r.rs }
func (r *tableTransportReader) Cursor() (string, error)        { return "", nil }
func (r *tableTransportReader) Close() error                   { return nil }
func (r *tableTransportReader) Next() (recordset.Row, recordset.Recordset, error) {
	if r.i == r.rs.RowsCount() {
		return nil, nil, dal.ErrNoMoreRecords
	}
	row := r.rs.GetRow(r.i)
	r.i++
	return row, r.rs, nil
}

type tableTransportSpan struct {
	session dal.ReadSession
	facts   dataaccess.TableIdentityFacts
}

func (s tableTransportSpan) Session() dal.ReadSession { return s.session }
func (s tableTransportSpan) Verify(context.Context, dataaccess.TableIdentityRequest) (dataaccess.TableIdentityFacts, error) {
	return s.facts, nil
}
func (s tableTransportSpan) Close() error { return nil }

type tableTransportSource func(context.Context, dataaccess.TableIdentityRequest) (dataaccess.TableReadSpan, error)

func (s tableTransportSource) OpenTableRead(ctx context.Context, r dataaccess.TableIdentityRequest) (dataaccess.TableReadSpan, error) {
	return s(ctx, r)
}

// This transport fixture executes real Core table construction/authorization and
// envelope serialization over a recording reader. It makes no native DB claim.
func protectedTableEnvelope(t *testing.T, r *http.Request, resolution dataaccess.Resolution) dataaccess.TableResultEnvelope {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	require.NoError(t, err)
	var command protectedTableRequest
	require.NoError(t, json.Unmarshal(body, &command))
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &fields))
	require.Len(t, fields, 4, "closed command contains scope and operation only")
	require.Equal(t, "space", command.Space)
	require.Equal(t, "project", command.Project)
	require.Equal(t, "catalog", command.Connection)
	require.Len(t, r.Header.Get("X-DataTug-Result-Nonce"), 32)
	columns := []recordset.Column[any]{recordset.NewTypedColumn[any]("id", nil), recordset.NewTypedColumn[any]("text", nil), recordset.NewTypedColumn[any]("nullable", nil), recordset.NewTypedColumn[any]("blob", nil)}
	rs := recordset.NewColumnarRecordset("transport", columns...)
	for _, v := range [][]any{{int64(9007199254740993), "Ada", nil, []byte{0, 255}}, {int64(2), "Grace", nil, []byte{}}, {int64(3), "extra", nil, []byte{}}} {
		row := rs.NewRow()
		for i, cell := range v {
			require.NoError(t, row.SetValueByIndex(i, cell, rs))
		}
	}
	authority, err := dataaccess.NewAuthority(resolution.Subject, resolution.Connection, func(context.Context, dataaccess.ResolveRequest) (dataaccess.Resolution, error) {
		return resolution, nil
	})
	require.NoError(t, err)
	digest := sha256.Sum256(body)
	result, err := authority.ReadTableResult(r.Context(), dataaccess.ResultRequest{Nonce: r.Header.Get("X-DataTug-Result-Nonce"), RequestSHA256: hex.EncodeToString(digest[:])}, command.Operation, tableTransportSource(func(context.Context, dataaccess.TableIdentityRequest) (dataaccess.TableReadSpan, error) {
		return tableTransportSpan{session: tableTransportSession{reader: &tableTransportReader{rs: rs}}, facts: dataaccess.TableIdentityFacts{Connection: resolution.Connection, Table: access.TableName{Schema: command.Operation.Object.Namespace, Name: command.Operation.Object.Name}}}, nil
	}))
	require.NoError(t, err)
	defer result.Close()
	var output bytes.Buffer
	require.NoError(t, result.WriteEnvelope(r.Context(), &output))
	envelope, err := dataaccess.DecodeTableResultEnvelope(output.Bytes())
	require.NoError(t, err)
	return envelope
}

func executeTablePreview(t *testing.T, ctx context.Context, d protectedReadDependencies, extra ...string) (string, error) {
	t.Helper()
	cmd := protectedTableCommand(d)
	cmd.SetContext(ctx)
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(io.Discard)
	args := []string{"--space", "space", "--project", "project", "--connection", "catalog", "--namespace", "main", "--table", "Customer", "--max-rows", "2", "--max-bytes", "4096"}
	cmd.SetArgs(append(args, extra...))
	err := cmd.Execute()
	return output.String(), err
}

func TestProtectedTableCommandRealHTTPAndFixedModes(t *testing.T) {
	mounted, _, err := dbCommand().Find([]string{"preview"})
	require.NoError(t, err)
	require.Equal(t, "preview", mounted.Name())
	require.Equal(t, "preview", dbPreviewCommand().Name())
	for _, agent := range []bool{false, true} {
		t.Run(map[bool]string{false: "user", true: "agent"}[agent], func(t *testing.T) {
			_, deps, token := protectedTableFixture(t)
			prefix := "/v0/" + protectedAccessPrefix(agent)
			reads, resolves := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, http.MethodPost, r.Method)
				require.Equal(t, "Bearer "+token, r.Header.Get("Authorization"))
				resolution := protectedResolution()
				if agent {
					resolution = protectedAgentResolution()
				}
				switch r.URL.Path {
				case prefix + "table/read":
					reads++
					envelope := protectedTableEnvelope(t, r, resolution)
					require.NoError(t, json.NewEncoder(w).Encode(envelope))
				case prefix + "resolve":
					resolves++
					var scope map[string]string
					require.NoError(t, json.NewDecoder(r.Body).Decode(&scope))
					require.Len(t, scope, 3)
					require.Equal(t, "project", scope["projectId"])
					require.NoError(t, json.NewEncoder(w).Encode(resolution))
				default:
					t.Errorf("unexpected route %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			t.Cleanup(server.Close)
			extra := []string{"--base-url", server.URL + "/v0/"}
			if agent {
				extra = append(extra, "--agent")
			}
			output, err := executeTablePreview(t, context.Background(), deps, extra...)
			require.NoError(t, err)
			require.Contains(t, output, "9007199254740993")
			var values struct {
				Rows [][]json.RawMessage `json:"rows"`
			}
			require.NoError(t, json.Unmarshal([]byte(output), &values))
			require.Equal(t, json.RawMessage(`9007199254740993`), values.Rows[0][0], "exact numeric token, never a float64 comparison")
			require.Equal(t, json.RawMessage(`null`), values.Rows[0][2])
			require.Contains(t, output, `{"base64":"AP8="}`)
			require.Contains(t, output, `"returnedRows":2`)
			require.Contains(t, output, `"hasMore":true`)
			require.Contains(t, output, `"complete":false`)
			require.NotContains(t, output, "extra")
			require.NotContains(t, output, token)
			require.Equal(t, 1, reads)
			require.GreaterOrEqual(t, resolves, 2)
		})
	}
}

func TestProtectedTableInputAndConstructionFailBeforeData(t *testing.T) {
	options, deps, _ := protectedTableFixture(t)
	calls := 0
	deps.client.Transport = protectedReadRoundTrip(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("unexpected") })
	var nilContext context.Context
	_, err := readProtectedTable(nilContext, options, deps)
	require.Error(t, err)
	for _, which := range []string{"space", "project", "connection", "namespace", "table"} {
		for _, bad := range []string{"", "*", "a.b", "a/b", "x?y", strings.Repeat("a", 129)} {
			o := options
			switch which {
			case "space":
				o.request.Space = bad
			case "project":
				o.request.Project = bad
			case "connection":
				o.request.Connection = bad
			case "namespace":
				o.request.Operation.Object.Namespace = bad
			case "table":
				o.request.Operation.Object.Name = bad
			}
			_, err := readProtectedTable(context.Background(), o, deps)
			require.Error(t, err)
		}
	}
	for _, bounds := range [][2]uint32{{0, 4096}, {101, 4096}, {2, 0}, {2, 262145}} {
		o := options
		o.request.Operation.MaxRows = bounds[0]
		o.request.Operation.MaxBytes = bounds[1]
		_, err := readProtectedTable(context.Background(), o, deps)
		require.Error(t, err)
	}
	o := options
	o.baseURL = "http://public.example/v0/"
	_, err = readProtectedTable(context.Background(), o, deps)
	require.Error(t, err)
	_, err = readProtectedTable(context.Background(), options, protectedReadDependencies{})
	require.Error(t, err)
	original := deps.credential
	for _, mode := range []string{"error", "legacy", "subject", "realm", "expired"} {
		deps.credential = func(ctx context.Context, o device.ProductOptions) (device.ProductReadCredential, error) {
			c, e := original(ctx, o)
			switch mode {
			case "error":
				return c, errors.New("login required")
			case "legacy":
				c.Bearer = "legacy"
			case "subject":
				c.Subject = ""
			case "realm":
				c.Realm = ""
			case "expired":
				c.ExpiresAt = time.Now().Add(-time.Second)
			}
			return c, e
		}
		_, err = readProtectedTable(context.Background(), options, deps)
		require.Error(t, err)
	}
	deps.credential = original
	deps.entropy = strings.NewReader("")
	_, err = readProtectedTable(context.Background(), options, deps)
	require.ErrorIs(t, err, io.EOF)
	deps.entropy = nil
	deps.request = func(context.Context, string, string, io.Reader) (*http.Request, error) {
		return nil, errors.New("construct")
	}
	_, err = readProtectedTable(context.Background(), options, deps)
	require.EqualError(t, err, "construct")
	require.Zero(t, calls)
	cmd := protectedTableCommand(deps)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--sql", "SELECT 1"})
	require.Error(t, cmd.Execute())
}

func TestProtectedTableRejectsWireBindingsAndReleaseChanges(t *testing.T) {
	for _, scenario := range []string{"transport", "denied", "unauthorized", "old-server", "redirect", "body-error", "oversize", "legacy-envelope", "unknown-envelope", "wrong-space", "bad-source", "wrong-nonce", "wrong-hash", "wrong-subject", "wrong-uid", "wrong-realm", "wrong-connection", "wrong-operation", "wrong-row-limit", "wrong-byte-limit", "payload-hash", "duplicate-profile", "malformed-surrogate", "payload-count", "payload-bytes", "resolve-revoked", "resolve-source", "resolve-denied", "resolve-null-agent-grants", "resolve-human-grants", "EOF", "Close", "close-error", "credential-expiry", "original-interval", "release-revoked", "release-expired"} {
		t.Run(scenario, func(t *testing.T) {
			options, deps, _ := protectedTableFixture(t)
			options.agent = true
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			resolving := 0
			reads := 0
			deps.client.Transport = protectedReadRoundTrip(func(r *http.Request) (*http.Response, error) {
				status := 200
				var raw []byte
				switch r.URL.Path {
				case "/v0/datatug/access/agent/resolve":
					resolving++
					resolution := protectedAgentResolution()
					switch scenario {
					case "resolve-revoked":
						resolution.Protection.AgentRead = dataaccess.AgentNone
					case "resolve-source":
						resolution.Connection.Fingerprint.EngineIdentity = "changed"
					case "resolve-denied":
						status = 403
					case "resolve-null-agent-grants":
						resolution.Grants = nil
					case "resolve-human-grants":
						resolution.Grants = []dataaccess.Grant{{ID: "human"}}
					case "release-revoked":
						if resolving >= 2 {
							resolution.Protection.AgentRead = dataaccess.AgentNone
						}
					}
					raw, _ = json.Marshal(resolution)
				case "/v0/datatug/access/agent/table/read":
					reads++
					switch scenario {
					case "transport":
						return nil, errors.New("offline")
					case "denied":
						status = 403
					case "unauthorized":
						status = 401
					case "old-server":
						status = 404
					case "redirect":
						status = 302
					}
					if status != 200 {
						return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
					}
					if scenario == "body-error" {
						return &http.Response{StatusCode: 200, Body: protectedReadErrorBody{}}, nil
					}
					if scenario == "oversize" {
						return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 294913)))}, nil
					}
					env := protectedTableEnvelope(t, r, protectedAgentResolution())
					switch scenario {
					case "legacy-envelope":
						env.Lease.Version = 1
					case "wrong-space":
						env.Lease.Connection.Space = "foreign"
					case "bad-source":
						env.Lease.Connection.Fingerprint.Engine = ""
					case "wrong-nonce":
						env.Lease.Nonce = "other"
					case "wrong-hash":
						env.Lease.RequestSHA256 = strings.Repeat("f", 64)
					case "wrong-subject":
						env.Lease.Subject.Kind = dataaccess.User
					case "wrong-uid":
						env.Lease.Subject.ID = "other"
					case "wrong-realm":
						env.Lease.Subject.Realm = "other"
					case "wrong-connection":
						env.Lease.Connection.ID = "other"
					case "wrong-operation":
						env.Lease.Operation.Object.Name = "Other"
					case "wrong-row-limit":
						env.Lease.Operation.MaxRows++
					case "wrong-byte-limit":
						env.Lease.Operation.MaxBytes++
					case "payload-hash":
						env.Payload = bytes.Replace(env.Payload, []byte("Ada"), []byte("tampered"), 1)
					case "payload-count":
						env.Payload = []byte(`{"columns":["x"],"rows":[[1]],"preview":{"returnedRows":2,"hasMore":false,"complete":true}}`)
					case "payload-bytes":
						env.Lease.Operation.MaxBytes = 1
					case "credential-expiry":
						<-r.Context().Done()
					case "original-interval":
						env.Lease.ExpiresAt = env.Lease.ServerTime.Add(time.Millisecond)
						time.Sleep(5 * time.Millisecond)
					}
					raw, _ = json.Marshal(env)
					if scenario == "duplicate-profile" {
						raw = bytes.Replace(raw, []byte(`"profile":`), []byte(`"profile":"table-recordset/1","profile":`), 1)
					}
					if scenario == "malformed-surrogate" {
						raw = bytes.Replace(raw, []byte(`"Ada"`), []byte(`"\ud800"`), 1)
					}
					if scenario == "unknown-envelope" {
						raw = bytes.Replace(raw, []byte(`"profile":`), []byte(`"unknown":true,"profile":`), 1)
					}
					if scenario == "EOF" || scenario == "Close" || scenario == "close-error" {
						return &http.Response{StatusCode: 200, Body: protectedBoundaryBody{Reader: strings.NewReader(string(raw)), cancel: cancel, atClose: scenario != "EOF", closeError: scenario == "close-error"}}, nil
					}
				default:
					t.Fatalf("fallback route %s", r.URL.Path)
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(raw))}, nil
			})
			if scenario == "credential-expiry" || scenario == "release-expired" {
				original := deps.credential
				deps.credential = func(ctx context.Context, o device.ProductOptions) (device.ProductReadCredential, error) {
					c, e := original(ctx, o)
					c.ExpiresAt = time.Now().Add(60 * time.Millisecond)
					return c, e
				}
			}
			result, err := readProtectedTable(ctx, options, deps)
			var output bytes.Buffer
			if err == nil {
				defer result.cancel()
				defer result.result.Close()
				if scenario == "release-expired" {
					<-result.ctx.Done()
				}
				err = result.result.WritePayload(result.ctx, &output)
			}
			require.Error(t, err)
			require.Empty(t, output.String())
			require.Equal(t, 1, reads)
		})
	}
}

func TestProtectedTableDoesNotFollowRedirectWithBearer(t *testing.T) {
	options, deps, _ := protectedTableFixture(t)
	var leaks atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaks.Add(1) }))
	t.Cleanup(target.Close)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(server.Close)
	options.baseURL = server.URL + "/v0/"
	_, err := readProtectedTable(context.Background(), options, deps)
	require.Error(t, err)
	require.Zero(t, leaks.Load())
}

func TestProtectedTableExactGrantImportAndRevocation(t *testing.T) {
	for _, change := range []string{"none", "unlisted", "revoked", "expired"} {
		t.Run(change, func(t *testing.T) {
			options, deps, _ := protectedTableFixture(t)
			resolution := protectedResolution()
			resolution.Protection.ReadPosture = dataaccess.ReadNone
			resolution.Grants = []dataaccess.Grant{{ID: "exact", Subject: resolution.Subject, RequestedBy: resolution.Subject, Permissions: []dataaccess.Permission{dataaccess.DataRead}, Scope: dataaccess.GrantScope{Connection: resolution.Connection.ID, Objects: []dataaccess.ObjectRef{options.request.Operation.Object}, Requires: []string{dataaccess.ObjectsExact}}, Reason: "Inspect owned fixture", ExpiresAt: time.Now().Add(150 * time.Millisecond), State: dataaccess.Approved, Decision: &dataaccess.Decision{By: resolution.Subject, At: time.Now()}, Revision: 1}}
			var revoked atomic.Bool
			deps.client.Transport = protectedReadRoundTrip(func(r *http.Request) (*http.Response, error) {
				var raw []byte
				switch r.URL.Path {
				case "/v0/datatug/access/table/read":
					raw, _ = json.Marshal(protectedTableEnvelope(t, r, resolution))
				case "/v0/datatug/access/resolve":
					fresh := resolution
					fresh.ServerTime = time.Now()
					fresh.ValidUntil = fresh.ServerTime.Add(time.Minute)
					if revoked.Load() {
						fresh.Grants = nil
					}
					if change == "unlisted" {
						fresh.Grants = append([]dataaccess.Grant(nil), resolution.Grants...)
						fresh.Grants[0].Scope.Objects = []dataaccess.ObjectRef{{Namespace: "main", Name: "Unlisted"}}
					}
					raw, _ = json.Marshal(fresh)
				default:
					t.Fatalf("unexpected route %s", r.URL.Path)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(raw))}, nil
			})
			result, err := readProtectedTable(context.Background(), options, deps)
			if change == "unlisted" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			defer result.cancel()
			defer result.result.Close()
			if change == "revoked" {
				revoked.Store(true)
			}
			if change == "expired" {
				time.Sleep(time.Until(resolution.Grants[0].ExpiresAt) + 5*time.Millisecond)
			}
			var output bytes.Buffer
			err = result.result.WritePayload(result.ctx, &output)
			if change == "none" {
				require.NoError(t, err)
				require.Contains(t, output.String(), "Ada")
			} else {
				require.Error(t, err)
				require.Empty(t, output.String())
			}
		})
	}
}

func TestProtectedTableCommandWithholdsValidationErrors(t *testing.T) {
	_, deps, _ := protectedTableFixture(t)
	cmd := protectedTableCommand(deps)
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	require.Error(t, cmd.Execute())
	require.Empty(t, output.String())
}
