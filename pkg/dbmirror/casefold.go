package dbmirror

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// A case-insensitive filesystem (macOS APFS and Windows NTFS by default)
// keeps one file for Bob.yaml and bob.yaml, under the name it was created
// with: writing bob.yaml over an existing Bob.yaml writes into Bob.yaml, and a
// later delete of Bob.yaml deletes bob's record. A run never deletes or
// writes over a live record because the filesystem folds case:
//
//   - When the output holds the file (or the folder of the sub-collections) of
//     a record under its id in another case, and the source proves that the
//     old id is gone (the document does not exist, and no document of a
//     defined sub-collection is below it), the run renames the file or folder
//     to the new case before it writes. That is the only rename it makes.
//   - When the old id is still in the source, or the old name is not one the
//     mirror writes, the two ids cannot be kept apart: the run stops with
//     ErrCaseFold, which names both, and writes neither.
//   - A prune never deletes a file that is the one this run wrote under a name
//     that differs only in case.
//
// Two ids of one run (or of the runs it resumes) that differ only in case are
// found before any of this, by caseCollision: the second is rejected.
//
// Whether a folder folds case is found out on the folder itself, when a name
// that differs only in case is there: the two names are one file (os.SameFile)
// or two. The operating system's name decides nothing: a case-sensitive APFS
// volume, or a case-sensitive folder on Windows, exists.

// ErrCaseFold is the failure of a run that would write a record into the file
// or folder of a document of the source whose id differs only in case, on a
// filesystem that folds case. The run stops there, like at an id too long
// (ErrIDTooLong), and writes neither.
var ErrCaseFold = errors.New("two ids differ only in case on a filesystem that folds case")

// caseSensitiveHint says where a case-sensitive folder comes from.
const caseSensitiveHint = ` (on macOS, a case-sensitive APFS volume: Disk Utility > Add Volume > format "APFS (Case-sensitive)", or diskutil apfs addVolume <container disk> "Case-sensitive APFS" <name>)`

// errBudget stops a run whose proof that an old id is gone (goneFromSource)
// would pass --max-reads: like a stop at the read budget, a resume continues.
var errBudget = errors.New("the read budget is spent")

// Leftovers of an interrupted run: a rename's temporary name (".tmp-case-",
// random hex, "-" and the old name when it fits) and writeFileIfChanged's
// temporary files (".tmp-"). No record file name starts with ".".
const (
	leftoverPrefix   = ".tmp-"
	caseRenamePrefix = ".tmp-case-"
)

// fileSystem is what a run does to its output folder (a test seam: a test
// stands in a case-insensitive filesystem on a case-sensitive one).
type fileSystem interface {
	ReadFile(name string) ([]byte, error)
	ReadDir(name string) ([]fs.DirEntry, error)
	Lstat(name string) (fs.FileInfo, error)
	MkdirAll(path string, perm fs.FileMode) error
	CreateTemp(dir, pattern string) (*os.File, error)
	Chmod(name string, mode fs.FileMode) error
	Rename(from, to string) error
	Remove(name string) error
	RemoveAll(path string) error
}

// osFS is the filesystem of the operating system.
type osFS struct{}

func (osFS) ReadFile(name string) ([]byte, error)             { return os.ReadFile(name) }
func (osFS) ReadDir(name string) ([]fs.DirEntry, error)       { return os.ReadDir(name) }
func (osFS) Lstat(name string) (fs.FileInfo, error)           { return os.Lstat(name) }
func (osFS) MkdirAll(path string, perm fs.FileMode) error     { return os.MkdirAll(path, perm) }
func (osFS) CreateTemp(dir, pattern string) (*os.File, error) { return os.CreateTemp(dir, pattern) }
func (osFS) Chmod(name string, mode fs.FileMode) error        { return os.Chmod(name, mode) }
func (osFS) Rename(from, to string) error                     { return os.Rename(from, to) }
func (osFS) Remove(name string) error                         { return os.Remove(name) }
func (osFS) RemoveAll(path string) error                      { return os.RemoveAll(path) }

// outFS is the filesystem the output is written to.
var outFS fileSystem = osFS{}

// sameFile reports whether two paths name one existing file or folder.
func sameFile(a, b string) bool {
	infoA, errA := outFS.Lstat(a)
	infoB, errB := outFS.Lstat(b)
	return errA == nil && errB == nil && os.SameFile(infoA, infoB)
}

