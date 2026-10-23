package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/meaninggraph"
	"github.com/spf13/cobra"
)

type dataModelResolver interface {
	Resolve(context.Context, datatug.DataModelReference) (*meaninggraph.Resolution, error)
}

type dataModelResolverFactory func(meaninggraph.Config) (dataModelResolver, error)

func dataModelsCommand() *cobra.Command {
	return dataModelsCommandWithResolver(func(config meaninggraph.Config) (dataModelResolver, error) {
		return meaninggraph.NewResolver(config)
	})
}

func dataModelsCommandWithResolver(factory dataModelResolverFactory) *cobra.Command {
	command := &cobra.Command{
		Use: "data-models", Short: "Read pinned logical data models from MeaningGraph",
	}
	resolve := &cobra.Command{
		Use: "resolve", Short: "Resolve an exact DataModel reference without querying or changing a project",
		Long: "Resolve a project DataModel reference, an environment/catalog assignment, or an explicit address and source commit. " +
			"The registry Git commit and SHA-256 of its exact catalog.json bytes are required. " +
			"Models are independent of project meanings and physical dbModels. Resolution reads metadata only. " +
			"A local fixture snapshot is development input, not proof of public Git admission.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			format, _ := command.Flags().GetString("format")
			switch format {
			case "json", "definition", "modelspec-json", "modelspec-hcl":
			default:
				return Exit("--format must be json, definition, modelspec-json or modelspec-hcl", exitCodeUsage)
			}
			reference, err := dataModelReferenceForCommand(command)
			if err != nil {
				return err
			}
			if err := reference.Validate(); err != nil {
				return Exit(err.Error(), exitCodeUsage)
			}
			registryCommit, _ := command.Flags().GetString("registry-commit")
			catalogSHA256, _ := command.Flags().GetString("catalog-sha256")
			fixtureRoot, _ := command.Flags().GetString("fixture-root")
			resolver, err := factory(meaninggraph.Config{RegistryCommit: registryCommit, CatalogSHA256: catalogSHA256, FixtureRoot: fixtureRoot})
			if err != nil {
				return Exit(fmt.Sprintf("invalid registry snapshot: %v", err), exitCodeUsage)
			}
			resolved, err := resolver.Resolve(command.Context(), reference)
			if err != nil {
				return fmt.Errorf("resolve DataModel: %w", err)
			}
			if resolved == nil {
				return fmt.Errorf("resolver returned no DataModel")
			}
			var body []byte
			switch format {
			case "definition":
				body = resolved.Root.Definition
			case "modelspec-json":
				body = resolved.Root.SourceJSON
			case "modelspec-hcl":
				body = resolved.Root.SourceHCL
			default:
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetEscapeHTML(false)
				return encoder.Encode(struct {
					Schema string `json:"schema"`
					*meaninggraph.Resolution
				}{Schema: "datatug-data-model-resolution/v1", Resolution: resolved})
			}
			_, err = command.OutOrStdout().Write(body)
			return err
		},
	}
	flags := resolve.Flags()
	flags.StringP("project", "p", "", "Registered project id/name")
	flags.StringP("directory", "d", "", "Local project directory (default: current directory)")
	flags.String("reference", "", "Project-local DataModel reference alias")
	flags.String("environment", "", "Environment of an explicitly assigned catalog")
	flags.String("catalog", "", "Catalog folder key, not a database filename")
	flags.String("address", "", "Unpinned modelspec:// module address (direct-reference mode)")
	flags.String("source-commit", "", "Exact full source commit (direct-reference mode)")
	flags.String("registry-commit", "", "Exact full MeaningGraph registry Git commit")
	flags.String("catalog-sha256", "", "SHA-256 of exact catalog.json bytes, without prefix")
	flags.String("fixture-root", "", "Read a local fixture snapshot instead of GitHub (development only)")
	flags.String("format", "json", "Output: json, definition, modelspec-json or modelspec-hcl")
	command.AddCommand(resolve)
	return command
}

func dataModelReferenceForCommand(command *cobra.Command) (datatug.DataModelReference, error) {
	flags := command.Flags()
	value := func(name string) string { result, _ := flags.GetString(name); return result }
	address, sourceCommit := value("address"), value("source-commit")
	project, directory := value("project"), value("directory")
	alias, environment, catalog := value("reference"), value("environment"), value("catalog")
	if address != "" || sourceCommit != "" {
		if address == "" || sourceCommit == "" || project != "" || directory != "" || alias != "" || environment != "" || catalog != "" {
			return datatug.DataModelReference{}, Exit("direct mode requires --address and --source-commit, without project selectors", exitCodeUsage)
		}
		return datatug.DataModelReference{Address: address, Commit: sourceCommit}, nil
	}
	if project != "" && directory != "" || (alias != "" && (environment != "" || catalog != "")) || (alias == "" && (environment == "" || catalog == "")) {
		return datatug.DataModelReference{}, Exit("select --reference or both --environment and --catalog; choose --project or --directory", exitCodeUsage)
	}
	if project == "" && directory == "" {
		var err error
		if directory, err = os.Getwd(); err != nil {
			return datatug.DataModelReference{}, err
		}
		// Some platforms can return the removed working directory's old name.
		// Preserve the filesystem failure rather than misreporting a project.
		if _, err = os.Stat(directory); err != nil {
			return datatug.DataModelReference{}, err
		}
	}
	base, err := openProjectForRead(project, directory)
	if err != nil {
		return datatug.DataModelReference{}, err
	}
	file, err := base.store.GetProjectStore(base.projectID).LoadProjectFile(command.Context())
	if err != nil {
		return datatug.DataModelReference{}, fmt.Errorf("read project metadata: %w", err)
	}
	if file.DataModels == nil {
		return datatug.DataModelReference{}, Exit("project has no logical DataModel references", exitCodeNotFound)
	}
	var reference datatug.DataModelReference
	if alias != "" {
		reference, err = file.DataModels.ReferenceByID(alias)
	} else {
		reference, err = file.DataModels.ReferenceForCatalog(environment, catalog)
	}
	if err != nil {
		return datatug.DataModelReference{}, Exit(err.Error(), exitCodeNotFound)
	}
	return reference, nil
}
