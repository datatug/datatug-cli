package detective

import (
	"encoding/json"
	"errors"
	"os"
)

// runCohortProof is private metadata from the accepted server-side Case
// context. Run history omits input values, so a run ID alone cannot authorize
// later Case evidence or manual Scenario completion.
type runCohortProof struct {
	RunID         string `json:"runId"`
	CaseID        string `json:"caseId"`
	ContextDigest string `json:"contextDigest"`
}

func (s *LocalStore) saveRunCohortProof(runID, caseID, digest string) error {
	if !validSlug(runID) || !validSlug(caseID) || len(digest) != 64 {
		return ErrInvalid
	}
	proof := runCohortProof{RunID: runID, CaseID: caseID, ContextDigest: digest}
	data, err := safeReadFile(s.privateItemDir("run-cohorts"), runID)
	if err == nil {
		var old runCohortProof
		if json.Unmarshal(data, &old) != nil || old != proof {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeItem(s.privateItemDir("run-cohorts"), runID, proof)
}

func (s *LocalStore) authorizeCaseRunLink(c Case, run CheckRun) error {
	if run.CaseID == "" {
		if len(c.Context) == 0 {
			return nil
		}
		return ErrInvalid
	}
	if run.CaseID != c.ID {
		return ErrInvalid
	}
	data, err := safeReadFile(s.privateItemDir("run-cohorts"), run.ID)
	if errors.Is(err, os.ErrNotExist) {
		return ErrInvalid
	}
	if err != nil {
		return err
	}
	var proof runCohortProof
	if json.Unmarshal(data, &proof) != nil || proof.RunID != run.ID || proof.CaseID != c.ID || len(proof.ContextDigest) != 64 {
		return ErrInvalid
	}
	digest, err := caseContextDigest(c.Context)
	if err != nil {
		return err
	}
	if digest != proof.ContextDigest {
		return ErrInvalid
	}
	return nil
}
