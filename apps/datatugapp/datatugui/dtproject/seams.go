package dtproject

import (
	"errors"
	"io"
	"io/fs"
	"time"

	"github.com/atotto/clipboard"
	"github.com/datatug/datatug-cli/pkg/auth/ghauth"
	"github.com/datatug/datatug-cli/pkg/dtstate"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/go-git/go-git/v5"
	"github.com/pkg/browser"

	tea "charm.land/bubbletea/v2"
)

// Doors to the outside world: settings, state, the keyring, GitHub, git, the
// browser, the clipboard and the clock. Screens reach them only through these
// variables, so tests replace them.
var (
	readSettings         = settingsOrNone
	getSettings          = dtconfig.GetSettings
	addProjectToSettings = dtconfig.AddProjectToSettings
	readState            = dtstate.GetDatatugState
	bumpRecentProject    = dtstate.BumpRecentProject
	newProjectStore      = filestore.NewProjectStore
	gitClone             = git.PlainCloneContext
	openURL              = browser.OpenURL
	copyToClipboard      = clipboard.WriteAll
	tick                 = tea.Tick
	now                  = time.Now

	getToken          = ghauth.GetToken
	saveToken         = ghauth.SaveToken
	deleteToken       = ghauth.DeleteToken
	requestDeviceCode = ghauth.RequestDeviceCode
	pollForToken      = ghauth.PollForToken
)

// settingsOrNone reads the settings of this machine. A first run has no
// settings file, or an empty one: that is a machine with no projects, not an
// error, so every screen still offers to create or add a project.
func settingsOrNone() (dtconfig.Settings, error) {
	settings, err := getSettings()
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, io.EOF) {
		return dtconfig.Settings{}, nil
	}
	return settings, err
}
