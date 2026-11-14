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
// ext/billing. ok is false for any other source, and for a --from with no
// "/", which keep their meaning (a table or a root collection by name).
//
// A path that is not collection/document/collection... is a usage error, and
// so is an inGitDB path whose collections are not defined: before, both read
// as an empty result.
func nestedFromRef(from string, backend dbcopy.BackendRef) (ref dal.CollectionRef, ok bool, err error) {
	if !strings.Contains(from, "/") || (backend.Scheme != "firestore" && backend.Scheme != "ingitdb") {
		return ref, false, nil
	}
	parts := strings.Split(from, "/")
	if len(parts)%2 == 0 || slices.Contains(parts, "") {
		return ref, false, Exit(fmt.Sprintf("--from %q is not a collection path: name a collection as collection/document/collection (for example ext/billing/payments)", from), exitCodeUsage)
	}
	if backend.Scheme == "ingitdb" {
		if err := inGitDBCollectionDefined(backend.Path, parts); err != nil {
			return ref, false, err
		}
	}
	var parent *record.Key
	for i := 0; i+1 < len(parts); i += 2 {
		parent = record.NewKeyWithParentAndID(parent, parts[i], parts[i+1])
	}
	return dal.NewCollectionRef(parts[len(parts)-1], "", parent), true, nil
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
