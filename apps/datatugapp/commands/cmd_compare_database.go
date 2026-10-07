package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dal-go/dalgo/dal"
	bigquery "github.com/dal-go/dalgo2bigquery"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/bigqueryread"
	"github.com/datatug/datatug-cli/pkg/dbcompare"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/spf13/cobra"
)

func runDatabaseCompareCommand(cmd *cobra.Command, leftName, rightName string) error {
	if cmd.Flags().Changed(compareAgentFlag) || cmd.Flags().Changed(compareStoreFlag) ||
		cmd.Flags().Changed(compareQueryFlag) || cmd.Flags().Changed(compareLeftFlag) ||
		cmd.Flags().Changed(compareRightFlag) || cmd.Flags().Changed(compareKeyFlag) ||
		cmd.Flags().Changed(compareDistributionFlag) || cmd.Flags().Changed(compareIncidentFlag) ||
		cmd.Flags().Changed(compareMutationFlag) {
		return Exit("database comparison does not use query-agent flags", exitCodeUsage)
	}
	project, _ := cmd.Flags().GetString(compareProjectFlag)
	if strings.TrimSpace(project) == "" {
		return Exit("database comparison requires --project <project-directory-or-id>", exitCodeUsage)
	}
	projectDir, projectStore, err := resolveQueryProject(project)
	if err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	defaultEnvironment, _ := cmd.Flags().GetString(compareEnvironmentFlag)
	leftEnvironment, _ := cmd.Flags().GetString(compareLeftEnvFlag)
	rightEnvironment, _ := cmd.Flags().GetString(compareRightEnvFlag)
	if leftEnvironment == "" {
		leftEnvironment = defaultEnvironment
	}
	if rightEnvironment == "" {
		rightEnvironment = defaultEnvironment
	}
	leftEnvID, err := resolveQueryEnvironment(ctx, projectStore, leftEnvironment)
	if err != nil {
		return Exit("left environment "+dbcopy.SourceIDDisplay(leftEnvironment)+": "+dbcopy.RedactText(err.Error()), exitCodeUsage)
	}
	rightEnvID, err := resolveQueryEnvironment(ctx, projectStore, rightEnvironment)
	if err != nil {
		return Exit("right environment "+dbcopy.SourceIDDisplay(rightEnvironment)+": "+dbcopy.RedactText(err.Error()), exitCodeUsage)
	}
	leftSchema, _ := cmd.Flags().GetString(compareLeftSchemaFlag)
	rightSchema, _ := cmd.Flags().GetString(compareRightSchemaFlag)
	leftSource, _ := cmd.Flags().GetString(compareLeftSourceFlag)
	rightSource, _ := cmd.Flags().GetString(compareRightSourceFlag)
	tableMapValues, _ := cmd.Flags().GetStringSlice(compareTableMapFlag)
	keyMapValues, _ := cmd.Flags().GetStringSlice(compareKeyMapFlag)
	mappingFile, _ := cmd.Flags().GetString(compareMappingFileFlag)
	tableMaps, err := parseComparisonTableMaps(tableMapValues)
	if err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}
	keyMaps, err := parseComparisonKeyMaps(keyMapValues)
	if err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}
	fileTableMaps, fileKeyMaps, err := readCompareMappingFile(projectDir, mappingFile)
	if err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}
	if err := mergeComparisonTableMaps(tableMaps, fileTableMaps); err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}
	if err := mergeComparisonKeyMaps(keyMaps, fileKeyMaps); err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}
	leftDB, leftURL, err := openProjectCompareDatabase(ctx, projectStore, projectDir, leftEnvID, leftName, leftSchema, leftSource)
	if err != nil {
		return Exit(fmt.Sprintf("open left database %q: %v", dbcopy.SourceIDDisplay(leftName), dbcopy.RedactText(err.Error())), exitCodeDatabase)
	}
	defer closeCompareDB(leftDB)
	rightDB, rightURL, err := openProjectCompareDatabase(ctx, projectStore, projectDir, rightEnvID, rightName, rightSchema, rightSource)
	if err != nil {
		return Exit(fmt.Sprintf("open right database %q: %v", dbcopy.SourceIDDisplay(rightName), dbcopy.RedactText(err.Error())), exitCodeDatabase)
	}
	defer closeCompareDB(rightDB)

	details, _ := cmd.Flags().GetBool(compareDetailsFlag)
	limit, _ := cmd.Flags().GetInt(compareLimitFlag)
	if limit < 0 {
		return Exit("--limit cannot be negative", exitCodeUsage)
	}
	report, err := dbcompare.Compare(ctx, compareReportName(leftEnvID, leftName), leftDB, compareReportName(rightEnvID, rightName), rightDB, dbcompare.Options{
		Details: details, DetailLimit: limit,
		LeftSchema: leftSchema, RightSchema: rightSchema,
		TableMaps: tableMaps, KeyMaps: keyMaps,
	})
	if err != nil {
		redacted := dbcopy.RedactErrorWithSecrets(err, leftURL)
		redacted = dbcopy.RedactErrorWithSecrets(redacted, rightURL)
		return Exit(redacted.Error(), exitCodeDatabase)
	}
	asJSON, _ := cmd.Flags().GetBool(compareJSONFlag)
	if asJSON {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return fmt.Errorf("write database comparison report: %w", err)
		}
		return nil
	}
	return writeDatabaseCompareText(cmd.OutOrStdout(), report, details)
}

