package device

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-co/sneat-go-core/sneatauth"
	"github.com/spf13/cobra"
	"github.com/strongo/deviceauth"
	"golang.org/x/oauth2"
)

// ProductOptions selects a separate credential family. It never loads the
// legacy Firebase account, exchanges a custom token, or refreshes a session.
type ProductOptions struct {
	Issuer, Realm   string
	InsecureStorage bool
}

var productOpenBrowser = deviceauth.OpenBrowser

var productScopes = []string{"openid", "profile", "datatug:projects:read"}

var productHTTPClient = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

type productClient struct {
	client             *deviceauth.Client
	store              deviceauth.Store
	realm, description string
	http               *http.Client
	now                func() time.Time
}

// The shared helper performs its own userinfo and replacement-revocation IO.
// Fence its persistence and final success against this credential's original
// expiry, without changing the helper's token cleanup or legacy stores.
type productLoginStore struct {
	deviceauth.Store
	ctx context.Context
	p   *productClient
}

func (s productLoginStore) check(credential deviceauth.Credential) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	return s.p.validToken(&oauth2.Token{AccessToken: credential.AccessToken, TokenType: credential.TokenType, RefreshToken: credential.RefreshToken, Expiry: credential.Expiry}, true)
}

func (s productLoginStore) discard(credential deviceauth.Credential) error {
	current, err := s.Load()
	if errors.Is(err, deviceauth.ErrCredentialNotFound) {
		return nil
	}
	if err != nil || current.AccessToken != credential.AccessToken {
		return err
	}
	return s.Delete()
}

func (s productLoginStore) Save(credential deviceauth.Credential) error {
	if err := s.check(credential); err != nil {
		return err
	}
	if err := s.Store.Save(credential); err != nil {
		return errors.Join(err, s.discard(credential))
	}
	if err := s.check(credential); err != nil {
		return errors.Join(err, s.discard(credential))
	}
	return nil
}

func (s productLoginStore) complete(credential deviceauth.Credential) error {
	if err := s.check(credential); err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 5*time.Second)
		defer cancel()
		return errors.Join(err, s.discard(credential), s.p.client.Revoke(cleanup, credential.AccessToken))
	}
	return nil
}

func newProductClient(options ProductOptions) (*productClient, error) {
	if options.Issuer == "" {
		options.Issuer = os.Getenv("DATATUG_PRODUCT_ISSUER")
	}
	if options.Issuer == "" {
		options.Issuer = "https://auth.sneat.co" + sneatauth.ProductDelegationPath
	}
	if options.Realm == "" {
		options.Realm = os.Getenv("DATATUG_PRODUCT_REALM")
	}
	if sneatauth.ValidateProductDelegationRealm(options.Issuer, options.Realm, "") != nil {
		return nil, errors.New("product login requires a canonical HTTPS versioned issuer and an explicit --realm")
	}
	client, err := deviceauth.NewClient(deviceauth.ClientConfig{Issuer: options.Issuer, ClientID: clientID, Scopes: productScopes, RequiredScopes: productScopes, KeyringService: "datatug-cli", KeyringAccount: string(sneatauth.ProductDelegationFamily) + ":" + options.Realm})
	if err != nil {
		return nil, err
	}
	p := &productClient{client: client, realm: options.Realm, http: productHTTPClient, now: time.Now, description: "operating system keyring (product-only)"}
	if !options.InsecureStorage {
		p.store, err = client.NewKeyringStore()
		return p, err
	}
	dir, err := userConfigDir()
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(options.Issuer + "\x00" + clientID + "\x00" + options.Realm + "\x00" + string(sneatauth.ProductDelegationFamily)))
	p.description = filepath.Join(dir, "datatug", "auth", string(sneatauth.ProductDelegationFamily), hex.EncodeToString(digest[:])+".json")
	p.store, err = deviceauth.NewFileStore(p.description)
	return p, err
}

func (p *productClient) validToken(token *oauth2.Token, active bool) error {
	if token == nil || !strings.EqualFold(token.TokenType, "Bearer") || token.RefreshToken != "" {
		return errors.New("invalid product-only credential; sign in again")
	}
	if _, err := sneatauth.ProductDelegationDigest(token.AccessToken); err != nil {
		return errors.New("invalid product-only credential family; sign in again")
	}
	if active && (token.Expiry.IsZero() || !p.now().Before(token.Expiry) || token.Expiry.After(p.now().Add(sneatauth.MaxProductDelegationLifetime))) {
		return errors.New("product login expired; run 'datatug auth product login'")
	}
	return nil
}

type productIdentity struct {
	Family   sneatauth.CredentialFamily `json:"credential_family"`
	Realm    string                     `json:"firebase_project"`
	Tenant   string                     `json:"firebase_tenant"`
	Subject  string                     `json:"sub"`
	Audience string                     `json:"aud"`
	Client   string                     `json:"client_id"`
	Product  string                     `json:"product"`
	Scope    string                     `json:"scope"`
	Scopes   []string                   `json:"scopes"`
}

