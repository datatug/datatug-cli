package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/datatug/datatug-cli/pkg/demofixtures"
	"github.com/datatug/datatug-cli/pkg/webui"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func maintenanceTempDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return path
}

func maintenanceWrite(t *testing.T, file string, data []byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o700))
	require.NoError(t, os.WriteFile(file, data, 0o600))
}

func TestLocalMaintenanceStandaloneRemoval(t *testing.T) {
	home := maintenanceTempDir(t)
	binary := filepath.Join(home, ".local", "bin", "datatug")
	config := filepath.Join(home, ".datatug.yaml")
	maintenanceWrite(t, binary, []byte("executable"))
	maintenanceWrite(t, config, []byte("credentials: keep"))
	op, err := cliRemoval(binary, binary, "darwin")
	require.NoError(t, err)
	require.Equal(t, "remove", op.Action)
	assert.Equal(t, localUsage{Files: 1, Bytes: 10}, *op.Usage)
	require.FileExists(t, binary, "planning does not remove the CLI")
	require.NoError(t, op.run())
	require.NoFileExists(t, binary)
	require.FileExists(t, config)
}

func TestLocalMaintenanceWebUIRefusesExtraFiles(t *testing.T) {
	home := maintenanceTempDir(t)
	store := webui.Store{Dir: filepath.Join(home, "web-ui")}
	version := filepath.Join(store.Dir, "versions", "0.1.0")
	html := []byte("<html></html>")
	m := webui.Manifest{
		Schema: webui.ManifestSchema, Name: webui.ManifestName, Version: "0.1.0",
		Requires: webui.Requirements{LocalAPI: ">=1.0 <2"},
		Files:    []webui.ManifestFileEntry{{Path: webui.IndexFile, Size: int64(len(html)), SHA256: fmt.Sprintf("%x", sha256.Sum256(html))}},
	}
	raw, err := json.Marshal(m)
	require.NoError(t, err)
	maintenanceWrite(t, filepath.Join(version, webui.ManifestFile), raw)
	maintenanceWrite(t, filepath.Join(version, filepath.FromSlash(webui.IndexFile)), html)
	extra := filepath.Join(version, "my-file.txt")
	maintenanceWrite(t, extra, []byte("keep"))
	_, err = planWebUIRemoval(store)
	require.ErrorContains(t, err, "unrecognized path")
	require.FileExists(t, extra)
	require.NoError(t, os.Remove(extra))

	op, err := planWebUIRemoval(store)
	require.NoError(t, err)
	require.NotNil(t, op)
	assert.Equal(t, localUsage{Files: 2, Directories: 4, Bytes: int64(len(raw) + len(html))}, *op.Usage)
	require.DirExists(t, store.Dir, "planning does not remove web assets")
	require.NoError(t, op.run())
	require.NoDirExists(t, store.Dir)
}

func TestLocalMaintenanceDemoResetKeepsEditsAndOtherSettings(t *testing.T) {
	home := maintenanceTempDir(t)
	project := filepath.Join(home, demoInstallProjectID)
	settings := filepath.Join(home, ".datatug.yaml")
	marker, err := json.Marshal(map[string]string{"projectId": demoInstallProjectID, "chinookSha256": demofixtures.ChinookSHA256})
	require.NoError(t, err)
	maintenanceWrite(t, filepath.Join(project, demoInstallMarker), marker)
	maintenanceWrite(t, filepath.Join(project, "notes.txt"), []byte("my demo edits"))
	config := fmt.Appendf(nil, "projects:\n  - id: datatug-demo\n    path: %q\n  - id: personal\n    path: /elsewhere\nfutureField: keep\n", project)
	maintenanceWrite(t, settings, config)

	plan, err := demoResetPlan(project, settings)
	require.NoError(t, err)
	require.Len(t, plan.Operations, 2)
	assert.Equal(t, localUsage{}, plan.Total, "archiving is not a disk saving")
	require.DirExists(t, project)
	raw, err := os.ReadFile(settings)
	require.NoError(t, err)
	assert.Equal(t, config, raw)

	require.NoError(t, plan.Operations[0].run())
	require.NoDirExists(t, project)
	require.FileExists(t, filepath.Join(plan.Operations[0].Target, "notes.txt"))
	raw, err = os.ReadFile(settings)
	require.NoError(t, err)
	var updated map[string]any
	require.NoError(t, yaml.Unmarshal(raw, &updated))
	assert.Equal(t, "keep", updated["futureField"])
	assert.Equal(t, []any{map[string]any{"id": "personal", "path": "/elsewhere"}}, updated["projects"])
}

