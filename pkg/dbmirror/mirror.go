package dbmirror

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ingitdb/ingitdb-go/ingitdb"
	"github.com/ingitdb/ingitdb-go/ingitdb/datavalidator"
)

// What the policy says about a defined collection.
const (
	ActionCopy        = "copy"        // copied as read
	ActionMask        = "mask"        // copied with the policy's fields masked
	ActionExclude     = "exclude"     // never copied: the policy excludes it
	ActionUndeclared  = "undeclared"  // never copied: the policy does not name it
	ActionUnsupported = "unsupported" // the policy copies it, but its record layout cannot be written
)

// ReportFormat is the format of the report a run writes.
const ReportFormat = "datatug-mirror-report/v1"

// DefaultPageSize is how many documents one read request asks for.
const DefaultPageSize = 300

// maxOtherParentPaths is how many distinct other-parent paths a collection lists.
const maxOtherParentPaths = 50

// ErrUsage marks an error in what the caller asked for (exit code 2).
var ErrUsage = errors.New("usage")

// ErrIDTooLong is the failure of a run that read a document of a collection
// kept one file per record whose file name (its encoded id) or path is too
// long to write. The run stops there: the report says so, and a resume
// starts again at that document.
var ErrIDTooLong = errors.New("id too long for one file per record")

// Options of a run.
type Options struct {
	// Output is the folder the inGitDB database is written to. Not used by a dry run.
	Output string
	// Collections, when not empty, copies only these defined collections
	// (a collection id path, or "<path>/**" for it and every collection below it).
	Collections []string
	// Under, when set, copies only the documents at or below this path: a
	// document path (spaces/abc) or a collection path (spaces/abc/ext).
	Under string
	// DryRun counts the documents of each collection and writes nothing.
	DryRun bool
	// MaxReads stops the run before more than this many Firestore reads (0: no limit).
	MaxReads int64
	// PageSize is how many documents one request reads (default DefaultPageSize).
	PageSize int
	// KeepRejected also writes each rejected document under
	// .datatug-mirror/rejected/, as read with only the policy's masks applied:
	// its undeclared fields and the values that break its definition are
	// written in clear. By default only its path, reasons and declared field
	// names are kept, in the report.
	KeepRejected bool
	// ContinueOnLongIDs copies the rest when a document's id is too long for
	// one file per record, and reports it (the run is then incomplete),
	// instead of failing at the first one.
	ContinueOnLongIDs bool
	// Prune deletes the record files whose document no longer exists, in the
	// collections this run read completely. By default they are only reported.
	Prune bool
	// Restart ignores the resume state an earlier run left (see Resume).
	Restart bool
	// Snapshot reads every document as it was when the run (or the run it
	// resumes, within the hour) started, when the source can.
	Snapshot bool
	// Source is how the source is shown in the report (never a credential).
	Source string
	// Identity is the database read. The output records the identity of the
	// source it was created from, and every later run into it, its resume
	// and its prune, must read the same one.
	Identity SourceIdentity
	// Log receives one line per collection and the summary (nil: discarded).
	Log io.Writer
	// Progress, when set, receives one line per page read.
	Progress io.Writer
}

// CollectionResult is what happened to one defined collection.
type CollectionResult struct {
	Collection string `json:"collection"`
	Action     string `json:"action"`
	Selected   bool   `json:"selected"`
	Note       string `json:"note,omitempty"`
	Query      string `json:"query,omitempty"`
	Count      *int64 `json:"count,omitempty"`
	// CountAtLeast says Count stopped at the read budget: there are at least that many.
	CountAtLeast bool     `json:"count_at_least,omitempty"`
	Records      int64    `json:"records"`
	Rejected     int64    `json:"rejected,omitempty"`
	Masked       []string `json:"masked_fields,omitempty"`
	// BelowMissingParent counts records whose parent document does not exist
	// in the source (Firestore keeps such sub-collections).
	BelowMissingParent int64 `json:"below_missing_parent,omitempty"`
	// OtherParents counts documents of the collection that were read but not
	// copied because their path is not one the policy's paths allow, and
	// OtherParentPaths shows those paths with every unpinned id as "*".
	OtherParents     int64    `json:"other_parents,omitempty"`
	OtherParentPaths []string `json:"other_parent_paths,omitempty"`
	// IDTooLong counts documents not copied because their record file name
	// (the encoded id) is longer than 255 bytes, or their path longer than the
	// system writes: file per record does not hold them.
	IDTooLong int64 `json:"not_copied_id_too_long,omitempty"`
	// Resumed says part of the collection was read by an earlier run.
	Resumed  bool `json:"resumed,omitempty"`
	Complete bool `json:"complete"`

	inSelection bool // asked for by Collections and Under, whatever the policy says
}

// Rejection is a document that was read but not written into the database:
// it does not fit its definition, or its file cannot be written. The report
// keeps its path, the reasons and the names of the declared fields concerned,
// never a value. A field name the definition does not declare, and a map key,
// may be data (an email used as a key): they are only counted, with a
// fingerprint that tells equal sets of names apart within one run.
type Rejection struct {
	Path       string   `json:"path"`
	Collection string   `json:"collection"`
	Reasons    []string `json:"reasons"`
	Fields     []string `json:"fields,omitempty"`
}

// OutsideRead counts documents a collection-group read returned that belong
// to no selected collection: their collection path is shown, so a collection
// that is missing from the definitions becomes visible.
type OutsideRead struct {
	Collection string `json:"collection"`
	Defined    bool   `json:"defined"`
	Documents  int64  `json:"documents"`
}

