package dbmirror

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// A run that stops at its read budget leaves .datatug-mirror/resume.json: for
// each read of its plan, the last document read or that it was done, and the
// counts so far. The next run with the same source, policy and selection
// continues from there instead of reading the same first documents again (and
// paying for them); Options.Restart starts over. A run that finishes removes
// the file.

const resumeFormat = "datatug-mirror-resume/v1"

func resumeFile(out string) string { return filepath.Join(out, stateDirName, "resume.json") }

type cursor struct {
	After string `json:"after,omitempty"`
	Done  bool   `json:"done,omitempty"`
}

type priorCounts struct {
	Records      int64    `json:"records,omitempty"`
	Rejected     int64    `json:"rejected,omitempty"`
	OtherParents int64    `json:"other_parents,omitempty"`
	Masked       []string `json:"masked,omitempty"`
}

// resumeState is the content of resume.json.
type resumeState struct {
	Format      string                  `json:"format"`
	Fingerprint string                  `json:"fingerprint"`
	ReadTime    string                  `json:"read_time,omitempty"`
	Reads       int64                   `json:"reads"`
	Items       map[string]*cursor      `json:"items"`
	Counts      map[string]*priorCounts `json:"counts,omitempty"`
	Rejected    []Rejection             `json:"rejected,omitempty"`
	Outside     map[string]int64        `json:"outside,omitempty"`

	merged bool // Counts, Rejected, Outside and Reads include this run
}

// item is the cursor of a read of the plan.
func (s *resumeState) item(label string) *cursor {
	if s.Items[label] == nil {
		s.Items[label] = &cursor{}
	}
	return s.Items[label]
}

// fingerprint identifies what a run reads, so a resume state is used only by
// a run that reads the same.
func (r *run) fingerprint() string {
	collections := slices.Clone(r.opts.Collections)
	slices.Sort(collections)
	var specs []string
	for _, spec := range r.defs.Specs {
		specs = append(specs, spec.ID+"|"+r.results[spec.ID].Action)
	}
	content, _ := json.Marshal([]any{r.opts.Source, r.opts.Under, collections, specs, r.policy.Copy, r.policy.Exclude, r.policy.Mask, r.policy.Paths})
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func (r *run) newState() *resumeState {
	return &resumeState{Format: resumeFormat, Fingerprint: r.fingerprint(), Items: map[string]*cursor{}, Counts: map[string]*priorCounts{}, Outside: map[string]int64{}}
}

// loadState is the resume state of an earlier run that reads what this one
// reads, or nil.
func (r *run) loadState(plan []*planItem) *resumeState {
	if r.opts.Restart {
		return nil
	}
	content, err := os.ReadFile(resumeFile(r.opts.Output))
	if err != nil {
		return nil
	}
	state := &resumeState{}
	if json.Unmarshal(content, state) != nil || state.Format != resumeFormat || state.Fingerprint != r.fingerprint() || state.Items == nil {
		_, _ = fmt.Fprintln(r.opts.Log, "db mirror: the resume state of an earlier run does not match this run: starting over")
		return nil
	}
	if state.Counts == nil {
		state.Counts = map[string]*priorCounts{}
	}
	if state.Outside == nil {
		state.Outside = map[string]int64{}
	}
	r.report.Resumed = true
	for _, item := range plan {
		if c := state.Items[item.label]; c != nil && (c.After != "" || c.Done) {
			for _, spec := range item.specs {
				r.resumed[spec.ID] = true
			}
		}
	}
	_, _ = fmt.Fprintln(r.opts.Log, "db mirror: resuming an earlier run that stopped at its read budget (--restart starts over)")
	return state
}

// snapshot is the state to save: the earlier runs' counts plus this run's.
func (r *run) snapshot() *resumeState {
	s := *r.state
	if s.merged {
		return &s
	}
	s.Reads += r.report.Reads
	s.Rejected = append(slices.Clone(s.Rejected), r.rejected...)
	s.Outside = map[string]int64{}
	for collection, n := range r.state.Outside {
		s.Outside[collection] += n
	}
	for collection, n := range r.outside {
		s.Outside[collection] += n
	}
	s.Counts = map[string]*priorCounts{}
	for _, spec := range r.defs.Specs {
		prior := r.state.Counts[spec.ID]
		if prior == nil {
			prior = &priorCounts{}
		}
		result := r.results[spec.ID]
		counts := &priorCounts{Records: prior.Records + result.Records, Rejected: prior.Rejected + result.Rejected, OtherParents: prior.OtherParents + result.OtherParents}
		counts.Masked = slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(prior.Masked), sortedKeys(r.masked[spec.ID])...))))
		s.Counts[spec.ID] = counts
	}
	return &s
}

// merge adds the earlier runs' counts into this run's results and report.
func (r *run) merge() {
	merged := r.snapshot()
	merged.merged = true
	r.state = merged
	r.report.Records = 0
	for _, spec := range r.defs.Specs {
		result := r.results[spec.ID]
		if counts := merged.Counts[spec.ID]; counts != nil {
			result.Records, result.Rejected, result.OtherParents = counts.Records, counts.Rejected, counts.OtherParents
			for _, field := range counts.Masked {
				if r.masked[spec.ID] == nil {
					r.masked[spec.ID] = map[string]bool{}
				}
				r.masked[spec.ID][field] = true
			}
		}
		r.report.Records += result.Records
	}
	if r.report.Resumed {
		r.report.ReadsTotal = merged.Reads
	}
}

// saveState writes the resume state (a dry run has none and never saves it).
func (r *run) saveState() error {
	content, _ := json.MarshalIndent(r.snapshot(), "", "  ") // only strings, numbers and bools
	_, err := writeFileIfChanged(resumeFile(r.opts.Output), append(content, '\n'))
	return err
}

// endState keeps the resume state of a run stopped at its budget and removes
// that of a run that read everything.
func (r *run) endState() error {
	if r.stopped {
		return r.saveState()
	}
	if err := removeFile(resumeFile(r.opts.Output)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