func TestLocalMaintenanceDiskDoesNotDoubleCount(t *testing.T) {
	home := maintenanceTempDir(t)
	project := filepath.Join(home, "project")
	maintenanceWrite(t, filepath.Join(project, "notes.txt"), []byte("project"))
	maintenanceWrite(t, filepath.Join(home, "state"), []byte("state"))
	locations := []localOperation{{Component: "parent", Path: home}, {Component: "project", Path: project}}
	report := diskLocations(localReport{}, locations)
	require.Len(t, report.Operations, 2)
	assert.Equal(t, localUsage{Files: 2, Directories: 2, Bytes: 12}, report.Total)
	assert.Equal(t, localUsage{Files: 1, Directories: 1, Bytes: 7}, *report.Operations[0].Usage)
}

func TestLocalMaintenanceUninstallSelection(t *testing.T) {
	home := maintenanceTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("DATATUG_WEB_UI_DIR", filepath.Join(home, "web-ui"))
	t.Setenv("PATH", "")
	preview := func(args ...string) (localReport, error) {
		t.Helper()
		cmd := uninstallCommand()
		var output bytes.Buffer
		cmd.SetOut(&output)
		cmd.SetErr(&output)
		cmd.SetArgs(append([]string{"--format", "json"}, args...))
		err := cmd.Execute()
		var report localReport
		if output.Len() > 0 && err == nil {
			require.NoError(t, json.Unmarshal(output.Bytes(), &report), output.String())
		}
		return report, err
	}
	report, err := preview("--dry-run")
	require.NoError(t, err)
	require.Len(t, report.Operations, 1)
	assert.Equal(t, "cli", report.Operations[0].Component)

	report, err = preview("--web-ui", "--apply")
	require.NoError(t, err)
	assert.Empty(t, report.Operations)
	assert.Equal(t, "applied", report.Mode)

	report, err = preview("--full", "--skills", "codex")
	require.NoError(t, err)
	require.Len(t, report.Operations, 2)
	assert.Equal(t, []string{"codex", "plugin", "remove", "datatug@datatug"}, report.Operations[0].Command)
	assert.Equal(t, "cli", report.Operations[1].Component)
	_, err = preview("--skills", "claude", "--apply")
	require.ErrorContains(t, err, "nothing changed")
	_, err = preview("--skills=")
	require.ErrorContains(t, err, "use --skills")
	_, err = preview("--skills", "other")
	require.ErrorContains(t, err, "use --skills")
	_, err = preview("--dry-run", "--apply")
	require.Error(t, err)
}

func TestLocalMaintenanceRemovalRevalidatesPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs extra Windows privileges")
	}
	home := maintenanceTempDir(t)
	original := filepath.Join(home, "datatug")
	replacement := filepath.Join(home, "personal")
	maintenanceWrite(t, original, []byte("managed"))
	maintenanceWrite(t, replacement, []byte("keep"))
	tree, err := inventoryTree(original, nil)
	require.NoError(t, err)
	require.NoError(t, os.Remove(original))
	require.NoError(t, os.Symlink(replacement, original))
	require.ErrorContains(t, tree.remove(), "path changed")
	data, err := os.ReadFile(replacement)
	require.NoError(t, err)
	assert.Equal(t, "keep", string(data))
}

func TestLocalMaintenanceDemoResetDoesNotOverwriteConcurrentSettings(t *testing.T) {
	home := maintenanceTempDir(t)
	project := filepath.Join(home, demoInstallProjectID)
	settings := filepath.Join(home, ".datatug.yaml")
	marker, err := json.Marshal(map[string]string{"projectId": demoInstallProjectID, "chinookSha256": demofixtures.ChinookSHA256})
	require.NoError(t, err)
	maintenanceWrite(t, filepath.Join(project, demoInstallMarker), marker)
	config := fmt.Appendf(nil, "projects:\n  - id: datatug-demo\n    path: %q\n", project)
	maintenanceWrite(t, settings, config)
	plan, err := demoResetPlan(project, settings)
	require.NoError(t, err)
	maintenanceWrite(t, settings, append(config, []byte("futureField: concurrent\n")...))
	require.ErrorContains(t, plan.Operations[0].run(), "settings file changed")
	require.DirExists(t, project)
	require.NoDirExists(t, plan.Operations[0].Target)
}