// Report is the result of a run. Without Snapshot and resume it holds no
// time, host or credential, so the same source and options give the same report.
type Report struct {
	Format         string          `json:"format"`
	Source         string          `json:"source"`
	SourceIdentity *SourceIdentity `json:"source_identity,omitempty"`
	Under          string          `json:"under,omitempty"`
	DryRun         bool            `json:"dry_run,omitempty"`
	Complete       bool            `json:"complete"`
	BudgetReached  bool            `json:"budget_reached,omitempty"`
	// Resumed says the run continued one that stopped at its read budget.
	Resumed    bool     `json:"resumed,omitempty"`
	ReadTime   string   `json:"read_time,omitempty"`
	Incomplete []string `json:"incomplete,omitempty"`
	// Reads are this run's billed reads; ReadsTotal adds those of the runs it resumed.
	Reads                    int64              `json:"reads"`
	ReadsTotal               int64              `json:"reads_total,omitempty"`
	MaxReads                 int64              `json:"max_reads,omitempty"`
	Records                  int64              `json:"records"`
	Collections              []CollectionResult `json:"collections"`
	Rejected                 []Rejection        `json:"rejected,omitempty"`
	UndefinedRootCollections []string           `json:"undefined_root_collections,omitempty"`
	Outside                  []OutsideRead      `json:"read_outside_selection,omitempty"`
	Stale                    []string           `json:"stale_record_files,omitempty"`
	Pruned                   []string           `json:"pruned_record_files,omitempty"`
	Validation               []string           `json:"validation_errors,omitempty"`

	// Written and Unchanged count record files of this run (not in the
	// report file: they differ between a first run and a re-run).
	Written   int64 `json:"-"`
	Unchanged int64 `json:"-"`
}

type planItem struct {
	query Query
	get   string // a single document to read instead of a query
	specs []*Spec
	label string
	done  bool
}

type run struct {
	ctx      context.Context
	src      Source
	defs     *Definitions
	policy   *Policy
	opts     Options
	report   *Report
	results  map[string]*CollectionResult
	under    []string
	patterns map[string][][]string // spec id -> allowed paths, 2*depth segments, "*" for any id
	seen     map[string]bool       // every document path read and matched to a collection
	copied   map[string]bool       // document paths written into the database
	files    map[string]string     // lower-cased record file -> document path, for collisions
	multi    map[string]*multiFile // record files kept in one file, being read
	folders  map[string]string     // lower-cased folder of a record -> the folder as written
	salt     []byte                // of the fingerprints of undeclared names, new each run
	defsHash string                // digest of the definition files
	masked   map[string]map[string]bool
	others   map[string]map[string]bool // spec id -> other-parent paths
	outside  map[string]int64
	stopped  bool
	complete map[string]bool // collection ids read to the end
	resumed  map[string]bool // collection ids partly read by an earlier run
	rejected []Rejection     // this run's rejections
	planned  []*planItem
	state    *resumeState
}

// Run copies what the policy and options select from src into opts.Output
// (or, for a dry run, only counts it) and returns the report.
//
// The returned error is a failure to read or write (or ErrUsage for a bad
// option). A run that stops at MaxReads, or that read documents which do not
// fit their definition, returns a report with Complete false and no error.
func Run(ctx context.Context, src Source, defs *Definitions, policy *Policy, opts Options) (*Report, error) {
	if err := policy.Check(defs); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUsage, err)
	}
	if opts.PageSize <= 0 {
		opts.PageSize = DefaultPageSize
	}
	if opts.Log == nil {
		opts.Log = io.Discard
	}
	r := &run{
		ctx: ctx, src: src, defs: defs, policy: policy, opts: opts,
		report:   &Report{Format: ReportFormat, Source: opts.Source, Under: opts.Under, DryRun: opts.DryRun, MaxReads: opts.MaxReads},
		results:  map[string]*CollectionResult{},
		patterns: map[string][][]string{},
		seen:     map[string]bool{},
		copied:   map[string]bool{},
		files:    map[string]string{},
		folders:  map[string]string{},
		multi:    map[string]*multiFile{},
		salt:     make([]byte, 16),
		masked:   map[string]map[string]bool{},
		others:   map[string]map[string]bool{},
		outside:  map[string]int64{},
		complete: map[string]bool{},
		resumed:  map[string]bool{},
	}
	_, _ = rand.Read(r.salt) // never fails
	if !opts.DryRun {
		r.report.SourceIdentity = &opts.Identity
	}
	if opts.Under != "" {
		r.under = strings.Split(opts.Under, "/")
		if slices.Contains(r.under, "") {
			return nil, fmt.Errorf("%w: %q is not a Firestore path", ErrUsage, opts.Under)
		}
	}
	for _, spec := range defs.Specs {
		r.patterns[spec.ID] = specPatterns(spec, policy.Paths[spec.ID])
	}
	if err := r.selectCollections(); err != nil {
		return nil, err
	}
	plan := r.plan()
	r.planned = plan
	if problems := r.knownIDProblems(); len(problems) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrIDTooLong, strings.Join(problems, "; "))
	}
	if !opts.DryRun {
		if err := r.prepareOutput(plan); err != nil {
			return nil, err
		}
	}
	r.startSnapshot()
	if err := r.listRoots(); err != nil {
		return nil, err
	}
	var failed error
	for _, item := range plan {
		if err := r.execute(item); errors.Is(err, ErrIDTooLong) && !opts.DryRun {
			// Keep the report and the resume state true to what was written: the
			// run stopped at this document, and a resume starts at its page.
			failed = err
			r.stopped = true
			r.report.Incomplete = append(r.report.Incomplete, "stopped: "+strings.TrimPrefix(err.Error(), ErrIDTooLong.Error()+": "))
			break
		} else if err != nil {
			return nil, err
		}
	}
	if !opts.DryRun {
		if err := r.finish(); err != nil {
			return nil, err
		}
		if err := r.writeReport(); err != nil {
			return nil, err
		}
	}
	r.summarize()
	return r.report, failed
}

