package commands

import (
	"bytes"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/internal/sourcecases"
)

// TestProperty_NoCommandPathEchoesASourceSecret is the DT-0C acceptance
// property at the command layer: for every generated source string (see
// internal/sourcecases) given to every command path that takes one, no secret of
// four or more characters is in stdout, in stderr, in the returned error, in the
// log, or in any file written.
//
// The command paths are `datatug db <url>`, `datatug db copy --from <url>` and
// `--to <url>`, and `datatug query run --db <url>`, each with the string itself
// and with `env:NAME` where the variable holds it. (`datatug chat` takes no
// source string: it reads its sources from the project. `datatug scan --dsn-env`
// is not in this tree yet; its refusals are covered where it lands.)
//
// Nothing here relies on a redactor: Exit no longer calls one, and the
// top-level handler in main.go that does is not run.
func TestProperty_NoCommandPathEchoesASourceSecret(t *testing.T) {
	// A relative path a source names lands in this directory, so a stray write is
	// found below.
	workdir := t.TempDir()
	t.Chdir(workdir)
	dir := t.TempDir()
	emptySource := "sqlite://" + filepath.Join(dir, "in.db")
	emptyTarget := "sqlite://" + filepath.Join(dir, "out.db")
	for _, name := range []string{"in.db", "out.db"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var logged bytes.Buffer
	savedLog := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(savedLog) })

	cases := sourcecases.CommandCases()
	failed := 0
	for _, c := range cases {
		t.Setenv("DT0C_PROPERTY_SOURCE", c.Source)
		var texts []string
		note := func(stdout, stderr string, err error) {
			texts = append(texts, stdout, stderr)
			if err != nil {
				texts = append(texts, err.Error())
			}
		}

		for _, source := range []string{c.Source, "env:DT0C_PROPERTY_SOURCE"} {
			cmd := dbCommand()
			var err error
			out := covDCaptureStdout(t, func() { err = cmd.RunE(cmd, []string{source}) })
			note(out, "", err)

			stdout, stderr, err := runCopy(t, "db", "copy", "--from", source, "--to", emptyTarget)
			note(stdout.String(), stderr.String(), err)
			stdout, stderr, err = runCopy(t, "db", "copy", "--from", emptySource, "--to", source)
			note(stdout.String(), stderr.String(), err)

			queryOut, queryErr, _ := runQuery(t, "", "--db", source, "--from", "customers", "--no-policies")
			note(queryOut, queryErr, nil)
		}
		texts = append(texts, logged.String())
		logged.Reset()

		if leaked := sourcecases.Leaks(c, texts...); len(leaked) > 0 {
			failed++
			if failed <= 20 {
				t.Errorf("%s\n  leaked %q in:\n    %s", c.Name, leaked, strings.Join(texts, "\n    "))
			}
		}
	}
	if failed > 0 {
		t.Errorf("%d of %d generated sources leaked a secret through a command", failed, len(cases))
	}

	// No file was written with a secret in its name or its content, and nothing
	// was written where a relative source pointed.
	for _, root := range []string{workdir, dir} {
		walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || path == root {
				return err
			}
			content := []byte(nil)
			if !entry.IsDir() {
				if content, err = os.ReadFile(path); err != nil {
					return err
				}
			}
			for _, c := range cases {
				if leaked := sourcecases.Leaks(c, path, string(content)); len(leaked) > 0 {
					t.Errorf("%s: %q was written to %s", c.Name, leaked, path)
				}
			}
			return nil
		})
		if walkErr != nil {
			t.Fatal(walkErr)
		}
	}
	if entries, err := os.ReadDir(workdir); err != nil || len(entries) != 0 {
		t.Errorf("a command wrote %d file(s) where a relative source pointed: %v, %v", len(entries), entries, err)
	}
}
