package commands

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/datatug/datatug-cli/pkg/auth/device"
	"github.com/datatug/datatug-core/pkg/dataaccess"
	"github.com/sneat-co/sneat-go-core/sneatauth"
	"github.com/spf13/cobra"
)

type protectedRecordRequest struct {
	Space      string `json:"spaceID"`
	Project    string `json:"projectId"`
	Connection string `json:"connectionId"`
	Collection string `json:"collection"`
	ID         string `json:"id"`
}

type protectedReadOptions struct {
	request protectedRecordRequest
	baseURL string
	auth    device.ProductOptions
}

type protectedReadDependencies struct {
	credential func(context.Context, device.ProductOptions) (device.ProductReadCredential, error)
	client     *http.Client
	entropy    io.Reader
	request    func(context.Context, string, string, io.Reader) (*http.Request, error)
}

func newProtectedReadHTTPClient() *http.Client {
	return &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func dbGetCommand() *cobra.Command {
	return protectedReadCommand(protectedReadDependencies{credential: device.ProductCredentialForRead, client: newProtectedReadHTTPClient()})
}

func protectedReadCommand(dependencies protectedReadDependencies) *cobra.Command {
	options := protectedReadOptions{baseURL: defaultAIAPIURL}
	command := &cobra.Command{Use: "get", Short: "Read one protected record through connection authority", Long: "Read one record through the server-owned source binding and shared core authority. Uses only 'datatug auth product login'; no legacy credential fallback. Protected recordsets and object-grant execution remain unavailable in this profile.", Args: cobra.NoArgs, SilenceUsage: true, RunE: func(command *cobra.Command, _ []string) error {
		record, err := readProtectedRecord(command.Context(), options, dependencies)
		if err != nil {
			return err
		}
		defer record.cancel()
		defer record.result.Close()
		return record.result.WritePayload(record.ctx, command.OutOrStdout())
	}}
	flags := command.Flags()
	flags.StringVar(&options.request.Space, "space", "", "Space containing the protected connection")
	flags.StringVar(&options.request.Project, "project", "", "Project ID")
	flags.StringVar(&options.request.Connection, "connection", "", "Exact project catalog connection ID")
	flags.StringVar(&options.request.Collection, "collection", "", "Root collection or table")
	flags.StringVar(&options.request.ID, "id", "", "Exact record ID")
	flags.StringVar(&options.baseURL, "base-url", defaultAIAPIURL, "API /v0/ base URL (HTTPS, or loopback development)")
	flags.StringVar(&options.auth.Issuer, "auth-host", "", "Versioned HTTPS product authorization issuer")
	flags.StringVar(&options.auth.Realm, "realm", "", "Exact server credential realm")
	flags.BoolVar(&options.auth.InsecureStorage, "insecure-storage", false, "Use the plaintext product credential created with auth product login --insecure-storage")
	return command
}

func validProtectedSelector(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

type protectedRecord struct {
	result *dataaccess.Result
	ctx    context.Context
	cancel context.CancelFunc
}

func readProtectedRecord(ctx context.Context, options protectedReadOptions, dependencies protectedReadDependencies) (*protectedRecord, error) {
	if ctx == nil {
		return nil, errors.New("protected read context is required")
	}
	for _, value := range []string{options.request.Space, options.request.Project, options.request.Connection, options.request.Collection, options.request.ID} {
		if !validProtectedSelector(value) {
			return nil, Exit("protected read requires valid --space, --project, --connection, --collection and --id", exitCodeUsage)
		}
	}
	if validateAIAPIURL(options.baseURL) != nil {
		return nil, Exit("protected read requires an HTTPS API /v0/ base URL, or loopback development", exitCodeUsage)
	}
	if dependencies.credential == nil || dependencies.client == nil {
		return nil, errors.New("protected read is not configured")
	}
	credential, err := dependencies.credential(ctx, options.auth)
	if err != nil {
		return nil, err
	}
	if _, err = sneatauth.ProductDelegationDigest(credential.Bearer); err != nil {
		return nil, errors.New("protected read requires a product-only login")
	}
	if credential.Subject == "" || credential.Realm == "" || !time.Now().Before(credential.ExpiresAt) {
		return nil, errors.New("protected read requires an active product identity")
	}
	ctx, cancel := context.WithDeadline(ctx, credential.ExpiresAt)
	success := false
	defer func() {
		if !success {
			cancel()
		}
	}()
	nonceBytes := make([]byte, 16)
	entropy := dependencies.entropy
	if entropy == nil {
		entropy = rand.Reader
	}
	if _, err := io.ReadFull(entropy, nonceBytes); err != nil {
		return nil, err
	}
	nonce := hex.EncodeToString(nonceBytes)
	body, _ := json.Marshal(options.request) // A fixed string-only wire DTO cannot fail JSON encoding.
	newRequest := dependencies.request
	if newRequest == nil {
		newRequest = http.NewRequestWithContext
	}
	req, err := newRequest(ctx, http.MethodPost, options.baseURL+"datatug/access/read", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+credential.Bearer)
	req.Header.Set("X-DataTug-Result-Nonce", nonce)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	started := time.Now()
	response, err := dependencies.client.Do(req)
	if err != nil {
		return nil, errors.New("protected read service unavailable")
	}
	closed := false
	defer func() {
		if !closed {
			_ = response.Body.Close()
		}
	}()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusUnauthorized {
			return nil, Exit("protected read denied", exitCodeAccessDenied)
		}
		return nil, Exit("protected read unavailable or unsupported; no record returned", exitCodeDatabase)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	defer clear(raw)
	if err != nil || len(raw) > 1<<20 {
		return nil, errors.New("invalid protected record response")
	}
	envelope, err := dataaccess.DecodeResultEnvelope(raw)
	clear(raw)
	if err != nil {
		return nil, errors.New("invalid protected record response")
	}
	defer clear(envelope.Payload)
	if !validProtectedPayload(envelope.Payload) {
		return nil, errors.New("invalid protected record response")
	}
	subject := dataaccess.Subject{Realm: credential.Realm, Kind: dataaccess.User, ID: credential.Subject}
	if envelope.Lease.Connection.Space != options.request.Space {
		return nil, errors.New("protected read binding mismatch")
	}
	// The authenticated host owns actual-source identity. Pin that initial
	// identity; subsequent resolution must match it inside shared Core.
	authority, err := dataaccess.NewAuthority(subject, envelope.Lease.Connection, func(ctx context.Context, _ dataaccess.ResolveRequest) (dataaccess.Resolution, error) {
		return resolveProtectedRecord(ctx, options, dependencies.client, credential.Bearer)
	})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(body)
	result, err := authority.ImportResult(ctx, dataaccess.ResultRequest{Nonce: nonce, RequestSHA256: hex.EncodeToString(digest[:])}, started, envelope)
	if err != nil {
		return nil, err
	}
	// The Core-owned buffer has expiry/cancellation watchers before Close.
	// An uncooperative body closer cannot postpone purging retained values.
	err = response.Body.Close()
	closed = true
	if err != nil {
		result.Close()
		return nil, errors.New("invalid protected record response")
	}
	if err = ctx.Err(); err != nil {
		result.Close()
		return nil, err
	}
	success = true
	return &protectedRecord{result: result, ctx: ctx, cancel: cancel}, nil
}

func resolveProtectedRecord(ctx context.Context, options protectedReadOptions, client *http.Client, token string) (dataaccess.Resolution, error) {
	var result dataaccess.Resolution
	body, _ := json.Marshal(struct {
		Space      string `json:"spaceID"`
		Project    string `json:"projectId"`
		Connection string `json:"connectionId"`
	}{options.request.Space, options.request.Project, options.request.Connection})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, options.baseURL+"datatug/access/resolve", bytes.NewReader(body))
	if err != nil {
		return result, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return result, errors.New("protected read authority unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return result, errors.New("protected read authority unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return result, errors.New("invalid protected authority response")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF {
		return dataaccess.Resolution{}, errors.New("invalid protected authority response")
	}
	return result, nil
}

// Core already validates the complete JSON document. Check the closed record
// wrapper without retaining decoded values across fresh authorization I/O.
func validProtectedPayload(data []byte) bool {
	var wrapper map[string]json.RawMessage
	defer func() {
		for _, raw := range wrapper {
			clear(raw)
		}
	}()
	if json.Unmarshal(data, &wrapper) != nil || len(wrapper) != 1 {
		return false
	}
	record := bytes.TrimSpace(wrapper["record"])
	return len(record) > 0 && record[0] == '{'
}
