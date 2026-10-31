package device

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-co/sneat-go-core/sneatauth"
	"github.com/stretchr/testify/require"
	"github.com/strongo/deviceauth"
	"golang.org/x/oauth2"
)

type productRoundTrip func(*http.Request) (*http.Response, error)

func (f productRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type productReadError struct{}

func (productReadError) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (productReadError) Close() error             { return nil }

func fixtureProductToken(t *testing.T) *oauth2.Token {
	t.Helper()
	bearer, _, err := sneatauth.NewProductDelegationBearer(bytes.NewReader(make([]byte, 32)))
	require.NoError(t, err)
	return (&oauth2.Token{AccessToken: bearer, TokenType: "Bearer", Expiry: time.Now().Add(30 * time.Minute)}).WithExtra(map[string]any{"credential_family": string(sneatauth.ProductDelegationFamily)})
}
func fixtureProductIdentity() productIdentity {
	return productIdentity{Family: sneatauth.ProductDelegationFamily, Realm: "demo-datatug-access", Subject: "uid", Client: clientID, Audience: clientID, Product: "datatug", Scopes: append([]string(nil), productScopes...), Scope: strings.Join(productScopes, " ")}
}

func TestProductOriginalExpiryAndCancellationRemainBoundAcrossIdentityIO(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancelled=%t", cancelled), func(t *testing.T) {
			now := time.Now()
			token := fixtureProductToken(t)
			token.Expiry = now.Add(time.Second)
			client, err := deviceauth.NewClient(deviceauth.ClientConfig{Issuer: "https://fixture.test" + sneatauth.ProductDelegationPath, ClientID: clientID, Scopes: productScopes, RequiredScopes: productScopes})
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p := &productClient{client: client, realm: "demo-datatug-access", now: func() time.Time { return now }, store: &memoryStore{credential: deviceauth.Credential{AccessToken: token.AccessToken, TokenType: "Bearer", Expiry: token.Expiry, Issuer: client.Issuer(), ClientID: clientID, AccountID: "uid"}}}
			p.http = &http.Client{Transport: productRoundTrip(func(*http.Request) (*http.Response, error) {
				if cancelled {
					cancel()
				} else {
					now = token.Expiry
				}
				raw, e := json.Marshal(fixtureProductIdentity())
				require.NoError(t, e)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(raw))}, nil
			})}
			got, identity, err := p.token(ctx)
			require.Error(t, err)
			require.Nil(t, got, "no credential may be returned active after its original deadline")
			require.Empty(t, identity.Subject)
		})
	}
}

