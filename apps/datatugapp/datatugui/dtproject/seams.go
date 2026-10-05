package dtproject

import (
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
	readSettings         = dtconfig.GetSettings
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
