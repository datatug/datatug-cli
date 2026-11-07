package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/user"
	"strings"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/datarolesfile"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/dataroles"
	"github.com/datatug/datatug-core/pkg/reconcile"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/spf13/cobra"
)

// Seams for tests.
var (
	rolesCurrentUser = user.Current
	rolesNow         = func() time.Time { return time.Now().UTC() }
)

func dbRolesCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "roles",
		Short: "Classify a database family's collections by data role (reference, master, configuration, …)",
		Long: "Data roles say what a collection is; their consistency rule says where it must agree across instances. " +
			"Classifications live in datatug-data-roles.json at the project root. Suggestions are made from schema alone " +
			"(no record is read) and never take effect until confirmed; confirmed rules are what `datatug reconcile --query` " +
			"uses when no --key is given.",
	}
	cmd.PersistentFlags().String("project", "", "Project folder or registered ID (default: the current folder's or the only registered project)")
	cmd.AddCommand(dbRolesSuggestCommand(), dbRolesListCommand(), dbRolesDecideCommand(dataroles.StateConfirmed), dbRolesDecideCommand(dataroles.StateDismissed))
	return cmd
}

// localProjectDir is the folder of the project --project names (a folder or
// a registered id), else of the current folder's or the only registered
// project.
func localProjectDir(cmd *cobra.Command) (string, error) {
	project, _ := cmd.Flags().GetString("project")
	if project == "" {
		name, dir, err := projectNameOf("")
		if err != nil {
			return "", errors.New("--project is required: run in a project folder, or have exactly one registered project")
		}
		project = name + dir
	}
	dir, _, _, err := resolveQueryProjectRef(project)
	return dir, err
}

func dbRolesSuggestCommand() *cobra.Command {
	return &cobra.Command{
		Use:          "suggest <dbModel>",
		Short:        "Suggest data roles for a DbModel's tables from schema alone and add new ones to datatug-data-roles.json",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !dbcopy.IsPlainSourceID(args[0]) {
				return Exit("the database model id must be a plain name", exitCodeUsage)
			}
			dir, err := localProjectDir(cmd)
			if err != nil {
				return err
			}
			modelID := args[0]
			schema, err := api.GetDbModelSchema(dir, modelID)
			if err != nil {
				return err
			}
			foreignKeys, err := filestore.LoadModelForeignKeys(dir, modelID)
			if err != nil {
				return err
			}
			indexes, err := filestore.LoadModelIndexes(dir, modelID)
			if err != nil {
				return err
			}
			collections := datarolesfile.Collections(schema, foreignKeys, indexes)
			if len(collections) == 0 {
				return Exit(fmt.Sprintf("database model %q has no stored tables: scan one of its catalogs first", modelID), 3)
			}
			suggestions := dataroles.Suggest(modelID, collections, rolesNow())
			added := 0
			doc, err := datarolesfile.Store{Dir: dir}.Update(cmd.Context(), func(doc *dataroles.Document) error {
				added = doc.Merge(suggestions)
				return nil
			})
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%d new suggestion(s) for %s; existing decisions are kept\n", added, modelID)
			writeClassifications(cmd.OutOrStdout(), doc.Classifications, modelID)
			return nil
		},
	}
}

func dbRolesListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "list [dbModel]",
		Short:        "List the project's data-role classifications",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := localProjectDir(cmd)
			if err != nil {
				return err
			}
			doc, err := datarolesfile.Store{Dir: dir}.Load()
			if err != nil {
				return err
			}
			if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
				body, _ := json.Marshal(doc) // plain values
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\n", body)
				return nil
			}
			writeClassifications(cmd.OutOrStdout(), doc.Classifications, argAt(args, 0))
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "Print the document as JSON")
	return cmd
}

func writeClassifications(w io.Writer, classifications []dataroles.Classification, family string) {
	for _, c := range classifications {
		if family != "" && c.Target.Family != family {
			continue
		}
		line := fmt.Sprintf("%-32s %-14s %-9s %s", c.Target, c.Role, c.State, c.Origin)
		if c.Confidence != "" {
			line += " (" + string(c.Confidence) + ")"
		}
		if c.Consistency != nil {
			line += fmt.Sprintf("  key %s, %s, presence %s", strings.Join(c.Consistency.Key, ","), c.Consistency.Mode, c.Consistency.Presence)
		}
		_, _ = fmt.Fprintln(w, line)
		for _, evidence := range c.Evidence {
			_, _ = fmt.Fprintf(w, "    - %s\n", evidence)
		}
	}
}

func dbRolesDecideCommand(state dataroles.State) *cobra.Command {
	verb := map[dataroles.State]string{dataroles.StateConfirmed: "confirm", dataroles.StateDismissed: "dismiss"}[state]
	cmd := &cobra.Command{
		Use:          verb + " <dbModel>/<collection>",
		Short:        strings.ToUpper(verb[:1]) + verb[1:] + " a data-role classification as the current user",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			family, collection, ok := strings.Cut(args[0], "/")
			if !ok || family == "" || collection == "" {
				return Exit("want <dbModel>/<collection>", exitCodeUsage)
			}
			dir, err := localProjectDir(cmd)
			if err != nil {
				return err
			}
			current, err := rolesCurrentUser()
			if err != nil || current.Username == "" {
				return errors.New("the current user cannot be determined: a decision names who made it")
			}
			role, _ := cmd.Flags().GetString("role")
			key, _ := cmd.Flags().GetStringSlice("key")
			reason, _ := cmd.Flags().GetString("reason")
			target := dataroles.Target{Family: family, Collection: collection}
			doc, err := datarolesfile.Store{Dir: dir}.Update(cmd.Context(), func(doc *dataroles.Document) error {
				var consistency *reconcile.Rules
				if len(key) > 0 {
					rules := reconcile.Rules{Mode: reconcile.ModeConsensus, Presence: reconcile.PresenceRequired}
					for _, c := range doc.Classifications {
						if c.Target == target && c.Consistency != nil {
							rules = *c.Consistency
						}
					}
					rules.Key = key
					consistency = &rules
				}
				return doc.Decide(target, state, current.Username, rolesNow(), dataroles.Role(role), consistency, reason)
			})
			if err != nil {
				return Exit(err.Error(), 1)
			}
			for _, c := range doc.Classifications {
				if c.Target == target {
					writeClassifications(cmd.OutOrStdout(), []dataroles.Classification{c}, "")
				}
			}
			return nil
		},
	}
	cmd.Flags().String("role", "", "Replace the suggested role")
	cmd.Flags().StringSlice("key", nil, "Set the consistency rule's semantic key columns")
	cmd.Flags().String("reason", "", "Why")
	return cmd
}
