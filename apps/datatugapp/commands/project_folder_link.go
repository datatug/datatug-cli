package commands

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// followProjectLinkFlagName is the flag of every command that writes into a project folder given
// by the person: it lets the command go on when that folder is a link.
const followProjectLinkFlagName = "follow-project-link"

// registerFollowProjectLinkFlag registers --follow-project-link on a command that writes into a
// project folder.
func registerFollowProjectLinkFlag(cmd *cobra.Command) {
	cmd.Flags().Bool(followProjectLinkFlagName, false,
		"Write into the project folder even when the folder given is a link. A command that writes into a project stops, and prints the folder the link leads to, when the last part of the folder it was given (-d, or the project folder argument) is a link: a repository made by somebody else may hold a link where a project folder is expected")
}

// Seams over the calls checkProjectFolderLink makes, so that a test makes a call fail or
// reports a junction on a platform that has none. Always the real calls in production.
var (
	projectFolderLstat        = os.Lstat
	projectFolderGetwd        = os.Getwd
	projectFolderEvalSymlinks = filepath.EvalSymlinks
)

// checkProjectFolderLink is an error when the last part of dir, the project folder the person
// gave a command that writes into it, is a link (a symbolic link, or a Windows junction, which
// Lstat reports as irregular), unless follow says to go on. The error names dir and the folder
// it leads to, and the flag that goes on. A path that is "." is the working directory, and is
// looked at as the path the person entered it by (the one a shell keeps, in PWD), since that is
// where a link would show.
//
// Only the last part of dir is looked at: the folders above it are the person's own to have given.
// Whatever is below the project folder is looked at by every write (package plainfs). A folder that
// is not there, or a file, or a folder that cannot be looked at, is not a link: the command refuses
// or makes it as it always did.
func checkProjectFolderLink(dir string, follow bool) error {
	if follow {
		return nil
	}
	path := filepath.Clean(dir) // "link/" is the link, and not what it leads to
	if path == "." {
		wd, err := projectFolderGetwd()
		if err != nil {
			return fmt.Errorf("cannot tell the working directory, which is the project folder: %w", err)
		}
		path = wd
	}
	info, err := projectFolderLstat(path)
	if err != nil || info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) == 0 {
		return nil
	}
	hint := fmt.Sprintf("a project cloned from someone else may hold a link where its folder is expected, so a command that writes does not write through one: pass --%s to write into that folder, or give the folder it leads to", followProjectLinkFlagName)
	resolved, err := projectFolderEvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("the project folder %q is a link that leads to a folder that is not there: %s", dir, hint)
	}
	return fmt.Errorf("the project folder %q is a link to %q: %s", dir, resolved, hint)
}
