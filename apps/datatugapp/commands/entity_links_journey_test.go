package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A command writes only plain files in plain folders of the project: it does not write through a
// link. The entity commands (entity add, entity field add, set and rm) write the file of an entity,
// entities/<id>/<id>.entity.json, and make the folders above it. For each of them a link stands
// in the place of the file and of each folder above it, pointing outside the project: the command
// is refused, names the path of the link inside the project and not where it leads, and the tree
// outside is byte-identical afterwards, with nothing new in it.

const entityOutsideSentinel = "{\"id\":\"a file that is not the project's\"}\n"

// linkedEntityProject is a project with one entity, User, and a link, at place (a path below the
// project folder, slash separated), to a place outside it. For a place that holds something the
// project has, what is there is moved outside and linked to; for one that is not there, what the
// link leads to is a file or a folder of its own that holds a sentinel, or nothing (dangling).
func linkedEntityProject(t *testing.T, place string, dangling bool) (projectDir, outside string) {
	t.Helper()
	projectDir = filepath.Join(t.TempDir(), "proj")
	_, _, err := runEntity(t, "entity", "add", "-d", projectDir, "-f", writeEntityDefinition(t, "id: User\nfields:\n  - id: id\n    type: string\n  - id: name\n    type: string\n"))
	require.NoError(t, err)
	outside = filepath.Join(t.TempDir(), "outside")
	require.NoError(t, os.MkdirAll(filepath.Dir(outside), 0o755))
	linkAt := filepath.Join(projectDir, filepath.FromSlash(place))
	switch _, statErr := os.Lstat(linkAt); {
	case statErr == nil:
		require.NoError(t, os.Rename(linkAt, outside))
	case dangling:
		outside = filepath.Join(outside, "nothing")
		require.NoError(t, os.MkdirAll(filepath.Dir(outside), 0o755))
	case filepath.Ext(place) == ".json":
		require.NoError(t, os.MkdirAll(filepath.Dir(outside), 0o755))
		require.NoError(t, os.WriteFile(outside, []byte(entityOutsideSentinel), 0o644))
	default:
		require.NoError(t, os.MkdirAll(outside, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(outside, "keep.json"), []byte(entityOutsideSentinel), 0o644))
	}
	require.NoError(t, os.MkdirAll(filepath.Dir(linkAt), 0o755))
	if err := os.Symlink(outside, linkAt); err != nil {
		t.Skipf("cannot make a symbolic link here (Windows needs a privilege for it; the refusal there is covered by the unit tests of internal/plainfs, with a faked Lstat): %v", err)
	}
	return projectDir, outside
}

func writeEntityDefinition(t *testing.T, content string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "definition.yaml")
	require.NoError(t, os.WriteFile(file, []byte(content), 0o644))
	return file
}

func TestEntityCommandsWriteNothingThroughALink(t *testing.T) {
	type command struct {
		name string
		// entity is the id the command writes, and so the folders of its file.
		entity string
		args   func(t *testing.T, projectDir string) []string
	}
	commands := []command{
		{"entity add", "Order", func(t *testing.T, dir string) []string {
			return []string{"entity", "add", "-d", dir, "-f", writeEntityDefinition(t, "id: Order\nfields:\n  - id: id\n    type: string\n")}
		}},
		{"entity add --continue-on-error", "Order", func(t *testing.T, dir string) []string {
			return []string{"entity", "add", "-d", dir, "--continue-on-error", "-f", writeEntityDefinition(t, "id: Order\nfields:\n  - id: id\n    type: string\n")}
		}},
		{"entity field add", "User", func(t *testing.T, dir string) []string {
			return []string{"entity", "field", "add", "User", "-d", dir, "-f", writeEntityDefinition(t, "id: email\ntype: string\n")}
		}},
		{"entity field set", "User", func(t *testing.T, dir string) []string {
			return []string{"entity", "field", "set", "User", "name", "-d", dir, "--title", "Full name"}
		}},
		{"entity field rm", "User", func(t *testing.T, dir string) []string {
			return []string{"entity", "field", "rm", "User", "name", "-d", dir}
		}},
	}
	for _, c := range commands {
		for _, place := range []string{"entities", "entities/" + c.entity, "entities/" + c.entity + "/" + c.entity + ".entity.json"} {
			for _, dangling := range []bool{false, true} {
				name := c.name + ", a link at " + place
				if dangling {
					name += " to nothing"
				}
				t.Run(name, func(t *testing.T) {
					exists := c.entity == "User"
					if dangling && exists {
						t.Skip("an entity that is there has nothing for a link to nothing to stand in for: the command does not find it")
					}
					projectDir, outside := linkedEntityProject(t, place, dangling)
					args := c.args(t, projectDir)
					outsideBefore, projectBefore := treeWithLinks(t, filepath.Dir(outside)), treeWithLinks(t, projectDir)

					_, _, err := runEntity(t, args...)

					require.Error(t, err, "a command that would write through a link is refused")
					if c.entity == "Order" && strings.HasSuffix(place, ".json") && !dangling {
						// A new entity whose file is a link to a file: the entity is found through the link, as
						// a read always finds it, and add never replaces an entity that is there.
						assert.ErrorContains(t, err, "already exists")
					} else {
						assert.ErrorContains(t, err, place, "and names the path of the link inside the project")
					}
					assert.NotContains(t, err.Error(), outside, "and never where it leads")
					assert.Equal(t, outsideBefore, treeWithLinks(t, filepath.Dir(outside)), "the tree outside is byte-identical and nothing new is in it")
					assert.Equal(t, projectBefore, treeWithLinks(t, projectDir), "the project is as it was: no file the command staged is left")
				})
			}
		}
	}
}

// What the commands wrote before still comes out the same: the file of an entity, and nothing
// beside it.
func TestEntityCommandsWriteOnlyTheFileOfTheEntity(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "proj")

	_, _, err := runEntity(t, "entity", "add", "-d", projectDir, "-f", writeEntityDefinition(t, "id: User\nfields:\n  - id: id\n    type: string\n"))
	require.NoError(t, err)
	_, _, err = runEntity(t, "entity", "field", "add", "User", "-d", projectDir, "-f", writeEntityDefinition(t, "id: email\ntype: string\n"))
	require.NoError(t, err)

	assert.Equal(t, []string{"entities/User/User.entity.json"}, projectFiles(t, projectDir, ""))
}

func TestPathInProject(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "work", "proj")
	assert.Equal(t, "entities/User/User.entity.json", pathInProject(root, filepath.Join(root, "entities", "User", "User.entity.json")))
	assert.Equal(t, "relative/path", pathInProject(root, "relative/path"), "a path that cannot be told from the project folder is said as it is")
}
