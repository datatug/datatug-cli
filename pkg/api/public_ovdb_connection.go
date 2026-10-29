package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

func canCreatePublicOVDBConnection() bool {
	secureMu.RLock()
	defer secureMu.RUnlock()
	return securityContextID != "" && secureSession.Principal != nil && secureSession.Principal.ID != nil && secureSession.Unrestricted && capabilities.AllowWrites
}

var readPublicConnectionState = (*filestore.EnvironmentFiles).PublicOVDBConnectionState

func publicConnectionError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	switch {
	case errors.Is(err, datatug.ErrProjectLifecycleInvalid):
		return environmentError("INVALID_REQUEST", 400)
	case errors.Is(err, datatug.ErrAmbiguousConnection):
		return environmentError("CONNECTION_ALREADY_EXISTS", 409)
	case errors.Is(err, datatug.ErrProjectLifecycleConflict):
		return environmentError("CONNECTION_REVISION_CONFLICT", 409)
	case errors.Is(err, os.ErrNotExist):
		return environmentError("DATABASE_OR_ENVIRONMENT_NOT_FOUND", 404)
	default:
		return environmentError("CONNECTION_UNAVAILABLE", 503)
	}
}

func publicConnectionState(scope dto.EnvironmentScope, binding, fence string, snapshot filestore.PublicOVDBConnectionSnapshot) *dto.PublicOVDBConnectionState {
	state := &dto.PublicOVDBConnectionState{Contract: "public-ovdb-connection-state/1", Scope: scope, ScopeBinding: binding,
		Revision:      "connection-revision/1." + envDigest([]byte(binding+":"+fence+":"+snapshot.Revision)),
		EnvironmentID: snapshot.EnvironmentID, DatabaseID: snapshot.DatabaseID, ConnectionID: snapshot.ConnectionID,
		CanAdd: canCreatePublicOVDBConnection() && !snapshot.Exists}
	if snapshot.Exists {
		state.Reason = "This connection ID is already declared; existing connections are preserved."
	} else if !state.CanAdd {
		state.Reason = "A writable native operator session is required."
	}
	return state
}

func LocalPublicOVDBConnectionState(ctx context.Context, scope dto.EnvironmentScope, environmentID, databaseID string) (result *dto.PublicOVDBConnectionState, err error) {
	err = withLocalEnvironment(ctx, scope, false, func(f *filestore.EnvironmentFiles, resolved dto.EnvironmentScope, binding string, _ dto.EnvironmentCapabilities, fence string) error {
		snapshot, err := readPublicConnectionState(f, ctx, environmentID, databaseID)
		if err != nil {
			return publicConnectionError(err)
		}
		result = publicConnectionState(resolved, binding, fence, snapshot)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

var publicConnectionCommit = func(ctx context.Context, f *filestore.EnvironmentFiles, snapshot filestore.PublicOVDBConnectionSnapshot, binding string, result *dto.EnvironmentOperationResult, authorize func() error) error {
	return f.CommitPublicOVDBConnectionGuarded(ctx, snapshot, binding, result, authorize)
}

// LocalCreatePublicOVDBConnection accepts references only. The owning project
// supplies the anonymous OVDB binding; no network connection is opened here.
func LocalCreatePublicOVDBConnection(ctx context.Context, request dto.CreatePublicOVDBConnectionRequest) (result *dto.EnvironmentOperationResult, err error) {
	if !canCreatePublicOVDBConnection() {
		return nil, environmentError("ACCESS_DENIED", 403)
	}
	if !datatug.ValidConnectionID(request.EnvironmentID) || !datatug.ValidConnectionID(request.DatabaseID) {
		return nil, environmentError("INVALID_REQUEST", 400)
	}
	generation := SecurityContextID()
	err = withLocalEnvironment(ctx, request.Scope, true, func(f *filestore.EnvironmentFiles, resolved dto.EnvironmentScope, binding string, _ dto.EnvironmentCapabilities, fence string) error {
		if !canCreatePublicOVDBConnection() || !ValidateSecurityContext(generation) {
			return environmentError("ACCESS_DENIED", 403)
		}
		if !environmentOperationPattern.MatchString(request.OperationID) || !strings.HasPrefix(request.OperationID, binding+".") {
			return environmentError("OPERATION_CONFLICT", 409)
		}
		payload, _ := json.Marshal(struct {
			Kind                                       string
			Scope                                      dto.EnvironmentScope
			Revision, Operation, Environment, Database string
		}{"create-public-ovdb-connection", resolved, request.IfRevision, request.OperationID, request.EnvironmentID, request.DatabaseID})
		digest := envDigest(payload)
		old, err := f.Operation(request.OperationID, binding)
		if err == nil {
			if old.Kind != "create-public-ovdb-connection" || old.PayloadDigest != digest {
				return environmentError("OPERATION_CONFLICT", 409)
			}
			until, err := time.Parse(time.RFC3339Nano, old.ReplayUntil)
			if err != nil || !environmentNow().Before(until) {
				return environmentError("OPERATION_EXPIRED", 410)
			}
			result = old
			return nil
		}
		if !os.IsNotExist(err) {
			return publicConnectionError(err)
		}
		snapshot, err := readPublicConnectionState(f, ctx, request.EnvironmentID, request.DatabaseID)
		if err != nil {
			return publicConnectionError(err)
		}
		state := publicConnectionState(resolved, binding, fence, snapshot)
		if state.Revision != request.IfRevision {
			return environmentError("CONNECTION_REVISION_CONFLICT", 409)
		}
		if snapshot.Exists {
			return environmentError("CONNECTION_ALREADY_EXISTS", 409)
		}
		authorize := func() error {
			if !ValidateSecurityContext(generation) || !canCreatePublicOVDBConnection() {
				return environmentError("ACCESS_DENIED", 403)
			}
			if fence != "non-git" {
				current, err := environmentGitState(ctx, f.Root)
				if err != nil || current.branch+":"+current.head != fence {
					return environmentError("CONNECTION_REVISION_CONFLICT", 409)
				}
			}
			return nil
		}
		if err := authorize(); err != nil {
			return err
		}
		now := environmentNow().UTC()
		result = &dto.EnvironmentOperationResult{OperationID: request.OperationID, Kind: "create-public-ovdb-connection", PayloadDigest: digest, EnvironmentID: snapshot.EnvironmentID, DatabaseID: snapshot.DatabaseID, ConnectionID: snapshot.ConnectionID, AcceptedAt: now.Format(time.RFC3339Nano), ReplayUntil: now.Add(7 * 24 * time.Hour).Format(time.RFC3339Nano), Status: "committed"}
		if err = publicConnectionCommit(ctx, f, snapshot, binding, result, authorize); err != nil {
			return environmentError("OUTCOME_UNCERTAIN", 409)
		}
		confirmed, err := readPublicConnectionState(f, ctx, request.EnvironmentID, request.DatabaseID)
		if err != nil || !confirmed.Exists || confirmed.Revision != result.AfterStateDigest {
			return environmentError("OUTCOME_UNCERTAIN", 409)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
