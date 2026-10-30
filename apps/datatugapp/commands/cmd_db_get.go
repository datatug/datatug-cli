package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/datatug/datatug-cli/pkg/auth/device"
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
	bearer func(context.Context, device.ProductOptions) (string, error)
	client *http.Client
}

func newProtectedReadHTTPClient() *http.Client {
	return &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func dbGetCommand() *cobra.Command {
	return protectedReadCommand(protectedReadDependencies{bearer: device.ProductBearer, client: newProtectedReadHTTPClient()})
}

func protectedReadCommand(dependencies protectedReadDependencies) *cobra.Command {
	options := protectedReadOptions{baseURL: defaultAIAPIURL}
	command := &cobra.Command{Use: "get", Short: "Read one protected record through connection authority", Long: "Read one record through the server-owned source binding and shared core authority. Uses only 'datatug auth product login'; no legacy credential fallback. Protected recordsets and object-grant execution remain unavailable in this profile.", Args: cobra.NoArgs, SilenceUsage: true, RunE: func(command *cobra.Command, _ []string) error {
		record, err := readProtectedRecord(command.Context(), options, dependencies)
		if err != nil {
			return err
		}
		return json.NewEncoder(command.OutOrStdout()).Encode(struct {
			Record map[string]any `json:"record"`
		}{record})
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
	return value != "" && len(value) <= 256 && value != "." && value != ".." && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "/\\?#") && !strings.ContainsFunc(value, unicode.IsControl)
}

func readProtectedRecord(ctx context.Context, options protectedReadOptions, dependencies protectedReadDependencies) (map[string]any, error) {
	for _, value := range []string{options.request.Space, options.request.Project, options.request.Connection, options.request.Collection, options.request.ID} {
		if !validProtectedSelector(value) {
			return nil, Exit("protected read requires valid --space, --project, --connection, --collection and --id", exitCodeUsage)
		}
	}
	if validateAIAPIURL(options.baseURL) != nil {
		return nil, Exit("protected read requires an HTTPS API /v0/ base URL, or loopback development", exitCodeUsage)
	}
	if dependencies.bearer == nil || dependencies.client == nil {
		return nil, errors.New("protected read is not configured")
	}
	token, err := dependencies.bearer(ctx, options.auth)
	if err != nil {
		return nil, err
	}
	if _, err = sneatauth.ProductDelegationDigest(token); err != nil {
		return nil, errors.New("protected read requires a product-only login")
	}
	body, _ := json.Marshal(options.request) // A fixed string-only wire DTO cannot fail JSON encoding.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, options.baseURL+"datatug/access/read", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	response, err := dependencies.client.Do(req)
	if err != nil {
		return nil, errors.New("protected read service unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusUnauthorized {
			return nil, Exit("protected read denied", exitCodeAccessDenied)
		}
		return nil, Exit("protected read unavailable or unsupported; no record returned", exitCodeDatabase)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, errors.New("invalid protected record response")
	}
	var out struct {
		Record map[string]any `json:"record"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if decoder.Decode(&out) != nil || out.Record == nil {
		return nil, errors.New("invalid protected record response")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid protected record response")
	}
	return out.Record, nil
}
