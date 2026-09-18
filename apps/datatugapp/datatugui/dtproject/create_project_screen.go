package dtproject

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/datatug/datatug-cli/pkg/auth/ghauth"
	"github.com/datatug/datatug-cli/pkg/dtgithub"
	"github.com/datatug/datatug-cli/pkg/sneatv"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/filetug/filetug/pkg/fsutils"
	"github.com/gdamore/tcell/v2"
	"github.com/google/go-github/v91/github"
	"github.com/pkg/browser"
	"github.com/rivo/tview"
	"golang.org/x/oauth2"
)

type createTarget string

const (
	createAtLocal  createTarget = "Local"
	createAtGitHub createTarget = "GitHub"
)

// newProjectStoreID is the store id the create screen quotes when it asks
// datatug-core whether the form is valid.
//
// dto.CreateProjectRequest.Validate checks the store alongside the id and
// the title, and this screen has no store to name: it writes the project's
// files itself (createLocalProject) or hands the job to dtgithub, and never
// calls storage.NewDatatugStore. "files" is the id `datatug serve`
// registers the local file store under (pkg/server/http_server.go), so it
// is the closest true answer for the Local target and a placeholder for the
// GitHub one. Nothing resolves a store from it here — it exists only so the
// id and title rules can be enforced by their owner instead of being
// copied into this package.
const newProjectStoreID = "files"

// validateNewProject reports whether the create form holds a project
// datatug-core would accept, and is the only validation this screen does.
//
// Since datatug-core v0.39.0 a project id is supplied by the caller and
// never derived from the title — it addresses the project for the rest of
// its life, as a directory name under a file-backed store and as a key
// segment elsewhere — so the rules it must satisfy (1-64 characters,
// lower-case ASCII letters, digits, "-" and "_", starting and ending with a
// letter or a digit, upper case refused rather than folded) live in
// dto.CreateProjectRequest.Validate. This function forwards to it and
// surfaces its error verbatim; it deliberately re-implements none of it, so
// the screen cannot drift from the store that will enforce it.
func validateNewProject(projectID, title string) error {
	return dto.CreateProjectRequest{
		StoreID: newProjectStoreID,
		ID:      projectID,
		Title:   title,
	}.Validate()
}

// suggestProjectID derives a project-id suggestion from a project title,
// for the create form to prefill the id field with. It is a convenience,
// not a rule: the id actually used is whatever the id field holds when the
// user presses Create, and validateNewProject alone decides whether that is
// acceptable.
//
// Every run of characters that cannot appear in an id becomes a single "-",
// ASCII letters are lower-cased, and separators are trimmed from both ends,
// so "My First Project" suggests "my-first-project". The candidate is then
// put through validateNewProject, the same check the form applies to what
// the user types: if it comes back refused — a non-Latin title yields no
// ASCII letters or digits at all, a very long one overruns the id length
// limit — the suggestion is dropped and "" returned. The screen then leaves
// the id empty for the user to type instead of blocking on the title, and
// this function never has to know why core refused it.
func suggestProjectID(title string) string {
	var suggestion strings.Builder
	separatorPending := false
	for _, r := range strings.ToLower(title) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			// Only between two kept characters, never leading: that trims
			// the start, and never emitting a trailing one trims the end.
			if separatorPending && suggestion.Len() > 0 {
				suggestion.WriteByte('-')
			}
			separatorPending = false
			suggestion.WriteRune(r)
			continue
		}
		separatorPending = true
	}
	candidate := suggestion.String()
	if candidate == "" || validateNewProject(candidate, title) != nil {
		return ""
	}
	return candidate
}

