package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/datatug/datatug-core/pkg/dataaccess"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/auth/device"
	"github.com/sneat-co/sneat-go-core/sneatauth"
	"github.com/stretchr/testify/require"
)

type protectedReadRoundTrip func(*http.Request) (*http.Response, error)

func (f protectedReadRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type protectedReadErrorBody struct{}

func (protectedReadErrorBody) Read([]byte) (int, error) { return 0, errors.New("body failed") }
func (protectedReadErrorBody) Close() error             { return nil }

func protectedReadFixture(t *testing.T) (protectedReadOptions, protectedReadDependencies, string) {
	t.Helper()
	token, _, err := sneatauth.NewProductDelegationBearer(bytes.NewReader(make([]byte, 32)))
	require.NoError(t, err)
	options := protectedReadOptions{request: protectedRecordRequest{Space: "space", Project: "project", Connection: "catalog", Collection: "Customer", ID: "1"}, baseURL: "https://api.fixture.test/v0/", auth: device.ProductOptions{Realm: "demo-datatug-access"}}
	dependencies := protectedReadDependencies{credential: func(context.Context, device.ProductOptions) (device.ProductReadCredential, error) {
		return device.ProductReadCredential{Bearer: token, Subject: "uid", Realm: "demo-datatug-access", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}, client: newProtectedReadHTTPClient()}
	return options, dependencies, token
}

func TestProtectedRecordCommandUsesSelectorsOnlyAndWithholdsDeniedResponses(t *testing.T) {
	mounted, _, err := dbCommand().Find([]string{"get"})
	require.NoError(t, err)
	require.Equal(t, "get", mounted.Name())
	_, dependencies, token := protectedReadFixture(t)
	status := 200
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/datatug/access/resolve" {
			require.NoError(t, json.NewEncoder(w).Encode(protectedResolution()))
			return
		}
		calls++
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/v0/datatug/access/read", r.URL.Path)
		require.Equal(t, "Bearer "+token, r.Header.Get("Authorization"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Len(t, body, 5)
		require.Equal(t, "space", body["spaceID"])
		require.Equal(t, "project", body["projectId"])
		require.Equal(t, "catalog", body["connectionId"])
		require.Equal(t, "Customer", body["collection"])
		require.Equal(t, "1", body["id"])
		w.WriteHeader(status)
		if status != http.StatusOK {
			return
		}
		encoded, _ := json.Marshal(protectedRecordRequest{Space: "space", Project: "project", Connection: "catalog", Collection: "Customer", ID: "1"})
		digest := sha256.Sum256(encoded)
		resolution := protectedResolution()
		authority, err := dataaccess.NewAuthority(resolution.Subject, resolution.Connection, func(context.Context, dataaccess.ResolveRequest) (dataaccess.Resolution, error) {
			return protectedResolution(), nil
		})
		require.NoError(t, err)
		result, err := authority.CaptureResult(r.Context(), dataaccess.ResultRequest{Nonce: r.Header.Get("X-DataTug-Result-Nonce"), RequestSHA256: hex.EncodeToString(digest[:])}, func(context.Context) ([]byte, error) {
			return []byte(`{"record":{"id":9007199254740993,"name":"permitted"}}`), nil
		})
		require.NoError(t, err)
		defer result.Close()
		require.NoError(t, result.WriteEnvelope(r.Context(), w))
	}))
	t.Cleanup(server.Close)
	run := func() (*bytes.Buffer, error) {
		cmd := protectedReadCommand(dependencies)
		var output bytes.Buffer
		cmd.SetOut(&output)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{"--base-url", server.URL + "/v0/", "--space", "space", "--project", "project", "--connection", "catalog", "--collection", "Customer", "--id", "1"})
		err := cmd.Execute()
		return &output, err
	}
	out, err := run()
	require.NoError(t, err)
	require.Contains(t, out.String(), "9007199254740993")
	require.Contains(t, out.String(), "permitted")
	require.NotContains(t, out.String(), token)
	for _, code := range []int{401, 403, 400, 404, 503, 302} {
		status = code
		out, err = run()
		require.Error(t, err)
		require.Empty(t, out.String(), "denials/errors cannot carry partial record data")
		var exit ExitCoder
		require.ErrorAs(t, err, &exit)
		if code == 401 || code == 403 {
			require.Equal(t, exitCodeAccessDenied, exit.ExitCode())
		} else {
			require.Equal(t, exitCodeDatabase, exit.ExitCode())
		}
	}
	require.Equal(t, 7, calls)
	command := dbGetCommand()
	require.Equal(t, "get", command.Name())
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"--sql", "SELECT 1"})
	require.Error(t, command.Execute())
}

