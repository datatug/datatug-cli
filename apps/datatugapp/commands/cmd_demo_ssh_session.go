package commands

// The public SSH demo's per-session child: `datatug demo ssh-session`, started
// only by `datatug demo ssh-server`, one process per SSH session, under the
// session's own uid. Design: spec/features/ssh-demo/README.md (D1, D2, D3, D8, D9).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtdemo"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtproject"
	"github.com/datatug/datatug-cli/pkg/auth/ghauth"
	"github.com/datatug/datatug-cli/pkg/demogit"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/spf13/cobra"
)

const (
	demoSSHDefaultRepo       = "https://github.com/datatug/datatug-demo-project"
	demoSSHDefaultProjectDir = "demo-project-1"
	demoSSHProjectID         = "demo"
	demoSSHDefaultEnv        = "local"
	demoSSHPreferredDB       = "orders"

	demoSSHDefaultRLimitAS     uint64 = 2147483648 // 2 GiB
	demoSSHDefaultRLimitCPU    uint64 = 900        // 900 s
	demoSSHDefaultRLimitFSize  uint64 = 67108864   // 64 MiB
	demoSSHDefaultRLimitNoFile uint64 = 512
	demoSSHDefaultRLimitNProc  uint64 = 64
)

type demoSSHRlimits struct {
	AS     uint64
	CPU    uint64
	FSize  uint64
	NoFile uint64
	NProc  uint64
}

type demoSSHSessionFlags struct {
	workspace      string
	template       string
	projectDir     string
	repo           string
	githubClientID string
	saveBranch     string
	sessionID      string
	deadline       string
	rlimits        demoSSHRlimits
	cleanup        string
}

// Seams for unit tests in cmd_demo_ssh_session_test.go.
var (
	demoSSHApplyRlimits     = applyDemoSSHRlimits
	demoSSHUseMemoryStore   = ghauth.UseMemoryStore
	demoSSHRunWelcome       = dtdemo.RunWelcome
	demoSSHRunWorkspaceLoop = runWorkspaceLoop
	demoSSHCleanupLstat     = os.Lstat
	demoSSHGeteuid          = os.Geteuid
	demoSSHFileOwnedByUID   = fileOwnedByUID
)

func demoSSHSessionCommand() *cobra.Command {
	var f demoSSHSessionFlags
	cmd := &cobra.Command{
		Use:          "ssh-session",
		Short:        "Run one SSH demo session (started by ssh-server only)",
		Hidden:       true,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDemoSSHSession(cmd.Context(), f)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&f.workspace, "workspace", "", "Session workspace directory containing home/ and repo/")
	flags.StringVar(&f.template, "template", "", "Read-only template repository directory")
	flags.StringVar(&f.projectDir, "project-dir", demoSSHDefaultProjectDir, "Relative path of the DataTug project inside the repository")
	flags.StringVar(&f.repo, "repo", demoSSHDefaultRepo, "Canonical demo GitHub repository URL")
	flags.StringVar(&f.githubClientID, "github-client-id", dtproject.DefaultGitHubClientID, "GitHub OAuth App client ID for device flow")
	flags.StringVar(&f.saveBranch, "save-branch", dtdemo.DefaultSaveBranch, "Branch name on the visitor's fork")
	flags.StringVar(&f.sessionID, "session-id", "", "Opaque session identifier assigned by ssh-server")
	flags.StringVar(&f.deadline, "deadline", "", "Session deadline in RFC3339 format")
	flags.Uint64Var(&f.rlimits.AS, "rlimit-as", demoSSHDefaultRLimitAS, "RLIMIT_AS cap in bytes")
	flags.Uint64Var(&f.rlimits.CPU, "rlimit-cpu", demoSSHDefaultRLimitCPU, "RLIMIT_CPU cap in seconds")
	flags.Uint64Var(&f.rlimits.FSize, "rlimit-fsize", demoSSHDefaultRLimitFSize, "RLIMIT_FSIZE cap in bytes")
	flags.Uint64Var(&f.rlimits.NoFile, "rlimit-nofile", demoSSHDefaultRLimitNoFile, "RLIMIT_NOFILE cap")
	flags.Uint64Var(&f.rlimits.NProc, "rlimit-nproc", demoSSHDefaultRLimitNProc, "RLIMIT_NPROC cap")
	flags.StringVar(&f.cleanup, "cleanup", "", "Remove contents of the specified session directory and exit")
	return cmd
}