func TestLocalMaintenanceReportDoesNotClaimFailedOperations(t *testing.T) {
	var output bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&output)
	report := localReport{
		Mode: "preview",
		Operations: []localOperation{
			{Component: "web-ui", Action: "remove", run: func() error { return nil }},
			{Component: "cli", Action: "remove", run: func() error { return fmt.Errorf("cannot remove") }},
		},
	}
	require.ErrorContains(t, finishLocalPlan(cmd, report, "json", true), "cannot remove")
	var got localReport
	require.NoError(t, json.Unmarshal(output.Bytes(), &got))
	assert.Equal(t, "incomplete", got.Mode)
	require.Len(t, got.Operations, 1)
	assert.Equal(t, "web-ui", got.Operations[0].Component)
}

func TestLocalMaintenanceDiskCommandUsesSettingsAndPrintsCounts(t *testing.T) {
	home := maintenanceTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("DATATUG_WEB_UI_DIR", filepath.Join(home, "web-ui"))
	project := filepath.Join(home, "personal")
	maintenanceWrite(t, filepath.Join(project, "project.txt"), []byte("project"))
	config := filepath.Join(home, ".datatug.yaml")
	maintenanceWrite(t, config, fmt.Appendf(nil, "projects:\n  - id: personal\n    path: %q\n", project))
	cmd := DatatugCommand()
	t.Cleanup(func() { dtconfig.SetConfigFilePath("") })
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--config", config, "disk"})
	require.NoError(t, cmd.Execute())
	assert.Contains(t, output.String(), "measure project personal: "+project+" (1 files, 1 directories, 7 bytes)")
	assert.Contains(t, output.String(), "disk total:")
}

func TestLocalMaintenanceDemoResetStaleRegistration(t *testing.T) {
	home := maintenanceTempDir(t)
	project := filepath.Join(home, demoInstallProjectID)
	config := filepath.Join(home, ".datatug.yaml")
	maintenanceWrite(t, config, fmt.Appendf(nil, "projects:\n  - id: datatug-demo\n    path: %q\n", project))
	plan, err := demoResetPlan(project, config)
	require.NoError(t, err)
	require.Len(t, plan.Operations, 1)
	require.Equal(t, "edit", plan.Operations[0].Action)
	require.NoError(t, plan.Operations[0].run())
	plan, err = demoResetPlan(project, config)
	require.NoError(t, err)
	assert.Empty(t, plan.Operations)
}

func TestLocalMaintenanceDemoResetConflicts(t *testing.T) {
	home := maintenanceTempDir(t)
	project := filepath.Join(home, demoInstallProjectID)
	config := filepath.Join(home, ".datatug.yaml")
	maintenanceWrite(t, filepath.Join(project, "unmanaged.txt"), []byte("personal"))
	_, err := demoResetPlan(project, config)
	require.ErrorContains(t, err, "without its demo install marker")
	maintenanceWrite(t, filepath.Join(project, demoInstallMarker), []byte("{}"))
	_, err = demoResetPlan(project, config)
	require.ErrorContains(t, err, "not the default installed demo")
	marker, err := json.Marshal(map[string]string{"projectId": demoInstallProjectID, "chinookSha256": demofixtures.ChinookSHA256})
	require.NoError(t, err)
	maintenanceWrite(t, filepath.Join(project, demoInstallMarker), marker)

	for _, scenario := range []struct{ settings, errorText string }{
		{"projects: {other: true}", "must be a YAML sequence"},
		{"projects: []\nprojects: []\n", "multiple projects keys"},
		{"projects: []\n---\nfuture: yes\n", "multi-document"},
		{"projects:\n  - id: datatug-demo\n    path: /other\n", "conflicting ID or path"},
		{"projects:\n  - id: personal\n    path: " + fmt.Sprintf("%q", project), "conflicting ID or path"},
	} {
		maintenanceWrite(t, config, []byte(scenario.settings))
		_, err := demoResetPlan(project, config)
		require.ErrorContains(t, err, scenario.errorText)
		require.FileExists(t, filepath.Join(project, "unmanaged.txt"))
	}
}

