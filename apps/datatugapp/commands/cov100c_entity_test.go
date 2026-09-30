package commands

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/go-git/go-git/v5"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// cov100cSetVar swaps a package-level seam for the duration of the test.
func cov100cSetVar[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

const cov100cEntityA = `{"id":"A","title":"Entity A","fields":[{"id":"f1","type":"string"}]}`

func cov100cEntityPath(dir, id string) string {
	return filepath.Join(dir, "entities", id, id+".entity.json")
}

func cov100cSeedEntity(t *testing.T, dir, id, body string) {
	t.Helper()
	p := cov100cEntityPath(dir, id)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
}

// cov100cBlockWrite makes atomicWriteFiles fail when staging id's entity file.
func cov100cBlockWrite(t *testing.T, dir, id string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(cov100cEntityPath(dir, id)+".tmp-0", 0o755))
}

// cov100cBareRepoDir returns a project dir that is a bare git repo: the git
// preflight passes but staging fails (bare repositories have no worktree).
func cov100cBareRepoDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	_, err := git.PlainInit(dir, true)
	require.NoError(t, err)
	return dir
}

func cov100cFailMarshal(t *testing.T) {
	t.Helper()
	cov100cSetVar(t, &entityMarshalFile, func(*datatug.Entity) ([]byte, error) {
		return nil, errors.New("marshal boom")
	})
}

func TestCov100cEntityList(t *testing.T) {
	t.Run("no project", func(t *testing.T) {
		_, _, err := runEntity(t, "entity", "list")
		require.Error(t, err)
	})
	t.Run("empty", func(t *testing.T) {
		out, _, err := runEntity(t, "entity", "list", "-d", t.TempDir())
		require.NoError(t, err)
		assert.Contains(t, out.String(), "no entities")
	})
	t.Run("corrupt", func(t *testing.T) {
		dir := t.TempDir()
		cov100cSeedEntity(t, dir, "A", "{bad")
		_, _, err := runEntity(t, "entity", "list", "-d", dir)
		require.Error(t, err)
	})
	t.Run("with and without title", func(t *testing.T) {
		dir := t.TempDir()
		cov100cSeedEntity(t, dir, "A", cov100cEntityA)
		cov100cSeedEntity(t, dir, "B", `{"id":"B"}`)
		out, _, err := runEntity(t, "entity", "list", "-d", dir)
		require.NoError(t, err)
		assert.Contains(t, out.String(), "A — Entity A")
		assert.Contains(t, out.String(), "B\n")
	})
}

func TestCov100cEntityShow(t *testing.T) {
	t.Run("no project", func(t *testing.T) {
		_, _, err := runEntity(t, "entity", "show", "A")
		require.Error(t, err)
	})
	t.Run("corrupt", func(t *testing.T) {
		dir := t.TempDir()
		cov100cSeedEntity(t, dir, "A", "{bad")
		_, _, err := runEntity(t, "entity", "show", "A", "-d", dir)
		require.Error(t, err)
	})
	t.Run("flat layout has no nested file to repair tables from", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "entities"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "entities", "A.entity.json"), []byte(cov100cEntityA), 0o600))
		out, _, err := runEntity(t, "entity", "show", "A", "-d", dir)
		require.NoError(t, err)
		assert.Contains(t, out.String(), "Entity A")
	})
	t.Run("unparsable tables are tolerated", func(t *testing.T) {
		dir := t.TempDir()
		cov100cSeedEntity(t, dir, "A", `{"id":"A","tables":[{"name":5}]}`)
		_, _, err := runEntity(t, "entity", "show", "A", "-d", dir)
		require.NoError(t, err)
	})
	t.Run("render failure", func(t *testing.T) {
		dir := t.TempDir()
		cov100cSeedEntity(t, dir, "A", cov100cEntityA)
		cov100cSetVar(t, &entityJSONMarshal, func(any) ([]byte, error) { return nil, errors.New("boom") })
		_, _, err := runEntity(t, "entity", "show", "A", "-d", dir)
		require.Error(t, err)
	})
}

type cov100cFailEnc struct {
	*yaml.Encoder
	failEncode, failClose bool
}

func (e *cov100cFailEnc) Encode(v any) error {
	if e.failEncode {
		return errors.New("encode boom")
	}
	return e.Encoder.Encode(v)
}

