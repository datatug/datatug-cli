package commands

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	bigquery "github.com/dal-go/dalgo2bigquery"
	"github.com/datatug/datatug-cli/pkg/bigqueryread"
	"github.com/spf13/cobra"
)

const pilotCap = "10485760"

// This is an operator-authored, private acceptance record. The CLI checks its
// exact binding; creating this file does not itself constitute a rights review.
type bigQueryPilotPolicy struct {
	Format             string `json:"format"`
	ApprovalDigest     string `json:"approvalDigest"`
	RightsReviewRef    string `json:"rightsReviewRef"`
	ExecutionProject   string `json:"executionProject"`
	MaximumBytesBilled string `json:"maximumBytesBilled"`
	SessionBudgetBytes string `json:"sessionBudgetBytes"`
	AllowancePath      string `json:"allowancePath"`
}

func privatePilotFile(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, bigqueryread.ErrInput
	}
	dir := filepath.Dir(path)
	ds, err := os.Lstat(dir)
	if err != nil || !ds.IsDir() || (runtime.GOOS != "windows" && ds.Mode().Perm()&0077 != 0) {
		return nil, bigqueryread.ErrInput
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() > 16<<10 || (runtime.GOOS != "windows" && before.Mode().Perm()&0077 != 0) {
		return nil, bigqueryread.ErrInput
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, bigqueryread.ErrInput
	}
	defer func() { _ = f.Close() }()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, bigqueryread.ErrInput
	}
	raw, err := io.ReadAll(io.LimitReader(f, 16<<10+1))
	if err != nil || len(raw) > 16<<10 {
		return nil, bigqueryread.ErrInput
	}
	return raw, nil
}

func readBigQueryPilotPolicy(path string) (bigQueryPilotPolicy, string, error) {
	raw, err := privatePilotFile(path)
	if err != nil {
		return bigQueryPilotPolicy{}, "", err
	}
	if _, err := bigquery.ParseJSON(raw, 16<<10); err != nil {
		return bigQueryPilotPolicy{}, "", bigqueryread.ErrInput
	}
	var policy bigQueryPilotPolicy
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if err := d.Decode(&policy); err != nil {
		return bigQueryPilotPolicy{}, "", bigqueryread.ErrInput
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return bigQueryPilotPolicy{}, "", bigqueryread.ErrInput
	}
	if policy.Format != "datatug-bigquery-operator-pilot/1" || policy.ApprovalDigest == "" || policy.RightsReviewRef == "" || policy.ExecutionProject != "demodb-dev" || policy.MaximumBytesBilled != pilotCap || policy.SessionBudgetBytes != pilotCap || !filepath.IsAbs(policy.AllowancePath) || filepath.Clean(filepath.Dir(policy.AllowancePath)) != filepath.Clean(filepath.Dir(path)) || filepath.Base(policy.AllowancePath) != "submission.claim" {
		return bigQueryPilotPolicy{}, "", bigqueryread.ErrInput
	}
	hash := sha256.Sum256(raw)
	return policy, hex.EncodeToString(hash[:]), nil
}

func validateBigQueryPilotSourcePlan(profile bigquery.SourceProfile, plan bigquery.ReadPlan) error {
	if profile.SourceID != "bigquery-world-bank-wdi" || profile.SourceProject != "bigquery-public-data" || profile.DatasetID != "world_bank_wdi" || profile.TableID != "country_summary" || profile.Location != "US" || profile.DescriptorDigest == "" || profile.PublisherReviewRef == "" || profile.RightsReviewRef == "" || (profile.Use != "connection-test" && profile.Use != "admitted") {
		return bigqueryread.ErrInput
	}
	if plan.Limit != 2 || plan.Where != nil || len(plan.Parameters) != 0 || len(plan.Projection) != 2 || plan.Projection[0] != "country_code" || plan.Projection[1] != "short_name" || len(plan.Order) != 2 || plan.Order[0] != (bigquery.Order{Column: "country_code", Direction: "ASC"}) || plan.Order[1] != (bigquery.Order{Column: "short_name", Direction: "ASC"}) {
		return bigqueryread.ErrInput
	}
	return nil
}

