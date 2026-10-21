package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"time"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/gofrs/flock"
)

const (
	maxQualifiedProjectItems = 1000
	maxQualifiedReadBytes    = 4 << 20
)

// withCurrentBranchRead admits only an explicitly selected current branch,
// holds the project API lock, and rejects a read if the branch or HEAD moves.
// The store reads the checkout, so opening a different branch by name would
// return misleading data; unsupported branch selections fail closed.
func withCurrentBranchRead(ctx context.Context, ref dto.ProjectRef, branch string, read func(*qualifiedProjectReader) (any, error)) (any, error) {
	if err := requireLocalReadPrincipal(); err != nil {
		return nil, err
	}
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	if ref.StoreID != LocalStoreID {
		return nil, ErrUnknownStoreID
	}
	if branch == "" {
		return nil, errors.New("branch is required for a qualified project read")
	}
	projectDir, err := servedProjectDir(ref.ProjectID)
	if err != nil {
		return nil, err
	}
	state, err := localGitStateSeam(ctx, projectDir)
	if err != nil {
		return nil, err
	}
	lock := flock.New(filepath.Join(state.gitDir, "datatug-project-api.lock"))
	locked, err := flockTryLockContextSeam(lock, ctx, 25*time.Millisecond)
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, context.DeadlineExceeded
	}
	defer func() { _ = lock.Unlock() }()
	state, err = localGitStateSeam(ctx, projectDir)
	if err != nil {
		return nil, err
	}
	if branch != state.branch {
		return nil, fmt.Errorf("%w: selected branch %q is not checked out; local reads require the current branch %q", dto.ErrBranchHeadConflict, branch, state.branch)
	}
	reader, err := openQualifiedProjectReaderSeam(projectDir)
	if err != nil {
		return nil, err
	}
	defer reader.close()
	var value any
	err = filestore.ReadEnvironmentFiles(ctx, projectDir, func(*filestore.EnvironmentFiles) error { var e error; value, e = read(reader); return e })
	if err != nil {
		return nil, err
	}
	afterRead, err := localGitStateSeam(ctx, projectDir)
	if err != nil || afterRead.branch != state.branch || afterRead.head != state.head {
		return nil, dto.ErrBranchHeadConflict
	}
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(body) > maxQualifiedReadBytes {
		return nil, fmt.Errorf("qualified project read exceeds %d bytes", maxQualifiedReadBytes)
	}
	return value, nil
}

var openQualifiedProjectReaderSeam = openQualifiedProjectReader

// LocalProjectBoards returns board briefs from the selected current branch.
func LocalProjectBoards(ctx context.Context, ref dto.ProjectRef, branch string) ([]datatug.ProjBoardBrief, error) {
	value, err := withCurrentBranchRead(ctx, ref, branch, func(reader *qualifiedProjectReader) (any, error) {
		boards, err := readQualifiedBoards(reader)
		if err != nil {
			return nil, itemsNotLoaded("boards", ref.ProjectID, err)
		}
		briefs := make([]datatug.ProjBoardBrief, 0, len(boards))
		for _, board := range boards {
			briefs = append(briefs, datatug.ProjBoardBrief{ProjItemBrief: datatug.ProjItemBrief{ID: board.ID, Title: board.Title, Folder: board.Folder, ListOfTags: board.ListOfTags}, Parameters: board.Parameters, RequiredParams: board.RequiredParams})
		}
		return briefs, nil
	})
	if err != nil {
		return nil, err
	}
	return value.([]datatug.ProjBoardBrief), nil
}

// LocalProjectBoard returns a full board from the selected current branch.
func LocalProjectBoard(ctx context.Context, ref dto.ProjectItemRef, branch string) (*datatug.Board, error) {
	value, err := withCurrentBranchRead(ctx, ref.ProjectRef, branch, func(reader *qualifiedProjectReader) (any, error) {
		if err := validateProjectItem(ref.ProjectID, boardIDField, ref.ID); err != nil {
			return nil, err
		}
		board, err := reader.readQualifiedBoard(ref.ID)
		if err != nil {
			return nil, itemNotFound("board", ref.ID, err)
		}
		return board, nil
	})
	if err != nil {
		return nil, err
	}
	return value.(*datatug.Board), nil
}

