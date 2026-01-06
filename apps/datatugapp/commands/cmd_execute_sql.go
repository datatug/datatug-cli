package commands

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"strings"

	"github.com/datatug/datatug-cli/pkg/datatug-core/dbconnection"
	"github.com/datatug/sql2csv"
	"github.com/google/uuid"
	"github.com/gosuri/uitable"
	"github.com/strongo/validation"
	"github.com/urfave/cli/v3"
)

func updateUrlConfigCommandArgs() *cli.Command {
	return &cli.Command{
		Name:        "updateUrlConfig",
		Usage:       "Executes query or a consoleCommand",
		Description: "The `updateUrlConfig` consoleCommand executes consoleCommand or query. Like an SQL query or an SQL stored procedure.",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "driver", Aliases: []string{"D"}, Usage: "SQL driver"},
			&cli.StringFlag{Name: "host", Aliases: []string{"h"}, Usage: "Database host", Value: "localhost"},
			&cli.StringFlag{Name: "mode", Usage: "rw - ReadWrite, ro - ReadOnly (default for SQLite)"},
			&cli.StringFlag{Name: "port", Usage: "Database port"},
			&cli.StringFlag{Name: "user", Aliases: []string{"U"}, Usage: "Database user"},
			&cli.StringFlag{Name: "password", Aliases: []string{"P"}, Usage: "Database password"},
			&cli.StringFlag{Name: "project", Aliases: []string{"p"}, Usage: "DataTug project ID"},
			&cli.StringFlag{Name: "schema", Aliases: []string{"s"}, Usage: "Database schema"},
			&cli.StringFlag{Name: "query", Aliases: []string{"q"}, Usage: "SQL query"},
			&cli.StringFlag{Name: "consoleCommand-text", Aliases: []string{"t"}, Usage: "SQL command text"},
			&cli.StringFlag{Name: "output-path", Aliases: []string{"o"}, Usage: "Output path"},
			&cli.StringFlag{Name: "output-format", Aliases: []string{"f"}, Usage: "Output format (csv)", Value: "csv"},
		},
		Action: updateUrlConfigCommandAction,
	}
}

// executeSQLCommand defines parameters for updateUrlConfig SQL consoleCommand
type executeSQLCommand struct {
	Driver       string
	Host         string
	Mode         string
	Port         string
	User         string
	Password     string
	Project      string
	Schema       string
	Query        string
	CommandText  string
	OutputPath   string
	OutputFormat string
}

func (v *executeSQLCommand) Validate() error {
	if v.Query != "" && v.CommandText != "" {
		return validation.NewBadRequestError(errors.New("either 'query' or 'consoleCommand-text' arguments should be specified but not both at the same time"))
	}
	return nil
}

// Execute - executes SQL consoleCommand
func (v *executeSQLCommand) Execute() error {
	fmt.Printf("Executing (%+v)\n", v)
	var err error

	var options []string

	if v.Port != "" {
		options = append(options, "port="+v.Port)
	}

	if v.Mode != "" {
		options = append(options, "mode="+v.Mode)
	}

	connString, err := dbconnection.NewConnectionString(v.Driver, v.Host, v.User, v.Password, v.Schema, options...)
	if err != nil {
		return err
	}

	var db *sql.DB

	log.Printf("Connecting to: %v\n", regexp.MustCompile("password=.+?(;|$)").ReplaceAllString(connString.String(), "password=******"))

	// Create connection pool

	if db, err = sql.Open(v.Driver, connString.String()); err != nil {
		log.Fatal("Error creating connection pool: " + err.Error())
	}
	// Close the database connection pool after consoleCommand executes
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("Failed to close DB: %v", err)
		}
	}()
	log.Println("Connected")

	if strings.HasPrefix(v.CommandText, "*=") {
		v.CommandText = "SELECT * FROM " + strings.TrimLeft(v.CommandText, "*=")
	}

	var rows *sql.Rows
	if rows, err = db.Query(v.CommandText); err != nil {
		log.Printf("Failed to updateUrlConfig %v: %v", v.CommandText, err)
		return err
	}
	defer func() {
		if err := rows.Close(); err != nil {
			log.Printf("Failed to close rows reader: %v", err)
		}
	}()

	var columnTypes []*sql.ColumnType
	if columnTypes, err = rows.ColumnTypes(); err != nil {
		return err
	}
	colNames := make([]interface{}, len(columnTypes))
	colSpec := make([]string, len(columnTypes))
	colTypeNames := make([]string, len(columnTypes))
	for i, colType := range columnTypes {
		colNames[i] = colType.Name()
		colTypeNames[i] = colType.DatabaseTypeName()
		colSpec[i] = fmt.Sprintf("%v: %v", colNames[i], colTypeNames[i])
	}
	log.Printf(`
----------
%v
----------
--	GetColumns:
--		%v
`, v.CommandText, strings.Join(colSpec, "\n--\t\t"))

	var handler rowsHandler
	if v.OutputPath != "" {
		handler = csvHandler{path: v.OutputPath}
	} else {
		handler = writerHandler{os.Stdout}
	}
	return handler.Process(columnTypes, rows)
}

