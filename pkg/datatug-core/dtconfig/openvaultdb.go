package dtconfig

import (
	"fmt"
	"os"
	"strings"

	"github.com/datatug/datatug-cli/pkg/openvaultdb"
)

type OpenVaultDBConfig struct {
	Targets map[string]OpenVaultDBTarget `yaml:"targets,omitempty" json:"-"`
}

type OpenVaultDBTarget struct {
	BaseURL    string `yaml:"baseUrl" json:"-"`
	DatabaseID string `yaml:"databaseId" json:"-"`
	TokenEnv   string `yaml:"tokenEnv" json:"-"`
}

func (v Settings) ResolveOpenVaultDBTargets() (map[string]openvaultdb.Target, error) {
	result := map[string]openvaultdb.Target{}
	if v.OpenVaultDB == nil {
		return result, nil
	}
	for id, configured := range v.OpenVaultDB.Targets {
		if configured.TokenEnv == "" || strings.ContainsAny(configured.TokenEnv, "=\x00") {
			return nil, fmt.Errorf("OpenVaultDB target %q has invalid tokenEnv", id)
		}
		target := openvaultdb.Target{BaseURL: configured.BaseURL, DatabaseID: configured.DatabaseID, Token: os.Getenv(configured.TokenEnv)}
		if err := target.Validate(); err != nil {
			return nil, fmt.Errorf("OpenVaultDB target %q: %w", id, err)
		}
		result[id] = target
	}
	return result, nil
}