// namesIn is the names in dir by their lower case, read once per run and kept
// up to date by the run's own writes (noteWritten) and renames.
func (r *run) namesIn(dir string) (map[string]string, error) {
	if names, ok := r.names[dir]; ok {
		return names, nil
	}
	entries, err := outFS.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	names := make(map[string]string, len(entries))
	var leftovers []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), leftoverPrefix) {
			leftovers = append(leftovers, entry.Name())
			continue
		}
		names[strings.ToLower(entry.Name())] = entry.Name()
	}
	r.names[dir] = names
	for _, name := range leftovers {
		if err := r.recoverLeftover(dir, name, names); err != nil {
			return nil, err
		}
	}
	return names, nil
}

// recoverLeftover deals with a name an interrupted run left in dir. A
// rename's temporary name is put back under its old name when that name (in
// any case) is free: the rename then starts again. When it is not, or the
// old name is unknown, it is left as it is and the run says so (it may hold
// the only copy of records). A temporary file of an interrupted write is
// removed: the file it was to replace was never touched.
func (r *run) recoverLeftover(dir, name string, names map[string]string) error {
	path := filepath.Join(dir, name)
	shownAs, _ := filepath.Rel(r.opts.Output, path)
	shownAs = filepath.ToSlash(shownAs)
	rest, rename := strings.CutPrefix(name, caseRenamePrefix)
	if !rename {
		if err := removeAll(path); err != nil {
			return err
		}
		r.report.Recovered = append(r.report.Recovered, "removed "+shownAs)
		return nil
	}
	_, original, _ := strings.Cut(rest, "-")
	if _, taken := names[strings.ToLower(original)]; original == "" || taken {
		r.report.Incomplete = appendOnce(r.report.Incomplete, fmt.Sprintf("%s was left by an interrupted rename and cannot be put back (its old name is unknown or taken): it is left as it is; compare it with the records next to it, then remove it", shown(shownAs)))
		return nil
	}
	if err := renameFile(path, filepath.Join(dir, original)); err != nil {
		return err
	}
	names[strings.ToLower(original)] = original
	r.report.Recovered = append(r.report.Recovered, shownAs+" -> "+filepath.ToSlash(filepath.Join(filepath.Dir(shownAs), original)))
	return nil
}

// alignRecord gives each name of the record of the document at parts that
// holds an id (the folders of its parents, its file, and the folder of its
// own sub-collections) the case of that id (see above). It reports whether it
// renamed one.
func (r *run) alignRecord(spec *Spec, parts []string) (bool, error) {
	dir := filepath.Join(r.opts.Output, spec.rootRel)
	last := len(spec.chain) - 1
	folderName := func(name string) string { return name }
	renamed := false
	align := func(dir, want string, level int, idPart func(string) string) error {
		did, err := r.alignID(dir, want, parts[:2*level+2], r.defs.Spec(strings.Join(spec.Segments[:level+1], "/")), idPart)
		renamed = renamed || did
		return err
	}
	for level, col := range spec.chain[:last] {
		dir = filepath.Join(dir, col.RecordFile.RecordsBasePath())
		name := keyEncoding.Encode(parts[2*level+1])
		if err := align(dir, name, level, folderName); err != nil {
			return renamed, err
		}
		dir = filepath.Join(dir, name, parts[2*level+2])
	}
	if spec.multi {
		return renamed, nil
	}
	dir = filepath.Join(dir, spec.Def.RecordFile.RecordsBasePath())
	name := keyEncoding.Encode(parts[2*last+1])
	filePart := func(file string) string { return file[len(spec.prefix) : len(file)-len(spec.suffix)] }
	if err := align(dir, spec.prefix+name+spec.suffix, last, filePart); err != nil {
		return renamed, err
	}
	if len(spec.Def.SubCollections) > 0 {
		// The folder of the record's sub-collections is named by its id too.
		return renamed, align(dir, name, last, folderName)
	}
	return renamed, nil
}

