package dtproject

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/datatug/datatug-cli/pkg/auth/ghauth"
	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/datatug-core/dtconfig"
	"github.com/datatug/datatug-cli/pkg/datatug-core/storage"
	"github.com/datatug/datatug-cli/pkg/datatug-core/storage/filestore"
	"github.com/datatug/datatug-cli/pkg/dtgithub"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/google/go-github/v80/github"
	"github.com/pkg/browser"
	"github.com/rivo/tview"
	"golang.org/x/oauth2"
)

type createTarget string

const (
	createAtLocal  createTarget = "Local"
	createAtGitHub createTarget = "GitHub"
)

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

	flex := tview.NewFlex().SetDirection(tview.FlexRow)

	tabs := sneatv.NewTabs(sneatv.WithRadio(), sneatv.WithLabel("Save to: "))
	tabs.AddTab(&sneatv.Tab{
		ID:        "GitHub",
		Title:     "GitHub",
		Primitive: tview.NewTextView().SetText("GitHub content"),
	})
	tabs.AddTab(&sneatv.Tab{
		ID:        "BitBucket",
		Title:     "BitBucket",
		Primitive: tview.NewTextView().SetText("BitBucket content"),
	})
	tabs.AddTab(&sneatv.Tab{
		ID:        "local",
		Title:     "Locally",
		Primitive: tview.NewTextView().SetText("Local content"),
	})

	flex.AddItem(tabs, 3, 0, true)

	//sneatv.DefaultBorderWithoutPadding(flex.Box)
	//flex.AddItem(tview.NewTextView(), 1, 0, false)
	flex.SetTitle("New Project")

	// --- Form ---
	form := tview.NewForm()

	var refreshForm func()

	// --- Menu List for "Create at" ---
	list := tview.NewList()
	list.SetTitle(" Create at ").SetBorder(true)
	list.AddItem("GitHub", "", 'g', func() {
		createAt = "GitHub"
		refreshForm()
	})
	list.AddItem("Local", "", 'l', func() {
		createAt = "Local"
		refreshForm()
	})

	// Set current selection based on default
	if createAt == "GitHub" {
		list.SetCurrentItem(0)
	} else {
		list.SetCurrentItem(1)
	}

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
					client := github.NewClient(oauth2.NewClient(context.Background(), oauth2.StaticTokenSource(token)))
					_, _, err := client.Repositories.Get(context.Background(), githubOwner, repoName)
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
			if createAt != "Local" {
				updateGithubPath()
			}
		})

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
						client := github.NewClient(oauth2.NewClient(context.Background(), oauth2.StaticTokenSource(token)))
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
			if strings.TrimSpace(title) == "" {
				sneatnav.ShowErrorModal(tui, fmt.Errorf("project title is required"))
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
			handleCreateProject(tui, createAt, title, location, repoName, projectVisibility)
		})
		form.AddButton("Cancel", func() {
			_ = GoDataTugProjectsScreen(tui, sneatnav.FocusToContent)
		})
	}

	refreshForm()

	flex.AddItem(form, 0, 1, true)

	menuPanel := sneatnav.NewPanel(tui, sneatv.WithDefaultBorders(list, list.Box))
	contentPanel := sneatnav.NewPanel(tui, sneatv.WithBordersWithoutPadding(flex, flex.Box))
	tui.SetPanels(menuPanel, contentPanel)
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

				client := github.NewClient(oauth2.NewClient(ctx, oauth2.StaticTokenSource(token)))
				user, _, _ := client.Users.Get(ctx, "")

				tui.App.QueueUpdateDraw(func() {
					onSuccess(user.GetLogin())
					goCreateProjectScreen(tui, createAtGitHub)
				})
			}()
		})
	}()
}

func handleCreateProject(tui *sneatnav.TUI, createAt createTarget, title, location, repoName string, visibility datatug.ProjectVisibility) {
	var projectRef dtconfig.ProjectRef
	var err error
	switch createAt {
	case createAtLocal:
		projectRef, err = createLocalProject(tui, title, location)
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

func createLocalProject(tui *sneatnav.TUI, name, location string) (projectRef dtconfig.ProjectRef, err error) {
	fullPath := filestore.ExpandHome(location)
	projectPath := filepath.Join(fullPath, name)

	if err = os.MkdirAll(projectPath, 0755); err != nil {
		sneatnav.ShowErrorModal(tui, fmt.Errorf("failed to create project directory: %w", err))
		return
	}

	datatugDir := filepath.Join(projectPath, "datatug")
	if err = os.MkdirAll(datatugDir, 0755); err != nil {
		sneatnav.ShowErrorModal(tui, fmt.Errorf("failed to create datatug directory: %w", err))
		return
	}

	// Create datatug-project.json
	configContent := fmt.Sprintf(`{
  "id": "%s",
  "title": "%s"
}`, name, name)
	configFilePath := filepath.Join(datatugDir, storage.ProjectSummaryFileName)
	if err = os.WriteFile(configFilePath, []byte(configContent), 0644); err != nil {
		sneatnav.ShowErrorModal(tui, fmt.Errorf("failed to create project config: %w", err))
		return
	}

	// Add to app settings
	if err = dtconfig.AddProjectToSettings(dtconfig.ProjectRef{
		Path: projectPath,
	}); err != nil {
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
	client := github.NewClient(tc)

	var projectID string
	projectsStore := dtgithub.NewRepoProjectsStore(client, "")

	_, err = projectsStore.CreateNewProject(ctx, projectID, title, visibility, func(step string, status string) {

	})

	return projectRef, err
}
