package dbmirror

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ingitdb/ingitdb-go/ingitdb"
	"github.com/ingitdb/ingitdb-go/ingitdb/validator"
)

// Spec is one defined collection of the mirror: one Firestore collection path
// such as teams/{team}/ext/{module}/events, named by its collection
// ids alone ("teams/ext/events"), the way inGitDB names a sub-collection.
type Spec struct {
	// ID is the collection ids of the path joined by "/".
	ID string
	// Segments are the collection ids of the path, root first.
	Segments []string
	// Def is the inGitDB definition of this collection.
	Def *ingitdb.CollectionDef
	// Unsupported, when not empty, says why records of this collection cannot
	// be written; the collection is then reported and never read.
	Unsupported string

	rootRel string                   // the root collection's data folder, relative to the definitions folder
	chain   []*ingitdb.CollectionDef // definitions from the root collection down to this one
	prefix  string                   // record file name before {key}
	suffix  string                   // record file name after {key}
	multi   bool                     // all records of a collection (per parent) in one file, keyed by id
}

// Leaf is the id of the collection itself (the last segment).
func (s *Spec) Leaf() string { return s.Segments[len(s.Segments)-1] }

// Depth is the number of collection levels of the path (1 for a root collection).
func (s *Spec) Depth() int { return len(s.Segments) }

// Definitions is the loaded mirror.
type Definitions struct {
	Dir   string // absolute folder of the definitions
	Specs []*Spec
	byID  map[string]*Spec
}

// Spec returns the defined collection named id, or nil.
func (d *Definitions) Spec(id string) *Spec { return d.byID[id] }

var (
	readDefinition = validator.ReadDefinition
	absPath        = filepath.Abs
)

// LoadDefinitions reads the inGitDB definitions of a mirror from dir (an
// inGitDB database folder: .ingitdb/root-collections.yaml and the collection
// definitions). Only definitions are used; any records in dir are ignored.
func LoadDefinitions(dir string) (*Definitions, error) {
	abs, err := absPath(dir)
	if err != nil {
		return nil, err
	}
	def, err := readDefinition(abs)
	if err != nil {
		return nil, fmt.Errorf("read inGitDB definitions in %s: %w", dir, err)
	}
	defs := &Definitions{Dir: abs, byID: map[string]*Spec{}}
	for _, id := range slices.Sorted(maps.Keys(def.Collections)) {
		col := def.Collections[id]
		rel, err := filepath.Rel(abs, col.DirPath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("root collection %q is stored outside the definitions folder", id)
		}
		defs.add(rel, nil, []string{id}, col)
	}
	if len(defs.Specs) == 0 {
		return nil, fmt.Errorf("the inGitDB definitions in %s define no collection", dir)
	}
	return defs, nil
}

func (d *Definitions) add(rootRel string, chain []*ingitdb.CollectionDef, segments []string, col *ingitdb.CollectionDef) {
	chain = append(slices.Clone(chain), col)
	spec := &Spec{ID: strings.Join(segments, "/"), Segments: segments, Def: col, rootRel: rootRel, chain: chain}
	spec.Unsupported = spec.layout()
	d.Specs = append(d.Specs, spec)
	d.byID[spec.ID] = spec
	for _, sub := range slices.Sorted(maps.Keys(col.SubCollections)) {
		d.add(rootRel, chain, append(slices.Clone(segments), sub), col.SubCollections[sub])
	}
}

