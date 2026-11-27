package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/dbmirror"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/spf13/cobra"
)

// exitCodeIncomplete is the exit code of a `db copy --schema=destination` run
// that finished but whose output is not a complete copy of what was selected.
const exitCodeIncomplete = 3

// The --schema values of db copy: where the collections and their shapes come from.
const (
	copySchemaSource      = "source"
	copySchemaDestination = "destination"
)

// destinationSchemaFlags are the db copy flags that apply only with
// --schema=destination; sourceSchemaFlags the ones that apply only without it.
var (
	destinationSchemaFlags = []string{"policy", "definitions", "space", "path", "max-reads", "page-size", "keep-rejected", "continue-on-long-ids", "prune", "restart", "snapshot"}
	sourceSchemaFlags      = []string{"from-schema", "to-schema", "fail-on-warning", "decimals", "recover-job-ref", "overwrite", "parallel-streams", "exclude", "where", "limit", "filter-config"}
)

// addDestinationSchemaFlags adds the flags of `db copy --schema=destination`:
// a read-only copy of a chosen part of a Firestore database into an inGitDB
// database laid out as the inGitDB definitions of a mirror say (see
// pkg/dbmirror and docs/db-copy.md).
func addDestinationSchemaFlags(cmd *cobra.Command) {
	flags := cmd.Flags()
	flags.String("schema", copySchemaSource, "Where the collections to copy and their shapes come from: source (default: the source's own schema) or destination (the inGitDB definitions of a mirror, for a source without a schema such as Firestore: see \"Copying from a schemaless source\" in docs/db-copy.md).")
	flags.String("policy", "", "With --schema=destination: the mirror policy file of the project (format datatug-mirror-policy/v1). Required with --schema=destination.")
	flags.String("definitions", "", "With --schema=destination: folder of the inGitDB definitions of the mirror; default: the policy's definitions.")
	flags.String("space", "", "With --schema=destination: copy only the documents under spaces/<ID> (the same as --path spaces/<ID>).")
	flags.String("path", "", "With --schema=destination: copy only the documents at or below this Firestore path: a document (ext/billing) or a collection (spaces/abc/ext).")
	flags.Int64("max-reads", 10000, "With --schema=destination: stop before more than this many Firestore reads (0: no limit); the run then exits 3, and the next run continues where it stopped.")
	flags.Int("page-size", dbmirror.DefaultPageSize, "With --schema=destination: documents per read request.")
	flags.Bool("keep-rejected", false, "With --schema=destination: also write each rejected document under <output>/.datatug-mirror/rejected/ with only the policy's masks applied: its undeclared fields and the values that break its definition are written IN CLEAR (by default only its path, reasons and declared field names are kept).")
	flags.Bool("continue-on-long-ids", false, "With --schema=destination: do not stop at a document whose id is too long for its collection's one-file-per-record form (255 bytes): copy the rest, count it in the report, and exit 3.")
	flags.Bool("prune", false, "With --schema=destination: delete the record files whose document no longer exists, in the collections this run read completely (by default they are only reported).")
	flags.Bool("restart", false, "With --schema=destination: start over instead of continuing a run that stopped at its read budget.")
	flags.Bool("snapshot", false, "With --schema=destination: read every document as it was when the run started (Firestore keeps old versions for an hour), so the copy is consistent.")
	// With --schema=destination, --to may be left out: the policy names the output.
	// cobra checks required flags after PreRunE and before RunE.
	cmd.PreRunE = checkCopySchema
}

// checkCopySchema checks --schema and the flags of each mode before cobra's
// required-flag check, so a mode mistake gets its own message, not that --to
// is missing. With --schema=destination, --to may be left out: the policy
// names the output.
func checkCopySchema(cmd *cobra.Command, _ []string) error {
	schema, _ := cmd.Flags().GetString("schema")
	if schema != copySchemaSource && schema != copySchemaDestination {
		return Exit(fmt.Sprintf("invalid --schema value %q: valid values are source, destination", schema), exitCodeUsage)
	}
	if reason := copySchemaRefusal(cmd, schema); reason != "" {
		return Exit(reason, exitCodeUsage)
	}
	if schema == copySchemaDestination {
		delete(cmd.Flags().Lookup("to").Annotations, cobra.BashCompOneRequiredFlag)
	}
	return nil
}

// copySchemaRefusal is the usage error of a flag given in the --schema mode
// it does not apply to, or "".
func copySchemaRefusal(cmd *cobra.Command, schema string) string {
	if schema == copySchemaDestination {
		for _, name := range sourceSchemaFlags {
			if cmd.Flags().Changed(name) {
				return fmt.Sprintf("--%s does not apply with --schema=destination: the collections and their shapes come from the destination's definitions and its policy", name)
			}
		}
		return ""
	}
	for _, name := range destinationSchemaFlags {
		if cmd.Flags().Changed(name) {
			return fmt.Sprintf("--%s applies only with --schema=destination: add --schema=destination to copy by the destination's definitions", name)
		}
	}
	return ""
}

