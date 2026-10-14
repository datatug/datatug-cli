package commands

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	bigquery "github.com/dal-go/dalgo2bigquery"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/bigqueryread"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestBigQueryExitCodeAndFileLimit(t *testing.T) {
	t.Parallel()
	cause := errors.New("cause-err")
	surfErr := &bigQuerySurfaceError{message: "surf-msg", cause: cause}
	require.Equal(t, exitCodeDatabase, surfErr.ExitCode())
	require.Equal(t, "surf-msg", surfErr.Error())
	require.Equal(t, cause, surfErr.Unwrap())

	// decodeBigQueryFileLimit error on empty name
	require.ErrorIs(t, decodeBigQueryFileLimit("", nil, nil, 100), bigqueryread.ErrInput)

	// decodeBigQueryFileLimit error on non-existent file
	require.ErrorIs(t, decodeBigQueryFileLimit("non-existent-file-path", nil, nil, 100), bigqueryread.ErrInput)

	// decodeBigQueryFileLimit success on stdin "-"
	var val map[string]string
	err := decodeBigQueryFileLimit("-", strings.NewReader(`{"key":"value"}`), &val, 100)
	require.NoError(t, err)
	require.Equal(t, "value", val["key"])

	// decodeBigQueryFile wrapper on real file
	dir := t.TempDir()
	p := filepath.Join(dir, "input.json")
	require.NoError(t, os.WriteFile(p, []byte(`{"hello":"world"}`), 0600))
	var m map[string]string
	require.NoError(t, decodeBigQueryFile(p, nil, &m))
	require.Equal(t, "world", m["hello"])
}

func TestBigQueryPolicyOptionsAndFailures(t *testing.T) {
	t.Parallel()
	cmd := queryBigQueryCommand()
	cmd.Flags().Set("var", "invalid_no_equal_sign")
	_, err := bigQueryPolicyOptions(cmd)
	require.Error(t, err)

	// bigQueryFailure branches
	require.Nil(t, bigQueryFailure(nil))
	noPolErr := bigQueryFailure(accesspolicies.ErrNoPolicies)
	require.Equal(t, accesspolicies.ErrNoPolicies.Error(), noPolErr.Error())
	require.Equal(t, exitCodeUsage, noPolErr.(ExitCoder).ExitCode())
	require.Error(t, bigQueryFailure(bigqueryread.ErrIdentity))

	exitCoderErr := Exit("custom-exit", 7)
	require.Equal(t, exitCoderErr, bigQueryFailure(exitCoderErr))

	stErr := &bigquery.Error{Code: "invalid_input"}
	require.Equal(t, exitCodeUsage, bigQueryFailure(stErr).(ExitCoder).ExitCode())

	stPolicyErr := &bigquery.Error{Code: "policy_denied"}
	require.Equal(t, exitCodeAccessDenied, bigQueryFailure(stPolicyErr).(ExitCoder).ExitCode())

	unknownErr := errors.New("something wild happened")
	require.Equal(t, exitCodeDatabase, bigQueryFailure(unknownErr).(ExitCoder).ExitCode())
}