func (e *cov100cFailEnc) Close() error {
	if e.failClose {
		return errors.New("close boom")
	}
	return e.Encoder.Close()
}

func TestCov100cRenderEntityShow(t *testing.T) {
	withTables := &datatug.Entity{Tables: datatug.TableKeys{datatug.NewTableKey("t", "s", "c", nil)}}
	withTables.ID = "A"

	t.Run("ok with tables", func(t *testing.T) {
		out, err := renderEntityShow(withTables)
		require.NoError(t, err)
		assert.Contains(t, out, "generated mapping copy")
	})
	t.Run("marshal error", func(t *testing.T) {
		cov100cSetVar(t, &entityJSONMarshal, func(any) ([]byte, error) { return nil, errors.New("boom") })
		_, err := renderEntityShow(withTables)
		require.Error(t, err)
	})
	t.Run("unmarshal error", func(t *testing.T) {
		cov100cSetVar(t, &entityJSONUnmarshal, func([]byte, any) error { return errors.New("boom") })
		_, err := renderEntityShow(withTables)
		require.Error(t, err)
	})
	for _, tc := range []struct {
		name                  string
		nth                   int
		failEncode, failClose bool
	}{
		{"doc encode", 1, true, false},
		{"doc close", 1, false, true},
		{"tables encode", 2, true, false},
		{"tables close", 2, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := 0
			cov100cSetVar(t, &newEntityYAMLEncoder, func(w io.Writer) entityYAMLEncoder {
				n++
				e := &cov100cFailEnc{Encoder: yaml.NewEncoder(w)}
				if n == tc.nth {
					e.failEncode, e.failClose = tc.failEncode, tc.failClose
				}
				return e
			})
			_, err := renderEntityShow(withTables)
			require.Error(t, err)
		})
	}
}

func TestCov100cUnitHelpers(t *testing.T) {
	t.Run("validateFieldType", func(t *testing.T) {
		assert.NoError(t, validateFieldType(""))
		assert.Error(t, validateFieldType("extends:  "))
		assert.NoError(t, validateFieldType("extends:X"))
		assert.Error(t, validateFieldType("bogus-type"))
	})
	t.Run("marshalEntityFile empty fields", func(t *testing.T) {
		e := &datatug.Entity{Fields: []*datatug.EntityField{}}
		e.ID = "A"
		_, err := marshalEntityFile(e)
		require.NoError(t, err)
		assert.Nil(t, e.Fields)
	})
	t.Run("parseEntityDocs", func(t *testing.T) {
		_, err := parseEntityDocs([]byte("a: [unclosed"))
		assert.Error(t, err, "yaml error")
		_, err = parseEntityDocs([]byte("1: x\n"))
		assert.NoError(t, err)
		_, err = parseEntityDocs([]byte("- x\n"))
		assert.Error(t, err, "bad list element")
		_, err = parseEntityDocs([]byte("x\n"))
		assert.Error(t, err, "scalar")
		es, err := parseEntityDocs([]byte("- id: A\n- id: B\n"))
		require.NoError(t, err)
		assert.Len(t, es, 2)

		cov100cSetVar(t, &entityJSONUnmarshal, func([]byte, any) error { return errors.New("boom") })
		_, err = parseEntityDocs([]byte("- id: A\n"))
		assert.Error(t, err, "list unmarshal")
	})
	t.Run("parseFieldDocs", func(t *testing.T) {
		_, err := parseFieldDocs([]byte("a: [unclosed"))
		assert.Error(t, err)
		_, err = parseFieldDocs([]byte("1: x\n"))
		assert.NoError(t, err)
		_, err = parseFieldDocs([]byte("- 1\n"))
		assert.Error(t, err, "bad list element")
		_, err = parseFieldDocs([]byte("x\n"))
		assert.Error(t, err, "scalar")
		fs, err := parseFieldDocs([]byte("- id: a\n- id: b\n"))
		require.NoError(t, err)
		assert.Len(t, fs, 2)
		fs, err = parseFieldDocs([]byte("id: a\n"))
		require.NoError(t, err)
		assert.Len(t, fs, 1)
	})
	t.Run("atomicWriteFiles", func(t *testing.T) {
		dir := t.TempDir()
		blocker := filepath.Join(dir, "file")
		require.NoError(t, os.WriteFile(blocker, nil, 0o600))
		// mkdir fails: parent is a regular file; first write staged then cleaned.
		err := atomicWriteFiles([]fileWrite{
			{path: filepath.Join(dir, "ok.txt"), content: []byte("x")},
			{path: filepath.Join(blocker, "sub", "f"), content: []byte("x")},
		})
		require.Error(t, err)
		assert.NoFileExists(t, filepath.Join(dir, "ok.txt.tmp-0"))

		// stage fails: temp path is a directory.
		target := filepath.Join(dir, "t.txt")
		require.NoError(t, os.MkdirAll(target+".tmp-0", 0o755))
		require.Error(t, atomicWriteFiles([]fileWrite{{path: target, content: []byte("x")}}))

		// rename fails: final path is a non-empty directory.
		final := filepath.Join(dir, "final")
		require.NoError(t, os.MkdirAll(filepath.Join(final, "child"), 0o755))
		err = atomicWriteFiles([]fileWrite{
			{path: filepath.Join(dir, "first.txt"), content: []byte("x")},
			{path: final, content: []byte("x")},
		})
		require.Error(t, err)
		assert.NoFileExists(t, filepath.Join(dir, "first.txt.tmp-0"))
	})
}

