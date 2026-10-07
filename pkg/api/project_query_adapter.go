package api

import (
	"bytes"
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
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
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

// ProjectBranch is an observed local Git ref. Head is a commit OID, not a
// query revision; SaveQuery still checks it against the selected current ref.
type ProjectBranch struct {
	Name string `json:"name"`
	Head string `json:"head"`
}

// ProjectBranches is a read-only snapshot for selecting the current branch
// and capturing its head before the first conditional query save.
type ProjectBranches struct {
	Branches      []ProjectBranch `json:"branches"`
	DefaultBranch string          `json:"defaultBranch,omitempty"`
	CurrentBranch string          `json:"currentBranch"`
}

var _ dto.ProjectQueryAdapter = LocalProjectQueryAdapter{}

// loadLocalQueryRevision is the storage boundary used by both the read and
// write paths. A test replaces it to switch Git branches during a read.
var loadLocalQueryRevision = func(ctx context.Context, store datatug.RevisionedQueriesStore, id string) (*datatug.StoredQuery, error) {
	return store.LoadQueryRevision(ctx, id)
}

func requireLocalReadPrincipal() error {
	secureMu.RLock()
	defer secureMu.RUnlock()
	if securityContextID == "" || secureSession.Principal == nil || secureSession.Principal.ID == nil {
		return secureread.ErrAccessDenied
	}
	return nil
}

// RequireLocalReadPrincipal lets HTTP reject an unauthenticated branch read
// before even resolving its request-supplied project identifier.
func RequireLocalReadPrincipal() error { return requireLocalReadPrincipal() }

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
	if err := requireLocalReadPrincipal(); err != nil {
		return dto.ProjectCapabilities{}, err
	}
	if _, err := servedProjectDir(ref.ProjectID); err != nil {
		return dto.ProjectCapabilities{}, err
	}
	if ref.StoreID != LocalStoreID {
		return dto.ProjectCapabilities{}, ErrUnknownStoreID
	}
	return dto.ProjectCapabilities{QueryRead: true, QuerySave: true}, nil
}

// LocalProjectBranches lists only local heads of the authenticated served
// project. Branch switching and creation are separate capabilities and are
// not implied by this read endpoint.
func LocalProjectBranches(ctx context.Context, ref dto.ProjectRef) (*ProjectBranches, error) {
	if err := requireLocalReadPrincipal(); err != nil {
		return nil, err
	}
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	if ref.StoreID != LocalStoreID {
		return nil, ErrUnknownStoreID
	}
	projectDir, err := servedProjectDir(ref.ProjectID)
	if err != nil {
		return nil, err
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
	output, err := localGitOutput(ctx, state.root, "for-each-ref", "--count=1001", "--sort=refname", "--format=%(refname:short)%00%(objectname)", "refs/heads")
	if err != nil {
		return nil, err
	}
	branches := make([]ProjectBranch, 0)
	if output != "" {
		for _, line := range strings.Split(output, "\n") {
			parts := strings.Split(line, "\x00")
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" || len(branches) == 1000 {
				return nil, dto.ErrUnsupportedCapability
			}
			branches = append(branches, ProjectBranch{Name: parts[0], Head: parts[1]})
		}
	}
	defaultBranch := ""
	if remoteHead, err := localGitOutput(ctx, state.root, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		defaultBranch = strings.TrimPrefix(remoteHead, "origin/")
	}
	afterRead, err := localGitState(ctx, projectDir)
	if err != nil || afterRead.branch != state.branch || afterRead.head != state.head {
		return nil, dto.ErrBranchHeadConflict
	}
	return &ProjectBranches{Branches: branches, DefaultBranch: defaultBranch, CurrentBranch: state.branch}, nil
}

