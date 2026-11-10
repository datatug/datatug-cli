package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/datatug/datatug-cli/pkg/webui"
	"github.com/spf13/cobra"
)

func uninstallCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "uninstall", Short: "Preview or remove installed DataTug components",
		Long: "By default, preview removal of just the CLI. --web-ui or --skills selects only those components; " +
			"--full selects the CLI and web UI. Name the agent explicitly to remove its skills. " +
			"Projects, credentials, shell settings and marketplaces are preserved. Use demo reset separately.",
		Args: cobra.NoArgs,
		RunE: uninstallAction,
	}
	cmd.Flags().Bool("apply", false, "Apply the removal; otherwise only preview")
	cmd.Flags().Bool("dry-run", false, "Only preview the removal (the default)")
	cmd.Flags().Bool("full", false, "Remove the CLI and installed web UI")
	cmd.Flags().Bool("web-ui", false, "Remove downloaded web UI files")
	cmd.Flags().String("skills", "", "Remove the DataTug plugin for this agent: codex or claude")
	cmd.Flags().String("format", "text", "Output format: text or json")
	cmd.MarkFlagsMutuallyExclusive("apply", "dry-run")
	return cmd
}

func uninstallAction(cmd *cobra.Command, _ []string) error {
	format, err := localFormat(cmd)
	if err != nil {
		return err
	}
	apply, _ := cmd.Flags().GetBool("apply")
	full, _ := cmd.Flags().GetBool("full")
	ui, _ := cmd.Flags().GetBool("web-ui")
	agent, _ := cmd.Flags().GetString("skills")
	if cmd.Flags().Changed("skills") && agent == "" {
		return Exit("use --skills codex or --skills claude", exitCodeUsage)
	}
	report := localReport{Mode: "preview", Operations: []localOperation{}, Notes: []string{
		"Projects, credentials, shell settings and marketplaces are preserved. Demo reset is a separate command.",
		"Byte totals are logical file sizes. Space reclaimed may differ.",
	}}
	if agent != "" {
		op, err := skillRemoval(cmd.Context(), agent)
		if err != nil {
			return err
		}
		report.add(op)
	}
	if ui || full {
		report.Notes = append(report.Notes, "Stop the local server if it is using the selected web UI files before applying.")
		op, err := planWebUIRemoval(webUIStore())
		if err != nil {
			return err
		}
		if op != nil {
			report.add(*op)
		}
	}
	// Remove the running executable last so another operation can be retried.
	if full || (!ui && agent == "") {
		op, err := planCLIRemoval()
		if err != nil {
			return err
		}
		report.add(op)
	}
	return finishLocalPlan(cmd, report, format, apply)
}

func finishLocalPlan(cmd *cobra.Command, report localReport, format string, apply bool) error {
	if !apply {
		return printLocalReport(cmd.OutOrStdout(), report, format)
	}
	for _, op := range report.Operations {
		if op.run == nil {
			if err := printLocalReport(cmd.OutOrStdout(), report, format); err != nil {
				return err
			}
			return Exit("nothing changed: a selected component requires manual removal", exitCodeUsage)
		}
	}
	report.Mode = "applied"
	result := localReport{Mode: report.Mode, Operations: []localOperation{}, Notes: report.Notes}
	for _, op := range report.Operations {
		if err := op.run(); err != nil {
			result.Mode = "incomplete"
			return errors.Join(printLocalReport(cmd.OutOrStdout(), result, format), fmt.Errorf("%s %s: %w", op.Action, op.Component, err))
		}
		result.add(op)
	}
	return printLocalReport(cmd.OutOrStdout(), result, format)
}

func planCLIRemoval() (localOperation, error) {
	exe, err := localExecutable()
	if err != nil {
		return localOperation{}, err
	}
	home, err := localHomeDir()
	if err != nil {
		return localOperation{}, err
	}
	return cliRemoval(exe, filepath.Join(home, ".local", "bin", "datatug"), runtime.GOOS)
}

