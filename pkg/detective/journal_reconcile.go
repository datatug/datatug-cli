package detective

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	ai "github.com/datatug/datatug-core/pkg/detectiveai"
)

// reconcilePendingOther is called with the project lock held before a newer
// mutation may replace a materialized resource image. This preserves the
// earlier operation's audit and exact retry across all Detective entry paths.
func (s *LocalStore) reconcilePendingOther(ctx context.Context, collection, resourceID string, exceptIDs ...string) error {
	exceptID := ""
	if len(exceptIDs) > 0 {
		exceptID = exceptIDs[0]
	}
	if err := s.visitJournal("ai-confirm-operations", func(id string, data []byte) error {
		var header struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(data, &header) != nil {
			return ErrInvalid
		}
		switch header.Kind {
		case "query":
			return nil // Revisioned query storage owns its own CAS receipt.
		case "run":
			var op aiRunOperation
			if json.Unmarshal(data, &op) != nil {
				return ErrInvalid
			}
			if op.Done || !op.Produced || collection != "cases" || op.Draft.CaseID != resourceID {
				return nil
			}
			return s.recoveryAI(op.Receipt).finishAIRun(ctx, id, &op)
		case ai.KindCheck, ai.KindValidator, ai.KindScenario:
			var op aiCatalogOperation
			if json.Unmarshal(data, &op) != nil {
				return ErrInvalid
			}
			if op.Done || aiCatalogCollection(op.Kind) != collection || op.Receipt.ResourceID != resourceID {
				return nil
			}
			return s.recoveryAI(op.Receipt).finishAICatalog(ctx, id, &op)
		case "", "case":
			var op aiCaseOperation
			if json.Unmarshal(data, &op) != nil {
				return ErrInvalid
			}
			if op.Done || collection != "cases" || op.After.ID != resourceID {
				return nil
			}
			return s.recoveryAI(op.Receipt).finishAICase(ctx, id, &op)
		default:
			return ErrInvalid
		}
	}, exceptID); err != nil {
		return err
	}
	if collection == "cases" {
		if err := s.visitJournal("incident-operations", func(id string, data []byte) error {
			var op IncidentOperation
			if json.Unmarshal(data, &op) != nil {
				return ErrInvalid
			}
			if op.Done || op.Case == nil || op.Case.ID != resourceID {
				return nil
			}
			return s.finishIncidentOperation(ctx, id, &op)
		}, exceptID); err != nil {
			return err
		}
	}
	if collection == "knowledge-proposals" {
		return s.visitJournal("knowledge-operations", func(id string, data []byte) error {
			var op knowledgeOperation
			if json.Unmarshal(data, &op) != nil {
				return ErrInvalid
			}
			if op.Done || op.Response.Proposal == nil || op.Response.Proposal.ID != resourceID {
				return nil
			}
			return s.finishKnowledgeOperation(ctx, id, &op)
		}, exceptID)
	}
	return nil
}

func (s *LocalStore) recoveryAI(receipt ai.Receipt) LocalAIStore {
	return LocalAIStore{Store: s, Scope: ai.Scope{Key: receipt.ScopeKey, Project: receipt.Project, Actor: receipt.Actor}}
}

var journalReaddirnames = (*os.File).Readdirnames

func (s *LocalStore) visitJournal(kind string, visit func(string, []byte) error, exceptIDs ...string) error {
	exceptID := ""
	if len(exceptIDs) > 0 {
		exceptID = exceptIDs[0]
	}
	dir := s.privateItemDir(kind)
	if err := ensureDirIfExists(dir); err != nil {
		return err
	}
	f, err := os.Open(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	for {
		names, err := journalReaddirnames(f, 128)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		for _, name := range names {
			if !strings.HasSuffix(name, ".json") {
				continue
			}
			id := strings.TrimSuffix(name, ".json")
			if id == exceptID {
				continue
			}
			data, readErr := safeReadFile(dir, id)
			if readErr != nil {
				return readErr
			}
			if err := visit(id, data); err != nil {
				return err
			}
		}
		if len(names) == 0 {
			return nil
		}
	}
}
