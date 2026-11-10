package commands

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/datatug/datatug-cli/pkg/demofixtures"
	"github.com/datatug/datatug-cli/pkg/dtroot"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var (
	demoReadRootFile = (*os.Root).ReadFile
	demoOpenRootFile = (*os.Root).OpenFile
	demoMarshalYAML  = yaml.Marshal
)

func demoResetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "reset", Short: "Preview or archive the default local Perimeter demo",
		Long: "Move the demo installed at ~/datatug/datatug-demo into a numbered sibling backup " +
			"and remove only its matching registration. Run demo install again for a clean demo.",
		Args: cobra.NoArgs,
		RunE: demoResetAction,
	}
	cmd.Flags().Bool("apply", false, "Archive and unregister the demo; otherwise only preview")
	cmd.Flags().Bool("dry-run", false, "Only preview the reset (the default)")
	cmd.Flags().String("format", "text", "Output format: text or json")
	cmd.MarkFlagsMutuallyExclusive("apply", "dry-run")
	return cmd
}

func demoResetAction(cmd *cobra.Command, _ []string) error {
	format, err := localFormat(cmd)
	if err != nil {
		return err
	}
	apply, _ := cmd.Flags().GetBool("apply")
	report, err := demoResetPlan(filepath.Join(dtroot.Path(), demoInstallProjectID), dtconfig.GetConfigFilePath())
	if err != nil {
		return err
	}
	return finishLocalPlan(cmd, report, format, apply)
}

func demoResetPlan(dir, settingsPath string) (localReport, error) {
	report := localReport{Mode: "preview", Operations: []localOperation{},
		Notes: []string{"The demo backup includes its audit and any edits. Moving it does not free disk space."}}
	tree, err := inventoryTree(dir, nil)
	if err != nil {
		return report, err
	}
	if len(tree.entries) == 0 {
		before, after, err := demoSettingsEdit(settingsPath, dir)
		if err != nil {
			return report, err
		}
		if after != nil {
			report.add(localOperation{
				Component: "demo registration", Action: "edit", Path: settingsPath,
				Note: "Remove the matching registration for the missing default demo folder.",
				run: func() error {
					if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
						return fmt.Errorf("the demo folder changed: %s", dir)
					}
					return replaceDemoSettings(settingsPath, before, after)
				},
			})
		}
		return report, nil
	}
	if !tree.entries[0].info.IsDir() {
		return report, fmt.Errorf("refusing to reset a non-directory demo path: %s", dir)
	}
	root, err := openLocalParent(dir)
	if err != nil {
		return report, err
	}
	defer func() { _ = root.Close() }()
	project := filepath.Base(dir)
	markerPath := filepath.Join(project, demoInstallMarker)
	metadata, err := root.Lstat(filepath.Dir(markerPath))
	if err != nil || !metadata.IsDir() {
		return report, fmt.Errorf("refusing to reset %s without its demo install marker", dir)
	}
	markerInfo, err := root.Lstat(markerPath)
	if err != nil || !markerInfo.Mode().IsRegular() {
		return report, fmt.Errorf("refusing to reset %s without a regular demo install marker", dir)
	}
	marker, err := demoReadRootFile(root, markerPath)
	if err != nil {
		return report, fmt.Errorf("refusing to reset %s without its demo install marker: %w", dir, err)
	}
	var ownership struct {
		ProjectID     string `json:"projectId"`
		ChinookSHA256 string `json:"chinookSha256"`
	}
	if json.Unmarshal(marker, &ownership) != nil || ownership.ProjectID != demoInstallProjectID || ownership.ChinookSHA256 != demofixtures.ChinookSHA256 {
		return report, fmt.Errorf("refusing to reset %s: it is not the default installed demo", dir)
	}
	backup, err := nextDemoBackup(root, project)
	if err != nil {
		return report, err
	}
	before, after, err := demoSettingsEdit(settingsPath, dir)
	if err != nil {
		return report, err
	}
	target := filepath.Join(backup, project)
	report.add(localOperation{
		Component: "demo", Action: "move", Path: dir, Target: filepath.Join(filepath.Dir(dir), target), Usage: &tree.usage,
		run: func() error {
			if err := moveDemo(dir, backup, target, tree.entries[0].info, marker); err != nil {
				return err
			}
			if after != nil {
				if err := replaceDemoSettings(settingsPath, before, after); err != nil {
					return errors.Join(err, restoreDemo(dir, backup, target))
				}
			}
			return nil
		},
	})
	if after != nil {
		report.add(localOperation{
			Component: "demo registration", Action: "edit", Path: settingsPath, Note: "Remove only the default demo registration.",
			run: func() error { return nil }, // Reported separately; the move commits both steps.
		})
	}
	return report, nil
}

