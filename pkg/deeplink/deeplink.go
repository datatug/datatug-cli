// Package deeplink reads the address of a page of the DataTug web app
// (https://datatug.app/...) into what the terminal UI needs to open the same
// project and page: `datatug ui --deep-link <address>`.
//
// The address shapes are those of the web app's project-address library
// (datatug/datatug-apps libs/datatug/project-address):
//
//   - GitHub: /project/github.com/<owner>/<repo>[/<dir>…][/-/<page>…][?ref=<ref>],
//     and the legacy …/<repo>/tree/<ref>[/<dir>…][/-/<page>…] and
//     …/<repo>/blob/<ref>[/<dir>…]/datatug-project.json;
//   - any store: /store/<storeId>/project/<projectId>[/<page>…];
//   - a DataTug Cloud project of a Space:
//     /space/<spaceId>/store/firestore/project/<projectId>[/<page>…].
//
// Parse is pure: it makes no request and touches no file.
package deeplink

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// Kind is what an address names.
type Kind int

const (
	// KindNone is a page of the web app that is not a project (home, sign-in…):
	// the terminal UI opens on its normal start screen.
	KindNone Kind = iota
	// KindGitHub is a project in a GitHub repository.
	KindGitHub
	// KindAgent is a project served by a `datatug serve` agent on this machine.
	KindAgent
	// KindCloud is a DataTug Cloud (Firestore) project.
	KindCloud
	// KindBrowser is a project stored in the browser of the web app.
	KindBrowser
	// KindOtherStore is a project of a store the terminal UI does not know.
	KindOtherStore
)

// GitHubProject locates a project in a GitHub repository.
type GitHubProject struct {
	// Owner and Repo are lower case.
	Owner, Repo string
	// Folder is the repository-relative directory of the project; "" is the
	// repository root.
	Folder string
	// Ref is the branch, tag or commit, "" for the default branch.
	Ref string
}

// Link is a parsed address.
type Link struct {
	Kind Kind
	// GitHub is set for KindGitHub.
	GitHub GitHubProject
	// StoreID and ProjectID are set for every project kind but KindGitHub;
	// SpaceID only for a Space project of DataTug Cloud.
	StoreID, ProjectID, SpaceID string
	// Page is the page of the project, as decoded segments (["query", "q1"]);
	// empty for the project's home.
	Page []string
}

// Usage is the form an address must have, for messages.
const Usage = "https://datatug.app/project/github.com/<owner>/<repo>[/<dir>…][/-/<page>…]"

// ErrInvalid is wrapped by every error of Parse. The error text never repeats
// the address: it may hold credentials or a query.
var ErrInvalid = errors.New("invalid DataTug web address")

func invalid(reason string) error {
	return fmt.Errorf("%w: %s; expected %s", ErrInvalid, reason, Usage)
}

const (
	githubStoreID   = "github.com"
	firestoreStore  = "firestore"
	browserStore    = "local"
	projectFileName = "datatug-project.json"
	defaultFolder   = "datatug"
	defaultRef      = "HEAD"
)