// knownIDProblems are the ids a run knows before it reads (those of --space or
// --path, and those the policy's paths name) that are too long for the file
// or folder name one file per record gives them, in the collections the run
// reads. A dry run and a run fail on them before any read. The ids of the
// documents a query returns are known only once read.
func (r *run) knownIDProblems() []string {
	var problems []string
	for _, spec := range r.defs.Specs {
		if !r.covers(spec) || r.results[spec.ID].Action != ActionCopy && r.results[spec.ID].Action != ActionMask {
			continue
		}
		for _, pattern := range r.patterns[spec.ID] {
			bound := r.bind(pattern)
			if bound == nil {
				continue
			}
			for i := 1; i < len(bound); i += 2 {
				last := i == len(bound)-1
				if bound[i] == "*" || last && spec.multi {
					continue
				}
				name := keyEncoding.Encode(bound[i])
				if last {
					name = spec.prefix + name + spec.suffix
				}
				if tooLong := nameTooLong(name); tooLong != "" {
					problems = appendOnce(problems, fmt.Sprintf("collection %s: the id %s at level %d: %s", spec.ID, shown(bound[i]), i/2+1, tooLong))
				}
			}
		}
	}
	return problems
}

// specPatterns are the allowed paths of spec as 2*depth segments: the
// policy's patterns (a collection path gets "*" for the document id), or one
// that allows every path.
func specPatterns(spec *Spec, patterns []string) [][]string {
	if len(patterns) == 0 {
		patterns = []string{strings.Join(anyPath(spec), "/")}
	}
	var all [][]string
	for _, pattern := range patterns {
		segments := strings.Split(pattern, "/")
		if len(segments) == 2*spec.Depth()-1 {
			segments = append(segments, "*")
		}
		all = append(all, segments)
	}
	return all
}

func anyPath(spec *Spec) []string {
	var segments []string
	for _, id := range spec.Segments {
		segments = append(segments, id, "*")
	}
	return segments
}

// bind narrows a pattern to opts.Under: it returns the pattern with the ids
// Under fixes, or nil when no document of the pattern is at or below Under.
func (r *run) bind(pattern []string) []string {
	if len(r.under) > len(pattern) {
		return nil
	}
	bound := slices.Clone(pattern)
	for i, segment := range r.under {
		switch {
		case bound[i] == "*" && i%2 == 1:
			bound[i] = segment
		case bound[i] != segment:
			return nil
		}
	}
	return bound
}

// matchesPattern reports whether a document path (split on "/") is allowed by a pattern.
func matchesPattern(pattern, parts []string) bool {
	return slices.EqualFunc(pattern, parts, func(segment, part string) bool { return segment == "*" || segment == part })
}

// selectCollections fills one result per defined collection.
func (r *run) selectCollections() error {
	wanted := map[string]bool{}
	for _, name := range r.opts.Collections {
		base, tree := strings.CutSuffix(name, "/**")
		if r.defs.Spec(base) == nil {
			return fmt.Errorf("%w: collection %q is not defined in the mirror", ErrUsage, base)
		}
		for _, spec := range r.defs.Specs {
			if spec.ID == base || (tree && strings.HasPrefix(spec.ID, base+"/")) {
				wanted[spec.ID] = true
			}
		}
		if action := r.policy.action(base); !tree && action != ActionCopy && action != ActionMask {
			return fmt.Errorf("%w: collection %q is not copied: the policy has it as %s", ErrUsage, base, action)
		}
	}
	inScope := false
	for _, spec := range r.defs.Specs {
		result := &CollectionResult{Collection: spec.ID, Action: r.policy.action(spec.ID)}
		result.inSelection = (len(wanted) == 0 || wanted[spec.ID]) && r.covers(spec)
		r.results[spec.ID] = result
		r.report.Collections = append(r.report.Collections, CollectionResult{})
		switch {
		case result.Action == ActionExclude:
			result.Note = "excluded by the policy"
		case result.Action == ActionUndeclared:
			result.Note = "not declared in the policy: not copied"
		case spec.Unsupported != "":
			result.Action, result.Note = ActionUnsupported, spec.Unsupported
		case len(wanted) > 0 && !wanted[spec.ID]:
			result.Note = "not in the selected collections"
		case !result.inSelection:
			result.Note = "outside " + r.opts.Under
		default:
			result.Selected = true
		}
		inScope = inScope || result.inSelection
	}
	if !inScope {
		return fmt.Errorf("%w: no collection defined in the mirror (at a path its policy allows) is at or below %s", ErrUsage, r.opts.Under)
	}
	return nil
}

// covers reports whether documents of spec, at a path its patterns allow,
// can be at or below opts.Under.
func (r *run) covers(spec *Spec) bool {
	for _, pattern := range r.patterns[spec.ID] {
		if r.bind(pattern) != nil {
			return true
		}
	}
	return false
}

// inScope reports whether a document path is at or below opts.Under.
func (r *run) inScope(path string) bool {
	return r.opts.Under == "" || path == r.opts.Under || strings.HasPrefix(path, r.opts.Under+"/")
}

