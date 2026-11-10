package commands

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/datatug/datatug-cli/pkg/dtroot"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/spf13/cobra"
)

type localUsage struct {
	Bytes       int64 `json:"bytes"`
	Files       int   `json:"files"`
	Directories int   `json:"directories"`
}

func (u *localUsage) add(other localUsage) {
	u.Bytes += other.Bytes
	u.Files += other.Files
	u.Directories += other.Directories
}

type localEntry struct {
	name string
	info fs.FileInfo
}

type localTree struct {
	path    string
	entries []localEntry
	usage   localUsage
}

var (
	localWalkDir    = filepath.WalkDir
	localExecutable = os.Executable
	localHomeDir    = os.UserHomeDir
)

// inventoryTree never traverses symlinks. Removal callers pass a nil covered
// set to capture entries. Disk reporting supplies a covered set to exclude
// already measured paths and keeps only the counts.
func inventoryTree(path string, covered map[string]bool) (localTree, error) {
	tree := localTree{path: path}
	err := localWalkDir(path, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			if name == path && errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if covered[name] {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if covered == nil {
			rel, err := filepath.Rel(filepath.Dir(path), name)
			if err != nil {
				return err
			}
			tree.entries = append(tree.entries, localEntry{rel, info})
		}
		if info.IsDir() {
			tree.usage.Directories++
		} else {
			tree.usage.Files++
			if info.Mode().IsRegular() {
				tree.usage.Bytes += info.Size()
			}
		}
		return nil
	})
	return tree, err
}

// openLocalParent rejects symlinked paths before destructive operations. The
// opened parent confines all subsequent operations to that directory.
func openLocalParent(path string) (*os.Root, error) {
	parent := filepath.Dir(path)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return nil, err
	}
	// An alias is not an ownership proof.
	if resolved != parent {
		return nil, fmt.Errorf("refusing to change a symlinked path: %s", path)
	}
	return os.OpenRoot(parent)
}

func (t localTree) remove() error {
	parent, err := openLocalParent(t.path)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	first := t.entries[0]
	if err := checkRemovalEntry(parent, first, first.name); err != nil {
		return err
	}
	if !first.info.IsDir() {
		return parent.Remove(first.name)
	}
	root, err := parent.OpenRoot(first.name)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := checkRemovalEntry(root, first, "."); err != nil {
		return err
	}
	prefix := first.name + string(filepath.Separator)
	entries := t.entries[1:]
	for _, entry := range entries {
		if err := checkRemovalEntry(root, entry, strings.TrimPrefix(entry.name, prefix)); err != nil {
			return err
		}
	}
	for _, entry := range slices.Backward(entries) {
		name := strings.TrimPrefix(entry.name, prefix)
		if err := checkRemovalEntry(root, entry, name); err != nil {
			return err
		}
		if err := root.Remove(name); err != nil {
			return err
		}
	}
	if err := checkRemovalEntry(parent, first, first.name); err != nil {
		return err
	}
	return parent.Remove(first.name)
}

func checkRemovalEntry(root *os.Root, entry localEntry, name string) error {
	info, err := root.Lstat(name)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, entry.info) ||
		(!info.IsDir() && (info.Size() != entry.info.Size() || !info.ModTime().Equal(entry.info.ModTime()))) {
		return fmt.Errorf("path changed during cleanup: %s", filepath.Join(root.Name(), name))
	}
	return nil
}

type localOperation struct {
	Component string      `json:"component"`
	Action    string      `json:"action"`
	Path      string      `json:"path,omitempty"`
	Target    string      `json:"target,omitempty"`
	Usage     *localUsage `json:"usage,omitempty"`
	Command   []string    `json:"command,omitempty"`
	Note      string      `json:"note,omitempty"`
	run       func() error
}

type localReport struct {
	Mode       string           `json:"mode"`
	Operations []localOperation `json:"operations"`
	Total      localUsage       `json:"total"`
	Notes      []string         `json:"notes,omitempty"`
}

