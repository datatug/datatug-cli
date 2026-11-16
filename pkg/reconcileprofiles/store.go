// Package reconcileprofiles keeps a local project's reconciliation profiles,
// one plain reviewable file per profile at
// <project>/reconciliations/<id>.json (reconcile.ProfilesFolder).
package reconcileprofiles

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/datatug/datatug-core/pkg/reconcile"
)

// ErrNotFound reports a profile the project does not hold.
var ErrNotFound = errors.New("reconcile profile not found")

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// Seams for failure tests.
var (
	readFile  = os.ReadFile
	writeFile = os.WriteFile
	rename    = os.Rename
	mkdirAll  = os.MkdirAll
	readDir   = os.ReadDir
)

// Store is the file-backed profile store of one project directory.
type Store struct {
	Dir string
}

func (s Store) folder() string { return filepath.Join(s.Dir, reconcile.ProfilesFolder) }

// Path is the file of profile id.
func (s Store) Path(id string) string { return filepath.Join(s.folder(), id+".json") }

// Load reads one profile. The id must be a lower-case kebab name, so it can
// never name a path outside the folder.
func (s Store) Load(id string) (reconcile.Profile, error) {
	if !idPattern.MatchString(id) {
		return reconcile.Profile{}, fmt.Errorf("%w: %q is not a profile id", ErrNotFound, id)
	}
	body, err := readFile(s.Path(id))
	if errors.Is(err, fs.ErrNotExist) {
		return reconcile.Profile{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return reconcile.Profile{}, err
	}
	var profile reconcile.Profile
	if err := json.Unmarshal(body, &profile); err != nil {
		return reconcile.Profile{}, fmt.Errorf("reconcile profile %s: %w", id, err)
	}
	if profile.ID != id {
		return reconcile.Profile{}, fmt.Errorf("reconcile profile %s: the file names id %q", id, profile.ID)
	}
	return profile, nil
}

// Save validates and writes a profile atomically.
func (s Store) Save(profile reconcile.Profile) error {
	if err := profile.Validate(); err != nil {
		return err
	}
	body, _ := json.MarshalIndent(profile, "", "  ") // plain values
	if err := mkdirAll(s.folder(), 0o755); err != nil {
		return err
	}
	tmp := s.Path(profile.ID) + ".tmp"
	if err := writeFile(tmp, append(body, '\n'), 0o644); err != nil {
		return err
	}
	return rename(tmp, s.Path(profile.ID))
}

// List returns every profile of the project in id order. A project with no
// folder has none.
func (s Store) List() ([]reconcile.Profile, error) {
	entries, err := readDir(s.folder())
	if errors.Is(err, fs.ErrNotExist) {
		return []reconcile.Profile{}, nil
	}
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		if entry.IsDir() || !ok || !idPattern.MatchString(id) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	profiles := make([]reconcile.Profile, 0, len(ids))
	for _, id := range ids {
		profile, err := s.Load(id)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, nil
}
