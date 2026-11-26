package demogit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/format/pktline"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
	"github.com/go-git/go-git/v5/plumbing/revlist"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
)

const project = "project"

var (
	mainRef = plumbing.NewBranchReferenceName("main")
	demoRef = plumbing.NewBranchReferenceName("datatug-demo")
)

type gitFixture struct {
	local  *git.Repository
	remote *git.Repository
	path   string
	cfg    Config
	url    string
	base   plumbing.Hash
}

func installFileTransport(t *testing.T, replacement transport.Transport) {
	t.Helper()
	previous := client.Protocols["file"]
	client.InstallProtocol("file", replacement)
	t.Cleanup(func() { client.InstallProtocol("file", previous) })
}

// The in-process server supplies the advertised refs and handles pushes.
// Its upload implementation lacks shallow support, so serve single-tip packs
// for the shallow fetches against these bare repositories.
type shallowFixtureTransport struct {
	transport.Transport
	repo     *git.Repository
	wrapPack func(*bytes.Buffer) io.Reader
}

func (t shallowFixtureTransport) NewUploadPackSession(ep *transport.Endpoint, auth transport.AuthMethod) (transport.UploadPackSession, error) {
	session, err := t.Transport.NewUploadPackSession(ep, auth)
	if err != nil {
		return nil, err
	}
	return shallowFixtureSession{session, t.repo, t.wrapPack}, nil
}

type shallowFixtureSession struct {
	transport.UploadPackSession
	repo     *git.Repository
	wrapPack func(*bytes.Buffer) io.Reader
}

func (s shallowFixtureSession) UploadPack(ctx context.Context, req *packp.UploadPackRequest) (*packp.UploadPackResponse, error) {
	if req.Depth == packp.DepthCommits(0) {
		return s.UploadPackSession.UploadPack(ctx, req)
	}
	if req.Depth != packp.DepthCommits(1) || !req.Capabilities.Supports(capability.Shallow) || len(req.Wants) != 1 {
		return nil, fmt.Errorf("unexpected shallow fetch depth or wants: %v, %v", req.Depth, req.Wants)
	}
	tip, err := s.repo.CommitObject(req.Wants[0])
	if err != nil {
		return nil, err
	}
	objects, err := revlist.Objects(s.repo.Storer, []plumbing.Hash{tip.TreeHash}, nil)
	if err != nil {
		return nil, err
	}
	var pack bytes.Buffer
	if _, err := packfile.NewEncoder(&pack, s.repo.Storer, false).Encode(append(objects, tip.Hash), 0); err != nil {
		return nil, err
	}
	var reader io.Reader = &pack
	if s.wrapPack != nil {
		reader = s.wrapPack(&pack)
	}
	response := packp.NewUploadPackResponseWithPackfile(req, io.NopCloser(reader))
	if len(tip.ParentHashes) > 0 {
		response.Shallows = []plumbing.Hash{tip.Hash}
	}
	return response, nil
}

func newGitFixture(t *testing.T) gitFixture {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "local")
	local := initRepo(t, path, false)
	writeFile(t, path, project+"/datatug-project.json", `{}`)
	writeFile(t, path, ".gitignore", "ignored.txt\n")
	writeFile(t, path, "delete.txt", "remove me")
	writeFile(t, path, "modify.txt", "before")
	base := commitAll(t, local, "base")
	remotePath := filepath.Join(root, "alice", "demo.git")
	remote := initRepo(t, remotePath, true)
	installFileTransport(t, shallowFixtureTransport{Transport: server.DefaultServer, repo: remote})
	baseURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(root) + "/"}).String()
	remoteURL := baseURL + aliceFork.FullName() + ".git"
	pushToFixture(t, local, remoteURL, base, mainRef)
	return gitFixture{local: local, remote: remote, path: path, cfg: Config{GitBaseURL: baseURL}, url: remoteURL, base: base}
}

func initRepo(t *testing.T, path string, bare bool) *git.Repository {
	t.Helper()
	r, err := git.PlainInitWithOptions(path, &git.PlainInitOptions{Bare: bare, InitOptions: git.InitOptions{DefaultBranch: mainRef}})
	must(t, err)
	return r
}

func commitAll(t *testing.T, repo *git.Repository, message string) plumbing.Hash {
	t.Helper()
	wt, err := repo.Worktree()
	must(t, err)
	must(t, wt.AddWithOptions(&git.AddOptions{All: true}))
	sha, err := wt.Commit(message, &git.CommitOptions{Author: &object.Signature{Name: "fixture", Email: "fixture@example.org", When: testTime}})
	must(t, err)
	return sha
}

func pushToFixture(t *testing.T, repo *git.Repository, url string, sha plumbing.Hash, branch plumbing.ReferenceName) {
	t.Helper()
	remote := git.NewRemote(repo.Storer, &config.RemoteConfig{Name: "fixture", URLs: []string{url}})
	must(t, remote.Push(&git.PushOptions{RemoteName: "fixture", RefSpecs: []config.RefSpec{config.RefSpec(sha.String() + ":" + branch.String())}}))
}

func writeFile(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, path)
	must(t, os.MkdirAll(filepath.Dir(full), 0o700))
	must(t, os.WriteFile(full, []byte(content), 0o600))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func assertRef(t *testing.T, repo *git.Repository, ref plumbing.ReferenceName, want plumbing.Hash) {
	t.Helper()
	got, err := repo.Reference(ref, true)
	must(t, err)
	if got.Hash() != want {
		t.Fatalf("%s = %s, want %s", ref, got.Hash(), want)
	}
}

