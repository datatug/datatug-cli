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
	"strings"
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
	IDTooLong    int64    `json:"id_too_long,omitempty"`
	Masked       []string `json:"masked,omitempty"`
}

// resumeState is the content of resume.json.
type resumeState struct {
	Format string `json:"format"`
	// Digests of what the run read, by: the source and selection, the
	// definitions' files, and the policy. A run resumes only when all match.
	Selection   string `json:"selection"`
	Definitions string `json:"definitions"`
	Policy      string `json:"policy"`
	// Files and Folders are the case-folded record files and folders written
	// so far, so a resumed run still finds names that differ only in case.
	Files map[string]string `json:"files,omitempty"`
	// Chunks counts the partial saves of collections kept in one file.
	Chunks   int                     `json:"chunks,omitempty"`
	Folders  map[string]string       `json:"folders,omitempty"`
	ReadTime string                  `json:"read_time,omitempty"`
	Reads    int64                   `json:"reads"`
	Items    map[string]*cursor      `json:"items"`
	Counts   map[string]*priorCounts `json:"counts,omitempty"`
	Rejected []Rejection             `json:"rejected,omitempty"`
	Outside  map[string]int64        `json:"outside,omitempty"`

	merged bool // Counts, Rejected, Outside and Reads include this run
}

// item is the cursor of a read of the plan.
func (s *resumeState) item(label string) *cursor {
	if s.Items[label] == nil {
		s.Items[label] = &cursor{}
	}
	return s.Items[label]
}

func digest(parts ...any) string {
	content, _ := json.Marshal(parts) // strings, lists and maps of strings
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// encoding is the name of the key encoding the output's state records (the
// one this version writes for a new output).
func (r *run) encoding() string {
	if r.encodingName != "" {
		return r.encodingName
	}
	return keyEncoding.Name()
}

func (r *run) newState() *resumeState {
	collections := slices.Sorted(slices.Values(r.opts.Collections))
	return &resumeState{
		Format:      resumeFormat,
		Selection:   digest(r.opts.Identity, r.opts.Under, collections, r.encoding()),
		Definitions: r.defsHash,
		Policy:      digest(r.policy.Copy, r.policy.Exclude, r.policy.Mask, r.policy.Paths),
		Items:       map[string]*cursor{}, Counts: map[string]*priorCounts{}, Outside: map[string]int64{},
	}
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
	state, want := &resumeState{}, r.newState()
	if json.Unmarshal(content, state) != nil || state.Format != resumeFormat || state.Items == nil {
		_, _ = fmt.Fprintln(r.opts.Log, "db copy: the resume state of an earlier run cannot be read: starting over")
		return nil
	}
	var changed []string
	for name, same := range map[string]bool{
		"the selection (--include, --space, --path)": state.Selection == want.Selection,
		"the definitions": state.Definitions == want.Definitions,
		"the policy":      state.Policy == want.Policy,
	} {
		if !same {
			changed = append(changed, name)
		}
	}
	if len(changed) > 0 {
		slices.Sort(changed)
		_, _ = fmt.Fprintf(r.opts.Log, "db copy: %s changed since the run that stopped at its read budget: starting over, not resuming\n", strings.Join(changed, " and "))
		return nil
	}
	for lower, file := range state.Files {
		r.files[lower] = file
	}
	for lower, folder := range state.Folders {
		r.folders[lower] = folder
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
	_, _ = fmt.Fprintln(r.opts.Log, "db copy: resuming an earlier run that stopped at its read budget (--restart starts over)")
	return state
}

// snapshot is the state to save: the earlier runs' counts plus this run's.
func (r *run) snapshot() *resumeState {
	s := *r.state
	s.Files, s.Folders = r.files, r.folders
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
		counts := &priorCounts{Records: prior.Records + result.Records, Rejected: prior.Rejected + result.Rejected, OtherParents: prior.OtherParents + result.OtherParents, IDTooLong: prior.IDTooLong + result.IDTooLong}
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
			result.Records, result.Rejected, result.OtherParents, result.IDTooLong = counts.Records, counts.Rejected, counts.OtherParents, counts.IDTooLong
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
	if err := r.savePartials(); err != nil {
		return err
	}
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
