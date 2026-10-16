package api

import (
	"context"
	"path/filepath"

	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/sessionstore"
	core "github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
)

// SessionDetectiveOperationScope is a conflict-checking locator, never a grant.
// Storage/Project name the original source selector; the private IDs are derived
// from the mounted local authority, not client-selected filesystem paths.
type SessionDetectiveOperationScope struct {
	Storage     string `json:"storage"`
	Project     string `json:"project"`
	SpaceID     string `json:"spaceID,omitempty"`
	DatabaseID  string `json:"databaseId"`
	ProjectID   string `json:"projectId"`
	NamespaceID string `json:"namespaceId"`
}

// SessionDetectiveInputSelection selects data only; the server derives provenance.
type SessionDetectiveInputSelection struct {
	InputName  string `json:"inputName"`
	EntryID    string `json:"entryId"`
	RevisionID string `json:"revisionId"`
}

type SessionDetectiveRequest struct {
	OperationID          string                           `json:"operationId"`
	SessionID            string                           `json:"sessionId"`
	ExpectedVersion      int64                            `json:"expectedVersion"`
	ExpectedContextSetID string                           `json:"expectedContextSetId"`
	CheckID              string                           `json:"checkId"`
	CaseID               string                           `json:"caseId,omitempty"`
	Scope                sessioncontext.Scope             `json:"scope"`
	Attachments          []sessioncontext.EntryRef        `json:"attachments,omitempty"`
	Bindings             []SessionDetectiveInputSelection `json:"bindings,omitempty"`
	Inputs               map[string]core.InputValue       `json:"inputs,omitempty"`
}

type SessionDetectiveResponse struct {
	OperationID    string                                 `json:"operationId"`
	Status         sessioncontext.AcceptedDetectiveStatus `json:"status"`
	OperationScope SessionDetectiveOperationScope         `json:"operationScope"`
}

var sessionResolvePrivateDir = executionstore.ResolvePrivateDir

func localSessionRepository(project, dir string) (*sessionstore.NativeRepository, error) {
	root, err := sessionResolvePrivateDir(GetCapabilities().EvidencePrivateDir)
	if err != nil {
		return nil, sessioncontext.ErrUnavailable
	}
	contextManager.Lock()
	if contextManager.manager != nil && contextManager.privateBase != root {
		contextManager.Unlock()
		return nil, sessioncontext.ErrForbidden
	}
	if contextManager.manager == nil {
		contextManager.manager, err = sessionstoreNew(filepath.Join(root, "session-context"))
		if err == nil {
			contextManager.privateBase = root
		}
	}
	manager := contextManager.manager
	contextManager.Unlock()
	if err != nil || manager == nil {
		return nil, sessioncontext.ErrUnavailable
	}
	repo, err := manager.NativeRepository(project, dir, root)
	if err != nil {
		return nil, sessioncontext.ErrUnavailable
	}
	return repo, nil
}

// ResolveAcceptedSessionContext omits source environment/catalog reads only.
// The HTTP route must still enforce the existing origin, agent token/client
// grant, runtime caller authentication and write capability wrappers BEFORE
// calling it. A fixed runtime Principal is not a grant to arbitrary requests.
func ResolveAcceptedSessionContext(ctx context.Context, storage, project string) (*sessioncontext.Service, sessioncontext.Principal, *sessionstore.NativeRepository, error) {
	if _, err := ResolveStoreID(storage, project); err != nil {
		return nil, sessioncontext.Principal{}, nil, sessioncontext.ErrForbidden
	}
	actor := SecurePrincipalID()
	dir, ok := sessionContextProjectDir(project)
	if actor == "" || !ok {
		return nil, sessioncontext.Principal{}, nil, sessioncontext.ErrForbidden
	}
	canonical, err := filepathEvalSymlinks(dir)
	if err != nil {
		return nil, sessioncontext.Principal{}, nil, sessioncontext.ErrUnavailable
	}
	repo, err := localSessionRepository(project, dir)
	if err != nil {
		return nil, sessioncontext.Principal{}, nil, err
	}
	principal := sessioncontext.Principal{Actor: actor, DatabaseID: repo.DatabaseID(), ProjectID: project}
	service, err := sessioncontext.NewNativePinService(repo, func(_ context.Context, access sessioncontext.Access) error {
		if access.Principal.Actor != SecurePrincipalID() || access.Principal.DatabaseID != principal.DatabaseID || access.Principal.ProjectID != project || !GetCapabilities().AllowWrites || !SecureSessionUnrestricted() {
			return sessioncontext.ErrForbidden
		}
		current, ok := sessionContextProjectDir(project)
		if !ok {
			return sessioncontext.ErrForbidden
		}
		current, err := filepathEvalSymlinks(current)
		if err != nil || current != canonical {
			return sessioncontext.ErrForbidden
		}
		return nil
	})
	return service, principal, repo, err
}

// AcceptedSessionActorGate supplements authenticated route admission. It does
// not replace origin/client grants, and deliberately performs no source reads.
func AcceptedSessionActorGate(ctx context.Context, p sessioncontext.Principal, id sessioncontext.DetectiveIdentity) error {
	if p.Actor == "" || p.Actor != SecurePrincipalID() || id.Actor != p.Actor || id.Namespace.DatabaseID != p.DatabaseID || id.Namespace.ProjectID != p.ProjectID {
		return sessioncontext.ErrForbidden
	}
	return nil
}