func TestSaveNewBranch(t *testing.T) {
	f := newGitFixture(t)
	c := testAPI(t, f.cfg, userReply(), forkReply(), forkReply())
	writeFile(t, f.path, "new.txt", "hello")
	writeFile(t, f.path, "modify.txt", "after")
	writeFile(t, f.path, "ignored.txt", "do not commit")
	must(t, os.Remove(filepath.Join(f.path, "delete.txt")))
	result, err := c.Save(context.Background(), f.path, aliceFork, SaveOptions{})
	must(t, err)
	sha := plumbing.NewHash(result.CommitSHA)
	if result.Fork != aliceFork || result.Branch != demoRef.Short() || result.CommitURL != f.cfg.GitBaseURL+"alice/demo/commit/"+sha.String() {
		t.Fatalf("unexpected save result: %+v", result)
	}
	assertRef(t, f.remote, demoRef, sha)
	assertRef(t, f.remote, mainRef, f.base)
	assertRef(t, f.local, plumbing.HEAD, sha)
	commit, err := f.remote.CommitObject(sha)
	must(t, err)
	if commit.Author.Name != "alice" || commit.Author.Email != "42+alice@users.noreply.github.com" || !commit.Author.When.Equal(testTime) ||
		commit.Committer.Name != "DataTug demo" || commit.Message != "Save DataTug demo workspace" || !reflect.DeepEqual(commit.ParentHashes, []plumbing.Hash{f.base}) {
		t.Fatalf("unexpected commit: %v", commit)
	}
	for _, name := range []string{"ignored.txt", "delete.txt"} {
		if _, err := commit.File(name); !errors.Is(err, object.ErrFileNotFound) {
			t.Fatalf("commit includes %s or unreadable: %v", name, err)
		}
	}
	for name, want := range map[string]string{"new.txt": "hello", "modify.txt": "after"} {
		file, err := commit.File(name)
		must(t, err)
		contents, err := file.Contents()
		must(t, err)
		if contents != want {
			t.Fatalf("%s = %q, want %q", name, contents, want)
		}
	}
	configBytes, err := os.ReadFile(filepath.Join(f.path, ".git", "config"))
	must(t, err)
	assertNoToken(t, string(configBytes))
	assertNoToken(t, commit.String())
	remotes, err := f.local.Remotes()
	must(t, err)
	if len(remotes) != 0 {
		t.Fatalf("Save persisted remotes: %v", remotes)
	}
	changes, err := Changes(f.path)
	must(t, err)
	if len(changes) != 0 {
		t.Fatalf("successful save left changes: %v", changes)
	}
}

func TestSaveFastForward(t *testing.T) {
	for _, atBase := range []bool{true, false} {
		t.Run(map[bool]string{true: "at-base", false: "ancestor"}[atBase], func(t *testing.T) {
			f := newGitFixture(t)
			pushToFixture(t, f.local, f.url, f.base, demoRef)
			base := f.base
			if !atBase {
				writeFile(t, f.path, "local.txt", "an earlier change")
				base = commitAll(t, f.local, "ahead of the fork")
			}
			writeFile(t, f.path, "new.txt", "more changes")
			c := testAPI(t, f.cfg, userReply(), forkReply(), forkReply())
			result, err := c.Save(context.Background(), f.path, aliceFork, SaveOptions{Message: "custom"})
			must(t, err)
			sha := plumbing.NewHash(result.CommitSHA)
			assertRef(t, f.remote, demoRef, sha)
			commit, err := f.remote.CommitObject(sha)
			must(t, err)
			if commit.Message != "custom" || commit.ParentHashes[0] != base {
				t.Fatalf("unexpected commit: %v", commit)
			}
		})
	}
}

func TestSaveDivergedThenNewBranch(t *testing.T) {
	f := newGitFixture(t)
	ctx := context.Background()
	otherPath := filepath.Join(t.TempDir(), "other")
	other, err := git.PlainClone(otherPath, false, &git.CloneOptions{URL: f.url})
	must(t, err)
	writeFile(t, otherPath, "other.txt", "different session's edit")
	tip := commitAll(t, other, "other visit")
	pushToFixture(t, other, f.url, tip, demoRef)
	writeFile(t, f.path, "new.txt", "my edit")
	c := testAPI(t, f.cfg, userReply(), forkReply(), userReply(), forkReply(), forkReply())
	if _, err := c.Save(ctx, f.path, aliceFork, SaveOptions{}); !errors.Is(err, ErrDiverged) {
		t.Fatalf("save diverged branch: %v", err)
	}
	assertRef(t, f.local, plumbing.HEAD, f.base)
	assertRef(t, f.remote, demoRef, tip)
	newName := c.NewBranchName()
	if newName != "datatug-demo-20261126-020506" {
		t.Fatalf("new branch name = %q", newName)
	}
	result, err := c.Save(ctx, f.path, aliceFork, SaveOptions{Branch: newName})
	must(t, err)
	assertRef(t, f.remote, plumbing.NewBranchReferenceName(newName), plumbing.NewHash(result.CommitSHA))
	assertRef(t, f.remote, demoRef, tip)
}

