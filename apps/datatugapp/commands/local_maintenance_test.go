package commands

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/demofixtures"
	"github.com/datatug/datatug-cli/pkg/dtroot"
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

func maintenanceWebUI(t *testing.T) (webui.Store, string, int64) {
	t.Helper()
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
	public, private, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	restore, err := webui.UseTrustedKeys(base64.StdEncoding.EncodeToString(public))
	require.NoError(t, err)
	t.Cleanup(restore)
	signature := fmt.Appendf(nil, "%s ed25519 %s %s\n", webui.SignatureFormat, webui.KeyID(public), base64.StdEncoding.EncodeToString(ed25519.Sign(private, raw)))
	maintenanceWrite(t, filepath.Join(version, webui.SignatureFile), signature)
	maintenanceWrite(t, filepath.Join(version, filepath.FromSlash(webui.IndexFile)), html)
	return store, version, int64(len(raw) + len(html) + len(signature))
}

func TestLocalMaintenanceWebUIRefusesExtraFiles(t *testing.T) {
	store, version, size := maintenanceWebUI(t)
	extra := filepath.Join(version, "my-file.txt")
	maintenanceWrite(t, extra, []byte("keep"))
	_, err := planWebUIRemoval(store)
	require.ErrorContains(t, err, "unrecognized path")
	require.FileExists(t, extra)
	require.NoError(t, os.Remove(extra))

	op, err := planWebUIRemoval(store)
	require.NoError(t, err)
	require.NotNil(t, op)
	assert.Equal(t, localUsage{Files: 3, Directories: 4, Bytes: size}, *op.Usage)
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
		{"projects:\n  - id: personal\n    path: " + fmt.Sprintf("%q", filepath.Join(project, "personal")), "another registered project"},
	} {
		maintenanceWrite(t, config, []byte(scenario.settings))
		_, err := demoResetPlan(project, config)
		require.ErrorContains(t, err, scenario.errorText)
		require.FileExists(t, filepath.Join(project, "unmanaged.txt"))
	}
	if runtime.GOOS != "windows" {
		child := filepath.Join(project, "personal")
		require.NoError(t, os.Mkdir(child, 0o700))
		alias := filepath.Join(home, "linked-personal")
		require.NoError(t, os.Symlink(child, alias))
		maintenanceWrite(t, config, fmt.Appendf(nil, "projects:\n  - id: personal\n    path: %q\n", alias))
		_, err := demoResetPlan(project, config)
		require.ErrorContains(t, err, "another registered project")
		info, err := os.Stat(alias)
		require.NoError(t, err)
		require.True(t, info.IsDir())
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
	assert.Empty(t, op.Command, "a copied Caskroom layout does not identify Homebrew")
	assert.Nil(t, op.run)

	restoreVar(t, &localEvalSymlinks)
	localEvalSymlinks = func(string) (string, error) {
		return "/opt/homebrew/Caskroom/datatug/1.0.0/datatug", nil
	}
	op, err = cliRemoval("/opt/homebrew/bin/datatug", "", "darwin")
	require.NoError(t, err)
	assert.Equal(t, []string{"/opt/homebrew/bin/brew", "uninstall", "--cask", "datatug"}, op.Command)
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

func TestLocalMaintenanceInvalidInputsDoNotApply(t *testing.T) {
	home := maintenanceTempDir(t)
	t.Setenv("HOME", home)
	for _, cmd := range []*cobra.Command{uninstallCommand(), demoResetCommand(), diskCommand()} {
		cmd.SetArgs([]string{"--format", "invalid"})
		require.ErrorContains(t, cmd.Execute(), "use --format text or json")
	}

	binary := filepath.Join(home, "parent", "datatug")
	_, err := cliRemoval(binary, binary, runtime.GOOS)
	require.ErrorIs(t, err, fs.ErrNotExist)
	require.NoError(t, os.MkdirAll(binary, 0o700))
	_, err = cliRemoval(binary, binary, "linux")
	require.ErrorContains(t, err, "no longer a regular file")

	restoreVar(t, &localExecutable)
	localExecutable = func() (string, error) { return "", fs.ErrPermission }
	uninstall := uninstallCommand()
	uninstall.SetArgs([]string{"--apply"})
	require.ErrorIs(t, uninstall.Execute(), fs.ErrPermission)
	disk := diskCommand()
	disk.SetArgs([]string{})
	require.ErrorIs(t, disk.Execute(), fs.ErrPermission)

	project := filepath.Join(dtroot.Path(), demoInstallProjectID)
	maintenanceWrite(t, project, []byte("personal file"))
	reset := demoResetCommand()
	reset.SetArgs([]string{"--apply"})
	require.ErrorContains(t, reset.Execute(), "non-directory demo")
	require.FileExists(t, project)
}

func TestLocalMaintenanceDemoResetRejectsChangedPreview(t *testing.T) {
	home := maintenanceTempDir(t)
	project := filepath.Join(home, demoInstallProjectID)
	config := filepath.Join(home, ".datatug.yaml")
	maintenanceWrite(t, config, fmt.Appendf(nil, "projects:\n  - id: datatug-demo\n    path: %q\n", project))
	stale, err := demoResetPlan(project, config)
	require.NoError(t, err)
	marker := filepath.Join(project, demoInstallMarker)
	require.NoError(t, os.MkdirAll(marker, 0o700))
	require.ErrorContains(t, stale.Operations[0].run(), "demo folder changed")
	_, err = demoResetPlan(project, config)
	require.ErrorContains(t, err, "regular demo install marker")
	require.NoError(t, os.Remove(marker))

	data := fmt.Appendf(nil, `{"projectId":%q,"chinookSha256":%q}`, demoInstallProjectID, demofixtures.ChinookSHA256)
	maintenanceWrite(t, marker, data)
	plan, err := demoResetPlan(project, config)
	require.NoError(t, err)
	maintenanceWrite(t, marker, []byte("{}"))
	require.ErrorContains(t, plan.Operations[0].run(), "marker changed")

	maintenanceWrite(t, marker, data)
	require.NoError(t, os.Mkdir(filepath.Dir(plan.Operations[0].Target), 0o700))
	require.ErrorIs(t, plan.Operations[0].run(), fs.ErrExist)
	second, err := demoResetPlan(project, config)
	require.NoError(t, err)
	assert.NotEqual(t, plan.Operations[0].Target, second.Operations[0].Target)

	require.NoError(t, os.Rename(project, filepath.Join(home, "original")))
	maintenanceWrite(t, marker, data)
	require.ErrorContains(t, second.Operations[0].run(), "directory changed")
	contents, err := os.ReadFile(config)
	require.NoError(t, err)
	assert.Contains(t, string(contents), "id: datatug-demo")
}

func TestLocalMaintenanceDemoResetRollbackErrors(t *testing.T) {
	home := maintenanceTempDir(t)
	missing := filepath.Join(home, "missing", demoInstallProjectID)
	require.ErrorIs(t, moveDemo(missing, "backup", "backup/demo", nil, nil), fs.ErrNotExist)
	require.ErrorIs(t, restoreDemo(missing, "backup", "backup/demo"), fs.ErrNotExist)
	project := filepath.Join(home, demoInstallProjectID)
	require.ErrorContains(t, restoreDemo(project, "backup", "backup/demo"), "demo remains in")
	require.NoDirExists(t, project)

	marker := []byte("owned")
	maintenanceWrite(t, filepath.Join(project, demoInstallMarker), marker)
	original, err := os.Stat(project)
	require.NoError(t, err)
	require.ErrorIs(t, moveDemo(project, "backup", "missing/demo", original, marker), fs.ErrNotExist)
	require.DirExists(t, project)
	require.NoDirExists(t, filepath.Join(home, "backup"))

	root, err := os.OpenRoot(home)
	require.NoError(t, err)
	require.NoError(t, root.Close())
	_, err = nextDemoBackup(root, demoInstallProjectID)
	require.Error(t, err)
}

func TestLocalMaintenanceDemoResetPreservesYAMLAliases(t *testing.T) {
	home := maintenanceTempDir(t)
	project := filepath.Join(home, demoInstallProjectID)
	config := filepath.Join(home, ".datatug.yaml")
	for _, data := range []string{"", "future: keep\n"} {
		maintenanceWrite(t, config, []byte(data))
		_, after, err := demoSettingsEdit(config, project)
		require.NoError(t, err)
		require.Nil(t, after)
	}
	data := fmt.Appendf(nil, "projects:\n  - &demo\n    id: datatug-demo\n    path: %q\nfuture: *demo\n", project)
	maintenanceWrite(t, config, data)
	_, _, err := demoSettingsEdit(config, project)
	require.ErrorContains(t, err, "would invalidate settings")
	require.ErrorIs(t, replaceDemoSettings(filepath.Join(home, "missing", "settings"), data, data), fs.ErrNotExist)
	require.ErrorContains(t, replaceDemoSettings(filepath.Join(home, "missing.yaml"), data, data), "settings file changed")
	content, err := os.ReadFile(config)
	require.NoError(t, err)
	assert.Equal(t, data, content)
}

func TestLocalMaintenanceDiskCoversNestedFiles(t *testing.T) {
	home := maintenanceTempDir(t)
	file := filepath.Join(home, "included")
	maintenanceWrite(t, file, []byte("data"))
	report := diskLocations(localReport{}, []localOperation{
		{Component: "folder", Path: home},
		{Component: "file", Path: file},
	})
	require.Len(t, report.Operations, 2)
	assert.Equal(t, localUsage{Bytes: 4, Files: 1, Directories: 1}, report.Total)
	var output bytes.Buffer
	require.NoError(t, printLocalReport(&output, localReport{Operations: []localOperation{
		{Action: "move", Component: "demo", Path: "old", Target: "backup"},
	}}, "text"))
	assert.Contains(t, output.String(), "old → backup")
}

func TestLocalMaintenanceWebUIListsAndVerifiesOwnedFiles(t *testing.T) {
	store, version, _ := maintenanceWebUI(t)
	t.Setenv(webui.DirEnv, store.Dir)
	state := filepath.Join(store.Dir, "state.json")
	maintenanceWrite(t, state, []byte(`{"active":"0.1.0"}`))
	asset := filepath.Join(version, filepath.FromSlash(webui.IndexFile))

	cmd := uninstallCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--web-ui", "--dry-run", "--format", "json"})
	require.NoError(t, cmd.Execute())
	var plan localReport
	require.NoError(t, json.Unmarshal(output.Bytes(), &plan))
	require.Len(t, plan.Operations, 1)
	require.Contains(t, plan.Operations[0].Paths, asset)
	require.Contains(t, plan.Operations[0].Paths, state)
	require.FileExists(t, asset)
	output.Reset()
	require.NoError(t, printLocalReport(&output, plan, "text"))
	require.Contains(t, output.String(), "  "+asset)

	maintenanceWrite(t, state, []byte(`{"pinned":"some unrelated value"}`))
	_, err := planWebUIRemoval(store)
	require.ErrorContains(t, err, "unrecognized version")
	maintenanceWrite(t, state, []byte(`{"future":"retain"}`))
	_, err = planWebUIRemoval(store)
	require.ErrorContains(t, err, "unrecognized fields")
	require.NoError(t, os.Remove(state))
	maintenanceWrite(t, filepath.Join(version, webui.SignatureFile), []byte("untrusted"))
	cmd = uninstallCommand()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--web-ui", "--apply"})
	require.ErrorContains(t, cmd.Execute(), "signature")
	require.FileExists(t, asset)
	require.NoError(t, os.Remove(filepath.Join(version, webui.SignatureFile)))
	_, err = planWebUIRemoval(store)
	require.ErrorContains(t, err, "cannot prove ownership")
	require.FileExists(t, asset)
}

func TestLocalMaintenanceFilesystemPlanErrors(t *testing.T) {
	home := maintenanceTempDir(t)
	binary := filepath.Join(home, "datatug")
	maintenanceWrite(t, binary, []byte("keep"))
	restoreVar(t, &localWalkDir)
	origWalk := localWalkDir
	localWalkDir = func(path string, visit fs.WalkDirFunc) error {
		return visit(path, nil, fs.ErrPermission)
	}
	_, err := cliRemoval(binary, binary, "linux")
	require.ErrorIs(t, err, fs.ErrPermission)
	localWalkDir = func(path string, visit fs.WalkDirFunc) error {
		return visit(path, maintenanceFailedDirEntry{}, nil)
	}
	_, err = inventoryTree(binary, nil)
	require.ErrorIs(t, err, fs.ErrPermission)
	localWalkDir = origWalk

	_, err = planWebUIRemoval(webui.Store{Dir: strings.Repeat("x", 4096)})
	require.Error(t, err)
	restoreVar(t, &localAbs)
	localAbs = func(string) (string, error) { return "", fs.ErrPermission }
	_, err = planWebUIRemoval(webui.Store{Dir: home})
	require.ErrorIs(t, err, fs.ErrPermission)
}

type maintenanceFailedDirEntry struct{ fs.DirEntry }

func (maintenanceFailedDirEntry) Info() (fs.FileInfo, error) { return nil, fs.ErrPermission }

func TestLocalMaintenanceWebUIRefusesUnreadableState(t *testing.T) {
	restoreVar(t, &localReadFile)
	localReadFile = func(string) ([]byte, error) { return nil, fs.ErrPermission }
	require.ErrorIs(t, checkWebUIStateOwnership("state.json"), fs.ErrPermission)
}

func TestLocalMaintenanceWebUIStageFailures(t *testing.T) {
	home := maintenanceTempDir(t)
	store := filepath.Join(home, "web-ui")
	asset := filepath.Join(store, "index.html")
	maintenanceWrite(t, asset, []byte("original"))
	tree, err := inventoryTree(store, nil)
	require.NoError(t, err)

	badPath := tree
	badPath.path = filepath.Join(home, "missing", "web-ui")
	require.ErrorIs(t, badPath.stageRemoval(), fs.ErrNotExist)
	require.ErrorIs(t, badPath.remove(), fs.ErrNotExist)
	badPath.path = filepath.Join(home, "other")
	require.ErrorContains(t, badPath.stageRemoval(), "path changed")

	restoreVar(t, &localRootMkdir)
	originalMkdir := localRootMkdir
	localRootMkdir = func(*os.Root, string, fs.FileMode) error { return fs.ErrPermission }
	require.ErrorIs(t, tree.stageRemoval(), fs.ErrPermission)
	localRootMkdir = originalMkdir
	restoreVar(t, &localRootRename)
	originalRename := localRootRename
	localRootRename = func(*os.Root, string, string) error { return fs.ErrPermission }
	require.ErrorIs(t, tree.stageRemoval(), fs.ErrPermission)
	localRootRename = originalRename
	require.FileExists(t, asset)
	matches, err := filepath.Glob(filepath.Join(home, ".datatug-uninstall-*"))
	require.NoError(t, err)
	require.Empty(t, matches, "failed staging left no extra directories")

	require.NoError(t, os.Remove(asset))
	require.ErrorContains(t, tree.stageRemoval(), "web UI detached; remaining files are at")
	require.NoDirExists(t, store)
	matches, err = filepath.Glob(filepath.Join(home, ".datatug-uninstall-*", "web-ui"))
	require.NoError(t, err)
	require.Len(t, matches, 1, "recovery path remains visible")
}

func TestLocalMaintenanceWebUIRemovalRefusesRaces(t *testing.T) {
	for _, target := range []string{"open", "root", "second file check", "final root check", "unplanned child"} {
		t.Run(target, func(t *testing.T) {
			home := maintenanceTempDir(t)
			dir := filepath.Join(home, "web-ui")
			folder := filepath.Join(dir, "browser")
			file := filepath.Join(folder, "index.html")
			maintenanceWrite(t, file, []byte("asset"))
			tree, err := inventoryTree(dir, nil)
			require.NoError(t, err)
			restoreVar(t, &localOpenChild)
			restoreVar(t, &localRootStat)
			if target == "open" {
				localOpenChild = func(*os.Root, string) (*os.Root, error) { return nil, fs.ErrPermission }
			}
			assetChecks, rootChecks := 0, 0
			localRootStat = func(root *os.Root, name string) (fs.FileInfo, error) {
				switch name {
				case ".":
					if target == "root" {
						return nil, fs.ErrNotExist
					}
				case filepath.Join("browser", "index.html"):
					assetChecks++
					if target == "second file check" && assetChecks == 2 {
						return nil, fs.ErrNotExist
					}
				case "web-ui":
					rootChecks++
					if target == "final root check" && rootChecks == 2 {
						return nil, fs.ErrNotExist
					}
				}
				return root.Lstat(name)
			}
			personal := filepath.Join(folder, "personal")
			if target == "unplanned child" {
				maintenanceWrite(t, personal, []byte("keep"))
			}
			require.Error(t, tree.remove())
			require.DirExists(t, dir)
			if target == "unplanned child" {
				require.FileExists(t, personal)
			}
		})
	}
}

func TestLocalMaintenanceDemoResetIOFailuresKeepSettings(t *testing.T) {
	home := maintenanceTempDir(t)
	project := filepath.Join(home, demoInstallProjectID)
	settings := filepath.Join(home, "settings.yaml")
	before := fmt.Appendf(nil, "projects:\n  - id: datatug-demo\n    path: %q\n", project)
	maintenanceWrite(t, settings, before)
	marker := fmt.Appendf(nil, `{"projectId":%q,"chinookSha256":%q}`, demoInstallProjectID, demofixtures.ChinookSHA256)
	maintenanceWrite(t, filepath.Join(project, demoInstallMarker), marker)

	_, _, err := demoSettingsEdit(settings+"\x00", project)
	require.Error(t, err)
	restoreVar(t, &localReadFile)
	originalReadFile := localReadFile
	localReadFile = func(string) ([]byte, error) { return nil, fs.ErrPermission }
	_, _, err = demoSettingsEdit(settings, project)
	require.ErrorIs(t, err, fs.ErrPermission)
	localReadFile = originalReadFile

	restoreVar(t, &demoReadRootFile)
	originalReadRootFile := demoReadRootFile
	demoReadRootFile = func(*os.Root, string) ([]byte, error) { return nil, fs.ErrPermission }
	_, err = demoResetPlan(project, settings)
	require.ErrorIs(t, err, fs.ErrPermission)
	demoReadRootFile = originalReadRootFile

	restoreVar(t, &localRootStat)
	originalStat := localRootStat
	localRootStat = func(*os.Root, string) (fs.FileInfo, error) { return nil, fs.ErrPermission }
	_, err = demoResetPlan(project, settings)
	require.ErrorIs(t, err, fs.ErrPermission)
	localRootStat = originalStat

	restoreVar(t, &demoMarshalYAML)
	demoMarshalYAML = func(any) ([]byte, error) { return nil, fs.ErrPermission }
	_, _, err = demoSettingsEdit(settings, project)
	require.ErrorContains(t, err, "cannot encode updated settings")

	restoreVar(t, &demoOpenRootFile)
	demoOpenRootFile = func(*os.Root, string, int, fs.FileMode) (*os.File, error) { return nil, fs.ErrPermission }
	require.ErrorIs(t, replaceDemoSettings(settings, before, nil), fs.ErrPermission)
	demoOpenRootFile = func(root *os.Root, name string, _ int, mode fs.FileMode) (*os.File, error) {
		return root.OpenFile(name, os.O_RDONLY|os.O_CREATE|os.O_EXCL, mode)
	}
	require.Error(t, replaceDemoSettings(settings, before, []byte("replacement")))
	got, err := os.ReadFile(settings)
	require.NoError(t, err)
	require.Equal(t, before, got)
	entries, err := filepath.Glob(filepath.Join(home, ".datatug-reset-*"))
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestLocalMaintenanceDemoResetRefusesSymlinkedParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs extra Windows privileges")
	}
	home := maintenanceTempDir(t)
	project := filepath.Join(home, "real", demoInstallProjectID)
	require.NoError(t, os.MkdirAll(project, 0o700))
	alias := filepath.Join(home, "alias")
	require.NoError(t, os.Symlink(filepath.Dir(project), alias))
	_, err := demoResetPlan(filepath.Join(alias, demoInstallProjectID), filepath.Join(home, "settings"))
	require.ErrorContains(t, err, "symlinked path")
	require.DirExists(t, project)
}

