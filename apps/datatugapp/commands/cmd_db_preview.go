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

// The complete fixed DTO is hashed independently on both sides of the transport.
// Neither SQL, a caller-selected source nor a caller-selected subject is accepted.
type protectedTableRequest struct {
	Space      string               `json:"spaceID"`
	Project    string               `json:"projectId"`
	Connection string               `json:"connectionId"`
	Operation  dataaccess.TableRead `json:"operation"`
}

type protectedTableOptions struct {
	request protectedTableRequest
	baseURL string
	agent   bool
	agentID string
	auth    device.ProductOptions
}

func dbPreviewCommand() *cobra.Command {
	return protectedTableCommand(protectedReadDependencies{credential: device.ProductCredentialForRead, client: newProtectedReadHTTPClient()})
}

func protectedTableCommand(dependencies protectedReadDependencies) *cobra.Command {
	options := protectedTableOptions{baseURL: defaultAIAPIURL}
	command := &cobra.Command{Use: "preview", Short: "Preview one protected table through shared Core authority", Long: "Return a bounded JSON table preview, including returnedRows, hasMore and complete. No ordering or pagination is promised. Namespace and table names accept 1..128 ASCII letters, digits, underscores or hyphens; native profiles may refuse further shapes. Requires product-only login and an exact namespace/table; --agent uses only its baseline and never human grants. Unsupported servers and profiles fail closed.", Args: cobra.NoArgs, SilenceUsage: true, RunE: func(command *cobra.Command, _ []string) error {
		result, err := readProtectedTable(command.Context(), options, dependencies)
		if err != nil {
			return err
		}
		defer result.cancel()
		defer result.result.Close()
		return result.result.WritePayload(result.ctx, command.OutOrStdout())
	}}
	flags := command.Flags()
	flags.StringVar(&options.request.Space, "space", "", "Space containing the protected connection")
	flags.StringVar(&options.request.Project, "project", "", "Project ID")
	flags.StringVar(&options.request.Connection, "connection", "", "Exact project catalog connection ID")
	flags.StringVar(&options.request.Operation.Object.Namespace, "namespace", "", "Exact explicit table namespace")
	flags.StringVar(&options.request.Operation.Object.Name, "table", "", "Exact table name")
	flags.Uint32Var(&options.request.Operation.MaxRows, "max-rows", 100, "Preview row limit, 1..100")
	flags.Uint32Var(&options.request.Operation.MaxBytes, "max-bytes", 262144, "Protected JSON byte limit, 1..262144")
	flags.StringVar(&options.baseURL, "base-url", defaultAIAPIURL, "API /v0/ base URL (HTTPS, or loopback development)")
	flags.StringVar(&options.auth.Issuer, "auth-host", "", "Versioned HTTPS product authorization issuer")
	flags.StringVar(&options.auth.Realm, "realm", "", "Exact server credential realm")
	flags.BoolVar(&options.agent, "agent", false, "Read as your agent: its own approvals, never beyond your access; unsupported servers fail closed")
	flags.StringVar(&options.agentID, "agent-id", "", "With --agent: the registered agent on its own account (registered in the DataTug web app; IDs start with ag-)")
	flags.BoolVar(&options.auth.InsecureStorage, "insecure-storage", false, "Use the plaintext product credential created with auth product login --insecure-storage")
	return command
}

