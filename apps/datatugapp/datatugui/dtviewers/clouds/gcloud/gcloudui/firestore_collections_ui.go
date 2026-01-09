package gcloudui

import (
	"context"
	"fmt"

	"cloud.google.com/go/firestore"
	"github.com/datatug/datatug-cli/pkg/auth/gauth"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/filetug/pkg/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
)

func goFirestoreCollections(gcProjCtx *CGProjectContext) error {
	breadcrumbs := newGCloudProjectBreadcrumbs(gcProjCtx)
	breadcrumbs.Push(sneatv.NewBreadcrumb("Firestore", nil))
	menu := firestoreMainMenu(gcProjCtx, firestoreScreenCollections, "")

	list := tview.NewList()
	sneatv.DefaultBorderWithPadding(list.Box)
	title := "Firestore Collections"
	if gcProjCtx.Project != nil && gcProjCtx.Project.ProjectId != "" {
		title += " — " + gcProjCtx.Project.ProjectId
	}
	list.SetTitle(title)
	content := sneatnav.NewPanel(gcProjCtx.TUI, sneatv.WithDefaultBorders(list, list.Box))

	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyLeft {
			gcProjCtx.TUI.SetFocus(menu)
			return nil
		}
		return event
	})

	list.AddItem("Loading...", "Fetching root collections", 0, nil)

	// Load collections asynchronously to avoid blocking UI
	go func() {
		ctx := context.Background()

		collections, err := gcProjCtx.Schema().GetCollections(ctx, nil)
		if err != nil {
			gcProjCtx.TUI.App.QueueUpdateDraw(func() {
				list.Clear()
				addAuthErrorItems(gcProjCtx, list, err)
			})
			return
		}

		gcProjCtx.TUI.App.QueueUpdateDraw(func() {
			list.Clear()
			if len(collections) == 0 {
				list.AddItem("No collections", "The Firestore database has no root collections", 0, nil)
				return
			}
			for _, collection := range collections {
				list.AddItem("📋 "+collection.ID, "", 0, func() {
					if err := goFirestoreCollection(gcProjCtx, collection, sneatnav.FocusToContent); err != nil {
						panic(err)
					}
				})
			}
		})
	}()

	gcProjCtx.TUI.SetPanels(menu, content, sneatnav.WithFocusTo(sneatnav.FocusToContent))
	return nil
}

// newFirestoreClient attempts to build a Firestore client using an OAuth2 TokenSource
// derived from a refresh token stored by our gauth package; falls back to ADC if not available.
func newFirestoreClient(ctx context.Context, projectID string) (*firestore.Client, error) {
	if projectID == "" {
		return nil, fmt.Errorf("project ID is empty")
	}

	// Try to use refresh token from keychain via gauth
	if rt, err := gauth.GetRefreshToken(); err == nil && rt != "" {
		// Use a desktop-app OAuth2 client with cloud-platform and datastore scopes
		cfg := &oauth2.Config{
			// These values mirror the desktop app config used in gauth
			ClientID:     "588648831063-393c7c5gfj70sstaioked6qpb0sfj87h.apps.googleusercontent.com",
			ClientSecret: "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",
			Scopes: []string{
				"https://www.googleapis.com/auth/cloud-platform",
				"https://www.googleapis.com/auth/datastore",
			},
			Endpoint:    google.Endpoint,
			RedirectURL: "http://localhost:8080/oauth2callback",
		}
		tok := &oauth2.Token{RefreshToken: rt}
		ts := cfg.TokenSource(ctx, tok)
		if client, err := firestore.NewClient(ctx, projectID, option.WithTokenSource(ts)); err == nil {
			return client, nil
		}
		// If it failed (e.g., invalid_grant), we will fall back to ADC below.
	}

	// Fallback: ADC
	return firestore.NewClient(ctx, projectID)
}

// addAuthErrorItems renders an error with recovery actions.
func addAuthErrorItems(gcProjCtx *CGProjectContext, list *tview.List, err error) {
	// Show base error
	list.AddItem("Error", err.Error(), 0, nil)

	// Tailored hint for insufficient scopes
	e := err.Error()
	if containsInsufficientScopes(e) {
		list.AddItem("Hint: Missing Firestore scopes", "Your sign-in lacks Datastore/Firestore scopes. Re-login to grant access.", 0, nil)
		// Action: Re-login with Firestore scopes
		list.AddItem("Re-login (add Firestore scope)", "Open browser to re-consent and save new token", 'l', func() {
			go func() {
				_, _ = gauth.StartInteractiveLogin(context.Background(), []string{
					"https://www.googleapis.com/auth/cloud-platform",
					"https://www.googleapis.com/auth/datastore",
				})
				// After login attempt, retry screen
				gcProjCtx.TUI.App.QueueUpdateDraw(func() {
					_ = goFirestoreCollections(gcProjCtx)
				})
			}()
		})
		// Action: Forget saved login
		list.AddItem("Forget saved login", "Delete saved refresh token to force re-consent", 'f', func() {
			_ = gauth.DeleteRefreshToken()
			_ = goFirestoreCollections(gcProjCtx)
		})
	} else {
		// Generic hint for invalid_grant etc.
		list.AddItem("Hint: Check time sync", "Ensure your system clock is correct (auto time on)", 0, nil)
	}

	// Action: Retry
	list.AddItem("Retry", "Try loading collections again", 'r', func() {
		_ = goFirestoreCollections(gcProjCtx)
	})

	// Action: Open Credentials screen
	list.AddItem("Open Credentials", "Configure or refresh Google auth", 'c', func() {
		_ = GoCredentials(gcProjCtx.GCloudContext, sneatnav.FocusToContent)
	})

	// Action: ADC login help
	list.AddItem("How to login with gcloud (ADC)", "Run: gcloud auth application-default login", 'g', func() {})
}

// containsInsufficientScopes returns true if the error string indicates missing auth scopes
func containsInsufficientScopes(errStr string) bool {
	if errStr == "" {
		return false
	}
	// Common markers from Google APIs
	if contains(errStr, "ACCESS_TOKEN_SCOPE_INSUF") || contains(errStr, "insufficient authentication scopes") || contains(errStr, "insufficient scopes") {
		return true
	}
	return false
}

// small helper to avoid importing strings for minor usage
func contains(s, sub string) bool {
	// simple substring search
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
