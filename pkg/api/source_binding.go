package api

import (
	"context"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
)

func SourceBoundProfile() bool {
	secureMu.RLock()
	defer secureMu.RUnlock()
	return secureSession.SourceBound()
}

func CheckSourceBinding(ctx context.Context) error {
	secureMu.RLock()
	session := secureSession
	secureMu.RUnlock()
	return session.CheckSourceBinding(ctx)
}

func BoundReadScope(ctx context.Context) (project, environment, catalog string, bound bool, err error) {
	secureMu.RLock()
	session := secureSession
	secureMu.RUnlock()
	project, environment, catalog, bound = session.BoundScope()
	if bound {
		err = session.CheckSourceBinding(ctx)
	}
	return
}

func BoundSourceBackend(ctx context.Context, sourceURL string) (dbcopy.BackendRef, bool, error) {
	secureMu.RLock()
	session := secureSession
	secureMu.RUnlock()
	return session.BoundBackend(ctx, sourceURL)
}

func boundResolvedSource(ctx context.Context, store datatug.ProjectStore, root, environment, requested string) (ResolvedSource, bool, error) {
	secureMu.RLock()
	session := secureSession
	secureMu.RUnlock()
	if !session.SourceBound() {
		return ResolvedSource{}, false, nil
	}
	if store == nil {
		return ResolvedSource{}, true, secureread.ErrSourceBinding
	}
	url, _, err := session.BoundSource(ctx, store.ProjectID(), root, environment, requested)
	if err != nil {
		return ResolvedSource{}, true, err
	}
	_, _, catalog, _ := session.BoundScope()
	return ResolvedSource{ID: catalog, Label: catalog, Kind: SourceKindSQL, URL: url}, true, nil
}