// layout checks that the definition stores records in a form the mirror
// writes, and returns why not, or "". The definition decides the form:
//
//   - one file per record (type map[string]any), named from its key alone: the
//     file name is the encoded document id, so it must fit 255 bytes;
//   - all records of the collection in one file (type
//     map[$record_id]map[$field_name]any, a fixed name), keyed by the real
//     document ids; for a sub-collection, one such file per parent document.
//
// A parent of a sub-collection keeps one file per record (its records are
// the folders of the sub-collection). The format is yaml, yml or json.
func (s *Spec) layout() string {
	for i, col := range s.chain {
		rf := col.RecordFile
		switch {
		case rf == nil:
			return "the definition has no record_file"
		case rf.RecordType == ingitdb.SingleRecord:
			if strings.Count(rf.Name, "{key}") != 1 || strings.Count(rf.Name, "{") != 1 || strings.ContainsAny(rf.Name, `/\`) {
				return fmt.Sprintf("record_file.name %q must name the file by {key} alone", rf.Name)
			}
		case rf.RecordType == ingitdb.MapOfRecords && i < len(s.chain)-1:
			return "a parent collection keeps all its records in one file (map[$record_id]map[$field_name]any), which holds no folder for a sub-collection: declare the parent with one file per record"
		case rf.RecordType == ingitdb.MapOfRecords:
			if rf.Name == "" || strings.ContainsAny(rf.Name, `{}/\`) {
				return fmt.Sprintf("record_file.name %q of a collection kept in one file must be a fixed file name", rf.Name)
			}
		default:
			return fmt.Sprintf("record_file.type %q keeps several records in one list, which holds no Firestore document id; declare one file per record (map[string]any) or one file keyed by id (map[$record_id]map[$field_name]any)", rf.RecordType)
		}
	}
	rf := s.Def.RecordFile
	switch rf.Format {
	case ingitdb.RecordFormatYAML, ingitdb.RecordFormatYML, ingitdb.RecordFormatJSON:
	default:
		return fmt.Sprintf("record_file.format %q is not written yet; use yaml or json", rf.Format)
	}
	s.multi = rf.RecordType == ingitdb.MapOfRecords
	if !s.multi {
		s.prefix, s.suffix, _ = strings.Cut(rf.Name, "{key}")
	}
	return ""
}

// recordFile is the path, relative to the database folder, of the record file
// of the document at parts (collection, id, collection, id, ...), which must
// match the spec. Every id is written by encodeID, so the path stays inside
// the folder whatever the ids hold. problem says why the file cannot be
// written (a name too long), or is "".
func (s *Spec) recordFile(parts []string) (path, problem string) {
	dir := s.rootRel
	last := len(s.chain) - 1
	for level, col := range s.chain[:last] {
		name := keyEncoding.Encode(parts[2*level+1])
		if tooLong := nameTooLong(name); tooLong != "" {
			problem = "the id of its parent document at level " + strconv.Itoa(level+1) + " is too long: " + tooLong
		}
		dir = filepath.Join(dir, col.RecordFile.RecordsBasePath(), name, parts[2*level+2])
	}
	if s.multi {
		return filepath.Join(dir, s.Def.RecordFile.RecordsBasePath(), s.Def.RecordFile.Name), problem
	}
	name := s.prefix + keyEncoding.Encode(parts[2*last+1]) + s.suffix
	if tooLong := nameTooLong(name); tooLong != "" && problem == "" {
		problem = "the document id is too long for one file per record: " + tooLong
	}
	return filepath.Join(dir, s.Def.RecordFile.RecordsBasePath(), name), problem
}

// recordKey is the inGitDB key of the record of a document id: the id as its
// file name holds it.
func recordKey(id string) string { return keyEncoding.Encode(id) }

// matches reports whether a document path (split on "/") is a document of
// this collection.
func (s *Spec) matches(parts []string) bool {
	if len(parts) != 2*len(s.Segments) {
		return false
	}
	for i, segment := range s.Segments {
		if parts[2*i] != segment {
			return false
		}
	}
	return true
}

// definitionFile reports whether a file of the definitions folder (relative
// path) is part of the definitions rather than a record: it is in a folder
// whose name starts with "." (.ingitdb, .collection, .collections), or it is a
// root collection's definition.yaml.
func (d *Definitions) definitionFile(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for _, part := range parts[:len(parts)-1] {
		if strings.HasPrefix(part, ".") {
			return part != ".git"
		}
	}
	if parts[len(parts)-1] != ingitdb.CollectionDefFileName {
		return false
	}
	dir := filepath.Dir(rel)
	for _, spec := range d.Specs {
		if spec.Depth() == 1 && spec.rootRel == dir {
			return true
		}
	}
	return false
}

// CopyTo writes the definition files (never the records) of the mirror into
// out, so that out is an inGitDB database with the same collections. A file
// already holding the same bytes is not rewritten.
//
// It returns a digest of the files copied (their paths and content), which a
// resumed run compares, so it never continues under changed definitions.
func (d *Definitions) CopyTo(out string) (digest string, err error) {
	hash := sha256.New()
	err = filepath.WalkDir(d.Dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(d.Dir, path)
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == stateDirName {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || !d.definitionFile(rel) {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(hash, "%s\x00%d\x00%s", filepath.ToSlash(rel), len(content), content)
		_, err = writeFileIfChanged(filepath.Join(out, rel), content)
		return err
	})
	return hex.EncodeToString(hash.Sum(nil)), err
}
