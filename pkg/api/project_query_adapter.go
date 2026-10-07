package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/gofrs/flock"
	"github.com/strongo/validation"
)

// ErrProjectMutationOutcomeUncertain means an interrupted local write may
// have changed files, but the durable receipt was not completed. The caller
// must reload and review; retrying with another operation ID is not automatic.
var ErrProjectMutationOutcomeUncertain = errors.New("project mutation outcome is uncertain; reload and review the query")

// LocalProjectQueryAdapter uses the already-served project and its current
// Git branch. The same dto.ProjectQueryAdapter contract is implemented by
// Cloud store adapters; no browser can supply the actor identity.
type LocalProjectQueryAdapter struct{}

var _ dto.ProjectQueryAdapter = LocalProjectQueryAdapter{}

// SecureProjectMutationScope binds an operation to the fixed authenticated
// serve principal. The request body has no actor field.
func SecureProjectMutationScope(ref dto.ProjectRef, branch, kind string) (dto.OperationScope, error) {
	secureMu.RLock()
	configured := securityContextID != ""
	session := secureSession
	secureMu.RUnlock()
	if !configured || session.Principal == nil || session.Principal.ID == nil {
		return dto.OperationScope{}, errors.New("authenticated serving principal is required")
	}
	return dto.OperationScope{ActorID: fmt.Sprint(session.Principal.ID), StoreID: ref.StoreID, ProjectID: ref.ProjectID, Branch: branch, Kind: kind}, nil
}

func (LocalProjectQueryAdapter) Capabilities(_ context.Context, ref dto.ProjectRef) (dto.ProjectCapabilities, error) {
	if _, err := servedProjectDir(ref.ProjectID); err != nil {
		return dto.ProjectCapabilities{}, err
	}
	if ref.StoreID != LocalStoreID {
		return dto.ProjectCapabilities{}, ErrUnknownStoreID
	}
	return dto.ProjectCapabilities{QueryRead: true, QuerySave: true}, nil
}

func (LocalProjectQueryAdapter) GetQuery(ctx context.Context, request dto.GetQueryRequest) (*dto.GetQueryResponse, error) {
	if err := request.ProjectRef.Validate(); err != nil {
		return nil, err
	}
	if request.StoreID != LocalStoreID {
		return nil, ErrUnknownStoreID
	}
	if err := validateQueryPath("id", request.ID); err != nil {
		return nil, validation.NewBadRequestError(err)
	}
	projectDir, err := servedProjectDir(request.ProjectID)
	if err != nil {
		return nil, err
	}
	state, err := localGitState(ctx, projectDir)
	if err != nil {
		return nil, err
	}
	if request.Branch == "" || request.Branch != state.branch {
		return nil, dto.ErrBranchHeadConflict
	}
	store, err := projectStoreForID(request.StoreID, request.ProjectID)
	if err != nil {
		return nil, err
	}
	revisioned, ok := store.(datatug.RevisionedQueriesStore)
	if !ok {
		return nil, dto.ErrUnsupportedCapability
	}
	stored, err := revisioned.LoadQueryRevision(ctx, request.ID)
	if err != nil {
		return nil, err
	}
	query := stored.Query
	if query.FolderPath == "" {
		query.FolderPath = datatug.RootSharedFolderName
	}
	return &dto.GetQueryResponse{Query: query, Revision: string(stored.Revision), BranchHead: state.head}, nil
}