// plan is the reads that cover every selected collection, as narrow as the
// patterns bound to Under allow: one document read when the path is fixed, a
// query of one collection when its parents are fixed (nothing else is read),
// and otherwise a collection-group query per collection id (shared by the
// collections with that id), limited to the fixed path prefix. A group query
// also reads same-named collections elsewhere below that prefix: they are
// counted as outside reads or other parents, not copied.
func (r *run) plan() []*planItem {
	byLabel := map[string]*planItem{}
	var items []*planItem
	add := func(item *planItem, spec *Spec) {
		if existing := byLabel[item.label]; existing != nil {
			if !slices.Contains(existing.specs, spec) {
				existing.specs = append(existing.specs, spec)
			}
			return
		}
		item.specs = []*Spec{spec}
		byLabel[item.label] = item
		items = append(items, item)
	}
	for _, spec := range r.defs.Specs {
		if !r.results[spec.ID].Selected {
			continue
		}
		for _, pattern := range r.patterns[spec.ID] {
			bound := r.bind(pattern)
			if bound == nil {
				continue
			}
			fixed := 0 // length of the prefix with no "*"
			for fixed < len(bound) && bound[fixed] != "*" {
				fixed++
			}
			switch {
			case fixed == len(bound):
				path := strings.Join(bound, "/")
				add(&planItem{get: path, label: "document " + path}, spec)
			case fixed == len(bound)-1:
				path := strings.Join(bound[:fixed], "/")
				add(&planItem{query: Query{Collection: path}, label: "collection " + path}, spec)
			default:
				query := Query{Collection: spec.Leaf(), Group: true}
				label := "collection group " + spec.Leaf()
				if prefix := fixed / 2 * 2; prefix >= 2 {
					query.Under = strings.Join(bound[:prefix], "/")
					label += " under " + query.Under
				}
				add(&planItem{query: query, label: label}, spec)
			}
		}
	}
	for _, item := range items {
		for _, spec := range item.specs {
			result := r.results[spec.ID]
			result.Query = strings.TrimPrefix(result.Query+"; "+item.label, "; ")
		}
	}
	return items
}

// budget is how many documents the next request may read: limit, or less
// when MaxReads is near. 0 means the budget is spent.
func (r *run) budget(limit int) int {
	if r.opts.MaxReads <= 0 {
		return limit
	}
	left := r.opts.MaxReads - r.report.Reads
	if left <= 0 {
		return 0
	}
	return int(min(int64(limit), left))
}

func (r *run) stop() {
	if !r.stopped {
		r.stopped = true
		r.report.BudgetReached = true
		r.report.Incomplete = append(r.report.Incomplete, fmt.Sprintf("stopped at the read budget of %d reads", r.opts.MaxReads))
	}
}

// now is the clock of a snapshot (a test seam).
var now = time.Now

// snapshotWindow is how long Firestore keeps the versions a snapshot reads
// (without point-in-time recovery), less a margin.
const snapshotWindow = 55 * time.Minute

func (r *run) startSnapshot() {
	reader, ok := r.src.(snapshotSource)
	if !r.opts.Snapshot || !ok {
		return
	}
	at := now().UTC().Truncate(time.Microsecond) // Firestore reads at microsecond precision within the hour
	if r.state != nil && r.state.ReadTime != "" {
		if earlier, err := time.Parse(time.RFC3339Nano, r.state.ReadTime); err == nil && at.Sub(earlier) < snapshotWindow {
			at = earlier
		} else {
			r.report.Incomplete = append(r.report.Incomplete, "the resumed run was read at another time than the run it continues (its snapshot is older than an hour)")
		}
	}
	reader.ReadAt(at)
	r.report.ReadTime = at.Format(timeLayout)
	if r.state != nil {
		r.state.ReadTime = r.report.ReadTime
	}
}

// listRoots is the first read of a run: a budget allows at least one.
func (r *run) listRoots() error {
	roots, err := r.src.RootCollections(r.ctx)
	if err != nil {
		return fmt.Errorf("list root collections: %w", err)
	}
	r.report.Reads++
	for _, id := range roots {
		if r.defs.Spec(id) == nil {
			r.report.UndefinedRootCollections = append(r.report.UndefinedRootCollections, id)
		}
	}
	slices.Sort(r.report.UndefinedRootCollections)
	return nil
}

func (r *run) execute(item *planItem) error {
	if r.opts.DryRun {
		return r.count(item)
	}
	cursor := r.state.item(item.label)
	if cursor.Done {
		return r.markDone(item)
	}
	if item.get != "" {
		if r.budget(1) == 0 {
			r.stop()
			return nil
		}
		doc, err := r.src.Get(r.ctx, item.get)
		if err != nil {
			return fmt.Errorf("read %s: %w", item.get, err)
		}
		r.report.Reads++
		if doc != nil {
			if err := r.process(item, *doc); err != nil {
				return err
			}
		}
		return r.markDone(item)
	}
	query := item.query
	query.After = cursor.After
	var read int64
	for {
		query.Limit = r.budget(r.opts.PageSize)
		if query.Limit == 0 {
			r.stop()
			return nil
		}
		docs, err := r.src.Page(r.ctx, query)
		if err != nil {
			return fmt.Errorf("read %s: %w", item.label, err)
		}
		r.report.Reads += int64(max(len(docs), 1)) // a read that returns nothing is billed as one
		read += int64(len(docs))
		for _, doc := range docs {
			if err := r.process(item, doc); err != nil {
				return err
			}
		}
		if r.opts.Progress != nil {
			_, _ = fmt.Fprintf(r.opts.Progress, "  %s: %d documents read (%d reads in total)\n", item.label, read, r.report.Reads)
		}
		if len(docs) < query.Limit {
			return r.markDone(item)
		}
		query.After = docs[len(docs)-1].Path
		cursor.After = query.After
		if err := r.saveState(); err != nil {
			return err
		}
	}
}

