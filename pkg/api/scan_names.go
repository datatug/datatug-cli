package api

import (
	"fmt"

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