func TestBigQueryValidationBranches(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	runCmd := func(args ...string) error {
		c := queryBigQueryCommand()
		c.SetArgs(args)
		return c.ExecuteContext(context.Background())
	}

	// Invalid auth
	err := runCmd("preview", "--ledger", dir, "--auth", "invalid")
	require.Error(t, err)
	require.Contains(t, err.Error(), "--auth adc or --auth google is required")

	// Empty ledger
	err = runCmd("preview", "--ledger", "", "--auth", "adc")
	require.Error(t, err)
	require.Contains(t, err.Error(), "--ledger is required")

	// preflight without --operator-pilot
	err = runCmd("preflight", "--ledger", dir, "--auth", "adc")
	require.Error(t, err)
	require.Contains(t, err.Error(), "operator pilot preflight requires --operator-pilot")

	// preflight with cancelPermission
	err = runCmd("preflight", "--ledger", dir, "--auth", "adc", "--operator-pilot", "--enable-cancellation")
	require.Error(t, err)
	require.Contains(t, err.Error(), "identity preflight checks read-only scope")

	// pilot without policy
	err = runCmd("run", "--ledger", dir, "--auth", "adc", "--operator-pilot")
	require.Error(t, err)
	require.Contains(t, err.Error(), "--operator-pilot-policy is required for pilot run and control")

	// pilot preview with policy path
	err = runCmd("preview", "--ledger", dir, "--auth", "adc", "--operator-pilot", "--operator-pilot-policy", "/some/policy.json")
	require.Error(t, err)
	require.Contains(t, err.Error(), "--operator-pilot-policy follows preview")

	// pilot with reconnect
	err = runCmd("preview", "--ledger", dir, "--auth", "adc", "--operator-pilot", "--reconnect")
	require.Error(t, err)
	require.Contains(t, err.Error(), "operator pilot does not rebind or page a result")

	// cancel without enable-cancellation
	err = runCmd("cancel", "--ledger", dir, "--auth", "adc")
	require.Error(t, err)
	require.Contains(t, err.Error(), "cancel requires explicit --enable-cancellation")

	// reconnect on run
	err = runCmd("run", "--ledger", dir, "--auth", "adc", "--reconnect")
	require.Error(t, err)
	require.Contains(t, err.Error(), "--reconnect requires an existing job")

	// bounds changed on run
	err = runCmd("run", "--ledger", dir, "--auth", "adc", "--maximum-bytes-billed", "10M")
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires preview; existing run bounds cannot be renewed")

	// approve-digest changed on preview
	err = runCmd("preview", "--ledger", dir, "--auth", "adc", "--approve-digest", "abc")
	require.Error(t, err)
	require.Contains(t, err.Error(), "--approve-digest requires run")

	// format not json
	err = runCmd("preview", "--ledger", dir, "--auth", "adc", "--format", "yaml")
	require.Error(t, err)
	require.Contains(t, err.Error(), "BigQuery supports --format json only")

	// preview-out invalid
	err = runCmd("preview", "--ledger", dir, "--auth", "adc", "--preview-out", "/tmp/bad.json")
	require.Error(t, err)
	require.Contains(t, err.Error(), "--preview-out requires preview-*.json in the private ledger directory")

	// receipt-out on preview
	err = runCmd("preview", "--ledger", dir, "--auth", "adc", "--receipt-out", filepath.Join(dir, "receipt-test.json"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "--receipt-out requires receipt-*.json in the private ledger directory")

	// preview missing execution-project or maximum-bytes-billed
	h := newCliBQHarness(t)
	_, err = h.execute("preview")
	require.Error(t, err)
	require.Contains(t, err.Error(), "preview requires --execution-project and --maximum-bytes-billed")

	// preview pilot missing demodb-dev or cap
	hPilot := newCliBQPilotHarness(t)
	_, err = hPilot.execute("preview", "--execution-project", "other-project", "--maximum-bytes-billed", "100")
	require.Error(t, err)
	require.Contains(t, err.Error(), "operator pilot requires demodb-dev")

	// unknown operation
	hUnknown := newCliBQHarness(t)
	root := &cobra.Command{Use: "datatug"}
	root.AddCommand(queryCommand())
	all := []string{"query", "bigquery", "preview", "--file", hUnknown.file, "--ledger", hUnknown.dir, "--auth", "adc", "--policy", hUnknown.policy, "--policies-dir", hUnknown.dir + "/policies", "--as", "alice", "--var", "min=\"10\""}
	root.SetArgs(all)
	sub, _, err := root.Find(all)
	require.NoError(t, err)
	sub.SetContext(context.Background())
	require.NoError(t, sub.ParseFlags(all[3:]))
	err = runBigQueryCommand(sub, "unknown_operation")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown BigQuery operation")
}

func TestBigQueryExportAndPilotHelpers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// writeBigQueryExport with un-marshalable channel
	err := writeBigQueryExport(filepath.Join(dir, "bad.json"), make(chan int), dir)
	require.ErrorIs(t, err, bigqueryread.ErrInput)

	// writeBigQueryExport with non-existent directory
	err = writeBigQueryExport("/non/existent/dir/file.json", "data", "/non/existent/dir")
	require.ErrorIs(t, err, bigqueryread.ErrInput)

	// writeBigQueryRun with nil run
	err = writeBigQueryRun(nil, nil, func(any) error { return nil })
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing run receipt")

	errOp := errors.New("operation fail")
	err = writeBigQueryRun(nil, errOp, func(any) error { return nil })
	require.Error(t, err)

	// writeBigQueryPilotResult default branch
	cmd := queryBigQueryCommand()
	err = writeBigQueryPilotResult(cmd, "unsupported type", "", "", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported pilot output")

	// writeBigQueryPilotRun with nil run
	err = writeBigQueryPilotRun(nil, errors.New("pilot run op error"), func(any) error { return nil })
	require.Error(t, err)

	// privatePilotFile relative path error
	_, err = privatePilotFile("relative/path/test.json")
	require.ErrorIs(t, err, bigqueryread.ErrInput)

	// validateBigQueryPilotSourcePlan error branches
	err = validateBigQueryPilotSourcePlan(bigquery.SourceProfile{}, bigquery.ReadPlan{})
	require.ErrorIs(t, err, bigqueryread.ErrInput)

	// claimBigQueryPilot error when hash mismatch
	err = claimBigQueryPilot("/non/existent", bigQueryPilotPolicy{}, "bad-hash")
	require.ErrorIs(t, err, bigqueryread.ErrInput)
}

// Not parallel: exercises package-level bigQueryDeps default implementations.
func TestBigQueryDepsDefaults(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session")

	// ledger default
	l, err := bigQueryDeps.ledger(dir)
	require.NoError(t, err)
	require.NotNil(t, l)

	// provider default with adc and google
	pAdc, err := bigQueryDeps.provider(dir, false, "adc")
	require.NoError(t, err)
	require.NotNil(t, pAdc)

	pGoogle, err := bigQueryDeps.provider(dir, false, "google")
	require.NoError(t, err)
	require.NotNil(t, pGoogle)

	// consentIdentity default with invalid dir
	_, err = bigQueryDeps.consentIdentity(context.Background(), "/non/existent/dir/cannot/exist", false, &oauth2.Token{AccessToken: "tok"})
	require.Error(t, err)
}

type mockFailingBQProvider struct {
	err error
}

func (m *mockFailingBQProvider) Authorize(context.Context, http.RoundTripper) (bigquery.Identity, http.RoundTripper, error) {
	return bigquery.Identity{}, nil, m.err
}

// Not parallel: modifies bigQueryDeps.
func TestBigQueryRemainingBranchesAndPilot(t *testing.T) {
	oldDeps := bigQueryDeps
	t.Cleanup(func() { bigQueryDeps = oldDeps })

	dir := t.TempDir()
	sessDir := filepath.Join(t.TempDir(), "session_identity")
	_, err := bigQueryDeps.ledger(sessDir)
	require.NoError(t, err)
	_, _ = oldDeps.consentIdentity(context.Background(), sessDir, false, &oauth2.Token{AccessToken: "tok"})

	// 2. bigQueryPolicyOptions with invalid variable flag
	cmdVar := queryBigQueryCommand()
	cmdVar.Flags().Bool(queryNoPoliciesFlag, true, "")
	cmdVar.Flags().StringArray(queryVarFlag, []string{"invalid_no_equals"}, "")
	_, err = bigQueryPolicyOptions(cmdVar)
	require.Error(t, err)

	// 3. writeBigQueryResult with receiptOut and non-page value
	cmd := queryBigQueryCommand()
	err = writeBigQueryResult(cmd, "unsupported-string-val", "", "/tmp/out.json", dir)
	require.Error(t, err)

	// 4. preflight error branches: ledger err
	bigQueryDeps.ledger = func(string) (bigquery.Ledger, error) {
		return nil, errors.New("ledger error")
	}
	cmdPreflight := queryBigQueryCommand()
	for _, c := range cmdPreflight.Commands() {
		if c.Name() == "preflight" {
			_ = c.Flags().Set("auth", "adc")
			_ = c.Flags().Set("ledger", dir)
			_ = c.Flags().Set("operator-pilot", "true")
			c.SetContext(context.Background())
			err = runBigQueryCommand(c, "preflight")
			require.Error(t, err)
		}
	}

	// preflight provider err
	bigQueryDeps.ledger = func(string) (bigquery.Ledger, error) {
		return bigquery.NewMemoryLedger(), nil
	}
	bigQueryDeps.provider = func(string, bool, string) (bigquery.Provider, error) {
		return nil, errors.New("provider error")
	}
	for _, c := range cmdPreflight.Commands() {
		if c.Name() == "preflight" {
			_ = c.Flags().Set("auth", "adc")
			_ = c.Flags().Set("ledger", dir)
			_ = c.Flags().Set("operator-pilot", "true")
			c.SetContext(context.Background())
			err = runBigQueryCommand(c, "preflight")
			require.Error(t, err)
		}
	}

	// preflight authorize err
	bigQueryDeps.provider = func(string, bool, string) (bigquery.Provider, error) {
		return &mockFailingBQProvider{err: errors.New("auth error")}, nil
	}
	for _, c := range cmdPreflight.Commands() {
		if c.Name() == "preflight" {
			_ = c.Flags().Set("auth", "adc")
			_ = c.Flags().Set("ledger", dir)
			_ = c.Flags().Set("operator-pilot", "true")
			c.SetContext(context.Background())
			err = runBigQueryCommand(c, "preflight")
			require.Error(t, err)
		}
	}

	// 5. connect error branches:
	cmdConnect := queryBigQueryCommand()
	for _, c := range cmdConnect.Commands() {
		if c.Name() == "connect" {
			_ = c.Flags().Set("auth", "google")
			_ = c.Flags().Set("ledger", dir)
			c.SetContext(context.Background())

			// connect with ledger err (line 315)
			origLedger := bigQueryDeps.ledger
			bigQueryDeps.ledger = func(string) (bigquery.Ledger, error) {
				return nil, errors.New("ledger err")
			}
			err = runBigQueryCommand(c, "connect")
			require.Error(t, err)
			bigQueryDeps.ledger = origLedger

			// connect with connect err
			bigQueryDeps.connect = func(context.Context, []string) (*oauth2.Token, error) {
				return nil, errors.New("connect err")
			}
			err = runBigQueryCommand(c, "connect")
			require.Error(t, err)

			// connect with consentIdentity err
			bigQueryDeps.connect = func(context.Context, []string) (*oauth2.Token, error) {
				return &oauth2.Token{AccessToken: "tok"}, nil
			}
			bigQueryDeps.consentIdentity = func(context.Context, string, bool, *oauth2.Token) (bigquery.Identity, error) {
				return bigquery.Identity{}, errors.New("consent err")
			}
			err = runBigQueryCommand(c, "connect")
			require.Error(t, err)

			// connect with provider err
			bigQueryDeps.consentIdentity = func(context.Context, string, bool, *oauth2.Token) (bigquery.Identity, error) {
				return bigquery.Identity{Principal: bigquery.Principal{Kind: "google-user", Subject: "sub"}, Read: true}, nil
			}
			bigQueryDeps.provider = func(string, bool, string) (bigquery.Provider, error) {
				return nil, errors.New("provider err")
			}
			err = runBigQueryCommand(c, "connect")
			require.Error(t, err)

			// connect with authorize err
			bigQueryDeps.provider = func(string, bool, string) (bigquery.Provider, error) {
				return &mockFailingBQProvider{err: errors.New("auth err")}, nil
			}
			err = runBigQueryCommand(c, "connect")
			require.Error(t, err)
		}
	}

	// 6. bqExportSync error
	oldSync := bqExportSync
	t.Cleanup(func() { bqExportSync = oldSync })
	bqExportSync = func(f *os.File) error { return errors.New("sync error") }
	err = writeBigQueryExport(filepath.Join(dir, "out_sync_err.json"), map[string]string{"k": "v"}, dir)
	require.ErrorIs(t, err, bigqueryread.ErrInput)
	bqExportSync = oldSync

	// 7. pilot seams
	pilotDir := t.TempDir()
	require.NoError(t, os.Chmod(pilotDir, 0700))

	// Line 60: open error on 0000 file
	noReadFile := filepath.Join(pilotDir, "no_read.json")
	require.NoError(t, os.WriteFile(noReadFile, []byte(`{}`), 0000))
	_, err = privatePilotFile(noReadFile)
	require.ErrorIs(t, err, bigqueryread.ErrInput)

	// Line 65: pilotFileStat error
	validPilotFile := filepath.Join(pilotDir, "valid.json")
	require.NoError(t, os.WriteFile(validPilotFile, []byte(`{}`), 0600))
	oldStat := pilotFileStat
	t.Cleanup(func() { pilotFileStat = oldStat })
	pilotFileStat = func(f *os.File) (os.FileInfo, error) { return nil, errors.New("stat err") }
	_, err = privatePilotFile(validPilotFile)
	require.ErrorIs(t, err, bigqueryread.ErrInput)
	pilotFileStat = oldStat

	// Line 69: pilotReader error
	oldReader := pilotReader
	t.Cleanup(func() { pilotReader = oldReader })
	pilotReader = func(r io.Reader) ([]byte, error) { return nil, errors.New("read err") }
	_, err = privatePilotFile(validPilotFile)
	require.ErrorIs(t, err, bigqueryread.ErrInput)
	pilotReader = oldReader

	// Policy decode & validation errors
	badPilotPath := filepath.Join(pilotDir, "bad_pilot.json")
	require.NoError(t, os.WriteFile(badPilotPath, []byte(`{"format": "datatug-bigquery-operator-pilot/2", "unknownField": 1}`), 0600))
	_, _, err = readBigQueryPilotPolicy(badPilotPath)
	require.Error(t, err)

	trailingPilotPath := filepath.Join(pilotDir, "trailing_pilot.json")
	require.NoError(t, os.WriteFile(trailingPilotPath, []byte(`{"format": "datatug-bigquery-operator-pilot/2"}{"extra": 1}`), 0600))
	_, _, err = readBigQueryPilotPolicy(trailingPilotPath)
	require.Error(t, err)

	badFmtPath := filepath.Join(pilotDir, "bad_fmt.json")
	require.NoError(t, os.WriteFile(badFmtPath, []byte(`{"format": "wrong"}`), 0600))
	_, _, err = readBigQueryPilotPolicy(badFmtPath)
	require.Error(t, err)

	badTypePath := filepath.Join(pilotDir, "bad_type.json")
	require.NoError(t, os.WriteFile(badTypePath, []byte(`{"format": 12345}`), 0600))
	_, _, err = readBigQueryPilotPolicy(badTypePath)
	require.Error(t, err)

	// Claim pilot seams
	validPolicy := bigQueryPilotPolicy{
		Format:             "datatug-bigquery-operator-pilot/2",
		ApprovalDigest:     "app",
		SourceDigest:       "src",
		DescriptorDigest:   "desc",
		PlanDigest:         "plan",
		RightsReviewRef:    "rev",
		ExecutionProject:   "demodb-dev",
		MaximumBytesBilled: pilotCap,
		SessionBudgetBytes: pilotCap,
		AllowancePath:      filepath.Join(pilotDir, "submission.claim"),
	}
	validPolPath := filepath.Join(pilotDir, "valid_pol.json")
	polRaw, _ := json.Marshal(validPolicy)
	require.NoError(t, os.WriteFile(validPolPath, polRaw, 0600))
	_, expectedHash, err := readBigQueryPilotPolicy(validPolPath)
	require.NoError(t, err)

	// Line 147: pilotClaimSync error
	oldClaimSync := pilotClaimSync
	t.Cleanup(func() { pilotClaimSync = oldClaimSync })
	pilotClaimSync = func(f *os.File) error { return errors.New("claim sync err") }
	err = claimBigQueryPilot(validPolPath, validPolicy, expectedHash)
	require.ErrorIs(t, err, bigqueryread.ErrInput)
	pilotClaimSync = oldClaimSync
	_ = os.Remove(validPolicy.AllowancePath)

	// Line 151: allowanceDirOpener error
	oldDirOpener := allowanceDirOpener
	t.Cleanup(func() { allowanceDirOpener = oldDirOpener })
	allowanceDirOpener = func(string) (*os.File, error) { return nil, errors.New("open dir err") }
	err = claimBigQueryPilot(validPolPath, validPolicy, expectedHash)
	require.ErrorIs(t, err, bigqueryread.ErrInput)
	allowanceDirOpener = oldDirOpener
	_ = os.Remove(validPolicy.AllowancePath)

	// Line 156: pilotDirSync error
	oldDirSync := pilotDirSync
	t.Cleanup(func() { pilotDirSync = oldDirSync })
	pilotDirSync = func(f *os.File) error { return errors.New("dir sync err") }
	err = claimBigQueryPilot(validPolPath, validPolicy, expectedHash)
	require.ErrorIs(t, err, bigqueryread.ErrInput)
	pilotDirSync = oldDirSync
	_ = os.Remove(validPolicy.AllowancePath)

	// Line 160: second read validation error (delete validPolPath before second read)
	savedDirOpener := allowanceDirOpener
	allowanceDirOpener = func(d string) (*os.File, error) {
		_ = os.Remove(validPolPath)
		return savedDirOpener(d)
	}
	err = claimBigQueryPilot(validPolPath, validPolicy, expectedHash)
	require.ErrorIs(t, err, bigqueryread.ErrInput)
	allowanceDirOpener = savedDirOpener

	// Re-create validPolPath for source plan test
	require.NoError(t, os.WriteFile(validPolPath, polRaw, 0600))

	// validateBigQueryPilotSourcePlan plan limit / projection / order errors
	goodProfile := bigquery.SourceProfile{
		SourceID:           "bigquery-world-bank-wdi",
		SourceProject:      "bigquery-public-data",
		DatasetID:          "world_bank_wdi",
		TableID:            "country_summary",
		Location:           "US",
		DescriptorDigest:   "desc",
		PublisherReviewRef: "pub",
		RightsReviewRef:    "rights",
		Use:                "admitted",
	}
	badPlan := bigquery.ReadPlan{
		Limit:      5, // not 2
		Projection: []string{"country_code", "short_name"},
		Order:      []bigquery.Order{{Column: "country_code", Direction: "ASC"}, {Column: "short_name", Direction: "ASC"}},
	}
	err = validateBigQueryPilotSourcePlan(goodProfile, badPlan)
	require.Error(t, err)

	// 8. Harness command branches
	h := newCliBQHarness(t)
	// Receipt error branches (lines 355, 359)
	_, err = h.execute("page", "--receipt", filepath.Join(h.dir, "missing.json"))
	require.Error(t, err)

	emptyReceipt := h.save("empty_receipt.json", map[string]any{"receipt": map[string]any{"runId": ""}})
	_, err = h.execute("page", "--receipt", emptyReceipt)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing existing run receipt")

	// Source profile error branches (lines 372, 375)
	badProfilePath := filepath.Join(h.dir, "bad_prof.json")
	require.NoError(t, os.WriteFile(badProfilePath, []byte(`{bad`), 0600))
	_, err = h.execute("preview", "--source-profile", badProfilePath, "--execution-project", "p", "--maximum-bytes-billed", "10M")
	require.Error(t, err)

	goodProfPath := h.save("good_prof.json", bigquery.SourceProfile{SourceID: "s"})
	badQueryFile := filepath.Join(h.dir, "bad_q.json")
	require.NoError(t, os.WriteFile(badQueryFile, []byte(`{bad`), 0600))
	_, err = h.execute("preview", "--source-profile", goodProfPath, "--file", badQueryFile, "--execution-project", "p", "--maximum-bytes-billed", "10M")
	require.Error(t, err)

	// Execution project changed on page (lines 381-383)
	jobReceipt := h.save("job_receipt.json", bigQueryControl{
		Receipt: bigquery.Receipt{
			RunID: "run-1",
			Job:   &bigquery.JobRef{ProjectID: "orig-project"},
		},
	})
	_, err = h.execute("page", "--receipt", jobReceipt, "--execution-project", "diff-project")
	require.Error(t, err)
	require.Contains(t, err.Error(), "execution project changed; existing job cannot be rerouted")

	// Cursor override (line 387) and Resume run == nil (line 497)
	_, _ = h.execute("page", "--receipt", jobReceipt, "--cursor", "invalid-cursor-not-b64!!!")

	// Preview bad file & approve-digest mismatch on run (lines 393, 397)
	_, err = h.execute("run", "--preview", filepath.Join(h.dir, "no_preview.json"))
	require.Error(t, err)

	prevPath := h.save("prev.json", bigquery.Preview{ApprovalDigest: "correct-digest"})
	_, err = h.execute("run", "--preview", prevPath, "--approve-digest", "wrong-digest")
	require.Error(t, err)
	require.Contains(t, err.Error(), "--approve-digest must equal the displayed preview digest")

	// Run with changed project (line 469)
	prevWithProj := h.save("prev_proj.json", bigquery.Preview{
		ApprovalDigest: "correct-digest",
		Execution:      bigquery.Execution{JobProject: "proj-1"},
	})
	_, err = h.execute("run", "--preview", prevWithProj, "--approve-digest", "correct-digest", "--execution-project", "proj-2")
	require.Error(t, err)
	require.Contains(t, err.Error(), "execution project changed; preview again")

	// Status and Cancel with Job == nil (lines 502, 514)
	noJobReceipt := h.save("no_job_receipt.json", bigQueryControl{
		Receipt: bigquery.Receipt{RunID: "r1"},
	})
	_, err = h.execute("status", "--receipt", noJobReceipt)
	require.Error(t, err)
	require.Contains(t, err.Error(), "submission is unresolved; no verified job reference exists")

	_, err = h.execute("cancel", "--receipt", noJobReceipt, "--enable-cancellation")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no verified job reference to cancel")

	// Ledger and provider error branches in runBigQueryCommand (lines 417, 421, 425)
	bigQueryDeps.ledger = func(string) (bigquery.Ledger, error) { return nil, errors.New("ledger error") }
	_, err = h.execute("preview", "--execution-project", "p", "--maximum-bytes-billed", "10M")
	require.Error(t, err)

	bigQueryDeps.ledger = func(string) (bigquery.Ledger, error) { return bigquery.NewMemoryLedger(), nil }
	bigQueryDeps.provider = func(string, bool, string) (bigquery.Provider, error) { return nil, errors.New("provider error") }
	_, err = h.execute("preview", "--execution-project", "p", "--maximum-bytes-billed", "10M")
	require.Error(t, err)

	bigQueryDeps.provider = func(string, bool, string) (bigquery.Provider, error) { return h.provider, nil }
	bigQueryDeps.transport = nil
	_, err = h.execute("preview", "--execution-project", "p", "--maximum-bytes-billed", "10M")
	require.Error(t, err)
	bigQueryDeps.transport = cliBQTransport(h.transport)

	// Preview authorize error (line 453)
	bigQueryDeps.provider = func(string, bool, string) (bigquery.Provider, error) {
		return &mockFailingBQProvider{err: errors.New("auth fail")}, nil
	}
	_, err = h.execute("preview", "--execution-project", "p", "--maximum-bytes-billed", "10M")
	require.Error(t, err)
	bigQueryDeps.provider = func(string, bool, string) (bigquery.Provider, error) { return h.provider, nil }

	// Preview client.Preview error on invalid project name (line 462)
	_, err = h.execute("preview", "--execution-project", "invalid project name with spaces!", "--maximum-bytes-billed", "10M")
	require.Error(t, err)

	// Pilot plan and claim verification (lines 407, 410)
	hPilot := newCliBQPilotHarness(t)
	// Line 407: pilot with non-pilot source profile in hPilot
	badProfileInput := hPilot.save("bad_profile_input.json", bigqueryread.Input{
		Profile: bigquery.SourceProfile{Version: 1, SourceID: "wrong-source-id", DescriptorDigest: "synthetic-descriptor", LogicalCollection: "country_summary", SourceProject: "bigquery-public-data", DatasetID: "world_bank_wdi", TableID: "country_summary", Location: "US", Schema: []bigquery.Field{{Name: "country_code", Type: "STRING", Mode: "NULLABLE"}, {Name: "short_name", Type: "STRING", Mode: "NULLABLE"}}, PublisherReviewRef: "synthetic-publisher", RightsReviewRef: "synthetic-rights", Use: "connection-test"},
		Query:   bigqueryread.QueryShape{From: bigqueryread.From{Name: "country_summary"}, Columns: []bigqueryread.Column{{Field: "country_code"}, {Field: "short_name"}}, OrderBy: []bigqueryread.Order{{Field: "country_code"}, {Field: "short_name"}}, Limit: 2},
	})
	_, err = hPilot.execute("preview", "--file", badProfileInput, "--execution-project", "demodb-dev", "--maximum-bytes-billed", pilotCap)
	require.Error(t, err)

	badClaimReceipt := hPilot.save("bad_claim_receipt.json", bigQueryControl{
		Receipt: bigquery.Receipt{RunID: "run-1", Job: &bigquery.JobRef{ProjectID: "demodb-dev"}},
	})
	_, err = hPilot.execute("status", "--receipt", badClaimReceipt, "--operator-pilot-policy", "/non/existent/policy.json")
	require.Error(t, err)

	// Pilot run policy validation and claim errors (lines 477, 486)
	pilotPrev, pilotPrevPath, pilotPolPath := hPilot.pilotPreview()
	// Line 477: policy validation error on pilot run
	_, err = hPilot.execute("run", "--preview", pilotPrevPath, "--approve-digest", pilotPrev.ApprovalDigest, "--operator-pilot-policy", "/non/existent/policy.json")
	require.Error(t, err)

	// Line 486: claim error on pilot run when claim file already exists
	claimFile := filepath.Join(hPilot.dir, "submission.claim")
	require.NoError(t, os.WriteFile(claimFile, []byte("existing"), 0600))
	_, err = hPilot.execute("run", "--preview", pilotPrevPath, "--approve-digest", pilotPrev.ApprovalDigest, "--operator-pilot-policy", pilotPolPath)
	require.Error(t, err)
	_ = os.Remove(claimFile)

	// Line 538: bqNextPage returns dal.ErrNoMoreRecords
	oldNextPage := bqNextPage
	t.Cleanup(func() { bqNextPage = oldNextPage })
	bqNextPage = func(r *bigquery.Run) (bigquery.Page, error) {
		return bigquery.Page{}, dal.ErrNoMoreRecords
	}
	hFresh := newCliBQHarness(t)
	pRaw, err := hFresh.execute("preview", "--execution-project", "job-project", "--maximum-bytes-billed", "1000", "--page-size", "1")
	require.NoError(t, err)
	var prev bigquery.Preview
	require.NoError(t, json.Unmarshal(pRaw, &prev))
	prevFile := hFresh.save("p_nomore.json", prev)
	_, err = hFresh.execute("run", "--preview", prevFile, "--approve-digest", prev.ApprovalDigest)
	require.Error(t, err)
	bqNextPage = oldNextPage
}