func cliRemoval(exe, standalone, goos string) (localOperation, error) {
	op := localOperation{Component: "cli", Action: "manual", Path: exe,
		Note: "Use the installer or package manager that owns this executable."}
	actual, err := localEvalSymlinks(exe)
	if err != nil {
		return op, err
	}
	if goos == "darwin" {
		for _, prefix := range []string{"/opt/homebrew", "/usr/local"} {
			if exe == prefix+"/bin/datatug" && strings.HasPrefix(actual, prefix+"/Caskroom/datatug/") {
				op.Command = []string{prefix + "/bin/brew", "uninstall", "--cask", "datatug"}
				return op, nil
			}
		}
	}
	if goos == "windows" {
		if appData := os.Getenv("LOCALAPPDATA"); filepath.IsAbs(appData) &&
			strings.EqualFold(actual, filepath.Join(appData, "DataTug", "bin", "datatug.exe")) {
			op.Command = []string{"Remove-Item", "-LiteralPath", actual}
			op.Note = "Run in PowerShell after all DataTug processes exit."
		}
		return op, nil
	}
	if exe != standalone || actual != exe {
		return op, nil
	}
	tree, err := inventoryTree(exe, nil)
	if err != nil {
		return op, err
	}
	if len(tree.entries) != 1 || !tree.entries[0].info.Mode().IsRegular() {
		return op, fmt.Errorf("running CLI is no longer a regular file at %s", exe)
	}
	op.Action, op.Usage, op.Note, op.run = "remove", &tree.usage, "", tree.remove
	op.Paths = tree.paths()
	return op, nil
}

func skillRemoval(ctx context.Context, agent string) (localOperation, error) {
	var argv []string
	switch agent {
	case "codex":
		argv = []string{"codex", "plugin", "remove", "datatug@datatug"}
	case "claude":
		argv = []string{"claude", "plugin", "uninstall", "datatug@datatug", "--scope", "user", "--keep-data"}
	default:
		return localOperation{}, Exit("use --skills codex or --skills claude", exitCodeUsage)
	}
	op := localOperation{Component: "skills", Action: "delegate", Target: agent, Command: argv,
		Note: "The agent's plugin manager decides the exact files and bytes."}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		op.Action, op.Note = "manual", "Agent CLI is not available on PATH."
		return op, nil
	}
	op.Command[0] = path
	op.run = func() error {
		return exec.CommandContext(ctx, path, argv[1:]...).Run()
	}
	return op, nil
}

var (
	localAbs          = filepath.Abs
	localEvalSymlinks = filepath.EvalSymlinks
)

func planWebUIRemoval(store webui.Store) (*localOperation, error) {
	dir, err := localAbs(store.Dir)
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(dir); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	} else if resolved != dir {
		return nil, fmt.Errorf("refusing to remove a symlinked web UI directory: %s", dir)
	}
	store.Dir = dir
	tree, err := inventoryTree(dir, nil)
	if err != nil {
		return nil, err
	}
	for _, entry := range tree.entries {
		if !entry.info.IsDir() && !entry.info.Mode().IsRegular() {
			return nil, fmt.Errorf("web UI directory contains a symlink or special file: %s", entry.name)
		}
	}
	statePath := filepath.Join(dir, "state.json")
	allowed := map[string]bool{dir: true, filepath.Join(dir, "versions"): true, statePath: false}
	if err := checkWebUIStateOwnership(statePath); err != nil {
		return nil, err
	}
	versions, err := store.List()
	if err != nil {
		return nil, err
	}
	for _, entry := range versions {
		manifest, err := webui.VerifyRemovalDir(entry.Dir)
		if err != nil {
			return nil, err
		}
		addWebUIPaths(allowed, entry.Dir, manifest)
	}
	for _, entry := range tree.entries {
		isDir, ok := allowed[entry.name]
		if !ok || (isDir && !entry.info.IsDir()) || (!isDir && !entry.info.Mode().IsRegular()) {
			return nil, fmt.Errorf("web UI directory contains an unrecognized path: %s", entry.name)
		}
	}
	if len(versions) == 0 {
		return nil, nil
	}
	return &localOperation{Component: "web-ui", Action: "remove", Path: dir, Paths: tree.paths(), Usage: &tree.usage, run: tree.stageRemoval}, nil
}

// addWebUIPaths receives an entry already validated against the signed
// manifest and the on-disk files by VerifyRemovalDir.
func addWebUIPaths(allowed map[string]bool, version string, manifest webui.Manifest) {
	allowed[version] = true
	allowed[filepath.Join(version, webui.ManifestFile)] = false
	allowed[filepath.Join(version, webui.SignatureFile)] = false
	for _, file := range manifest.Files {
		name := filepath.Join(version, filepath.FromSlash(file.Path))
		allowed[name] = false
		for parent := filepath.Dir(name); parent != version; parent = filepath.Dir(parent) {
			allowed[parent] = true
		}
	}
}

// A store selector can point at a custom directory. Keep arbitrary state
// documents there even if their filename happens to be state.json.
func checkWebUIStateOwnership(path string) error {
	data, err := localReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var state *webui.State
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil || state == nil || !json.Valid(data) {
		return fmt.Errorf("web UI state is damaged or has unrecognized fields: %s", path)
	}
	for _, version := range []string{state.Pinned, state.Active, state.Previous} {
		if version != "" && !webui.ValidVersion(version) {
			return fmt.Errorf("web UI state has an unrecognized version: %s", path)
		}
	}
	return nil
}
