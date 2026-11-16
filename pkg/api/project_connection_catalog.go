package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

var errConnectionCatalogUnavailable = errors.New("project connection catalog unavailable")

// Defaults to the real codec; tests exercise refusal without partial output.
var marshalLocalConnectionVisibility = json.Marshal

var browserFixtureAssetURL = regexp.MustCompile(`^/assets/showcase/[a-z0-9][a-z0-9-]*\.sqlite$`)

// LocalConnectionCatalog exposes declaration metadata, never connection profiles
// or arbitrary files. Public-source executors must independently admit its pins.
type LocalConnectionCatalog struct {
	ArchivedItems    map[string]bool             `json:"-"`
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
	servedProjectDirCatalog     = servedProjectDir
	ioReadAllCatalog            = io.ReadAll
	openConnectionCatalogReader = openQualifiedProjectReader
	openConnectionCatalogFile   = func(r *qualifiedProjectReader, name string) (connectionCatalogFile, error) { return r.root.Open(name) }
)

type connectionCatalogFile interface {
	io.ReadCloser
	Stat() (os.FileInfo, error)
}

func LocalProjectConnectionCatalog(ctx context.Context, ref dto.ProjectRef) (result *LocalConnectionCatalog, err error) {
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
	err = filestore.ReadEnvironmentFiles(ctx, dir, func(_ *filestore.EnvironmentFiles) error {
		reader, e := openConnectionCatalogReader(dir)
		if e != nil {
			return errConnectionCatalogUnavailable
		}
		defer reader.close()
		result, e = readMergedLocalConnectionCatalog(reader)
		return e
	})
	if err == nil {
		err = filterLocalConnectionVisibility(dir, result)
	}
	return result, err
}

// LocalProjectConnectionCatalogForBranch binds declaration metadata to the
// selected current branch and its observed HEAD.
func LocalProjectConnectionCatalogForBranch(ctx context.Context, ref dto.ProjectRef, branch string) (*LocalConnectionCatalog, error) {
	value, err := withCurrentBranchRead(ctx, ref, branch, func(reader *qualifiedProjectReader) (any, error) {
		dir, err := servedProjectDirCatalog(ref.ProjectID)
		if err != nil {
			return nil, err
		}
		var result *LocalConnectionCatalog
		err = filestore.ReadEnvironmentFiles(ctx, dir, func(_ *filestore.EnvironmentFiles) error {
			var e error
			result, e = readMergedLocalConnectionCatalog(reader)
			return e
		})
		if err == nil {
			err = filterLocalConnectionVisibility(dir, result)
		}
		return result, err
	})
	if err != nil {
		return nil, err
	}
	return value.(*LocalConnectionCatalog), nil
}

// Read the same bounded collection namespace as the owning file provider.
// Unknown fields stay private; source URLs and browser pins remain validated.
func readMergedLocalConnectionCatalog(reader *qualifiedProjectReader) (*LocalConnectionCatalog, error) {
	entries, err := reader.dirEntries("connections")
	if err != nil || len(entries) > 1024 {
		return nil, errConnectionCatalogUnavailable
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	result := &LocalConnectionCatalog{Format: datatug.ConnectionCatalogFormat, Connections: []LocalCatalogConnection{}}
	all := datatug.ProjectConnections{Format: datatug.ConnectionCatalogFormat}
	files := 0
	catalogFiles := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		files++
		if files > 32 || !datatug.ValidConnectionID(strings.TrimSuffix(entry.Name(), ".json")) {
			return nil, errConnectionCatalogUnavailable
		}
		body, err := readLocalCatalogDocument(reader, "connections/"+entry.Name())
		if err != nil {
			return nil, errConnectionCatalogUnavailable
		}
		_, isCatalog, err := datatug.ParsePublicProjectConnectionDirectoryJSON(entry.Name(), body)
		if err != nil {
			return nil, errConnectionCatalogUnavailable
		}
		if !isCatalog {
			continue
		} // only an exactly owned and fully validated log document
		catalogFiles++
		catalog, err := readLocalConnectionCatalogBody(body)
		if err != nil {
			return nil, err
		}
		result.Connections = append(result.Connections, catalog.Connections...)
		result.BigQueryEditions = append(result.BigQueryEditions, catalog.BigQueryEditions...)
		result.BigQueryPlans = append(result.BigQueryPlans, catalog.BigQueryPlans...)
		for _, connection := range catalog.Connections {
			all.Connections = append(all.Connections, connection.EditionConnection)
		}
		all.BigQueryEditions = append(all.BigQueryEditions, catalog.BigQueryEditions...)
		all.BigQueryPlans = append(all.BigQueryPlans, catalog.BigQueryPlans...)
	}
	if catalogFiles == 0 || all.Validate() != nil {
		return nil, errConnectionCatalogUnavailable
	}
	return result, nil
}

