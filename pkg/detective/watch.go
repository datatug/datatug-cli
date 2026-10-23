package detective

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"

	core "github.com/datatug/datatug-core/pkg/detective"
)

type watchVersionKey struct{}
type watchSourcePin struct{ version, expected, actual string }

// ExecuteWatchAction reuses ordinary Check admission and execution, adding a
// version pin checked under the same lock that snapshots its transitive catalog.
// A changed Check requires an explicit Watch edit, which resets its baseline.
func (s *LocalStore) ExecuteWatchAction(ctx context.Context, operationID, actor string, req ActionRequest, version, fingerprint string, runner QueryRunner, metadata ExecutionMetadata, revisions QueryRevisionLookup) (ActionResponse, string, error) {
	if req.Type != "run-check" || version == "" || revisions == nil {
		return ActionResponse{}, "", ErrInvalid
	}
	pin := &watchSourcePin{version: version, expected: fingerprint}
	response, err := s.ExecuteAction(context.WithValue(ctx, watchVersionKey{}, pin), operationID, actor, req, runner, metadata, nil, nil, revisions)
	return response, pin.actual, err
}

func checkWatchCatalog(ctx context.Context, catalog core.Catalog, checkID string, revisions map[string]string) error {
	pin, pinned := ctx.Value(watchVersionKey{}).(*watchSourcePin)
	if !pinned {
		return nil
	}
	check, ok := catalog.Checks[checkID]
	if !ok || strconv.FormatInt(check.Version, 10) != pin.version {
		return ErrConflict
	}
	body, err := json.Marshal(struct {
		Catalog   core.Catalog
		Revisions map[string]string
	}{catalog, revisions})
	if err != nil {
		return ErrInvalid
	}
	sum := sha256.Sum256(body)
	pin.actual = hex.EncodeToString(sum[:])
	if pin.expected != "" && pin.actual != pin.expected {
		return ErrConflict
	}
	return nil
}