var (
	openMirrorSource = func(ctx context.Context, ref dbcopy.BackendRef) (dbmirror.Source, func() error, error) {
		source, err := dbmirror.OpenFirestore(ctx, ref.ProjectID, ref.DatasetID)
		if err != nil {
			return nil, nil, err
		}
		return source, source.Close, nil
	}
	// mirrorRun is the engine of --schema=destination (a test seam).
	mirrorRun = dbmirror.Run
	// mirrorAbs makes the output path absolute (a test seam).
	mirrorAbs = filepath.Abs
	// gitCommand runs git; a variable so tests can stand in for a missing git.
	gitCommand = func(dir string, args ...string) *exec.Cmd {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = gitEnvironment()
		return cmd
	}
)

// dbCopyFromDestinationSchema is `db copy --schema=destination`: the
// collections and their shapes come from the inGitDB definitions of the
// destination, and a policy says which of them are copied (pkg/dbmirror).
func dbCopyFromDestinationSchema(cmd *cobra.Command) error {
	ctx := cmd.Context()
	flags := cmd.Flags()
	from, _ := flags.GetString("from")
	policyPath, _ := flags.GetString("policy")
	if from == "" {
		return Exit("required flag(s) \"from\" not set", exitCodeUsage)
	}
	if policyPath == "" {
		return Exit("--schema=destination requires --policy: the mirror policy file of the project that says which defined collections are copied", exitCodeUsage)
	}
	// --to is the output folder: a DALgo URL of another database is refused,
	// not taken for a folder name (a folder named "sqlite:").
	if to, _ := flags.GetString("to"); strings.Contains(to, "://") && !strings.HasPrefix(to, "ingitdb://") {
		scheme, _, _ := strings.Cut(to, "://")
		return Exit(fmt.Sprintf("--to: --schema=destination writes an inGitDB folder (a path or ingitdb://PATH), not %s://", scheme), exitCodeUsage)
	}
	space, _ := flags.GetString("space")
	under, _ := flags.GetString("path")
	dryRun, _ := flags.GetBool("dry-run")
	maxReads, _ := flags.GetInt64("max-reads")
	pageSize, _ := flags.GetInt("page-size")
	stderr := cmd.ErrOrStderr()

	if space != "" && under != "" {
		return Exit("--space and --path are mutually exclusive", exitCodeUsage)
	}
	if space != "" {
		if strings.Contains(space, "/") {
			return Exit("--space takes one space ID", exitCodeUsage)
		}
		under = "spaces/" + space
	}
	if maxReads < 0 || pageSize < 1 {
		return Exit("--max-reads must be 0 or more and --page-size 1 or more", exitCodeUsage)
	}
	ref, _, err := parseProjectSourceArg(ctx, from)
	if err != nil {
		return keepExit(err, func(err error) error {
			return Exit(fmt.Sprintf("--from: %v", dbcopy.RedactErrorWithSecrets(err, from)), exitCodeUsage)
		})
	}
	if ref.Scheme != "firestore" {
		return Exit("--from: --schema=destination reads a Firestore source only (firestore://PROJECT[/DATABASE]); for other databases leave out --schema", exitCodeUsage)
	}
	policy, err := dbmirror.ReadPolicy(policyPath)
	if err != nil {
		return Exit(fmt.Sprintf("--policy: %v", err), exitCodeUsage)
	}
	definitionsDir, _ := flags.GetString("definitions")
	if definitionsDir == "" {
		definitionsDir = policy.DefinitionsDir()
	}
	if definitionsDir == "" {
		return Exit("the definitions of the mirror are not known: set definitions in the policy or pass --definitions", exitCodeUsage)
	}
	defs, err := dbmirror.LoadDefinitions(definitionsDir)
	if err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}
	output := ""
	if !dryRun {
		if output, err = mirrorOutput(cmd, policy, defs.Dir); err != nil {
			return err
		}
	}
	var collections []string
	if list, _ := flags.GetString("include"); list != "" {
		for _, name := range strings.Split(list, ",") {
			if name = strings.TrimSpace(name); name != "" {
				collections = append(collections, name)
			}
		}
	}

	source, closeSource, err := openMirrorSource(ctx, ref)
	if err != nil {
		return Exit(fmt.Sprintf("open --from: %v", dbcopy.RedactErrorWithSecrets(err, from)), exitCodeDatabase)
	}
	defer func() { _ = closeSource() }()
	sourceDisplay := dbcopy.SourceDisplay(from)
	_, _ = fmt.Fprintf(stderr, "db copy: reading Firestore project %s, database %s%s (read-only)\n", ref.ProjectID, ref.DatasetID, emulatorNote())
	if output != "" {
		_, _ = fmt.Fprintf(stderr, "db copy: writing %s\n", output)
	}
	opts := dbmirror.Options{
		Output: output, Collections: collections, Under: under, DryRun: dryRun,
		MaxReads: maxReads, PageSize: pageSize, Source: sourceDisplay, Log: stderr,
		Identity: dbmirror.SourceIdentity{Project: ref.ProjectID, Database: ref.DatasetID, Emulator: os.Getenv("FIRESTORE_EMULATOR_HOST")},
	}
	opts.KeepRejected, _ = flags.GetBool("keep-rejected")
	if opts.KeepRejected && !dryRun {
		_, _ = fmt.Fprintln(stderr, "db copy: warning: --keep-rejected writes each rejected document with only the policy's masks applied: its undeclared fields and the values that break its definition are written in clear under .datatug-mirror/rejected/")
	}
	opts.ContinueOnLongIDs, _ = flags.GetBool("continue-on-long-ids")
	opts.Prune, _ = flags.GetBool("prune")
	opts.Restart, _ = flags.GetBool("restart")
	opts.Snapshot, _ = flags.GetBool("snapshot")
	if progress, _ := flags.GetBool("progress"); progress {
		opts.Progress = stderr
	}
	report, err := mirrorRun(ctx, source, defs, policy, opts)
	if errors.Is(err, dbmirror.ErrUsage) {
		return Exit(strings.TrimPrefix(err.Error(), dbmirror.ErrUsage.Error()+": "), exitCodeUsage)
	}
	asJSON, _ := flags.GetBool("json")
	if errors.Is(err, dbmirror.ErrIDTooLong) || errors.Is(err, dbmirror.ErrRoundTrip) {
		if asJSON && report != nil {
			writeMirrorJSON(cmd.OutOrStdout(), report)
		}
		return Exit(fmt.Sprintf("db copy: %v", err), 1)
	}
	if err != nil {
		return Exit(dbcopy.RedactErrorWithSecrets(err, from).Error(), 1)
	}
	if asJSON {
		writeMirrorJSON(cmd.OutOrStdout(), report)
	}
	if dryRun && report.BudgetReached {
		return Exit(fmt.Sprintf("db copy: the dry run stopped at the read budget of %d reads: the counts are incomplete", maxReads), exitCodeIncomplete)
	}
	if !dryRun && !report.Complete {
		return Exit(fmt.Sprintf("db copy: the output is incomplete: %s", strings.Join(report.Incomplete, "; ")), exitCodeIncomplete)
	}
	return nil
}