func TestLocalMaintenanceDelegatesOnlyToNamedPluginManager(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test executable uses POSIX sh")
	}
	home := maintenanceTempDir(t)
	t.Setenv("PATH", home)
	trace := filepath.Join(home, "calls")
	t.Setenv("DATATUG_TEST_PLUGIN_TRACE", trace)
	pluginCLI := filepath.Join(home, "codex")
	maintenanceWrite(t, pluginCLI, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$DATATUG_TEST_PLUGIN_TRACE\"\n"))
	require.NoError(t, os.Chmod(pluginCLI, 0o700))
	plan, err := skillRemoval(context.Background(), "codex")
	require.NoError(t, err)
	require.Equal(t, "delegate", plan.Action)
	require.NoFileExists(t, trace)
	require.NoError(t, plan.run())
	raw, err := os.ReadFile(trace)
	require.NoError(t, err)
	assert.Equal(t, "plugin\nremove\ndatatug@datatug\n", string(raw))
}

func TestLocalMaintenanceDemoResetCommandPreviewsWithoutChanges(t *testing.T) {
	home := maintenanceTempDir(t)
	t.Setenv("HOME", home)
	project := filepath.Join(home, "datatug", demoInstallProjectID)
	config := filepath.Join(home, ".datatug.yaml")
	maintenanceWrite(t, filepath.Join(project, demoInstallMarker), fmt.Appendf(nil,
		"{\"projectId\":\"datatug-demo\",\"chinookSha256\":%q}", demofixtures.ChinookSHA256))
	maintenanceWrite(t, config, fmt.Appendf(nil, "projects:\n  - id: datatug-demo\n    path: %q\n", project))
	dtconfig.SetConfigFilePath(config)
	t.Cleanup(func() { dtconfig.SetConfigFilePath("") })
	run := func(args ...string) (string, error) {
		cmd := demoResetCommand()
		var output bytes.Buffer
		cmd.SetOut(&output)
		cmd.SetErr(&output)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return output.String(), err
	}
	_, err := run("--format", "bad")
	require.ErrorContains(t, err, "use --format")
	text, err := run("--dry-run")
	require.NoError(t, err)
	require.Contains(t, text, "move demo: "+project)
	require.Contains(t, text, "preview total: 0 files, 0 directories, 0 logical bytes")
	require.DirExists(t, project)
	_, err = run("--apply")
	require.NoError(t, err)
	require.NoDirExists(t, project)
}

func TestLocalMaintenanceRefusesSymlinkedInstallPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs extra Windows privileges")
	}
	home := maintenanceTempDir(t)
	real := filepath.Join(home, "real")
	alias := filepath.Join(home, "alias")
	require.NoError(t, os.Mkdir(real, 0o700))
	require.NoError(t, os.Symlink(real, alias))
	_, err := openLocalParent(filepath.Join(alias, "managed"))
	require.ErrorContains(t, err, "symlinked path")
	_, err = planWebUIRemoval(webui.Store{Dir: alias})
	require.ErrorContains(t, err, "symlinked web UI")
	path := filepath.Join(real, "link")
	require.NoError(t, os.Symlink(home, path))
	_, err = planWebUIRemoval(webui.Store{Dir: real})
	require.ErrorContains(t, err, "contains a symlink")
	_, err = cliRemoval(filepath.Join(home, "missing"), filepath.Join(home, "missing"), "darwin")
	require.Error(t, err)

	brew := filepath.Join(home, "Caskroom", "datatug", "1", "datatug")
	maintenanceWrite(t, brew, []byte("managed"))
	op, err := cliRemoval(brew, filepath.Join(home, "datatug"), "darwin")
	require.NoError(t, err)
	assert.Equal(t, []string{"brew", "uninstall", "--cask", "datatug"}, op.Command)
	assert.Nil(t, op.run)
}

func TestLocalMaintenanceResetRejectsUnownedAndDamagedSettings(t *testing.T) {
	home := maintenanceTempDir(t)
	project := filepath.Join(home, demoInstallProjectID)
	config := filepath.Join(home, ".datatug.yaml")
	maintenanceWrite(t, project, []byte("not a directory"))
	_, err := demoResetPlan(project, config)
	require.ErrorContains(t, err, "non-directory demo")
	require.NoError(t, os.Remove(project))
	_, err = demoResetPlan(project, config)
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(config, 0o700))
	_, err = demoResetPlan(project, config)
	require.ErrorContains(t, err, "non-regular settings")
	require.NoError(t, os.Remove(config))
	for _, scenario := range []struct{ content, want string }{
		{"projects: [broken", "cannot parse settings"},
		{"- not a map", "must contain a YAML mapping"},
		{"projects: [bad]", "cannot parse a project registration"},
	} {
		maintenanceWrite(t, config, []byte(scenario.content))
		_, err = demoResetPlan(project, config)
		require.ErrorContains(t, err, scenario.want)
	}
}

