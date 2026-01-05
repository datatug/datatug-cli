package dtlog

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/google/uuid"
	"github.com/posthog/posthog-go"
	"github.com/strongo/logus"
	"github.com/strongo/random"
	"gopkg.in/yaml.v3"
)

var ph posthog.Client
var posthogDistinctID string
var sessionID string
var sessionStarted time.Time

type posthogConfig struct {
	ApiKey          string    `yaml:"api_key"`
	ApiKeyTimestamp time.Time `yaml:"api_key_timestamp"`
	LockApiKey      bool      `yaml:"lock_api_key,omitempty"`
	DistinctID      string    `yaml:"distinct_id"`
}

func init() {
	ph = getPostHogClient()
	sessionID = uuid.NewString()
	sessionStarted = time.Now()
}

func Close() {
	_ = ph.Close()
	ph = nil
}

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
			config.ApiKeyTimestamp = time.Now()
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
	posthogDistinctID = config.DistinctID
	client, err := posthog.NewWithConfig(config.ApiKey, posthog.Config{Endpoint: "https://eu.i.posthog.com"})
	if err != nil {
		ctx := context.Background()
		logus.Errorf(ctx, "Failed to initialize PostHog client: %v", err)
		return nil
	}
	return client
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

func ScreenOpened(id, name string) {
	if id == "" {
		panic("id is empty")
	}
	props := posthog.NewProperties().
		Set("$app_name", "DataTug").
		Set("$app_version", version).
		Set("$screen_id", id)

	if name != "" {
		props.Set("$screen_name", name)
	}

	m := posthog.Capture{
		Event:      "Screen opened",
		Properties: props,
	}
	Enqueue(m)
}

func DistinctID() string {
	return posthogDistinctID
}

func withSession(p posthog.Properties) posthog.Properties {
	return p.
		Set("$session_id", sessionID).
		Set("$session_start_time", sessionStarted)
}

func Enqueue(msg posthog.Message) {
	if ph == nil {
		return
	}
	switch m := msg.(type) {
	case posthog.Capture:
		if m.DistinctId == "" {
			m.DistinctId = posthogDistinctID
		}
		if m.Timestamp.IsZero() {
			m.Timestamp = time.Now()
		}
		m.Properties = withSession(m.Properties)
		msg = m
	case posthog.Exception:
		if m.DistinctId == "" {
			m.DistinctId = posthogDistinctID
		}
		if m.Timestamp.IsZero() {
			m.Timestamp = time.Now()
		}
		msg = m
	}
	if err := ph.Enqueue(msg); err != nil {
		ctx := context.Background()
		logus.Errorf(ctx, "posthog.enqueue failed: %v", err)
	}
}
