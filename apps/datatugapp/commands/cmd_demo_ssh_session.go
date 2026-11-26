package commands

// The public SSH demo's per-session child: `datatug demo ssh-session`, started
// only by `datatug demo ssh-server`, one process per SSH session, under the
// session's own uid. Design: spec/features/ssh-demo/README.md (D1, D2, D3, D8, D9).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	"github.com/datatug/datatug-cli/pkg/sshdemo"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/spf13/cobra"
)

const (
	demoSSHDefaultRepo       = "https://github.com/datatug/datatug-demo-project"
	demoSSHDefaultProjectDir = "demo-project-1"
	demoSSHProjectID         = "demo"
	demoSSHDefaultEnv        = "local"
	demoSSHStartDB           = "chinook"
	demoSSHStartTable        = "Invoice"
	demoSSHPreferredDB       = "orders"

	demoSSHDefaultRLimitAS     uint64 = 8589934592 // 8 GiB (address space; see pkg/sshdemo RlimitAS)
	demoSSHDefaultRLimitCPU    uint64 = 900        // 900 s
	demoSSHDefaultRLimitFSize  uint64 = 67108864   // 64 MiB
	demoSSHDefaultRLimitNoFile uint64 = 512
	demoSSHDefaultRLimitNProc  uint64 = 256
)

type demoSSHRlimits struct {
	AS     uint64
	CPU    uint64
	FSize  uint64
	NoFile uint64
	NProc  uint64
}

type demoSSHSessionFlags struct {
	workspace       string
	template        string
	projectDir      string
	repo            string
	githubClientID  string
	saveBranch      string
	sessionID       string
	deadline        string
	rlimits         demoSSHRlimits
	cleanup         string
	prepareTemplate string
	slot            sshdemo.SlotEntry
	slotRequested   bool
}

// Seams for unit tests in cmd_demo_ssh_session_test.go.
var (
	demoSSHApplyRlimits     = applyDemoSSHRlimits
	demoSSHRaiseOOMScore    = raiseDemoSSHOOMScore
	demoSSHUseMemoryStore   = ghauth.UseMemoryStore
	demoSSHRunWelcome       = dtdemo.RunWelcome
	demoSSHRunWorkspaceLoop = runWorkspaceLoop
	demoSSHCleanupLstat     = os.Lstat
	// A directory read that fails mid-way; a healthy filesystem does not.
	demoSSHCleanupReadDir = func(f *os.File, n int) ([]os.DirEntry, error) { return f.ReadDir(n) }
	// Slot entry runs only as root in the container; tests stand in for it.
	demoSSHEnterSlot     = sshdemo.EnterSlot
	demoSSHChdir         = os.Chdir
	demoSSHSlotReadyFD   = uintptr(sshdemo.SlotReadyFD)
	demoSSHCleanupRemove = os.Remove
	// Seams for I/O failures a test cannot provoke on a healthy filesystem.
	demoSSHTemplateLstat  = os.Lstat
	demoSSHTemplateChmod  = os.Chmod
	demoSSHTemplateCopy   = io.Copy
	demoSSHGeteuid        = os.Geteuid
	demoSSHFileOwnedByUID = fileOwnedByUID
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
			flags := cmd.Flags()
			f.slotRequested = flags.Changed("slot-cgroup") || flags.Changed("slot-uid") || flags.Changed("slot-gid")
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
	flags.StringVar(&f.prepareTemplate, "prepare-template", "", "Prepare a freshly cloned template directory and exit")
	flags.StringVar(&f.slot.CgroupDir, "slot-cgroup", "", "Session slot cgroup directory")
	flags.IntVar(&f.slot.UID, "slot-uid", 0, "Session slot uid")
	flags.IntVar(&f.slot.GID, "slot-gid", 0, "Session slot gid")
	_ = flags.MarkHidden("slot-cgroup")
	_ = flags.MarkHidden("slot-uid")
	_ = flags.MarkHidden("slot-gid")
	return cmd
}