type cov100cErrReader struct{}

func (cov100cErrReader) Read([]byte) (int, error) { return 0, errors.New("read boom") }

func TestCov100cReadDefinitionInput(t *testing.T) {
	newCmd := func() *cobra.Command {
		c := &cobra.Command{Use: "x"}
		registerEntityDefinitionFlags(c)
		return c
	}
	t.Run("stdin error", func(t *testing.T) {
		c := newCmd()
		c.SetIn(cov100cErrReader{})
		_, _, err := readDefinitionInput(c)
		require.Error(t, err)
	})
	t.Run("file error", func(t *testing.T) {
		c := newCmd()
		require.NoError(t, c.Flags().Set("file", filepath.Join(t.TempDir(), "missing.yaml")))
		_, _, err := readDefinitionInput(c)
		require.Error(t, err)
	})
	t.Run("bad format", func(t *testing.T) {
		c := newCmd()
		require.NoError(t, c.Flags().Set("format", "toml"))
		_, _, err := readDefinitionInput(c)
		require.Error(t, err)
	})
	t.Run("empty stdin", func(t *testing.T) {
		c := newCmd()
		c.SetIn(strings.NewReader("  \n"))
		_, _, err := readDefinitionInput(c)
		require.Error(t, err)
	})
}

func TestCov100cEntityAdd(t *testing.T) {
	const okDef = "id: N\nfields:\n  - id: id\n    type: string\n"
	t.Run("no project", func(t *testing.T) {
		_, _, err := runEntityStdin(t, okDef, "entity", "add")
		require.Error(t, err)
	})
	t.Run("stage outside repo", func(t *testing.T) {
		_, _, err := runEntityStdin(t, okDef, "entity", "add", "-d", t.TempDir(), "--git", "stage")
		require.Error(t, err)
	})
	t.Run("bad input format", func(t *testing.T) {
		_, _, err := runEntityStdin(t, okDef, "entity", "add", "-d", t.TempDir(), "--format", "toml")
		require.Error(t, err)
	})
	t.Run("parse error", func(t *testing.T) {
		_, _, err := runEntityStdin(t, "a: [unclosed", "entity", "add", "-d", t.TempDir())
		require.Error(t, err)
	})
	t.Run("empty list", func(t *testing.T) {
		_, _, err := runEntityStdin(t, "[]", "entity", "add", "-d", t.TempDir())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no entities")
	})
	t.Run("atomic missing id", func(t *testing.T) {
		out, _, err := runEntityStdin(t, "- title: x\n- id: N\n", "entity", "add", "-d", t.TempDir())
		require.Error(t, err)
		assert.Contains(t, out.String(), "<missing id>")
		assert.Contains(t, out.String(), "rolled back")
	})
	t.Run("atomic marshal error", func(t *testing.T) {
		cov100cFailMarshal(t)
		out, _, err := runEntityStdin(t, okDef, "entity", "add", "-d", t.TempDir())
		require.Error(t, err)
		assert.Contains(t, out.String(), "marshal boom")
	})
	t.Run("atomic write error", func(t *testing.T) {
		dir := t.TempDir()
		cov100cBlockWrite(t, dir, "N")
		_, _, err := runEntityStdin(t, okDef, "entity", "add", "-d", dir)
		require.Error(t, err)
	})
	t.Run("stage error on bare repo", func(t *testing.T) {
		_, _, err := runEntityStdin(t, okDef, "entity", "add", "-d", cov100cBareRepoDir(t), "--git", "stage")
		require.Error(t, err)
	})
	t.Run("continue-on-error all ok", func(t *testing.T) {
		dir := t.TempDir()
		out, _, err := runEntityStdin(t, "- id: N\n- id: M\n", "entity", "add", "-d", dir, "--continue-on-error")
		require.NoError(t, err)
		assert.Contains(t, out.String(), "created: N")
		assert.FileExists(t, cov100cEntityPath(dir, "M"))
	})
	t.Run("continue-on-error failures", func(t *testing.T) {
		dir := t.TempDir()
		cov100cSeedEntity(t, dir, "X", `{"id":"X"}`)
		cov100cBlockWrite(t, dir, "W")
		def := "- title: nameless\n- id: X\n- id: T\n  fields:\n    - id: f\n      type: nope\n- id: W\n- id: OK\n"
		out, _, err := runEntityStdin(t, def, "entity", "add", "-d", dir, "--continue-on-error")
		require.Error(t, err)
		s := out.String()
		assert.Contains(t, s, "failed: <missing id>")
		assert.Contains(t, s, "failed: X (already exists)")
		assert.Contains(t, s, "failed: T")
		assert.Contains(t, s, "failed: W")
		assert.Contains(t, s, "created: OK")
	})
	t.Run("continue-on-error marshal error", func(t *testing.T) {
		cov100cFailMarshal(t)
		_, _, err := runEntityStdin(t, okDef, "entity", "add", "-d", t.TempDir(), "--continue-on-error")
		require.Error(t, err)
	})
}