func TestProductOnlyLoginStatusLogoutAndCurrentState(t *testing.T) {
	token := fixtureProductToken(t)
	identity := fixtureProductIdentity()
	revoked := false
	redirectIdentity := false
	revokeStatus := 200
	family := string(sneatauth.ProductDelegationFamily)
	identityCalls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case sneatauth.ProductDelegationPath + "/oauth/device/code":
			_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "device", "user_code": "ABCD-EFGH", "verification_uri": "https://fixture.test/device", "expires_in": 120, "interval": 1})
		case sneatauth.ProductDelegationPath + "/oauth/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"credential_family": family, "access_token": token.AccessToken, "token_type": "Bearer", "expires_in": 1800, "scope": identity.Scope})
		case sneatauth.ProductDelegationPath + "/oauth/userinfo":
			identityCalls++
			if redirectIdentity {
				w.Header().Set("Location", "/must-not-follow")
				w.WriteHeader(http.StatusTemporaryRedirect)
				return
			}
			require.Equal(t, "Bearer "+token.AccessToken, r.Header.Get("Authorization"))
			if revoked {
				w.WriteHeader(401)
				return
			}
			_ = json.NewEncoder(w).Encode(identity)
		case sneatauth.ProductDelegationPath + "/oauth/revoke":
			if revokeStatus == 200 {
				revoked = true
			}
			w.WriteHeader(revokeStatus)
		default:
			t.Errorf("unexpected request (no Firebase exchange/refresh permitted): %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	oldDir, oldHTTP, oldBrowser := userConfigDir, productHTTPClient, productOpenBrowser
	t.Cleanup(func() { userConfigDir = oldDir; productHTTPClient = oldHTTP; productOpenBrowser = oldBrowser })
	userConfigDir = func() (string, error) { return dir, nil }
	productHTTPClient = server.Client()
	productHTTPClient.CheckRedirect = oldHTTP.CheckRedirect
	productOpenBrowser = func(string) error { return nil }
	options := ProductOptions{Issuer: server.URL + sneatauth.ProductDelegationPath, Realm: identity.Realm, InsecureStorage: true}
	run := func(verb string) error {
		cmd := ProductCommand()
		var output bytes.Buffer
		cmd.SetOut(&output)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{verb, "--auth-host", options.Issuer, "--realm", options.Realm, "--insecure-storage"})
		err := cmd.Execute()
		require.NotContains(t, output.String(), token.AccessToken)
		return err
	}
	require.NoError(t, run("login"))
	require.NoError(t, run("status"))
	p, err := newProductClient(options)
	require.NoError(t, err)
	credential, err := p.client.ScopedStore(p.store).Load()
	require.NoError(t, err)
	require.Empty(t, credential.RefreshToken)
	require.Equal(t, options.Issuer, credential.Issuer)
	require.Equal(t, clientID, credential.ClientID)
	info, err := os.Stat(p.description)
	require.NoError(t, err)
	require.EqualValues(t, 0600, info.Mode().Perm())
	before := identityCalls
	bearer, err := ProductBearer(context.Background(), options)
	require.NoError(t, err)
	require.Equal(t, token.AccessToken, bearer)
	require.Greater(t, identityCalls, before)
	readCredential, err := ProductCredentialForRead(context.Background(), options)
	require.NoError(t, err)
	require.Equal(t, credential.Expiry, readCredential.ExpiresAt)
	require.Equal(t, identity.Subject, readCredential.Subject)
	require.Equal(t, identity.Realm, readCredential.Realm)
	require.Equal(t, token.AccessToken, readCredential.Bearer)
	redirectIdentity = true
	_, err = ProductBearer(context.Background(), options)
	require.Error(t, err, "product bearer must never follow an identity redirect")
	redirectIdentity = false
	after, err := p.client.ScopedStore(p.store).Load()
	require.NoError(t, err)
	require.Equal(t, credential.Expiry, after.Expiry)
	revoked = true
	_, err = ProductBearer(context.Background(), options)
	require.Error(t, err)
	revoked = false
	credential.Expiry = time.Now().Add(-time.Second)
	require.NoError(t, p.client.ScopedStore(p.store).Save(credential))
	before = identityCalls
	require.Error(t, run("status"))
	require.Equal(t, before, identityCalls, "expired credentials are never refreshed")
	require.NoError(t, run("logout"))
	require.NoError(t, run("logout"))
	_, err = ProductBearer(context.Background(), options)
	require.ErrorIs(t, err, deviceauth.ErrCredentialNotFound)
	family = "firebase-session"
	revoked = false
	require.Error(t, run("login"))
	_, err = p.store.Load()
	require.ErrorIs(t, err, deviceauth.ErrCredentialNotFound)
	family = string(sneatauth.ProductDelegationFamily)
	revoked = true
	require.Error(t, run("login"), "an inactive identity must not be stored")
	require.NoError(t, os.WriteFile(p.description, []byte("{"), 0600))
	require.Error(t, run("logout"))
	credential.AccessToken = "firebase-id"
	require.NoError(t, p.client.ScopedStore(p.store).Save(credential))
	require.Error(t, run("logout"), "a legacy credential must not reach product revocation")
	credential.AccessToken = token.AccessToken
	require.NoError(t, p.client.ScopedStore(p.store).Save(credential))
	revokeStatus = 503
	require.Error(t, run("logout"))
	_, err = p.store.Load()
	require.NoError(t, err, "failed revocation retains local state for retry")
	revokeStatus = 200
	require.NoError(t, run("logout"))
}

