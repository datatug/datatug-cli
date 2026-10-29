package detective

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/datatug"
	core "github.com/datatug/datatug-core/pkg/detective"
)

func TestOrdinaryArchiveAndPreparedLegacyFailuresCannotChangeCase(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	before, err := s.SaveCase(ctx, Case{ID: "case-boundary", Title: "Case"}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	archived := before
	archived.Status = "archived"
	request := ActionRequest{Type: "update-case", Project: "p", Case: &archived}
	if _, err := s.ExecuteAction(ctx, "archive", "alice", request, nil, nil, nil, nil); !errors.Is(err, datatug.ErrProjectUsageIncomplete) {
		t.Fatal("ordinary archive bypassed inventory", err)
	}
	binding, err := s.ActionBinding(request, "alice")
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := core.NewOperationReceipt("prepared", binding, before.Version+1, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(archived)
	previous := actionSafeReadFile
	t.Cleanup(func() { actionSafeReadFile = previous })
	for _, mode := range []string{"malformed image", "missing current", "read error"} {
		t.Run(mode, func(t *testing.T) {
			op := actionOperation{Receipt: receipt, Kind: "cases", ID: before.ID, Produced: true, After: after}
			want := datatug.ErrProjectUsageIncomplete
			switch mode {
			case "malformed image":
				op.After = json.RawMessage(`{`)
				want = ErrInvalid
			case "missing current":
				actionSafeReadFile = func(string, string) ([]byte, error) { return nil, os.ErrNotExist }
			case "read error":
				want = errors.New("source IO failed")
				actionSafeReadFile = func(string, string) ([]byte, error) { return nil, want }
			}
			if err := s.finishAction(ctx, "prepared", &op); !errors.Is(err, want) {
				t.Fatal("unverified recovery image accepted", mode, err)
			}
			actionSafeReadFile = previous
			current, err := s.GetCase(ctx, before.ID)
			if err != nil || current.Status != before.Status || current.Version != before.Version {
				t.Fatal("refused archive changed case", err)
			}
		})
	}
}
