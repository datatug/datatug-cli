package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

// CheckScanName is an error that names flag (such as "--db") when value cannot be the
// id of a database, a database model or an environment in a project: each is the name
// of a folder of the project, so it must be a plain name (letters and digits of any
// script, ".", "_" and "-", at most 128 characters, starting with a letter or a digit,
// as every source id is, see dbcopy.SourceIDDisplay) that is also a folder name on
// every system a project is opened on (see folderNameProblem). A value that is not a
// plain name is not in the message: it may be a connection string.
func CheckScanName(flag, value string) error {
	if value == "" {
		return fmt.Errorf("%s must not be empty", flag)
	}
	if dbcopy.SourceIDDisplay(value) != value {
		return fmt.Errorf("%s is not a plain name: use letters, digits, \".\", \"_\" and \"-\", starting with a letter or a digit, at most 128 characters, because it is the name of a folder of the project", flag)
	}
	if problem := folderNameProblem(value); problem != "" {
		return fmt.Errorf("%s %q cannot be the name of a folder of the project: %s", flag, value, problem)
	}
	return nil
}

// CheckSQLitePath is an error when the SQLite database file has a "?" in its path, and
// nil otherwise: path is the --path as it was typed, and absolute is the file it is,
// which is what the open of a source gets, so it is the one that is looked at (a relative
// path is the file from a working directory that can have a "?" in its own path, and a
// database inside a project whose folder has one has it too). A scan reads such a file,
// but the project could not open it again: the open of a source (pkg/dbcopy) hands the
// bare path to the driver, which reads a "?" in it as the start of its own parameters,
// opens the name before it and creates a file of that name. So the scan refuses the
// file before it reads or writes anything, and the message names the character and says
// to rename the file. It lifts on the day that open reads such a name back.
func CheckSQLitePath(path, absolute string) error {
	if !strings.Contains(absolute, "?") {
		return nil
	}
	subject := fmt.Sprintf("--path %q has the character \"?\" in it", path)
	if !strings.Contains(path, "?") {
		subject = fmt.Sprintf("--path %q is the file %q, which has the character \"?\" in its path", path, absolute)
	}
	return fmt.Errorf("%s, and the project could not open the file again, because the open of a source reads a \"?\" in a path as the start of the driver's own parameters, and opens another name; rename the file (or the folder it is in, or move it to a path with no \"?\") and scan again", subject)
}

// CheckScanNamesAgainstProject is an error when the environment, the catalog id or the
// database model of a scan differs only by case from a name the project already has in
// the same place: an environment in environments/, a catalog in the folder of the
// catalogs of the environment (a folder, or a file in the flat place datatug-core also
// reads), a model in dbmodels/. On a file system that does not tell the case of a name
// apart (macOS and Windows by default) the two are one folder, and a scan of one database
// would take the tables of another for its own and remove those it does not find. The names
// are those the folders list, so the answer does not depend on the file system the project
// is on; ids that are the same, or that differ by more than case, are not refused. The ids
// are plain names already (see CheckScanName).
func CheckScanNamesAgainstProject(projectDir, environment, catalogID, model string) error {
	environments := filepath.Join(projectDir, storage.EnvironmentsFolder)
	for _, check := range []struct {
		subject string                       // the value, as the message names it
		has     func(existing string) string // the name the project has, as the message names it
		value   string
		dir     string
		suffix  string
	}{
		{fmt.Sprintf("--env %q", environment), func(existing string) string { return fmt.Sprintf("environment %q", existing) },
			environment, environments, ""},
		{fmt.Sprintf("--db %q", catalogID), func(existing string) string {
			return fmt.Sprintf("catalog %q of environment %q", existing, environment)
		}, catalogID, filepath.Join(environments, environment, storage.EnvDbCatalogsFolder), catalogFileSuffix},
		{fmt.Sprintf("the database model %q (--dbmodel, or else --db)", model), func(existing string) string { return fmt.Sprintf("database model %q", existing) },
			model, filepath.Join(projectDir, storage.DbModelsFolder), ""},
	} {
		if existing := nameThatDiffersOnlyByCase(check.dir, check.suffix, check.value); existing != "" {
			return fmt.Errorf("%s differs only by case from the %s that this project has, and on a file system that does not tell the two apart (macOS and Windows by default) they would be one folder: use %q, or a name that differs by more than case", check.subject, check.has(existing), existing)
		}
	}
	return nil
}

// catalogFileSuffix is what a catalog file in the flat place ends with, after the id.
const catalogFileSuffix = "." + storage.DbCatalogFileSuffix + ".json"

