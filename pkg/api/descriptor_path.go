package api

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

// scanEvalSymlinks is a seam over filepath.EvalSymlinks, so a test can make the
// look at a link fail. Always filepath.EvalSymlinks in production.
var scanEvalSymlinks = filepath.EvalSymlinks

// errDescriptorOutsideProject is what a PostgreSQL catalog is refused with when its
// connection descriptor is not a file inside the project folder. It names the rule and
// not the path: the path is whatever the project file says, which a message does not
// repeat.
var errDescriptorOutsideProject = errors.New("the connection descriptor of a PostgreSQL catalog must be a file inside the project folder, named by a path relative to it (such as connections/<env>/<db>.json), and not a file that a link leads out of the folder to")

// ResolveDescriptorPath is the file that the path of a PostgreSQL catalog names, which is
// the connection descriptor of the catalog, below projectDir. The descriptor names the
// environment variable that holds the connection URL, and a project file is not trusted
// (a cloned project must not be able to point a reader at any file of the machine), so
// the path must be a path inside the project folder: relative, with no ".." that leaves
// the folder, not starting with "~" or "$" (which ResolveCatalogPath reads as the home
// directory), and not a path a link leads out of the folder by. A path that is a URL is
// refused as ResolveCatalogPath refuses it.
func ResolveDescriptorPath(projectDir, catalogPath string) (string, error) {
	if dbcopy.PathHoldsURL(catalogPath) {
		return "", errors.New("the catalog path is a URL, not a file or a directory (a catalog's path names a local file; keep credentials out of a project)")
	}
	local := filepath.FromSlash(catalogPath)
	if strings.HasPrefix(catalogPath, "~") || strings.HasPrefix(catalogPath, "$") || !filepath.IsLocal(local) {
		return "", errDescriptorOutsideProject
	}
	if projectDir == "" {
		return "", fmt.Errorf("relative catalog path has no project directory to resolve against")
	}
	file := filepath.Join(projectDir, local)
	// A file that is not there is not a link to anywhere: the read of it says so.
	resolved, err := scanEvalSymlinks(file)
	if err != nil {
		return file, nil
	}
	root, err := scanEvalSymlinks(projectDir)
	if err != nil {
		return "", errDescriptorOutsideProject
	}
	if _, inside := relativeInside(root, resolved); !inside {
		return "", errDescriptorOutsideProject
	}
	return file, nil
}
