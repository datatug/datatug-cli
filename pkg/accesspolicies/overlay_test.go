package accesspolicies

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const overlayBase = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: base
default: deny
scopes:
  - path: /a
    rules:
      - {id: a, effect: allow, operations: [query]}
`

func TestLoadDirWithOverlay(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "base.yaml"), []byte(overlayBase), 0o600))
	var seen []string
	loaded, err := LoadDirWith(dir, func(path string, data []byte, codec access.Codec) ([]byte, error) {
		seen = append(seen, filepath.Base(path))
		assert.Equal(t, access.YAMLCodec{}, codec)
		return []byte(strings.Replace(string(data), "name: base", "name: overlaid", 1)), nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"base.yaml"}, seen)
	require.Len(t, loaded, 1)
	assert.Equal(t, "overlaid", loaded[0].Policy.Name())

	_, err = LoadDirWith(dir, func(string, []byte, access.Codec) ([]byte, error) { return nil, errors.New("overlay failed") })
	assert.ErrorContains(t, err, "overlay failed")

	plain, err := LoadDir(dir)
	require.NoError(t, err)
	assert.Equal(t, "base", plain[0].Policy.Name())
}

func TestCodecFor(t *testing.T) {
	assert.Equal(t, access.JSONCodec{}, CodecFor("a.JSON"))
	assert.Nil(t, CodecFor("a.txt"))
}

func TestNonProjectionFields(t *testing.T) {
	query := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("Customer", ""))).
		WhereField("Country", dal.Equal, "Canada").
		OrderBy(dal.AscendingField("City")).
		SelectColumns(dal.Column{Expression: dal.Field("FirstName")}, dal.Column{Expression: dal.Field("Email"), Alias: "Email"}, dal.Column{Expression: dal.Field("Phone"), Alias: "contact"})
	fields, err := NonProjectionFields(query)
	require.NoError(t, err)
	assert.Equal(t, []string{"City", "Country", "Phone"}, fields)
	all, err := ReferencedFields(query)
	require.NoError(t, err)
	assert.Equal(t, []string{"City", "Country", "Email", "FirstName", "Phone"}, all)
}
