package commands

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dtlog"
	"github.com/stretchr/testify/assert"
	"github.com/urfave/cli/v3"
)

// runDatatug runs the real root command exactly as DatatugCommand wires it,
// capturing stdout. argv is passed without the leading "datatug" argv[0]
// (mirrors runCopy's convention in cmd_db_copy_test.go).
func runDatatug(t *testing.T, argv ...string) (stdout *bytes.Buffer, err error) {
	t.Helper()
	stdout = &bytes.Buffer{}
	cmd := DatatugCommand()
	cmd.Writer = stdout
	cmd.ErrWriter = &bytes.Buffer{}
	// Override the default ExitErrHandler (which calls os.Exit) so the test
	// can observe the returned error directly instead of killing itself.
	cmd.ExitErrHandler = func(_ context.Context, _ *cli.Command, _ error) {}
	err = cmd.Run(context.Background(), append([]string{"datatug"}, argv...))
	return
}

var subcommandLineShape = regexp.MustCompile(`^datatug \S+ \(\S+\) \S+\n$`)

// REQ: subcommand-output — `datatug version` prints exactly one line of the
// form "datatug <version> (<commit>) <date>", newline-terminated.
func TestVersionCommand_PrintsSubcommandLine(t *testing.T) {
	stdout, err := runDatatug(t, "version")
	assert.NoError(t, err)
	assert.Equal(t, dtlog.VersionLine()+"\n", stdout.String())
	assert.Regexp(t, subcommandLineShape, stdout.String())
}

// REQ: flag-output, REQ: short-flag — `--version` and `-v` print only the
// bare semver, identically, with no program name, commit or date.
func TestVersionFlag_PrintsBareSemver(t *testing.T) {
	for _, flag := range []string{"--version", "-v"} {
		t.Run(flag, func(t *testing.T) {
			stdout, err := runDatatug(t, flag)
			assert.NoError(t, err)
			assert.Equal(t, dtlog.Version()+"\n", stdout.String())
		})
	}
}

// AC: scripting-friendly-flag — $(datatug --version) must yield a single
// bare semver: no leading "v" (REQ: no-v-prefix), no trailing whitespace
// beyond one newline, no embedded whitespace once that newline is trimmed.
func TestVersionFlag_ScriptingFriendly(t *testing.T) {
	stdout, err := runDatatug(t, "--version")
	assert.NoError(t, err)

	got := stdout.String()
	assert.True(t, strings.HasSuffix(got, "\n"))

	trimmed := strings.TrimSuffix(got, "\n")
	assert.Equal(t, dtlog.Version(), trimmed, "$(datatug --version) must yield exactly the version string")
	assert.False(t, strings.HasPrefix(trimmed, "v"), "REQ: no-v-prefix")
	assert.NotContains(t, trimmed, "\n", "must be a single line")
	assert.NotContains(t, trimmed, " ", "must be a bare token, no program name/commit/date")
}

// AC: surfaces-agree — `datatug version`, `datatug --version` and
// `datatug -v` all report the same version string.
func TestVersion_SurfacesAgree(t *testing.T) {
	subOut, err := runDatatug(t, "version")
	assert.NoError(t, err)
	longOut, err := runDatatug(t, "--version")
	assert.NoError(t, err)
	shortOut, err := runDatatug(t, "-v")
	assert.NoError(t, err)

	v := strings.TrimSuffix(longOut.String(), "\n")
	assert.NotEmpty(t, v)
	assert.Equal(t, v, strings.TrimSuffix(shortOut.String(), "\n"), "--version and -v must be identical")
	assert.True(t, strings.HasPrefix(subOut.String(), "datatug "+v+" ("), "subcommand must embed the same version")
}
