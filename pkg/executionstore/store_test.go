package executionstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/incidentstore"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/stretchr/testify/require"
)

func TestSnapshotSidecarCapAndExpiry(t *testing.T) {
	repositoryRoot, privateRoot := t.TempDir(), t.TempDir()
	project := incidents.ProjectRef{StoreID: "payments", ProjectID: "payments"}
	location := incidents.StoreLocation{StoreID: "payments", Kind: incidents.StoreLocationProjectRepository, Project: &project}
	now := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	manager, err := NewManager(
		incidentstore.RepositoryRoots{Projects: map[incidents.ProjectRef]string{project: repositoryRoot}},
		map[string]incidents.StoreLocation{"payments": location},
		Options{PrivateDir: privateRoot, ByteCap: 256, Retention: time.Hour, Now: func() time.Time { return now }},
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	store, err := manager.ProjectStore("payments")
	require.NoError(t, err)
	ref := apicontract.ExecutionRef{StoreID: "payments", ProjectID: "payments", ExecutionID: "exec-1"}
	recordset := apicontract.Recordset{
		Columns: []apicontract.Column{{Name: "id", Type: "integer"}},
		Rows:    [][]apicontract.TypedValue{{apicontract.NewIntegerValue("7")}},
	}
	snapshotRef, stored, err := store.PutSnapshot(context.Background(), ref, recordset, now)
	require.NoError(t, err)
	require.True(t, stored)
	response, err := store.Snapshot(context.Background(), ref, snapshotRef)
	require.NoError(t, err)
	require.Equal(t, &recordset, response.Recordset)
	_, err = os.Stat(filepath.Join(privateRoot, "payments", "snapshots.sqlite"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(repositoryRoot, "snapshots.sqlite"))
	require.True(t, os.IsNotExist(err))

	now = now.Add(2 * time.Hour)
	response, err = store.Snapshot(context.Background(), ref, snapshotRef)
	require.NoError(t, err)
	require.Equal(t, apicontract.SnapshotExpired, response.SnapshotState.Availability)
	require.Nil(t, response.Recordset)
}

func TestSnapshotSidecarRefusesOversizedBytesWithoutState(t *testing.T) {
	repositoryRoot := t.TempDir()
	project := incidents.ProjectRef{StoreID: "p", ProjectID: "p"}
	location := incidents.StoreLocation{StoreID: "p", Kind: incidents.StoreLocationProjectRepository, Project: &project}
	manager, err := NewManager(
		incidentstore.RepositoryRoots{Projects: map[incidents.ProjectRef]string{project: repositoryRoot}},
		map[string]incidents.StoreLocation{"p": location}, Options{PrivateDir: t.TempDir(), ByteCap: 8},
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	store, err := manager.ProjectStore("p")
	require.NoError(t, err)
	ref := apicontract.ExecutionRef{StoreID: "p", ProjectID: "p", ExecutionID: "exec-large"}
	_, stored, err := store.PutSnapshot(context.Background(), ref, apicontract.Recordset{
		Columns: []apicontract.Column{{Name: "value", Type: "string"}},
		Rows:    [][]apicontract.TypedValue{{apicontract.NewStringValue("too large")}},
	}, time.Now().UTC())
	require.NoError(t, err)
	require.False(t, stored)
	_, err = store.State(context.Background(), "exec-large")
	require.True(t, errors.Is(err, ErrSnapshotNotFound))
}

func TestNewManagerRejectsPrivateDirInsideEvidenceRepository(t *testing.T) {
	repositoryRoot := t.TempDir()
	privateRoot := filepath.Join(repositoryRoot, ".server-private")
	project := incidents.ProjectRef{StoreID: "p", ProjectID: "p"}
	location := incidents.StoreLocation{StoreID: "p", Kind: incidents.StoreLocationProjectRepository, Project: &project}
	manager, err := NewManager(
		incidentstore.RepositoryRoots{Projects: map[incidents.ProjectRef]string{project: repositoryRoot}},
		map[string]incidents.StoreLocation{"p": location},
		Options{PrivateDir: privateRoot},
	)
	require.Error(t, err)
	require.Nil(t, manager)
	_, statErr := os.Stat(privateRoot)
	require.True(t, os.IsNotExist(statErr), "unsafe directory was created: %v", statErr)
}

func TestNewManagerRejectsPrivateDirInsideUnconfiguredGitRepository(t *testing.T) {
	evidenceRoot := t.TempDir()
	gitRoot := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(gitRoot, ".git"), 0o700))
	privateRoot := filepath.Join(gitRoot, "private")
	project := incidents.ProjectRef{StoreID: "p", ProjectID: "p"}
	location := incidents.StoreLocation{StoreID: "p", Kind: incidents.StoreLocationProjectRepository, Project: &project}
	manager, err := NewManager(
		incidentstore.RepositoryRoots{Projects: map[incidents.ProjectRef]string{project: evidenceRoot}},
		map[string]incidents.StoreLocation{"p": location},
		Options{PrivateDir: privateRoot},
	)
	require.Error(t, err)
	require.Nil(t, manager)
}

func TestNewManagerRejectsPrivateDirSymlinkedIntoEvidenceRepository(t *testing.T) {
	repositoryRoot := t.TempDir()
	linkParent := t.TempDir()
	link := filepath.Join(linkParent, "evidence-link")
	require.NoError(t, os.Symlink(repositoryRoot, link))
	privateRoot := filepath.Join(link, ".server-private")
	project := incidents.ProjectRef{StoreID: "p", ProjectID: "p"}
	location := incidents.StoreLocation{StoreID: "p", Kind: incidents.StoreLocationProjectRepository, Project: &project}
	manager, err := NewManager(
		incidentstore.RepositoryRoots{Projects: map[incidents.ProjectRef]string{project: repositoryRoot}},
		map[string]incidents.StoreLocation{"p": location},
		Options{PrivateDir: privateRoot},
	)
	require.Error(t, err)
	require.Nil(t, manager)
	_, statErr := os.Stat(filepath.Join(repositoryRoot, ".server-private"))
	require.True(t, os.IsNotExist(statErr), "unsafe directory was created: %v", statErr)
}
