package commands

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/ingitdb/ingitdb-go/ingitdb"
	"github.com/ingitdb/ingitdb-go/ingitdb/validator"
)

var readInGitDBDefinition = validator.ReadDefinition

// nestedFromRef is the collection a `--from` path names in a source whose
// collections nest under documents (Firestore, inGitDB): for
// ext/billing/payments the collection payments of the document
// ext/billing. ok is false when --from keeps the meaning it always had (one
// name, read as given).
//
// Without --from-path (explicit false) only a Firestore --from of that shape
// is read as nested: Firestore refused the whole path as one collection name,
// so nothing that worked changes. Every other --from is read as before.
//
// With --from-path, an inGitDB --from is read as nested too, and a --from
// that is not collection/document/collection..., an inGitDB path whose
// collections are not defined, or another source, is a usage error.
func nestedFromRef(from string, backend dbcopy.BackendRef, explicit bool) (ref dal.CollectionRef, ok bool, err error) {
	parts := strings.Split(from, "/")
	shaped := len(parts)%2 == 1 && !slices.Contains(parts, "")
	if !explicit {
		if backend.Scheme != "firestore" || len(parts) == 1 || !shaped {
			return ref, false, nil
		}
		return nestedRef(parts), true, nil
	}
	if backend.Scheme != "firestore" && backend.Scheme != "ingitdb" {
		return ref, false, Exit(fmt.Sprintf("--from-path reads a nested collection of a Firestore or inGitDB source, not of %s", backend.Scheme), exitCodeUsage)
	}
	if !shaped {
		return ref, false, Exit(fmt.Sprintf("--from %q is not a collection path: name a collection as collection/document/collection (for example ext/billing/payments)", from), exitCodeUsage)
	}
	if backend.Scheme == "ingitdb" {
		if err := inGitDBCollectionDefined(backend.Path, parts); err != nil {
			return ref, false, err
		}
	}
	return nestedRef(parts), true, nil
}

// nestedRef is the collection of parts (collection, document, ..., collection).
func nestedRef(parts []string) dal.CollectionRef {
	var parent *record.Key
	for i := 0; i+1 < len(parts); i += 2 {
		parent = record.NewKeyWithParentAndID(parent, parts[i], parts[i+1])
	}
	return dal.NewCollectionRef(parts[len(parts)-1], "", parent)
}

// inGitDBCollectionDefined fails unless the collections of a --from path
// (parts: collection, document, collection, ...) are defined in the inGitDB
// database at dir, naming the collections that are defined where it stops.
func inGitDBCollectionDefined(dir string, parts []string) error {
	def, err := readInGitDBDefinition(dir)
	if err != nil {
		return Exit(fmt.Sprintf("--db: read the inGitDB definitions: %v", err), exitCodeUsage)
	}
	collections := def.Collections
	for i := 0; i < len(parts); i += 2 {
		col, found := collections[parts[i]]
		if !found {
			defined := "no collection is defined there"
			if len(collections) > 0 {
				defined = "defined there: " + strings.Join(slices.Sorted(maps.Keys(collections)), ", ")
			}
			where := "at the root"
			if i > 0 {
				where = "under " + strings.Join(collectionIDsOf(parts[:i]), "/")
			}
			return Exit(fmt.Sprintf("--from %q: collection %q is not defined in the inGitDB database %s (%s)", strings.Join(parts, "/"), parts[i], where, defined), exitCodeUsage)
		}
		collections = col.SubCollections
		if collections == nil {
			collections = map[string]*ingitdb.CollectionDef{}
		}
	}
	return nil
}

func collectionIDsOf(parts []string) []string {
	var ids []string
	for i := 0; i < len(parts); i += 2 {
		ids = append(ids, parts[i])
	}
	return ids
}