func (p *productClient) identity(ctx context.Context, token *oauth2.Token) (productIdentity, error) {
	if err := p.validToken(token, true); err != nil {
		return productIdentity{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.client.Issuer()+"/oauth/userinfo", nil)
	if err != nil {
		return productIdentity{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Accept", "application/json")
	response, err := p.http.Do(req)
	if err != nil {
		return productIdentity{}, errors.New("product identity service unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return productIdentity{}, errors.New("product login is not active; sign in again")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return productIdentity{}, errors.New("invalid product identity response")
	}
	var identity productIdentity
	if json.Unmarshal(raw, &identity) != nil || identity.Family != sneatauth.ProductDelegationFamily || identity.Realm != p.realm || identity.Tenant != "" || identity.Subject == "" ||
		identity.Client != clientID || identity.Audience != clientID || identity.Product != "datatug" || strings.Join(identity.Scopes, " ") != identity.Scope || deviceauth.ValidateRequiredScopes(identity.Scopes, productScopes) != nil {
		return productIdentity{}, errors.New("product identity binding does not match this login")
	}
	if err = ctx.Err(); err != nil {
		return productIdentity{}, err
	}
	if err = p.validToken(token, true); err != nil {
		return productIdentity{}, err
	}
	return identity, nil
}

func (p *productClient) token(ctx context.Context) (*oauth2.Token, productIdentity, error) {
	credential, err := p.client.ScopedStore(p.store).Load()
	if err != nil {
		return nil, productIdentity{}, err
	}
	token := &oauth2.Token{AccessToken: credential.AccessToken, TokenType: credential.TokenType, RefreshToken: credential.RefreshToken, Expiry: credential.Expiry}
	identity, err := p.identity(ctx, token)
	if err != nil {
		return nil, productIdentity{}, err
	}
	if credential.AccountID != identity.Subject {
		return nil, productIdentity{}, errors.New("product login subject changed; sign in again")
	}
	return token, identity, nil
}

// ProductBearer validates current product state for each request. No cached
// credential or successful earlier call can extend the original hard expiry.
func ProductBearer(ctx context.Context, options ProductOptions) (string, error) {
	p, err := newProductClient(options)
	if err != nil {
		return "", err
	}
	token, _, err := p.token(ctx)
	if err != nil {
		return "", err
	}
	return token.AccessToken, nil
}

// ProductCommand is additive: the existing auth login/status/logout retain
// their legacy behavior until the coordinated credential-family cutover.
func ProductCommand() *cobra.Command {
	var options ProductOptions
	command := &cobra.Command{Use: "product", Short: "Manage a product-only DataTug login"}
	command.PersistentFlags().StringVar(&options.Issuer, "auth-host", "", "Versioned HTTPS product authorization issuer (or DATATUG_PRODUCT_ISSUER)")
	command.PersistentFlags().StringVar(&options.Realm, "realm", "", "Exact server credential realm (or DATATUG_PRODUCT_REALM)")
	command.PersistentFlags().BoolVar(&options.InsecureStorage, "insecure-storage", false, "Store only this product credential in a plaintext 0600 file")
	command.AddCommand(&cobra.Command{Use: "login", Short: "Sign in without creating a Firebase account session", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		p, err := newProductClient(options)
		if err != nil {
			return err
		}
		ctx := context.WithValue(command.Context(), oauth2.HTTPClient, p.http)
		store := productLoginStore{Store: p.store, ctx: ctx, p: p}
		auth, err := p.client.DeviceLoginAndStore(ctx, deviceauth.DeviceLoginOptions{DeviceInfo: deviceInfo(), OpenBrowser: productOpenBrowser, Output: command.OutOrStdout(), ErrorOutput: command.ErrOrStderr(), TokenTransformer: func(ctx context.Context, token *oauth2.Token) (*oauth2.Token, error) {
			if token == nil || token.Extra("credential_family") != string(sneatauth.ProductDelegationFamily) {
				return nil, errors.New("authorization server returned a different credential family")
			}
			if _, err := p.identity(ctx, token); err != nil {
				return nil, err
			}
			return token, nil
		}}, store)
		if err != nil {
			return err
		}
		printWarnings(command.ErrOrStderr(), auth.Warnings)
		if err = store.complete(auth.Credential); err != nil {
			return err
		}
		_, err = fmt.Fprintf(command.OutOrStdout(), "Product login active as %s.\nToken storage: %s\n", auth.Identity.Subject, p.description)
		return err
	}})
	command.AddCommand(&cobra.Command{Use: "status", Short: "Check current product login", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		p, err := newProductClient(options)
		if err != nil {
			return err
		}
		token, identity, err := p.token(command.Context())
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(command.OutOrStdout(), "Product login active as %s until %s.\n", identity.Subject, token.Expiry.UTC().Format(time.RFC3339))
		return err
	}})
	command.AddCommand(&cobra.Command{Use: "logout", Short: "Revoke this product login", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		p, err := newProductClient(options)
		if err != nil {
			return err
		}
		credential, err := p.client.ScopedStore(p.store).Load()
		if errors.Is(err, deviceauth.ErrCredentialNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err = p.validToken(&oauth2.Token{AccessToken: credential.AccessToken, TokenType: credential.TokenType, RefreshToken: credential.RefreshToken}, false); err != nil {
			return err
		}
		if err = p.client.Logout(context.WithValue(command.Context(), oauth2.HTTPClient, p.http), p.store); err != nil {
			return err
		}
		_, err = fmt.Fprintln(command.OutOrStdout(), "Product login revoked.")
		return err
	}})
	return command
}
