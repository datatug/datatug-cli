package incidentstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
)

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
			} else if !errors.Is(readErr, os.ErrNotExist) {
				return fmt.Errorf("read indexed execution receipt: %w", readErr)
			}
			// Recover an interrupted append whose private index was published but
			// whose immutable receipt never became visible.
			delete(index.Paths, record.Ref.ExecutionID)
		}
		existingPaths, scanErr := store.executionPathsForID(record.Ref.ExecutionID)
		if scanErr != nil {
			return scanErr
		}
		if len(existingPaths) > 0 {
			if len(existingPaths) > 1 {
				return fmt.Errorf("execution id %q has multiple immutable receipts", record.Ref.ExecutionID)
			}
			index.Paths[record.Ref.ExecutionID] = existingPaths[0]
			if err := store.executionOps.WriteJSONAtomicWithMode(".store/index.json", index, 0o600); err != nil {
				return fmt.Errorf("recover execution index: %w", err)
			}
			return ErrExecutionExists
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
			delete(index.Paths, record.Ref.ExecutionID)
			_ = store.executionOps.WriteJSONAtomicWithMode(".store/index.json", index, 0o600)
			return fmt.Errorf("write execution receipt: %w", err)
		}
		return nil
	})
}

func (s *RepositoryStore) executionPathsForID(executionID string) ([]string, error) {
	years, err := fs.ReadDir(s.executionRoot.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("list execution receipt years: %w", err)
	}
	var paths []string
	for _, year := range years {
		if !year.IsDir() || !executionDateSegment(year.Name(), 4, 0, 9999) {
			continue
		}
		months, readErr := fs.ReadDir(s.executionRoot.FS(), year.Name())
		if readErr != nil {
			return nil, fmt.Errorf("list execution receipt months: %w", readErr)
		}
		for _, month := range months {
			if !month.IsDir() || !executionDateSegment(month.Name(), 2, 1, 12) {
				continue
			}
			monthPath := year.Name() + "/" + month.Name()
			receipts, listErr := fs.ReadDir(s.executionRoot.FS(), monthPath)
			if listErr != nil {
				return nil, fmt.Errorf("list execution receipts: %w", listErr)
			}
			filename := executionID + ".json"
			for _, receipt := range receipts {
				if receipt.Name() == filename {
					paths = append(paths, monthPath+"/"+filename)
					break
				}
			}
		}
	}
	sort.Strings(paths)
	return paths, nil
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

func (s *RepositoryStore) readExecutionIndex() (executionIndex, error) {
	index := executionIndex{Paths: make(map[string]string)}
	if err := s.executionOps.ReadJSON(".store/index.json", &index); errors.Is(err, os.ErrNotExist) {
		return index, nil
	} else if err != nil {
		return index, fmt.Errorf("read execution index: %w", err)
	}
	if index.Paths == nil {
		index.Paths = make(map[string]string)
	}
	return index, nil
}
