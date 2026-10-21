package api

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Testdata is a portable copy of the URL-bearing fields and all 18 connection
// declarations in datatug-demo-project/demo-project-1/connections/demo-db.json.
func TestReadLocalConnectionCatalogBody_AdmitsDemoProjectAssetFixtures(t *testing.T) {
	body, err := os.ReadFile("testdata/demo-project-connections.json")
	require.NoError(t, err)

	catalog, err := readLocalConnectionCatalogBody(body)
	require.NoError(t, err)
	require.Len(t, catalog.Connections, 18)

	datasets := make(map[string]struct{})
	for _, connection := range catalog.Connections {
		datasets[connection.Dataset] = struct{}{}
	}
	assert.Len(t, datasets, 6)
	assert.Equal(t, "/assets/showcase/chinook.sqlite", catalog.Connections[0].BrowserFixture.URL)
	assert.Equal(t, "/assets/showcase/northwind.sqlite", catalog.Connections[3].BrowserFixture.URL)
}

func TestReadLocalConnectionCatalogBody_RejectsHostileBrowserFixtureURLs(t *testing.T) {
	body, err := os.ReadFile("testdata/demo-project-connections.json")
	require.NoError(t, err)

	var fixture map[string]any
	require.NoError(t, json.Unmarshal(body, &fixture))
	connections := fixture["connections"].([]any)
	connection := connections[0].(map[string]any)
	browserFixture := connection["browserFixture"].(map[string]any)

	for _, fixtureURL := range []string{
		"//example.com/assets/showcase/chinook.sqlite",
		"/assets/showcase/chinook.sqlite?changed=1",
		"/assets/showcase/chinook.sqlite#fragment",
		"/assets/showcase/../showcase/chinook.sqlite",
		"/assets/showcase/%2e%2e/showcase/chinook.sqlite",
		"/assets/showcase/%2Fchinook.sqlite",
		`/assets/showcase/..\\chinook.sqlite`,
		"/assets/showcase/Chinook.sqlite",
		"/assets/other/chinook.sqlite",
		"data:application/vnd.sqlite3;base64,AAAA",
	} {
		t.Run(fixtureURL, func(t *testing.T) {
			browserFixture["url"] = fixtureURL
			candidate, marshalErr := json.Marshal(fixture)
			require.NoError(t, marshalErr)
			_, readErr := readLocalConnectionCatalogBody(candidate)
			assert.ErrorIs(t, readErr, errConnectionCatalogUnavailable)
		})
	}
}

func TestReadLocalConnectionCatalogBody_KeepsRemoteURLValidation(t *testing.T) {
	body, err := os.ReadFile("testdata/demo-project-connections.json")
	require.NoError(t, err)

	var fixture map[string]any
	require.NoError(t, json.Unmarshal(body, &fixture))
	connection := fixture["connections"].([]any)[0].(map[string]any)

	for _, sourceURL := range []string{
		"://bad-parse",
		"ftp://example.com/db",
		"https:///missing-host",
		"https://user:password@example.com/db",
		"https://example.com/db?changed=1",
		"https://example.com/db#fragment",
	} {
		t.Run(sourceURL, func(t *testing.T) {
			connection["source"] = sourceURL
			candidate, marshalErr := json.Marshal(fixture)
			require.NoError(t, marshalErr)
			_, readErr := readLocalConnectionCatalogBody(candidate)
			assert.ErrorIs(t, readErr, errConnectionCatalogUnavailable)
		})
	}
}
