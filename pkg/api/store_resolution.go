package api

import (
	"errors"
	"fmt"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/strongo/validation"
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
		// explicit and projectID are what a client sent: only a plain name is quoted.
		return "", fmt.Errorf("%w: %q is not configured for project %q", ErrUnknownStoreID, dbcopy.SourceIDDisplay(explicit), dbcopy.SourceIDDisplay(projectID))
	}
	switch len(configured) {
	case 1:
		return configured[0], nil
	case 0:
		return "", errNoStoreConfigured(projectID)
	default:
		return "", fmt.Errorf("%w: project %q", ErrAmbiguousStore, dbcopy.SourceIDDisplay(projectID))
	}
}

// errNoStoreConfigured is the answer for a project that nothing serves (see
// ResolveStoreID), shown with the project's ID only when it is a plain name.
func errNoStoreConfigured(projectID string) error {
	return fmt.Errorf("%w: no store configured for project %q", ErrUnknownStoreID, dbcopy.SourceIDDisplay(projectID))
}

// servedProjectDir returns the directory of a project this process serves, and for any
// other project the answer of a route that resolves its store (see ResolveStoreID).
// An entry that takes the project from the request itself, and so is not reached
// through such a route, asks it before it asks for a project store: a project the
// process does not serve has no folder to open, and its ID is not turned into one.
func servedProjectDir(projectID string) (string, error) {
	dir, served := projectDir(projectID)
	if !served {
		return "", errNoStoreConfigured(projectID)
	}
	return dir, nil
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
//
// It is the one place an entry gets the store of a served project from (ProjectStoreFor and
// every entry call it, and a test fails on a call of GetProjectStore anywhere else); the scan
// opens the folder it is given and builds its own store (see
// TestFileStoreConstructors_AreCalledByTheScanOnly). So the rule about which projects there
// is a store for is kept here, once, and not in each entry: the
// project's ID must be a served project or a plain name (see ValidateProjectIdentifier), and
// when a session is configured (see sessionConfigured) it must be a project that this process
// serves. A plain name that is not served has no project folder, so no store is handed out for
// it: the answer is the one of the routes that resolve the store of their project (see
// ResolveStoreID), as a bad request. With no session (a handler under test, or a command that
// is not serve) any plain name is handed a store.
func projectStoreForID(storeID, projectID string) (datatug.ProjectStore, error) {
	if err := ValidateProjectIdentifier("project", projectID); err != nil {
		return nil, err
	}
	if sessionConfigured() {
		if _, err := servedProjectDir(projectID); err != nil {
			return nil, projectNotServed{err}
		}
	}
	store, err := storeFor(storeID)
	if err != nil {
		return nil, err
	}
	return store.GetProjectStore(projectID), nil
}

// projectNotServed is the refusal of a project that this process does not serve: the answer
// of a route that resolves its store (an ErrUnknownStoreID, which the legacy routes answer
// as a 400 of their own) that is also a bad request, which is the only error that the routes
// answered by apicore give a status of 400 to.
type projectNotServed struct{ refusal error }

func (e projectNotServed) Error() string { return e.refusal.Error() }

// Unwrap lets errors.Is find both: the refusal, and a bad request that says the same.
func (e projectNotServed) Unwrap() []error {
	return []error{e.refusal, validation.NewBadRequestError(e.refusal)}
}
