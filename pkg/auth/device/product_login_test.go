package device

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

	"github.com/sneat-co/sneat-go-core/sneatauth"
	"github.com/stretchr/testify/require"
	"github.com/strongo/deviceauth"
	"golang.org/x/oauth2"
)

func TestProductLoginRechecksAfterSharedHelperIO(t *testing.T) {
	for _, stage := range []string{"second userinfo expiry", "second userinfo cancellation", "replacement revocation expiry", "replacement revocation cancellation"} {
		t.Run(stage, func(t *testing.T) {
			token := fixtureProductToken(t)
			identity := fixtureProductIdentity()
			priorDir, priorHTTP, priorBrowser := userConfigDir, productHTTPClient, productOpenBrowser
			t.Cleanup(func() { userConfigDir, productHTTPClient, productOpenBrowser = priorDir, priorHTTP, priorBrowser })
			dir := t.TempDir()
			userConfigDir = func() (string, error) { return dir, nil }
			productOpenBrowser = func(string) error { return nil }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			options := ProductOptions{Issuer: "https://fixture.test" + sneatauth.ProductDelegationPath, Realm: identity.Realm, InsecureStorage: true}
			p, err := newProductClient(options)
			require.NoError(t, err)
			oldBearer, _, err := sneatauth.NewProductDelegationBearer(bytes.NewReader(bytes.Repeat([]byte{1}, 32)))
			require.NoError(t, err)
			replacement := strings.HasPrefix(stage, "replacement")
			if replacement {
				require.NoError(t, p.client.ScopedStore(p.store).Save(deviceauth.Credential{AccessToken: oldBearer, TokenType: "Bearer", Expiry: time.Now().Add(time.Minute), AccountID: identity.Subject}))
			}
			userinfoCalls := 0
			var revoked []string
			invalidate := func() {
				if strings.HasSuffix(stage, "expiry") {
					time.Sleep(1100 * time.Millisecond) // The command must preserve the original one-second wire expiry.
				} else {
					cancel()
				}
			}
			productHTTPClient = &http.Client{Transport: productRoundTrip(func(r *http.Request) (*http.Response, error) {
				var value any
				switch r.URL.Path {
				case sneatauth.ProductDelegationPath + "/oauth/device/code":
					value = map[string]any{"device_code": "fixture-code", "user_code": "ABCD-EFGH", "verification_uri": "https://fixture.test/device", "expires_in": 120, "interval": 1}
				case sneatauth.ProductDelegationPath + "/oauth/token":
					value = map[string]any{"credential_family": string(sneatauth.ProductDelegationFamily), "access_token": token.AccessToken, "token_type": "Bearer", "expires_in": 1, "scope": identity.Scope}
				case sneatauth.ProductDelegationPath + "/oauth/userinfo":
					userinfoCalls++
					if userinfoCalls == 2 && !replacement {
						invalidate()
					}
					value = identity
				case sneatauth.ProductDelegationPath + "/oauth/revoke":
					require.NoError(t, r.ParseForm())
					revoked = append(revoked, r.Form.Get("token"))
					if r.Form.Get("token") == oldBearer {
						invalidate()
					}
					value = map[string]any{}
				default:
					t.Fatalf("unexpected request: %s", r.URL.Path)
				}
				raw, err := json.Marshal(value)
				require.NoError(t, err)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(raw)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
			})}
			command := ProductCommand()
			var output bytes.Buffer
			command.SetContext(ctx)
			command.SetOut(&output)
			command.SetErr(io.Discard)
			command.SetArgs([]string{"login", "--auth-host", options.Issuer, "--realm", options.Realm, "--insecure-storage"})
			require.Error(t, command.Execute())
			require.Equal(t, 2, userinfoCalls, "exercise both the strict transformer and the helper userinfo")
			require.NotContains(t, output.String(), "Product login active")
			require.NotContains(t, output.String(), token.AccessToken)
			_, err = p.client.ScopedStore(p.store).Load()
			require.ErrorIs(t, err, deviceauth.ErrCredentialNotFound)
			require.Contains(t, revoked, token.AccessToken, "failed login must clean up the newly issued server credential")
			if replacement {
				require.Equal(t, []string{oldBearer, token.AccessToken}, revoked)
			}
		})
	}
}