func (LocalProjectQueryAdapter) SaveQuery(ctx context.Context, scope dto.OperationScope, request dto.SaveQueryRequest) (*dto.SaveQueryResponse, error) {
	if err := request.Validate(); err != nil {
		return nil, validation.NewBadRequestError(err)
	}
	if request.StoreID != LocalStoreID {
		return nil, ErrUnknownStoreID
	}
	expectedScope, err := SecureProjectMutationScope(request.ProjectRef, request.Branch, "save-query")
	if err != nil {
		return nil, err
	}
	if scope != expectedScope {
		return nil, validation.NewBadRequestError(errors.New("invalid project mutation scope"))
	}
	queryID, err := legacyQueryID(request.Query.FolderPath, request.Query.ID)
	if err != nil {
		return nil, validation.NewBadRequestError(err)
	}
	if err := requireFolderSupport(queryID); err != nil {
		return nil, validation.NewBadRequestError(err)
	}
	projectDir, err := servedProjectDir(request.ProjectID)
	if err != nil {
		return nil, err
	}
	// Reauthorization precedes even a matching receipt replay.
	operation := access.Set
	if request.IfNoneMatch {
		operation = access.Insert
	}
	if err := AuthorizeProjectQueryWrite(ctx, request.ProjectID, queryID, operation); err != nil {
		return nil, err
	}
	if field, reason, found := querywriteQueryCredentialReason(&request.Query.QueryDef); found {
		return nil, validation.NewBadRequestError(validation.NewErrBadRecordFieldValue(field, reason))
	}
	state, err := localGitState(ctx, projectDir)
	if err != nil {
		return nil, err
	}
	lock := flock.New(filepath.Join(state.gitDir, "datatug-project-api.lock"))
	locked, err := lock.TryLockContext(ctx, 25*time.Millisecond)
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, context.DeadlineExceeded
	}
	defer func() { _ = lock.Unlock() }()
	state, err = localGitState(ctx, projectDir)
	if err != nil {
		return nil, err
	}
	digest, err := request.PayloadDigest()
	if err != nil {
		return nil, err
	}
	receiptDir := filepath.Join(state.gitDir, "datatug-project-api", "operations")
	if err := os.MkdirAll(receiptDir, 0o700); err != nil {
		return nil, err
	}
	idHash := sha256.Sum256([]byte(request.OperationID))
	receiptPath := filepath.Join(receiptDir, hex.EncodeToString(idHash[:])+".json")
	var journal localQueryJournal
	if data, readErr := os.ReadFile(receiptPath); readErr == nil {
		if err := json.Unmarshal(data, &journal); err != nil {
			return nil, ErrProjectMutationOutcomeUncertain
		}
		if err := journal.OperationReceipt.MatchSaveRetry(scope, request); err != nil {
			return nil, err
		}
		if !journal.Complete {
			return nil, ErrProjectMutationOutcomeUncertain
		}
		result := journal.Result
		return &result, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return nil, readErr
	}
	if request.Branch == "" || request.Branch != state.branch || request.ExpectedBranchHead != state.head {
		return nil, dto.ErrBranchHeadConflict
	}
	store, err := projectStoreForID(request.StoreID, request.ProjectID)
	if err != nil {
		return nil, err
	}
	revisioned, ok := store.(datatug.RevisionedQueriesStore)
	if !ok {
		return nil, dto.ErrUnsupportedCapability
	}
	var current *datatug.StoredQuery
	current, err = revisioned.LoadQueryRevision(ctx, queryID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if request.IfNoneMatch && current != nil || request.IfMatch != "" && (current == nil || string(current.Revision) != request.IfMatch) {
		return nil, dto.ErrQueryRevisionConflict
	}
	if request.IfNoneMatch && request.Query.Capture != nil {
		return nil, clientCaptureRefusal("save_query cannot create capture provenance")
	}
	if current != nil && !reflect.DeepEqual(current.Query.Capture, request.Query.Capture) {
		return nil, clientCaptureRefusal("save_query cannot alter capture provenance")
	}
	journal = localQueryJournal{OperationReceipt: dto.OperationReceipt{Scope: scope, OperationID: request.OperationID, PayloadDigest: digest}}
	if err := writeLocalQueryJournal(receiptPath, journal); err != nil {
		return nil, err
	}
	query := request.Query
	if query.FolderPath == datatug.RootSharedFolderName {
		query.FolderPath = ""
	}
	condition := datatug.QueryWriteCondition{IfNoneMatch: request.IfNoneMatch, IfMatch: datatug.QueryRevision(request.IfMatch)}
	stored, err := revisioned.PutQuery(ctx, &query, condition)
	if err != nil {
		var conflict *datatug.QueryRevisionConflictError
		if errors.As(err, &conflict) {
			_ = os.Remove(receiptPath)
			return nil, dto.ErrQueryRevisionConflict
		}
		if validation.IsBadRecordError(err) || datatug.IsInvalidQueryLocation(err) {
			_ = os.Remove(receiptPath)
			return nil, validation.NewBadRequestError(err)
		}
		// A store error can follow its logical commit point. Retain the
		// pending receipt so a retry cannot silently write a second time.
		return nil, fmt.Errorf("query save may have committed: %w", ErrProjectMutationOutcomeUncertain)
	}
	oldType := datatug.QueryType("")
	if current != nil {
		oldType = current.Query.Type
	}
	beforeStage, err := localGitState(ctx, projectDir)
	if err != nil || beforeStage.branch != state.branch || beforeStage.head != state.head {
		return nil, ErrProjectMutationOutcomeUncertain
	}
	if err := stageLocalQueryPair(ctx, state.root, projectDir, queryID, query.Type, oldType); err != nil {
		return nil, fmt.Errorf("query saved but staging failed: %w", ErrProjectMutationOutcomeUncertain)
	}
	afterStage, err := localGitState(ctx, projectDir)
	if err != nil || afterStage.branch != state.branch || afterStage.head != state.head {
		return nil, ErrProjectMutationOutcomeUncertain
	}
	result := dto.SaveQueryResponse{Query: request.Query, Revision: string(stored.Revision), BranchHead: state.head}
	journal.Result = result
	journal.Complete = true
	if err := writeLocalQueryJournal(receiptPath, journal); err != nil {
		return nil, fmt.Errorf("query saved but receipt failed: %w", ErrProjectMutationOutcomeUncertain)
	}
	return &result, nil
}

