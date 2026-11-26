package sshdemo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/datatug/datatug-cli/pkg/demofixtures"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	gitclient "github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

var installGitProxyOnce sync.Once

// ensureGitHTTPProxy registers go-git HTTP/HTTPS clients whose transport
// honours HTTPS_PROXY / HTTP_PROXY via http.ProxyFromEnvironment.
func ensureGitHTTPProxy() {
	installGitProxyOnce.Do(func() {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.Proxy = http.ProxyFromEnvironment
		c := githttp.NewClient(&http.Client{Transport: tr})
		gitclient.InstallProtocol("http", c)
		gitclient.InstallProtocol("https", c)
	})
}

// TemplateConfig controls how PrepareTemplate clones and prepares the read-only
// demo project template.
type TemplateConfig struct {
	StateDir       string
	Repo           string
	Ref            string
	ProjectDir     string
	EgressProxy    string
	Executable     string
	ExecPrefixArgs []string

	// PrepareFunc optionally overrides invoking Executable with
	// `demo ssh-session --prepare-template` (seam for tests).
	PrepareFunc func(ctx context.Context, templateDir, projectDir string) error

	// ChinookBytes optionally overrides the embedded Chinook SQLite fixture
	// (useful in tests to keep test I/O tiny).
	ChinookBytes []byte
}

// BuildPrepareTemplateArgs constructs the exact argument list for
// `demo ssh-session --prepare-template <dir> --project-dir <rel>`.
func BuildPrepareTemplateArgs(templateDir, projectDir string) []string {
	return []string{"demo", "ssh-session", "--prepare-template", templateDir, "--project-dir", projectDir}
}

func buildPrepareTemplateEnv(homeDir, egressProxy string) []string {
	env := []string{
		"HOME=" + homeDir,
		"LANG=C.UTF-8",
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"DATATUG_TELEMETRY=0",
		"DO_NOT_TRACK=1",
	}
	if egressProxy != "" {
		env = append(env,
			"HTTPS_PROXY="+egressProxy,
			"HTTP_PROXY="+egressProxy,
			"https_proxy="+egressProxy,
			"http_proxy="+egressProxy,
		)
	}
	return append(env, "NO_PROXY=")
}

// PrepareTemplate clones cfg.Repo at cfg.Ref (default branch when empty) with
// go-git (depth 1, no submodules) into <state-dir>/template.tmp, verifies
// <project-dir>/datatug-project.json exists, writes the bundled Chinook SQLite
// fixture to <project-dir>/.demo-data/chinook.sqlite when absent, runs template
// preparation (scanning local catalogs and committing prepared metadata),
// ensures the tree is world-readable without removing any existing mode bits,
// and atomically replaces <state-dir>/template only after the new tree is complete.
func PrepareTemplate(ctx context.Context, cfg TemplateConfig) (string, error) {
	if cfg.StateDir == "" {
		return "", errors.New("sshdemo: state-dir must not be empty")
	}
	if cfg.Repo == "" {
		return "", errors.New("sshdemo: repo must not be empty")
	}
	if err := validateRelProjectDir(cfg.ProjectDir); err != nil {
		return "", err
	}

	ensureGitHTTPProxy()

	if err := os.MkdirAll(cfg.StateDir, 0o755); err != nil {
		return "", fmt.Errorf("sshdemo: create state dir %s: %w", cfg.StateDir, err)
	}

	tmpDir := filepath.Join(cfg.StateDir, "template.tmp")
	finalDir := filepath.Join(cfg.StateDir, "template")
	oldDir := filepath.Join(cfg.StateDir, "template.old")

	if err := os.RemoveAll(tmpDir); err != nil {
		return "", fmt.Errorf("sshdemo: clean stale template.tmp: %w", err)
	}
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	cloneOpts := &git.CloneOptions{
		URL:               cfg.Repo,
		Depth:             1,
		SingleBranch:      true,
		RecurseSubmodules: git.NoRecurseSubmodules,
		Tags:              git.NoTags,
	}
	if cfg.Ref != "" {
		if strings.HasPrefix(cfg.Ref, "refs/") {
			cloneOpts.ReferenceName = plumbing.ReferenceName(cfg.Ref)
		} else {
			cloneOpts.ReferenceName = plumbing.NewBranchReferenceName(cfg.Ref)
		}
	}
	if cfg.EgressProxy != "" && isHTTPURL(cfg.Repo) {
		cloneOpts.ProxyOptions = transport.ProxyOptions{URL: cfg.EgressProxy}
	}

	if _, err := git.PlainCloneContext(ctx, tmpDir, false, cloneOpts); err != nil {
		return "", fmt.Errorf("sshdemo: clone template repo %s: %w", cfg.Repo, err)
	}

	projectPath := filepath.Join(tmpDir, cfg.ProjectDir)
	manifestPath := filepath.Join(projectPath, "datatug-project.json")
	manifestInfo, err := os.Stat(manifestPath)
	if err != nil {
		return "", fmt.Errorf("sshdemo: template missing %s: %w", filepath.Join(cfg.ProjectDir, "datatug-project.json"), err)
	}
	if !manifestInfo.Mode().IsRegular() {
		return "", fmt.Errorf("sshdemo: template %s is not a regular file", filepath.Join(cfg.ProjectDir, "datatug-project.json"))
	}

	chinookDest := filepath.Join(projectPath, ".demo-data", "chinook.sqlite")
	if _, err := os.Stat(chinookDest); os.IsNotExist(err) {
		data := cfg.ChinookBytes
		if len(data) == 0 {
			data, err = loadChinookFixtureBytes()
			if err != nil {
				return "", err
			}
		}
		if err := os.MkdirAll(filepath.Dir(chinookDest), 0o755); err != nil {
			return "", fmt.Errorf("sshdemo: create .demo-data dir: %w", err)
		}
		if err := os.WriteFile(chinookDest, data, 0o644); err != nil {
			return "", fmt.Errorf("sshdemo: write chinook fixture: %w", err)
		}
	} else if err != nil {
		return "", fmt.Errorf("sshdemo: stat chinook fixture path: %w", err)
	}

	if cfg.PrepareFunc != nil {
		if err := cfg.PrepareFunc(ctx, tmpDir, cfg.ProjectDir); err != nil {
			return "", fmt.Errorf("sshdemo: prepare template: %w", err)
		}
	} else if cfg.Executable != "" {
		if err := runPrepareTemplateCommand(ctx, cfg.Executable, cfg.ExecPrefixArgs, cfg.StateDir, tmpDir, cfg.ProjectDir, cfg.EgressProxy); err != nil {
			return "", err
		}
	}

	if err := chmodWorldReadable(tmpDir); err != nil {
		return "", fmt.Errorf("sshdemo: chmod template world-readable: %w", err)
	}

	if err := replaceDirAtomic(tmpDir, finalDir, oldDir); err != nil {
		return "", err
	}
	cleanupTmp = false
	return finalDir, nil
}

