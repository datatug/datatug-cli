package endpoints

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
)

// TestKeepAsIsRoutesAcceptContractAndClientParamNames is S80 Fix 1's
// required proof: every "keep-as-is" GET route that carries a project id
// and/or environment/item id in its query string accepts BOTH
// api-contract.md's Scope names (project, environment) and whatever name
// the live datatug-apps client currently sends for that route — see each
// subtest's "client names" case, sourced from the client files named in the
// fix's report (environment.service.ts, db-server.service.ts,
// agent.service.ts, project-item-service.ts) — with the pre-existing
// default kept working too, so no existing caller regresses.
func TestKeepAsIsRoutesAcceptContractAndClientParamNames(t *testing.T) {
	t.Run("GET /environment-summary", func(t *testing.T) {
		var got dto.ProjectItemRef
		saved := getEnvironmentSummaryFunc
		defer func() { getEnvironmentSummaryFunc = saved }()
		getEnvironmentSummaryFunc = func(_ context.Context, ref dto.ProjectItemRef) (*datatug.EnvironmentSummary, error) {
			got = ref
			return &datatug.EnvironmentSummary{}, nil
		}

		withFakeHandle(t, func() {
			cases := []struct {
				name  string
				query string
			}{
				{"client names proj/env (environment.service.ts's getEnvSummary — the S77-reported bug)", "proj=p1&env=env1"},
				{"contract names project/environment", "project=p1&environment=env1"},
				{"legacy default project/id", "project=p1&id=env1"},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					got = dto.ProjectItemRef{}
					w := httptest.NewRecorder()
					r := httptest.NewRequest(http.MethodGet, "/?"+tc.query, nil)
					getEnvironmentSummary(w, r)
					if got.ProjectID != "p1" {
						t.Errorf("ProjectID = %q, want %q", got.ProjectID, "p1")
					}
					if got.ID != "env1" {
						t.Errorf("ID = %q, want %q", got.ID, "env1")
					}
				})
			}
		})
	})

	t.Run("GET /dbserver-summary", func(t *testing.T) {
		var got dto.ProjectRef
		savedFunc := getDbServerSummaryFunc
		savedCtx := getContextFromRequest
		defer func() {
			getDbServerSummaryFunc = savedFunc
			getContextFromRequest = savedCtx
		}()
		getDbServerSummaryFunc = func(_ context.Context, ref dto.ProjectRef, _ datatug.ServerRef) (*datatug.ProjDbServer, error) {
			got = ref
			return &datatug.ProjDbServer{}, nil
		}
		getContextFromRequest = func(r *http.Request) (context.Context, error) { return r.Context(), nil }

		cases := []struct {
			name  string
			query string
		}{
			{"client name proj (db-server.service.ts's getDbServerSummary)", "proj=p1"},
			{"contract name project", "project=p1"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got = dto.ProjectRef{}
				w := httptest.NewRecorder()
				r := httptest.NewRequest(http.MethodGet, "/?"+tc.query, nil)
				getDbServerSummary(w, r)
				if got.ProjectID != "p1" {
					t.Errorf("ProjectID = %q, want %q", got.ProjectID, "p1")
				}
			})
		}
	})

	t.Run("GET /dbserver-databases", func(t *testing.T) {
		var got dto.GetServerDatabasesRequest
		saved := getServerDatabasesFunc
		defer func() { getServerDatabasesFunc = saved }()
		getServerDatabasesFunc = func(request dto.GetServerDatabasesRequest) ([]*datatug.DbCatalog, error) {
			got = request
			return nil, nil
		}

		cases := []struct {
			name  string
			query string
		}{
			{"client names proj/env (db-server.service.ts's getServerDatabases)", "proj=p1&env=env1"},
			{"contract names project/environment", "project=p1&environment=env1"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got = dto.GetServerDatabasesRequest{}
				w := httptest.NewRecorder()
				r := httptest.NewRequest(http.MethodGet, "/?"+tc.query, nil)
				getServerDatabases(w, r)
				if got.Project != "p1" || got.Environment != "env1" {
					t.Errorf("got Project=%q Environment=%q, want %q/%q", got.Project, got.Environment, "p1", "env1")
				}
			})
		}
	})

	t.Run("GET /exec/select", func(t *testing.T) {
		var got api.SelectRequest
		saved := executeSelectFunc
		defer func() { executeSelectFunc = saved }()
		executeSelectFunc = func(_ context.Context, _ string, request api.SelectRequest) (api.QueryResultResponse, error) {
			got = request
			return api.QueryResultResponse{}, nil
		}

		cases := []struct {
			name  string
			query string
		}{
			{"client names proj/env (agent.service.ts's select)", "proj=p1&env=env1&sql=select+1"},
			{"contract names project/environment", "project=p1&environment=env1&sql=select+1"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got = api.SelectRequest{}
				w := httptest.NewRecorder()
				r := httptest.NewRequest(http.MethodGet, "/?"+tc.query, nil)
				executeSelectHandler(w, r)
				if got.Project != "p1" || got.Environment != "env1" {
					t.Errorf("got Project=%q Environment=%q, want %q/%q", got.Project, got.Environment, "p1", "env1")
				}
			})
		}
	})

	t.Run("GET /queries/get_query", func(t *testing.T) {
		var got dto.ProjectItemRef
		saved := getQuery
		defer func() { getQuery = saved }()
		getQuery = func(_ context.Context, ref dto.ProjectItemRef) (*datatug.QueryDefWithFolderPath, error) {
			got = ref
			return &datatug.QueryDefWithFolderPath{}, nil
		}

		withFakeHandle(t, func() {
			cases := []struct {
				name  string
				query string
			}{
				{"client name query (project-item-service.ts's getProjItem, itemPath=query)", "project=p1&query=q1"},
				{"legacy default id", "project=p1&id=q1"},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					got = dto.ProjectItemRef{}
					w := httptest.NewRecorder()
					r := httptest.NewRequest(http.MethodGet, "/?"+tc.query, nil)
					getQueryHandler(w, r)
					if got.ProjectID != "p1" {
						t.Errorf("ProjectID = %q, want %q", got.ProjectID, "p1")
					}
					if got.ID != "q1" {
						t.Errorf("ID = %q, want %q", got.ID, "q1")
					}
				})
			}
		})
	})
}