type localQueryJournal struct {
	dto.OperationReceipt
	Complete bool `json:"complete"`
}

func writeLocalQueryJournal(path string, record localQueryJournal) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".receipt-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

type gitState struct{ root, gitDir, branch, head string }

func localGitState(ctx context.Context, projectDir string) (gitState, error) {
	root, err := localGitOutput(ctx, projectDir, "rev-parse", "--show-toplevel")
	if err != nil {
		return gitState{}, dto.ErrUnsupportedCapability
	}
	gitDir, err := localGitOutput(ctx, projectDir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return gitState{}, dto.ErrUnsupportedCapability
	}
	branch, err := localGitOutput(ctx, projectDir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return gitState{}, dto.ErrUnsupportedCapability
	}
	head, err := localGitOutput(ctx, projectDir, "rev-parse", "HEAD")
	if err != nil {
		return gitState{}, dto.ErrInitializationRequired
	}
	return gitState{root: root, gitDir: gitDir, branch: branch, head: head}, nil
}

func localGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func stageLocalQueryPair(ctx context.Context, root, projectDir, queryID string, newType, oldType datatug.QueryType) error {
	// macOS may hand serve a /var/... path while Git canonicalizes it to
	// /private/var/.... Resolve both before checking containment.
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	projectDir, err = filepath.EvalSymlinks(projectDir)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, projectDir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return validation.NewBadRequestError(errors.New("project directory is outside its Git repository"))
	}
	base := filepath.ToSlash(filepath.Join(rel, "queries", queryID+".query"))
	paths := []string{":(literal)" + base + ".json", ":(literal)" + base + "." + strings.ToLower(string(newType))}
	if oldType != "" && oldType != newType {
		oldPath := ":(literal)" + base + "." + strings.ToLower(string(oldType))
		if _, err := localGitOutput(ctx, root, "ls-files", "--error-unmatch", "--", oldPath); err == nil {
			paths = append(paths, oldPath)
		}
	}
	_, err = localGitOutput(ctx, root, append([]string{"add", "-A", "--"}, paths...)...)
	return err
}
