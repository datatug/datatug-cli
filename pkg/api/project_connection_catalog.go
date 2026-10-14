package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
)

var errConnectionCatalogUnavailable = errors.New("project connection catalog unavailable")

// LocalConnectionCatalog exposes declaration metadata, never connection profiles
// or arbitrary files. Public-source executors must independently admit its pins.
type LocalConnectionCatalog struct {
	Format           string                      `json:"format"`
	Connections      []LocalCatalogConnection    `json:"connections"`
	BigQueryEditions []datatug.EditionConnection `json:"bigQueryEditions,omitempty"`
	BigQueryPlans    []datatug.ConnectionPlan    `json:"bigQueryPlans,omitempty"`
}
type LocalCatalogConnection struct {
	datatug.EditionConnection
	Source         string `json:"source,omitempty"`
	FixtureSHA256  string `json:"fixtureSha256,omitempty"`
	BrowserFixture struct {
		URL   string `json:"url,omitempty"`
		Bytes int64  `json:"bytes,omitempty"`
	} `json:"browserFixture,omitempty"`
}

var (
	servedProjectDirCatalog = servedProjectDir
	ioReadAllCatalog        = io.ReadAll
)

func LocalProjectConnectionCatalog(_ context.Context, ref dto.ProjectRef) (*LocalConnectionCatalog, error) {
	if err := requireLocalReadPrincipal(); err != nil {
		return nil, err
	}
	if _, err := ResolveStoreID(ref.StoreID, ref.ProjectID); err != nil {
		return nil, err
	}
	if ref.StoreID != LocalStoreID {
		return nil, ErrUnknownStoreID
	}
	dir, err := servedProjectDirCatalog(ref.ProjectID)
	if err != nil {
		return nil, err
	}
	return readLocalConnectionCatalog(dir)
}

func readLocalConnectionCatalog(dir string) (*LocalConnectionCatalog, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, errConnectionCatalogUnavailable
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open("connections/demo-db.json")
	if err != nil {
		return nil, errConnectionCatalogUnavailable
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 256<<10 {
		return nil, errConnectionCatalogUnavailable
	}
	body, err := ioReadAllCatalog(io.LimitReader(f, (256<<10)+1))
	if err != nil || len(body) > 256<<10 {
		return nil, errConnectionCatalogUnavailable
	}
	var declarations datatug.ProjectConnections
	var catalog LocalConnectionCatalog
	if json.Unmarshal(body, &declarations) != nil || declarations.Validate() != nil || json.Unmarshal(body, &catalog) != nil {
		return nil, errConnectionCatalogUnavailable
	}
	for _, connection := range catalog.Connections {
		for _, source := range []string{connection.Source, connection.BrowserFixture.URL} {
			if source == "" {
				continue
			}
			parsed, parseErr := url.Parse(source)
			if parseErr != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
				return nil, errConnectionCatalogUnavailable
			}
		}
	}
	return &catalog, nil
}