// markDone records that item was read to its end.
func (r *run) markDone(item *planItem) error {
	if err := r.flushPending(); err != nil {
		return err
	}
	item.done = true
	r.state.item(item.label).Done = true
	for _, spec := range item.specs {
		if r.plannedDone(spec) {
			r.complete[spec.ID] = true
		}
	}
	return r.saveState()
}

// plannedDone reports whether every read of the plan that covers spec is done.
func (r *run) plannedDone(spec *Spec) bool {
	for _, item := range r.planned {
		if !item.done && slices.Contains(item.specs, spec) {
			return false
		}
	}
	return true
}

func (r *run) count(item *planItem) error {
	var n int64
	atLeast := false
	if item.get != "" {
		n = 1 // one document read; whether it exists is not asked in a dry run
	} else {
		left := r.budget(1)
		if left == 0 {
			r.stop()
			return nil
		}
		query := item.query
		if r.opts.MaxReads > 0 {
			// a count is billed one read per 1000 documents: count no more than the budget pays for
			query.Limit = int(min(r.opts.MaxReads-r.report.Reads, 1<<20)) * 1000
		}
		var err error
		if n, err = r.src.Count(r.ctx, query); err != nil {
			return fmt.Errorf("count %s: %w", item.label, err)
		}
		r.report.Reads += max(1, (n+999)/1000)
		if query.Limit > 0 && n >= int64(query.Limit) {
			atLeast = true
			r.stop()
		}
	}
	for _, spec := range item.specs {
		result := r.results[spec.ID]
		count := n
		if result.Count != nil { // several reads cover the collection
			count += *result.Count
		}
		result.Count, result.CountAtLeast = &count, result.CountAtLeast || atLeast
		if item.query.Group {
			result.Note = "the count is of every collection " + spec.Leaf() + " the group query reads, wherever its parents are"
		}
	}
	return nil
}

// process writes one document read by item.
func (r *run) process(item *planItem, doc Doc) error {
	parts := strings.Split(doc.Path, "/")
	var spec *Spec
	for _, candidate := range item.specs {
		if candidate.matches(parts) {
			spec = candidate
		}
	}
	if spec == nil || !r.inScope(doc.Path) {
		r.outside[strings.Join(collectionIDs(parts), "/")]++
		return nil
	}
	if r.seen[doc.Path] {
		return nil // read by another query of this run
	}
	result := r.results[spec.ID]
	if !r.allowed(spec, parts) {
		result.OtherParents++
		r.noteOtherParent(spec, parts)
		return nil
	}
	r.seen[doc.Path] = true
	id := parts[len(parts)-1]
	file, tooLong := spec.recordFile(parts)
	if tooLong == "" {
		tooLong = pathTooLong(filepath.Join(r.opts.Output, file))
	}
	if tooLong != "" {
		// The definition decides the form: one file per record holds an id only
		// when its file name fits. The run fails here, unless asked to go on.
		if !r.opts.ContinueOnLongIDs {
			return fmt.Errorf("%w: collection %s, document %s: %s; declare the collection with all its records in one file (record_file.type map[$record_id]map[$field_name]any), or pass --continue-on-long-ids to copy the rest and report it", ErrIDTooLong, spec.ID, shown(doc.Path), tooLong)
		}
		result.IDTooLong++
		_, _ = fmt.Fprintf(r.opts.Log, "not copied (id too long for one file per record): %s: %s\n", shown(doc.Path), tooLong)
		return nil
	}
	if collision := r.caseCollision(file, spec.multi); collision != "" {
		return r.reject(spec, doc.Path, []string{collision}, nil, nil)
	}
	converted, err := convertRecord(doc.Data, spec.Def)
	var bad *valueError
	if errors.As(err, &bad) {
		column, _, nested := strings.Cut(bad.path, ".")
		if spec.Def.Columns[column] == nil {
			return r.reject(spec, doc.Path, []string{r.undeclared([]string{column})}, nil, nil)
		}
		where := map[bool]string{true: " (inside it)"}[nested]
		return r.reject(spec, doc.Path, []string{fmt.Sprintf("field %q holds a Firestore value of type %s%s that the mirror cannot write", column, bad.kind, where)}, []string{column}, nil)
	}
	for _, field := range applyMasks(converted, r.policy.Mask[spec.ID], spec.Def) {
		if r.masked[spec.ID] == nil {
			r.masked[spec.ID] = map[string]bool{}
		}
		r.masked[spec.ID][field] = true
	}
	content := spec.encode(converted)
	key := recordKey(id)
	if spec.multi {
		key = id // a record kept in one file is keyed by its id as it is
	}
	readBack, _ := spec.decode(content) // the mirror's own encoding always reads back (TestEncodeYAMLRoundTrips)
	var problems, fields, undeclared []string
	for _, problem := range datavalidator.ValidateRecordData(spec.Def, key, readBack) {
		switch {
		case problem.FieldName != "" && spec.Def.Columns[problem.FieldName] == nil:
			undeclared = append(undeclared, problem.FieldName)
		default:
			problems = append(problems, reasonOf(problem))
			if problem.FieldName != "" && !slices.Contains(fields, problem.FieldName) {
				fields = append(fields, problem.FieldName)
			}
		}
	}
	if len(undeclared) > 0 {
		problems = append([]string{r.undeclared(undeclared)}, problems...)
	}
	for _, problem := range temporalProblems(spec.Def, converted) {
		problems = append(problems, problem)
		name, _, _ := strings.Cut(strings.TrimPrefix(problem, `field "`), `"`)
		fields = append(fields, name)
	}
	if len(problems) > 0 {
		return r.reject(spec, doc.Path, problems, fields, content)
	}
	if spec.multi {
		multi := r.pending(spec, file)
		multi.records[id], multi.unsaved[id] = converted, converted
	} else {
		written, err := writeFileIfChanged(filepath.Join(r.opts.Output, file), content)
		if err != nil {
			return err
		}
		r.files[strings.ToLower(file)] = doc.Path
		if written {
			r.report.Written++
		} else {
			r.report.Unchanged++
		}
	}
	for dir := filepath.Dir(file); dir != "."; dir = filepath.Dir(dir) {
		r.folders[strings.ToLower(dir)] = dir
	}
	result.Records++
	r.report.Records++
	r.copied[doc.Path] = true
	return nil
}

