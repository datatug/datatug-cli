package endpoints

import (
	"context"
	"errors"
	"testing"

	"github.com/datatug/datatug-cli/pkg/projectvisibility"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"github.com/stretchr/testify/require"
)

func restoreVisibilityIO(t *testing.T) {
	t.Helper()
	list, execute, read, store, view, accepted := visibilityListSessions, visibilityExecuteSession, visibilityReadOperation, visibilityIncidentStoreByID, visibilityIncidentView, visibilityAcceptedSessionContext
	t.Cleanup(func() {
		visibilityListSessions, visibilityExecuteSession, visibilityReadOperation, visibilityIncidentStoreByID, visibilityIncidentView = list, execute, read, store, view
		visibilityAcceptedSessionContext = accepted
	})
}

func TestVisibilitySessionInventoryRequiresBoundedAdvancingCompletePages(t *testing.T) {
	runtime, scope, _, _, _, _ := localVisibilityTransportFixture(t)
	restoreVisibilityIO(t)
	failure := errors.New("owning session list unavailable")
	largePage := make([]sessioncontext.Session, 10001)
	for i := range largePage {
		largePage[i].ID = "same-known-session"
	}
	for _, test := range []struct {
		name string
		page sessioncontext.Page[sessioncontext.Session]
		err  error
	}{
		{"storage failure", sessioncontext.Page[sessioncontext.Session]{}, failure},
		{"empty advancing cursor", sessioncontext.Page[sessioncontext.Session]{HasMore: true}, nil},
		{"repeated cursor", sessioncontext.Page[sessioncontext.Session]{HasMore: true, NextCursor: "same"}, nil},
		{"inventory budget", sessioncontext.Page[sessioncontext.Session]{Items: largePage}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			visibilityListSessions = func(_ *sessioncontext.Service, _ context.Context, _ sessioncontext.Principal, cursor string, limit int) (sessioncontext.Page[sessioncontext.Session], error) {
				calls++
				require.Equal(t, 100, limit)
				if calls > 2 {
					t.Fatal("nonadvancing page loop")
				}
				return test.page, test.err
			}
			rows, err := runtime.Inventory(context.Background(), scope)
			require.Error(t, err)
			require.Nil(t, rows)
			if test.err != nil {
				require.ErrorIs(t, err, failure)
			} else {
				require.ErrorIs(t, err, datatug.ErrProjectUsageIncomplete)
			}
		})
	}
}

func TestVisibilitySessionRestoreRequiresAnExactOwningReceipt(t *testing.T) {
	runtime, scope, _, principal, _, _ := localVisibilityTransportFixture(t)
	restoreVisibilityIO(t)
	target := projectvisibility.Target{Item: datatug.ProjectUsageItem{Ref: visibilitySessionRef(t, "s"), Archived: true}, Revision: "4"}
	failure := errors.New("private operation storage unavailable")
	visibilityExecuteSession = func(_ *sessioncontext.Service, _ context.Context, _ sessioncontext.Principal, command sessioncontext.Command) (sessioncontext.CommandResult, error) {
		require.Equal(t, "restore", command.Transition)
		require.EqualValues(t, 4, command.ExpectedVersion)
		return sessioncontext.CommandResult{}, failure
	}
	_, err := runtime.Restore(context.Background(), scope, target, "restore")
	require.ErrorIs(t, err, failure)
	visibilityExecuteSession = func(*sessioncontext.Service, context.Context, sessioncontext.Principal, sessioncontext.Command) (sessioncontext.CommandResult, error) {
		return sessioncontext.CommandResult{}, sessioncontext.ErrConflict
	}
	visibilityReadOperation = func(*sessioncontext.Service, context.Context, sessioncontext.Principal, string) (sessioncontext.Receipt, error) {
		return sessioncontext.Receipt{}, failure
	}
	_, err = runtime.Restore(context.Background(), scope, target, "restore")
	require.ErrorIs(t, err, failure)
	receipt := sessioncontext.Receipt{Actor: principal.Actor, OpID: "restore", Action: "transitionSession", SessionID: "s", SessionVersion: 5}
	for _, field := range []string{"actor", "operation", "action", "session", "version", "valid recovery"} {
		t.Run(field, func(t *testing.T) {
			proof := receipt
			switch field {
			case "actor":
				proof.Actor = "foreign"
			case "operation":
				proof.OpID = "foreign"
			case "action":
				proof.Action = "createSession"
			case "session":
				proof.SessionID = "foreign"
			case "version":
				proof.SessionVersion = 6
			}
			visibilityReadOperation = func(*sessioncontext.Service, context.Context, sessioncontext.Principal, string) (sessioncontext.Receipt, error) {
				return proof, nil
			}
			result, err := runtime.Restore(context.Background(), scope, target, "restore")
			if field == "valid recovery" {
				require.NoError(t, err)
				require.Equal(t, "5", result.Revision)
				require.False(t, result.Item.Archived)
			} else {
				require.ErrorIs(t, err, projectvisibility.ErrRestoreConflict)
				require.Empty(t, result.Revision)
			}
		})
	}
}