func TestLocalMaintenanceDiskReportsUnreadableRegistrations(t *testing.T) {
	home := maintenanceTempDir(t)
	config := filepath.Join(home, "settings.yaml")
	dtconfig.SetConfigFilePath(config)
	t.Cleanup(func() { dtconfig.SetConfigFilePath("") })
	maintenanceWrite(t, config, []byte("[invalid"))
	report, err := diskInventory()
	require.NoError(t, err)
	require.Contains(t, report.Notes, "Could not read registered projects from the settings file.")
	var output bytes.Buffer
	require.NoError(t, printLocalReport(&output, localReport{Operations: []localOperation{
		{Action: "manual", Component: "skills", Target: "codex", Command: []string{"codex", "plugin", "remove"}},
	}}, "text"))
	require.Contains(t, output.String(), "skills: codex")
	require.Contains(t, output.String(), "size unknown")
}

func TestLocalMaintenanceWebUIRecoveryKeepsUnplannedFiles(t *testing.T) {
	store, version, _ := maintenanceWebUI(t)
	plan, err := planWebUIRemoval(store)
	require.NoError(t, err)
	require.NotNil(t, plan)

	personal := filepath.Join(version, webui.BrowserDir, "personal.txt")
	maintenanceWrite(t, personal, []byte("keep"))
	err = plan.run()
	require.ErrorContains(t, err, "web UI detached; remaining files are at")
	require.NoDirExists(t, store.Dir)
	rel, err := filepath.Rel(store.Dir, personal)
	require.NoError(t, err)
	preserved, err := filepath.Glob(filepath.Join(filepath.Dir(store.Dir), ".datatug-uninstall-*", filepath.Base(store.Dir), rel))
	require.NoError(t, err)
	require.Len(t, preserved, 1)
	contents, err := os.ReadFile(preserved[0])
	require.NoError(t, err)
	require.Equal(t, "keep", string(contents))

	// An interrupted store is outside the active path and a new installation
	// can use that path without risking the recovered files.
	op, err := planWebUIRemoval(store)
	require.NoError(t, err)
	require.Nil(t, op)
	require.FileExists(t, preserved[0])
}