func readProtectedTable(ctx context.Context, options protectedTableOptions, dependencies protectedReadDependencies) (*protectedRecord, error) {
	if ctx == nil {
		return nil, errors.New("protected preview context is required")
	}
	for _, value := range []string{options.request.Space, options.request.Project, options.request.Connection, options.request.Operation.Object.Namespace, options.request.Operation.Object.Name} {
		if !validProtectedSelector(value) {
			return nil, Exit("protected preview requires valid --space, --project, --connection, --namespace and --table", exitCodeUsage)
		}
	}
	if !validProtectedAgent(options.agent, options.agentID) {
		return nil, Exit("--agent-id requires --agent and a valid agent ID", exitCodeUsage)
	}
	operation := options.request.Operation
	if operation.MaxRows < 1 || operation.MaxRows > 100 || operation.MaxBytes < 1 || operation.MaxBytes > 262144 {
		return nil, Exit("protected preview requires --max-rows 1..100 and --max-bytes 1..262144", exitCodeUsage)
	}
	if validateAIAPIURL(options.baseURL) != nil {
		return nil, Exit("protected preview requires an HTTPS API /v0/ base URL, or loopback development", exitCodeUsage)
	}
	if dependencies.credential == nil || dependencies.client == nil {
		return nil, errors.New("protected preview is not configured")
	}
	credential, err := dependencies.credential(ctx, options.auth)
	if err != nil {
		return nil, err
	}
	if _, err = sneatauth.ProductDelegationDigest(credential.Bearer); err != nil {
		return nil, errors.New("protected preview requires a product-only login")
	}
	if credential.Subject == "" || credential.Realm == "" || !time.Now().Before(credential.ExpiresAt) {
		return nil, errors.New("protected preview requires an active product identity")
	}
	subject := dataaccess.Subject{Realm: credential.Realm, Kind: dataaccess.User, ID: credential.Subject}
	if options.agent {
		subject.Kind = dataaccess.Agent
		if options.agentID != "" {
			subject.ID = options.agentID
		}
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
	body, _ := json.Marshal(options.request) // Fixed string/uint32 DTO cannot invoke custom encoding.
	newRequest := dependencies.request
	if newRequest == nil {
		newRequest = http.NewRequestWithContext
	}
	req, err := newRequest(ctx, http.MethodPost, options.baseURL+protectedAccessPrefix(options.agent)+"table/read", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+credential.Bearer)
	setProtectedAgentHeader(req, options.agentID)
	req.Header.Set("X-DataTug-Result-Nonce", nonce)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	started := time.Now()
	response, err := dependencies.client.Do(req)
	if err != nil {
		return nil, errors.New("protected preview service unavailable")
	}
	closed := false
	defer func() {
		if !closed {
			_ = response.Body.Close()
		}
	}()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusUnauthorized {
			return nil, Exit("protected preview denied", exitCodeAccessDenied)
		}
		return nil, Exit("protected preview unavailable or unsupported; no table returned", exitCodeDatabase)
	}
	// Core's closed v2 decoder admits no envelope larger than this bound.
	raw, err := io.ReadAll(io.LimitReader(response.Body, 294913))
	defer clear(raw)
	if err != nil || len(raw) > 294912 {
		return nil, errors.New("invalid protected table response")
	}
	envelope, err := dataaccess.DecodeTableResultEnvelope(raw)
	clear(raw)
	if err != nil {
		return nil, errors.New("invalid protected table response")
	}
	defer clear(envelope.Payload)
	if envelope.Lease.Connection.Space != options.request.Space {
		return nil, errors.New("protected preview binding mismatch")
	}
	// The initial authenticated server source is pinned; fresh resolution must
	// match it. The expected operation always comes from our original local DTO.
	resolveOptions := protectedReadOptions{request: protectedRecordRequest{Space: options.request.Space, Project: options.request.Project, Connection: options.request.Connection}, baseURL: options.baseURL, agent: options.agent, agentID: options.agentID, person: dataaccess.Subject{Realm: credential.Realm, Kind: dataaccess.User, ID: credential.Subject}}
	authority, err := dataaccess.NewAuthority(subject, envelope.Lease.Connection, func(ctx context.Context, _ dataaccess.ResolveRequest) (dataaccess.Resolution, error) {
		return resolveProtectedRecord(ctx, resolveOptions, dependencies.client, credential.Bearer)
	})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(body)
	result, err := authority.ImportTableResult(ctx, dataaccess.ResultRequest{Nonce: nonce, RequestSHA256: hex.EncodeToString(digest[:])}, started, operation, envelope)
	if err != nil {
		return nil, err
	}
	// Import arms Core purge before an external body closer can block.
	err = response.Body.Close()
	closed = true
	if err != nil {
		result.Close()
		return nil, errors.New("invalid protected table response")
	}
	if err = ctx.Err(); err != nil {
		result.Close()
		return nil, err
	}
	success = true
	return &protectedRecord{result: result, ctx: ctx, cancel: cancel}, nil
}