// goCreateProjectScreen shows a modal to create a new project
func goCreateProjectScreen(tui *sneatnav.TUI, createAt createTarget) {
	/*
		The modal should be defined in separate file
		The modal initially consist of 2 fields:
		Name: string (max 50 chars)
		Create at: (radio group: local, GitHub)
		If GitHub is choosen an addiional "Repository title" field shown
		If Local is choose an additional "Location" text field shown with default value of "~/datatug"
		At bottom of the modal 2 buttons: "Create" and "Cancel"
		Cancel closes dialog and nothing happens
		If "Create" button selected:
		   1) creates a `datatug.Project` with provided title
		   2) If local chosen safes to files store,
		      otherwise create repo using GitHub API `client.Repositories.Create`,
			  clones it to the "~/datatug/github.com/{owner}/{repo}" directory.
		      See example at `openDatatugDemoProject` and refactor code to reuse logic.
		   3) Once project created and if a Github one has local copy open the project (see how in `openDatatugDemoProject`)
	*/

	b := projectsBreadcrumbs(tui)
	b.Push(sneatv.NewBreadcrumb("New project", nil))

	var title, location string
	var githubOwner string
	var visibility = "Public"
	location = "~/datatug"

	// projectID is what the user will create the project as. It starts out
	// as a suggestion derived from the title and keeps following the title
	// until the user edits the field themselves — from that moment
	// projectIDEdited stays true and the id is theirs, never overwritten by
	// a later keystroke in Title.
	var projectID string
	var projectIDEdited bool

	flex := tview.NewFlex().SetDirection(tview.FlexRow)

	// --- Form ---
	form := tview.NewForm()

	tabs := sneatv.NewTabs(tui.App,
		sneatv.UnderlineTabsStyle,
		sneatv.WithLabel("[gray]Save to:[-] "),
		sneatv.FocusDown(func(tview.Primitive) {
			tui.App.SetFocus(form)
		}),
		sneatv.FocusUp(func(current tview.Primitive) {
			tui.Header.SetFocus(sneatnav.ToBreadcrumbs, current)
		}),
		sneatv.FocusLeft(func(current tview.Primitive) {
			tui.App.SetFocus(tui.Menu)
		}),
	)
	tabs.AddTabs(
		&sneatv.Tab{
			ID:        "GitHub",
			Title:     "GitHub",
			Primitive: tview.NewTextView().SetText("GitHub content"),
		},
		&sneatv.Tab{
			ID:        "BitBucket",
			Title:     "BitBucket",
			Closable:  true,
			Primitive: tview.NewTextView().SetText("BitBucket content"),
		},
		&sneatv.Tab{
			ID:        "local",
			Title:     "Locally",
			Primitive: tview.NewTextView().SetText("Local content"),
		},
	)

	flex.AddItem(tabs, 3, 0, true)

	//sneatv.DefaultBorderWithoutPadding(flex.Box)
	//flex.AddItem(tview.NewTextView(), 1, 0, false)
	flex.SetTitle("New Project")

	// validationView carries the form's inline error: an empty or malformed
	// id (or a missing title) has to say so here and stop the create, never
	// reach a store that would refuse it out of sight or, worse, accept it.
	validationView := tview.NewTextView().SetDynamicColors(true).SetWordWrap(true)

	// idField is rebuilt by every refreshForm, so the closures below reach
	// it through this variable rather than capturing one instance.
	var idField *tview.InputField

	// settingProjectID is true only while the screen itself writes into
	// idField. tview's changed handler cannot tell a programmatic SetText
	// from a keystroke, and without this guard the very first suggestion
	// would look like a user edit and freeze the id at one character.
	var settingProjectID bool

	showValidationError := func(err error) {
		validationView.SetText("[red]" + tview.Escape(err.Error()) + "[-]")
	}

	// refreshValidation re-checks the form as it is typed. A form nobody has
	// touched yet stays quiet — "id is required" before the first keystroke
	// is noise, not help — but anything typed is judged immediately, and
	// pressing Create re-checks unconditionally.
	refreshValidation := func() {
		if title == "" && projectID == "" {
			validationView.SetText("")
			return
		}
		if err := validateNewProject(projectID, title); err != nil {
			showValidationError(err)
			return
		}
		validationView.SetText("")
	}

	// setSuggestedProjectID installs a title-derived suggestion without
	// letting it count as a user edit.
	setSuggestedProjectID := func(suggested string) {
		projectID = suggested
		if idField != nil {
			settingProjectID = true
			idField.SetText(suggested)
			settingProjectID = false
		}
	}

	var refreshForm func()

	// GitHub info components
	githubRepoPath := tview.NewTextView().
		SetDynamicColors(true).
		SetRegions(true).
		SetLabel("Repository")

	var repoExists bool

	normalizeRepoName := func(n string) string {
		return strings.Trim(strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
				return r
			}
			return '-'
		}, strings.ToLower(n)), "-_")
	}

	githubRepoPath.SetHighlightedFunc(func(added, removed, remaining []string) {
		if len(added) == 0 {
			return
		}
		region := added[0]
		var url string
		if region == "owner" {
			url = fmt.Sprintf("https://github.com/%s", githubOwner)
		} else if region == "repo" && repoExists {
			url = fmt.Sprintf("https://github.com/%s/%s", githubOwner, normalizeRepoName(title))
		}
		if url != "" {
			_ = browser.OpenURL(url)
			// Unhighlight after opening so it can be clicked again
			githubRepoPath.Highlight()
		}
	})

	var updateGithubPath func()

	updateGithubPath = func() {
		if githubOwner != "" {
			repoName := normalizeRepoName(title)
			go func() {
				token, _ := ghauth.GetToken()
				if token != nil {
					client, err := githubClient(context.Background(), token)
					if err != nil {
						return
					}
					_, _, err = client.Repositories.Get(context.Background(), githubOwner, repoName)
					newRepoExists := err == nil
					if newRepoExists != repoExists {
						repoExists = newRepoExists
						tui.App.QueueUpdateDraw(func() {
							// Trigger a redraw of the text with new region tags if needed
							updateGithubPath()
						})
					}
				}
			}()

			ownerPart := fmt.Sprintf("[\"owner\"]github.com/%s[\"\"]", githubOwner)
			repoPart := repoName
			if repoExists {
				repoPart = fmt.Sprintf("[\"repo\"]/%s[\"\"]", repoName)
			} else {
				repoPart = "/" + repoPart
			}

			githubRepoPath.SetText(fmt.Sprintf("%s%s (as [green]%s[-])", ownerPart, repoPart, githubOwner))
		} else {
			githubRepoPath.SetText("")
		}
	}

	refreshForm = func() {
		form.Clear(true)
		form.AddInputField("Title", title, 50, nil, func(text string) {
			title = text
			if !projectIDEdited {
				setSuggestedProjectID(suggestProjectID(title))
			}
			if createAt != "Local" {
				updateGithubPath()
			}
			refreshValidation()
		}).SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			switch event.Key() {
			case tcell.KeyUp:
				focusedItemIndex, _ := form.GetFocusedItemIndex()
				if focusedItemIndex == 0 {
					tui.App.SetFocus(tabs.TextView)
				} else if focusedItemIndex > 0 {
					tui.App.SetFocus(form.GetFormItem(focusedItemIndex - 1))
				}
				return nil
			case tcell.KeyDown:
				focusedItemIndex, _ := form.GetFocusedItemIndex()
				if focusedItemIndex < form.GetFormItemCount()-1 {
					tui.App.SetFocus(form.GetFormItem(1))
					return nil
				}
				return event
			default:
				return event
			}
		})

		// The id field follows Title, so that the suggestion appears right
		// under the words it was derived from and is edited in place. The
		// user owns it from their first keystroke in it onwards.
		idField = tview.NewInputField().
			SetLabel("ID").
			SetText(projectID).
			SetFieldWidth(50).
			SetChangedFunc(func(text string) {
				if !settingProjectID {
					projectIDEdited = true
				}
				projectID = text
				refreshValidation()
			})
		form.AddFormItem(idField)

		if createAt == "Local" {
			form.AddInputField("Location", location, 0, nil, func(text string) {
				location = text
			})
		} else {
			form.AddDropDown("Visibility", []string{"Public", "Private"}, 0, func(option string, optionIndex int) {
				visibility = option
			})

			token, _ := ghauth.GetToken()
			if token == nil {
				// In tview Form we can't easily add a clickable text, maybe a button or just a note
				form.AddButton("Authenticate with GitHub", func() {
					authenticateGitHub(tui, func(owner string) {
						githubOwner = owner
						refreshForm()
					})
				})
			} else {
				if githubOwner == "" {
					// Fetch owner title
					go func() {
						client, err := githubClient(context.Background(), token)
						if err != nil {
							return
						}
						user, _, err := client.Users.Get(context.Background(), "")
						if err == nil {
							githubOwner = user.GetLogin()
							tui.App.QueueUpdateDraw(func() {
								refreshForm()
							})
						}
					}()
				}
				updateGithubPath()
				form.AddFormItem(githubRepoPath)
			}
		}

		form.SetButtonBackgroundColor(tcell.ColorCornflowerBlue)
		form.AddButton("Create", func() {
			// Checked here as well as on every keystroke: a form the user
			// never touched is quiet, and pressing Create on it must still
			// say what is missing rather than create a project with no id.
			if err := validateNewProject(projectID, title); err != nil {
				showValidationError(err)
				return
			}
			repoName := title
			if createAt == "GitHub" {
				repoName = normalizeRepoName(title)
			}
			var projectVisibility datatug.ProjectVisibility
			switch visibility {
			case "Private":
				projectVisibility = datatug.PrivateProject
			case "Public":
				projectVisibility = datatug.PublicProject
			default:
			}
			handleCreateProject(tui, createAt, projectID, title, location, repoName, projectVisibility)
		})
		form.AddButton("Cancel", func() {
			_ = GoDataTugProjectsScreen(tui, sneatnav.FocusToContent)
		})
	}

	refreshForm()

	flex.AddItem(form, 0, 1, true)
	// Three rows so a full validation message (core's "invalid character"
	// error spells out the whole charset) is readable without truncation.
	flex.AddItem(validationView, 3, 0, false)

	contentPanel := sneatnav.NewPanel(tui, sneatv.WithBordersWithoutPadding(flex, flex.Box))
	tui.SetPanels(nil, contentPanel)
	tui.App.SetFocus(tabs.TextView)

	// TODO(help-wanted): This is not called :(, we need to activate tabs when coming from the left menu
	flex.SetFocusFunc(func() {
		tui.App.SetFocus(tabs.TextView)
	})
}