func TestLocalMaintenanceUninstallCannotProveBinaryOwnership(t *testing.T) {
	home := maintenanceTempDir(t)
	restoreVar(t, &localExecutable)
	restoreVar(t, &localHomeDir)
	binary := filepath.Join(home, "other-bin", "datatug")
	maintenanceWrite(t, binary, []byte("unknown installation"))
	localExecutable = func() (string, error) { return "", fs.ErrPermission }
	_, err := planCLIRemoval()
	require.ErrorIs(t, err, fs.ErrPermission)
	_, err = diskInventory()
	require.ErrorIs(t, err, fs.ErrPermission)
	localExecutable = func() (string, error) { return binary, nil }
	localHomeDir = func() (string, error) { return "", fs.ErrPermission }
	_, err = planCLIRemoval()
	require.ErrorIs(t, err, fs.ErrPermission)
	op, err := cliRemoval(binary, filepath.Join(home, ".local", "bin", "datatug"), "windows")
	require.NoError(t, err)
	assert.Nil(t, op.run)

	t.Setenv("LOCALAPPDATA", home)
	win := filepath.Join(home, "DataTug", "bin", "datatug.exe")
	maintenanceWrite(t, win, []byte("exe"))
	op, err = cliRemoval(win, "", "windows")
	require.NoError(t, err)
	assert.Equal(t, []string{"Remove-Item", "-LiteralPath", win}, op.Command)
	assert.Nil(t, op.run)
	require.FileExists(t, win)
}

func TestLocalMaintenanceInventorySurfacesWalkFailure(t *testing.T) {
	home := maintenanceTempDir(t)
	restoreVar(t, &localWalkDir)
	localWalkDir = func(root string, visit fs.WalkDirFunc) error {
		return visit(filepath.Join(root, "inaccessible"), nil, fs.ErrPermission)
	}
	_, err := inventoryTree(home, nil)
	require.ErrorIs(t, err, fs.ErrPermission)
	report := diskLocations(localReport{}, []localOperation{{Component: "project", Path: home}})
	assert.Empty(t, report.Operations)
	assert.Equal(t, []string{"Could not measure " + home}, report.Notes)
	_, err = planWebUIRemoval(webui.Store{Dir: home})
	require.ErrorIs(t, err, fs.ErrPermission)
	_, err = demoResetPlan(home, filepath.Join(home, "settings"))
	require.ErrorIs(t, err, fs.ErrPermission)
}

func TestLocalMaintenanceAppliesOnlyReportedActions(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetOut(&maintenanceBrokenWriter{})
	err := finishLocalPlan(cmd, localReport{Operations: []localOperation{{Action: "manual"}}}, "json", true)
	require.ErrorIs(t, err, fs.ErrPermission)
}

type maintenanceBrokenWriter struct{}

func (*maintenanceBrokenWriter) Write([]byte) (int, error) { return 0, fs.ErrPermission }

func TestLocalMaintenanceWebUIDamagedStore(t *testing.T) {
	home := maintenanceTempDir(t)
	store := webui.Store{Dir: home}
	state := filepath.Join(home, "state.json")
	maintenanceWrite(t, state, []byte("{"))
	_, err := planWebUIRemoval(store)
	require.ErrorContains(t, err, "damaged")
	require.NoError(t, os.Remove(state))
	op, err := planWebUIRemoval(store)
	require.NoError(t, err)
	assert.Nil(t, op, "an empty directory is not owned")
	maintenanceWrite(t, filepath.Join(home, "versions"), []byte("keep"))
	_, err = planWebUIRemoval(store)
	require.Error(t, err)
	require.FileExists(t, filepath.Join(home, "versions"))
}

func TestLocalMaintenanceRefusesToRestoreOverAReplacement(t *testing.T) {
	home := maintenanceTempDir(t)
	project := filepath.Join(home, demoInstallProjectID)
	backup := "demo-backup"
	target := filepath.Join(backup, demoInstallProjectID)
	maintenanceWrite(t, filepath.Join(home, target, "old"), []byte("old"))
	maintenanceWrite(t, filepath.Join(project, "replacement"), []byte("new"))
	require.ErrorContains(t, restoreDemo(project, backup, target), "original path is in use")
	require.FileExists(t, filepath.Join(home, target, "old"))
	require.FileExists(t, filepath.Join(project, "replacement"))
}