func runDemoSSHSession(ctx context.Context, f demoSSHSessionFlags) error {
	if f.cleanup != "" {
		return runDemoSSHCleanup(f.cleanup)
	}

	// FIRST action, before reading input or touching files: apply rlimits (fail closed).
	if err := demoSSHApplyRlimits(f.rlimits); err != nil {
		return fmt.Errorf("apply resource limits: %w", err)
	}

	// Keep GitHub tokens strictly in process memory; never touch the OS keyring.
	demoSSHUseMemoryStore()

	if strings.TrimSpace(f.workspace) == "" {
		return errors.New("--workspace is required")
	}
	if strings.TrimSpace(f.template) == "" {
		return errors.New("--template is required")
	}
	canonical, err := parseCanonicalRepo(f.repo)
	if err != nil {
		return fmt.Errorf("invalid --repo: %w", err)
	}
	var deadline time.Time
	if strings.TrimSpace(f.deadline) != "" {
		deadline, err = time.Parse(time.RFC3339, strings.TrimSpace(f.deadline))
		if err != nil {
			return fmt.Errorf("invalid --deadline: %w", err)
		}
	}

	workspaceDir := filepath.Clean(f.workspace)
	repoDir := filepath.Join(workspaceDir, "repo")
	projectPath, err := resolveSafeProjectDir(repoDir, f.projectDir)
	if err != nil {
		return fmt.Errorf("invalid --project-dir: %w", err)
	}

	authState := dtdemo.NewAuthState()
	demoCfg := dtdemo.Config{
		Canonical:      canonical,
		RepoDir:        repoDir,
		ProjectDir:     f.projectDir,
		SaveBranch:     f.saveBranch,
		GitHubClientID: f.githubClientID,
		Deadline:       deadline,
		Auth:           authState,
	}

	welcomeRes, err := demoSSHRunWelcome(demoCfg)
	if err != nil {
		return fmt.Errorf("demo welcome: %w", err)
	}
	switch welcomeRes.Outcome {
	case dtdemo.OutcomeQuit:
		return nil
	case dtdemo.OutcomeExplore:
		if err := copyTemplateTree(f.template, repoDir); err != nil {
			return fmt.Errorf("prepare demo workspace: %w", err)
		}
	case dtdemo.OutcomeOpenedFork:
		// demogit.Client.Open already populated repoDir.
	}

	if ctx == nil {
		ctx = context.Background()
	}
	if err := registerDemoSSHProject(ctx, workspaceDir, projectPath); err != nil {
		return fmt.Errorf("register demo project: %w", err)
	}

	database := resolveDemoSSHDatabase(ctx, projectPath, demoSSHDefaultEnv)
	modules := append(uiModules(), dtdemo.SaveModule(demoCfg))
	uiOpts := datatugui.Options{Start: datatugui.ScreenProjects}
	chatCmd, chatOpts := buildDemoSSHWorkspaceOptions(modules, uiOpts, database, demoSSHNotice(deadline))
	return demoSSHRunWorkspaceLoop(chatCmd, chatOpts)
}

func buildDemoSSHWorkspaceOptions(modules []datatugui.Module, uiOpts datatugui.Options, database, notice string) (*cobra.Command, chatOptions) {
	chatCmd, opts := workspaceOptions(modules, uiOpts, []tea.ProgramOption(nil))
	opts.project = demoSSHProjectID
	opts.env = demoSSHDefaultEnv
	opts.database = database
	opts.roles = []string{"admin"}
	opts.notice = notice
	return chatCmd, opts
}

