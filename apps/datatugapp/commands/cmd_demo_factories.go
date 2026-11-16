package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/demofixtures/factories"
	"github.com/datatug/datatug-cli/pkg/dtroot"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/spf13/cobra"
)

const (
	demoFactoriesProjectID = "datatug-factories"
	// demoFactoriesMarker marks a folder the factories demo made, so a
	// second run regenerates it and never writes into another project.
	demoFactoriesMarker = ".datatug/demo-factories.json"
)

// Seams for failure tests.
var (
	demoFactoriesGenerate     = factories.Generate
	demoFactoriesRegistration = demoRegistration
	demoFactoriesRegister     = registerDemoProject
	demoFactoriesInit         = initCommandAction
)

func demoInstallFactoriesCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "factories [dir]",
		Short: "Install the five-factory reconciliation demo (seven local SQLite databases; offline)",
		Long: "Writes a DataTug project with one application (database family factory) as five production factories and two " +
			"test copies, each a SQLite database with a few deliberate, documented differences, plus saved queries, " +
			"reconciliation profiles and the Check reference-data-agrees, and registers it. README.md in the project lists every " +
			"difference and the verdict `datatug reconcile` gives. A second run regenerates the demo in place.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE:         demoInstallFactoriesAction,
	}
	cmd.Flags().String("project-id", demoFactoriesProjectID, "Project ID to register")
	return cmd
}

func demoInstallFactoriesAction(cmd *cobra.Command, args []string) error {
	projectID, _ := cmd.Flags().GetString("project-id")
	if !dbcopy.IsPlainSourceID(projectID) || strings.ToLower(projectID) != projectID {
		return Exit("--project-id must be a plain lower-case name", exitCodeUsage)
	}
	dir := argAt(args, 0)
	if dir == "" {
		dir = filepath.Join(dtroot.Path(), demoFactoriesProjectID)
	}
	dir, err := absolutePath(dir)
	if err != nil {
		return Exit(fmt.Sprintf("cannot resolve the folder: %v", err), exitCodeUsage)
	}
	registered, err := demoFactoriesRegistration(projectID)
	if err != nil {
		return err
	}
	if registered != "" && filepath.Clean(registered) != dir {
		return Exit(fmt.Sprintf("project %s is already registered at another folder: pass --project-id with another ID, or that folder", projectID), exitCodeUsage)
	}
	marker, _ := json.Marshal(map[string]string{"projectId": projectID, "demo": "factories"})
	markerPath := filepath.Join(dir, filepath.FromSlash(demoFactoriesMarker))
	_, projectErr := os.Stat(filepath.Join(dir, storage.ProjectSummaryFileName))
	existing, markerErr := os.ReadFile(markerPath)
	switch {
	case projectErr == nil && markerErr != nil:
		return Exit(fmt.Sprintf("%s holds a DataTug project that the factories demo did not make: pass another folder", dbcopy.PathDisplay(dir)), exitCodeUsage)
	case projectErr == nil && string(existing) != string(marker):
		return Exit(fmt.Sprintf("%s holds a factories demo of another project ID: pass another folder", dbcopy.PathDisplay(dir)), exitCodeUsage)
	}
	status := "regenerated"
	if projectErr != nil {
		status = "installed"
		if err := demoInstallMkdirAll(filepath.Dir(markerPath), 0o755); err != nil {
			return fmt.Errorf("failed to write %s: %w", demoFactoriesMarker, err)
		}
		if err := demoInstallWriteFile(markerPath, marker, 0o644); err != nil {
			return fmt.Errorf("failed to write %s: %w", demoFactoriesMarker, err)
		}
		initCmd := initCommand()
		initCmd.SetContext(cmd.Context())
		if err := demoFactoriesInit(initCmd, []string{projectID, dir}); err != nil {
			return fmt.Errorf("failed to create the demo project: %w", err)
		}
	}
	fsStore, id := filestore.NewSingleProjectStore(dir, projectID)
	if err := demoFactoriesGenerate(cmd.Context(), dir, fsStore.GetProjectStore(id)); err != nil {
		return fmt.Errorf("failed to generate the factories demo: %w", err)
	}
	if registered == "" {
		if err := demoFactoriesRegister(projectID, dir); err != nil {
			return fmt.Errorf("failed to register the demo project in %s: %w", dtconfig.ConfigFileName, err)
		}
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Five-factory demo %s %s at %s\n  %d databases in %s; deliberate differences: %s\n"+
		"Try:\n  datatug serve --project %s --allow-writes\n  datatug reconcile --project %s --profile %s\n  datatug checks run %s --project %s\n",
		projectID, status, dir, len(factories.Instances), filepath.Join(dir, "data"), filepath.Join(dir, "README.md"),
		dir, projectID, factories.ProfileCountries, factories.CheckID, projectID)
	return nil
}