func TestSavePushRace(t *testing.T) {
	f := newGitFixture(t)
	pushToFixture(t, f.local, f.url, f.base, demoRef)
	writeFile(t, f.path, "other.txt", "arrived during the push")
	tip := commitAll(t, f.local, "new remote tip")
	pushToFixture(t, f.local, f.url, tip, plumbing.NewBranchReferenceName("other"))
	wt, err := f.local.Worktree()
	must(t, err)
	must(t, wt.Reset(&git.ResetOptions{Commit: f.base, Mode: git.HardReset}))
	writeFile(t, f.path, "new.txt", "my edit")
	installFileTransport(t, raceTransport{Transport: server.DefaultServer, beforePush: func() error {
		return f.remote.Storer.SetReference(plumbing.NewHashReference(demoRef, tip))
	}})
	c := testAPI(t, f.cfg, userReply(), forkReply(), forkReply(), userReply(), forkReply(), forkReply())
	if _, err := c.Save(context.Background(), f.path, aliceFork, SaveOptions{}); !errors.Is(err, ErrDiverged) {
		t.Fatalf("racing save error = %v", err)
	}
	assertRef(t, f.local, plumbing.HEAD, f.base)
	assertRef(t, f.remote, demoRef, tip)
	_, err = c.Save(context.Background(), f.path, aliceFork, SaveOptions{Branch: c.NewBranchName()})
	must(t, err)
}

func TestSaveRevalidatesForkBeforePushAndRetry(t *testing.T) {
	transferred := reply("GET", forkPath, 200, `{"full_name":"org/demo","owner":{"login":"org"},"fork":true,"parent":{"full_name":"team/demo"}}`)
	for _, retry := range []bool{false, true} {
		t.Run(fmt.Sprintf("retry=%v", retry), func(t *testing.T) {
			f := newGitFixture(t)
			replies := []apiReply{userReply(), forkReply(), transferred}
			if retry {
				other, err := git.PlainClone(filepath.Join(t.TempDir(), "other"), false, &git.CloneOptions{URL: f.url})
				must(t, err)
				wt, err := other.Worktree()
				must(t, err)
				file, err := wt.Filesystem.Create("other.txt")
				must(t, err)
				must(t, file.Close())
				pushToFixture(t, other, f.url, commitAll(t, other, "other visit"), demoRef)
				replies = []apiReply{userReply(), forkReply(), userReply(), transferred}
			}
			writeFile(t, f.path, "edit.txt", "visitor edit")
			c := testAPI(t, f.cfg, replies...)
			uploads := 0
			installFileTransport(t, uploadHookTransport{Transport: client.Protocols["file"], onOpen: func() { uploads++ }})
			branch := c.NewBranchName()
			if retry {
				if _, err := c.Save(context.Background(), f.path, aliceFork, SaveOptions{}); !errors.Is(err, ErrDiverged) {
					t.Fatalf("initial save = %v, want ErrDiverged", err)
				}
			}
			if _, err := c.Save(context.Background(), f.path, aliceFork, SaveOptions{Branch: branch}); !errors.Is(err, ErrNoFork) {
				t.Fatalf("save after transfer = %v, want ErrNoFork", err)
			}
			if uploads != 1 {
				t.Fatalf("opened %d Git upload sessions, want 1", uploads)
			}
			assertRef(t, f.local, plumbing.HEAD, f.base)
			if _, err := f.remote.Reference(plumbing.NewBranchReferenceName(branch), false); !errors.Is(err, plumbing.ErrReferenceNotFound) {
				t.Fatalf("alternate branch after transfer = %v, want no branch", err)
			}
		})
	}
}

type raceTransport struct {
	transport.Transport
	beforePush func() error
}

func (t raceTransport) NewReceivePackSession(ep *transport.Endpoint, auth transport.AuthMethod) (transport.ReceivePackSession, error) {
	if err := t.beforePush(); err != nil {
		return nil, err
	}
	return t.Transport.NewReceivePackSession(ep, auth)
}

func TestSaveNothing(t *testing.T) {
	f := newGitFixture(t)
	writeFile(t, f.path, "ignored.txt", "ignored")
	c := testAPI(t, f.cfg)
	if _, err := c.Save(context.Background(), f.path, aliceFork, SaveOptions{}); !errors.Is(err, ErrNothingToSave) {
		t.Fatalf("save clean workspace: %v", err)
	}
}

func TestSaveHTTPPushAuth(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newGitFixture(t)
			writeFile(t, f.path, "new.txt", "my edit")
			srv := testHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/user":
					fmt.Fprint(w, userJSON)
					return
				case forkPath:
					fmt.Fprint(w, forkJSON)
					return
				}
				user, pass, ok := r.BasicAuth()
				if !ok || user != gitAuthUser || pass != testToken || r.URL.User != nil || r.URL.Path != "/alice/demo.git/info/refs" {
					t.Errorf("unexpected Git request: %s", r.URL.Path)
				}
				switch r.URL.Query().Get("service") {
				case "git-upload-pack":
					w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
					if err := advertiseGitRefs(w, f.base, "git-upload-pack"); err != nil {
						t.Errorf("advertise Git refs: %v", err)
					}
				case "git-receive-pack":
					http.Error(w, "denied "+r.Header.Get("Authorization")+" "+testToken, status)
				default:
					t.Errorf("unexpected Git service: %s", r.URL.RawQuery)
					w.WriteHeader(500)
				}
			}))
			c := New(Config{Canonical: canonical, APIBaseURL: srv.URL, GitBaseURL: srv.URL, HTTPClient: srv.Client()}, testToken)
			if _, err := c.Save(context.Background(), f.path, aliceFork, SaveOptions{}); !errors.Is(err, ErrAuth) || !strings.Contains(err.Error(), "push workspace") {
				t.Fatalf("save with refused Git credentials: %v", err)
			} else {
				assertNoToken(t, err.Error())
			}
			assertRef(t, f.local, plumbing.HEAD, f.base)
			configBytes, err := os.ReadFile(filepath.Join(f.path, ".git", "config"))
			must(t, err)
			assertNoToken(t, string(configBytes))
		})
	}
}