func TestProductIdentityBindingsAndInvalidCredentialsFailClosed(t *testing.T) {
	token := fixtureProductToken(t)
	client, err := deviceauth.NewClient(deviceauth.ClientConfig{Issuer: "https://fixture.test" + sneatauth.ProductDelegationPath, ClientID: clientID, Scopes: productScopes, RequiredScopes: productScopes})
	require.NoError(t, err)
	p := &productClient{client: client, realm: "demo-datatug-access", now: time.Now}
	identity := fixtureProductIdentity()
	calls := 0
	p.http = &http.Client{Transport: productRoundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		raw, _ := json.Marshal(identity)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(raw))}, nil
	})}
	_, err = p.identity(context.Background(), token)
	require.NoError(t, err)
	for name, mutate := range map[string]func(*productIdentity){"family": func(i *productIdentity) { i.Family = "firebase-session" }, "realm": func(i *productIdentity) { i.Realm = "other" }, "tenant": func(i *productIdentity) { i.Tenant = "tenant" }, "subject": func(i *productIdentity) { i.Subject = "" }, "client": func(i *productIdentity) { i.Client = "sneat-cli" }, "audience": func(i *productIdentity) { i.Audience = "sneat-cli" }, "product": func(i *productIdentity) { i.Product = "sneat" }, "scope": func(i *productIdentity) { i.Scopes = []string{"openid"}; i.Scope = "openid" }, "scope text": func(i *productIdentity) { i.Scope = "other" }} {
		t.Run(name, func(t *testing.T) {
			identity = fixtureProductIdentity()
			mutate(&identity)
			_, err = p.identity(context.Background(), token)
			require.Error(t, err)
		})
	}
	identity = fixtureProductIdentity()
	for _, bad := range []*oauth2.Token{nil, {AccessToken: "firebase-id", TokenType: "Bearer", Expiry: token.Expiry}, {AccessToken: token.AccessToken, TokenType: "Other", Expiry: token.Expiry}, {AccessToken: token.AccessToken, TokenType: "Bearer", RefreshToken: "firebase-refresh", Expiry: token.Expiry}, {AccessToken: token.AccessToken, TokenType: "Bearer"}, {AccessToken: token.AccessToken, TokenType: "Bearer", Expiry: time.Now().Add(-time.Second)}, {AccessToken: token.AccessToken, TokenType: "Bearer", Expiry: time.Now().Add(2 * time.Hour)}} {
		before := calls
		_, err = p.identity(context.Background(), bad)
		require.Error(t, err)
		require.Equal(t, before, calls)
	}
	var invalidContext context.Context
	_, err = p.identity(invalidContext, token)
	require.Error(t, err)
	for _, body := range []io.ReadCloser{productReadError{}, io.NopCloser(strings.NewReader(strings.Repeat("x", (64<<10)+1))), io.NopCloser(strings.NewReader("{"))} {
		p.http.Transport = productRoundTrip(func(*http.Request) (*http.Response, error) { return &http.Response{StatusCode: 200, Body: body}, nil })
		_, err = p.identity(context.Background(), token)
		require.Error(t, err)
	}
	p.http.Transport = productRoundTrip(func(*http.Request) (*http.Response, error) { return nil, errors.New("private transport failure") })
	_, err = p.identity(context.Background(), token)
	require.Error(t, err)
	p.http.Transport = productRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	_, err = p.identity(context.Background(), token)
	require.Error(t, err)
	store := &memoryStore{credential: deviceauth.Credential{AccessToken: token.AccessToken, TokenType: "Bearer", Expiry: token.Expiry, Issuer: client.Issuer(), ClientID: clientID, AccountID: "other"}}
	p.store = store
	p.http.Transport = productRoundTrip(func(*http.Request) (*http.Response, error) {
		raw, _ := json.Marshal(identity)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(raw))}, nil
	})
	_, _, err = p.token(context.Background())
	require.Error(t, err)
}

func TestProductStorageNamespacesAndConfiguration(t *testing.T) {
	oldDir, oldScopes := userConfigDir, productScopes
	t.Cleanup(func() { userConfigDir = oldDir; productScopes = oldScopes })
	dir := t.TempDir()
	userConfigDir = func() (string, error) { return dir, nil }
	t.Setenv("DATATUG_PRODUCT_ISSUER", "")
	t.Setenv("DATATUG_PRODUCT_REALM", "demo-datatug-access")
	p, err := newProductClient(ProductOptions{InsecureStorage: true})
	require.NoError(t, err)
	require.Contains(t, p.description, string(sneatauth.ProductDelegationFamily))
	require.NotContains(t, filepath.Base(p.description), "firebase-session")
	for _, options := range []ProductOptions{{Issuer: "https://other.test" + sneatauth.ProductDelegationPath, Realm: "demo-datatug-access", InsecureStorage: true}, {Issuer: p.client.Issuer(), Realm: "other", InsecureStorage: true}} {
		other, err := newProductClient(options)
		require.NoError(t, err)
		require.NotEqual(t, p.description, other.description)
	}
	_, err = newProductClient(ProductOptions{Realm: "demo-datatug-access"})
	require.NoError(t, err, "constructing keyring config performs no keyring read/write")
	for _, options := range []ProductOptions{{Issuer: "https://fixture.test", Realm: "demo"}, {Issuer: "http://127.0.0.1/product-delegation/v1", Realm: "demo"}, {Issuer: "https://fixture.test/product-delegation/v1", Realm: "bad/realm"}} {
		_, err = newProductClient(options)
		require.Error(t, err)
	}
	t.Setenv("DATATUG_PRODUCT_REALM", "")
	_, err = newProductClient(ProductOptions{})
	require.Error(t, err)
	_, err = ProductBearer(context.Background(), ProductOptions{})
	require.Error(t, err)
	for _, verb := range []string{"login", "status", "logout"} {
		cmd := ProductCommand()
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{verb})
		require.Error(t, cmd.Execute())
	}
	userConfigDir = func() (string, error) { return "", errors.New("config failed") }
	_, err = newProductClient(ProductOptions{Realm: "demo", InsecureStorage: true})
	require.Error(t, err)
	userConfigDir = oldDir
	productScopes = []string{""}
	_, err = newProductClient(ProductOptions{Realm: "demo"})
	require.Error(t, err)
}