func TestProtectedRecordRejectsInvalidInputCredentialsAndWireShapes(t *testing.T) {
	options, dependencies, token := protectedReadFixture(t)
	calls := 0
	dependencies.client = &http.Client{Transport: protectedReadRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"record":{"id":1}}`))}, nil
	})}
	for _, bad := range []string{"", ".", "..", "/x", "x\\y", "x?y", "x#y", " x", "x\n", strings.Repeat("x", 257)} {
		copy := options
		copy.request.ID = bad
		_, err := readProtectedRecord(context.Background(), copy, dependencies)
		require.Error(t, err)
	}
	for _, bad := range []string{"https://api.test/", "https://user:secret@api.test/v0/", "http://public.test/v0/"} {
		copy := options
		copy.baseURL = bad
		_, err := readProtectedRecord(context.Background(), copy, dependencies)
		require.Error(t, err)
	}
	require.Zero(t, calls)
	_, err := readProtectedRecord(context.Background(), options, protectedReadDependencies{})
	require.Error(t, err)
	original := dependencies.credential
	dependencies.credential = func(context.Context, device.ProductOptions) (device.ProductReadCredential, error) {
		return device.ProductReadCredential{}, errors.New("not logged in")
	}
	_, err = readProtectedRecord(context.Background(), options, dependencies)
	require.Error(t, err)
	dependencies.credential = func(context.Context, device.ProductOptions) (device.ProductReadCredential, error) {
		return device.ProductReadCredential{Bearer: "firebase-id"}, nil
	}
	_, err = readProtectedRecord(context.Background(), options, dependencies)
	require.Error(t, err)
	require.Zero(t, calls)
	dependencies.credential = original
	var invalidContext context.Context
	_, err = readProtectedRecord(invalidContext, options, dependencies)
	require.Error(t, err)
	for _, body := range []string{`{}`, `null`, `{"record":null}`, `{"record":[]}`, `{"record":{},"future":true}`, `{"record":{}} {}`, strings.Repeat("x", (1<<20)+1)} {
		dependencies.client.Transport = protectedReadRoundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		_, err = readProtectedRecord(context.Background(), options, dependencies)
		require.Error(t, err)
	}
	dependencies.client.Transport = protectedReadRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: protectedReadErrorBody{}}, nil
	})
	_, err = readProtectedRecord(context.Background(), options, dependencies)
	require.Error(t, err)
	dependencies.client.Transport = protectedReadRoundTrip(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("transport contains private data " + token)
	})
	_, err = readProtectedRecord(context.Background(), options, dependencies)
	require.Error(t, err)
	require.NotContains(t, err.Error(), token)
}

func TestProtectedRecordSelectorsMatchServerProfileBeforeCredentialIO(t *testing.T) {
	options, dependencies, _ := protectedReadFixture(t)
	credentialCalls, httpCalls := 0, 0
	dependencies.credential = func(context.Context, device.ProductOptions) (device.ProductReadCredential, error) {
		credentialCalls++
		return device.ProductReadCredential{}, nil
	}
	dependencies.client = &http.Client{Transport: protectedReadRoundTrip(func(*http.Request) (*http.Response, error) { httpCalls++; return nil, errors.New("unexpected HTTP") })}
	for _, set := range []func(*protectedRecordRequest, string){func(r *protectedRecordRequest, v string) { r.Space = v }, func(r *protectedRecordRequest, v string) { r.Project = v }, func(r *protectedRecordRequest, v string) { r.Connection = v }, func(r *protectedRecordRequest, v string) { r.Collection = v }, func(r *protectedRecordRequest, v string) { r.ID = v }} {
		for _, invalid := range []string{"bad space", strings.Repeat("x", 129), "caf\u00e9", "a.b"} {
			copy := options
			set(&copy.request, invalid)
			result, err := readProtectedRecord(context.Background(), copy, dependencies)
			require.Nil(t, result)
			var exit ExitCoder
			require.ErrorAs(t, err, &exit)
			require.Equal(t, exitCodeUsage, exit.ExitCode())
		}
	}
	require.Zero(t, credentialCalls)
	require.Zero(t, httpCalls)
	for _, valid := range []string{"a-Z_09", strings.Repeat("x", 128)} {
		require.True(t, validProtectedSelector(valid))
	}
}

func TestProtectedRecordNeverForwardsBearerAcrossRedirect(t *testing.T) {
	options, dependencies, _ := protectedReadFixture(t)
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls++ }))
	t.Cleanup(target.Close)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(server.Close)
	options.baseURL = server.URL + "/v0/"
	_, err := readProtectedRecord(context.Background(), options, dependencies)
	require.Error(t, err)
	require.Zero(t, targetCalls)
}

func protectedResolution() dataaccess.Resolution {
	now := time.Now()
	fingerprint := dataaccess.Fingerprint{Engine: "sqlite", Database: "fixture", EngineIdentity: "fixture-instance"}
	return dataaccess.Resolution{Subject: dataaccess.Subject{Realm: "demo-datatug-access", Kind: dataaccess.User, ID: "uid"}, Connection: dataaccess.ConnectionRef{Space: "space", ID: "protection", Fingerprint: fingerprint}, Protection: dataaccess.ConnectionProtection{ID: "protection", Fingerprint: fingerprint, ReadPosture: dataaccess.ReadAll, AgentRead: dataaccess.AgentInherit}, ServerTime: now, ValidUntil: now.Add(time.Minute)}
}

// These bodies reproduce cancellation at the final receive boundaries without
// opening ports or returning any data before server/Core authorization.
type protectedBoundaryBody struct {
	*strings.Reader
	cancel     context.CancelFunc
	atClose    bool
	closeError bool
}

func (b protectedBoundaryBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err == io.EOF && !b.atClose {
		b.cancel()
	}
	return n, err
}
func (b protectedBoundaryBody) Close() error {
	if b.atClose {
		b.cancel()
	}
	if b.closeError {
		return errors.New("close failed")
	}
	return nil
}

func TestProtectedRecordBufferedReleaseFences(t *testing.T) {
	for _, boundary := range []string{"EOF", "Close", "close-error", "credential-expiry", "revoked-before-output", "wrong-nonce", "wrong-space", "bad-engine", "bad-payload", "resolve-error", "resolve-invalid", "resolve-oversize", "resolve-denied", "resolve-body-error"} {
		t.Run(boundary, func(t *testing.T) {
			_, deps, _ := protectedReadFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			resolves := 0
			deps.client = &http.Client{Transport: protectedReadRoundTrip(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/resolve") {
					resolves++
					if boundary == "resolve-error" {
						return nil, errors.New("transport failed")
					}
					if boundary == "resolve-denied" {
						return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader(""))}, nil
					}
					if boundary == "resolve-body-error" {
						return &http.Response{StatusCode: 200, Body: protectedReadErrorBody{}}, nil
					}
					if boundary == "resolve-invalid" {
						return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"future":true}`))}, nil
					}
					if boundary == "resolve-oversize" {
						return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", (1<<20)+1)))}, nil
					}
					resolution := protectedResolution()
					if boundary == "revoked-before-output" && resolves >= 2 {
						resolution.Protection.ReadPosture = dataaccess.ReadNone
					}
					raw, _ := json.Marshal(resolution)
					return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(raw))}, nil
				}
				requestBytes, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				digest := sha256.Sum256(requestBytes)
				resolution := protectedResolution()
				authority, err := dataaccess.NewAuthority(resolution.Subject, resolution.Connection, func(context.Context, dataaccess.ResolveRequest) (dataaccess.Resolution, error) {
					return protectedResolution(), nil
				})
				require.NoError(t, err)
				result, err := authority.CaptureResult(ctx, dataaccess.ResultRequest{Nonce: r.Header.Get("X-DataTug-Result-Nonce"), RequestSHA256: hex.EncodeToString(digest[:])}, func(context.Context) ([]byte, error) { return []byte(`{"record":{"id":"one"}}`), nil })
				require.NoError(t, err)
				defer result.Close()
				var encoded bytes.Buffer
				require.NoError(t, result.WriteEnvelope(ctx, &encoded))
				envelope, err := dataaccess.DecodeResultEnvelope(encoded.Bytes())
				require.NoError(t, err)
				switch boundary {
				case "wrong-nonce":
					envelope.Lease.Nonce = "different-unpredictable-nonce"
				case "wrong-space":
					envelope.Lease.Connection.Space = "foreign-space"
				case "bad-engine":
					envelope.Lease.Connection.Fingerprint.Engine = ""
				case "bad-payload":
					envelope.Payload = []byte(`{"unexpected":true}`)
				case "credential-expiry":
					<-r.Context().Done()
				}
				raw, err := json.Marshal(envelope)
				require.NoError(t, err)
				body := io.ReadCloser(io.NopCloser(bytes.NewReader(raw)))
				if boundary == "EOF" || boundary == "Close" || boundary == "close-error" {
					body = protectedBoundaryBody{Reader: strings.NewReader(string(raw)), cancel: cancel, atClose: boundary != "EOF", closeError: boundary == "close-error"}
				}
				return &http.Response{StatusCode: 200, Body: body}, nil
			})}
			if boundary == "credential-expiry" {
				original := deps.credential
				deps.credential = func(ctx context.Context, o device.ProductOptions) (device.ProductReadCredential, error) {
					v, e := original(ctx, o)
					v.ExpiresAt = time.Now().Add(50 * time.Millisecond)
					return v, e
				}
			}
			cmd := protectedReadCommand(deps)
			cmd.SetContext(ctx)
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"--space", "space", "--project", "project", "--connection", "catalog", "--collection", "Customer", "--id", "1"})
			require.Error(t, cmd.Execute())
			require.Empty(t, output.String(), "no buffered record may cross an invalid release boundary")
		})
	}
}

