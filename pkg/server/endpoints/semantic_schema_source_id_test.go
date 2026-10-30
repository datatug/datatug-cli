package endpoints

import (
	"context"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// By the time resolveSource builds these messages the source has matched an
// entry of the project's registry, so what it names is the registered ID, not
// something a client made up. The ID of a source of an unsupported kind is shown
// as it is, whatever it holds: a space, a slash or a letter outside ASCII is not a
// reason to call it invalid. The ID of a source that is read as a recordset
// definition is joined into the path of its file, so it is named, and read, only when
// it is a plain source ID (see dbcopy.IsPlainSourceID): any other is refused with the
// same sentence, naming nothing of it.
func TestResolveSource_NamesTheRegisteredSourceID(t *testing.T) {
	original := apiResolveSource
	t.Cleanup(func() { apiResolveSource = original })

	for _, tc := range []struct {
		name       string
		registered string
		kind       api.SourceKind
		want       string
	}{
		{"no recordset definition", "Café-1", api.SourceKindInGitDB, `source "Café-1" has no readable recordset definition`},
		{"a recordset source that is not a name of a file", "Café 1/données", api.SourceKindInGitDB, `source "` + dbcopy.SourceIDNotShown + `" has no readable recordset definition`},
		{"unsupported kind", "Café 1/données", api.SourceKind("graph"), `source "Café 1/données" has an unsupported kind "graph"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			apiResolveSource = func(context.Context, datatug.ProjectStore, string, string, string) (api.ResolvedSource, error) {
				return api.ResolvedSource{ID: tc.registered, Kind: tc.kind, URL: "ingitdb://x"}, nil
			}
			_, err := resolveSource(context.Background(), nil, t.TempDir(), "local", "alias", "customers")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}