func demoSSHNotice(deadline time.Time) string {
	if !deadline.IsZero() {
		return fmt.Sprintf(
			"Temporary demo workspace: changes disappear when you disconnect (session ends %s). To keep them: Save to GitHub in the module menu.",
			deadline.UTC().Format("15:04 UTC"),
		)
	}
	return "Temporary demo workspace: changes disappear when you disconnect. To keep them: Save to GitHub in the module menu."
}

func parseCanonicalRepo(raw string) (demogit.Repo, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return demogit.Repo{}, errors.New("repository URL is empty")
	}
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return demogit.Repo{}, err
		}
		s = strings.Trim(u.Path, "/")
	} else {
		s = strings.TrimPrefix(s, "github.com/")
		s = strings.Trim(s, "/")
	}
	parts := strings.Split(s, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return demogit.Repo{}, fmt.Errorf("expected owner/name repository, got %q", raw)
	}
	return demogit.Repo{Owner: parts[0], Name: parts[1]}, nil
}

func resolveSafeProjectDir(repoDir, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("project-dir must be relative: %q", rel)
	}
	cleanRepo := filepath.Clean(repoDir)
	joined := filepath.Clean(filepath.Join(cleanRepo, rel))
	if joined != cleanRepo && !isWithinDir(cleanRepo, joined) {
		return "", fmt.Errorf("project-dir escapes repository root: %q", rel)
	}
	return joined, nil
}

func isWithinDir(root, target string) bool {
	cleanRoot := filepath.Clean(root)
	cleanTarget := filepath.Clean(target)
	rel, err := filepath.Rel(cleanRoot, cleanTarget)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// copyTemplateTree copies regular files and directories from srcRoot into dstRoot,
// preserving .git, refusing symlinks and non-regular entries, enforcing 0755 dirs
// and 0644 files, and ensuring every written path stays inside dstRoot.
func copyTemplateTree(srcRoot, dstRoot string) error {
	cleanSrc := filepath.Clean(srcRoot)
	cleanDst := filepath.Clean(dstRoot)

	srcInfo, err := os.Lstat(cleanSrc)
	if err != nil {
		return fmt.Errorf("stat template %q: %w", cleanSrc, err)
	}
	if !srcInfo.IsDir() {
		return fmt.Errorf("template %q is not a directory", cleanSrc)
	}
	if err := os.MkdirAll(cleanDst, 0o755); err != nil {
		return fmt.Errorf("create repo dir %q: %w", cleanDst, err)
	}
	return copyTemplateDir(cleanSrc, cleanSrc, cleanDst, cleanDst)
}

func copyTemplateDir(srcRoot, srcDir, dstRoot, dstDir string) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return fmt.Errorf("read template dir %q: %w", srcDir, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		srcPath := filepath.Join(srcDir, name)
		dstPath := filepath.Join(dstDir, name)
		if !isWithinDir(dstRoot, dstPath) {
			return fmt.Errorf("template entry %q escapes destination", name)
		}
		info, err := os.Lstat(srcPath)
		if err != nil {
			return fmt.Errorf("lstat %q: %w", srcPath, err)
		}
		mode := info.Mode()
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(dstPath, 0o755); err != nil {
				return fmt.Errorf("mkdir %q: %w", dstPath, err)
			}
			if err := os.Chmod(dstPath, 0o755); err != nil {
				return fmt.Errorf("chmod dir %q: %w", dstPath, err)
			}
			if err := copyTemplateDir(srcRoot, srcPath, dstRoot, dstPath); err != nil {
				return err
			}
		case mode.IsRegular():
			if err := copyRegularFile(srcPath, dstPath); err != nil {
				return err
			}
		default:
			rel, _ := filepath.Rel(srcRoot, srcPath)
			return fmt.Errorf("refusing non-regular entry %q (mode %s) in template", rel, mode.Type())
		}
	}
	return nil
}