type rowsHandler interface {
	Process(columnTypes []*sql.ColumnType, rows *sql.Rows) (err error)
}

type csvHandler struct {
	path string
}

func (handler csvHandler) Process(_ []*sql.ColumnType, rows *sql.Rows) (err error) {
	converter := sql2csv.NewConverter(rows)
	return converter.WriteFile(handler.path)
}

type writerHandler struct {
	w io.Writer
}

func (handler writerHandler) Process(columnTypes []*sql.ColumnType, rows *sql.Rows) (err error) {

	//if colNames, err = rows.GetColumns(); err != nil {
	//	return err
	//}

	//if rowsAffected, err := result.RowsAffected(); err != nil {
	//	return err
	//} else {
	//	log.Printf("%v rows affected", rowsAffected)
	//}

	// https://kylewbanks.com/blog/query-result-to-map-in-golang

	values := make([]interface{}, len(columnTypes))
	valPointers := make([]interface{}, len(values))
	for i := range columnTypes {
		valPointers[i] = &values[i]
	}

	row := make([]interface{}, len(values))

	table := uitable.New()
	table.MaxColWidth = 50

	colNames := make([]interface{}, len(columnTypes))
	for i, colType := range columnTypes {
		colNames[i] = colType.Name()
	}

	table.AddRow(colNames...)
	for i, colName := range colNames {
		colNames[i] = strings.Repeat("-", len(colName.(string)))
	}
	table.AddRow(colNames...)
	for rows.Next() {
		if err = rows.Scan(valPointers...); err != nil {
			return err
		}
		for i, val := range values {
			switch columnTypes[i].DatabaseTypeName() {
			case "UNIQUEIDENTIFIER":
				var v uuid.UUID
				{
				}
				if v, err = uuid.FromBytes(val.([]byte)); err != nil {
					return err
				}
				row[i] = v.String()
			default:
				row[i] = val
			}
		}
		table.AddRow(row...)
	}
	if _, err = fmt.Fprintf(handler.w, "%v records:\n%v\n", len(table.Rows), table); err != nil {
		return err
	}

	return err
}

func updateUrlConfigCommandAction(_ context.Context, cmd *cli.Command) error {
	v := &executeSQLCommand{
		Driver:       cmd.String("driver"),
		Host:         cmd.String("host"),
		Mode:         cmd.String("mode"),
		Port:         cmd.String("port"),
		User:         cmd.String("user"),
		Password:     cmd.String("password"),
		Project:      cmd.String("project"),
		Schema:       cmd.String("schema"),
		Query:        cmd.String("query"),
		CommandText:  cmd.String("consoleCommand-text"),
		OutputPath:   cmd.String("output-path"),
		OutputFormat: cmd.String("output-format"),
	}
	if err := v.Validate(); err != nil {
		return err
	}
	return v.Execute()
}
