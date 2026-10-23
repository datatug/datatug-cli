package dbcopy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2openvaultdb"
	"github.com/datatug/datatug-cli/internal/strictjson"
)

// OpenVaultDBDescriptor is CLI-owned connection metadata an openvaultdb://
// source points at. The token stays in the daemon environment under TokenEnv
// and never enters the project. PrincipalID binds the credential to the fixed
// local session; OVDB authenticates the token and resolves memberships
// independently, and no caller-supplied identity is sent.
type OpenVaultDBDescriptor struct {
	BaseURL     string `json:"baseUrl"`
	DatabaseID  string `json:"databaseId"`
	TokenEnv    string `json:"tokenEnv"`
	PrincipalID string `json:"principalId"`
}

const maxOpenVaultDBDescriptorBytes = 64 << 10

// OpenOpenVaultDBSource reads the descriptor at path and opens the source
// through dalgo2openvaultdb's guarded read profile: queries only, for the
// bound principal, through the server's ACL-evaluated route.
func OpenOpenVaultDBSource(path string) (dal.DB, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open OpenVaultDB connection descriptor: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxOpenVaultDBDescriptorBytes+1))
	if err != nil || len(data) > maxOpenVaultDBDescriptorBytes {
		return nil, errors.New("invalid OpenVaultDB connection descriptor size")
	}
	var fields map[string]json.RawMessage
	if err = strictjson.DecodeObject(data, &fields); err != nil {
		return nil, errors.New("invalid OpenVaultDB connection descriptor")
	}
	for key := range fields {
		switch key {
		case "baseUrl", "databaseId", "tokenEnv", "principalId":
		default:
			return nil, fmt.Errorf("unknown OpenVaultDB connection field %q", key)
		}
	}
	var descriptor OpenVaultDBDescriptor
	if err = json.Unmarshal(data, &descriptor); err != nil {
		return nil, errors.New("invalid OpenVaultDB connection descriptor")
	}
	if descriptor.TokenEnv == "" || strings.ContainsAny(descriptor.TokenEnv, "=\x00") || strings.TrimSpace(descriptor.PrincipalID) == "" {
		return nil, errors.New("OpenVaultDB connection requires tokenEnv and principalId")
	}
	// Bind each credential to its operator-configured destination independently
	// of editable project/catalog files, preventing credential exfiltration.
	if os.Getenv(descriptor.TokenEnv+"_BASE_URL") != descriptor.BaseURL || os.Getenv(descriptor.TokenEnv+"_PRINCIPAL_ID") != descriptor.PrincipalID {
		return nil, errors.New("OpenVaultDB credential destination is not configured")
	}
	return dalgo2openvaultdb.NewDB(descriptor.BaseURL, descriptor.DatabaseID,
		dalgo2openvaultdb.WithBearerToken(os.Getenv(descriptor.TokenEnv)),
		dalgo2openvaultdb.WithGuardedReads(descriptor.PrincipalID))
}