// nameThatDiffersOnlyByCase is the name in the folder dir that differs from name by case
// only, or "" when none does. A file that stands for a name has it followed by suffix,
// which is taken off ("" for none). A folder that cannot be listed has no names.
func nameThatDiffersOnlyByCase(dir, suffix, name string) string {
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		existing := strings.TrimSuffix(entry.Name(), suffix)
		if existing != name && strings.EqualFold(existing, name) {
			return existing
		}
	}
	return ""
}

// ResolveScanDbModel is the database model a scan of catalog catalogID in environment
// puts the catalog on, for the project at projectDir, given the value of --dbmodel
// (flag, "" when it is not given, and a plain name when it is). The environment and the
// catalog id are plain names too, as they are names of folders of the project.
//
// A catalog the project already records has a model, and a scan keeps it: without the
// flag the model is the one the catalog file names, and a flag that names another model
// is an error that names both, because scanning onto it would leave the tables of the
// first model in the project for good and make a second model of the same database.
// A catalog the project does not record in this environment yet is put on the model of
// the flag, or else, as one database is one model in every environment, on the model that
// the other environments record for a catalog of that id when they all name the same one;
// when they name different ones the scan is refused, naming them, as no model is the
// obvious one; and when none records one it is put on the model called as the database.
// A model the catalog file names that cannot be the name of a folder, or a catalog file
// that cannot be read, records nothing: the scan writes the file anew.
func ResolveScanDbModel(projectDir, environment, catalogID, flag string) (string, error) {
	recorded := recordedDbModel(projectDir, environment, catalogID)
	switch {
	case recorded != "" && flag != "" && flag != recorded:
		return "", fmt.Errorf("--dbmodel %q: catalog %q of environment %q is on database model %q in this project, and scanning it onto %q would leave the tables of %q in the project and make a second model of the same database; scan without --dbmodel, or with --dbmodel %s, to keep the model", flag, catalogID, environment, recorded, flag, recorded, recorded)
	case recorded != "":
		return recorded, nil
	case flag != "":
		return flag, nil
	}
	byModel := modelsOfOtherEnvironments(projectDir, environment, catalogID)
	models := make([]string, 0, len(byModel))
	for model := range byModel {
		models = append(models, model)
	}
	sort.Strings(models)
	switch len(models) {
	case 0:
		return catalogID, nil
	case 1:
		return models[0], nil
	}
	described := make([]string, len(models))
	for i, model := range models {
		environments := byModel[model]
		noun := "environment"
		if len(environments) > 1 {
			noun = "environments"
		}
		described[i] = fmt.Sprintf("model %q in %s %s", model, noun, strings.Join(quoted(environments), ", "))
	}
	return "", fmt.Errorf("catalog %q of environment %q is not in this project yet, and the project records the database on more than one database model (%s): scan with --dbmodel <model> to say which", catalogID, environment, strings.Join(described, "; "))
}

// recordedDbModel is the database model the project records for catalog catalogID in
// environment, read through the project store, which reads the catalog file in the nested
// place and in the flat one, as every reader does; "" when the project records none: it
// has no catalog of that id in that environment (by the names its folder lists, not by
// what the file system makes of the case of a name), or its file cannot be read, or the
// model it names cannot be the name of a folder.
func recordedDbModel(projectDir, environment, catalogID string) string {
	if !slices.Contains(catalogIDsOf(projectDir, environment), catalogID) {
		return ""
	}
	store := filestore.NewProjectStore(storage.SingleProjectID, projectDir)
	catalog, err := store.LoadEnvDbCatalog(context.Background(), environment, "", catalogID)
	if err != nil || CheckScanName("the model of the catalog file", catalog.DbModel) != nil {
		return ""
	}
	return catalog.DbModel
}

// catalogIDsOf is the ids the folder of the catalogs of environment lists: a folder for
// each catalog, or a file in the flat place.
func catalogIDsOf(projectDir, environment string) []string {
	entries, _ := os.ReadDir(filepath.Join(projectDir, storage.EnvironmentsFolder, environment, storage.EnvDbCatalogsFolder))
	ids := make([]string, len(entries))
	for i, entry := range entries {
		ids[i] = strings.TrimSuffix(entry.Name(), catalogFileSuffix)
	}
	return ids
}

// modelsOfOtherEnvironments is the environments of the project, other than environment,
// that record catalog catalogID, by the database model each records, in name order.
func modelsOfOtherEnvironments(projectDir, environment, catalogID string) map[string][]string {
	byModel := map[string][]string{}
	entries, _ := os.ReadDir(filepath.Join(projectDir, storage.EnvironmentsFolder))
	for _, entry := range entries {
		other := entry.Name()
		if other == environment {
			continue
		}
		if model := recordedDbModel(projectDir, other, catalogID); model != "" {
			byModel[model] = append(byModel[model], other)
		}
	}
	return byModel
}
