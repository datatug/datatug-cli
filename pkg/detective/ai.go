package detective

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sort"
	"time"

	core "github.com/datatug/datatug-core/pkg/detective"
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
)

// LocalAIStore binds private proposals and paste evidence to the authenticated
// local actor. The model receives source text, while the shared proposal keeps
// only verified citation spans and source digests.
type LocalAIStore struct {
	Store *LocalStore
	Scope ai.Scope
}

type localAISource struct {
	Scope    ai.Scope          `json:"scope"`
	Evidence ai.SourceEvidence `json:"evidence"`
	SavedAt  time.Time         `json:"savedAt"`
}

type AIPage struct {
	Proposals  []ai.Proposal `json:"proposals"`
	NextCursor string        `json:"nextCursor,omitempty"`
	HasMore    bool          `json:"hasMore"`
}

type aiGenerationOperation struct {
	Receipt  core.OperationReceipt `json:"receipt"`
	Response ai.Response           `json:"response"`
	Produced bool                  `json:"produced"`
	Done     bool                  `json:"done"`
}

// BeginAIGeneration durably accepts an LLM call before network dispatch.
// An interrupted call is reported as uncertain; it is never silently
// repeated under the same operation ID.
func (s LocalAIStore) BeginAIGeneration(ctx context.Context, id string, binding core.OperationBinding) (ai.Response, bool, error) {
	var response ai.Response
	if s.Store == nil || !validSlug(id) || binding.Project != s.Scope.Key || binding.Actor != s.Scope.Actor || binding.Action != "ai-generate" {
		return response, false, ErrInvalid
	}
	var replay bool
	err := s.Store.withLock(ctx, func() error {
		data, err := safeReadFile(s.Store.privateItemDir("ai-generations"), id)
		if err == nil {
			var operation aiGenerationOperation
			if json.Unmarshal(data, &operation) != nil || core.CheckOperationReplay(operation.Receipt, binding) != nil {
				return ErrConflict
			}
			if !operation.Produced {
				return ErrOutcomeUncertain
			}
			if !operation.Done {
				if err := s.finishAIGeneration(ctx, id, &operation); err != nil {
					return err
				}
			}
			response, replay = operation.Response, true
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		receipt, err := core.NewOperationReceipt(id, binding, 0, time.Now().UTC())
		if err != nil {
			return ErrInvalid
		}
		return writeItem(s.Store.privateItemDir("ai-generations"), id, aiGenerationOperation{Receipt: receipt})
	})
	return response, replay, err
}

func (s LocalAIStore) CompleteAIGeneration(ctx context.Context, id string, binding core.OperationBinding, result ai.Response) error {
	return s.Store.withLock(ctx, func() error {
		data, err := safeReadFile(s.Store.privateItemDir("ai-generations"), id)
		if err != nil {
			return err
		}
		var operation aiGenerationOperation
		if json.Unmarshal(data, &operation) != nil || core.CheckOperationReplay(operation.Receipt, binding) != nil {
			return ErrConflict
		}
		if operation.Produced {
			return ErrConflict
		}
		operation.Response, operation.Produced = result, true
		if err := writeItem(s.Store.privateItemDir("ai-generations"), id, operation); err != nil {
			return err
		}
		return s.finishAIGeneration(ctx, id, &operation)
	})
}

func (s LocalAIStore) finishAIGeneration(ctx context.Context, id string, operation *aiGenerationOperation) error {
	audit := AuditEvent{ID: id, At: operation.Receipt.At, Actor: s.Scope.Actor, Project: s.Scope.Project, Action: "ai-generate"}
	recorded, err := s.Store.auditRecorded(audit)
	if err != nil {
		return err
	}
	if !recorded {
		if err := s.Store.auditMutation(ctx, audit); err != nil {
			return err
		}
	}
	operation.Done = true
	if err := writeItem(s.Store.privateItemDir("ai-generations"), id, operation); err != nil {
		return ErrOutcomeUncertain
	}
	return nil
}

func (s LocalAIStore) Save(ctx context.Context, proposal ai.Proposal) error {
	if s.Store == nil || proposal.Scope != s.Scope || !validSlug(proposal.ID) {
		return ErrInvalid
	}
	return s.Store.withLock(ctx, func() error {
		data, err := safeReadFile(s.Store.privateItemDir("ai-proposals"), proposal.ID)
		if err == nil {
			var old ai.Proposal
			if json.Unmarshal(data, &old) != nil || old.Scope != s.Scope {
				return ErrInvalid
			}
			if reflect.DeepEqual(old, proposal) {
				return nil
			}
			return ErrConflict
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		items, err := readItems[ai.Proposal](s.Store.privateItemDir("ai-proposals"))
		if err != nil {
			return err
		}
		if len(items) >= maxItems {
			return ErrCapacity
		}
		return writeItem(s.Store.privateItemDir("ai-proposals"), proposal.ID, proposal)
	})
}

func (s LocalAIStore) Get(ctx context.Context, id string) (ai.Proposal, error) {
	if s.Store == nil || !validSlug(id) {
		return ai.Proposal{}, ErrInvalid
	}
	var proposal ai.Proposal
	err := s.Store.withLock(ctx, func() error {
		data, err := safeReadFile(s.Store.privateItemDir("ai-proposals"), id)
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if json.Unmarshal(data, &proposal) != nil || proposal.ID != id || proposal.Scope != s.Scope {
			return ErrNotFound
		}
		return nil
	})
	return proposal, err
}

func (s LocalAIStore) Page(ctx context.Context, after string) (AIPage, error) {
	page := AIPage{Proposals: []ai.Proposal{}}
	if s.Store == nil || after != "" && !validSlug(after) {
		return page, ErrInvalid
	}
	err := s.Store.withLock(ctx, func() error {
		items, err := readItems[ai.Proposal](s.Store.privateItemDir("ai-proposals"))
		if err != nil {
			return err
		}
		sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
		for _, item := range items {
			if item.Scope != s.Scope || item.ID <= after {
				continue
			}
			if len(page.Proposals) == 50 {
				page.HasMore = true
				page.NextCursor = page.Proposals[49].ID
				break
			}
			page.Proposals = append(page.Proposals, item)
		}
		return nil
	})
	return page, err
}

// SavePaste retains only a scoped digest and length. Full pasted text remains
// ephemeral; the proposal itself retains only verified citation spans.
func (s LocalAIStore) SavePaste(ctx context.Context, text string) (ai.Source, error) {
	if s.Store == nil || text == "" || len(text) > 16*1024 {
		return ai.Source{}, ErrInvalid
	}
	digest := sha256.Sum256([]byte(text))
	contentDigest := hex.EncodeToString(digest[:])
	scopeDigest := sha256.Sum256([]byte(s.Scope.Key + "\x00" + s.Scope.Project + "\x00" + s.Scope.Actor + "\x00" + contentDigest))
	id := "paste-" + hex.EncodeToString(scopeDigest[:])
	source := ai.Source{ID: id, Kind: "paste", Revision: contentDigest, Text: text}
	evidence := ai.SourceEvidence{ID: id, Kind: "paste", Revision: contentDigest, Digest: contentDigest, Length: len(text)}
	err := s.Store.withLock(ctx, func() error {
		data, err := safeReadFile(s.Store.privateItemDir("ai-sources"), id)
		if err == nil {
			var old localAISource
			if json.Unmarshal(data, &old) != nil || old.Scope != s.Scope || old.Evidence != evidence {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		items, err := readItems[localAISource](s.Store.privateItemDir("ai-sources"))
		if err != nil {
			return err
		}
		if len(items) >= maxItems {
			return ErrCapacity
		}
		return writeItem(s.Store.privateItemDir("ai-sources"), id, localAISource{Scope: s.Scope, Evidence: evidence, SavedAt: time.Now().UTC()})
	})
	return source, err
}

func (s LocalAIStore) AuthorizeSources(ctx context.Context, scope ai.Scope, sources []ai.SourceEvidence) error {
	if scope != s.Scope || len(sources) > 32 {
		return ErrInvalid
	}
	return s.Store.withLock(ctx, func() error {
		for _, evidence := range sources {
			if !validSlug(evidence.ID) || evidence.Kind != "paste" {
				return ErrInvalid
			}
			data, err := safeReadFile(s.Store.privateItemDir("ai-sources"), evidence.ID)
			if err != nil {
				return ErrNotFound
			}
			var saved localAISource
			if json.Unmarshal(data, &saved) != nil || saved.Scope != scope || saved.Evidence != evidence {
				return ErrInvalid
			}
		}
		return nil
	})
}
