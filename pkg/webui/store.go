package webui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/datatug/datatug-cli/pkg/dtroot"
)

// DirEnv overrides where installed web UIs live.
const DirEnv = "DATATUG_WEB_UI_DIR"

// ErrNotInstalled is returned when no web UI is installed.
var ErrNotInstalled = errors.New("no DataTug web UI is installed")

// Store is the directory of installed web UI versions and their selection state.
type Store struct{ Dir string }

// DefaultStore is ~/datatug/web-ui, or $DATATUG_WEB_UI_DIR.
func DefaultStore() Store {
	if dir := os.Getenv(DirEnv); dir != "" {
		return Store{Dir: dir}
	}
	return Store{Dir: filepath.Join(dtrootPath(), "web-ui")}
}

var dtrootPath = dtroot.Path

// State is which version is pinned, served last, and served before that.
type State struct {
	Pinned   string `json:"pinned,omitempty"`
	Active   string `json:"active,omitempty"`
	Previous string `json:"previous,omitempty"`
}

// Entry is one installed version.
type Entry struct {
	Version  string
	Dir      string
	Manifest Manifest
	// Incompatible is why this CLI will not serve it; nil when it can.
	Incompatible error
}

func (s Store) versionsDir() string { return filepath.Join(s.Dir, "versions") }
func (s Store) statePath() string   { return filepath.Join(s.Dir, "state.json") }

// LoadState reads state.json; a missing file is the zero State.
func (s Store) LoadState() (State, error) {
	var st State
	data, err := os.ReadFile(s.statePath())
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, fmt.Errorf("%s is damaged: %w", s.statePath(), err)
	}
	return st, nil
}

func (s Store) saveState(st State) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(st, "", "  ") // a State of strings always marshals
	tmp, err := os.CreateTemp(s.Dir, ".state-*.json")
	if err != nil {
		return err
	}
	_, err = tmp.Write(append(data, '\n'))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), s.statePath())
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}

// List returns installed versions, newest first. A directory whose manifest
// cannot be read is skipped: it is not a usable install.
func (s Store) List() ([]Entry, error) {
	dirs, err := os.ReadDir(s.versionsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, d := range dirs {
		if !d.IsDir() || !ValidVersion(d.Name()) {
			continue
		}
		dir := filepath.Join(s.versionsDir(), d.Name())
		data, err := os.ReadFile(filepath.Join(dir, ManifestFile))
		if err != nil {
			continue
		}
		m, err := ParseManifest(data)
		if err != nil || m.Version != d.Name() {
			continue
		}
		out = append(out, Entry{Version: m.Version, Dir: dir, Manifest: m, Incompatible: Compatible(m)})
	}
	sort.Slice(out, func(i, j int) bool { return CompareVersions(out[i].Version, out[j].Version) > 0 })
	return out, nil
}

func (s Store) find(version string) (Entry, error) {
	entries, err := s.List()
	if err != nil {
		return Entry{}, err
	}
	for _, e := range entries {
		if e.Version == version {
			return e, nil
		}
	}
	return Entry{}, fmt.Errorf("web UI %s is not installed; run `datatug web-ui list`", version)
}

// Select picks the version to serve: requested, else pinned, else the newest
// compatible one. A requested or pinned version that is missing or incompatible
// is an error: it never silently falls back. The chosen version is re-verified.
func (s Store) Select(requested string) (Entry, error) {
	st, err := s.LoadState()
	if err != nil {
		return Entry{}, err
	}
	want, why := requested, "requested"
	if want == "" && st.Pinned != "" {
		want, why = st.Pinned, "pinned"
	}
	var chosen Entry
	if want != "" {
		chosen, err = s.find(want)
		if err != nil {
			return Entry{}, fmt.Errorf("%s %w", why, err)
		}
		if chosen.Incompatible != nil {
			return Entry{}, fmt.Errorf("%s %w", why, chosen.Incompatible)
		}
	} else {
		entries, err := s.List()
		if err != nil {
			return Entry{}, err
		}
		if len(entries) == 0 {
			return Entry{}, ErrNotInstalled
		}
		var reasons []error
		for _, e := range entries {
			if e.Incompatible == nil {
				chosen = e
				break
			}
			reasons = append(reasons, e.Incompatible)
		}
		if chosen.Version == "" {
			return Entry{}, fmt.Errorf("none of the installed web UIs can be served: %w", errors.Join(reasons...))
		}
	}
	if _, err := VerifyDir(chosen.Dir); err != nil {
		return Entry{}, fmt.Errorf("web UI %s failed verification (reinstall it): %w", chosen.Version, err)
	}
	return chosen, nil
}

// MarkActive records that version is now being served.
func (s Store) MarkActive(version string) error {
	st, err := s.LoadState()
	if err != nil {
		return err
	}
	if st.Active == version {
		return nil
	}
	if st.Active != "" {
		st.Previous = st.Active
	}
	st.Active = version
	return s.saveState(st)
}

// Use pins an installed, compatible version.
func (s Store) Use(version string) (State, error) {
	e, err := s.find(version)
	if err != nil {
		return State{}, err
	}
	if e.Incompatible != nil {
		return State{}, e.Incompatible
	}
	st, err := s.LoadState()
	if err != nil {
		return st, err
	}
	current := st.Pinned
	if current == "" {
		current = st.Active
	}
	if current != "" && current != version {
		st.Previous = current
	}
	st.Pinned = version
	return st, s.saveState(st)
}

// Unpin returns selection to "newest compatible".
func (s Store) Unpin() (State, error) {
	st, err := s.LoadState()
	if err != nil {
		return st, err
	}
	st.Pinned = ""
	return st, s.saveState(st)
}

// Rollback pins the previously used version.
func (s Store) Rollback() (State, error) {
	st, err := s.LoadState()
	if err != nil {
		return st, err
	}
	if st.Previous == "" {
		return st, errors.New("there is no previous web UI version to roll back to")
	}
	return s.Use(st.Previous)
}

// Remove deletes an installed version. The pinned version cannot be removed.
func (s Store) Remove(version string) error {
	e, err := s.find(version)
	if err != nil {
		return err
	}
	st, err := s.LoadState()
	if err != nil {
		return err
	}
	if st.Pinned == version {
		return fmt.Errorf("web UI %s is pinned; `datatug web-ui use` another version or `datatug web-ui unpin` first", version)
	}
	// The last served version may be in use by a running `serve --web-ui`: removing its files under it
	// would break that page. Serve another version first.
	if st.Active == version {
		return fmt.Errorf("web UI %s is the one `serve --web-ui` served last (it may still be running); serve another version first", version)
	}
	trash := filepath.Join(s.versionsDir(), ".trash-"+randomSuffix())
	if err := rename(e.Dir, trash); err != nil {
		return err
	}
	if st.Previous == version {
		st.Previous = ""
		if err := s.saveState(st); err != nil {
			return err
		}
	}
	return os.RemoveAll(trash)
}
