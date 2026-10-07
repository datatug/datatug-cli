package commands

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	bqwriter "github.com/dal-go/dalgo2bigquery"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

type copyHTTPTransport func(*http.Request) (*http.Response, error)

func (f copyHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func copyHTTPResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}
}

// runCopy invokes the copy command with the given argv slice (no "datatug"
// prefix; pass starting from "db"). Captures stderr and stdout. Returns
// the returned error (or nil) for the caller to inspect — an ExitCoder when
// the command used Exit(), a plain error otherwise.
func runCopy(t *testing.T, argv ...string) (stdout, stderr *bytes.Buffer, err error) {
	t.Helper()
	stdout = &bytes.Buffer{}
	stderr = &bytes.Buffer{}
	root := &cobra.Command{
		Use:           "datatug",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(dbCommand())
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(argv)
	err = root.ExecuteContext(context.Background())
	return
}

// REQ:required-flags — missing --from must exit 2 naming the missing flag.
func TestDBCopy_MissingFrom_Exit2(t *testing.T) {
	t.Parallel()
	_, _, err := runCopy(t, "db", "copy", "--to", "sqlite:///tmp/out.db")
	assert.Error(t, err)
	if ec, ok := err.(ExitCoder); ok {
		assert.Equal(t, 2, ec.ExitCode())
	}
	assert.Contains(t, err.Error(), "from")
}

// REQ:required-flags — missing --to must exit 2 naming the missing flag.
func TestDBCopy_MissingTo_Exit2(t *testing.T) {
	t.Parallel()
	_, _, err := runCopy(t, "db", "copy", "--from", "sqlite:///tmp/in.db")
	assert.Error(t, err)
	if ec, ok := err.(ExitCoder); ok {
		assert.Equal(t, 2, ec.ExitCode())
	}
	assert.Contains(t, err.Error(), "to")
}

// REQ:overwrite-values — bogus --overwrite=foo must exit 2.
func TestDBCopy_OverwriteBogus_Exit2(t *testing.T) {
	t.Parallel()
	_, _, err := runCopy(t, "db", "copy",
		"--from", "sqlite:///tmp/in.db",
		"--to", "sqlite:///tmp/out.db",
		"--overwrite", "merge",
	)
	assert.Error(t, err)
	if ec, ok := err.(ExitCoder); ok {
		assert.Equal(t, 2, ec.ExitCode())
	}
	msg := err.Error()
	assert.Contains(t, msg, "merge")
	assert.Contains(t, msg, "recreate")
	assert.Contains(t, msg, "reload")
}

func TestDBCopy_BigQueryRejectsOverwriteBeforeOpeningSource(t *testing.T) {
	t.Parallel()
	_, _, err := runCopy(t, "db", "copy",
		"--from", "sqlite:///tmp/source-does-not-exist.db",
		"--to", "bigquery://demodb/research?location=US",
		"--overwrite", "recreate",
	)
	if ec, ok := err.(ExitCoder); !ok || ec.ExitCode() != 2 {
		t.Fatalf("error = %v, want exit 2", err)
	}
	assert.Contains(t, err.Error(), "never replace existing tables")
}

func TestDBCopyRecoverBigQueryPollsSameJobWithoutResubmitting(t *testing.T) {
	target, err := dbcopy.Parse("bigquery://demodb/research?location=US")
	if err != nil {
		t.Fatal(err)
	}
	ref := bqwriter.LoadJobRef{JobID: "job-123", ProjectID: "demodb", DatasetID: "research", TableID: "People", Location: "US"}
	var jobGets, jobPosts int
	client := &http.Client{Transport: copyHTTPTransport(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/jobs/job-123"):
			jobGets++
			return copyHTTPResponse(req, http.StatusOK, `{"jobReference":{"projectId":"demodb","jobId":"job-123","location":"US"},"configuration":{"load":{"destinationTable":{"projectId":"demodb","datasetId":"research","tableId":"People"}}},"status":{"state":"DONE"},"statistics":{"load":{"outputRows":"3"}}}`), nil
		case req.Method == http.MethodPost && strings.Contains(req.URL.Path, "/jobs"):
			jobPosts++
			return copyHTTPResponse(req, http.StatusInternalServerError, `{}`), nil
		default:
			t.Errorf("unexpected recovery request: %s %s", req.Method, req.URL)
			return copyHTTPResponse(req, http.StatusInternalServerError, `{}`), nil
		}
	})}
	var stderr bytes.Buffer
	if err := dbCopyRecoverBigQuery(context.Background(), target, ref, client, &stderr); err != nil {
		t.Fatal(err)
	}
	if jobGets != 1 || jobPosts != 0 {
		t.Fatalf("recovery sent GET=%d POST=%d, want one GET and no resubmission", jobGets, jobPosts)
	}
	assert.Contains(t, stderr.String(), "recovered BigQuery load job job-123")
	assert.Contains(t, stderr.String(), "verified 3 rows")
}