// splitLast is a document path without its id, and the id.
func splitLast(path string) (string, string) {
	at := strings.LastIndexByte(path, '/')
	return path[:at], path[at+1:]
}

// caseCollision says why a record file cannot be written because it, or a
// folder it is in, differs only in case from one an earlier document of this
// run (or of the runs it resumes) was written to, or is "". On a
// case-insensitive filesystem (macOS and Windows by default) the two would be
// one file, or one folder holding the records of two parents.
//
// The ids in a file that keeps all records of a collection are keys of one
// map, which no filesystem folds: only its folders are checked (multi).
func (r *run) caseCollision(file string, multi bool) string {
	if other, taken := r.files[strings.ToLower(file)]; taken && !multi {
		return fmt.Sprintf("its record file name differs only in case from the one of %s: a case-insensitive filesystem would keep one of them", other)
	}
	var dirs []string
	for dir := filepath.Dir(file); dir != "."; dir = filepath.Dir(dir) {
		dirs = append(dirs, dir)
	}
	for _, dir := range slices.Backward(dirs) { // the folder nearest the root names the parent
		if other, taken := r.folders[strings.ToLower(dir)]; taken && other != dir {
			return fmt.Sprintf("its folder %s differs only in case from the folder %s of other records: a case-insensitive filesystem would put both in one", filepath.ToSlash(dir), filepath.ToSlash(other))
		}
	}
	return ""
}

// undeclared describes field names the definition does not declare without
// naming them: a name can be data. The fingerprint is keyed by this run's
// salt, so it cannot be looked up, and equal sets of names have equal
// fingerprints within the run.
func (r *run) undeclared(names []string) string {
	names = slices.Compact(slices.Sorted(slices.Values(names)))
	mac := hmac.New(sha256.New, r.salt)
	mac.Write([]byte(strings.Join(names, "\x00")))
	return fmt.Sprintf("%d field(s) the definition does not declare (names not shown, as they may be data; fingerprint %x)", len(names), mac.Sum(nil)[:4])
}

// allowed reports whether the policy's paths allow a document path of spec.
func (r *run) allowed(spec *Spec, parts []string) bool {
	for _, pattern := range r.patterns[spec.ID] {
		if matchesPattern(pattern, parts) {
			return true
		}
	}
	return false
}

// noteOtherParent records the path of a document at a path the policy does
// not allow, with every id the patterns do not pin shown as "*".
func (r *run) noteOtherParent(spec *Spec, parts []string) {
	shown := slices.Clone(parts)
	for i := 1; i < len(shown); i += 2 {
		pinned := false
		for _, pattern := range r.patterns[spec.ID] {
			pinned = pinned || pattern[i] != "*"
		}
		if !pinned || i == len(shown)-1 {
			shown[i] = "*"
		}
	}
	if r.others[spec.ID] == nil {
		r.others[spec.ID] = map[string]bool{}
	}
	if len(r.others[spec.ID]) < maxOtherParentPaths {
		r.others[spec.ID][strings.Join(shown, "/")] = true
	}
}

// reject records a document that is not written into the database. Its
// masked content goes to the rejected folder only with KeepRejected.
func (r *run) reject(spec *Spec, path string, reasons, fields []string, content []byte) error {
	r.results[spec.ID].Rejected++
	slices.Sort(fields)
	r.rejected = append(r.rejected, Rejection{Path: path, Collection: spec.ID, Reasons: reasons, Fields: slices.Compact(fields)})
	if !r.opts.KeepRejected || content == nil {
		return nil
	}
	file := filepath.Join(r.opts.Output, stateDirName, "rejected")
	for _, part := range strings.Split(path, "/") {
		file = filepath.Join(file, keyEncoding.Encode(part)) // the record file of a rejected document was writable: its names fit
	}
	if tooLong := pathTooLong(file + ".json"); tooLong != "" {
		last := &r.rejected[len(r.rejected)-1]
		last.Reasons = append(last.Reasons, "its rejected record was not kept: "+tooLong)
		return nil
	}
	_, err := writeFileIfChanged(file+".json", content)
	return err
}

// valueFreeMessages start the inGitDB findings that name no value.
var valueFreeMessages = []string{"missing required field", "wrong type for field", "computed column"}

// reasonOf describes a validation finding without the value it concerns: a
// finding that may quote a value (an enum or length check) is named by its
// field alone, and an undeclared field is not named.
func reasonOf(problem ingitdb.ValidationError) string {
	if strings.HasPrefix(problem.Message, "undeclared field") {
		return "a field the definition does not declare (its name is not shown, as it may be data)"
	}
	for _, prefix := range valueFreeMessages {
		if strings.HasPrefix(problem.Message, prefix) {
			return describe(problem)
		}
	}
	if problem.FieldName != "" {
		return fmt.Sprintf("field %q: the value does not meet the column's constraints (the value is not shown)", problem.FieldName)
	}
	return "the record does not meet its definition (the finding is not shown, as it may quote a value)"
}