func runDemoSSHSession(ctx context.Context, f demoSSHSessionFlags) error {
	if f.slotRequested {
		if err := demoSSHEnterSlot(f.slot); err != nil {
			return fmt.Errorf("enter session slot: %w", err)
		}
	}
	if f.cleanup != "" {
		if err := demoSSHApplyRlimits(f.rlimits); err != nil {
			return fmt.Errorf("apply cleanup resource limits: %w", err)
		}
		return runDemoSSHCleanup(f.cleanup)
	}
	if f.slotRequested {
		// The supervisor cannot enter the slot user's mode-0700 workspace.
		if err := demoSSHChdir(f.workspace); err != nil {
			return fmt.Errorf("enter session workspace: %w", err)
		}
		ready := os.NewFile(demoSSHSlotReadyFD, "slot-ready")
		_, err := ready.WriteString("1")
		if err := errors.Join(err, ready.Close()); err != nil {
			return fmt.Errorf("acknowledge session slot entry: %w", err)
		}
	}
	if f.prepareTemplate != "" {
		return runDemoSSHPrepareTemplate(ctx, f.prepareTemplate, f.projectDir)
	}

	// Apply rlimits before reading input.
	if err := demoSSHApplyRlimits(f.rlimits); err != nil {
		return fmt.Errorf("apply resource limits: %w", err)
	}

	// Under memory pressure the kernel kills a session child, never the
	// supervisor (best effort: only raising the score needs no privilege).
	_ = demoSSHRaiseOOMScore()

	// Keep GitHub tokens strictly in process memory; never touch the OS keyring.
	demoSSHUseMemoryStore()
	ghauth.LimitScope(dtdemo.GitHubScope)

	if strings.TrimSpace(f.workspace) == "" {
		return errors.New("--workspace is required")
	}
	if strings.TrimSpace(f.template) == "" {
		return errors.New("--template is required")
	}
	canonical, githubEnabled := parseGitHubRepo(f.repo)
	var deadline time.Time
	if strings.TrimSpace(f.deadline) != "" {
		var err error
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

	if err := seedDemoSSHChinookLocal(workspaceDir, projectPath); err != nil {
		return fmt.Errorf("seed chinook-local database: %w", err)
	}

	if ctx == nil {
		ctx = context.Background()
	}
	if err := registerDemoSSHProject(ctx, workspaceDir, projectPath); err != nil {
		return fmt.Errorf("register demo project: %w", err)
	}

	database, openAtStart := resolveDemoSSHStartTarget(ctx, projectPath, demoSSHDefaultEnv)
	modules := uiModules()
	for i, module := range modules {
		if module.ID == datatugui.ScreenProjects {
			modules[i] = dtproject.DemoModule()
			break
		}
	}
	modules = append(modules, dtdemo.SaveModule(demoCfg))
	uiOpts := datatugui.Options{Start: datatugui.ScreenProjects}
	chatCmd, chatOpts := buildDemoSSHWorkspaceOptions(modules, uiOpts, database, openAtStart, demoSSHNotice(deadline, githubEnabled))
	chatOpts.workspace.sessionWarningAt = dtdemo.SessionWarningAt(deadline)
	return demoSSHRunWorkspaceLoop(chatCmd, chatOpts)
}

func buildDemoSSHWorkspaceOptions(modules []datatugui.Module, uiOpts datatugui.Options, database, openAtStart, notice string) (*cobra.Command, chatOptions) {
	uiOpts.DisableBrowser = true
	chatCmd, opts := workspaceOptions(modules, uiOpts, []tea.ProgramOption(nil))
	opts.project = demoSSHProjectID
	opts.env = demoSSHDefaultEnv
	opts.database = database
	opts.openAtStart = openAtStart
	opts.roles = []string{"admin"}
	opts.notice = notice
	return chatCmd, opts
}

func demoSSHNotice(deadline time.Time, githubEnabled bool) string {
	base := "Temporary demo workspace: changes disappear when you disconnect"
	if !deadline.IsZero() {
		base += fmt.Sprintf(" (session ends %s)", deadline.UTC().Format("15:04 UTC"))
	}
	if githubEnabled {
		return base + ". To keep them: Save to GitHub in the module menu."
	}
	return base + "."
}

// parseGitHubRepo parses raw as a GitHub owner/name repository. Non-GitHub
// repositories (such as local paths, file:// URLs, or other hosts) return
// (demogit.Repo{}, false) so the session disables GitHub features cleanly.
func parseGitHubRepo(raw string) (demogit.Repo, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return demogit.Repo{}, false
	}
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return demogit.Repo{}, false
		}
		scheme := strings.ToLower(u.Scheme)
		if (scheme != "http" && scheme != "https") || !strings.EqualFold(u.Hostname(), "github.com") {
			return demogit.Repo{}, false
		}
		s = strings.Trim(u.Path, "/")
	} else {
		if strings.HasPrefix(s, "/") || strings.HasPrefix(s, ".") || strings.HasPrefix(s, "~") {
			return demogit.Repo{}, false
		}
		s = strings.TrimPrefix(s, "github.com/")
		s = strings.Trim(s, "/")
	}
	parts := strings.Split(s, "/")
	if len(parts) != 2 || !isGitHubSlug(parts[0]) || !isGitHubSlug(parts[1]) {
		return demogit.Repo{}, false
	}
	return demogit.Repo{Owner: parts[0], Name: parts[1]}, true
}