func TestCov100cEntityFieldAdd(t *testing.T) {
	const fieldDef = "id: f2\ntype: string\n"
	seed := func(t *testing.T) string {
		dir := t.TempDir()
		cov100cSeedEntity(t, dir, "A", cov100cEntityA)
		return dir
	}
	run := func(t *testing.T, stdin string, args ...string) (string, error) {
		out, _, err := runEntityStdin(t, stdin, append([]string{"entity", "field", "add"}, args...)...)
		return out.String(), err
	}
	t.Run("no name", func(t *testing.T) {
		_, err := run(t, fieldDef)
		require.Error(t, err)
	})
	t.Run("no project", func(t *testing.T) {
		_, err := run(t, fieldDef, "A")
		require.Error(t, err)
	})
	t.Run("bad git", func(t *testing.T) {
		_, err := run(t, fieldDef, "A", "-d", seed(t), "--git", "bogus")
		require.Error(t, err)
	})
	t.Run("stage outside repo", func(t *testing.T) {
		_, err := run(t, fieldDef, "A", "-d", seed(t), "--git", "stage")
		require.Error(t, err)
	})
	t.Run("bad format", func(t *testing.T) {
		_, err := run(t, fieldDef, "A", "-d", seed(t), "--format", "toml")
		require.Error(t, err)
	})
	t.Run("parse error", func(t *testing.T) {
		_, err := run(t, "a: [unclosed", "A", "-d", seed(t))
		require.Error(t, err)
	})
	t.Run("empty list", func(t *testing.T) {
		_, err := run(t, "[]", "A", "-d", seed(t))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no fields")
	})
	t.Run("corrupt entity", func(t *testing.T) {
		dir := t.TempDir()
		cov100cSeedEntity(t, dir, "A", "{bad")
		_, err := run(t, fieldDef, "A", "-d", dir)
		require.Error(t, err)
	})
	t.Run("atomic failures", func(t *testing.T) {
		out, err := run(t, "- title: x\n- id: f1\n- id: g\n  type: nope\n", "A", "-d", seed(t))
		require.Error(t, err)
		assert.Contains(t, out, "failed field")
	})
	t.Run("continue-on-error failures and successes", func(t *testing.T) {
		dir := seed(t)
		out, err := run(t, "- title: x\n- id: f1\n- id: g\n  type: nope\n- id: ok\n", "A", "-d", dir, "--continue-on-error")
		require.Error(t, err)
		assert.Contains(t, out, "failed field: <missing id>")
		assert.Contains(t, out, "failed field: f1 (already exists)")
		assert.Contains(t, out, "failed field: g")
		assert.Contains(t, out, "added field: ok")
	})
	t.Run("continue-on-error nothing to add", func(t *testing.T) {
		_, err := run(t, "- id: f1\n", "A", "-d", seed(t), "--continue-on-error")
		require.Error(t, err)
	})
	t.Run("marshal error", func(t *testing.T) {
		dir := seed(t)
		cov100cFailMarshal(t)
		_, err := run(t, fieldDef, "A", "-d", dir)
		require.Error(t, err)
	})
	t.Run("write error", func(t *testing.T) {
		dir := seed(t)
		cov100cBlockWrite(t, dir, "A")
		_, err := run(t, fieldDef, "A", "-d", dir)
		require.Error(t, err)
	})
	t.Run("stage error on bare repo", func(t *testing.T) {
		dir := cov100cBareRepoDir(t)
		cov100cSeedEntity(t, dir, "A", cov100cEntityA)
		_, err := run(t, fieldDef, "A", "-d", dir, "--git", "stage")
		require.Error(t, err)
	})
}

