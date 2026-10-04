package commands

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
)

// `datatug db <url>` takes a database URL as its argument and prints it back.
func TestDBOpen_NeverPrintsThePassword(t *testing.T) {
	for _, tc := range []struct {
		name string
		arg  string
	}{
		{"postgres", "postgres://alice:" + sourceSecret + "@db.example.com/shop"},
		{"password with an escaped space", "postgres://alice:s3cr3t%20DT01@db.example.com/shop"},
		{"mysql with a query password", "mysql://alice@db.example.com/shop?password=" + sourceSecret},
		// Go reads this as host "alice", port 42; it is a password that holds a slash.
		{"password that starts with digits and holds a slash", "postgres://alice:42/" + sourceSecret + "@db.example.com/shop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := dbCommand()
			var err error
			out := covDCaptureStdout(t, func() { err = cmd.RunE(cmd, []string{tc.arg}) })
			if err != nil {
				t.Fatal(err)
			}
			assertNoSecret(t, out)
			if strings.Contains(out, "s3cr3t") || strings.Contains(out, "DT01") {
				t.Fatalf("stdout leaks the password: %q", out)
			}
			if !strings.Contains(out, "Opening database at ") || !strings.Contains(out, "xxxxx") {
				t.Fatalf("stdout = %q, want the redacted URL", out)
			}
		})
	}
}

func TestDBOpen_ParseFailureShowsNeitherTheArgumentsNorTheParserText(t *testing.T) {
	for _, arg := range []string{
		"postgres://alice:" + sourceSecret + "@db.example.com:notaport/shop",
		"postgres://alice:" + sourceSecret + " with space@db.example.com/shop",
		"postgres://alice:s3%zzcret" + sourceSecret + "@h/db",
	} {
		cmd := dbCommand()
		var err error
		out := covDCaptureStdout(t, func() { err = cmd.RunE(cmd, []string{arg, "extra-" + sourceSecret}) })
		if err == nil {
			t.Fatalf("%q must fail to parse", arg)
		}
		assertNoSecret(t, out, err.Error())
		if out != "" {
			t.Errorf("stdout = %q, want nothing", out)
		}
		if strings.Contains(err.Error(), "alice") || strings.Contains(err.Error(), "db.example.com") {
			t.Errorf("error %q echoes the argument", err)
		}
	}
}

// Every message `db copy` writes about --from or --to, whichever scheme.
func TestDBCopy_NeverEchoesAPasswordFromEitherSide(t *testing.T) {
	t.Setenv("DT01_COPY_PG", "postgres://alice:"+sourceSecret+"@127.0.0.1:1/shop")
	// Both sides must reach Open: the other side is a real, empty SQLite file.
	dir := t.TempDir()
	emptySource := "sqlite://" + filepath.Join(dir, "in.db")
	emptyTarget := "sqlite://" + filepath.Join(dir, "out.db")
	for _, path := range []string{filepath.Join(dir, "in.db"), filepath.Join(dir, "out.db")} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	secretURLs := []string{
		"postgres://alice:" + sourceSecret + "@127.0.0.1:1/shop?sslmode=disable",
		"postgres://alice:42/" + sourceSecret + "@127.0.0.1:1/shop",
		"ingitdb://" + sourceSecret + "@github.com/org/repo",
		"ingitdb://https://" + sourceSecret + "@github.com/org/repo",
		"postgres://alice:" + sourceSecret + "@127.0.0.1:bad/shop",
		"mysql://alice:" + sourceSecret + "@db.example.com/shop",
		"ingitdb://https://alice:" + sourceSecret + "@github.com/org/repo",
		"ingitdb://alice:" + sourceSecret + "@github.com/org/repo",
		"https://alice:" + sourceSecret + "@api.example.com/x",
		"http://alice:" + sourceSecret + "@host/x",
		"sqlite://alice:" + sourceSecret + "@host/x.db",
		"env:DT01_COPY_PG",
		"env:" + sourceSecret,
	}
	for _, secretURL := range secretURLs {
		for _, side := range []string{"from", "to"} {
			argv := []string{"db", "copy", "--" + side, secretURL}
			if side == "from" {
				argv = append(argv, "--to", emptyTarget)
			} else {
				argv = append(argv, "--from", emptySource)
			}
			stdout, stderr, err := runCopy(t, argv...)
			if err == nil {
				t.Fatalf("%v must fail", argv)
			}
			assertNoSecret(t, stdout.String(), stderr.String(), err.Error())
			if strings.Contains(err.Error(), "s3cr3t") {
				t.Fatalf("%v: error leaks the password: %v", argv, err)
			}
		}
	}
	_, _, err := runCopy(t, "db", "copy", "--from", "postgres://alice:"+sourceSecret+"@127.0.0.1:1/shop", "--to", emptyTarget)
	if err == nil || !strings.Contains(err.Error(), "open --from") {
		t.Fatalf("the failing side should still be named: %v", err)
	}
	// The --to side reaches Open too (the source file exists), and says so.
	for _, secretURL := range []string{
		"postgres://alice:" + sourceSecret + "@127.0.0.1:1/shop",
		"postgres://alice:42/" + sourceSecret + "@127.0.0.1:1/shop",
	} {
		stdout, stderr, err := runCopy(t, "db", "copy", "--from", emptySource, "--to", secretURL)
		if err == nil || !strings.Contains(err.Error(), "open --to") {
			t.Fatalf("the --to side should reach Open and be named: %v", err)
		}
		assertNoSecret(t, stdout.String(), stderr.String(), err.Error())
	}
}