func TestDBCopyRecoverBigQueryRejectsMismatchedTargetAndDuplicateJSONKeys(t *testing.T) {
	target, err := dbcopy.Parse("bigquery://demodb/research?location=US")
	if err != nil {
		t.Fatal(err)
	}
	duplicate := `{"jobId":"job-1","jobId":"job-2","projectId":"demodb","datasetId":"research","tableId":"People","location":"US"}`
	if _, err := decodeBigQueryLoadJobRef(duplicate); err == nil {
		t.Fatal("duplicate reference keys were accepted")
	}
	ref := bqwriter.LoadJobRef{JobID: "job-1", ProjectID: "demodb", DatasetID: "other", TableID: "People", Location: "US"}
	if err := dbCopyRecoverBigQuery(context.Background(), target, ref, &http.Client{}, io.Discard); err == nil || !strings.Contains(err.Error(), "does not match --to") {
		t.Fatalf("mismatched target error = %v", err)
	}
	_, _, err = runCopy(t, "db", "copy", "--to", "bigquery://demodb/research?location=US", "--recover-job-ref", duplicate)
	if coder, ok := err.(ExitCoder); !ok || coder.ExitCode() != 2 || !strings.Contains(err.Error(), "invalid --recover-job-ref") {
		t.Fatalf("invalid recovery command error = %v, want exit 2 before credentials", err)
	}
	_, _, err = runCopy(t, "db", "copy", "--from", "sqlite:///source.db", "--to", "bigquery://demodb/research?location=US", "--recover-job-ref", "{}")
	if coder, ok := err.(ExitCoder); !ok || coder.ExitCode() != 2 || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("mixed recovery/copy command error = %v, want exit 2", err)
	}
}

func TestBigQueryRecoveryHintContainsOnlyCredentialFreeReference(t *testing.T) {
	target, err := dbcopy.Parse("bigquery://demodb/research?location=US")
	if err != nil {
		t.Fatal(err)
	}
	ref := bqwriter.LoadJobRef{JobID: "job-123", ProjectID: "demodb", DatasetID: "research", TableID: "People", Location: "US"}
	hint := bigQueryRecoveryHint(target, ref)
	for _, want := range []string{"Do not rerun", "--recover-job-ref", `"jobId":"job-123"`, `"tableId":"People"`, "bigquery://demodb/research?location=US"} {
		if !strings.Contains(hint, want) {
			t.Errorf("recovery hint missing %q: %s", want, hint)
		}
	}
	if strings.Contains(hint, "password") || strings.Contains(hint, "rows") {
		t.Fatalf("recovery hint includes non-reference material: %s", hint)
	}
	joined := errors.Join(&bqwriter.LoadOutcomeUnknownError{Job: ref}, dbcopy.ErrStagingCleanup)
	message := bigQueryCopyRecoveryMessage(target, ref, joined)
	if !strings.Contains(message, "staged source data may remain in temporary storage") || !strings.Contains(message, "--recover-job-ref") {
		t.Fatalf("joined uncertain outcome did not report recovery and staging cleanup: %s", message)
	}
}

// REQ:unknown-scheme-rejected — exit 2 with substrings naming the bad
// scheme and the supported list.
func TestDBCopy_UnknownScheme_Exit2(t *testing.T) {
	t.Parallel()
	_, _, err := runCopy(t, "db", "copy",
		"--from", "mongodb://host/db",
		"--to", "sqlite:///tmp/out.db",
	)
	assert.Error(t, err)
	if ec, ok := err.(ExitCoder); ok {
		assert.Equal(t, 2, ec.ExitCode())
	}
	msg := err.Error()
	assert.Contains(t, msg, "mongodb")
	assert.Contains(t, msg, "sqlite")
	assert.Contains(t, msg, "ingitdb")
}

// REQ:ingitdb-url-local-only — remote ingitdb URLs exit 2.
func TestDBCopy_RemoteInGitDB_Exit2(t *testing.T) {
	t.Parallel()
	_, _, err := runCopy(t, "db", "copy",
		"--from", "sqlite:///tmp/in.db",
		"--to", "ingitdb://github.com/owner/repo",
	)
	assert.Error(t, err)
	if ec, ok := err.(ExitCoder); ok {
		assert.Equal(t, 2, ec.ExitCode())
	}
	assert.Contains(t, err.Error(), "local paths only")
}

// End-to-end happy path against the checked-in Chinook fixture going to
// an empty inGitDB target. Succeeds with exit 0, the row-copy summary on
// stderr, schemas+rows for the 7 describe-able tables (including
// PlaylistTrack with its composite PK, copied via `__`-joined keys);
// 4 tables are describe-skipped because dalgo2sqlite can't describe
// DATETIME / NUMERIC; tracked upstream).
func TestDBCopy_Chinook_SQLiteToInGitDB_HappyPath(t *testing.T) {
	t.Parallel()
	chinook, err := filepath.Abs("../../../pkg/dbcopy/testdata/chinook.db")
	assert.NoError(t, err)

	tgtDir := t.TempDir()
	_, stderr, runErr := runCopy(t, "db", "copy",
		"--from", "sqlite://"+chinook,
		"--to", "ingitdb://"+tgtDir,
	)
	assert.NoError(t, runErr)
	assert.Contains(t, stderr.String(), "db copy: replicated schema for 11/11 collections (0 skipped)")
	// Full Chinook: 347+275+59+8+25+412+2240+5+18+8715+3503 = 15607.
	assert.Contains(t, stderr.String(), "copied 15607 rows")
	assert.NotContains(t, stderr.String(), "row copy skipped",
		"no row skips expected after composite-PK support landed")
	assert.NotContains(t, stderr.String(), "skipping",
		"no describe-skips expected after dalgo2sqlite DATETIME/NUMERIC support landed")
}