// cov100cFieldVerbCommon covers the failure branches shared by field set/rm.
func cov100cFieldVerbCommon(t *testing.T, verb string, extra ...string) {
	t.Helper()
	seed := func(t *testing.T) string {
		dir := t.TempDir()
		cov100cSeedEntity(t, dir, "A", cov100cEntityA)
		return dir
	}
	run := func(t *testing.T, args ...string) error {
		_, _, err := runEntity(t, append([]string{"entity", "field", verb}, append(args, extra...)...)...)
		return err
	}
	t.Run("no project", func(t *testing.T) {
		require.Error(t, run(t, "A", "f1"))
	})
	t.Run("bad git", func(t *testing.T) {
		require.Error(t, run(t, "A", "f1", "-d", seed(t), "--git", "bogus"))
	})
	t.Run("stage outside repo", func(t *testing.T) {
		require.Error(t, run(t, "A", "f1", "-d", seed(t), "--git", "stage"))
	})
	t.Run("entity not found", func(t *testing.T) {
		require.Error(t, run(t, "Nope", "f1", "-d", t.TempDir()))
	})
	t.Run("corrupt entity", func(t *testing.T) {
		dir := t.TempDir()
		cov100cSeedEntity(t, dir, "A", "{bad")
		require.Error(t, run(t, "A", "f1", "-d", dir))
	})
	t.Run("field not found", func(t *testing.T) {
		require.Error(t, run(t, "A", "missing", "-d", seed(t)))
	})
	t.Run("marshal error", func(t *testing.T) {
		dir := seed(t)
		cov100cFailMarshal(t)
		require.Error(t, run(t, "A", "f1", "-d", dir))
	})
	t.Run("write error", func(t *testing.T) {
		dir := seed(t)
		cov100cBlockWrite(t, dir, "A")
		require.Error(t, run(t, "A", "f1", "-d", dir))
	})
	t.Run("stage error on bare repo", func(t *testing.T) {
		dir := cov100cBareRepoDir(t)
		cov100cSeedEntity(t, dir, "A", cov100cEntityA)
		require.Error(t, run(t, "A", "f1", "-d", dir, "--git", "stage"))
	})
}

func TestCov100cEntityFieldRm(t *testing.T) {
	t.Run("missing args", func(t *testing.T) {
		_, _, err := runEntity(t, "entity", "field", "rm", "A")
		require.Error(t, err)
	})
	cov100cFieldVerbCommon(t, "rm")
}

func TestCov100cEntityFieldSet(t *testing.T) {
	t.Run("missing args", func(t *testing.T) {
		_, _, err := runEntity(t, "entity", "field", "set", "A")
		require.Error(t, err)
	})
	t.Run("nothing to update", func(t *testing.T) {
		_, _, err := runEntity(t, "entity", "field", "set", "A", "f1")
		require.Error(t, err)
	})
	t.Run("title and invalid type", func(t *testing.T) {
		dir := t.TempDir()
		cov100cSeedEntity(t, dir, "A", cov100cEntityA)
		_, _, err := runEntity(t, "entity", "field", "set", "A", "f1", "-d", dir, "--title", "New")
		require.NoError(t, err)
		_, _, err = runEntity(t, "entity", "field", "set", "A", "f1", "-d", dir, "--type", "nope")
		require.Error(t, err)
	})
	cov100cFieldVerbCommon(t, "set", "--title", "T")
}
