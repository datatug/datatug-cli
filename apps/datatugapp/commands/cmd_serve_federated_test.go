package commands

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/stretchr/testify/assert"
)

// serve's federated label guard reads the project's labels: a project whose
// labels cannot be read refuses the leaf.
func TestServeFederatedLabelGuardReadsTheProjectLabels(t *testing.T) {
	dir := t.TempDir()
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "datatug-labels.json"), []byte("{not json"), 0o600))
	guard := serveFederatedLabelGuard(secureread.Session{Unrestricted: true}, io.Discard)(dir)
	err := guard(context.Background(), secureread.FederatedLeaf{Database: "chinook", Collection: "Customer"})
	var protected *secureread.FieldProtectedError
	assert.ErrorAs(t, err, &protected)
}