func writeMirrorJSON(w io.Writer, report *dbmirror.Report) {
	content, _ := json.MarshalIndent(report, "", "  ") // the report holds only strings, numbers and bools
	_, _ = fmt.Fprintln(w, string(content))
}

func emulatorNote() string {
	if host := os.Getenv("FIRESTORE_EMULATOR_HOST"); host != "" {
		return " on the emulator at " + host
	}
	return ""
}

// mirrorOutput resolves the output folder (--to, else the policy's output,
// else data/<policy name> in the project) and refuses one that is or holds the
// definitions, or that git would commit.
func mirrorOutput(cmd *cobra.Command, policy *dbmirror.Policy, definitionsDir string) (string, error) {
	output, _ := cmd.Flags().GetString("to")
	output = strings.TrimPrefix(output, "ingitdb://")
	if output == "" {
		output = policy.OutputDir()
	}
	if output == "" {
		name := strings.TrimSuffix(filepath.Base(policy.Path), filepath.Ext(policy.Path))
		output = filepath.Join(projectRootOf(filepath.Dir(policy.Path)), "data", name)
	}
	abs, err := mirrorAbs(output)
	if err != nil {
		return "", Exit(fmt.Sprintf("--to: %v", err), exitCodeUsage)
	}
	if within(abs, definitionsDir) || within(definitionsDir, abs) {
		return "", Exit(fmt.Sprintf("the output %s must be outside the definitions folder %s, and must not hold it", abs, definitionsDir), exitCodeUsage)
	}
	if problem, err := gitIgnoreProblem(abs); err != nil {
		return "", Exit(fmt.Sprintf("check the output against .gitignore: %v (the output must be ignored by a .gitignore the repository tracks, or be outside any repository)", err), 1)
	} else if problem != "" {
		return "", Exit(fmt.Sprintf("the output %s is inside a git repository and %s, so the copied records could be committed: add the folder (for example /data/) to a .gitignore of the repository and commit it, or choose a folder outside the repository", abs, problem), exitCodeUsage)
	}
	return abs, nil
}