type visibilityIncidentStore struct {
	incidents.APIStore
	candidates           []incidents.Incident
	listErr, errorEvents error
}

func (s visibilityIncidentStore) List(context.Context, incidents.CandidateListQuery) ([]incidents.Incident, error) {
	return s.candidates, s.listErr
}
func (s visibilityIncidentStore) Events(context.Context, incidents.IncidentRef, uint64) ([]incidents.Event, error) {
	return []incidents.Event{{}}, s.errorEvents
}

func TestVisibilityIncidentInventoryPropagatesPrivateFailuresAndUsesCanonicalOwner(t *testing.T) {
	runtime, scope, _, _, _, _ := localVisibilityTransportFixture(t)
	restoreVisibilityIO(t)
	failure := errors.New("owning Incident storage unavailable")
	for _, stage := range []string{"route", "list", "events", "view", "canonical owner", "budget"} {
		t.Run(stage, func(t *testing.T) {
			candidates := []incidents.Incident{{Ref: incidents.IncidentRef{StoreID: "p", IncidentID: "INC-1"}}, {Ref: incidents.IncidentRef{StoreID: "p", IncidentID: "INC-2"}}, {Ref: incidents.IncidentRef{StoreID: "p", IncidentID: "INC-3"}}}
			if stage == "budget" {
				candidates = make([]incidents.Incident, 10001)
			}
			port := visibilityIncidentStore{candidates: candidates}
			if stage == "list" {
				port.listErr = failure
			}
			if stage == "events" {
				port.errorEvents = failure
			}
			visibilityIncidentStoreByID = func(project, store string) (incidents.APIStore, error) {
				require.Equal(t, "p", project)
				require.Equal(t, "p", store)
				if stage == "route" {
					return nil, failure
				}
				return port, nil
			}
			visibilityIncidentView = func(_ context.Context, candidate incidents.Incident, _ []incidents.Event) (incidents.IncidentView, incidents.ViewPolicy, error) {
				if stage == "view" {
					return incidents.IncidentView{}, incidents.ViewPolicy{}, failure
				}
				view := incidents.IncidentView{Ref: candidate.Ref, Title: "Retained Incident", Projects: []incidents.ProjectRef{{StoreID: "local", ProjectID: "p"}}, LastSeq: 7}
				if candidate.Ref.IncidentID == "INC-2" {
					view.Projects = []incidents.ProjectRef{{StoreID: "local", ProjectID: "foreign"}}
				}
				if candidate.Ref.IncidentID == "INC-3" {
					view.WorkItemID = "canonical-work"
				}
				return view, incidents.ViewPolicy{}, nil
			}
			rows, err := runtime.Inventory(context.Background(), scope)
			if stage == "canonical owner" {
				require.NoError(t, err)
				count := 0
				for _, row := range rows {
					if row.Item.Ref.Kind == "incident" {
						count++
						require.Equal(t, "p/INC-1", row.Item.Ref.ID)
						require.Equal(t, "7", row.Revision)
					}
				}
				require.Equal(t, 1, count, "foreign or Work-owned Incident gained a second mutable alias")
			} else {
				require.Error(t, err)
				require.Nil(t, rows)
				if stage == "budget" {
					require.ErrorIs(t, err, datatug.ErrProjectUsageIncomplete)
				} else {
					require.ErrorIs(t, err, failure)
				}
			}
		})
	}
}
