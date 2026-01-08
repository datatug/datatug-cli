package dtlog

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/posthog/posthog-go"
	"github.com/stretchr/testify/assert"
)

func TestPosthogConfig(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dtlog_test")
	assert.NoError(t, err)
	defer func(path string) {
		_ = os.RemoveAll(path)
	}(tempDir)

	configPath := filepath.Join(tempDir, ".posthog.yaml")

	oldGetPath := getPosthogConfigFilePath
	getPosthogConfigFilePath = func() string {
		return configPath
	}
	defer func() {
		getPosthogConfigFilePath = oldGetPath
	}()

	config := posthogConfig{
		ApiKey:     "test-api-key",
		DistinctID: "test-distinct-id",
	}

	err = writePostHogConfigToFile(context.Background(), config)
	assert.NoError(t, err)

	readConfig := readPostHogConfig()
	assert.Equal(t, config.ApiKey, readConfig.ApiKey)
	assert.Equal(t, config.DistinctID, readConfig.DistinctID)
}

func TestWithSession(t *testing.T) {
	sessionID = "test-session-id"
	sessionStarted = time.Now().Add(-10 * time.Second)

	props := posthog.NewProperties()
	props = withSession(props)

	assert.Equal(t, "test-session-id", props["$session_id"])
	assert.Equal(t, sessionStarted, props["$session_start_time"])
	assert.True(t, props["$session_duration"].(int64) >= 10000)
}

type mockPosthogClient struct {
	posthog.Client
	enqueued []posthog.Message
	closed   bool
}

func (m *mockPosthogClient) Enqueue(msg posthog.Message) error {
	m.enqueued = append(m.enqueued, msg)
	return nil
}

func (m *mockPosthogClient) Close() error {
	m.closed = true
	return nil
}

func TestEnqueue(t *testing.T) {
	mockClient := &mockPosthogClient{}

	mu.Lock()
	oldPh := ph
	oldInitialized := initialized
	oldPosthogDistinctID := posthogDistinctID
	oldQueue := queue

	ph = mockClient
	initialized = true
	posthogDistinctID = "test-distinct-id"
	mu.Unlock()

	defer func() {
		mu.Lock()
		ph = oldPh
		initialized = oldInitialized
		posthogDistinctID = oldPosthogDistinctID
		queue = oldQueue
		mu.Unlock()
	}()

	t.Run("Capture", func(t *testing.T) {
		mockClient.enqueued = nil
		msg := posthog.Capture{
			Event: "test-event",
		}
		Enqueue(msg)
		assert.Len(t, mockClient.enqueued, 1)
		captured := mockClient.enqueued[0].(posthog.Capture)
		assert.Equal(t, "test-event", captured.Event)
		assert.Equal(t, "test-distinct-id", captured.DistinctId)
		assert.NotZero(t, captured.Timestamp)
		assert.Equal(t, "test-session-id", captured.Properties["$session_id"])
	})

	t.Run("Exception", func(t *testing.T) {
		mockClient.enqueued = nil
		msg := posthog.Exception{
			ExceptionList: []posthog.ExceptionItem{
				{Type: "test-error"},
			},
		}
		Enqueue(msg)
		assert.Len(t, mockClient.enqueued, 1)
		exception := mockClient.enqueued[0].(posthog.Exception)
		assert.Equal(t, "test-error", exception.ExceptionList[0].Type)
		assert.Equal(t, "test-distinct-id", exception.DistinctId)
		assert.NotZero(t, exception.Timestamp)
	})

	t.Run("Queuing", func(t *testing.T) {
		mu.Lock()
		initialized = false
		queue = nil
		mu.Unlock()

		msg := posthog.Capture{Event: "queued-event"}
		Enqueue(msg)

		mu.Lock()
		assert.Len(t, queue, 1)
		assert.Equal(t, "queued-event", queue[0].(posthog.Capture).Event)
		mu.Unlock()
	})
}

func TestScreenOpened(t *testing.T) {
	mockClient := &mockPosthogClient{}

	mu.Lock()
	oldPh := ph
	oldInitialized := initialized
	ph = mockClient
	initialized = true
	mu.Unlock()

	defer func() {
		mu.Lock()
		ph = oldPh
		initialized = oldInitialized
		mu.Unlock()
	}()

	assert.Panics(t, func() {
		ScreenOpened("", "Test Screen")
	})

	ScreenOpened("test-screen-id", "Test Screen")

	assert.Len(t, mockClient.enqueued, 1)
	captured := mockClient.enqueued[0].(posthog.Capture)
	assert.Equal(t, "Screen opened", captured.Event)
	assert.Equal(t, "test-screen-id", captured.Properties["$screen_id"])
	assert.Equal(t, "Test Screen", captured.Properties["$screen_name"])
	assert.Equal(t, "DataTug", captured.Properties["$app_name"])
}

func TestClose(t *testing.T) {
	mockClient := &mockPosthogClient{}
	mu.Lock()
	oldPh := ph
	ph = mockClient
	mu.Unlock()

	defer func() {
		mu.Lock()
		ph = oldPh
		mu.Unlock()
	}()

	Close()

	assert.True(t, mockClient.closed)
	assert.Nil(t, ph)
}
