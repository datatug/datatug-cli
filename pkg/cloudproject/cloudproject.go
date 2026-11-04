// Package cloudproject reads a DataTug Cloud project owned by a Space through
// the DataTug backend's /v0/datatug/* endpoints, the same reads datatug.app
// makes. It never talks to Firestore: the backend checks the caller's Space
// membership and the project's access on every read
// (spec/features/cli/cloud-projects).
package cloudproject

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// Errors of a read. Each maps to one exit code and one line of the CLI.
var (
	// ErrNotFound is a 404: the project does not exist or was deleted.
	ErrNotFound = errors.New("project not found, or it was deleted")
	// ErrUnauthorized is a 401: the login expired or was revoked.
	ErrUnauthorized = errors.New("DataTug sign-in expired")
	// ErrForbidden is a 403: the account may not read the project.
	ErrForbidden = errors.New("no access to this project")
	// ErrUnavailable wraps a network failure or a 5xx answer.
	ErrUnavailable = errors.New("can't reach DataTug Cloud")
)

// maxResponseBytes bounds every answer the CLI reads.
const maxResponseBytes = 8 << 20

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// ValidIdentifier reports whether s is a valid Space or project id: the rule
// of datatug.app's isSharedProjectIdentifier and the backend's
// ValidateSharedProjectIdentifier.
func ValidIdentifier(s string) bool { return identifier.MatchString(s) }

// Ref names a Space-owned cloud project.
type Ref struct {
	SpaceID, ProjectID string
}

// Validate returns which part of the reference is invalid, or nil.
func (r Ref) Validate() error {
	if !ValidIdentifier(r.SpaceID) {
		return errors.New("invalid space id: use 1-128 letters, digits, '_' or '-'")
	}
	if !ValidIdentifier(r.ProjectID) {
		return errors.New("invalid project id: use 1-128 letters, digits, '_' or '-'")
	}
	return nil
}

// Address is the project's canonical datatug.app address.
func (r Ref) Address() string {
	return "https://datatug.app/space/" + r.SpaceID + "/store/firestore/project/" + r.ProjectID
}

// Client sends authenticated GET requests to the backend.
type Client struct {
	// BaseURL ends with "/v0/".
	BaseURL string
	// HTTP must not follow redirects, so a bearer never reaches another host.
	HTTP *http.Client
	// Token returns the current bearer.
	Token func(context.Context) (string, error)
}

func (c *Client) get(ctx context.Context, path string, ref Ref, out any) (http.Header, error) {
	params := url.Values{"storage": {"firestore"}, "spaceID": {ref.SpaceID}, "project": {ref.ProjectID}}
	token, err := c.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"datatug/"+path+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, transportReason(err))
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNotFound
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case resp.StatusCode == http.StatusForbidden:
		return nil, ErrForbidden
	case resp.StatusCode >= 500:
		return nil, fmt.Errorf("%w: HTTP %d", ErrUnavailable, resp.StatusCode)
	default:
		return nil, fmt.Errorf("unexpected answer from DataTug Cloud: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, transportReason(err))
	}
	if len(body) > maxResponseBytes {
		return nil, errors.New("answer from DataTug Cloud is too large")
	}
	if err = json.Unmarshal(body, out); err != nil {
		return nil, errors.New("unreadable answer from DataTug Cloud")
	}
	return resp.Header, nil
}

// transportReason is a short reason for a transport error. A *url.Error
// carries the request URL, which is left out.
func transportReason(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	return err.Error()
}

// Item is an id with an optional title.
type Item struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}

// Project is what the terminal UI shows of a cloud project. A list that
// failed to load carries its error, shown in its own panel.
type Project struct {
	Ref          Ref
	ID, Title    string
	Environments []Item
	EnvsErr      error
	Databases    []Item
	DBsErr       error
	// Queries are ids, with "<folder>/<id>" for a query in a folder.
	Queries    []Item
	QueriesErr error
}

// Load reads the project. Only a failing summary is an error: a failing list
// is kept on the project.
func Load(ctx context.Context, c *Client, ref Ref) (Project, error) {
	var summary Item
	if _, err := c.get(ctx, "projects/project_summary", ref, &summary); err != nil {
		return Project{}, err
	}
	p := Project{Ref: ref, ID: ref.ProjectID, Title: summary.Title}
	if summary.ID != "" {
		p.ID = summary.ID
	}
	p.ID, p.Title = clean(p.ID), clean(p.Title)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); p.Environments, p.EnvsErr = environments(ctx, c, ref) }()
	go func() { defer wg.Done(); p.Databases, p.DBsErr = databases(ctx, c, ref) }()
	go func() { defer wg.Done(); p.Queries, p.QueriesErr = queries(ctx, c, ref) }()
	wg.Wait()
	return p, nil
}

// clean drops control characters from text other Space members wrote, so a
// title cannot carry terminal escape sequences.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

func cleanItems(items []Item) []Item {
	for i := range items {
		items[i] = Item{ID: clean(items[i].ID), Title: clean(items[i].Title)}
	}
	return items
}

func environments(ctx context.Context, c *Client, ref Ref) ([]Item, error) {
	var state struct {
		Environments []Item `json:"environments"`
	}
	if _, err := c.get(ctx, "environments/state", ref, &state); err != nil {
		return nil, err
	}
	return cleanItems(state.Environments), nil
}

// databases are the connections of the project's own catalog. The backend
// sends its public demo catalog, without an ETag, to a project that has none:
// that is shown as no databases.
func databases(ctx context.Context, c *Client, ref Ref) ([]Item, error) {
	var catalog struct {
		Connections []Item `json:"connections"`
		// Revision is sent only with a project's own catalog, as its ETag.
		Revision *int64 `json:"connectionsRevision"`
	}
	header, err := c.get(ctx, "projects/connection_catalog", ref, &catalog)
	if err != nil {
		return nil, err
	}
	if header.Get("ETag") == "" && catalog.Revision == nil {
		return []Item{}, nil
	}
	items := make([]Item, 0, len(catalog.Connections))
	for _, connection := range catalog.Connections {
		if connection.ID != "" {
			items = append(items, connection)
		}
	}
	return cleanItems(items), nil
}

type queryFolder struct {
	ID      string         `json:"id"`
	Folders []*queryFolder `json:"folders"`
	Items   []Item         `json:"items"`
}

func queries(ctx context.Context, c *Client, ref Ref) ([]Item, error) {
	var root queryFolder
	if _, err := c.get(ctx, "queries/all_queries", ref, &root); err != nil {
		return nil, err
	}
	var items []Item
	var walk func(f *queryFolder, path []string)
	walk = func(f *queryFolder, path []string) {
		for _, q := range f.Items {
			if q.ID == "" {
				continue
			}
			id := strings.Join(append(append([]string(nil), path...), q.ID), "/")
			items = append(items, Item{ID: id, Title: q.Title})
		}
		for _, child := range f.Folders {
			if child != nil {
				walk(child, append(path, child.ID))
			}
		}
	}
	walk(&root, nil)
	sort.SliceStable(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return cleanItems(items), nil
}