func readLocalCatalogDocument(reader *qualifiedProjectReader, name string) (body []byte, err error) {
	f, err := openConnectionCatalogFile(reader, name)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			body, err = nil, closeErr
		}
	}()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > datatug.MaxPublicConnectionCatalogBytes {
		return nil, errConnectionCatalogUnavailable
	}
	remaining := maxQualifiedReadBytes - reader.bytes
	limit := datatug.MaxPublicConnectionCatalogBytes
	if remaining < limit {
		limit = remaining
	}
	if limit < 0 {
		return nil, errConnectionCatalogUnavailable
	}
	body, err = ioReadAllCatalog(io.LimitReader(f, int64(limit)+1))
	if err != nil || len(body) > limit {
		return nil, errConnectionCatalogUnavailable
	}
	reader.bytes += len(body)
	return body, nil
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
	if err != nil || !info.Mode().IsRegular() || info.Size() > datatug.MaxPublicConnectionCatalogBytes {
		return nil, errConnectionCatalogUnavailable
	}
	body, err := ioReadAllCatalog(io.LimitReader(f, datatug.MaxPublicConnectionCatalogBytes+1))
	if err != nil || len(body) > datatug.MaxPublicConnectionCatalogBytes {
		return nil, errConnectionCatalogUnavailable
	}
	return readLocalConnectionCatalogBody(body)
}

func readLocalConnectionCatalogBody(body []byte) (*LocalConnectionCatalog, error) {
	_, err := datatug.ParsePublicProjectConnectionsJSON(body)
	var catalog LocalConnectionCatalog
	if err != nil || json.Unmarshal(body, &catalog) != nil {
		return nil, errConnectionCatalogUnavailable
	}
	for _, connection := range catalog.Connections {
		if connection.Source != "" && !isCatalogRemoteURL(connection.Source) {
			return nil, errConnectionCatalogUnavailable
		}
		if fixtureURL := connection.BrowserFixture.URL; fixtureURL != "" &&
			!browserFixtureAssetURL.MatchString(fixtureURL) && !isCatalogRemoteURL(fixtureURL) {
			return nil, errConnectionCatalogUnavailable
		}
	}
	return &catalog, nil
}

func isCatalogRemoteURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != "" &&
		parsed.User == nil && parsed.RawQuery == "" && !parsed.ForceQuery && parsed.Fragment == ""
}

// Discovery receives visibility flags alongside the complete declarations.
// Saved references continue to resolve through the complete owning catalog.
func filterLocalConnectionVisibility(root string, catalog *LocalConnectionCatalog) error {
	if catalog == nil {
		return nil
	}
	hidden, err := filestore.ReadProjectArchivedItems(root)
	if err != nil {
		return err
	}
	catalog.ArchivedItems = map[string]bool{}
	for ref, value := range hidden {
		if value && ref.Kind == "connection" {
			catalog.ArchivedItems[ref.ID] = true
		}
	}
	return nil
}

// Discovery flags stay alongside the complete resolver declarations. Browser
// execution may still resolve an archived connection explicitly by ID.
func (catalog LocalConnectionCatalog) MarshalJSON() ([]byte, error) {
	type plain LocalConnectionCatalog
	body, err := marshalLocalConnectionVisibility(plain(catalog))
	if err != nil {
		return nil, err
	}
	var response map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err = decoder.Decode(&response); err != nil {
		return nil, err
	}
	for _, collection := range []string{"connections", "bigQueryEditions", "bigQueryPlans"} {
		items, _ := response[collection].([]any)
		for _, item := range items {
			fields, ok := item.(map[string]any)
			if !ok {
				continue
			}
			id, _ := fields["id"].(string)
			fields["archived"] = catalog.ArchivedItems[id]
		}
	}
	return json.Marshal(response)
}
