package accesspolicies

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/record"
	"github.com/stretchr/testify/require"
)

func TestProjectTagResourcesCanonicalizePolicyAndResourceIDs(t *testing.T) {
	for _, collection := range []string{"boards", "dbservers"} {
		t.Run(collection, func(t *testing.T) {
			project := record.NewKeyWithID(ProjectsCollection, "demo")
			for _, pattern := range []string{"Revenue", "CAFÉ", "MySQL%2FMySQL:LocalHost:3306"} {
				id, err := url.PathUnescape(pattern)
				require.NoError(t, err)
				resource := access.RecordResourceForKey(record.NewKeyWithParentAndID(project, collection, CanonicalQueryID(strings.ToUpper(id))))
				denied := decodeLoaded(t, "deny.yaml", denyQueryDoc("/datatug_projects/demo/"+collection+"/"+pattern))
				require.ErrorIs(t, AuthorizeWrite(context.Background(), WriteOptions{Policies: []Loaded{denied}}, access.Update, resource), access.ErrAccessDenied)
				grant := `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: specific-tag-grant
default: deny
scopes:
  - path: /datatug_projects/demo/` + collection + `/` + pattern + `
    rules:
      - id: edit-one
        effect: allow
        operations: [update]
`
				allowed := decodeLoaded(t, "grant.yaml", grant)
				require.NoError(t, AuthorizeWrite(context.Background(), WriteOptions{Policies: []Loaded{allowed}}, access.Update, resource))
				unrelated := access.RecordResourceForKey(record.NewKeyWithParentAndID(project, collection, "other"))
				require.ErrorIs(t, AuthorizeWrite(context.Background(), WriteOptions{Policies: []Loaded{allowed}}, access.Update, unrelated), access.ErrAccessDenied)
			}
		})
	}
}