func authenticateGitHub(tui *sneatnav.TUI, onSuccess func(owner string)) {
	// This should probably be a simplified version of ShowAddToGitHubRepo's auth flow
	// or we just call ShowAddToGitHubRepo but that might be too much.
	// For now let's implement the device flow here or reuse ghauth.
	ctx := context.Background()
	clientID := "Ov23liAIKfguW2oYiore"
	clientSecret := os.Getenv("GITHUB_OAUTH_SECRET")

	go func() {
		deviceRes, err := ghauth.RequestDeviceCode(ctx, clientID)
		if err != nil {
			tui.App.QueueUpdateDraw(func() {
				sneatnav.ShowErrorModal(tui, fmt.Errorf("failed to request device code: %w", err))
			})
			return
		}

		tui.App.QueueUpdateDraw(func() {
			statusText := tview.NewTextView().
				SetDynamicColors(true).
				SetTextAlign(tview.AlignCenter).
				SetText(fmt.Sprintf("\nGo to %s\n\nEnter code: [yellow]%s[-]\n\nWaiting for authorization...", deviceRes.VerificationURI, deviceRes.UserCode))

			form := tview.NewForm().
				AddButton("Cancel", func() {
					goCreateProjectScreen(tui, createAtGitHub)
				})
			form.SetButtonsAlign(tview.AlignCenter)

			flex := tview.NewFlex().SetDirection(tview.FlexRow).
				AddItem(statusText, 0, 1, false).
				AddItem(form, 3, 1, true)
			flex.SetBorder(true).SetTitle("GitHub Device Activation")

			panel := sneatnav.NewPanel(tui, sneatv.WithDefaultBorders(flex, flex.Box))
			tui.SetPanels(nil, panel)

			go func() {
				token, err := ghauth.PollForToken(ctx, clientID, clientSecret, deviceRes.DeviceCode, deviceRes.Interval, nil)
				if err != nil {
					tui.App.QueueUpdateDraw(func() {
						sneatnav.ShowErrorModal(tui, fmt.Errorf("authentication failed: %w", err))
					})
					return
				}
				_ = ghauth.SaveToken(token)

				client, err := githubClient(ctx, token)
				if err != nil {
					tui.App.QueueUpdateDraw(func() {
						sneatnav.ShowErrorModal(tui, fmt.Errorf("failed to create GitHub client: %w", err))
					})
					return
				}
				user, _, _ := client.Users.Get(ctx, "")

				tui.App.QueueUpdateDraw(func() {
					onSuccess(user.GetLogin())
					goCreateProjectScreen(tui, createAtGitHub)
				})
			}()
		})
	}()
}