func TestSaveRejectsPushRedirect(t *testing.T) {
	f := newGitFixture(t)
	writeFile(t, f.path, "edit.txt", "visitor edit")
	var posted atomic.Bool
	srv := testHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			fmt.Fprint(w, userJSON)
		case forkPath:
			fmt.Fprint(w, forkJSON)
		case "/alice/demo.git/info/refs":
			service := r.URL.Query().Get("service")
			w.Header().Set("Content-Type", "application/x-"+service+"-advertisement")
			if err := advertiseGitRefs(w, f.base, service); err != nil {
				t.Errorf("advertise Git refs: %v", err)
			}
		case "/alice/demo.git/git-receive-pack":
			posted.Store(r.Method == http.MethodPost)
			http.Redirect(w, r, "/org/demo.git/git-receive-pack", http.StatusTemporaryRedirect)
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	c := New(Config{Canonical: canonical, APIBaseURL: srv.URL, GitBaseURL: srv.URL, HTTPClient: srv.Client()}, testToken)
	_, err := c.Save(context.Background(), f.path, aliceFork, SaveOptions{})
	if !errors.Is(err, ErrGitRedirect) || !errors.Is(err, ErrNoFork) || !posted.Load() {
		t.Fatalf("Save() = %v, posted = %v, want push rejected with ErrGitRedirect and ErrNoFork", err, posted.Load())
	}
	assertRef(t, f.local, plumbing.HEAD, f.base)
}

func advertiseGitRefs(w io.Writer, base plumbing.Hash, service string) error {
	enc := pktline.NewEncoder(w)
	if err := enc.EncodeString("# service=" + service + "\n"); err != nil {
		return err
	}
	if err := enc.Flush(); err != nil {
		return err
	}
	adv := packp.NewAdvRefs()
	adv.Head = &base
	adv.References[mainRef.String()] = base
	if err := adv.Capabilities.Set(capability.SymRef, "HEAD:refs/heads/main"); err != nil {
		return err
	}
	return adv.Encode(w)
}

func TestOpen(t *testing.T) {
	t.Run("saved-branch", func(t *testing.T) {
		f := newGitFixture(t)
		writeFile(t, f.path, project+"/saved.sql", "select 1;")
		c := testAPI(t, f.cfg, userReply(), forkReply(), forkReply(), userReply(), forkReply())
		result, err := c.Save(context.Background(), f.path, aliceFork, SaveOptions{})
		must(t, err)
		path := filepath.Join(t.TempDir(), "reopened")
		branch, err := c.Open(context.Background(), aliceFork, path, project)
		must(t, err)
		if branch != result.Branch {
			t.Fatalf("opened branch %q, want %q", branch, result.Branch)
		}
		contents, err := os.ReadFile(filepath.Join(path, project, "saved.sql"))
		must(t, err)
		if string(contents) != "select 1;" {
			t.Fatalf("saved file = %s", contents)
		}
		opened, err := git.PlainOpen(path)
		must(t, err)
		sha := plumbing.NewHash(result.CommitSHA)
		assertRef(t, opened, plumbing.HEAD, sha)
		shallow, err := opened.Storer.Shallow()
		must(t, err)
		if !reflect.DeepEqual(shallow, []plumbing.Hash{sha}) {
			t.Fatalf("shallow boundary = %v, want [%s]", shallow, sha)
		}
		if _, err := opened.CommitObject(f.base); !errors.Is(err, plumbing.ErrObjectNotFound) {
			t.Fatalf("depth-one reopen included base commit: %v", err)
		}
		writeFile(t, path, "next.txt", "another visit")
		next := testAPI(t, f.cfg, userReply(), forkReply(), forkReply())
		resaved, err := next.Save(context.Background(), path, aliceFork, SaveOptions{})
		must(t, err)
		assertRef(t, f.remote, demoRef, plumbing.NewHash(resaved.CommitSHA))
	})
	t.Run("fallback-default-branch", func(t *testing.T) {
		f := newGitFixture(t)
		c := testAPI(t, f.cfg, userReply(), forkReply(), forkReply())
		path := t.TempDir()
		if branch, err := c.Open(context.Background(), aliceFork, path, project); err != nil || branch != mainRef.Short() {
			t.Fatalf("open default branch = %q, %v", branch, err)
		}
	})
	t.Run("nonempty-destination", func(t *testing.T) {
		path := t.TempDir()
		writeFile(t, path, "keep", "untouched")
		c := New(Config{}, testToken)
		if _, err := c.Open(context.Background(), aliceFork, path, project); err == nil || !strings.Contains(err.Error(), "not empty") {
			t.Fatalf("open nonempty destination: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(path, "keep"))
		must(t, err)
		if string(got) != "untouched" {
			t.Fatalf("replaced existing file: %q", got)
		}
	})
}

func TestOpenRefusesUntrustedContent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		prepare  func(*testing.T, string)
		limits   CloneLimits
		contains string
		existing bool
	}{
		{"escaping-symlink", func(t *testing.T, path string) { must(t, os.Symlink("../outside", filepath.Join(path, "escape"))) }, CloneLimits{}, "escape", false},
		{"absolute-symlink", func(t *testing.T, path string) { must(t, os.Symlink("/outside", filepath.Join(path, "absolute"))) }, CloneLimits{}, "absolute", true},
		{"too-many-files", nil, CloneLimits{MaxFiles: 1}, "file limit", false},
		{"too-large", nil, CloneLimits{MaxBytes: 1}, "size limit", false},
		{"missing-project", func(t *testing.T, path string) { must(t, os.RemoveAll(filepath.Join(path, project))) }, CloneLimits{}, project + "/datatug-project.json", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGitFixture(t)
			sha := f.base
			if tc.prepare != nil {
				tc.prepare(t, f.path)
				sha = commitAll(t, f.local, tc.name)
			}
			pushToFixture(t, f.local, f.url, sha, demoRef)
			f.cfg.Limits = tc.limits
			c := testAPI(t, f.cfg, userReply(), forkReply())
			path := t.TempDir()
			if !tc.existing {
				path = filepath.Join(path, "reopened")
			}
			if _, err := c.Open(context.Background(), aliceFork, path, project); !errors.Is(err, ErrUntrustedContent) || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("open unsafe workspace: %v, want %q", err, tc.contains)
			}
			if tc.existing {
				contents, err := os.ReadDir(path)
				if err != nil || len(contents) != 0 {
					t.Fatalf("existing destination after refusal: %v, %v", contents, err)
				}
			} else if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("clone survived refusal: %v", err)
			}
		})
	}
}