// alignID renames the entry of dir whose name differs from want only in case
// to want, when the two names are one file (the folder folds case) and the
// source proves the document of the old name gone. doc is the document path
// of want's id, spec its collection, and idPart the part of a name that holds
// the encoded id. On a case-sensitive folder the two names are two files, and
// nothing is renamed. The rename goes through a temporary name, as not every
// filesystem renames a name to itself in another case.
func (r *run) alignID(dir, want string, doc []string, spec *Spec, idPart func(string) string) (bool, error) {
	names, err := r.namesIn(dir)
	if err != nil {
		return false, err
	}
	lower := strings.ToLower(want)
	have, ok := names[lower]
	if !ok || have == want || !sameFile(filepath.Join(dir, have), filepath.Join(dir, want)) {
		return false, nil
	}
	shownFrom, _ := filepath.Rel(r.opts.Output, filepath.Join(dir, have))
	shownFrom = filepath.ToSlash(shownFrom)
	newID := doc[len(doc)-1]
	oldID, decoded := keyEncoding.Decode(idPart(have))
	if !decoded {
		return false, fmt.Errorf("%w: collection %s: the document %s would be written into %s, whose name differs only in case and is not one the mirror writes: this filesystem keeps them as one, so it is not written; copy into a folder on a case-sensitive filesystem%s, or move that file away", ErrCaseFold, spec.ID, shown(strings.Join(doc, "/")), shown(shownFrom), caseSensitiveHint)
	}
	if oldID != newID {
		oldDoc := strings.Join(append(doc[:len(doc)-1:len(doc)-1], oldID), "/")
		gone, err := r.goneFromSource(oldDoc, spec)
		if err != nil {
			return false, err
		}
		if !gone {
			return false, fmt.Errorf("%w: collection %s: the ids %q (document %s) and %q (document %s, in the output as %s) differ only in case, and both are in the source: this filesystem keeps them as one file or folder, so neither is written over; copy into a folder on a case-sensitive filesystem%s, or give one of them another id", ErrCaseFold, spec.ID, shown(newID), shown(strings.Join(doc, "/")), shown(oldID), shown(oldDoc), shown(shownFrom), caseSensitiveHint)
		}
	}
	random := make([]byte, 8)
	_, _ = rand.Read(random) // never fails
	temp := caseRenamePrefix + hex.EncodeToString(random)
	if len(temp)+1+len(have) <= maxNameBytes {
		temp += "-" + have // so an interrupted rename can be put back
	}
	from, to := filepath.Join(dir, have), filepath.Join(dir, want)
	temp = filepath.Join(dir, temp)
	if err := renameFile(from, temp); err != nil {
		return false, fmt.Errorf("rename %s to %s: %w", from, want, err)
	}
	if err := renameFile(temp, to); err != nil {
		return false, firstError(renameFile(temp, from), fmt.Errorf("rename %s to %s: %w", from, want, err))
	}
	names[lower] = want
	shownTo, _ := filepath.Rel(r.opts.Output, to)
	r.report.Renamed = append(r.report.Renamed, shownFrom+" -> "+filepath.ToSlash(shownTo))
	return true, nil
}

// goneFromSource reports whether the source proves that the document at path
// (of spec) does not exist, and that no document of a defined collection below
// spec is below it: Firestore keeps the sub-collections of a deleted document.
// It costs one read, and one per such collection, from the read budget
// (errBudget when that would pass it); the answer is kept.
func (r *run) goneFromSource(path string, spec *Spec) (bool, error) {
	if gone, ok := r.gone[path]; ok {
		return gone, nil
	}
	var below []*Spec
	for _, other := range r.defs.Specs {
		if strings.HasPrefix(other.ID, spec.ID+"/") {
			below = append(below, other)
		}
	}
	if need := 1 + len(below); r.budget(need) < need {
		return false, errBudget
	}
	doc, err := r.src.Get(r.ctx, path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	r.report.Reads++
	gone := doc == nil
	for i := 0; gone && i < len(below); i++ { // until one is found
		sub := below[i]
		docs, err := r.src.Page(r.ctx, Query{Collection: sub.Leaf(), Group: true, Under: path, Limit: 1})
		if err != nil {
			return false, fmt.Errorf("read collection group %s under %s: %w", sub.Leaf(), path, err)
		}
		r.report.Reads++
		gone = len(docs) == 0
	}
	r.gone[path] = gone
	return gone, nil
}

// noteRead records the record file (relative to the output) of a document
// this run read, written or rejected, so a prune never deletes it under a
// name that differs only in case.
func (r *run) noteRead(file string) {
	if _, ok := r.owned[strings.ToLower(file)]; !ok {
		r.owned[strings.ToLower(file)] = file
	}
}

// noteWritten records a file this run wrote (relative to the output): in the
// names of the folders already read, and as a file a prune never deletes
// under a name that differs only in case.
func (r *run) noteWritten(file string) {
	r.noteRead(file)
	dir := r.opts.Output
	for _, name := range strings.Split(filepath.ToSlash(file), "/") {
		if names, ok := r.names[dir]; ok {
			if _, known := names[strings.ToLower(name)]; !known {
				names[strings.ToLower(name)] = name
			}
		}
		dir = filepath.Join(dir, name)
	}
}

// isOwned reports whether file (relative to the output) is the record file of
// a document this run read, under a name that differs only in case from it on
// a filesystem that folds case. Deleting it would delete that record.
func (r *run) isOwned(file string) bool {
	owned, ok := r.owned[strings.ToLower(file)]
	return ok && owned != file && sameFile(filepath.Join(r.opts.Output, file), filepath.Join(r.opts.Output, owned))
}
