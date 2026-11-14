package dbmirror

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

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

// ErrUsage marks an error in what the caller asked for (exit code 2).
var ErrUsage = errors.New("usage")

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
	// MaxReads stops the run once this many Firestore reads were made (0: no limit).
	MaxReads int64
	// PageSize is how many documents one request reads (default DefaultPageSize).
	PageSize int
	// Source is how the source is shown in the report (never a credential).
	Source string
	// Log receives one line per collection and the summary (nil: discarded).
	Log io.Writer
	// Progress, when set, receives one line per page read.
	Progress io.Writer
}

// CollectionResult is what happened to one defined collection.
type CollectionResult struct {
	Collection string   `json:"collection"`
	Action     string   `json:"action"`
	Selected   bool     `json:"selected"`
	Note       string   `json:"note,omitempty"`
	Query      string   `json:"query,omitempty"`
	Count      *int64   `json:"count,omitempty"`
	Records    int64    `json:"records"`
	Rejected   int64    `json:"rejected,omitempty"`
	Masked     []string `json:"masked_fields,omitempty"`
	// BelowMissingParent counts records whose parent document does not exist
	// in the source (Firestore keeps such sub-collections).
	BelowMissingParent int64 `json:"below_missing_parent,omitempty"`
	Complete           bool  `json:"complete"`

	inSelection bool // asked for by Collections and Under, whatever the policy says
}

// Rejection is a document that was read but not written into the database
// because it does not fit its definition. Its record is kept under
// .datatug-mirror/rejected/ so nothing is lost.
type Rejection struct {
	Path       string   `json:"path"`
	Collection string   `json:"collection"`
	Reasons    []string `json:"reasons"`
}

// OutsideRead counts documents a collection-group read returned that belong
// to no selected collection: their collection path is shown, so a collection
// that is missing from the definitions becomes visible.
type OutsideRead struct {
	Collection string `json:"collection"`
	Defined    bool   `json:"defined"`
	Documents  int64  `json:"documents"`
}

// Report is the result of a run. It holds no time, host or credential, so the
// same source and options give the same report.
type Report struct {
	Format                   string             `json:"format"`
	Source                   string             `json:"source"`
	Under                    string             `json:"under,omitempty"`
	DryRun                   bool               `json:"dry_run,omitempty"`
	Complete                 bool               `json:"complete"`
	Incomplete               []string           `json:"incomplete,omitempty"`
	Reads                    int64              `json:"reads"`
	MaxReads                 int64              `json:"max_reads,omitempty"`
	Records                  int64              `json:"records"`
	Collections              []CollectionResult `json:"collections"`
	Rejected                 []Rejection        `json:"rejected,omitempty"`
	UndefinedRootCollections []string           `json:"undefined_root_collections,omitempty"`
	Outside                  []OutsideRead      `json:"read_outside_selection,omitempty"`
	Stale                    []string           `json:"stale_record_files,omitempty"`
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
	seen     map[string]bool // every document path read
	copied   map[string]bool // document paths written into the database
	masked   map[string]map[string]bool
	outside  map[string]int64
	stopped  bool
	complete map[string]bool // collection ids read to the end
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
		seen:     map[string]bool{},
		copied:   map[string]bool{},
		masked:   map[string]map[string]bool{},
		outside:  map[string]int64{},
		complete: map[string]bool{},
	}
	if opts.Under != "" {
		r.under = strings.Split(opts.Under, "/")
		if slices.Contains(r.under, "") {
			return nil, fmt.Errorf("%w: %q is not a Firestore path", ErrUsage, opts.Under)
		}
	}
	if err := r.selectCollections(); err != nil {
		return nil, err
	}
	plan := r.plan()
	if !opts.DryRun {
		if err := r.prepareOutput(); err != nil {
			return nil, err
		}
	}
	if err := r.listRoots(); err != nil {
		return nil, err
	}
	for _, item := range plan {
		if err := r.execute(item); err != nil {
			return nil, err
		}
	}
	if !opts.DryRun {
		r.finish()
		if err := r.writeReport(); err != nil {
			return nil, err
		}
	}
	r.summarize()
	return r.report, nil
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
		return fmt.Errorf("%w: no collection defined in the mirror is at or below %s", ErrUsage, r.opts.Under)
	}
	return nil
}

// covers reports whether documents of spec can be at or below opts.Under.
func (r *run) covers(spec *Spec) bool {
	if len(r.under) > 2*spec.Depth() {
		return false
	}
	for i := 0; 2*i < len(r.under); i++ {
		if r.under[2*i] != spec.Segments[i] {
			return false
		}
	}
	return true
}