func TestOpenRevalidatesForkBeforeClone(t *testing.T) {
	t.Run("invalid API configuration", func(t *testing.T) {
		c := testAPI(t, Config{})
		c.cfg.APIBaseURL = "%"
		if _, err := c.Open(context.Background(), aliceFork, filepath.Join(t.TempDir(), "open"), project); err == nil || !strings.Contains(err.Error(), "configure GitHub API") {
			t.Fatalf("Open() = %v, want API configuration error", err)
		}
	})
	for _, tc := range []struct {
		name    string
		replies []apiReply
		want    error
	}{
		{"user denied", []apiReply{reply("GET", "/user", 401, "{}")}, ErrAuth},
		{"deleted", []apiReply{userReply(), reply("GET", forkPath, 404, "{}")}, ErrNoFork},
		{"renamed", []apiReply{userReply(), reply("GET", forkPath, 200, `{"full_name":"alice/renamed","owner":{"login":"alice"},"fork":true,"parent":{"full_name":"team/demo"}}`)}, ErrNoFork},
		{"detached", []apiReply{userReply(), reply("GET", forkPath, 200, `{"full_name":"alice/demo","owner":{"login":"alice"},"fork":false}`)}, ErrNoFork},
		{"api unavailable", []apiReply{userReply(), reply("GET", forkPath, 403, "{}")}, ErrAuth},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGitFixture(t)
			installFileTransport(t, uploadHookTransport{Transport: client.Protocols["file"], onOpen: func() {
				t.Error("opened a Git upload session after failed revalidation")
			}})
			c := testAPI(t, f.cfg, tc.replies...)
			root := t.TempDir()
			if _, err := c.Open(context.Background(), aliceFork, filepath.Join(root, "open"), project); !errors.Is(err, tc.want) {
				t.Fatalf("Open() = %v, want %v", err, tc.want)
			}
			entries, err := os.ReadDir(root)
			must(t, err)
			if len(entries) != 0 {
				t.Fatalf("failed clone left files: %v", entries)
			}
		})
	}
	t.Run("before default branch fallback", func(t *testing.T) {
		f := newGitFixture(t)
		c := testAPI(t, f.cfg, userReply(), forkReply(), reply("GET", forkPath, 404, "{}"))
		uploads := 0
		installFileTransport(t, uploadHookTransport{Transport: client.Protocols["file"], onOpen: func() { uploads++ }})
		if _, err := c.Open(context.Background(), aliceFork, filepath.Join(t.TempDir(), "open"), project); !errors.Is(err, ErrNoFork) || uploads != 1 {
			t.Fatalf("fallback Open() = %v, Git sessions = %d, want ErrNoFork after one session", err, uploads)
		}
	})
}

func TestOpenCannotResetMetadataForFallback(t *testing.T) {
	requireNonroot(t)
	f := newGitFixture(t)
	root := t.TempDir()
	var metadata string
	t.Cleanup(func() { must(t, os.Chmod(metadata, 0o700)) })
	installFileTransport(t, uploadHookTransport{Transport: client.Protocols["file"], onOpen: func() {
		metadataDirs, err := filepath.Glob(filepath.Join(root, ".datatug-demo-*", ".git"))
		must(t, err)
		if len(metadataDirs) != 1 {
			t.Fatalf("clone metadata = %v", metadataDirs)
		}
		metadata = metadataDirs[0]
		must(t, os.Chmod(metadata, 0o500))
	}})
	c := testAPI(t, f.cfg, userReply(), forkReply())
	if _, err := c.Open(context.Background(), aliceFork, filepath.Join(root, "open"), project); !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), "reset metadata") {
		t.Fatalf("Open() = %v, want permission error resetting clone metadata", err)
	}
}

type countingPackReader struct {
	io.Reader
	bytesRead int
}

func (r *countingPackReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytesRead += n
	return n, err
}

func TestOpenStopsOversizedPackDuringFetch(t *testing.T) {
	f := newGitFixture(t)
	data := make([]byte, 512<<10)
	_, err := rand.New(rand.NewSource(1)).Read(data)
	must(t, err)
	must(t, os.WriteFile(filepath.Join(f.path, "sample.bin"), data, 0o600))
	pushToFixture(t, f.local, f.url, commitAll(t, f.local, "incompressible sample"), demoRef)

	var reader *countingPackReader
	var packSize int
	installFileTransport(t, shallowFixtureTransport{Transport: server.DefaultServer, repo: f.remote, wrapPack: func(pack *bytes.Buffer) io.Reader {
		packSize = pack.Len()
		reader = &countingPackReader{Reader: pack}
		return reader
	}})
	f.cfg.Limits.MaxBytes = 64 << 10
	c := testAPI(t, f.cfg, userReply(), forkReply())
	root := t.TempDir()
	_, err = c.Open(context.Background(), aliceFork, filepath.Join(root, "reopened"), project)
	if !errors.Is(err, ErrUntrustedContent) || !strings.Contains(err.Error(), "while writing clone metadata") {
		t.Fatalf("Open() = %v, want clone metadata budget error", err)
	}
	if reader == nil {
		t.Fatal("no pack reader was created")
	}
	if reader.bytesRead == 0 || reader.bytesRead >= packSize/2 {
		t.Fatalf("pack transfer read %d of %d bytes, want an early abort", reader.bytesRead, packSize)
	}
	entries, err := os.ReadDir(root)
	must(t, err)
	if len(entries) != 0 {
		t.Fatalf("partial clone remains: %v", entries)
	}
}

