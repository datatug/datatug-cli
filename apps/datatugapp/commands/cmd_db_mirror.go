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

// exitCodeIncomplete is the exit code of a db mirror run that finished but
// whose output is not a complete copy of what was selected.
const exitCodeIncomplete = 3

// dbMirrorCommand wires `datatug db mirror`: a read-only copy of a chosen part
// of a Firestore database into an inGitDB database laid out as the inGitDB
// definitions of a mirror say (see pkg/dbmirror and docs/db-mirror.md).
func dbMirrorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mirror",
		Short: "Copy a chosen part of a Firestore database, read-only, into an inGitDB database laid out by a mirror's definitions",
		Long: "Firestore has no schema, so `db copy` and `db export` cannot read it. `db mirror` takes what to read from the inGitDB definitions of a mirror " +
			"(one collection definition per Firestore collection path, at any depth: teams/{team}/ext/{module}/events is the definition " +
			"teams/ext/events) and from a mirror policy file of the DataTug project (format datatug-mirror-policy/v1) that lists the collections " +
			"to copy, those excluded, the fields to mask, and the Firestore paths a collection is copied from (paths: teams/*/ext/calendar/events, so " +
			"the events of another module are not copied). A defined collection the policy does not name is never copied, and is listed.\n\n" +
			"The source is only read: no write request is ever sent, whatever the credentials allow (use an identity with read-only IAM, such as " +
			"roles/datastore.viewer, as well). Credentials come only from the environment: Application Default Credentials " +
			"(gcloud auth application-default login, or GOOGLE_APPLICATION_CREDENTIALS); --from is firestore://PROJECT[/DATABASE], env:NAME of a " +
			"variable holding it, or a datatug:// reference to a project source.\n\n" +
			"Every document is written as one JSON record file at the path its definition gives, its ids encoded so that any id is a safe file " +
			"name (deterministic: a re-run over unchanged data changes no file), checked against its definition first. A document that does not " +
			"fit is not written: the report lists its path, the reasons and the names of the fields concerned, never their values (--keep-rejected " +
			"also keeps its masked record under <output>/.datatug-mirror/rejected/). The output then gets inGitDB's own validation. The run's " +
			"report is <output>/.datatug-mirror/report.json.\n\n" +
			"Choose what to read with --collections, --space and --path; --dry-run counts the documents of each collection and writes nothing; " +
			"--max-reads stops before more Firestore reads than that (each read is billed), and the next run continues where it stopped " +
			"(--restart starts over). A record file whose document no longer exists is reported; --prune deletes it. The output must be ignored " +
			"by a .gitignore its git repository tracks, or be outside any repository. Exit codes: 0 complete, 1 a read or write failed, 2 a usage " +
			"or policy error, 3 finished but incomplete (read budget reached, also in a dry run, documents rejected, or validation findings), " +
			"4 the source cannot be opened.",
		Args: cobra.NoArgs,
		RunE: dbMirrorAction,
	}
	flags := cmd.Flags()
	flags.String("from", "", "Firestore source: firestore://PROJECT[/DATABASE], env:NAME, or datatug://PROJECT/DB[?env=ENV]. Required.")
	flags.String("policy", "", "Mirror policy file of the project (format datatug-mirror-policy/v1). Required.")
	flags.String("definitions", "", "Folder of the inGitDB definitions of the mirror; default: the policy's definitions.")
	flags.String("to", "", "Output folder (or ingitdb://folder); default: the policy's output, else data/<policy name> in the project.")
	flags.String("collections", "", "Comma-separated defined collections to copy (e.g. teams,teams/ext/events); <path>/** adds every collection below it.")
	flags.String("space", "", "Copy only the documents under spaces/<ID> (the same as --path spaces/<ID>).")
	flags.String("path", "", "Copy only the documents at or below this Firestore path: a document (ext/billing) or a collection (spaces/abc/ext).")
	flags.Bool("dry-run", false, "Count the documents of each selected collection and write nothing.")
	flags.Int64("max-reads", 10000, "Stop before more than this many Firestore reads (0: no limit); the run then exits 3.")
	flags.Int("page-size", dbmirror.DefaultPageSize, "Documents per read request.")
	flags.Bool("progress", false, "Print one line per page read on stderr.")
	flags.Bool("json", false, "Print the report as JSON on stdout.")
	flags.Bool("keep-rejected", false, "Also write each rejected document, masked, under <output>/.datatug-mirror/rejected/ (by default only its path, reasons and field names are kept).")
	flags.Bool("prune", false, "Delete the record files whose document no longer exists, in the collections this run read completely (by default they are only reported).")
	flags.Bool("restart", false, "Start over instead of continuing a run that stopped at its read budget.")
	flags.Bool("snapshot", false, "Read every document as it was when the run started (Firestore keeps old versions for an hour), so the copy is consistent.")
	_ = cmd.MarkFlagRequired("from")
	_ = cmd.MarkFlagRequired("policy")
	return cmd
}

