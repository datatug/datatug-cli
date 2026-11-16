package deeplink

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func gh(owner, repo, folder, ref string, page ...string) Link {
	l := Link{Kind: KindGitHub, GitHub: GitHubProject{Owner: owner, Repo: repo, Folder: folder, Ref: ref}}
	if len(page) > 0 {
		l.Page = page
	}
	return l
}

func TestParseAccepted(t *testing.T) {
	const app = "https://datatug.app"
	for _, tc := range []struct {
		in   string
		want Link
	}{
		// Non-project pages.
		{app, Link{}},
		{app + "/", Link{}},
		{app + "/home", Link{}},
		{"http://localhost:4200/home", Link{}},
		{"http://127.0.0.1:4200/", Link{}},
		{"http://[::1]:4200/", Link{}},
		{app + "/project/gitlab.com/a/b", Link{}},
		{app + "/project", Link{}},
		{app + "/project/github.com/acme", Link{}},
		{app + "/store/firestore", Link{}},
		{app + "/store/firestore/projects/x", Link{}},
		{app + "/space/s1/store/github.com/project/p", Link{}},

		// GitHub, short shape.
		{app + "/project/github.com/Acme/Repo", gh("acme", "repo", "", "")},
		{app + "/project/github.com/acme/repo.git/", gh("acme", "repo", "", "")},
		{app + "/project/github.com/acme/repo/some/dir", gh("acme", "repo", "some/dir", "")},
		{app + "/project/github.com/acme/repo/some/dir/-/queries", gh("acme", "repo", "some/dir", "", "queries")},
		{app + "/project/github.com/acme/repo/-/query/a%20b", gh("acme", "repo", "", "", "query", "a b")},
		{app + "/project/github.com/acme/repo/queries", gh("acme", "repo", "", "", "queries")},
		{app + "/project/github.com/acme/repo/Query/q1", gh("acme", "repo", "", "", "Query", "q1")},
		{app + "/project/github.com/acme/repo/chat/-", gh("acme", "repo", "chat", "")},
		{app + "/project/github.com/acme/repo/dir?ref=feature/x&tab=1#frag", gh("acme", "repo", "dir", "feature/x")},
		{app + "/project/github.com/acme/repo?ref=HEAD", gh("acme", "repo", "", "")},
		{"http://localhost:4200/project/github.com/acme/repo", gh("acme", "repo", "", "")},

		// GitHub, legacy shapes.
		{app + "/project/github.com/acme/repo/tree/main/dir/-/boards", gh("acme", "repo", "dir", "main", "boards")},
		{app + "/project/github.com/acme/repo/TREE/HEAD", gh("acme", "repo", "", "")},
		{app + "/project/github.com/acme/repo/blob/v1/dir/datatug-project.json", gh("acme", "repo", "dir", "v1")},
		{app + "/store/github.com/project/repo@acme", gh("acme", "repo", "datatug", "")},
		{app + "/store/github/project/Repo@Acme@/overview", gh("acme", "repo", "", "", "overview")},
		{app + "/store/github.com/project/repo@acme@dir@dev", gh("acme", "repo", "dir", "dev")},
		{app + "/store/github.com/project/repo@acme@dir@HEAD", gh("acme", "repo", "dir", "")},
		{app + "/store/github.com/project/repo@acme@dir@", gh("acme", "repo", "dir", "")},

		// Other stores.
		{app + "/store/firestore/project/p1/queries", Link{Kind: KindCloud, StoreID: "firestore", ProjectID: "p1", Page: []string{"queries"}}},
		{app + "/space/s1/store/firestore/project/p1/query/q", Link{Kind: KindCloud, StoreID: "firestore", SpaceID: "s1", ProjectID: "p1", Page: []string{"query", "q"}}},
		{app + "/space/s1/store/firestore/project/p1", Link{Kind: KindCloud, StoreID: "firestore", SpaceID: "s1", ProjectID: "p1"}},
		{app + "/store/local/project/mine", Link{Kind: KindBrowser, StoreID: "local", ProjectID: "mine"}},
		{app + "/store/http-localhost:8989/project/demo/environments", Link{Kind: KindAgent, StoreID: "http-localhost:8989", ProjectID: "demo", Page: []string{"environments"}}},
		{app + "/store/localhost:8989/project/demo", Link{Kind: KindAgent, StoreID: "localhost:8989", ProjectID: "demo"}},
		{app + "/store/http-127.0.0.1:9000/project/demo", Link{Kind: KindAgent, StoreID: "http-127.0.0.1:9000", ProjectID: "demo"}},
		{app + "/store/http-example.com:8989/project/demo", Link{Kind: KindOtherStore, StoreID: "http-example.com:8989", ProjectID: "demo"}},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := Parse(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseRefused(t *testing.T) {
	const app = "https://datatug.app"
	for _, in := range []string{
		"",
		"datatug.app/project/github.com/a/b",
		"mailto:me@datatug.app",
		"%zz",
		"https://user:secret@datatug.app/project/github.com/a/b",
		"https://evil.example/project/github.com/a/b",
		"http://datatug.app/project/github.com/a/b",
		"ftp://localhost:21/",
		"http://localhost/",
		"http://192.168.1.2:4200/",
		app + "/project/github.com/-bad/repo",
		app + "/project/github.com/acme/re%2Fpo",
		app + "/project/github.com/acme/..",
		app + "/project/github.com/acme/repo?ref=a&ref=b",
		app + "/project/github.com/acme/repo?ref=",
		app + "/project/github.com/acme/repo?ref=a..b",
		app + "/project/github.com/acme/repo?ref=x.lock",
		app + "/project/github.com/acme/repo?ref=a@b",
		app + "/project/github.com/acme/repo/tree",
		app + "/project/github.com/acme/repo/tree/main?ref=dev",
		app + "/project/github.com/acme/repo/tree/a@b",
		app + "/project/github.com/acme/repo/blob/main/readme.md",
		app + "/project/github.com/acme/repo/blob/main",
		app + "/project/github.com/acme/repo/d@r",
		app + "/project/github.com/acme/repo/%20dir",
		app + "/project/github.com/acme/repo/dir/-/q%00",
		app + "/project/github.com/acme/repo/dir/-/a/%2E%2E",
		app + "/store/%zz/project/p",
		app + "/store/firestore/project/p/a/.",
		app + "/store/GitHub.com/project/repo@acme",
		app + "/store/github.com/project/repo",
		app + "/store/github.com/project/a@b@c@d@e",
		app + "/store/github.com/project/repo@-acme",
		app + "/store/github.com/project/repo@acme@-",
		app + "/store/github.com/project/repo@acme@dir@a..b",
		app + "/space/%20/store/firestore/project/p",
		app + "/space/s/store/firestore/project/p/%zz",
	} {
		t.Run(in, func(t *testing.T) {
			_, err := Parse(in)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v", err)
			}
			if !strings.Contains(err.Error(), Usage) {
				t.Errorf("the message must name the expected form: %v", err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Errorf("the message must not repeat the address: %v", err)
			}
		})
	}
}