// inScope reports whether a document path is at or below opts.Under.
func (r *run) inScope(path string) bool {
	return r.opts.Under == "" || path == r.opts.Under || strings.HasPrefix(path, r.opts.Under+"/")
}

// plan is the reads that cover every selected collection: one document read
// when Under is a document of the collection, one query per root collection
// whose id no deeper collection shares, and otherwise one collection-group
// query per collection id, shared by every selected collection with that id.
func (r *run) plan() []*planItem {
	byLeaf := map[string][]*Spec{}
	var items []*planItem
	for _, spec := range r.defs.Specs {
		if !r.results[spec.ID].Selected {
			continue
		}
		if len(r.under) == 2*spec.Depth() {
			items = append(items, &planItem{get: r.opts.Under, specs: []*Spec{spec}, label: "document " + r.opts.Under})
			continue
		}
		byLeaf[spec.Leaf()] = append(byLeaf[spec.Leaf()], spec)
	}
	for _, leaf := range sortedKeys(byLeaf) {
		specs := byLeaf[leaf]
		roots := true
		for _, spec := range specs {
			roots = roots && spec.Depth() == 1
		}
		if roots { // one spec: two root collections cannot share an id
			items = append(items, &planItem{query: Query{Collection: leaf}, specs: specs, label: "collection " + leaf})
			continue
		}
		query := Query{Collection: leaf, Group: true}
		label := "collection group " + leaf
		if len(r.under) >= 2 {
			query.Under = strings.Join(r.under[:len(r.under)/2*2], "/")
			label += " under " + query.Under
		}
		items = append(items, &planItem{query: query, specs: specs, label: label})
	}
	for _, item := range items {
		for _, spec := range item.specs {
			r.results[spec.ID].Query = item.label
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
		r.report.Incomplete = append(r.report.Incomplete, fmt.Sprintf("stopped at the read budget of %d reads", r.opts.MaxReads))
	}
}

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
		r.markDone(item)
		return nil
	}
	query := item.query
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
			r.markDone(item)
			return nil
		}
		query.After = docs[len(docs)-1].Path
	}
}

func (r *run) markDone(item *planItem) {
	item.done = true
	for _, spec := range item.specs {
		r.complete[spec.ID] = true
	}
}

func (r *run) count(item *planItem) error {
	var n int64
	if item.get != "" {
		n = 1 // one document read; whether it exists is not asked in a dry run
	} else {
		if r.budget(1) == 0 {
			r.stop()
			return nil
		}
		var err error
		if n, err = r.src.Count(r.ctx, item.query); err != nil {
			return fmt.Errorf("count %s: %w", item.label, err)
		}
		r.report.Reads += max(1, (n+999)/1000) // a count is billed one read per 1000 documents
	}
	for _, spec := range item.specs {
		count := n
		r.results[spec.ID].Count = &count
		if len(item.specs) > 1 {
			r.results[spec.ID].Note = "count is the whole collection group: shared by " + specIDs(item.specs)
		}
	}
	return nil
}

func specIDs(specs []*Spec) string {
	ids := make([]string, len(specs))
	for i, spec := range specs {
		ids[i] = spec.ID
	}
	return strings.Join(ids, ", ")
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
		var collection []string
		for i := 0; i < len(parts); i += 2 {
			collection = append(collection, parts[i])
		}
		r.outside[strings.Join(collection, "/")]++
		return nil
	}
	r.seen[doc.Path] = true
	result := r.results[spec.ID]
	id := parts[len(parts)-1]
	reasons := []string(nil)
	var content []byte
	converted, err := jsonValue(doc.Data, "")
	switch {
	case strings.HasPrefix(id, "."):
		reasons = append(reasons, "the document id starts with \".\": inGitDB does not read such a file")
	case err != nil:
		reasons = append(reasons, err.Error())
	default:
		data := converted.(map[string]any)
		for _, field := range applyMasks(data, r.policy.Mask[spec.ID]) {
			if r.masked[spec.ID] == nil {
				r.masked[spec.ID] = map[string]bool{}
			}
			r.masked[spec.ID][field] = true
		}
		content = encodeRecord(data)
		for _, problem := range datavalidator.ValidateRecordData(spec.Def, id, decodeRecord(content)) {
			reasons = append(reasons, describe(problem))
		}
	}
	if len(reasons) > 0 {
		result.Rejected++
		r.report.Rejected = append(r.report.Rejected, Rejection{Path: doc.Path, Collection: spec.ID, Reasons: reasons})
		if content != nil {
			if _, err := writeFileIfChanged(filepath.Join(r.opts.Output, stateDirName, "rejected", filepath.FromSlash(doc.Path)+".json"), content); err != nil {
				return err
			}
		}
		return nil
	}
	written, err := writeFileIfChanged(filepath.Join(r.opts.Output, spec.recordFile(parts)), content)
	if err != nil {
		return err
	}
	if written {
		r.report.Written++
	} else {
		r.report.Unchanged++
	}
	result.Records++
	r.report.Records++
	r.copied[doc.Path] = true
	return nil
}

