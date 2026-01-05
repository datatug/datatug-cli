package dtlog

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/posthog/posthog-go"
	"github.com/strongo/logus"
	"github.com/strongo/random"
	"gopkg.in/yaml.v3"
)

var ph posthog.Client

type posthogConfig struct {
	ApiKey          string    `yaml:"api_key"`
	ApiKeyTimestamp time.Time `yaml:"api_key_timestamp"`
	LockApiKey      bool      `yaml:"lock_api_key,omitempty"`
	DistinctID      string    `yaml:"distinct_id"`
}

func init() {
	ph = getPostHogClient()
}

var posthogDistinctID string

func getPostHogClient() posthog.Client {
	_, _ = fmt.Println("Initializing PostHog client...")
	config := readPostHogConfig()
	var configChanged bool
	if apiKey := os.Getenv("POSTHOG_API_KEY"); apiKey != "" {
		config.ApiKey = apiKey
	}
	if !config.LockApiKey && time.Now().After(config.ApiKeyTimestamp.Add(24*time.Hour)) {
		apiKey, err := getPostHogApiKeyFromServer()
		if err != nil {
			ctx := context.Background()
			logus.Warningf(ctx, "Failed to get PostHog API key from server: %v", err)
		} else {
			config.ApiKey = apiKey
			configChanged = true
		}
	}
	if config.ApiKey == "" {
		config.ApiKey = "phc_rsWNWZT0BM3UFazc38kmXvSacEYhYn7lNuqyRsg9ZJ1"
	}
	if config.DistinctID == "" {
		config.DistinctID = random.ID(16)
		configChanged = true
	}
	if configChanged {
		ctx := context.Background()
		if err := writePostHogConfigToFile(ctx, config); err != nil {
			logus.Errorf(ctx, "Failed to write PostHog config file: %v", err)
		}
	}
	return posthog.New(config.ApiKey)
}

func writePostHogConfigToFile(ctx context.Context, config posthogConfig) error {
	file, err := os.Create(getPosthogConfigFilePath())
	if err != nil {
		logus.Errorf(ctx, "Failed to create PostHog config file: %v", err)
	} else {
		defer func() {
			_ = file.Close()
		}()
		encoder := yaml.NewEncoder(file)
		if err = encoder.Encode(config); err != nil {
			ctx := context.Background()
			logus.Errorf(ctx, "Failed to encode PostHog config file: %v", err)
		}
	}
	return nil
}

func getPostHogApiKeyFromServer() (string, error) {
	const url = "https://raw.githubusercontent.com/datatug/datatug-cli/refs/heads/main/envs/prod/posthog-api-key.txt"
	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("failed to fetch posthog api key from server: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read posthog api key from server response: %w", err)
	}
	return string(respBytes), nil
}

func readPostHogConfig() (c posthogConfig) {
	name := getPosthogConfigFilePath()
	data, err := os.ReadFile(name)
	if err != nil {
		ctx := context.Background()
		logus.Errorf(ctx, "Failed to read PostHog config file: %v", err)
	}
	if err = yaml.Unmarshal(data, &c); err != nil {
		ctx := context.Background()
		logus.Errorf(ctx, "Failed to parse PostHog config file: %v", err)
	}
	return
}

func getPosthogConfigFilePath() string {
	return filestore.ExpandHome("~/datatug/.posthog.yaml")
}

func Enqueue(m posthog.Capture) {
	if ph == nil {
		return
	}
	if m.DistinctId == "" {
		m.DistinctId = posthogDistinctID
	}
	if err := ph.Enqueue(m); err != nil {
		ctx := context.Background()
		logus.Errorf(ctx, "posthog.enqueue failed: %v", err)
	}
}
