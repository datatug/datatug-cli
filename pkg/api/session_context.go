package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"sync"

	"github.com/datatug/datatug-cli/pkg/sessionstore"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
)

// SessionContextQuerySource is an admitted identity for UI scope routing, not
// a source grant. Execution still performs its ordinary current policy checks.
type SessionContextQuerySource struct {
	Kind            string `json:"kind"`
	ConnectionID    string `json:"connectionId,omitempty"`
	DatabaseID      string `json:"databaseId"`
	FixtureSHA256   string `json:"fixtureSha256"`
	Origin          string `json:"origin,omitempty"`
	CatalogRevision string `json:"catalogRevision,omitempty"`
}

type SessionContextEnvironment struct {
	EnvironmentID  string                      `json:"environmentId"`
	IdentityDomain string                      `json:"identityDomain"`
	Title          string                      `json:"title,omitempty"`
	QuerySources   []SessionContextQuerySource `json:"querySources,omitempty"`
}
type SessionContextCapabilities struct {
	ActorID         string                          `json:"actorId"`
	SchemaVersion   int                             `json:"schemaVersion"`
	DatabaseID      string                          `json:"databaseId"`
	ProjectID       string                          `json:"projectId"`
	Environments    []SessionContextEnvironment     `json:"environments"`
	Actions         []string                        `json:"actions"`
	OperationScope  *SessionDetectiveOperationScope `json:"operationScope,omitempty"`
	NativeDetective bool                            `json:"nativeDetective"`
	Subjects        []SessionContextSubject         `json:"subjects,omitempty"`
}

var contextManager struct {
	sync.Mutex
	manager     *sessionstore.Manager
	privateBase string
}

var (
	filepathEvalSymlinks     = filepath.EvalSymlinks
	sessionstoreNew          = sessionstore.New
	sessionContextProjectDir = ProjectDir
)

func CloseSessionContext() error {
	contextManager.Lock()
	defer contextManager.Unlock()
	if contextManager.manager == nil {
		return nil
	}
	err := contextManager.manager.Close()
	contextManager.manager = nil
	contextManager.privateBase = ""
	return err
}

// ResolveSessionContext binds source metadata and the process's attested actor
// for every request. Reading working context never dials the source database.
func ResolveSessionContext(ctx context.Context, storage, project string) (*sessioncontext.Service, sessioncontext.Principal, SessionContextCapabilities, error) {
	var caps SessionContextCapabilities
	if _, err := ResolveStoreID(storage, project); err != nil {
		return nil, sessioncontext.Principal{}, caps, sessioncontext.ErrForbidden
	}
	actor := SecurePrincipalID()
	if actor == "" {
		return nil, sessioncontext.Principal{}, caps, sessioncontext.ErrForbidden
	}
	dir, ok := sessionContextProjectDir(project)
	if !ok {
		return nil, sessioncontext.Principal{}, caps, sessioncontext.ErrForbidden
	}
	store, err := ProjectStoreFor(project)
	if err != nil {
		return nil, sessioncontext.Principal{}, caps, sessioncontext.ErrUnavailable
	}
	environments, err := store.LoadEnvironments(ctx)
	if err != nil {
		return nil, sessioncontext.Principal{}, caps, sessioncontext.ErrUnavailable
	}
	identityDir, pathErr := filepathEvalSymlinks(dir)
	if pathErr != nil {
		return nil, sessioncontext.Principal{}, caps, sessioncontext.ErrUnavailable
	}
	sampleEnvironments := []SessionContextEnvironment{}
	admitted := make(map[sessioncontext.Scope]bool)
	caps.Environments = []SessionContextEnvironment{}
	for _, env := range environments {
		if env == nil {
			continue
		}
		digest := sha256.Sum256([]byte("local\x00" + project + "\x00" + identityDir + "\x00" + env.ID))
		scope := sessioncontext.Scope{EnvironmentID: env.ID, IdentityDomain: "local-" + hex.EncodeToString(digest[:16])}
		admitted[scope] = true
		caps.Environments = append(caps.Environments, SessionContextEnvironment{EnvironmentID: env.ID, IdentityDomain: scope.IdentityDomain, Title: env.Title})
		sample := false
		for _, server := range env.DbServers {
			if server == nil || (server.Driver != "sqlite3" && server.Driver != "sqlite") {
				continue
			}
			for _, catalog := range server.Catalogs {
				if catalog == "chinook" || catalog == "chinook-local" {
					sample = true
				}
			}
		}
		if sample {
			sampleEnvironments = append(sampleEnvironments, caps.Environments[len(caps.Environments)-1])
		}

	}
	repo, err := localSessionRepository(project, dir)
	if err != nil {
		return nil, sessioncontext.Principal{}, caps, err
	}
	principal := sessioncontext.Principal{Actor: actor, DatabaseID: repo.DatabaseID(), ProjectID: project}
	service, err := sessioncontext.NewNativePinService(repo, func(_ context.Context, access sessioncontext.Access) error {
		if access.Principal.Actor != SecurePrincipalID() || access.Principal.DatabaseID != principal.DatabaseID || access.Principal.ProjectID != project || access.Scope != nil && !admitted[*access.Scope] {
			return sessioncontext.ErrForbidden
		}
		if _, ok := ProjectDir(project); !ok {
			return sessioncontext.ErrForbidden
		}
		return nil
	})
	caps.SchemaVersion, caps.DatabaseID, caps.ProjectID, caps.ActorID = 1, principal.DatabaseID, project, principal.Actor
	n := repo.NativeNamespace()
	sourceStorage := storage
	if sourceStorage == "" {
		sourceStorage = "local"
	}
	caps.OperationScope = &SessionDetectiveOperationScope{Storage: sourceStorage, Project: project, DatabaseID: n.DatabaseID, ProjectID: n.ProjectID, NamespaceID: n.NamespaceID}
	caps.NativeDetective = GetCapabilities().NativeSessionDetective
	caps.Subjects = localSessionSampleSubjects(dir, sampleEnvironments)
	for i := range caps.Environments {
		for _, subject := range caps.Subjects {
			if subject.Scope.EnvironmentID == caps.Environments[i].EnvironmentID && subject.Scope.IdentityDomain == caps.Environments[i].IdentityDomain {
				caps.Environments[i].QuerySources = []SessionContextQuerySource{{Kind: "public-sqlite", ConnectionID: "chinook-sqlite", DatabaseID: "chinook", FixtureSHA256: "7651ba378ac2fcd0dfc3c66fb101f7a7eed3ba39a612ec642b96e20702061f15"}}
				break
			}
		}
	}
	caps.Actions = []string{}
	if GetCapabilities().AllowWrites {
		caps.Actions = []string{"createSession", "createBag", "putEntry", "applyContext", "clearBag", "transitionSession", "pinInput", "reportDashboard", "reportClientRun", "recordRunExecution", "renameSession", "renameBag", "importLegacy"}
	}
	return service, principal, caps, err
}