func TestProtectedRecordRequiresVerifiedOriginalCredentialFacts(t *testing.T) {
	for _, mutate := range []func(*device.ProductReadCredential){func(c *device.ProductReadCredential) { c.Subject = "" }, func(c *device.ProductReadCredential) { c.Realm = "" }, func(c *device.ProductReadCredential) { c.ExpiresAt = time.Now().Add(-time.Second) }} {
		options, deps, _ := protectedReadFixture(t)
		original := deps.credential
		deps.credential = func(ctx context.Context, o device.ProductOptions) (device.ProductReadCredential, error) {
			v, e := original(ctx, o)
			mutate(&v)
			return v, e
		}
		_, err := readProtectedRecord(context.Background(), options, deps)
		require.Error(t, err)
	}
}

func TestProtectedResolveRejectsInvalidTransportURL(t *testing.T) {
	options, deps, token := protectedReadFixture(t)
	options.baseURL = ":bad-url"
	_, err := resolveProtectedRecord(context.Background(), options, deps.client, token)
	require.Error(t, err)
}
func TestProtectedReadConstructionFailuresWithholdOutput(t *testing.T) {
	options, deps, _ := protectedReadFixture(t)
	deps.entropy = strings.NewReader("")
	_, err := readProtectedRecord(context.Background(), options, deps)
	require.ErrorIs(t, err, io.EOF)
	deps.entropy = nil
	deps.request = func(context.Context, string, string, io.Reader) (*http.Request, error) {
		return nil, errors.New("request construction failed")
	}
	_, err = readProtectedRecord(context.Background(), options, deps)
	require.EqualError(t, err, "request construction failed")
}

func TestProtectedPayloadClosedRecordWrapper(t *testing.T) {
	for _, data := range []string{`{}`, `null`, `{bad`, `{"record":null}`, `{"record":[]}`, `{"record":{},"future":true}`, `{"other":{}}`} {
		require.False(t, validProtectedPayload([]byte(data)))
	}
	require.True(t, validProtectedPayload([]byte(`{"record":{"id":1}}`)))
}