func compareReportName(environment, database string) string {
	return dbcopy.SourceIDDisplay(environment) + "/" + dbcopy.SourceIDDisplay(database)
}

const maxCompareMappingFileBytes = 4 << 20

type comparisonMappingFile struct {
	Format    string                    `json:"format"`
	Relations []comparisonMappingRecord `json:"relations"`
}

type comparisonMappingRecord struct {
	Left       string   `json:"left"`
	Right      string   `json:"right"`
	KeyColumns []string `json:"keyColumns,omitempty"`
}

// readCompareMappingFile loads explicit source-to-target names. It does not
// infer correspondence from column names, row counts, or relation order.
func readCompareMappingFile(projectDir, path string) (map[string]string, map[string][]string, error) {
	if path == "" {
		return map[string]string{}, map[string][]string{}, nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(projectDir, path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, errors.New("comparison mapping file could not be opened")
	}
	defer func() { _ = file.Close() }()
	contents, err := io.ReadAll(io.LimitReader(file, maxCompareMappingFileBytes+1))
	if err != nil {
		return nil, nil, errors.New("comparison mapping file could not be read")
	}
	if len(contents) > maxCompareMappingFileBytes {
		return nil, nil, errors.New("comparison mapping file exceeds the 4 MiB limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var mapping comparisonMappingFile
	if err := decoder.Decode(&mapping); err != nil {
		return nil, nil, errors.New("comparison mapping file is not valid datatug-database-compare-mappings/v1 JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, nil, errors.New("comparison mapping file must contain exactly one JSON object")
	}
	if mapping.Format != "datatug-database-compare-mappings/v1" || len(mapping.Relations) == 0 {
		return nil, nil, errors.New("comparison mapping file must use datatug-database-compare-mappings/v1 and contain relations")
	}
	tableMappings := make(map[string]string, len(mapping.Relations))
	keyMappings := make(map[string][]string, len(mapping.Relations))
	rightNames := make(map[string]string, len(mapping.Relations))
	for _, relation := range mapping.Relations {
		if relation.Left == "" || relation.Right == "" || strings.TrimSpace(relation.Left) != relation.Left || strings.TrimSpace(relation.Right) != relation.Right {
			return nil, nil, errors.New("comparison mapping relation names must be non-empty and have no surrounding whitespace")
		}
		if _, exists := tableMappings[relation.Left]; exists {
			return nil, nil, fmt.Errorf("comparison mapping file repeats left relation %q", relation.Left)
		}
		if previous, exists := rightNames[relation.Right]; exists {
			return nil, nil, fmt.Errorf("comparison mapping file maps %q and %q to right relation %q", previous, relation.Left, relation.Right)
		}
		seenKeyColumns := make(map[string]bool, len(relation.KeyColumns))
		for _, column := range relation.KeyColumns {
			if strings.TrimSpace(column) != column || column == "" || seenKeyColumns[column] {
				return nil, nil, fmt.Errorf("comparison mapping file has an empty, whitespace-padded, or duplicate key column for %q", relation.Left)
			}
			seenKeyColumns[column] = true
		}
		tableMappings[relation.Left] = relation.Right
		rightNames[relation.Right] = relation.Left
		if len(relation.KeyColumns) > 0 {
			keyMappings[relation.Left] = append([]string(nil), relation.KeyColumns...)
		}
	}
	return tableMappings, keyMappings, nil
}

func mergeComparisonTableMaps(base, extra map[string]string) error {
	rightNames := make(map[string]string, len(base)+len(extra))
	for left, right := range base {
		rightNames[right] = left
	}
	for left, right := range extra {
		if _, exists := base[left]; exists {
			return fmt.Errorf("relation %q has both a --table-map flag and a mapping-file entry", left)
		}
		if previous, exists := rightNames[right]; exists {
			return fmt.Errorf("table mappings for %q and %q both target right relation %q", previous, left, right)
		}
		base[left] = right
		rightNames[right] = left
	}
	return nil
}

func mergeComparisonKeyMaps(base, extra map[string][]string) error {
	for relation, key := range extra {
		if _, exists := base[relation]; exists {
			return fmt.Errorf("relation %q has both a --key-map flag and a mapping-file key", relation)
		}
		base[relation] = append([]string(nil), key...)
	}
	return nil
}

func parseComparisonTableMaps(values []string) (map[string]string, error) {
	result := make(map[string]string, len(values))
	rightNames := make(map[string]string, len(values))
	for _, value := range values {
		left, right, ok := strings.Cut(value, "=")
		if !ok || strings.TrimSpace(left) != left || strings.TrimSpace(right) != right || left == "" || right == "" {
			return nil, fmt.Errorf("invalid --table-map %q; expected LEFT=RIGHT", value)
		}
		if _, exists := result[left]; exists {
			return nil, fmt.Errorf("--table-map was provided more than once for left relation %q", left)
		}
		if previous, exists := rightNames[right]; exists {
			return nil, fmt.Errorf("--table-map maps both %q and %q to right relation %q", previous, left, right)
		}
		result[left] = right
		rightNames[right] = left
	}
	return result, nil
}

func parseComparisonKeyMaps(values []string) (map[string][]string, error) {
	result := make(map[string][]string, len(values))
	for _, value := range values {
		table, columns, ok := strings.Cut(value, "=")
		if !ok || strings.TrimSpace(table) != table || table == "" || columns == "" {
			return nil, fmt.Errorf("invalid --key-map %q; expected TABLE=column[,column]", value)
		}
		if _, exists := result[table]; exists {
			return nil, fmt.Errorf("--key-map was provided more than once for table %q", table)
		}
		seen := make(map[string]bool)
		for _, column := range strings.Split(columns, ",") {
			column = strings.TrimSpace(column)
			if column == "" || seen[column] {
				return nil, fmt.Errorf("invalid or duplicate key column in --key-map for table %q", table)
			}
			seen[column] = true
			result[table] = append(result[table], column)
		}
	}
	return result, nil
}

func openProjectCompareDatabase(ctx context.Context, store datatug.ProjectStore, projectDir, envID, database, schema, sourceBinding string) (dal.DB, string, error) {
	if strings.TrimSpace(database) == "" {
		return nil, "", errors.New("database name is required")
	}
	var modernNotReady error
	var modernSourceErr error
	if resolver, ok := store.(datatug.EnvironmentConnectionResolver); ok {
		edition, resolveErr := resolver.ResolveEnvironmentConnection(ctx, envID, database)
		if resolveErr == nil || errors.Is(resolveErr, datatug.ErrConnectionNotReady) {
			if edition.ID != "" {
				if sourceURL, sourceErr := compareEditionSourceURL(edition, sourceBinding); sourceErr == nil {
					return openCompareSource(ctx, sourceURL, schema, edition.Storage)
				} else if sourceBinding != "" || strings.EqualFold(edition.Storage, "bigquery") {
					return nil, "", sourceErr
				} else {
					modernSourceErr = sourceErr
				}
				if errors.Is(resolveErr, datatug.ErrConnectionNotReady) {
					modernNotReady = resolveErr
				}
			} else if resolveErr != nil {
				// A planned connection has no executable identity and cannot be
				// made usable by a source binding.
				return nil, "", resolveErr
			}
		} else if !errors.Is(resolveErr, datatug.ErrConnectionNotFound) {
			return nil, "", resolveErr
		}
	}
	catalogs, err := store.LoadEnvDbCatalogs(ctx, envID)
	if err != nil {
		return nil, "", fmt.Errorf("load database catalogs for environment %q: %s", dbcopy.SourceIDDisplay(envID), dbcopy.RedactText(err.Error()))
	}
	var matches []datatug.DbCatalog
	for _, catalog := range catalogs {
		if catalog != nil && catalog.GetID() == database {
			matches = append(matches, *catalog)
		}
	}
	if len(matches) == 0 {
		if modernNotReady != nil {
			return nil, "", modernNotReady
		}
		if modernSourceErr != nil {
			return nil, "", modernSourceErr
		}
		return nil, "", fmt.Errorf("database %q is not configured in environment %q", dbcopy.SourceIDDisplay(database), dbcopy.SourceIDDisplay(envID))
	}
	if len(matches) != 1 {
		return nil, "", fmt.Errorf("database %q is ambiguous in environment %q (%d catalogs match)", dbcopy.SourceIDDisplay(database), dbcopy.SourceIDDisplay(envID), len(matches))
	}
	var sourceURL string
	if sourceBinding != "" {
		sourceURL, err = resolveCompareSourceBinding(sourceBinding)
	} else {
		sourceURL, err = compareCatalogSourceURL(matches[0], projectDir)
	}
	if err != nil {
		return nil, "", fmt.Errorf("resolve database source: %w", err)
	}
	return openCompareSource(ctx, sourceURL, schema, matches[0].Driver)
}

func compareEditionSourceURL(edition datatug.EditionConnection, sourceBinding string) (string, error) {
	if sourceBinding != "" {
		sourceURL, err := resolveCompareSourceBinding(sourceBinding)
		if err != nil {
			return "", err
		}
		ref, err := dbcopy.Parse(sourceURL)
		if err != nil || !compareDriverMatchesScheme(edition.Storage, ref.Scheme) {
			return "", errors.New("explicit source provider does not match the declared storage type")
		}
		return sourceURL, nil
	}
	if strings.EqualFold(edition.Storage, "bigquery") && edition.SourceProjectID != "" && edition.DatasetID != "" && edition.Location != "" {
		if !edition.ReadyForBinding() {
			return "", errors.New("BigQuery edition is not ready for direct read access")
		}
		return fmt.Sprintf("bigquery://%s/%s?location=%s", edition.SourceProjectID, edition.DatasetID, edition.Location), nil
	}
	return "", errors.New("this project edition has no executable source URL; provide an explicit --left-source or --right-source binding")
}

func openCompareSource(ctx context.Context, sourceURL, schema, expectedStorage string) (dal.DB, string, error) {
	ref, err := dbcopy.Parse(sourceURL)
	if err != nil {
		return nil, "", errors.New("database source URL is invalid or unsupported")
	}
	if expectedStorage != "" && !compareDriverMatchesScheme(expectedStorage, ref.Scheme) {
		return nil, "", errors.New("source provider does not match the configured database storage type")
	}
	ref = ref.WithFlag("database comparison source")
	if ref.Scheme == "bigquery" {
		db, err := openBigQueryCompareDatabase(ref)
		if err != nil {
			return nil, "", err
		}
		return db, sourceURL, nil
	}
	if schema != "" && ref.Scheme != "postgres" {
		return nil, "", errors.New("schema selection is supported only for PostgreSQL sources")
	}
	if err := dbcopy.CheckPostgresRead(ref, 0); err != nil {
		return nil, "", err
	}
	db, err := ref.OpenForCopy(ctx, schema)
	if err != nil {
		return nil, "", dbcopy.RedactErrorWithSecrets(err, sourceURL)
	}
	return db, sourceURL, nil
}

func compareDriverMatchesScheme(driver, scheme string) bool {
	name := strings.ToLower(strings.TrimSpace(driver))
	switch name {
	case "sqlite3":
		name = "sqlite"
	case "postgresql":
		name = "postgres"
	}
	return name == scheme
}

// resolveCompareSourceBinding resolves a caller-supplied DALgo source locator.
// Network DSNs should be passed by environment-variable reference (env:NAME)
// so credentials do not appear in command history or the process argument list.
func resolveCompareSourceBinding(binding string) (string, error) {
	binding = strings.TrimSpace(binding)
	if binding == "" {
		return "", errors.New("explicit source binding is empty")
	}
	fromEnv := false
	if strings.HasPrefix(binding, "env:") {
		name := strings.TrimPrefix(binding, "env:")
		if !dbcopy.ValidEnvName(name) {
			return "", errors.New("environment source binding must use env:VARIABLE with a valid variable name")
		}
		value, ok := os.LookupEnv(name)
		if !ok || strings.TrimSpace(value) == "" {
			return "", errors.New("explicit source environment variable is not set")
		}
		binding = value
		fromEnv = true
	}
	ref, err := dbcopy.Parse(binding)
	if err != nil {
		return "", errors.New("explicit source binding is not a supported DALgo source URL")
	}
	if !containsSourceScheme(dbcopy.SupportedSchemes(), ref.Scheme) {
		return "", errors.New("explicit source binding uses an unsupported DALgo provider")
	}
	if ref.Scheme == "postgres" && !fromEnv {
		return "", errors.New("PostgreSQL source bindings must use env:VARIABLE so credentials stay out of command history")
	}
	return binding, nil
}

func containsSourceScheme(schemes []string, scheme string) bool {
	for _, supported := range schemes {
		if supported == scheme {
			return true
		}
	}
	return false
}

func compareCatalogSourceURL(catalog datatug.DbCatalog, projectDir string) (string, error) {
	if strings.EqualFold(catalog.Driver, "bigquery") {
		if catalog.Path == "" {
			return "", errors.New("BigQuery catalog must contain a bigquery://PROJECT/DATASET?location=LOCATION source URL")
		}
		ref, err := dbcopy.Parse(catalog.Path)
		if err != nil || ref.Scheme != "bigquery" {
			return "", errors.New("BigQuery catalog source URL is invalid")
		}
		return catalog.Path, nil
	}
	return api.ResolveCatalogSourceURL(catalog, projectDir)
}

func openBigQueryCompareDatabase(ref dbcopy.BackendRef) (dal.DB, error) {
	if ref.Scheme != "bigquery" || ref.ProjectID == "" || ref.DatasetID == "" || ref.Location == "" {
		return nil, errors.New("BigQuery source must name one project, dataset, and location")
	}
	configDir, err := chatUserConfigDir()
	if err != nil {
		return nil, errors.New("BigQuery comparison requires the user's private configuration directory")
	}
	ledgerDir := filepath.Join(configDir, "datatug", "bigquery-readonly")
	provider, err := bigQueryDeps.provider(ledgerDir, false, "google")
	if err != nil {
		return nil, bigqueryread.ErrIdentity
	}
	db, err := bigquery.NewReadOnlyDatabase(bigquery.SourceConfig{
		ProjectID: ref.ProjectID,
		DatasetID: ref.DatasetID,
		Provider:  provider,
		Transport: bigQueryDeps.transport,
	})
	if err != nil {
		return nil, errors.New("could not initialize the read-only BigQuery source")
	}
	return db, nil
}

func closeCompareDB(db dal.DB) {
	if closer, ok := db.(io.Closer); ok {
		_ = closer.Close()
	}
}

func writeDatabaseCompareText(w io.Writer, report dbcompare.Report, details bool) error {
	if _, err := fmt.Fprintf(w, "%s -> %s: +%d added, -%d removed, ~%d changed, %d unchanged\n",
		report.LeftName, report.RightName, report.Summary.Added, report.Summary.Removed, report.Summary.Changed, report.Summary.Unchanged); err != nil {
		return err
	}
	for _, change := range report.SchemaChanges {
		if _, err := fmt.Fprintf(w, "schema: %s\n", change); err != nil {
			return err
		}
	}
	for _, warning := range report.Warnings {
		if _, err := fmt.Fprintf(w, "warning: %s\n", warning); err != nil {
			return err
		}
	}
	for _, relation := range report.Relations {
		name := relation.Name
		if relation.RightName != "" && relation.RightName != relation.Name {
			name += " -> " + relation.RightName
		}
		if _, err := fmt.Fprintf(w, "%s %s (%s identity): +%d -%d ~%d, %d unchanged (rows %d -> %d)\n",
			relation.Kind, name, relation.Identity, relation.Counts.Added, relation.Counts.Removed, relation.Counts.Changed,
			relation.Counts.Unchanged, relation.LeftRows, relation.RightRows); err != nil {
			return err
		}
		if details {
			for _, diff := range relation.Details {
				if _, err := fmt.Fprintf(w, "  %s [%s]", diff.Status, diff.ID); err != nil {
					return err
				}
				for _, field := range diff.Fields {
					before := "<absent>"
					if !field.BeforeAbsent {
						encoded, _ := json.Marshal(field.Before)
						before = string(encoded)
					}
					after := "<absent>"
					if !field.AfterAbsent {
						encoded, _ := json.Marshal(field.After)
						after = string(encoded)
					}
					if _, err := fmt.Fprintf(w, " %s: %s -> %s", field.Name, before, after); err != nil {
						return err
					}
				}
				if _, err := io.WriteString(w, "\n"); err != nil {
					return err
				}
			}
		}
	}
	if report.DetailsLimited {
		_, err := io.WriteString(w, "record details are limited; all tables and rows were still compared\n")
		return err
	}
	return nil
}
