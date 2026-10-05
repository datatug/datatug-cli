package endpoints

import (
	"context"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// By the time resolveSource builds these two messages the source has matched an
// entry of the project's registry, so what it names is the registered ID, not
// something a client made up. The ID is shown as it is, whatever it holds: a
// space, a slash or a letter outside ASCII is not a reason to call it invalid.
func TestResolveSource_NamesTheRegisteredSourceID(t *testing.T) {
	registered := "Café 1/données"
	original := apiResolveSource
	t.Cleanup(func() { apiResolveSource = original })

	for _, tc := range []struct {
		name string
		kind api.SourceKind
		want string
	}{
		{"no recordset definition", api.SourceKindInGitDB, `source "Café 1/données" has no recordset definition at `},
		{"unsupported kind", api.SourceKind("graph"), `source "Café 1/données" has an unsupported kind "graph"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			apiResolveSource = func(context.Context, datatug.ProjectStore, string, string, string) (api.ResolvedSource, error) {
				return api.ResolvedSource{ID: registered, Kind: tc.kind, URL: "ingitdb://x"}, nil
			}
			_, err := resolveSource(context.Background(), nil, t.TempDir(), "local", "alias", "customers")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}