// within reports whether path is dir or below it.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// projectRootOf is the nearest folder at or above dir holding a DataTug
// project file, else dir.
func projectRootOf(dir string) string {
	for current := dir; ; {
		if _, err := os.Stat(filepath.Join(current, storage.ProjectSummaryFileName)); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return dir
		}
		current = parent
	}
}

// gitIgnoreProblem says why git could commit what is written at path, or "".
// The check fails closed. An output is allowed only when
//
//   - it is outside any git work tree (git says "not a git repository", or,
//     without git installed, no folder at or above it holds a .git), or
//   - the rule git ignores it by is a non-negated pattern of a .gitignore file
//     inside the work tree that the repository tracks, so every clone ignores
//     it too.
//
// An output ignored only by a rule of this machine (core.excludesFile, the
// global ignore file, .git/info/exclude) or of an untracked .gitignore is
// refused, and so is one git does not ignore. Any other git failure is an
// error. git runs without the GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE and
// GIT_COMMON_DIR of the environment, so it looks at the folder itself.
func gitIgnoreProblem(path string) (string, error) {
	dir := existingDir(path)
	top, err := gitCommand(dir, "rev-parse", "--show-toplevel").Output()
	var exitErr *exec.ExitError
	switch {
	case errors.Is(err, exec.ErrNotFound):
		if repo := enclosingRepository(dir); repo != "" {
			return "", fmt.Errorf("git is not installed, and %s holds a git repository, so whether it ignores the output cannot be checked", repo)
		}
		return "", nil
	case errors.As(err, &exitErr) && exitErr.ExitCode() == 128 && strings.Contains(string(exitErr.Stderr), "not a git repository"):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("git rev-parse: %w %s", err, gitStderr(err))
	}
	root := strings.TrimSpace(string(top))
	check := gitCommand(root, "check-ignore", "--verbose", "-z", "--stdin")
	check.Stdin = strings.NewReader(path + "\x00")
	out, err := check.Output()
	switch {
	case errors.As(err, &exitErr) && exitErr.ExitCode() == 1:
		return "not ignored by it", nil
	case err != nil:
		return "", fmt.Errorf("git check-ignore: %w %s", err, gitStderr(err))
	}
	fields := strings.Split(string(out), "\x00")
	if len(fields) < 4 {
		return "", fmt.Errorf("git check-ignore printed %q, not source, line, pattern and path", out)
	}
	source, line, pattern := fields[0], fields[1], fields[2]
	switch {
	case strings.HasPrefix(pattern, "!"):
		return "not ignored by it (" + source + ":" + line + " re-includes it)", nil
	case filepath.IsAbs(source) || !filepath.IsLocal(source) || filepath.Base(source) != ".gitignore" || slices.Contains(strings.Split(filepath.ToSlash(source), "/"), ".git"):
		return "ignored only by " + source + ", which is not a .gitignore of the repository (other clones would not ignore it)", nil
	}
	if err := gitCommand(root, "ls-files", "--error-unmatch", "--", source).Run(); err != nil {
		return "ignored only by " + source + ", which the repository does not track (other clones would not ignore it)", nil
	}
	// The rule must be committed: an edit not in HEAD (staged or not) does not count.
	if err := gitCommand(root, "diff", "--quiet", "HEAD", "--", source).Run(); err != nil {
		return "ignored by " + source + ":" + line + ", but " + source + " has changes that are not committed (other clones would not have them): commit it", nil
	}
	return "", nil
}

// existingDir is the nearest folder at or above path that exists.
func existingDir(path string) string {
	for {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return path
		}
		path = filepath.Dir(path)
	}
}

// enclosingRepository is the nearest folder at or above dir holding .git, or "".
func enclosingRepository(dir string) string {
	for current := dir; ; current = filepath.Dir(current) {
		if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil {
			return current
		}
		if filepath.Dir(current) == current {
			return ""
		}
	}
}

func gitStderr(err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return strings.TrimSpace(string(exitErr.Stderr))
	}
	return ""
}

// gitEnvironment is the environment without the variables that point git at
// another repository than the folder it runs in.
func gitEnvironment() []string {
	return slices.DeleteFunc(os.Environ(), func(variable string) bool {
		name, _, _ := strings.Cut(variable, "=")
		return slices.Contains([]string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_CEILING_DIRECTORIES"}, name)
	})
}

// hintFirestoreMirror points a db copy or db export from Firestore at db copy
// --schema=destination: Firestore has no schema to list collections from.
func hintFirestoreMirror(w io.Writer, source dbcopy.BackendRef) {
	if source.Scheme == "firestore" {
		_, _ = fmt.Fprintln(w, "hint: Firestore has no schema to list its collections from; to copy it into inGitDB, use `datatug db copy --schema=destination` with the inGitDB definitions of a mirror and its policy (see docs/db-copy.md)")
	}
}
