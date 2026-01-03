package sqlinfoschema

import (
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/stretchr/testify/assert"
)

func TestNewInformationSchema(t *testing.T) {
	var server = datatug.ServerRef{Driver: "sql"}
	v := NewInformationSchema(server)
	assert.EqualValues(t, server, v.server)
}