func handleCreateProject(tui *sneatnav.TUI, createAt createTarget, projectID, title, location, repoName string, visibility datatug.ProjectVisibility) {
	var projectRef dtconfig.ProjectRef
	var err error
	switch createAt {
	case createAtLocal:
		projectRef, err = createLocalProject(tui, projectID, title, location)
	case createAtGitHub:
		projectRef, err = createGitHubProject(tui, repoName, visibility)
	}
	if err != nil {
		sneatnav.ShowErrorModal(tui, fmt.Errorf("failed to create project: %w", err))
		return
	}
	// Open project
	openProject(tui, projectRef)
}

// createLocalProject writes a new project under location.
//
// The directory is named after projectID, not the title: the id is the
// caller-supplied, charset-restricted name a project is addressed by
// (datatug-core v0.39.0), while a title is free text that may contain path
// separators, "..", whitespace or characters a file system cannot store.
// The title is recorded inside the project file, where it belongs.
func createLocalProject(tui *sneatnav.TUI, projectID, title, location string) (projectRef dtconfig.ProjectRef, err error) {
	fullPath := fsutils.ExpandHome(location)
	projectPath := filepath.Join(fullPath, projectID)

	if err = os.MkdirAll(projectPath, 0755); err != nil {
		sneatnav.ShowErrorModal(tui, fmt.Errorf("failed to create project directory: %w", err))
		return
	}

	datatugDir := filepath.Join(projectPath, "datatug")
	if err = os.MkdirAll(datatugDir, 0755); err != nil {
		sneatnav.ShowErrorModal(tui, fmt.Errorf("failed to create datatug directory: %w", err))
		return
	}

	// Create datatug-project.json. Marshalled rather than formatted into a
	// template: a title is free text now that the id carries the naming
	// rules, so a quote or a backslash in it would otherwise write a file
	// that is not JSON at all.
	var configContent []byte
	if configContent, err = json.MarshalIndent(struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}{ID: projectID, Title: title}, "", "  "); err != nil {
		sneatnav.ShowErrorModal(tui, fmt.Errorf("failed to build project config: %w", err))
		return
	}
	configFilePath := filepath.Join(datatugDir, storage.ProjectSummaryFileName)
	if err = os.WriteFile(configFilePath, configContent, 0644); err != nil {
		sneatnav.ShowErrorModal(tui, fmt.Errorf("failed to create project config: %w", err))
		return
	}

	// Add to app settings. The id is recorded here too:
	// dtconfig.AddProjectToSettings rejects a project whose ID matches one
	// already listed, so leaving it empty made the second locally created
	// project collide with the first ("project already exists, id: ").
	projectRef = dtconfig.ProjectRef{
		ID:    projectID,
		Path:  projectPath,
		Title: title,
	}
	if err = dtconfig.AddProjectToSettings(projectRef); err != nil {
		sneatnav.ShowErrorModal(tui, fmt.Errorf("failed to update app settings: %w", err))
		return
	}
	return projectRef, err
}

func openProject(tui *sneatnav.TUI, projectRef dtconfig.ProjectRef) {
	store := filestore.NewProjectStore(projectRef.ID, projectRef.Path)
	projectCtx := NewProjectContext(tui, store, projectRef)
	GoDatatugProjectScreen(projectCtx)
}

func createGitHubProject(tui *sneatnav.TUI, title string, visibility datatug.ProjectVisibility) (projectRef dtconfig.ProjectRef, err error) {
	ctx := context.Background()
	token, err := ghauth.GetToken()
	if err != nil || token == nil {
		sneatnav.ShowErrorModal(tui, fmt.Errorf("GitHub authentication required"))
		return
	}

	ts := oauth2.StaticTokenSource(token)
	tc := oauth2.NewClient(ctx, ts)
	client, err := github.NewClient(github.WithHTTPClient(tc))
	if err != nil {
		return projectRef, fmt.Errorf("failed to create GitHub client: %w", err)
	}

	var projectID string
	projectsStore := dtgithub.NewRepoProjectsStore(client, "")

	_, err = projectsStore.CreateNewProject(ctx, projectID, title, visibility, func(step string, status string) {

	})

	return projectRef, err
}
