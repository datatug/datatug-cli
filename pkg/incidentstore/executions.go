package incidentstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/ingitdb/dalgo2ingitdb"
)

const executionLayoutVersion = 1

var ErrExecutionIndexUnavailable = errors.New("execution index unavailable")

// PutExecution appends one immutable execution receipt to the routed evidence
// repository. The receipt is never overwritten, including by an idempotent
// retry, because an execution ID names exactly one immutable observation.
func (s *RepositoryStore) PutExecution(ctx context.Context, record apicontract.ExecutionRecord) error {
	if err := record.Validate(); err != nil {
		return err
	}
	if record.Ref.StoreID != s.location.StoreID {
		return fmt.Errorf("incident store %q cannot write execution for evidence store %q", s.location.StoreID, record.Ref.StoreID)
	}
	executedAt, _ := time.Parse(time.RFC3339, record.ExecutedAt)
	path := executionPath(executedAt, record.Ref.ExecutionID)
	return s.withLock(ctx, func(store *RepositoryStore) error {
		index, err := store.readExecutionIndex()
		if err != nil {
			return err
		}
		if indexedPath, found := index.Paths[record.Ref.ExecutionID]; found {
			var existing apicontract.ExecutionRecord
			if readErr := store.executionOps.ReadJSON(indexedPath, &existing); readErr == nil {
				return ErrExecutionExists
			} else {
				return fmt.Errorf("%w: read indexed execution receipt: %v", ErrExecutionIndexUnavailable, readErr)
			}
		}
		var occupied json.RawMessage
		if readErr := store.executionOps.ReadJSON(path, &occupied); readErr == nil {
			return ErrExecutionExists
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return fmt.Errorf("inspect execution receipt destination: %w", readErr)
		}
		index.Paths[record.Ref.ExecutionID] = path
		if err := store.executionOps.WriteJSONAtomicWithMode(".store/index.json", index, 0o600); err != nil {
			return fmt.Errorf("prepare execution index: %w", err)
		}
		if err := store.executionOps.WriteJSONAtomicWithMode(path, record, 0o644); err != nil {
			// Atomic publication may report a directory-sync failure after rename
			// made the receipt visible. Keep the authoritative reservation: a
			// later retry either observes the immutable receipt or fails closed as
			// reserved-but-missing until an explicit repair path is provided.
			return fmt.Errorf("write execution receipt with reservation retained: %w", err)
		}
		return nil
	})
}

func executionDateSegment(value string, width, minimum, maximum int) bool {
	if len(value) != width {
		return false
	}
	number, err := strconv.Atoi(value)
	return err == nil && number >= minimum && number <= maximum
}

// Execution loads an immutable receipt by its store-qualified ID.
func (s *RepositoryStore) Execution(ctx context.Context, ref apicontract.ExecutionRef) (record apicontract.ExecutionRecord, err error) {
	if err = ref.Validate(); err != nil {
		return record, err
	}
	if ref.StoreID != s.location.StoreID {
		return record, ErrExecutionNotFound
	}
	err = s.withLock(ctx, func(store *RepositoryStore) error {
		path, found, findErr := store.findExecution(ref.ExecutionID)
		if findErr != nil {
			return findErr
		}
		if !found {
			return ErrExecutionNotFound
		}
		if readErr := store.executionOps.ReadJSON(path, &record); readErr != nil {
			return fmt.Errorf("read execution receipt: %w", readErr)
		}
		if validateErr := record.Validate(); validateErr != nil {
			return fmt.Errorf("validate stored execution receipt: %w", validateErr)
		}
		if record.Ref != ref {
			return ErrExecutionNotFound
		}
		return nil
	})
	return record, err
}

// Executions returns all valid receipts in newest-first order. Filtering and
// response limits are deliberately applied by the contract-facing service.
func (s *RepositoryStore) Executions(ctx context.Context) (records []apicontract.ExecutionRecord, err error) {
	records, _, err = s.ExecutionsBounded(ctx, -1)
	return records, err
}

// ExecutionsBounded returns at most limit receipts while bounding receipt
// reads. omitted reports that one or more indexed receipts were not read.
// A negative limit preserves the unbounded internal/listing behavior.
func (s *RepositoryStore) ExecutionsBounded(ctx context.Context, limit int) (records []apicontract.ExecutionRecord, omitted bool, err error) {
	err = s.withLock(ctx, func(store *RepositoryStore) error {
		paths, listErr := store.executionPaths()
		if listErr != nil {
			return listErr
		}
		sort.Sort(sort.Reverse(sort.StringSlice(paths)))
		if limit >= 0 && len(paths) > limit {
			paths = paths[:limit]
			omitted = true
		}
		for _, path := range paths {
			var record apicontract.ExecutionRecord
			if readErr := store.executionOps.ReadJSON(path, &record); readErr != nil {
				return fmt.Errorf("read execution receipt %q: %w", path, readErr)
			}
			if validateErr := record.Validate(); validateErr != nil {
				return fmt.Errorf("validate execution receipt %q: %w", path, validateErr)
			}
			if record.Ref.StoreID != store.location.StoreID {
				continue
			}
			records = append(records, record)
		}
		return nil
	})
	sort.Slice(records, func(i, j int) bool {
		if records[i].ExecutedAt != records[j].ExecutedAt {
			return records[i].ExecutedAt > records[j].ExecutedAt
		}
		return records[i].Ref.ExecutionID > records[j].Ref.ExecutionID
	})
	return records, omitted, err
}

