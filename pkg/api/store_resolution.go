package api

import (
	"errors"
	"fmt"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
)

// LocalStoreID is the store id a `datatug serve` session's filestore-backed
// project store is addressed by. It is the ONLY store kind a local serve
// session ever configures (ConfigureSecureSession's pathsByID, wired through
// newDatatugStoreFactory in pkg/server/http_server.go) — never "firestore",
// which no `datatug serve` session ever registers a real store for. See
// ResolveStoreID.
const LocalStoreID = "local"

// ErrUnknownStoreID is returned by ResolveStoreID when an explicit ?storage=
// (or storeID DTO field) names a store this process has not configured for
// the given project — including the "firestore" literal
// pkg/server/endpoints/constants.go used to hardcode as a default, which was
// never actually configured in a `datatug serve --project` session (S87).
var ErrUnknownStoreID = errors.New("unknown storage id")

// ErrAmbiguousStore is returned by ResolveStoreID when a project is served
// by more than one configured store and the caller supplied no explicit
// ?storage= to disambiguate. Unreachable with today's serve architecture
// (ConfiguredStoreIDs never returns more than one entry — a single
// `datatug serve` process configures exactly one filestore-backed store for
// the whole union of served projects), kept as an explicit case per the
// brief's own design so a future multi-store serve session fails loudly
// instead of silently picking one.
var ErrAmbiguousStore = errors.New("project is served by more than one store")

// ConfiguredStoreIDs returns the store id(s) this serve session has
// actually configured for projectID: [LocalStoreID] when projectID is one
// of ConfigureSecureSession's served projects, nil otherwise.
func ConfiguredStoreIDs(projectID string) []string {
	if _, ok := projectDir(projectID); ok {
		return []string{LocalStoreID}
	}
	return nil
}

// ResolveStoreID picks the store id a "keep-as-is" GET/mutation route
// should use for projectID, replacing the previous hardcoded "firestore"
// default (pkg/server/endpoints/constants.go) that was simply wrong for a
// `datatug serve --project` session — no Firestore store is ever configured
// there, so every request silently defaulting to it failed downstream with
// "no store configured for id=firestore" (S85's finding).
//
//   - explicit != "": honored only when it names a store actually
//     configured for projectID (ErrUnknownStoreID otherwise) — an explicit
//     ?storage= is never silently overridden or ignored.
//   - explicit == "": defaults to the one configured store; zero configured
//     stores is ErrUnknownStoreID (nothing serves this project — a distinct
//     concern from "project not found", which callers already validate
//     separately); several is ErrAmbiguousStore (see its own doc comment).
func ResolveStoreID(explicit, projectID string) (string, error) {
	return resolveStoreID(explicit, projectID, ConfiguredStoreIDs(projectID))
}

// resolveStoreID is ResolveStoreID's pure decision logic, taking the
// configured store ids as a parameter instead of deriving them from process
// state — factored out so the "several configured stores" branch (today
// unreachable via ConfiguredStoreIDs itself — see ErrAmbiguousStore's own
// doc comment) has a direct, real test, not just a comment asserting it is
// unreachable.
func resolveStoreID(explicit, projectID string, configured []string) (string, error) {
	if explicit != "" {
		for _, id := range configured {
			if id == explicit {
				return explicit, nil
			}
		}
		return "", fmt.Errorf("%w: %q is not configured for project %q", ErrUnknownStoreID, explicit, projectID)
	}
	switch len(configured) {
	case 1:
		return configured[0], nil
	case 0:
		return "", fmt.Errorf("%w: no store configured for project %q", ErrUnknownStoreID, projectID)
	default:
		return "", fmt.Errorf("%w: project %q", ErrAmbiguousStore, projectID)
	}
}

// storeFor resolves storeID via storage.NewDatatugStore — the mechanism
// `datatug serve` actually wires (ServeHTTP's newDatatugStoreFactory) — not
// storage.GetStore/GetProjectStore, which resolve through a package-private
// `stores` map (never populated anywhere in this codebase) or a
// context-stashed Store (storage.ContextWithDatatugStore, never called for
// a served HTTP request either), and so always fail with "no store
// configured for id=..." regardless of the id given — exactly S85's
// reproduced error. GetProjectSummary/GetProjectFull/CreateProject/
// GetProjects/ExecuteSelect/RunQuery already used this mechanism (see
// GetProjectSummary's own comment in project_api.go); storeFor/
// projectStoreForID extend the same established fix to every remaining
// pkg/api/*.go function that still called the broken one (S87).
func storeFor(storeID string) (storage.Store, error) {
	return storage.NewDatatugStore(storeID)
}

// projectStoreForID is storeFor plus the project-store lookup, replacing
// every remaining storage.GetProjectStore(ctx, storeID, projectID) call
// site — see storeFor's doc comment.
func projectStoreForID(storeID, projectID string) (datatug.ProjectStore, error) {
	store, err := storeFor(storeID)
	if err != nil {
		return nil, err
	}
	return store.GetProjectStore(projectID), nil
}