var (
	ownerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	repoPattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	agentStore   = regexp.MustCompile(`^(?:https?-)?(?:localhost|127\.0\.0\.1|\[::1\]):\d{1,5}$`)
	refPiece     = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// pageNames are the first segments of the project pages of the web app
// (GITHUB_PROJECT_PAGE_NAMES): at a repository root with no `/-/`, a first
// segment that is one of them is a page, any other a folder.
var pageNames = map[string]bool{}

func init() {
	for _, name := range strings.Fields(`archived board boards browser-db card cards challenge chat checks composer
		data-models database dbmodel dbmodels demo-db-sandbox detective diff entities entity env environments
		graph-source incidents invoice-explorer invoice-overview items meanings menu new-entity overview project
		public-data queries query quick-search resources search-schema server servers settings sessions shipping
		sources start-chat tags teams variables walkthrough watch widgets work`) {
		pageNames[name] = true
	}
}

// Parse reads an address of the web app: https://datatug.app/… or, for a
// development build of the web app, http://localhost:<port>/….
func Parse(raw string) (Link, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Opaque != "" {
		return Link{}, invalid("not a web address")
	}
	if u.User != nil {
		return Link{}, invalid("an address with credentials is not accepted")
	}
	if !webAppOrigin(u) {
		return Link{}, invalid("not an address of the DataTug web app")
	}
	segments := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	if n := len(segments); n > 0 && segments[n-1] == "" {
		segments = segments[:n-1]
	}
	if len(segments) == 0 {
		return Link{}, nil
	}
	switch segments[0] {
	case "project":
		if len(segments) < 2 || segments[1] != githubStoreID {
			return Link{}, nil
		}
		refs := u.Query()["ref"]
		if len(refs) > 1 {
			return Link{}, invalid("more than one ref")
		}
		return parseGitHubPath(segments[2:], refs)
	case "store":
		if len(segments) < 4 || segments[2] != "project" {
			return Link{}, nil
		}
		return parseStorePath(segments[1], segments[3], segments[4:])
	case "space":
		if len(segments) < 6 || segments[2] != "store" || segments[3] != firestoreStore || segments[4] != "project" {
			return Link{}, nil
		}
		space, project := decode(segments[1]), decode(segments[5])
		page, ok := pageOf(segments[6:])
		if space == "" || project == "" || !ok {
			return Link{}, invalid("malformed project address")
		}
		return Link{Kind: KindCloud, StoreID: firestoreStore, SpaceID: space, ProjectID: project, Page: page}, nil
	}
	return Link{}, nil
}

// webAppOrigin reports whether u is on the web app: https://datatug.app, or a
// loopback host on a port (a local build of the web app).
func webAppOrigin(u *url.URL) bool {
	switch u.Scheme {
	case "https":
		if u.Host == "datatug.app" {
			return true
		}
	case "http":
	default:
		return false
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil || port == "" {
		return false
	}
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// decode is a decoded path segment, or "" when it cannot be part of an
// address: undecodable, empty, `.` or `..`, with a `/ \ % ? #`, a control
// character or surrounding white space.
func decode(raw string) string {
	s, err := url.PathUnescape(raw)
	if err != nil || s == "" || s == "." || s == ".." || s != strings.TrimSpace(s) || strings.ContainsAny(s, `/\%?#`) {
		return ""
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return s
}

// pageOf decodes the segments of a page.
func pageOf(segments []string) ([]string, bool) {
	page := make([]string, 0, len(segments))
	for _, raw := range segments {
		s := decode(raw)
		if s == "" {
			return nil, false
		}
		page = append(page, s)
	}
	if len(page) == 0 {
		return nil, true
	}
	return page, true
}

// validRef reports whether ref is a branch, tag or commit name an address can
// carry: pieces of letters, digits, `.`, `_`, `-` joined by `/`, never `..`.
func validRef(ref string) bool {
	for _, piece := range strings.Split(ref, "/") {
		if !refPiece.MatchString(piece) || piece == "." || strings.Contains(piece, "..") || strings.HasSuffix(piece, ".lock") {
			return false
		}
	}
	return true
}

func parseGitHubPath(segments []string, refs []string) (Link, error) {
	if len(segments) < 2 {
		return Link{}, nil
	}
	owner, repo := decode(segments[0]), decode(segments[1])
	if len(repo) > 4 && strings.EqualFold(repo[len(repo)-4:], ".git") {
		repo = repo[:len(repo)-4]
	}
	if !ownerPattern.MatchString(owner) || !repoPattern.MatchString(repo) || repo == "." || repo == ".." {
		return Link{}, invalid("not a GitHub owner and repository")
	}
	after := segments[2:]
	var ref string
	if len(refs) == 1 {
		if ref = refs[0]; ref == "" || !validRef(ref) {
			return Link{}, invalid("malformed ref")
		}
	}
	var dirs, page []string
	word := ""
	if len(after) > 0 {
		word = strings.ToLower(after[0])
	}
	switch word {
	case "tree", "blob":
		if len(after) < 2 || len(refs) > 0 {
			return Link{}, invalid("malformed GitHub tree address")
		}
		if ref = decode(after[1]); ref == "" || !validRef(ref) {
			return Link{}, invalid("malformed ref")
		}
		tail := after[2:]
		if word == "blob" {
			if len(tail) == 0 || tail[len(tail)-1] != projectFileName {
				return Link{}, invalid("a link to a file is not a project")
			}
			tail = tail[:len(tail)-1]
		}
		dirs, page = splitAtDash(tail)
	default:
		dirs, page = splitAtDash(after)
		if page == nil && len(dirs) > 0 && pageNames[strings.ToLower(dirs[0])] {
			dirs, page = nil, dirs
		}
	}
	folder := make([]string, 0, len(dirs))
	for _, raw := range dirs {
		dir := decode(raw)
		if dir == "" || dir == "-" || strings.Contains(dir, "@") {
			return Link{}, invalid("malformed project folder")
		}
		folder = append(folder, dir)
	}
	pageSegments, ok := pageOf(page)
	if !ok {
		return Link{}, invalid("malformed project page")
	}
	if ref == defaultRef {
		ref = ""
	}
	return Link{
		Kind: KindGitHub,
		GitHub: GitHubProject{
			Owner:  strings.ToLower(owner),
			Repo:   strings.ToLower(repo),
			Folder: strings.Join(folder, "/"),
			Ref:    ref,
		},
		Page: pageSegments,
	}, nil
}

// splitAtDash splits segments at the first `-`: the folder before it, the page
// after it (nil when there is no `-`).
func splitAtDash(segments []string) (dirs, page []string) {
	for i, s := range segments {
		if s == "-" {
			return segments[:i], append([]string{}, segments[i+1:]...)
		}
	}
	return segments, nil
}

func parseStorePath(rawStore, rawProject string, rest []string) (Link, error) {
	store, project := decode(rawStore), decode(rawProject)
	page, ok := pageOf(rest)
	if store == "" || project == "" || !ok {
		return Link{}, invalid("malformed project address")
	}
	switch lower := strings.ToLower(store); {
	case lower == githubStoreID || lower == "github":
		if store != lower {
			return Link{}, invalid("unsupported spelling of the GitHub store")
		}
		return githubProjectID(project, page)
	case store == firestoreStore:
		return Link{Kind: KindCloud, StoreID: store, ProjectID: project, Page: page}, nil
	case store == browserStore:
		return Link{Kind: KindBrowser, StoreID: store, ProjectID: project, Page: page}, nil
	case agentStore.MatchString(lower):
		return Link{Kind: KindAgent, StoreID: store, ProjectID: project, Page: page}, nil
	}
	return Link{Kind: KindOtherStore, StoreID: store, ProjectID: project, Page: page}, nil
}

// githubProjectID reads a GitHub project id: `repo@owner`, `repo@owner@folder`
// or `repo@owner@folder@ref`; a two-part id is the `datatug` folder.
func githubProjectID(id string, page []string) (Link, error) {
	parts := strings.Split(id, "@")
	if len(parts) < 2 || len(parts) > 4 {
		return Link{}, invalid("malformed GitHub project id")
	}
	repo, owner := parts[0], parts[1]
	if !ownerPattern.MatchString(owner) || !repoPattern.MatchString(repo) || repo == "." || repo == ".." {
		return Link{}, invalid("not a GitHub owner and repository")
	}
	p := GitHubProject{Owner: strings.ToLower(owner), Repo: strings.ToLower(repo), Folder: defaultFolder}
	if len(parts) > 2 {
		// The id is one decoded segment, so the folder holds no `/`.
		if p.Folder = parts[2]; p.Folder == "." || p.Folder == ".." || p.Folder == "-" {
			return Link{}, invalid("malformed project folder")
		}
	}
	if len(parts) == 4 && parts[3] != "" && parts[3] != defaultRef {
		if !validRef(parts[3]) {
			return Link{}, invalid("malformed ref")
		}
		p.Ref = parts[3]
	}
	return Link{Kind: KindGitHub, GitHub: p, Page: page}, nil
}