func (r *localReport) add(op localOperation) {
	r.Operations = append(r.Operations, op)
	if op.Usage != nil && op.Action != "move" {
		r.Total.add(*op.Usage)
	}
}

func printLocalReport(w io.Writer, r localReport, format string) error {
	if format == "json" {
		return writeJSON(w, r)
	}
	for _, op := range r.Operations {
		_, _ = fmt.Fprintf(w, "%s %s", op.Action, op.Component)
		if op.Path != "" {
			_, _ = fmt.Fprintf(w, ": %s", op.Path)
		}
		if op.Target != "" && op.Path != "" {
			_, _ = fmt.Fprintf(w, " → %s", op.Target)
		} else if op.Target != "" {
			_, _ = fmt.Fprintf(w, ": %s", op.Target)
		}
		if op.Usage != nil {
			_, _ = fmt.Fprintf(w, " (%d files, %d directories, %d bytes)", op.Usage.Files, op.Usage.Directories, op.Usage.Bytes)
		} else if len(op.Command) > 0 {
			_, _ = fmt.Fprintf(w, " (%v; size unknown)", op.Command)
		}
		if op.Note != "" {
			_, _ = fmt.Fprintf(w, " — %s", op.Note)
		}
		_, _ = fmt.Fprintln(w)
	}
	for _, note := range r.Notes {
		_, _ = fmt.Fprintln(w, note)
	}
	_, err := fmt.Fprintf(w, "%s total: %d files, %d directories, %d logical bytes\n", r.Mode, r.Total.Files, r.Total.Directories, r.Total.Bytes)
	return err
}

func localFormat(cmd *cobra.Command) (string, error) {
	format, _ := cmd.Flags().GetString("format")
	if format != "text" && format != "json" {
		return "", Exit("use --format text or json", exitCodeUsage)
	}
	return format, nil
}

func diskCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "disk", Short: "Show disk usage of the CLI, web UI, settings and local projects",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := localFormat(cmd)
			if err != nil {
				return err
			}
			report, err := diskInventory()
			if err != nil {
				return err
			}
			return printLocalReport(cmd.OutOrStdout(), report, format)
		},
	}
	cmd.Flags().String("format", "text", "Output format: text or json")
	return cmd
}

func diskInventory() (localReport, error) {
	report := localReport{Mode: "disk", Operations: []localOperation{}}
	exe, err := localExecutable()
	if err != nil {
		return report, err
	}
	locations := []localOperation{
		{Component: "cli", Path: exe},
		{Component: "web-ui", Path: webUIStore().Dir},
		{Component: "settings", Path: dtconfig.GetConfigFilePath()},
	}
	settings, err := dtconfig.GetSettings()
	if err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, io.EOF) {
		report.Notes = append(report.Notes, "Could not read registered projects from the settings file.")
	}
	for _, project := range settings.Projects {
		if project != nil && filepath.IsAbs(project.Path) {
			locations = append(locations, localOperation{Component: "project " + project.ID, Path: project.Path})
		}
	}
	locations = append(locations, localOperation{Component: "other DataTug files", Path: dtroot.Path()})
	return diskLocations(report, locations), nil
}

func diskLocations(report localReport, locations []localOperation) localReport {
	for i := range locations {
		path, err := filepath.EvalSymlinks(locations[i].Path)
		if err == nil {
			locations[i].Path = path
		}
	}
	// Count descendants before their containing directory, independent of
	// registration order, then count only the remainder of each ancestor.
	slices.SortStableFunc(locations, func(a, b localOperation) int { return len(b.Path) - len(a.Path) })
	covered := make(map[string]bool)
	for _, op := range locations {
		tree, err := inventoryTree(op.Path, covered)
		if err != nil {
			report.Notes = append(report.Notes, "Could not measure "+op.Path)
			continue
		}
		if tree.usage.Files == 0 && tree.usage.Directories == 0 {
			continue
		}
		covered[op.Path] = true
		op.Action, op.Usage = "measure", &tree.usage
		report.add(op)
	}
	return report
}