func describe(problem ingitdb.ValidationError) string {
	if problem.FieldName != "" && !strings.Contains(problem.Message, problem.FieldName) {
		return fmt.Sprintf("field %q: %s", problem.FieldName, problem.Message)
	}
	return problem.Message
}

// prepareOutput loads the resume state, clears what a previous run left in
// the state folder (unless it is resumed), and writes the definitions.
func (r *run) prepareOutput(plan []*planItem) error {
	if err := r.claimOutput(); err != nil {
		return err
	}
	digest, err := r.defs.CopyTo(r.opts.Output)
	if err != nil {
		return err
	}
	r.defsHash = digest
	r.state = r.loadState(plan)
	if r.state == nil {
		r.state = r.newState()
		return firstError(removeAll(filepath.Join(r.opts.Output, stateDirName, "rejected")), removeAll(partialDir(r.opts.Output)))
	}
	return r.loadPartials()
}

// finish fills what is known once every read is done.
func (r *run) finish() error {
	r.merge()
	for _, spec := range r.defs.Specs {
		result := r.results[spec.ID]
		result.Complete = result.Selected && r.complete[spec.ID] && result.Rejected == 0 && result.IDTooLong == 0
		result.Masked = sortedKeys(r.masked[spec.ID])
		result.OtherParentPaths = sortedKeys(r.others[spec.ID])
		result.Resumed = r.resumed[spec.ID]
		if !r.complete[spec.ID] || r.resumed[spec.ID] {
			continue // stale files are known only for a collection this run read from its start
		}
		gone := map[string][]string{} // record file kept in one file -> ids no longer in the source
		for _, path := range existingRecords(r.opts.Output, spec) {
			if !r.inScope(path) || r.seen[path] || !r.allowed(spec, strings.Split(path, "/")) {
				continue
			}
			file, _ := spec.recordFile(strings.Split(path, "/"))
			shownAs := filepath.ToSlash(file)
			if spec.multi {
				_, id := splitLast(path)
				shownAs += "#" + keyEncoding.Encode(id)
				gone[file] = append(gone[file], id)
			}
			if !r.opts.Prune {
				r.report.Stale = append(r.report.Stale, shownAs)
				continue
			}
			if !spec.multi {
				if err := removeFile(filepath.Join(r.opts.Output, file)); err != nil {
					return err
				}
			}
			r.report.Pruned = append(r.report.Pruned, shownAs)
		}
		for _, file := range slices.Sorted(maps.Keys(gone)) {
			if r.opts.Prune {
				if err := pruneRecords(r.opts.Output, file, spec, gone[file]); err != nil {
					return err
				}
			}
		}
	}
	for path := range r.copied {
		parts := strings.Split(path, "/")
		if len(parts) < 4 {
			continue
		}
		parent := strings.Join(parts[:len(parts)-2], "/")
		parentSpec := r.defs.Spec(strings.Join(collectionIDs(parts[:len(parts)-2]), "/"))
		if parentSpec != nil && r.complete[parentSpec.ID] && !r.resumed[parentSpec.ID] && r.inScope(parent) && !r.seen[parent] {
			r.results[strings.Join(collectionIDs(parts), "/")].BelowMissingParent++
		}
	}
	slices.Sort(r.report.Stale)
	slices.Sort(r.report.Pruned)
	for collection, n := range r.state.Outside {
		r.report.Outside = append(r.report.Outside, OutsideRead{Collection: collection, Defined: r.defs.Spec(collection) != nil, Documents: n})
	}
	slices.SortFunc(r.report.Outside, func(a, b OutsideRead) int { return strings.Compare(a.Collection, b.Collection) })
	r.report.Rejected = slices.Clone(r.state.Rejected)
	slices.SortFunc(r.report.Rejected, func(a, b Rejection) int { return strings.Compare(a.Path, b.Path) })
	r.report.Validation = validateOutput(r.ctx, r.opts.Output)
	if len(r.report.Validation) > 0 {
		r.report.Incomplete = append(r.report.Incomplete, fmt.Sprintf("inGitDB validation of the output found %d error(s)", len(r.report.Validation)))
	}
	return r.endState()
}

func collectionIDs(parts []string) []string {
	var ids []string
	for i := 0; i < len(parts); i += 2 {
		ids = append(ids, parts[i])
	}
	return ids
}

// validateDatabase is inGitDB's database validator (a test seam).
var validateDatabase = datavalidator.NewValidator().Validate

// validateOutput runs inGitDB's own validation of the written database (the
// checks `ingitdb validate` makes) and returns its findings, without values.
func validateOutput(ctx context.Context, out string) []string {
	def, err := readDefinition(out)
	if err != nil {
		return []string{err.Error()}
	}
	result, err := validateDatabase(ctx, out, def)
	if err != nil {
		return []string{err.Error()}
	}
	var findings []string
	for _, problem := range result.Errors() {
		where := problem.CollectionID
		if problem.RecordKey != "" {
			where += "/" + problem.RecordKey
		}
		if problem.FilePath != "" {
			if rel, relErr := filepath.Rel(out, problem.FilePath); relErr == nil {
				where = filepath.ToSlash(rel)
			}
		}
		findings = append(findings, where+": "+reasonOf(problem))
	}
	slices.Sort(findings)
	return findings
}