func (LocalProjectQueryAdapter) GetQuery(ctx context.Context, request dto.GetQueryRequest) (*dto.GetQueryResponse, error) {
	if err := requireLocalReadPrincipal(); err != nil {
		return nil, err
	}
	if err := request.Validate(); err != nil {
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
	stored, err := loadLocalQueryRevision(ctx, revisioned, request.ID)
	if err != nil {
		return nil, err
	}
	afterRead, err := localGitState(ctx, projectDir)
	if err != nil || afterRead.branch != state.branch || afterRead.head != state.head {
		return nil, dto.ErrBranchHeadConflict
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
	digest, err := request.PayloadDigest()
	if err != nil {
		return nil, err
	}
	receiptPath, err := localMutationReceiptPath(scope.ActorID, request.OperationID)
	if err != nil {
		return nil, err
	}
	// A receipt key is actor + operation ID, never store/project/branch. This
	// makes an ID reused against a different authorized project a conflict
	// rather than a fresh mutation. The per-key lock spans repositories.
	opLock := flock.New(receiptPath + ".lock")
	locked, err := opLock.TryLockContext(ctx, 25*time.Millisecond)
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, context.DeadlineExceeded
	}
	defer func() { _ = opLock.Unlock() }()
	var journal localQueryJournal
	pending := false
	if data, readErr := os.ReadFile(receiptPath); readErr == nil {
		if err := json.Unmarshal(data, &journal); err != nil {
			return nil, ErrProjectMutationOutcomeUncertain
		}
		if err := journal.MatchSaveRetry(scope, request); err != nil {
			return nil, err
		}
		if journal.Complete {
			result := journal.Result
			return &result, nil
		}
		pending = true
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return nil, readErr
	}
	state, err := localGitState(ctx, projectDir)
	if err != nil {
		return nil, err
	}
	lock := flock.New(filepath.Join(state.gitDir, "datatug-project-api.lock"))
	locked, err = lock.TryLockContext(ctx, 25*time.Millisecond)
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
	if request.Branch == "" || request.Branch != state.branch || request.ExpectedBranchHead != state.head {
		if pending {
			return nil, ErrProjectMutationOutcomeUncertain
		}
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
	query := request.Query
	if query.FolderPath == datatug.RootSharedFolderName {
		query.FolderPath = ""
	}
	shouldPut := true
	if pending {
		if journal.QueryID != queryID || journal.Result.Revision == "" || journal.Result.BranchHead != state.head || journal.Result.Query.Type != request.Query.Type || len(journal.MetadataBytes) == 0 {
			return nil, ErrProjectMutationOutcomeUncertain
		}
		previousUnchanged := request.IfNoneMatch && current == nil || request.IfMatch != "" && current != nil && string(current.Revision) == journal.PreviousRevision
		if current != nil && string(current.Revision) == journal.Result.Revision {
			// Core's revision hashes the exact JSON and body sidecar bytes, so
			// this is the intended pair already committed before interruption.
			shouldPut = false
		} else if !previousUnchanged {
			// Foreign bytes replaced the previous or intended pair. Do not
			// overwrite them or pretend the pending operation completed.
			return nil, ErrProjectMutationOutcomeUncertain
		}
	} else {
		if request.IfNoneMatch && current != nil || request.IfMatch != "" && (current == nil || string(current.Revision) != request.IfMatch) {
			return nil, dto.ErrQueryRevisionConflict
		}
		if request.IfNoneMatch && request.Query.Capture != nil {
			return nil, clientCaptureRefusal("save_query cannot create capture provenance")
		}
		if current != nil && !reflect.DeepEqual(current.Query.Capture, request.Query.Capture) {
			return nil, clientCaptureRefusal("save_query cannot alter capture provenance")
		}
		preview, err := previewLocalQueryRevision(ctx, query)
		if err != nil {
			return nil, err
		}
		journal = localQueryJournal{
			OperationReceipt: dto.OperationReceipt{
				Scope: scope, OperationID: request.OperationID, PayloadDigest: digest,
				Result: dto.SaveQueryResponse{Query: request.Query, Revision: string(preview.revision), BranchHead: state.head},
			},
			QueryID: queryID, MetadataBytes: preview.metadata, BodyBytes: preview.body,
		}
		if current != nil {
			journal.PreviousRevision = string(current.Revision)
			journal.OldType = current.Query.Type
		}
		if err := writeLocalQueryJournal(receiptPath, journal); err != nil {
			return nil, err
		}
	}
	if shouldPut {
		condition := datatug.QueryWriteCondition{IfNoneMatch: request.IfNoneMatch, IfMatch: datatug.QueryRevision(request.IfMatch)}
		stored, err := revisioned.PutQuery(ctx, &query, condition)
		if err != nil {
			var conflict *datatug.QueryRevisionConflictError
			if errors.As(err, &conflict) && !pending {
				_ = os.Remove(receiptPath)
				return nil, dto.ErrQueryRevisionConflict
			}
			if (validation.IsBadRecordError(err) || datatug.IsInvalidQueryLocation(err)) && !pending {
				_ = os.Remove(receiptPath)
				return nil, validation.NewBadRequestError(err)
			}
			return nil, fmt.Errorf("query save may have committed: %w", ErrProjectMutationOutcomeUncertain)
		}
		if string(stored.Revision) != journal.Result.Revision {
			return nil, ErrProjectMutationOutcomeUncertain
		}
	}
	beforeStage, err := localGitState(ctx, projectDir)
	if err != nil || beforeStage.branch != state.branch || beforeStage.head != state.head {
		return nil, ErrProjectMutationOutcomeUncertain
	}
	if err := stageLocalQueryPair(ctx, state.root, projectDir, queryID, query.Type, journal.OldType, journal.MetadataBytes, journal.BodyBytes); err != nil {
		return nil, fmt.Errorf("query saved but staging failed: %w", ErrProjectMutationOutcomeUncertain)
	}
	// The index receives only the bytes frozen in the pending intent. An
	// external editor can still change the worktree after Core's PutQuery;
	// refuse completion unless the current pair remains that exact revision.
	stagedPair, err := revisioned.LoadQueryRevision(ctx, queryID)
	if err != nil || string(stagedPair.Revision) != journal.Result.Revision {
		return nil, ErrProjectMutationOutcomeUncertain
	}
	afterStage, err := localGitState(ctx, projectDir)
	if err != nil || afterStage.branch != state.branch || afterStage.head != state.head {
		return nil, ErrProjectMutationOutcomeUncertain
	}
	journal.Complete = true
	if err := writeLocalQueryJournal(receiptPath, journal); err != nil {
		return nil, fmt.Errorf("query saved but receipt failed: %w", ErrProjectMutationOutcomeUncertain)
	}
	result := journal.Result
	return &result, nil
}

type localQueryJournal struct {
	dto.OperationReceipt
	Complete         bool              `json:"complete"`
	QueryID          string            `json:"queryId"`
	PreviousRevision string            `json:"previousRevision,omitempty"`
	OldType          datatug.QueryType `json:"oldType,omitempty"`
	MetadataBytes    []byte            `json:"metadataBytes"`
	BodyBytes        []byte            `json:"bodyBytes"`
}

// previewLocalQueryRevision runs the exact Core serializer in a private
// temporary filestore before the durable intent is written. The resulting
// revision identifies the exact metadata and body bytes a retry must find;
// equality of decoded QueryDef values alone would not prove those bytes.
type localQueryPreview struct {
	revision datatug.QueryRevision
	metadata []byte
	body     []byte
}

func previewLocalQueryRevision(ctx context.Context, query datatug.QueryDefWithFolderPath) (localQueryPreview, error) {
	dir, err := os.MkdirTemp("", "datatug-query-preview-*")
	if err != nil {
		return localQueryPreview{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	store, ok := filestore.NewProjectStore("preview", dir).(datatug.RevisionedQueriesStore)
	if !ok {
		return localQueryPreview{}, dto.ErrUnsupportedCapability
	}
	stored, err := store.PutQuery(ctx, &query, datatug.QueryWriteCondition{IfNoneMatch: true})
	if err != nil {
		return localQueryPreview{}, err
	}
	base := filepath.Join(dir, "queries", filepath.FromSlash(query.FolderPath), query.ID+".query")
	metadata, err := os.ReadFile(base + ".json")
	if err != nil {
		return localQueryPreview{}, err
	}
	body, err := os.ReadFile(base + "." + strings.ToLower(string(query.Type)))
	if err != nil {
		return localQueryPreview{}, err
	}
	return localQueryPreview{revision: stored.Revision, metadata: metadata, body: body}, nil
}

// localMutationReceiptPath is shared by every project mutation kind so one
// actor cannot reuse an operation ID for a branch, sync, commit or another
// project and accidentally obtain a second result.
func localMutationReceiptPath(actorID, operationID string) (string, error) {
	root := os.Getenv("DATATUG_OPERATION_RECEIPTS_DIR")
	if root == "" {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(configDir, "datatug", "operation-receipts")
	}
	actorHash := sha256.Sum256([]byte(actorID))
	idHash := sha256.Sum256([]byte(operationID))
	dir := filepath.Join(root, hex.EncodeToString(actorHash[:]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, hex.EncodeToString(idHash[:])+".json"), nil
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
	defer func() { _ = os.Remove(tmp.Name()) }()
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
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	// The pending receipt must be durable before PutQuery can reach its
	// commit point; syncing the directory makes the rename crash-stable.
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	return errors.Join(syncErr, closeErr)
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

func localGitInput(ctx context.Context, dir string, input []byte, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	command.Stdin = bytes.NewReader(input)
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func stageLocalQueryPair(ctx context.Context, root, projectDir, queryID string, newType, oldType datatug.QueryType, metadata, body []byte) error {
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
	paths := []string{base + ".json", base + "." + strings.ToLower(string(newType))}
	var expectedOIDs [2]string
	for i, content := range [][]byte{metadata, body} {
		blobOID, err := localGitInput(ctx, root, content, "hash-object", "-w", "--stdin")
		if err != nil {
			return err
		}
		expectedOIDs[i] = blobOID
		if _, err := localGitOutput(ctx, root, "update-index", "--add", "--cacheinfo", "100644", blobOID, paths[i]); err != nil {
			return err
		}
	}
	if oldType != "" && oldType != newType {
		oldPath := base + "." + strings.ToLower(string(oldType))
		if _, err := localGitOutput(ctx, root, "ls-files", "--error-unmatch", "--", ":(literal)"+oldPath); err == nil {
			if _, err := localGitOutput(ctx, root, "update-index", "--force-remove", "--", oldPath); err != nil {
				return err
			}
			if _, err := localGitOutput(ctx, root, "ls-files", "--error-unmatch", "--", ":(literal)"+oldPath); err == nil {
				return ErrProjectMutationOutcomeUncertain
			}
		}
	}
	for i, path := range paths {
		stagedOID, err := localGitOutput(ctx, root, "rev-parse", ":"+path)
		if err != nil || stagedOID != expectedOIDs[i] {
			return ErrProjectMutationOutcomeUncertain
		}
	}
	return nil
}