func copyRegularFile(srcPath, dstPath string) error {
	in, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open %q: %w", srcPath, err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create %q: %w", dstPath, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy %q: %w", dstPath, err)
	}
	if err := out.Chmod(0o644); err != nil {
		_ = out.Close()
		return fmt.Errorf("chmod %q: %w", dstPath, err)
	}
	return out.Close()
}

func registerDemoSSHProject(ctx context.Context, workspaceDir, projectPath string) error {
	title := demoSSHProjectID
	store := filestore.NewProjectStore(demoSSHProjectID, projectPath)
	if proj, err := store.LoadProject(ctx); err == nil && proj != nil && strings.TrimSpace(proj.Title) != "" {
		title = proj.Title
	}

	configPath := filepath.Join(workspaceDir, "home", dtconfig.ConfigFileName)
	dtconfig.SetConfigFilePath(configPath)

	settings, err := dtconfig.GetSettings()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read settings: %w", err)
	}
	updated := false
	for _, p := range settings.Projects {
		if p != nil && p.ID == demoSSHProjectID {
			p.Path = projectPath
			p.Title = title
			updated = true
			break
		}
	}
	if !updated {
		settings.Projects = append(settings.Projects, &dtconfig.ProjectRef{
			ID:    demoSSHProjectID,
			Path:  projectPath,
			Title: title,
		})
	}
	if err := dtconfig.SaveSettings(settings); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	return nil
}

func resolveDemoSSHDatabase(ctx context.Context, projectPath, env string) string {
	store := filestore.NewProjectStore(demoSSHProjectID, projectPath)
	dbs, err := store.LoadEnvDbCatalogs(ctx, env)
	if err != nil || len(dbs) == 0 {
		return demoSSHPreferredDB
	}
	ids := make([]string, 0, len(dbs))
	for _, db := range dbs {
		if db == nil || db.ID == "" {
			continue
		}
		if db.ID == demoSSHPreferredDB {
			return demoSSHPreferredDB
		}
		ids = append(ids, db.ID)
	}
	if len(ids) == 0 {
		return demoSSHPreferredDB
	}
	sort.Strings(ids)
	return ids[0]
}

// isHex32 reports whether s is exactly 32 lowercase hexadecimal characters.
func isHex32(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// runDemoSSHCleanup removes all contents of dir without following symlinks.
// It refuses unless dir is absolute, its base name is 32 lowercase hex chars,
// and it is owned by the current uid. Missing dir succeeds idempotently.
func runDemoSSHCleanup(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("cleanup path must be absolute: %q", dir)
	}
	cleaned := filepath.Clean(dir)
	if !isHex32(filepath.Base(cleaned)) {
		return fmt.Errorf("cleanup directory base name must be 32 lowercase hex chars: %q", filepath.Base(cleaned))
	}
	info, err := demoSSHCleanupLstat(cleaned)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lstat cleanup dir %q: %w", cleaned, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("cleanup path %q is not a directory", cleaned)
	}
	if !demoSSHFileOwnedByUID(info, demoSSHGeteuid()) {
		return fmt.Errorf("cleanup path %q is not owned by current uid", cleaned)
	}
	return removeDirContentsNoFollow(cleaned)
}

func removeDirContentsNoFollow(dir string) error {
	_ = os.Chmod(dir, 0o700)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read dir %q: %w", dir, err)
	}
	for _, entry := range entries {
		p := filepath.Join(dir, entry.Name())
		fi, err := demoSSHCleanupLstat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("lstat %q: %w", p, err)
		}
		if fi.IsDir() {
			if err := removeDirContentsNoFollow(p); err != nil {
				return err
			}
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %q: %w", p, err)
		}
	}
	return nil
}