func (r *run) writeReport() error {
	r.fillReport()
	content, _ := json.MarshalIndent(r.report, "", "  ") // the report holds only strings, numbers and bools
	_, err := writeFileIfChanged(filepath.Join(r.opts.Output, stateDirName, "report.json"), append(content, '\n'))
	return err
}

func (r *run) fillReport() {
	r.report.Complete = !r.opts.DryRun
	for i, spec := range r.defs.Specs {
		result := *r.results[spec.ID]
		r.report.Collections[i] = result
		if result.Action == ActionUnsupported && result.inSelection {
			r.report.Incomplete = appendOnce(r.report.Incomplete, fmt.Sprintf("collection %s cannot be written: %s", spec.ID, result.Note))
		}
	}
	var tooLong int64
	for _, result := range r.report.Collections {
		tooLong += result.IDTooLong
	}
	if tooLong > 0 {
		r.report.Incomplete = appendOnce(r.report.Incomplete, fmt.Sprintf("%d document(s) were not copied: their id is too long for file per record (a record file name holds at most 255 bytes)", tooLong))
	}
	if len(r.report.Rejected) > 0 {
		r.report.Incomplete = appendOnce(r.report.Incomplete, fmt.Sprintf("%d document(s) were not written into the database (see rejected)", len(r.report.Rejected)))
	}
	if len(r.report.Incomplete) > 0 {
		r.report.Complete = false
	}
}

func appendOnce(list []string, item string) []string {
	if slices.Contains(list, item) {
		return list
	}
	return append(list, item)
}

// shown shortens a document path for the log: an id may be 1500 bytes long.
func shown(path string) string {
	if len(path) > 200 {
		return path[:200] + "…"
	}
	return path
}

// summarize writes the human summary to the log.
func (r *run) summarize() {
	if r.opts.DryRun {
		r.fillReport()
	}
	log := r.opts.Log
	for _, result := range r.report.Collections {
		switch {
		case r.opts.DryRun && result.Selected && result.Count != nil:
			atLeast := map[bool]string{true: "≥"}[result.CountAtLeast]
			_, _ = fmt.Fprintf(log, "%-11s %-50s %8s documents  (%s)\n", result.Action, result.Collection, fmt.Sprintf("%s%d", atLeast, *result.Count), result.Query)
		case r.opts.DryRun && result.Selected:
			_, _ = fmt.Fprintf(log, "%-11s %-50s %8s documents  (%s)\n", result.Action, result.Collection, "?", result.Query)
		case result.Selected:
			line := fmt.Sprintf("%-11s %-50s %8d records", result.Action, result.Collection, result.Records)
			if result.Rejected > 0 {
				line += fmt.Sprintf(", %d rejected", result.Rejected)
			}
			if result.OtherParents > 0 {
				line += fmt.Sprintf(", %d at other parents not copied (%s)", result.OtherParents, strings.Join(result.OtherParentPaths, ", "))
			}
			if result.IDTooLong > 0 {
				line += fmt.Sprintf(", %d not copied: id too long for file per record", result.IDTooLong)
			}
			if result.BelowMissingParent > 0 {
				line += fmt.Sprintf(", %d below a parent document that does not exist", result.BelowMissingParent)
			}
			_, _ = fmt.Fprintln(log, line)
		default:
			_, _ = fmt.Fprintf(log, "%-11s %-50s not copied: %s\n", result.Action, result.Collection, result.Note)
		}
	}
	for _, id := range r.report.UndefinedRootCollections {
		_, _ = fmt.Fprintf(log, "%-11s %-50s not copied: a root collection of the source that the mirror does not define\n", "undefined", id)
	}
	for _, outside := range r.report.Outside {
		_, _ = fmt.Fprintf(log, "read, not copied: %d document(s) of %s (%s)\n", outside.Documents, outside.Collection, map[bool]string{true: "defined, not selected", false: "not defined in the mirror"}[outside.Defined])
	}
	for _, rejection := range r.report.Rejected {
		_, _ = fmt.Fprintf(log, "rejected: %s: %s\n", shown(rejection.Path), strings.Join(rejection.Reasons, "; "))
	}
	for _, stale := range r.report.Stale {
		_, _ = fmt.Fprintf(log, "stale: %s is in the output but no longer in the source (left as it is; --prune deletes it)\n", shown(stale))
	}
	for _, pruned := range r.report.Pruned {
		_, _ = fmt.Fprintf(log, "pruned: %s (no longer in the source)\n", shown(pruned))
	}
	for _, finding := range r.report.Validation {
		_, _ = fmt.Fprintf(log, "validation: %s\n", finding)
	}
	if r.opts.DryRun {
		_, _ = fmt.Fprintf(log, "dry run: %d read(s) spent counting; nothing was written\n", r.report.Reads)
		if r.report.BudgetReached {
			_, _ = fmt.Fprintf(log, "incomplete: stopped at the read budget of %d reads\n", r.opts.MaxReads)
		}
		return
	}
	_, _ = fmt.Fprintf(log, "db mirror: %d records (%d written, %d unchanged), %d rejected, %d Firestore reads\n", r.report.Records, r.report.Written, r.report.Unchanged, len(r.report.Rejected), r.report.Reads)
	if r.report.Resumed {
		_, _ = fmt.Fprintf(log, "resumed: an earlier run stopped at its read budget; %d reads in all\n", r.report.ReadsTotal)
	}
	for _, reason := range r.report.Incomplete {
		_, _ = fmt.Fprintf(log, "incomplete: %s\n", reason)
	}
}
