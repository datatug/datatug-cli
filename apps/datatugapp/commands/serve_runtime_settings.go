package commands

import (
	"errors"
	"io/fs"
	"os"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/incidentstore"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"gopkg.in/yaml.v3"
)

type serveRuntimeSettings struct {
	Server struct {
		IncidentStores    []incidentstore.ConfiguredStore      `yaml:"incidentStores"`
		EvidenceDir       string                               `yaml:"evidenceDir"`
		SnapshotByteCap   int                                  `yaml:"snapshotByteCap"`
		SnapshotRetention time.Duration                        `yaml:"snapshotRetention"`
		SnapshotPolicies  map[string]api.SnapshotProjectPolicy `yaml:"snapshotPolicies"`
	} `yaml:"server"`
}

func loadServeRuntimeSettings() (serveRuntimeSettings, error) {
	var settings serveRuntimeSettings
	data, err := os.ReadFile(dtconfig.GetConfigFilePath())
	if errors.Is(err, fs.ErrNotExist) {
		return settings, nil
	}
	if err != nil {
		return settings, err
	}
	if err := decodeServeRuntimeSettings(data, &settings); err != nil {
		return settings, err
	}
	return settings, nil
}

func decodeServeRuntimeSettings(data []byte, settings *serveRuntimeSettings) error {
	return yaml.Unmarshal(data, settings)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func firstNonZeroDuration(values ...time.Duration) time.Duration {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}