// `datatug updateUrlConfig` was printing its own parameters, password included.
func TestExecuteSQL_NeverPrintsThePassword(t *testing.T) {
	cov100eSetup(t)
	const password = sourceSecret + ";x y"
	v := cov100eCmd()
	v.Password = password
	v.CommandText = "fail"

	text := v.String()
	if strings.Contains(text, password) || !strings.Contains(text, "Password:xxxxx") || !strings.Contains(text, "User:u") {
		t.Fatalf("String() = %q", text)
	}
	if empty := (&executeSQLCommand{Host: "h"}).String(); strings.Contains(empty, "xxxxx") {
		t.Fatalf("a command without a password must not show a marker: %q", empty)
	}

	var logged bytes.Buffer
	savedLog := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(savedLog)
	var err error
	out := covDCaptureStdout(t, func() { err = v.Execute() })
	if err == nil {
		t.Fatal("want the query error")
	}
	assertNoSecret(t, out, logged.String(), err.Error())
	if strings.Contains(out+logged.String(), "x y") {
		t.Fatalf("the password leaked.\nstdout: %q\nlog: %q", out, logged.String())
	}
}

func TestExecuteSQL_ErrorsThatQuoteThePasswordAreRedacted(t *testing.T) {
	cov100eSetup(t)
	cov100ePrepareError = "login failed, password was " + sourceSecret
	defer func() { cov100ePrepareError = "" }()
	v := cov100eCmd()
	v.Password = sourceSecret
	v.CommandText = "anything"
	var logged bytes.Buffer
	savedLog := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(savedLog)
	var err error
	_ = covDCaptureStdout(t, func() { err = v.Execute() })
	if err == nil {
		t.Fatal("want the query error")
	}
	assertNoSecret(t, err.Error(), logged.String())
	if !strings.Contains(err.Error(), "login failed") {
		t.Fatalf("the error should keep its text: %v", err)
	}
}

// A project directory such as my@proj or @acme/proj worked as an http source
// before credentials were refused in http URLs; every command that builds one
// from a directory must keep working.
func TestRunHTTPSavedQuery_ProjectDirectoryWithAnAtSignStillParses(t *testing.T) {
	orig := runStructuredHTTPQuery
	t.Cleanup(func() { runStructuredHTTPQuery = orig })
	var seenSource string
	runStructuredHTTPQuery = func(_ context.Context, _ *secureread.Executor, sourceURL string, _ dal.Query, _ map[string]any) (secureread.Result, error) {
		seenSource = sourceURL
		return secureread.Result{}, nil
	}
	def := &datatug.QueryDef{}
	def.ID = "web"
	executor := secureread.NewExecutor(secureread.Session{Unrestricted: true})
	for _, dir := range []string{"my@proj", "@acme/proj", "plain-proj"} {
		if _, err := runHTTPSavedQuery(context.Background(), executor, dir, def, nil); err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		ref, err := dbcopy.Parse(seenSource)
		if err != nil {
			t.Fatalf("%s: the source URL %q must parse: %v", dir, seenSource, err)
		}
		if strings.TrimPrefix(ref.Path, "./") != dir {
			t.Errorf("%s: parsed path %q names another directory", dir, ref.Path)
		}
	}
}

// The chat's saved-query service names its HTTP source from the project
// directory; a relative directory holding an "@" must still give a source URL
// that parses.
func TestChatSavedQueries_HTTPSourceOfAProjectDirectoryWithAnAtSign(t *testing.T) {
	built := covCWriteProject(t, covCQueryFiles("f", "web", "HTTP", "http", "https://example.test/x", ""))
	parent := t.TempDir()
	if err := os.Rename(built, filepath.Join(parent, "my@proj")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(parent)

	orig := runStructuredHTTPQuery
	t.Cleanup(func() { runStructuredHTTPQuery = orig })
	runStructuredHTTPQuery = func(context.Context, *secureread.Executor, string, dal.Query, map[string]any) (secureread.Result, error) {
		return secureread.Result{Columns: []string{"a"}}, nil
	}
	service := covCChatService(t, "my@proj", true)
	result, err := service.RunHTTPWithVariables(context.Background(), "f/web", nil)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := dbcopy.Parse(result.Source)
	if err != nil {
		t.Fatalf("the chat source %q must parse: %v", result.Source, err)
	}
	if ref.Path != "./my@proj" {
		t.Errorf("source path = %q, want ./my@proj", ref.Path)
	}
}
