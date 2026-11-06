// Package demofixtures holds the files `datatug demo install` writes into the
// local demo project: the pinned Chinook SQLite database, a small inGitDB
// database of synthetic support notes, the curated Perimeter policy and the
// Meaning Graph sensitivity labels. Everything is embedded in the binary, so
// installing the demo needs no network; the Chinook bytes are checked against
// their pinned sha256 before they are written.
package demofixtures

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"time"

	"github.com/datatug/datatug-core/pkg/meaninglabels"
)

// ChinookSHA256 and ChinookSize pin the embedded Chinook SQLite file: the same
// fixture https://chinook.demodb.dev/data/chinook.sqlite serves.
const (
	ChinookSHA256       = "7651ba378ac2fcd0dfc3c66fb101f7a7eed3ba39a612ec642b96e20702061f15"
	ChinookSize   int64 = 1007616
)

// Paths of the demo project files, relative to the project directory.
const (
	ChinookPath = "data/chinook.sqlite"
	SupportPath = "data/support"
	PolicyPath  = "policies/agent.yaml"
	LabelsPath  = "datatug-labels.json"
	NoticePath  = "data/NOTICE.md"
)

// Source ids of the demo project.
const (
	ChinookSource = "chinook"
	SupportSource = "support"
)

//go:embed all:data
var embedded embed.FS

// embeddedFS is the tree Files reads; a seam for tests.
var embeddedFS fs.FS = embedded

// File is one file of the demo project.
type File struct {
	Path    string // relative to the project directory, slash-separated
	Content []byte
	// Fixture marks a file whose bytes are pinned: a different file on disk
	// is refused (fail closed) rather than preserved.
	Fixture bool
}

// SHA256 is the hex sha256 of the content.
func (f File) SHA256() string {
	sum := sha256.Sum256(f.Content)
	return hex.EncodeToString(sum[:])
}

// labeledAt is the fixed time of the demo labels, so the document is the same
// on every install.
var labeledAt = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

// labelChanges are the owner decisions of the demo: contact details are pii,
// names were reviewed and are not sensitive.
var labelChanges = []meaninglabels.Change{
	{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: ChinookSource, Table: "Customer", Column: "Email"}, Class: meaninglabels.ClassPII, Kind: "email", Reason: "customer contact email"},
	{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: ChinookSource, Table: "Customer", Column: "Phone"}, Class: meaninglabels.ClassPII, Kind: "phone", Reason: "customer phone"},
	{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: ChinookSource, Table: "Customer", Column: "FirstName"}, Class: meaninglabels.ClassNone, Reason: "reviewed: shown to support agents"},
	{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: ChinookSource, Table: "Customer", Column: "LastName"}, Class: meaninglabels.ClassNone, Reason: "reviewed: shown to support agents"},
	{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: ChinookSource, Table: "Employee", Column: "Email"}, Class: meaninglabels.ClassPII, Kind: "email", Reason: "employee contact email"},
	{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: ChinookSource, Table: "Employee", Column: "Phone"}, Class: meaninglabels.ClassPII, Kind: "phone", Reason: "employee phone"},
	{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: ChinookSource, Table: "Employee", Column: "BirthDate"}, Class: meaninglabels.ClassPII, Kind: "birth-date", Reason: "employee birth date"},
	{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: ChinookSource, Table: "Employee", Column: "Address"}, Class: meaninglabels.ClassPII, Kind: "postal-address", Reason: "employee home address"},
	{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: ChinookSource, Table: "Employee", Column: "FirstName"}, Class: meaninglabels.ClassNone, Reason: "reviewed: staff directory name"},
	{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: ChinookSource, Table: "Employee", Column: "LastName"}, Class: meaninglabels.ClassNone, Reason: "reviewed: staff directory name"},
}

// applyLabel is meaninglabels.Apply; a seam for tests.
var applyLabel = meaninglabels.Apply

// Labels is the demo's label document.
func Labels() (meaninglabels.Document, error) {
	doc := meaninglabels.NewDocument()
	owner := meaninglabels.Actor{ID: "datatug-demo"}
	for _, change := range labelChanges {
		var err error
		if doc, _, err = applyLabel(doc, change, owner, labeledAt); err != nil {
			return meaninglabels.Document{}, fmt.Errorf("demo label %s: %w", change.Target, err)
		}
	}
	return doc, nil
}

// encodeLabels is meaninglabels.Encode; a seam for tests.
var encodeLabels = meaninglabels.Encode

// Files lists every file of the demo project, sorted by path. The Chinook
// bytes are verified against ChinookSHA256 and ChinookSize first.
func Files() ([]File, error) {
	var files []File
	err := fs.WalkDir(embeddedFS, "data", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		content, err := fs.ReadFile(embeddedFS, name)
		if err != nil {
			return err
		}
		rel := name
		if path.Dir(name) == "data/policies" {
			rel = path.Join("policies", path.Base(name))
		}
		files = append(files, File{Path: rel, Content: content, Fixture: rel == ChinookPath})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("demo fixtures: %w", err)
	}
	chinook := -1
	for i, f := range files {
		if f.Path == ChinookPath {
			chinook = i
		}
	}
	if chinook < 0 || int64(len(files[chinook].Content)) != ChinookSize || files[chinook].SHA256() != ChinookSHA256 {
		return nil, fmt.Errorf("demo fixtures: the embedded Chinook database does not match its pinned sha256")
	}
	doc, err := Labels()
	if err != nil {
		return nil, err
	}
	labels, err := encodeLabels(doc)
	if err != nil {
		return nil, fmt.Errorf("demo labels: %w", err)
	}
	files = append(files, File{Path: LabelsPath, Content: labels})
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}
