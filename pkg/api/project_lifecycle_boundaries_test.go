package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
)

type lifecycleBoundaryProvider struct {
	*lifecycleTestStore
	review  func() (datatug.ProjectLifecycleReview, error)
	catalog func() (datatug.ProjectLifecycleCatalog, error)
}

func TestLocalLifecycleScopeFailureAndConfirmedWrite(t *testing.T) {
	selection, provider := lifecycleAPISetup(t, true)
	ack, err := LocalProjectLifecycleChange(context.Background(), lifecycleWrite(selection))
	if err != nil || ack.OperationID != "operation-1" || provider.writes != 1 {
		t.Fatal("confirmed provider acknowledgement lost", ack, err)
	}
	previous := lifecycleMutationScope
	t.Cleanup(func() { lifecycleMutationScope = previous })
	lifecycleMutationScope = func(dto.ProjectRef, string, string) (dto.OperationScope, error) {
		return dto.OperationScope{}, errors.New("authority changed")
	}
	if out, err := LocalProjectLifecycleReview(context.Background(), selection, datatug.ProjectObjectRef{Kind: "query", ID: "q"}); err == nil || out.Revision != "" {
		t.Fatal("failed scope authority admitted review")
	}
}

func (s lifecycleBoundaryProvider) ReviewProjectLifecycle(ctx context.Context, scope datatug.ProjectLifecycleScope, ref datatug.ProjectObjectRef) (datatug.ProjectLifecycleReview, error) {
	if s.review != nil {
		return s.review()
	}
	return s.lifecycleTestStore.ReviewProjectLifecycle(ctx, scope, ref)
}
func (s lifecycleBoundaryProvider) ListProjectLifecycle(ctx context.Context, scope datatug.ProjectLifecycleScope, a bool) (datatug.ProjectLifecycleCatalog, error) {
	if s.catalog != nil {
		return s.catalog()
	}
	return s.lifecycleTestStore.ListProjectLifecycle(ctx, scope, a)
}

func TestLocalLifecycleRejectsInvalidAuthorityAndProviderOutput(t *testing.T) {
	ctx := context.Background()
	target := datatug.ProjectObjectRef{Kind: "query", ID: "q"}
	for _, mode := range []string{"no principal", "empty scope", "bad project", "nonlocal", "limited", "unserved", "factory", "bad target", "review error", "review shape", "catalog error", "catalog shape", "catalog valid"} {
		t.Run(mode, func(t *testing.T) {
			selection, base := lifecycleAPISetup(t, true)
			provider := lifecycleBoundaryProvider{lifecycleTestStore: base}
			failure := errors.New("provider failure")
			original := storage.NewDatatugStore
			storage.NewDatatugStore = func(string) (storage.Store, error) {
				if mode == "factory" {
					return nil, failure
				}
				return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore { return provider }}, nil
			}
			defer func() { storage.NewDatatugStore = original }()
			switch mode {
			case "no principal":
				ConfigureSecureSession(secureread.Session{}, nil, Capabilities{})
			case "empty scope":
				selection.ProjectID = ""
			case "bad project":
				selection.ProjectID = "bad/path"
			case "nonlocal":
				selection.StoreID = "other"
			case "limited":
				secureMu.Lock()
				secureSession.Unrestricted = false
				secureMu.Unlock()
			case "unserved":
				selection.ProjectID = "foreign"
			case "bad target":
				target = datatug.ProjectObjectRef{Kind: "unknown", ID: "q"}
				defer func() { target = datatug.ProjectObjectRef{Kind: "query", ID: "q"} }()
			case "review error":
				provider.review = func() (datatug.ProjectLifecycleReview, error) { return datatug.ProjectLifecycleReview{}, failure }
			case "review shape":
				provider.review = func() (datatug.ProjectLifecycleReview, error) {
					return datatug.ProjectLifecycleReview{Revision: strings.Repeat("x", 513)}, nil
				}
			case "catalog error":
				provider.catalog = func() (datatug.ProjectLifecycleCatalog, error) { return datatug.ProjectLifecycleCatalog{}, failure }
			case "catalog shape":
				provider.catalog = func() (datatug.ProjectLifecycleCatalog, error) { return datatug.ProjectLifecycleCatalog{}, nil }
			}
			if strings.HasPrefix(mode, "catalog") {
				out, err := LocalProjectLifecycleCatalog(ctx, selection, false)
				if mode == "catalog valid" {
					if err != nil || out.Revision == "" {
						t.Fatal(err)
					}
					return
				}
				if err == nil || out.Revision != "" {
					t.Fatal("bad catalog exposed metadata", out, err)
				}
				return
			}
			if out, err := LocalProjectLifecycleReview(ctx, selection, target); err == nil || out.Revision != "" {
				t.Fatal("invalid authority/provider review accepted", mode, out, err)
			}
			if mode == "no principal" {
				if _, err := LocalProjectLifecycleCatalog(ctx, selection, false); err == nil {
					t.Fatal("catalog bypassed principal")
				}
			}
		})
	}
}