func executionPath(executedAt time.Time, executionID string) string {
	return fmt.Sprintf("%04d/%02d/%s.json", executedAt.Year(), int(executedAt.Month()), executionID)
}

func (s *RepositoryStore) findExecution(executionID string) (path string, found bool, err error) {
	index, err := s.readExecutionIndex()
	if err != nil {
		return "", false, err
	}
	path, found = index.Paths[executionID]
	return path, found, nil
}

func (s *RepositoryStore) executionPaths() ([]string, error) {
	index, err := s.readExecutionIndex()
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(index.Paths))
	for _, path := range index.Paths {
		paths = append(paths, path)
	}
	return paths, nil
}

type executionIndex struct {
	Paths map[string]string `json:"paths"`
}

type executionLayout struct {
	Version int `json:"version"`
}

func initializeExecutionIndex(files, executions *dalgo2ingitdb.RootedFiles) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	return files.WithExclusiveLock(ctx, ".store/execution-index-init.lock", func(locked dalgo2ingitdb.LockedFiles) error {
		var layout executionLayout
		layoutErr := locked.ReadJSON(".store/execution-layout.json", &layout)
		_, indexErr := loadExecutionIndex(executions)
		if errors.Is(layoutErr, os.ErrNotExist) {
			if errors.Is(indexErr, os.ErrNotExist) {
				index := executionIndex{Paths: make(map[string]string)}
				if err := executions.WriteJSONAtomicWithMode(".store/index.json", index, 0o600); err != nil {
					return fmt.Errorf("initialize execution index: %w", err)
				}
			} else if indexErr != nil {
				return fmt.Errorf("%w: %v", ErrExecutionIndexUnavailable, indexErr)
			}
			layout = executionLayout{Version: executionLayoutVersion}
			if err := locked.WriteJSONAtomicWithMode(".store/execution-layout.json", layout, 0o600); err != nil {
				return fmt.Errorf("initialize execution layout: %w", err)
			}
			return nil
		}
		if layoutErr != nil {
			return fmt.Errorf("%w: read layout: %v", ErrExecutionIndexUnavailable, layoutErr)
		}
		if layout.Version != executionLayoutVersion {
			return fmt.Errorf("%w: unsupported layout version %d", ErrExecutionIndexUnavailable, layout.Version)
		}
		if indexErr != nil {
			return fmt.Errorf("%w: %v", ErrExecutionIndexUnavailable, indexErr)
		}
		return nil
	})
}

func validateExecutionIndex(index executionIndex) error {
	if index.Paths == nil {
		return fmt.Errorf("%w: paths are required", ErrExecutionIndexUnavailable)
	}
	seenPaths := make(map[string]struct{}, len(index.Paths))
	for executionID, path := range index.Paths {
		if err := (apicontract.ExecutionRef{StoreID: "index", ProjectID: "index", ExecutionID: executionID}).Validate(); err != nil {
			return fmt.Errorf("%w: invalid execution id %q", ErrExecutionIndexUnavailable, executionID)
		}
		parts := strings.Split(path, "/")
		if len(parts) != 3 || !executionDateSegment(parts[0], 4, 0, 9999) || !executionDateSegment(parts[1], 2, 1, 12) || parts[2] != executionID+".json" {
			return fmt.Errorf("%w: invalid path for execution %q", ErrExecutionIndexUnavailable, executionID)
		}
		if _, exists := seenPaths[path]; exists {
			return fmt.Errorf("%w: duplicate receipt path %q", ErrExecutionIndexUnavailable, path)
		}
		seenPaths[path] = struct{}{}
	}
	return nil
}

func (s *RepositoryStore) readExecutionIndex() (executionIndex, error) {
	var layout executionLayout
	if err := s.ops.ReadJSON(".store/execution-layout.json", &layout); err != nil {
		return executionIndex{}, fmt.Errorf("%w: read layout: %v", ErrExecutionIndexUnavailable, err)
	}
	if layout.Version != executionLayoutVersion {
		return executionIndex{}, fmt.Errorf("%w: unsupported layout version %d", ErrExecutionIndexUnavailable, layout.Version)
	}
	index, err := loadExecutionIndex(s.executionOps)
	if err != nil {
		return executionIndex{}, fmt.Errorf("%w: %v", ErrExecutionIndexUnavailable, err)
	}
	return index, nil
}

func loadExecutionIndex(ops rootedFileOps) (executionIndex, error) {
	index := executionIndex{}
	if err := ops.ReadJSON(".store/index.json", &index); err != nil {
		return executionIndex{}, fmt.Errorf("read execution index: %w", err)
	}
	if err := validateExecutionIndex(index); err != nil {
		return executionIndex{}, err
	}
	return index, nil
}
