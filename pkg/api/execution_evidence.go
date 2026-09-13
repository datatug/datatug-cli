package api

import (
	"fmt"
	"sync"

	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/incidentstore"
	"github.com/datatug/datatug-core/pkg/incidents"
)

var (
	evidenceMu      sync.RWMutex
	evidenceManager *executionstore.Manager
)

// ConfigureExecutionEvidence wires repository routing for every project the
// serve process exposes and opens private snapshot sidecars lazily.
func ConfigureExecutionEvidence(pathsByID map[string]string, configured []incidentstore.ConfiguredStore, options executionstore.Options) error {
	roots, locations, err := incidentstore.ResolveRouting(pathsByID, configured)
	if err != nil {
		return err
	}
	manager, err := executionstore.NewManager(roots, locations, options)
	if err != nil {
		return err
	}
	evidenceMu.Lock()
	previous := evidenceManager
	evidenceManager = manager
	evidenceMu.Unlock()
	if previous != nil {
		return previous.Close()
	}
	return nil
}

// ExecutionEvidenceStore resolves the configured primary project evidence
// store. Dedicated and application routes are opened explicitly by services
// that have a qualified IncidentRef.
func ExecutionEvidenceStore(projectID string, incident *incidents.IncidentRef) (*executionstore.Store, error) {
	evidenceMu.RLock()
	manager := evidenceManager
	evidenceMu.RUnlock()
	if manager == nil {
		return nil, fmt.Errorf("execution evidence store is not configured")
	}
	return manager.RoutedStore(projectID, incident)
}

func ExecutionEvidenceStoreByID(projectID, storeID string) (*executionstore.Store, error) {
	evidenceMu.RLock()
	manager := evidenceManager
	evidenceMu.RUnlock()
	if manager == nil {
		return nil, fmt.Errorf("execution evidence store is not configured")
	}
	return manager.StoreByID(projectID, storeID)
}

// CloseExecutionEvidence releases all routed repository and SQLite handles.
func CloseExecutionEvidence() error {
	evidenceMu.Lock()
	manager := evidenceManager
	evidenceManager = nil
	evidenceMu.Unlock()
	if manager == nil {
		return nil
	}
	return manager.Close()
}