func runPrepareTemplateCommand(ctx context.Context, execPath string, execPrefix []string, stateDir, templateDir, projectDir, egressProxy string) error {
	homeDir := filepath.Join(stateDir, "template.home")
	_ = os.RemoveAll(homeDir)
	if err := os.MkdirAll(homeDir, 0o700); err != nil {
		return fmt.Errorf("sshdemo: create template home dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(homeDir) }()

	args := append(append([]string(nil), execPrefix...), BuildPrepareTemplateArgs(templateDir, projectDir)...)
	cmd := exec.CommandContext(ctx, execPath, args...)
	cmd.Env = buildPrepareTemplateEnv(homeDir, egressProxy)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("sshdemo: prepare template: %w: %s", err, msg)
		}
		return fmt.Errorf("sshdemo: prepare template: %w", err)
	}
	return nil
}

func isHTTPURL(u string) bool {
	lower := strings.ToLower(u)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

// loadChinookFixtureBytes returns the pinned Chinook SQLite bytes from
// pkg/demofixtures (which verifies the embedded SHA256 and size).
var loadChinookFixtureBytes = defaultLoadChinookFixtureBytes

func defaultLoadChinookFixtureBytes() ([]byte, error) {
	files, err := demofixtures.Files()
	if err != nil {
		return nil, fmt.Errorf("sshdemo: load demofixtures: %w", err)
	}
	for _, f := range files {
		if f.Path == demofixtures.ChinookPath {
			return f.Content, nil
		}
	}
	return nil, errors.New("sshdemo: embedded chinook fixture not found in demofixtures")
}

var templateDirEntryInfo = func(d fs.DirEntry) (fs.FileInfo, error) {
	return d.Info()
}

func chmodWorldReadable(root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := templateDirEntryInfo(d)
		if err != nil {
			return err
		}
		perm := info.Mode().Perm()
		want := perm | 0o004
		if d.IsDir() || perm&0o100 != 0 {
			want |= 0o001
		}
		if want == perm {
			return nil
		}
		return os.Chmod(p, want)
	})
}

func replaceDirAtomic(tmpDir, finalDir, oldDir string) error {
	if _, err := os.Stat(finalDir); os.IsNotExist(err) {
		if err := os.Rename(tmpDir, finalDir); err != nil {
			return fmt.Errorf("sshdemo: install template %s: %w", finalDir, err)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("sshdemo: stat existing template %s: %w", finalDir, err)
	}

	if err := os.RemoveAll(oldDir); err != nil {
		return fmt.Errorf("sshdemo: clean stale template.old: %w", err)
	}
	if err := os.Rename(finalDir, oldDir); err != nil {
		return fmt.Errorf("sshdemo: stage old template: %w", err)
	}
	if err := os.Rename(tmpDir, finalDir); err != nil {
		_ = os.Rename(oldDir, finalDir)
		return fmt.Errorf("sshdemo: replace template %s: %w", finalDir, err)
	}
	_ = os.RemoveAll(oldDir)
	return nil
}