func describe(problem ingitdb.ValidationError) string {
	if problem.FieldName != "" && !strings.Contains(problem.Message, problem.FieldName) {
		return fmt.Sprintf("field %q: %s", problem.FieldName, problem.Message)
	}
	return problem.Message
}

// prepareOutput writes the definitions into the output and clears the state
// folder of the previous run.
func (r *run) prepareOutput() error {
	if err := removeAll(filepath.Join(r.opts.Output, stateDirName)); err != nil {
		return err
	}
	return r.defs.CopyTo(r.opts.Output)
}

// finish fills what is known once every read is done.
func (r *run) finish() {
	for _, spec := range r.defs.Specs {
		result := r.results[spec.ID]
		result.Complete = result.Selected && r.complete[spec.ID] && result.Rejected == 0
		result.Masked = sortedKeys(r.masked[spec.ID])
		if !r.complete[spec.ID] {
			continue
		}
		for _, path := range existingRecords(r.opts.Output, spec) {
			if r.inScope(path) && !r.copied[path] {
				r.report.Stale = append(r.report.Stale, filepath.ToSlash(spec.recordFile(strings.Split(path, "/"))))
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
		if parentSpec != nil && r.complete[parentSpec.ID] && r.inScope(parent) && !r.seen[parent] {
			r.results[strings.Join(collectionIDs(parts), "/")].BelowMissingParent++
		}
	}
	slices.Sort(r.report.Stale)
	for collection, n := range r.outside {
		r.report.Outside = append(r.report.Outside, OutsideRead{Collection: collection, Defined: r.defs.Spec(collection) != nil, Documents: n})
	}
	slices.SortFunc(r.report.Outside, func(a, b OutsideRead) int { return strings.Compare(a.Collection, b.Collection) })
	slices.SortFunc(r.report.Rejected, func(a, b Rejection) int { return strings.Compare(a.Path, b.Path) })
	r.report.Validation = validateOutput(r.ctx, r.opts.Output)
	if len(r.report.Validation) > 0 {
		r.report.Incomplete = append(r.report.Incomplete, fmt.Sprintf("inGitDB validation of the output found %d error(s)", len(r.report.Validation)))
	}
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
// checks `ingitdb validate` makes) and returns its findings.
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
		findings = append(findings, where+": "+describe(problem))
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
	if len(r.report.Rejected) > 0 {
		r.report.Incomplete = appendOnce(r.report.Incomplete, fmt.Sprintf("%d document(s) did not fit their definition and were not written (see rejected)", len(r.report.Rejected)))
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

// summarize writes the human summary to the log.
func (r *run) summarize() {
	if r.opts.DryRun {
		r.fillReport()
	}
	log := r.opts.Log
	for _, result := range r.report.Collections {
		switch {
		case r.opts.DryRun && result.Selected && result.Count != nil:
			_, _ = fmt.Fprintf(log, "%-11s %-50s %8d documents  (%s)\n", result.Action, result.Collection, *result.Count, result.Query)
		case r.opts.DryRun && result.Selected:
			_, _ = fmt.Fprintf(log, "%-11s %-50s %8s documents  (%s)\n", result.Action, result.Collection, "?", result.Query)
		case result.Selected:
			line := fmt.Sprintf("%-11s %-50s %8d records", result.Action, result.Collection, result.Records)
			if result.Rejected > 0 {
				line += fmt.Sprintf(", %d rejected", result.Rejected)
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
		_, _ = fmt.Fprintf(log, "rejected: %s: %s\n", rejection.Path, strings.Join(rejection.Reasons, "; "))
	}
	for _, stale := range r.report.Stale {
		_, _ = fmt.Fprintf(log, "stale: %s is in the output but no longer in the source (left as it is)\n", stale)
	}
	for _, finding := range r.report.Validation {
		_, _ = fmt.Fprintf(log, "validation: %s\n", finding)
	}
	if r.opts.DryRun {
		_, _ = fmt.Fprintf(log, "dry run: %d read(s) spent counting; nothing was written\n", r.report.Reads)
		return
	}
	_, _ = fmt.Fprintf(log, "db mirror: %d records (%d written, %d unchanged), %d rejected, %d Firestore reads\n", r.report.Records, r.report.Written, r.report.Unchanged, len(r.report.Rejected), r.report.Reads)
	for _, reason := range r.report.Incomplete {
		_, _ = fmt.Fprintf(log, "incomplete: %s\n", reason)
	}
}