func validateBigQueryPilot(profile bigquery.SourceProfile, plan bigquery.ReadPlan, preview bigquery.Preview, policy bigQueryPilotPolicy) error {
	if validateBigQueryPilotSourcePlan(profile, plan) != nil || profile.RightsReviewRef != policy.RightsReviewRef {
		return bigqueryread.ErrInput
	}
	if plan.Digest != preview.Plan.Digest || preview.ApprovalDigest != policy.ApprovalDigest || preview.Execution.JobProject != policy.ExecutionProject || preview.Execution.MaximumBytesBilled != policy.MaximumBytesBilled || preview.Execution.SessionBudgetBytes != policy.SessionBudgetBytes || preview.Plan.Limit != 2 || preview.Bounds.PageSize != 1 || preview.Bounds.MaxRows != 2 || preview.Bounds.MaxPages != 1 || preview.Observation.Location != "US" || preview.Observation.Type != "TABLE" || preview.Observation.Table != (bigquery.TableRef{ProjectID: "bigquery-public-data", DatasetID: "world_bank_wdi", TableID: "country_summary"}) {
		return bigqueryread.ErrInput
	}
	return nil
}

func verifyBigQueryPilotClaim(path string, receipt bigquery.Receipt) error {
	policy, hash, err := readBigQueryPilotPolicy(path)
	if err != nil || receipt.ApprovalDigest != policy.ApprovalDigest {
		return bigqueryread.ErrInput
	}
	raw, err := privatePilotFile(policy.AllowancePath)
	if err != nil || string(raw) != "datatug-bigquery-operator-pilot-claim/1\npolicySHA256="+hash+"\n" {
		return bigqueryread.ErrInput
	}
	return nil
}

// O_EXCL is the cross-process claim. A failed or ambiguous submit never refunds
// it. Sync both file and directory before Execute can dispatch its POST.
func claimBigQueryPilot(path string, policy bigQueryPilotPolicy, expectedHash string) error {
	_, actualHash, err := readBigQueryPilotPolicy(path)
	if err != nil || actualHash != expectedHash {
		return bigqueryread.ErrInput
	}
	f, err := os.OpenFile(policy.AllowancePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return bigqueryread.ErrInput
	}
	_, writeErr := fmt.Fprintf(f, "datatug-bigquery-operator-pilot-claim/1\npolicySHA256=%s\n", expectedHash)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return bigqueryread.ErrInput
	}
	dir, err := os.Open(filepath.Dir(policy.AllowancePath))
	if err != nil {
		return bigqueryread.ErrInput
	}
	syncErr := dir.Sync()
	closeErr = dir.Close()
	if syncErr != nil || closeErr != nil {
		return bigqueryread.ErrInput
	}
	_, actualHash, err = readBigQueryPilotPolicy(path)
	if err != nil || actualHash != expectedHash {
		return bigqueryread.ErrInput
	}
	return nil
}

func writeBigQueryPilotResult(cmd *cobra.Command, value any, previewOut, receiptOut, dir string) error {
	switch v := value.(type) {
	case bigquery.Page:
		return writeBigQueryResult(cmd, bigquery.Page{Receipt: v.Receipt}, previewOut, receiptOut, dir)
	case bigQueryControl:
		return writeBigQueryResult(cmd, bigquery.Page{Receipt: v.Receipt}, previewOut, receiptOut, dir)
	case bigquery.Preview:
		return writeBigQueryResult(cmd, v, previewOut, receiptOut, dir)
	default:
		return errors.New("unsupported pilot output")
	}
}

func writeBigQueryPilotRun(run *bigquery.Run, operationError error, write func(any) error) error {
	if run == nil {
		return bigQueryFailure(operationError)
	}
	// Execute may already have parsed the jobs.query response. Do not deliver its
	// rows, mint a paging cursor, or perform a result-page GET in this pilot.
	closeErr := run.Close()
	return errors.Join(bigQueryFailure(operationError), bigQueryFailure(closeErr), write(bigquery.Page{Receipt: run.Receipt()}))
}