func TestOpenCanceledDuringFetch(t *testing.T) {
	for _, branch := range []string{"main", "fallback"} {
		t.Run(branch, func(t *testing.T) {
			f := newGitFixture(t)
			f.cfg.Branch = branch
			root := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			opens := 0
			installFileTransport(t, uploadHookTransport{Transport: client.Protocols["file"], onOpen: func() {
				opens++
				if branch == "main" || opens == 2 {
					cancel()
				}
			}})
			replies := []apiReply{userReply(), forkReply()}
			if branch == "fallback" {
				replies = append(replies, forkReply())
			}
			_, err := testAPI(t, f.cfg, replies...).Open(ctx, aliceFork, filepath.Join(root, "repo"), project)
			if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUntrustedContent) {
				t.Fatalf("Open() = %v", err)
			}
			entries, err := os.ReadDir(root)
			must(t, err)
			if len(entries) != 0 {
				t.Fatalf("canceled checkout remained: %v", entries)
			}
		})
	}
}

func TestOpenCanceledDuringAndAfterCheckout(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		afterWrite bool
	}{
		{"during", ".git/index", false},
		{"after", project + "/datatug-project.json", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGitFixture(t)
			f.cfg.Branch = "main"
			root := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			hooked := hookContext{Context: ctx, onCheck: func() {
				paths, err := filepath.Glob(filepath.Join(root, ".datatug-demo-*", tc.path))
				must(t, err)
				if len(paths) == 1 {
					info, err := os.Stat(paths[0])
					must(t, err)
					if !tc.afterWrite || info.Size() > 0 {
						cancel()
					}
				}
			}}
			_, err := testAPI(t, f.cfg, userReply(), forkReply()).Open(hooked, aliceFork, filepath.Join(root, "repo"), project)
			if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUntrustedContent) {
				t.Fatalf("Open() = %v", err)
			}
			if inCheckout := strings.Contains(err.Error(), "check out cloned workspace"); inCheckout == tc.afterWrite {
				t.Fatalf("Open() returned from wrong phase: %v", err)
			}
			entries, err := os.ReadDir(root)
			must(t, err)
			if len(entries) != 0 {
				t.Fatalf("canceled checkout remained: %v", entries)
			}
		})
	}
}

func TestCheckoutFilesystemCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs := checkoutFilesystem{Filesystem: memfs.New(), ctx: ctx}
	if _, err := fs.OpenFile("missing", os.O_RDONLY, 0); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("open missing file: %v", err)
	}
	file, err := fs.OpenFile("open", os.O_CREATE|os.O_WRONLY, 0o600)
	must(t, err)
	defer file.Close()
	cancel()
	if _, err := fs.OpenFile("new", os.O_CREATE|os.O_WRONLY, 0o600); !errors.Is(err, context.Canceled) {
		t.Fatalf("open after cancel: %v", err)
	}
	if _, err := file.Write([]byte("data")); !errors.Is(err, context.Canceled) {
		t.Fatalf("write after cancel: %v", err)
	}
	if err := fs.Symlink("open", "link"); !errors.Is(err, context.Canceled) {
		t.Fatalf("symlink after cancel: %v", err)
	}
}

func TestSaveRefusals(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := New(Config{Canonical: canonical}, testToken)
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		fork    Repo
		branch  string
		prepare func(*testing.T, gitFixture) string
		message string
	}{
		{"canceled", ctx, aliceFork, "", nil, "context canceled"},
		{"canonical", context.Background(), Repo{"TEAM", "demo"}, "", nil, "canonical demo repository"},
		{"invalid-branch", context.Background(), aliceFork, "invalid branch", nil, "invalid save branch"},
		{"not-a-repo", context.Background(), aliceFork, "", func(t *testing.T, _ gitFixture) string { return t.TempDir() }, "open workspace repository"},
		{"unborn", context.Background(), aliceFork, "", func(t *testing.T, _ gitFixture) string {
			path := t.TempDir()
			initRepo(t, path, false)
			return path
		}, "read workspace HEAD"},
		{"bare", context.Background(), aliceFork, "", func(t *testing.T, f gitFixture) string {
			remoteURL, err := url.Parse(f.url)
			must(t, err)
			return remoteURL.Path
		}, "open workspace worktree"},
		{"bad-index", context.Background(), aliceFork, "", func(t *testing.T, f gitFixture) string {
			writeFile(t, f.path, ".git/index", "invalid index")
			return f.path
		}, "read workspace status"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var path string
			if tc.prepare != nil {
				path = tc.prepare(t, newGitFixture(t))
			}
			if _, err := c.Save(tc.ctx, path, tc.fork, SaveOptions{Branch: tc.branch}); err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("Save() = %v, want %q", err, tc.message)
			}
		})
	}
	for _, tc := range []struct {
		name    string
		config  func(c *Client)
		replies []apiReply
		branch  string
		message string
	}{
		{"invalid-api", func(c *Client) { c.cfg.APIBaseURL = "%" }, nil, "", "configure GitHub API"},
		{"user-denied", nil, []apiReply{reply("GET", "/user", 401, `{}`)}, "", ErrAuth.Error()},
		{"missing-id", nil, []apiReply{reply("GET", "/user", 200, `{"login":"alice"}`)}, "", "no account ID"},
		{"default-branch", nil, []apiReply{userReply(), forkReply()}, "main", "fork's default branch"},
		{"remote-unavailable", func(c *Client) { c.cfg.GitBaseURL += "missing/" }, []apiReply{userReply(), forkReply()}, "", "list Git refs on fork"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGitFixture(t)
			writeFile(t, f.path, "new", "new")
			c := testAPI(t, f.cfg, tc.replies...)
			if tc.config != nil {
				tc.config(c)
			}
			if _, err := c.Save(context.Background(), f.path, aliceFork, SaveOptions{Branch: tc.branch}); err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("Save() = %v, want %q", err, tc.message)
			}
			assertRef(t, f.remote, mainRef, f.base)
		})
	}
}