var (
	openMirrorSource = func(ctx context.Context, ref dbcopy.BackendRef) (dbmirror.Source, func() error, error) {
		source, err := dbmirror.OpenFirestore(ctx, ref.ProjectID, ref.DatasetID)
		if err != nil {
			return nil, nil, err
		}
		return source, source.Close, nil
	}
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

func dbMirrorAction(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	flags := cmd.Flags()
	from, _ := flags.GetString("from")
	policyPath, _ := flags.GetString("policy")
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
		return Exit("--from: db mirror reads Firestore only (firestore://PROJECT[/DATABASE]); for other databases use db copy or db export", exitCodeUsage)
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
	if list, _ := flags.GetString("collections"); list != "" {
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
	_, _ = fmt.Fprintf(stderr, "db mirror: reading Firestore project %s, database %s%s (read-only)\n", ref.ProjectID, ref.DatasetID, emulatorNote())
	if output != "" {
		_, _ = fmt.Fprintf(stderr, "db mirror: writing %s\n", output)
	}
	opts := dbmirror.Options{
		Output: output, Collections: collections, Under: under, DryRun: dryRun,
		MaxReads: maxReads, PageSize: pageSize, Source: sourceDisplay, Log: stderr,
	}
	opts.KeepRejected, _ = flags.GetBool("keep-rejected")
	opts.Prune, _ = flags.GetBool("prune")
	opts.Restart, _ = flags.GetBool("restart")
	opts.Snapshot, _ = flags.GetBool("snapshot")
	if progress, _ := flags.GetBool("progress"); progress {
		opts.Progress = stderr
	}
	report, err := dbmirror.Run(ctx, source, defs, policy, opts)
	if errors.Is(err, dbmirror.ErrUsage) {
		return Exit(strings.TrimPrefix(err.Error(), dbmirror.ErrUsage.Error()+": "), exitCodeUsage)
	}
	if err != nil {
		return Exit(dbcopy.RedactErrorWithSecrets(err, from).Error(), 1)
	}
	if asJSON, _ := flags.GetBool("json"); asJSON {
		writeMirrorJSON(cmd.OutOrStdout(), report)
	}
	if dryRun && report.BudgetReached {
		return Exit(fmt.Sprintf("db mirror: the dry run stopped at the read budget of %d reads: the counts are incomplete", maxReads), exitCodeIncomplete)
	}
	if !dryRun && !report.Complete {
		return Exit(fmt.Sprintf("db mirror: the output is incomplete: %s", strings.Join(report.Incomplete, "; ")), exitCodeIncomplete)
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

// hintFirestoreMirror points a db copy or db export from Firestore at db
// mirror: Firestore has no schema to list collections from.
func hintFirestoreMirror(w io.Writer, source dbcopy.BackendRef) {
	if source.Scheme == "firestore" {
		_, _ = fmt.Fprintln(w, "hint: Firestore has no schema to list its collections from; to copy it into inGitDB, use `datatug db mirror` with the inGitDB definitions of a mirror (see docs/db-mirror.md)")
	}
}