// LocalProjectEntities returns entity briefs from the selected current branch.
func LocalProjectEntities(ctx context.Context, ref dto.ProjectRef, branch string) ([]datatug.ProjEntityBrief, error) {
	value, err := withCurrentBranchRead(ctx, ref, branch, func(reader *qualifiedProjectReader) (any, error) {
		entities, err := readQualifiedEntities(reader)
		if err != nil {
			return nil, itemsNotLoaded("entities", ref.ProjectID, err)
		}
		briefs := make([]datatug.ProjEntityBrief, 0, len(entities))
		for _, entity := range entities {
			briefs = append(briefs, datatug.ProjEntityBrief{ProjItemBrief: datatug.ProjItemBrief{ID: entity.ID, Title: entity.Title, Folder: entity.Folder, ListOfTags: entity.ListOfTags}})
		}
		return briefs, nil
	})
	if err != nil {
		return nil, err
	}
	return value.([]datatug.ProjEntityBrief), nil
}

// LocalProjectEntity returns a full entity from the selected current branch.
func LocalProjectEntity(ctx context.Context, ref dto.ProjectItemRef, branch string) (json.RawMessage, error) {
	value, err := withCurrentBranchRead(ctx, ref.ProjectRef, branch, func(reader *qualifiedProjectReader) (any, error) {
		if err := validateEntityInput(ref.ProjectID, ref.ID); err != nil {
			return nil, err
		}
		return readQualifiedEntityDocument(reader, ref.ID)
	})
	if err != nil {
		return nil, err
	}
	return value.(json.RawMessage), nil
}

const maxQualifiedEntityDocumentBytes = 256 << 10

// readQualifiedEntityDocument preserves valid web-only entity fields that
// datatug-core's Entity model does not currently represent. It reads only the
// two fixed filestore layouts after the caller's entity ID validation, and
// reconciles dual-layout files before returning a bounded JSON object.
func readQualifiedEntityDocument(reader *qualifiedProjectReader, id string) (json.RawMessage, error) {
	fileName := entityFileName(id)
	nested, nestedErr := readQualifiedOptionalFileLimit(reader, filepath.ToSlash(filepath.Join("entities", id, fileName)), maxQualifiedEntityDocumentBytes)
	flat, flatErr := readQualifiedOptionalFileLimit(reader, filepath.ToSlash(filepath.Join("entities", fileName)), maxQualifiedEntityDocumentBytes)
	if nestedErr != nil && !errors.Is(nestedErr, fs.ErrNotExist) {
		return nil, nestedErr
	}
	if flatErr != nil && !errors.Is(flatErr, fs.ErrNotExist) {
		return nil, flatErr
	}
	if nested == nil && flat == nil {
		return nil, fs.ErrNotExist
	}
	selected := nested
	if selected == nil {
		selected = flat
	}
	var selectedFields map[string]json.RawMessage
	if err := json.Unmarshal(selected, &selectedFields); err != nil || selectedFields == nil {
		return nil, errors.New("entity document is not a JSON object")
	}
	canonicalizeID := false
	if rawID, ok := selectedFields["id"]; ok {
		var storedID string
		if json.Unmarshal(rawID, &storedID) != nil || storedID != id {
			return nil, errors.New("entity document ID does not match the selected entity")
		}
	} else {
		selectedFields["id"], _ = json.Marshal(id)
		canonicalizeID = true
	}
	if nested != nil && flat != nil {
		var flatFields map[string]json.RawMessage
		if json.Unmarshal(flat, &flatFields) != nil || flatFields == nil {
			return nil, errors.New("entity is stored in both layouts with different content")
		}
		if _, ok := flatFields["id"]; !ok {
			flatFields["id"], _ = json.Marshal(id)
		}
		if !sameJSONFields(selectedFields, flatFields) {
			return nil, errors.New("entity is stored in both layouts with different content")
		}
	}
	if !canonicalizeID {
		return json.RawMessage(selected), nil
	}
	body, err := json.Marshal(selectedFields)
	return json.RawMessage(body), err
}

func sameJSONFields(a, b map[string]json.RawMessage) bool {
	var left, right any
	if json.Unmarshal(mustMarshal(a), &left) != nil || json.Unmarshal(mustMarshal(b), &right) != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}

func mustMarshal(value any) []byte {
	body, _ := json.Marshal(value)
	return body
}