func TestSaveDivergedWithKnownRemoteCommit(t *testing.T) {
	f := newGitFixture(t)
	writeFile(t, f.path, "other.txt", "fork history")
	tip := commitAll(t, f.local, "different history")
	pushToFixture(t, f.local, f.url, tip, demoRef)
	wt, err := f.local.Worktree()
	must(t, err)
	must(t, wt.Reset(&git.ResetOptions{Commit: f.base, Mode: git.HardReset}))
	writeFile(t, f.path, "edit.txt", "my edit")
	c := testAPI(t, f.cfg, userReply(), forkReply())
	if _, err := c.Save(context.Background(), f.path, aliceFork, SaveOptions{}); !errors.Is(err, ErrDiverged) {
		t.Fatalf("Save() = %v", err)
	}
	assertRef(t, f.remote, demoRef, tip)
}

func TestOpenHTTPRedirectsAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		to      string
		check   func(*http.Request, []*http.Request) error
		wantErr error
		message string
	}{
		{"auth", 403, "", nil, ErrAuth, "clone workspace"},
		{"cross-origin", 301, "http://elsewhere.invalid/alice/demo.git/info/refs", nil, ErrGitRedirect, "changes repository path or origin"},
		{"different-owner", 301, "/org/demo.git/info/refs", nil, ErrGitRedirect, "changes repository path or origin"},
		{"different-repo", 301, "/alice/moved.git/info/refs", nil, ErrGitRedirect, "changes repository path or origin"},
		{"different-service", 301, "/alice/demo.git/git-upload-pack", nil, ErrGitRedirect, "changes repository path or origin"},
		{"encoded-separator", 301, "/alice%2Fdemo.git/info/refs", nil, ErrGitRedirect, "changes repository path or origin"},
		{"same-repo", 301, "/ALICE/DEMO/info/refs", nil, nil, "status code: 500"},
		{"configured-redirect-policy", 301, "/ALICE/DEMO/info/refs", func(*http.Request, []*http.Request) error {
			return errors.New("configured client refused the redirect")
		}, nil, "configured client refused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := testHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.EscapedPath() {
				case "/user":
					fmt.Fprint(w, userJSON)
					return
				case forkPath:
					fmt.Fprint(w, forkJSON)
					return
				case "/ALICE/DEMO/info/refs":
					if tc.name != "same-repo" {
						t.Errorf("unexpected redirected request to %s", r.URL.Path)
					}
					http.Error(w, testToken, 500)
					return
				}
				if r.URL.EscapedPath() != "/alice/demo.git/info/refs" || r.Host != "127.0.0.1" {
					t.Errorf("unexpected request to %s%s", r.Host, r.URL.Path)
					w.WriteHeader(500)
					return
				}
				if tc.to != "" {
					http.Redirect(w, r, tc.to, tc.status)
					return
				}
				http.Error(w, testToken, tc.status)
			}))
			srv.Client().CheckRedirect = tc.check
			c := New(Config{Canonical: canonical, APIBaseURL: srv.URL, GitBaseURL: srv.URL, HTTPClient: srv.Client()}, testToken)
			parent := t.TempDir()
			path := filepath.Join(parent, "repo")
			_, err := c.Open(context.Background(), aliceFork, path, project)
			if err == nil || (tc.wantErr != nil && !errors.Is(err, tc.wantErr)) || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("Open() = %v, want %q", err, tc.message)
			}
			if tc.wantErr == ErrGitRedirect && !errors.Is(err, ErrNoFork) {
				t.Fatalf("Open() = %v, want ErrNoFork as well", err)
			}
			assertNoToken(t, err.Error())
			entries, err := os.ReadDir(parent)
			must(t, err)
			if len(entries) != 0 {
				t.Fatalf("partial clone remains: %v", entries)
			}
		})
	}
	adapter := client.Protocols["http"].(gitHTTPTransport)
	backend, auth := adapter.sessionTransport(nil)
	if backend != adapter.Transport || auth != nil {
		t.Fatal("ordinary Git callers must keep the original transport")
	}
}

type uploadHookTransport struct {
	transport.Transport
	onOpen func()
}

func (t uploadHookTransport) NewUploadPackSession(ep *transport.Endpoint, auth transport.AuthMethod) (transport.UploadPackSession, error) {
	t.onOpen()
	return t.Transport.NewUploadPackSession(ep, auth)
}

