package commands

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/gosuri/uitable"
	"github.com/urfave/cli/v3"
)

func showCommandArgs() *cli.Command {
	return &cli.Command{
		Name:        "show",
		Usage:       "Displays project data",
		Description: "Outputs project data in human readable format",
		Action: func(ctx context.Context, c *cli.Command) error {
			v := &showProjectCommand{}
			return v.Execute(nil)
		},
	}
}

// showProjectCommand defines parameters for show project consoleCommand
type showProjectCommand struct {
	projectBaseCommand
}

// Execute executes show project consoleCommand
func (v *showProjectCommand) Execute(_ []string) error {
	if err := v.initProjectCommand(projectCommandOptions{projNameOrDirRequired: true}); err != nil {
		return err
	}
	project, err := v.store.GetProjectStore(v.projectID).LoadProject(context.Background())
	if err != nil {
		return fmt.Errorf("failed to load project from [%v]: %w", v.ProjectDir, err)
	}
	var wd string
	if wd, err = os.Getwd(); err != nil {
		return err
	}
	w := os.Stdout
	_, _ = fmt.Fprintln(w, "GetProjectStore: ", path.Join(wd, v.ProjectDir))
	for _, env := range project.Environments {
		_, _ = fmt.Fprintln(w, "\t🌎 Environment: ", env.ID)
		for _, dbServer := range env.DbServers {
			_, _ = fmt.Fprintln(w, "\t\t🛢️🛢️ DB server: ", dbServer.GetID())
			for _, db := range dbServer.Catalogs {
				_, _ = fmt.Fprintln(w, "\t\t\t🛢️ DB: ", db)
			}
		}
	}
	_, _ = fmt.Fprintln(w, "DB drivers: ", len(project.DbDrivers))
	for _, dbDriver := range project.DbDrivers {
		_, _ = fmt.Fprintln(w, "\t\t🛢️ Driver: ", dbDriver.ID)
		for _, dbServer := range dbDriver.Servers {
			_, _ = fmt.Fprintf(w, "\t🛢️🛢️ %v: %v\n", dbServer.Server.Driver, dbServer.Server.Address())
			for _, db := range dbServer.Catalogs {
				_, _ = fmt.Fprintln(w, "\t\t🛢️ Server: ", db.ID)
				for _, schema := range db.Schemas {
					_, _ = fmt.Fprintln(w, "\t\t\t schema: ", db.ID)
					printCols := func(t *datatug.CollectionInfo) {
						if len(t.Columns) > 0 {
							table := uitable.New()
							for _, c := range t.Columns {
								s := strings.ToUpper(c.DbType)
								if c.CharMaxLength != nil && c.DbType != "text" {
									s = fmt.Sprintf("%v(%v)", s, *c.CharMaxLength)
								}
								table.AddRow("\t\t\t\t\t\t"+c.Name, s)
							}
							_, _ = fmt.Fprintf(w, "\t\t\t\t\tColumns (%v):\n", len(t.Columns))
							_, _ = fmt.Fprintln(w, table.String())
						}
					}
					printTable := func(singular string, t *datatug.CollectionInfo) {
						_, _ = fmt.Fprintf(w, "\t\t\t\t📄 %v: %s.%s\n", singular, t.Schema(), t.Name())
						if t.PrimaryKey != nil {
							_, _ = fmt.Fprintf(w, "\t\t\t\t\t🔑 Primary key: %s (%s)\n", t.PrimaryKey.Name, strings.Join(t.PrimaryKey.Columns, ", "))
						}
						if len(t.ForeignKeys) > 0 {
							_, _ = fmt.Fprintf(w, "\t\t\t\t\t🔗 Foreign keys (%d):", len(t.ForeignKeys))
							for _, fk := range t.ForeignKeys {
								_, _ = fmt.Fprintf(w, "\t\t\t\t\t\t (%v) %s.%s @ %v\n", strings.Join(fk.Columns, ", "), fk.RefTable.Schema(), fk.RefTable.Name(), fk.Name)
							}
						}
						for _, refBy := range t.ReferencedBy {
							for _, fk := range refBy.ForeignKeys {
								_, _ = fmt.Fprintf(w, "\t\t\t\t\t📎 Referenced by: %s.%s (%s) @ %v\n", refBy.Schema(), refBy.Name(), strings.Join(fk.Columns, ", "), fk.Name)
							}
						}
						printCols(t)
					}
					for _, t := range schema.Tables {
						printTable("Table", t)
					}
					for _, t := range schema.Views {
						printTable("View", t)
					}
				}
			}
		}
	}

	return err
}