func nextDemoBackup(root *os.Root, project string) (string, error) {
	for number := 1; ; number++ {
		name := fmt.Sprintf("%s.reset-%d", project, number)
		if _, err := localRootStat(root, name); errors.Is(err, fs.ErrNotExist) {
			return name, nil
		} else if err != nil {
			return "", err
		}
	}
}

func moveDemo(dir, backup, target string, original fs.FileInfo, marker []byte) error {
	root, err := openLocalParent(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	project := filepath.Base(dir)
	info, err := root.Lstat(project)
	if err != nil || !os.SameFile(info, original) {
		return fmt.Errorf("demo directory changed: %s", dir)
	}
	current, err := root.ReadFile(filepath.Join(project, demoInstallMarker))
	if err != nil || !bytes.Equal(current, marker) {
		return fmt.Errorf("demo install marker changed: %s", dir)
	}
	if err := root.Mkdir(backup, 0o700); err != nil {
		return err
	}
	if err := root.Rename(project, target); err != nil {
		_ = root.Remove(backup)
		return err
	}
	return nil
}

func restoreDemo(dir, backup, target string) error {
	root, err := openLocalParent(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if _, err := root.Lstat(filepath.Base(dir)); !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("demo remains in %s: the original path is in use", filepath.Join(filepath.Dir(dir), target))
	}
	if err := root.Rename(target, filepath.Base(dir)); err != nil {
		return fmt.Errorf("demo remains in %s: %w", filepath.Join(filepath.Dir(dir), target), err)
	}
	return root.Remove(backup)
}

// Edit the YAML tree so unknown settings, including credentials and future
// config fields, are preserved. The report never prints YAML contents.
func demoSettingsEdit(path, dir string) ([]byte, []byte, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("refusing to edit a non-regular settings file: %s", path)
	}
	before, err := localReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var doc yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(before))
	if err := decoder.Decode(&doc); errors.Is(err, io.EOF) {
		return nil, nil, nil
	} else if err != nil {
		return nil, nil, fmt.Errorf("cannot parse settings at %s", path)
	}
	if err := decoder.Decode(&yaml.Node{}); !errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf("refusing to edit a multi-document settings file: %s", path)
	}
	changed, err := removeDemoRegistration(&doc, dir)
	if err != nil || !changed {
		return nil, nil, err
	}
	after, err := demoMarshalYAML(&doc)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot encode updated settings at %s", path)
	}
	var verified yaml.Node
	if err := yaml.Unmarshal(after, &verified); err != nil {
		return nil, nil, fmt.Errorf("removing demo registration would invalidate settings at %s", path)
	}
	return before, after, nil
}

func removeDemoRegistration(doc *yaml.Node, dir string) (bool, error) {
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return false, fmt.Errorf("settings must contain a YAML mapping")
	}
	root := doc.Content[0]
	var projects *yaml.Node
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == "projects" {
			if projects != nil {
				return false, fmt.Errorf("settings contain multiple projects keys")
			}
			projects = root.Content[i+1]
		}
	}
	if projects == nil {
		return false, nil
	}
	if projects.Kind != yaml.SequenceNode {
		return false, fmt.Errorf("settings projects must be a YAML sequence")
	}
	match := -1
	for i, node := range projects.Content {
		var project dtconfig.ProjectRef
		if err := node.Decode(&project); err != nil {
			return false, fmt.Errorf("cannot parse a project registration")
		}
		atDemoPath := filepath.Clean(project.Path) == dir
		if project.ID != demoInstallProjectID && !atDemoPath {
			if relative, err := filepath.Rel(dir, project.Path); err == nil && filepath.IsLocal(relative) {
				return false, fmt.Errorf("another registered project is inside the demo folder; no files were moved")
			}
			continue
		}
		if project.ID != demoInstallProjectID || !atDemoPath || match >= 0 {
			return false, fmt.Errorf("demo registration has a conflicting ID or path; no files were moved")
		}
		match = i
	}
	if match < 0 {
		return false, nil
	}
	projects.Content = append(projects.Content[:match], projects.Content[match+1:]...)
	return true, nil
}

func replaceDemoSettings(path string, before, after []byte) error {
	root, err := openLocalParent(path)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	name := filepath.Base(path)
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("settings file changed: %s", path)
	}
	tmp := ".datatug-reset-" + rand.Text()
	f, err := demoOpenRootFile(root, tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(tmp) }()
	_, writeErr := f.Write(after)
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	current, err := root.ReadFile(name)
	if err != nil || !bytes.Equal(current, before) {
		return fmt.Errorf("settings file changed: %s", path)
	}
	return root.Rename(tmp, name)
}