func TestSaveAfterPreflightErrors(t *testing.T) {
	t.Run("context-canceled-before-staging", func(t *testing.T) {
		f := newGitFixture(t)
		writeFile(t, f.path, "new", "new")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		installFileTransport(t, uploadHookTransport{Transport: client.Protocols["file"], onOpen: cancel})
		c := testAPI(t, f.cfg, userReply(), forkReply())
		if _, err := c.Save(ctx, f.path, aliceFork, SaveOptions{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("Save() = %v", err)
		}
		assertRef(t, f.local, plumbing.HEAD, f.base)
	})
	t.Run("lost-change-before-staging", func(t *testing.T) {
		f := newGitFixture(t)
		writeFile(t, f.path, "new", "new")
		installFileTransport(t, uploadHookTransport{Transport: client.Protocols["file"], onOpen: func() {
			must(t, os.Remove(filepath.Join(f.path, "new")))
		}})
		c := testAPI(t, f.cfg, userReply(), forkReply())
		if _, err := c.Save(context.Background(), f.path, aliceFork, SaveOptions{}); !errors.Is(err, ErrNothingToSave) {
			t.Fatalf("Save() = %v", err)
		}
	})
	t.Run("index-write-error", func(t *testing.T) {
		f := newGitFixture(t)
		writeFile(t, f.path, "new", "new")
		installFileTransport(t, uploadHookTransport{Transport: client.Protocols["file"], onOpen: func() {
			must(t, os.Remove(filepath.Join(f.path, ".git", "index")))
			must(t, os.Mkdir(filepath.Join(f.path, ".git", "index"), 0o700))
		}})
		c := testAPI(t, f.cfg, userReply(), forkReply())
		if _, err := c.Save(context.Background(), f.path, aliceFork, SaveOptions{}); err == nil || !strings.Contains(err.Error(), "commit workspace changes") {
			t.Fatalf("Save() = %v", err)
		}
	})
	t.Run("local-head-moves-during-push", func(t *testing.T) {
		f := newGitFixture(t)
		writeFile(t, f.path, "earlier", "peer commit")
		peer := commitAll(t, f.local, "peer commit")
		wt, err := f.local.Worktree()
		must(t, err)
		must(t, wt.Reset(&git.ResetOptions{Commit: f.base, Mode: git.HardReset}))
		writeFile(t, f.path, "new", "new")
		installFileTransport(t, raceTransport{Transport: client.Protocols["file"], beforePush: func() error {
			return f.local.Storer.SetReference(plumbing.NewHashReference(mainRef, peer))
		}})
		c := testAPI(t, f.cfg, userReply(), forkReply(), forkReply())
		result, err := c.Save(context.Background(), f.path, aliceFork, SaveOptions{})
		if err == nil || !strings.Contains(err.Error(), "updating local HEAD failed") || result.CommitSHA == "" {
			t.Fatalf("Save() = %+v, %v", result, err)
		}
		assertRef(t, f.local, mainRef, peer)
		assertRef(t, f.remote, demoRef, plumbing.NewHash(result.CommitSHA))
	})
}

func TestOpenDestinationErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := New(Config{}, testToken)
	if _, err := c.Open(ctx, aliceFork, "unused", project); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open() = %v", err)
	}
	if err := InstallClone(ctx, "unused", "unused"); !errors.Is(err, context.Canceled) {
		t.Fatalf("installClone() = %v", err)
	}
	root := t.TempDir()
	writeFile(t, root, "existing-file", "unchanged")
	if _, err := c.Open(context.Background(), aliceFork, filepath.Join(root, "existing-file"), project); err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("Open() = %v", err)
	}
	if _, err := c.Open(context.Background(), aliceFork, filepath.Join(root, "existing-file", "subdir"), project); err == nil || !strings.Contains(err.Error(), "inspect clone destination") {
		t.Fatalf("Open() = %v", err)
	}
	t.Run("parent-permissions", func(t *testing.T) {
		requireNonroot(t)
		parent := t.TempDir()
		must(t, os.Chmod(parent, 0o500))
		t.Cleanup(func() { must(t, os.Chmod(parent, 0o700)) })
		for _, tc := range []struct{ path, message string }{
			{"repo", "prepare temporary clone"}, {"nested/repo", "prepare clone destination"},
		} {
			if _, err := c.Open(context.Background(), aliceFork, filepath.Join(parent, tc.path), project); err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("Open() = %v, want %q", err, tc.message)
			}
		}
	})
	t.Run("destination-unreadable", func(t *testing.T) {
		requireNonroot(t)
		path := t.TempDir()
		must(t, os.Chmod(path, 0o100))
		t.Cleanup(func() { must(t, os.Chmod(path, 0o700)) })
		if _, err := c.Open(context.Background(), aliceFork, path, project); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("Open() = %v", err)
		}
	})
	t.Run("destination-filled-during-clone", func(t *testing.T) {
		f := newGitFixture(t)
		pushToFixture(t, f.local, f.url, f.base, demoRef)
		root := t.TempDir()
		path := filepath.Join(root, "reopened")
		installFileTransport(t, uploadHookTransport{Transport: client.Protocols["file"], onOpen: func() {
			writeFile(t, path, "keep", "created during the clone")
		}})
		c := testAPI(t, f.cfg, userReply(), forkReply())
		if _, err := c.Open(context.Background(), aliceFork, path, project); err == nil || !strings.Contains(err.Error(), "install cloned workspace") {
			t.Fatalf("Open() = %v", err)
		}
		if got, err := os.ReadFile(filepath.Join(path, "keep")); err != nil || string(got) != "created during the clone" {
			t.Fatalf("existing data changed: %q, %v", got, err)
		}
		entries, err := os.ReadDir(root)
		must(t, err)
		if len(entries) != 1 || entries[0].Name() != "reopened" {
			t.Fatalf("partial clone remains: %v", entries)
		}
	})
}

func requireNonroot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("fixture requires ordinary filesystem permissions")
	}
}
