package commands

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/demofixtures"
	"github.com/datatug/datatug-cli/pkg/dtroot"
	"github.com/datatug/datatug-cli/pkg/policyexceptions"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/spf13/cobra"
)

// The local Perimeter demo: `datatug demo install` writes a project with two
// small local sources (Chinook in SQLite, support notes in inGitDB), the
// curated agent policy and the sensitivity labels, all embedded in the
// binary, and registers it. It serves nothing.

const (
	demoInstallProjectID = "datatug-demo"
	demoInstallEnv       = "local"
	// demoInstallMarker marks a project folder demo install made, so a second
	// run never writes into someone else's project.
	demoInstallMarker = ".datatug/demo-install.json"
)

// Seams for tests.
var (
	demoInstallFiles     = demofixtures.Files
	demoInstallWriteFile = os.WriteFile
	demoInstallMkdirAll  = os.MkdirAll
)

type demoInstallSource struct {
	ID     string `json:"id"`
	Engine string `json:"engine"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type demoInstallResult struct {
	ProjectID  string              `json:"projectId"`
	ProjectDir string              `json:"projectDir"`
	Sources    []demoInstallSource `json:"sources"`
	Policy     string              `json:"policy"`
	Labels     string              `json:"labels"`
	Status     string              `json:"status"`
	Preserved  []string            `json:"preserved"`
	Next       map[string]string   `json:"next"`
	Note       string              `json:"note"`
}

func demoInstallCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the local Perimeter demo project (two small local databases; serves nothing, needs no network)",
		Long: "Writes a DataTug project with two local sources, chinook (the pinned Chinook SQLite database) and support " +
			"(an inGitDB database of synthetic support notes), the curated policy policies/agent.yaml for role agent and the " +
			"Meaning Graph labels datatug-labels.json (Customer.Email is pii), and registers it in ~/.datatug.yaml. Every file is " +
			"embedded in the binary and checked against its pinned sha256. A second run changes nothing (status unchanged); a file " +
			"you changed is kept and reported (preserved), except the Chinook database, which must match its pin or the install fails.\n\n" +
			"Local Perimeter keeps an honest agent inside the lines. A real security boundary needs DB-native permissions or DataTug Cloud approvals.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE:         demoInstallAction,
	}
	cmd.Flags().String("dir", "", "Project folder (default: ~/datatug/datatug-demo)")
	cmd.Flags().String("project-id", demoInstallProjectID, "Project ID to register")
	cmd.Flags().String("format", "text", "Output format: text or json")
	cmd.AddCommand(demoInstallFactoriesCommand())
	return cmd
}

func demoInstallAction(cmd *cobra.Command, _ []string) error {
	dir, _ := cmd.Flags().GetString("dir")
	projectID, _ := cmd.Flags().GetString("project-id")
	format, _ := cmd.Flags().GetString("format")
	if format != "text" && format != "json" {
		return Exit(fmt.Sprintf("unsupported --format %q: want text or json", format), exitCodeUsage)
	}
	if !dbcopy.IsPlainSourceID(projectID) || strings.ToLower(projectID) != projectID {
		return Exit("--project-id must be a plain lower-case name", exitCodeUsage)
	}
	if dir == "" {
		dir = filepath.Join(dtroot.Path(), demoInstallProjectID)
	}
	dir, err := absolutePath(dir)
	if err != nil {
		return Exit(fmt.Sprintf("cannot resolve --dir: %v", err), exitCodeUsage)
	}
	result, err := installDemo(cmd, dir, projectID)
	if err != nil {
		return err
	}
	return writeDemoInstallResult(cmd.OutOrStdout(), format, result)
}

// demoRegistration is where projectID is registered: "" when it is not.
func demoRegistration(projectID string) (string, error) {
	settings, err := dtconfig.GetSettings()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("failed to read %s: %w", dtconfig.ConfigFileName, err)
	}
	if p := settings.GetProjectConfig(projectID); p != nil {
		return p.Path, nil
	}
	return "", nil
}

func installDemo(cmd *cobra.Command, dir, projectID string) (demoInstallResult, error) {
	files, err := demoInstallFiles()
	if err != nil {
		return demoInstallResult{}, Exit(err.Error(), 1)
	}
	registered, err := demoRegistration(projectID)
	if err != nil {
		return demoInstallResult{}, err
	}
	if registered != "" && filepath.Clean(registered) != dir {
		return demoInstallResult{}, Exit(fmt.Sprintf("project %s is already registered at another folder: pass --project-id with another ID, or --dir with that folder", projectID), exitCodeUsage)
	}
	marker, _ := json.Marshal(map[string]string{"projectId": projectID, "chinookSha256": demofixtures.ChinookSHA256})
	_, projectErr := os.Stat(filepath.Join(dir, storage.ProjectSummaryFileName))
	existing, markerErr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(demoInstallMarker)))
	switch {
	case projectErr == nil && markerErr != nil:
		return demoInstallResult{}, Exit(fmt.Sprintf("%s holds a DataTug project that demo install did not make: pass another --dir", dbcopy.PathDisplay(dir)), exitCodeUsage)
	case projectErr == nil && !bytes.Equal(existing, marker):
		return demoInstallResult{}, Exit(fmt.Sprintf("%s holds a demo project of another ID or version: pass another --dir", dbcopy.PathDisplay(dir)), exitCodeUsage)
	}
	// A pinned fixture that differs on disk fails closed before anything is written.
	var missing []demofixtures.File
	var preserved []string
	for _, f := range files {
		current, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f.Path)))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			missing = append(missing, f)
		case err != nil:
			return demoInstallResult{}, Exit(fmt.Sprintf("cannot read %s: %v", f.Path, err), 1)
		case bytes.Equal(current, f.Content):
		case f.Fixture:
			return demoInstallResult{}, Exit(fmt.Sprintf("%s does not match its pinned sha256 %s: it was changed, so the install stops; move it away and run demo install again", f.Path, demofixtures.ChinookSHA256), 1)
		default:
			preserved = append(preserved, f.Path)
		}
	}
	status := "unchanged"
	if projectErr != nil {
		status = "installed"
		// The marker goes first: a run that fails after it can be repeated,
		// and the next run recognises the folder as its own.
		markerPath := filepath.Join(dir, filepath.FromSlash(demoInstallMarker))
		if err := demoInstallMkdirAll(filepath.Dir(markerPath), 0o755); err != nil {
			return demoInstallResult{}, fmt.Errorf("failed to write %s: %w", demoInstallMarker, err)
		}
		if err := demoInstallWriteFile(markerPath, marker, 0o644); err != nil {
			return demoInstallResult{}, fmt.Errorf("failed to write %s: %w", demoInstallMarker, err)
		}
		initCmd := initCommand()
		initCmd.SetContext(cmd.Context())
		if err := initCommandAction(initCmd, []string{projectID, dir}); err != nil {
			return demoInstallResult{}, fmt.Errorf("failed to create the demo project: %w", err)
		}
	}
	for _, f := range missing {
		target := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := demoInstallMkdirAll(filepath.Dir(target), 0o755); err != nil {
			return demoInstallResult{}, fmt.Errorf("failed to write %s: %w", f.Path, err)
		}
		if err := demoInstallWriteFile(target, f.Content, 0o644); err != nil {
			return demoInstallResult{}, fmt.Errorf("failed to write %s: %w", f.Path, err)
		}
		if status == "unchanged" {
			status = "repaired"
		}
	}
	fsStore, id := filestore.NewSingleProjectStore(dir, projectID)
	store := fsStore.GetProjectStore(id)
	for _, req := range []api.ProjectSourceRequest{
		{Environment: demoInstallEnv, ID: demofixtures.ChinookSource, SQLitePath: filepath.Join(dir, filepath.FromSlash(demofixtures.ChinookPath))},
		{Environment: demoInstallEnv, ID: demofixtures.SupportSource, IngitDBPath: filepath.Join(dir, filepath.FromSlash(demofixtures.SupportPath))},
	} {
		added, err := api.AddProjectSource(cmd.Context(), store, dir, req)
		if err != nil {
			return demoInstallResult{}, fmt.Errorf("failed to add source %s: %w", req.ID, err)
		}
		if added.Added && status == "unchanged" {
			status = "repaired"
		}
	}
	if registered == "" {
		if err := registerDemoProject(projectID, dir); err != nil {
			return demoInstallResult{}, fmt.Errorf("failed to register the demo project in %s: %w", dtconfig.ConfigFileName, err)
		}
		if status == "unchanged" {
			status = "repaired"
		}
	}
	return demoInstallResult{
		ProjectID: projectID, ProjectDir: dir, Status: status, Preserved: append([]string{}, preserved...),
		Sources: []demoInstallSource{
			{ID: demofixtures.ChinookSource, Engine: "sqlite", Path: filepath.Join(dir, filepath.FromSlash(demofixtures.ChinookPath)), SHA256: demofixtures.ChinookSHA256},
			{ID: demofixtures.SupportSource, Engine: "ingitdb", Path: filepath.Join(dir, filepath.FromSlash(demofixtures.SupportPath)), SHA256: treeSHA256(files, demofixtures.SupportPath)},
		},
		Policy: filepath.Join(dir, filepath.FromSlash(demofixtures.PolicyPath)),
		Labels: filepath.Join(dir, filepath.FromSlash(demofixtures.LabelsPath)),
		Next: map[string]string{
			"permittedRead": fmt.Sprintf("datatug query run --project %s --source chinook --from Customer --as agent --role agent --limit 3 --format json", projectID),
			"deniedRead":    fmt.Sprintf("datatug query run --project %s --source chinook --from Employee --as agent --role agent --limit 3 --format json", projectID),
			"supportNotes":  fmt.Sprintf("datatug query run --project %s --source support --from support-notes --as agent --role agent --format json", projectID),
		},
		Note: policyexceptions.HonestLimit,
	}, nil
}

// treeSHA256 is one hash of the files under prefix: each path and the hash of
// its content, in path order.
func treeSHA256(files []demofixtures.File, prefix string) string {
	h := sha256.New()
	for _, f := range files {
		if strings.HasPrefix(f.Path, prefix+"/") {
			_, _ = fmt.Fprintf(h, "%s\x00%s\n", f.Path, f.SHA256())
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeDemoInstallResult(w io.Writer, format string, result demoInstallResult) error {
	if format == "json" {
		data, _ := json.Marshal(result) // plain values
		_, err := fmt.Fprintf(w, "%s\n", data)
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Demo project %s %s at %s\n", result.ProjectID, result.Status, result.ProjectDir)
	for _, s := range result.Sources {
		fmt.Fprintf(&b, "  source %s (%s): %s\n", s.ID, s.Engine, s.Path)
	}
	fmt.Fprintf(&b, "  policy: %s\n  labels: %s\n", result.Policy, result.Labels)
	for _, p := range result.Preserved {
		fmt.Fprintf(&b, "  preserved (you changed it): %s\n", p)
	}
	fmt.Fprintf(&b, "Try: %s\n%s\n", result.Next["permittedRead"], result.Note)
	_, err := io.WriteString(w, b.String())
	return err
}
