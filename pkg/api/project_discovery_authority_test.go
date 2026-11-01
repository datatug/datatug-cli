package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/stretchr/testify/require"
)

// A context may expose an already elapsed deadline before its Done signal is
// delivered. Discovery must honor the original deadline in that interval too.
type discoveryElapsedDeadline struct{ context.Context }

func (discoveryElapsedDeadline) Deadline() (time.Time, bool) { return time.Unix(1, 0), true }

func TestOrdinaryDiscoveryRejectsInvalidOriginalAuthority(t *testing.T) {
	_, branch, ref := ordinaryDiscoveryFixture(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		branch string
		want   error
	}{
		{"nil-context", nil, branch, dto.ErrUnsupportedCapability},
		{"missing-branch", t.Context(), "", dto.ErrUnsupportedCapability},
		{"canceled", canceled, branch, context.Canceled},
		{"elapsed-deadline", discoveryElapsedDeadline{context.Background()}, branch, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := LocalProjectDbServerDiscovery(tc.ctx, ref, tc.branch)
			require.ErrorIs(t, err, tc.want)
			require.Nil(t, rows)
			catalog, err := LocalProjectMeaningDiscovery(tc.ctx, ref, tc.branch)
			require.ErrorIs(t, err, tc.want)
			require.Empty(t, catalog.Entities)
		})
	}
	secureMu.Lock()
	securityContextID = ""
	secureMu.Unlock()
	_, err := LocalProjectDbServerDiscovery(t.Context(), ref, branch)
	require.ErrorIs(t, err, secureread.ErrAccessDenied)
}

func TestOrdinaryDiscoveryDoesNotReleaseAfterLateAuthorityChange(t *testing.T) {
	for _, kind := range []string{"server", "meaning"} {
		for _, change := range []string{"cancel", "session"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				dir, branch, ref := ordinaryDiscoveryFixture(t)
				require.NoError(t, os.Mkdir(filepath.Join(dir, "dbs"), 0o700))
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				original := localGitStateSeam
				t.Cleanup(func() { localGitStateSeam = original })
				calls := 0
				localGitStateSeam = func(ctx context.Context, root string) (gitState, error) {
					state, err := original(ctx, root)
					calls++
					if calls == 3 && err == nil {
						if change == "cancel" {
							cancel()
						} else {
							secureMu.Lock()
							securityContextID = "replacement-session"
							secureMu.Unlock()
						}
					}
					return state, err
				}
				var result any
				var err error
				if kind == "server" {
					var rows []datatug.ProjDbServer
					rows, err = LocalProjectDbServerDiscovery(ctx, ref, branch)
					if rows != nil {
						result = rows
					}
				} else {
					var catalog datatug.ProjectMeaningsCatalog
					catalog, err = LocalProjectMeaningDiscovery(ctx, ref, branch)
					if catalog.Version != 0 {
						result = catalog
					}
				}
				require.GreaterOrEqual(t, calls, 3)
				if change == "cancel" {
					require.ErrorIs(t, err, context.Canceled)
				} else {
					require.ErrorIs(t, err, secureread.ErrAccessDenied)
				}
				require.Nil(t, result, "no old-snapshot output may survive a changed original authority")
			})
		}
	}
}

func TestOrdinaryServerDiscoveryRejectsForeignOwningIdentity(t *testing.T) {
	for _, body := range []string{
		`{"id":"mysql:foreign:3306","server":{"driver":"mysql","host":"active","port":3306}}`,
		`{"id":"mysql:active:3306","server":{"driver":"postgres","host":"active","port":3306}}`,
		`{"id":"mysql:active:3306","server":{"driver":"mysql","host":"foreign","port":3306}}`,
	} {
		t.Run(body, func(t *testing.T) {
			dir, branch, ref := ordinaryDiscoveryFixture(t)
			writeDiscoveryFixture(t, dir, "dbs/mysql/mysql:active:3306.dbserver.json", []byte(body))
			rows, err := LocalProjectDbServerDiscovery(t.Context(), ref, branch)
			require.Error(t, err)
			require.Nil(t, rows)
		})
	}
}