type productLoginFixtureStore struct {
	deviceauth.Credential
	saveErr, loadErr, deleteErr error
	afterSave                   func()
	write                       bool
	saves, deletes              int
}

func (s *productLoginFixtureStore) Save(c deviceauth.Credential) error {
	s.saves++
	if s.write {
		s.Credential = c
	}
	if s.afterSave != nil {
		s.afterSave()
	}
	return s.saveErr
}
func (s *productLoginFixtureStore) Load() (deviceauth.Credential, error) {
	if s.loadErr != nil {
		return deviceauth.Credential{}, s.loadErr
	}
	if s.AccessToken == "" {
		return deviceauth.Credential{}, deviceauth.ErrCredentialNotFound
	}
	return s.Credential, nil
}
func (s *productLoginFixtureStore) Delete() error {
	s.deletes++
	if s.deleteErr == nil {
		s.Credential = deviceauth.Credential{}
	}
	return s.deleteErr
}

func TestProductLoginStoreFencesPersistenceAndPreservesUnreplacedCredentials(t *testing.T) {
	for _, stage := range []string{"valid", "expired before", "cancelled before", "expired after", "cancelled after", "partial save error", "save error with previous", "cleanup read error", "cleanup delete error", "already absent"} {
		t.Run(stage, func(t *testing.T) {
			now := time.Now()
			token := fixtureProductToken(t)
			credential := deviceauth.Credential{AccessToken: token.AccessToken, TokenType: "Bearer", Expiry: now.Add(time.Minute)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failure := errors.New("store failed")
			backing := &productLoginFixtureStore{write: true}
			store := productLoginStore{Store: backing, ctx: ctx, p: &productClient{now: func() time.Time { return now }}}
			switch stage {
			case "expired before":
				now = credential.Expiry
			case "cancelled before":
				cancel()
			case "expired after":
				backing.afterSave = func() { now = credential.Expiry }
			case "cancelled after":
				backing.afterSave = cancel
			case "partial save error":
				backing.saveErr = failure
			case "save error with previous":
				backing.write, backing.saveErr = false, failure
				backing.Credential = deviceauth.Credential{AccessToken: "previous"}
			case "cleanup read error":
				backing.saveErr, backing.loadErr = failure, failure
			case "cleanup delete error":
				backing.saveErr, backing.deleteErr = failure, failure
			case "already absent":
				backing.write, backing.saveErr = false, failure
			}
			err := store.Save(credential)
			if stage == "valid" {
				require.NoError(t, err)
				require.NoError(t, store.complete(credential))
				require.Equal(t, credential.Expiry, backing.Expiry)
				return
			}
			require.Error(t, err)
			if strings.Contains(stage, "before") {
				require.Zero(t, backing.saves)
			} else {
				require.Equal(t, 1, backing.saves)
			}
			if stage == "save error with previous" {
				require.Equal(t, "previous", backing.AccessToken)
				require.Zero(t, backing.deletes)
			} else if !strings.HasPrefix(stage, "cleanup") {
				require.Empty(t, backing.AccessToken)
			}
			if strings.Contains(stage, "error") || stage == "already absent" {
				require.ErrorIs(t, err, failure)
			}
		})
	}
}

func TestProductLoginCompletePreservesCleanupFailuresWithoutUsingCancelledContext(t *testing.T) {
	now := time.Now()
	token := fixtureProductToken(t)
	client, err := deviceauth.NewClient(deviceauth.ClientConfig{Issuer: "https://fixture.test" + sneatauth.ProductDelegationPath, ClientID: clientID, Scopes: productScopes})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	ctx = context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Transport: productRoundTrip(func(r *http.Request) (*http.Response, error) {
		require.NoError(t, r.Context().Err())
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(""))}, nil
	})})
	cancel()
	failure := errors.New("delete failed")
	backing := &productLoginFixtureStore{Credential: deviceauth.Credential{AccessToken: token.AccessToken}, deleteErr: failure}
	store := productLoginStore{Store: backing, ctx: ctx, p: &productClient{client: client, now: func() time.Time { return now }}}
	err = store.complete(deviceauth.Credential{AccessToken: token.AccessToken, TokenType: "Bearer", Expiry: token.Expiry})
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, err, failure)
	require.Contains(t, err.Error(), "503")
	require.Equal(t, 1, backing.deletes)
}
