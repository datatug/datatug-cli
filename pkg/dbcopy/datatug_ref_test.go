package dbcopy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDatatugRef(t *testing.T) {
	for raw, want := range map[string]DatatugRef{
		"datatug://shop/crm":              {Project: "shop", DB: "crm"},
		"datatug:///crm":                  {DB: "crm"},
		"datatug://shop/crm?env=prod":     {Project: "shop", DB: "crm", Env: "prod"},
		"DATATUG://shop/crm.v2?env=a-b_c": {Project: "shop", DB: "crm.v2", Env: "a-b_c"},
	} {
		got, err := ParseDatatugRef(raw)
		require.NoError(t, err, raw)
		assert.Equal(t, want, got, raw)
	}
	for _, raw := range []string{
		"sqlite://x", "datatug://", "datatug://shop", "datatug://shop/", "datatug://shop/a/b",
		"datatug://shop/crm?", "datatug://shop/crm?env=", "datatug://shop/crm?other=1", "datatug://shop/crm?env=a&x=1",
		"datatug://u:secret@shop/crm", "datatug://shop/crm#frag", "datatug://../crm", "datatug://shop/..",
	} {
		_, err := ParseDatatugRef(raw)
		require.Error(t, err, raw)
		assert.NotContains(t, err.Error(), "secret")
	}
}

func TestDatatugRefStringAndDisplay(t *testing.T) {
	assert.True(t, IsDatatugRef("datatug://x"))
	assert.False(t, IsDatatugRef("datatug:/x"))
	ref := DatatugRef{Project: "shop", DB: "crm", Env: "prod"}
	assert.Equal(t, "datatug://shop/crm?env=prod", ref.String())
	assert.Equal(t, "datatug:///crm", DatatugRef{DB: "crm"}.String())
	assert.Equal(t, "datatug://shop/crm?env=prod", SourceDisplay("datatug://shop/crm?env=prod"))
	assert.Equal(t, UnparsableSource, SourceDisplay("datatug://u:secret@shop/crm"))
}
