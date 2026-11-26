//go:build linux

package sshdemo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/demofixtures"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const testProjectPath = "demo-project-1"

func TestTemplateSetupAndPublicationFailures(t *testing.T) {
	repoPath := t.TempDir()
	initTestGitRepo(t, repoPath, map[string][]byte{testProjectPath + "/datatug-project.json": []byte("{}")})
	for _, tc := range []struct {
		name  string
		want  string
		setup func(*testing.T, *TemplateConfig)
	}{
		{"state path is a file", "create state dir", func(t *testing.T, cfg *TemplateConfig) {
			p := filepath.Join(cfg.StateDir, "file")
			if err := os.WriteFile(p, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			cfg.StateDir = p
		}},
		{"stale temporary directory", "clean stale template.tmp", func(t *testing.T, _ *TemplateConfig) {
			original := removeTemplateDir
			t.Cleanup(func() { removeTemplateDir = original })
			removeTemplateDir = func(string) error { return errTestIO }
		}},
		{"fixture read", "fixture I/O failure", func(t *testing.T, cfg *TemplateConfig) {
			original := loadChinookFixtureBytes
			t.Cleanup(func() { loadChinookFixtureBytes = original })
			loadChinookFixtureBytes = func() ([]byte, error) { return nil, errTestIO }
			cfg.ChinookBytes = nil
		}},
		{"chmod template", "chmod template world-readable", func(t *testing.T, cfg *TemplateConfig) {
			cfg.PrepareFunc = func(_ context.Context, templateDir, _ string) error { return os.RemoveAll(templateDir) }
		}},
		{"atomic install", "install template", func(t *testing.T, _ *TemplateConfig) {
			original := renameTemplateDir
			t.Cleanup(func() { renameTemplateDir = original })
			renameTemplateDir = func(string, string) error { return errTestIO }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := TemplateConfig{StateDir: t.TempDir(), Repo: repoPath, ProjectDir: testProjectPath, ChinookBytes: []byte("fixture")}
			tc.setup(t, &cfg)
			_, err := PrepareTemplate(t.Context(), cfg)
			requireErrorContains(t, err, tc.want)
		})
	}
}

func TestTemplateFixtureFilesystemFailures(t *testing.T) {
	for _, tc := range []struct {
		name, rel, target, want string
	}{
		{"directory", ".demo-data", "missing", "create .demo-data dir"},
		{"write", ".demo-data/chinook.sqlite", "missing/file", "write chinook fixture"},
		{"stat", ".demo-data/chinook.sqlite", "chinook.sqlite", "stat chinook fixture path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repoPath := t.TempDir()
			repo, _ := initTestGitRepo(t, repoPath, map[string][]byte{testProjectPath + "/datatug-project.json": []byte("{}")})
			p := filepath.Join(repoPath, testProjectPath, tc.rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(tc.target, p); err != nil {
				t.Fatal(err)
			}
			wt, err := repo.Worktree()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := wt.Add(filepath.Join(testProjectPath, tc.rel)); err != nil {
				t.Fatal(err)
			}
			author := &object.Signature{Name: "Demo test", Email: "demo@example.invalid", When: time.Now()}
			if _, err := wt.Commit("test fixture path", &git.CommitOptions{Author: author}); err != nil {
				t.Fatal(err)
			}

			_, err = PrepareTemplate(t.Context(), TemplateConfig{
				StateDir: t.TempDir(), Repo: repoPath, ProjectDir: testProjectPath, ChinookBytes: []byte("fixture"),
			})
			requireErrorContains(t, err, tc.want)
		})
	}
}

func TestTemplateUsesExplicitHTTPProxy(t *testing.T) {
	method := make(chan string, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case method <- r.Method:
		default:
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	_, err := PrepareTemplate(t.Context(), TemplateConfig{
		StateDir: t.TempDir(), Repo: "https://example.invalid/repo.git", ProjectDir: testProjectPath, EgressProxy: proxy.URL,
	})
	requireErrorContains(t, err, "clone template repo")
	select {
	case got := <-method:
		if got != http.MethodConnect {
			t.Fatalf("proxy method = %s", got)
		}
	default:
		t.Fatal("clone did not use its configured proxy")
	}
}

func TestTemplateMissingOrInvalidEmbeddedFixture(t *testing.T) {
	original := readDemoFixtureFiles
	t.Cleanup(func() { readDemoFixtureFiles = original })
	readDemoFixtureFiles = func() ([]demofixtures.File, error) { return nil, errTestIO }
	_, err := defaultLoadChinookFixtureBytes()
	requireErrorContains(t, err, "load demofixtures")
	readDemoFixtureFiles = func() ([]demofixtures.File, error) { return nil, nil }
	_, err = defaultLoadChinookFixtureBytes()
	requireErrorContains(t, err, "embedded chinook fixture not found")
}

func TestTemplateAtomicReplacementRollsBack(t *testing.T) {
	for _, tc := range []struct {
		name, want string
	}{
		{"stat", "stat existing template"},
		{"remove", "clean stale template.old"},
		{"stage", "stage old template"},
		{"replace", "replace template"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tmp, final, old := filepath.Join(dir, "new"), filepath.Join(dir, "final"), filepath.Join(dir, "old")
			if err := os.WriteFile(tmp, []byte("new"), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.name == "stat" {
				if err := os.Symlink("final", final); err != nil {
					t.Fatal(err)
				}
				requireErrorContains(t, replaceDirAtomic(tmp, final, old), tc.want)
				return
			}
			if err := os.WriteFile(final, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			previousRemove, previousRename := removeTemplateDir, renameTemplateDir
			t.Cleanup(func() { removeTemplateDir, renameTemplateDir = previousRemove, previousRename })
			removeTemplateDir = func(path string) error {
				if tc.name == "remove" && path == old {
					return errTestIO
				}
				return os.RemoveAll(path)
			}
			renameTemplateDir = func(from, to string) error {
				if tc.name == "stage" && from == final || tc.name == "replace" && from == tmp {
					return errTestIO
				}
				return os.Rename(from, to)
			}
			err := replaceDirAtomic(tmp, final, old)
			requireErrorContains(t, err, tc.want)
			if !errors.Is(err, errTestIO) {
				t.Fatalf("rename/removal error = %v", err)
			}
			if data, err := os.ReadFile(final); err != nil || string(data) != "old" {
				t.Fatalf("old template after failure = %q / %v", data, err)
			}
		})
	}
}