func isGitHubSlug(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, " \t\r\n")
}

func seedDemoSSHChinookLocal(workspaceDir, projectPath string) error {
	src := filepath.Join(projectPath, ".demo-data", "chinook.sqlite")
	info, err := os.Stat(src)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat %q: %w", src, err)
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	dst := filepath.Join(workspaceDir, "home", "datatug", "dbs", "chinook-local.sqlite")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create chinook-local dir: %w", err)
	}
	return copyRegularFile(src, dst, 0o644)
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
	if cleanRoot == string(filepath.Separator) {
		return cleanTarget != cleanRoot && strings.HasPrefix(cleanTarget, cleanRoot)
	}
	return strings.HasPrefix(cleanTarget, cleanRoot+string(filepath.Separator))
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
		// ReadDir names never contain a separator, so dstPath stays in dstDir.
		srcPath := filepath.Join(srcDir, name)
		dstPath := filepath.Join(dstDir, name)
		info, err := demoSSHTemplateLstat(srcPath)
		if err != nil {
			return fmt.Errorf("lstat %q: %w", srcPath, err)
		}
		mode := info.Mode()
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(dstPath, 0o755); err != nil {
				return fmt.Errorf("mkdir %q: %w", dstPath, err)
			}
			if err := demoSSHTemplateChmod(dstPath, 0o755); err != nil {
				return fmt.Errorf("chmod dir %q: %w", dstPath, err)
			}
			if err := copyTemplateDir(srcRoot, srcPath, dstRoot, dstPath); err != nil {
				return err
			}
		case mode.IsRegular():
			perm := fs.FileMode(0o644)
			if mode.Perm()&0o111 != 0 {
				perm = 0o755
			}
			if err := copyRegularFile(srcPath, dstPath, perm); err != nil {
				return err
			}
		default:
			rel, _ := filepath.Rel(srcRoot, srcPath)
			return fmt.Errorf("refusing non-regular entry %q (mode %s) in template", rel, mode.Type())
		}
	}
	return nil
}

func copyRegularFile(srcPath, dstPath string, perm fs.FileMode) error {
	in, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open %q: %w", srcPath, err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("create %q: %w", dstPath, err)
	}
	if _, err := demoSSHTemplateCopy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy %q: %w", dstPath, err)
	}
	// Exact modes regardless of the umask the child inherited.
	if err := demoSSHTemplateChmod(dstPath, perm); err != nil {
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

func resolveDemoSSHStartTarget(ctx context.Context, projectPath, env string) (database, openAtStart string) {
	store := filestore.NewProjectStore(demoSSHProjectID, projectPath)
	dbs, err := store.LoadEnvDbCatalogs(ctx, env)
	if err != nil || len(dbs) == 0 {
		return demoSSHPreferredDB, ""
	}
	hasOrders := false
	ids := make([]string, 0, len(dbs))
	for _, db := range dbs {
		if db.ID == demoSSHStartDB {
			return demoSSHStartDB, demoSSHStartTable
		}
		if db.ID == demoSSHPreferredDB {
			hasOrders = true
		}
		ids = append(ids, db.ID)
	}
	if hasOrders {
		return demoSSHPreferredDB, ""
	}
	sort.Strings(ids)
	return ids[0], ""
}

// isHex32 reports whether s is exactly 32 lowercase hexadecimal characters.
func isHex32(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
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
	return removeCleanupEntries(dir, 0)
}

func removeCleanupEntries(dir string, depth int) error {
	const cleanupMaxDepth = 128
	if depth >= cleanupMaxDepth {
		return fmt.Errorf("cleanup exceeded maximum directory depth (%d) at %q", cleanupMaxDepth, dir)
	}
	file, pinned, err := sshdemo.OpenCleanupDirectory(dir)
	if err != nil {
		return fmt.Errorf("read dir %q: %w", dir, err)
	}
	defer func() { _ = file.Close() }()
	for {
		entries, err := demoSSHCleanupReadDir(file, 256)
		for _, entry := range entries {
			p := filepath.Join(pinned, entry.Name())
			info, err := demoSSHCleanupLstat(p)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return fmt.Errorf("lstat %q: %w", p, err)
			}
			if info.IsDir() {
				if err := removeCleanupEntries(p, depth+1); err != nil {
					return err
				}
			}
			if err := demoSSHCleanupRemove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove %q: %w", p, err)
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read dir %q: %w", dir, err)
		}
	}
}