// LocalProjectSummary loads the overview data under a branch and HEAD guard.
func LocalProjectSummary(ctx context.Context, ref dto.ProjectRef, branch string) (*datatug.ProjectSummary, error) {
	value, err := withCurrentBranchRead(ctx, ref, branch, func(reader *qualifiedProjectReader) (any, error) {
		var projectFile datatug.ProjectFile
		_, err := reader.readJSON("datatug-project.json", &projectFile)
		if err != nil {
			return nil, itemNotFound("project", ref.ProjectID, err)
		}
		projectFile.SetID(ref.ProjectID)
		entities, err := readQualifiedEntities(reader)
		if err != nil {
			return nil, itemsNotLoaded("entities", ref.ProjectID, err)
		}
		summary := &datatug.ProjectSummary{ProjectFile: projectFile, Entities: make([]*datatug.ProjEntityBrief, 0, len(entities))}
		for _, entity := range entities {
			summary.Entities = append(summary.Entities, &datatug.ProjEntityBrief{ProjItemBrief: datatug.ProjItemBrief{ID: entity.ID, Title: entity.Title, Folder: entity.Folder, ListOfTags: entity.ListOfTags}})
		}
		return summary, nil
	})
	if err != nil {
		return nil, err
	}
	return value.(*datatug.ProjectSummary), nil
}

// LocalProjectEnvironments exposes safe descriptive fields only. In
// particular, the Environment model's dbServers and connection declarations
// are never serialized by this response.
func LocalProjectEnvironments(ctx context.Context, ref dto.ProjectRef, branch string) ([]LocalEnvironmentBrief, error) {
	value, err := withCurrentBranchRead(ctx, ref, branch, func(reader *qualifiedProjectReader) (any, error) {
		environments, err := readQualifiedEnvironments(reader)
		if err != nil {
			return nil, itemsNotLoaded("environments", ref.ProjectID, err)
		}
		briefs := make([]LocalEnvironmentBrief, 0, len(environments))
		for _, environment := range environments {
			env := environment.env
			title := env.Title
			if title == "" {
				title = env.ID
			}
			briefs = append(briefs, LocalEnvironmentBrief{ID: env.ID, Title: title, Presentation: environment.presentation})
		}
		return briefs, nil
	})
	if err != nil {
		return nil, err
	}
	return value.([]LocalEnvironmentBrief), nil
}

// LocalEnvironmentBrief contains only browser-safe presentation metadata.
type LocalEnvironmentBrief struct {
	ID           string                        `json:"id"`
	Title        string                        `json:"title,omitempty"`
	Presentation *LocalEnvironmentPresentation `json:"presentation,omitempty"`
}

// LocalEnvironmentPresentation is a deliberately closed projection of the
// presentation fields understood by the web client.
type LocalEnvironmentPresentation struct {
	Label string `json:"label,omitempty"`
	Icon  string `json:"icon,omitempty"`
	Tone  string `json:"tone,omitempty"`
}

const maxEnvironmentDocumentBytes = 256 << 10

func parseEnvironmentPresentation(body []byte) (*LocalEnvironmentPresentation, error) {
	if len(body) == 0 {
		return nil, nil
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(body, &document); err != nil || document == nil {
		return nil, errors.New("environment document is not a JSON object")
	}
	raw, exists := document["presentation"]
	if !exists || string(raw) == "null" {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, errors.New("environment presentation is invalid")
	}
	result := &LocalEnvironmentPresentation{}
	for key, value := range fields {
		var text string
		if json.Unmarshal(value, &text) != nil {
			return nil, errors.New("environment presentation is invalid")
		}
		switch key {
		case "label":
			if len(text) > 256 {
				return nil, errors.New("environment presentation label is too long")
			}
			result.Label = text
		case "icon":
			if !allowedEnvironmentIcon(text) {
				return nil, errors.New("environment presentation icon is unsupported")
			}
			result.Icon = text
		case "tone":
			switch text {
			case "neutral", "primary", "success", "warning", "danger":
				result.Tone = text
			default:
				return nil, errors.New("environment presentation tone is unsupported")
			}
		default:
			return nil, errors.New("environment presentation has an unsupported field")
		}
	}
	return result, nil
}

func allowedEnvironmentIcon(icon string) bool {
	switch icon {
	case "globe-outline", "desktop-outline", "flask-outline", "cloud-outline", "shield-checkmark-outline", "warning-outline":
		return true
	default:
		return false
	}
}
