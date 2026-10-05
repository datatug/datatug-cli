package api

import (
	"fmt"
	"strings"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
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

// CheckSQLitePath is an error when the SQLite database file at path has a "?" in its
// path, and nil otherwise. A scan reads such a file, but the project could not open
// it again: the open of a source (pkg/dbcopy) hands the bare path to the driver, which
// reads a "?" in it as the start of its own parameters, opens the name before it and
// creates a file of that name. So the scan refuses the file before it reads or writes
// anything, and the message names the character and says to rename the file. It lifts
// on the day that open reads such a name back.
func CheckSQLitePath(path string) error {
	if !strings.Contains(path, "?") {
		return nil
	}
	return fmt.Errorf("--path %q has the character \"?\" in it, and the project could not open the file again, because the open of a source reads a \"?\" in a path as the start of the driver's own parameters, and opens another name; rename the file (or move it to a path with no \"?\") and scan again", path)
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
// A catalog the project does not record yet is put on the model of the flag, or else on
// the model called as the database. A model the catalog file names that cannot be the
// name of a folder, or a catalog file that cannot be read, records nothing: the scan
// writes the file anew.
func ResolveScanDbModel(projectDir, environment, catalogID, flag string) (string, error) {
	recorded, err := catalogDbModel(projectDir, environment, catalogID)
	if err != nil || CheckScanName("the model of the catalog file", recorded) != nil {
		recorded = ""
	}
	switch {
	case flag == "" && recorded != "":
		return recorded, nil
	case flag == "":
		return catalogID, nil
	case recorded != "" && flag != recorded:
		return "", fmt.Errorf("--dbmodel %q: catalog %q of environment %q is on database model %q in this project, and scanning it onto %q would leave the tables of %q in the project and make a second model of the same database; scan without --dbmodel, or with --dbmodel %s, to keep the model", flag, catalogID, environment, recorded, flag, recorded, recorded)
	}
	return flag, nil
}
