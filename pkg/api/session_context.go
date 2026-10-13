package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"sync"

	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/sessionstore"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
)

type SessionContextEnvironment struct {
	EnvironmentID  string `json:"environmentId"`
	IdentityDomain string `json:"identityDomain"`
	Title          string `json:"title,omitempty"`
}
type SessionContextCapabilities struct {
	ActorID       string                      `json:"actorId"`
	SchemaVersion int                         `json:"schemaVersion"`
	DatabaseID    string                      `json:"databaseId"`
	ProjectID     string                      `json:"projectId"`
	Environments  []SessionContextEnvironment `json:"environments"`
	Actions       []string                    `json:"actions"`
	Subjects      []SessionContextSubject     `json:"subjects,omitempty"`
}

var contextManager struct {
	sync.Mutex
	manager *sessionstore.Manager
}

func CloseSessionContext() error {
	contextManager.Lock()
	defer contextManager.Unlock()
	if contextManager.manager == nil {
		return nil
	}
	err := contextManager.manager.Close()
	contextManager.manager = nil
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
	dir, ok := ProjectDir(project)
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
	identityDir, pathErr := filepath.EvalSymlinks(dir)
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
	contextManager.Lock()
	if contextManager.manager == nil {
		var root string
		root, err = executionstore.ResolvePrivateDir(GetCapabilities().EvidencePrivateDir)
		if err == nil {
			contextManager.manager, err = sessionstore.New(filepath.Join(root, "session-context"))
		}
	}
	manager := contextManager.manager
	contextManager.Unlock()
	if err != nil || manager == nil {
		return nil, sessioncontext.Principal{}, caps, sessioncontext.ErrUnavailable
	}
	repo, err := manager.Repository(project, dir)
	if err != nil {
		return nil, sessioncontext.Principal{}, caps, sessioncontext.ErrUnavailable
	}
	principal := sessioncontext.Principal{Actor: actor, DatabaseID: repo.DatabaseID(), ProjectID: project}
	service, err := sessioncontext.NewService(repo, func(_ context.Context, access sessioncontext.Access) error {
		if access.Principal.Actor != SecurePrincipalID() || access.Principal.DatabaseID != principal.DatabaseID || access.Principal.ProjectID != project || access.Scope != nil && !admitted[*access.Scope] {
			return sessioncontext.ErrForbidden
		}
		if _, ok := ProjectDir(project); !ok {
			return sessioncontext.ErrForbidden
		}
		return nil
	})
	caps.SchemaVersion, caps.DatabaseID, caps.ProjectID, caps.ActorID = 1, principal.DatabaseID, project, principal.Actor
	caps.Subjects = localSessionSampleSubjects(dir, sampleEnvironments)
	caps.Actions = []string{}
	if GetCapabilities().AllowWrites {
		caps.Actions = []string{"createSession", "createBag", "putEntry", "applyContext", "clearBag", "transitionSession", "pinInput", "reportDashboard", "reportClientRun", "recordRunExecution", "renameSession", "renameBag", "importLegacy"}
	}
	return service, principal, caps, err
}
