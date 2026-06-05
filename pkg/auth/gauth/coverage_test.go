package gauth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

// ---------------------------------------------------------------------------
// command.go
// ---------------------------------------------------------------------------

func TestGoogleAuthCommand(t *testing.T) {
	cmd := GoogleAuthCommand()
	if cmd == nil {
		t.Fatal("GoogleAuthCommand returned nil")
	}
	if cmd.Name != "google" {
		t.Fatalf("unexpected Name: %q", cmd.Name)
	}
	if cmd.Description == "" {
		t.Fatal("expected non-empty Description")
	}
	// Invoke the Action closure to cover the nil-return branch.
	if err := cmd.Action(context.Background(), cmd); err != nil {
		t.Fatalf("Action returned error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// authenticate.go – keyring functions via MockInit
// ---------------------------------------------------------------------------

func TestSaveAndGetRefreshToken(t *testing.T) {
	keyring.MockInit()
	const token = "test-refresh-token"
	if err := saveRefreshToken(token); err != nil {
		t.Fatalf("saveRefreshToken: %v", err)
	}
	got, err := GetRefreshToken()
	if err != nil {
		t.Fatalf("GetRefreshToken: %v", err)
	}
	if got != token {
		t.Fatalf("got %q, want %q", got, token)
	}
}

func TestGetRefreshToken_NotFound(t *testing.T) {
	keyring.MockInit()
	_, err := GetRefreshToken()
	if err == nil {
		t.Fatal("expected error when no token stored")
	}
}

func TestDeleteRefreshToken(t *testing.T) {
	keyring.MockInit()
	const token = "to-delete"
	if err := saveRefreshToken(token); err != nil {
		t.Fatalf("saveRefreshToken: %v", err)
	}
	if err := DeleteRefreshToken(); err != nil {
		t.Fatalf("DeleteRefreshToken: %v", err)
	}
	_, err := GetRefreshToken()
	if !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// authenticate.go – StartInteractiveLogin via getTokenFromWebFn seam
// ---------------------------------------------------------------------------

func TestStartInteractiveLogin_DefaultScopes(t *testing.T) {
	keyring.MockInit()
	orig := getTokenFromWebFn
	defer func() { getTokenFromWebFn = orig }()

	returned := &oauth2.Token{AccessToken: "acc", RefreshToken: "ref"}
	getTokenFromWebFn = func(_ context.Context, _ *oauth2.Config) (*oauth2.Token, error) {
		return returned, nil
	}

	tok, err := StartInteractiveLogin(context.Background(), nil) // nil → defaults scopes branch
	if err != nil {
		t.Fatalf("StartInteractiveLogin: %v", err)
	}
	if tok.AccessToken != "acc" {
		t.Fatalf("unexpected token: %+v", tok)
	}
	// Verify refresh token was persisted to mock keyring.
	got, err := GetRefreshToken()
	if err != nil {
		t.Fatalf("GetRefreshToken after login: %v", err)
	}
	if got != "ref" {
		t.Fatalf("stored refresh token %q, want %q", got, "ref")
	}
}

func TestStartInteractiveLogin_NoRefreshToken(t *testing.T) {
	keyring.MockInit()
	orig := getTokenFromWebFn
	defer func() { getTokenFromWebFn = orig }()

	// Token without RefreshToken — saveRefreshToken must NOT be called.
	getTokenFromWebFn = func(_ context.Context, _ *oauth2.Config) (*oauth2.Token, error) {
		return &oauth2.Token{AccessToken: "acc2"}, nil
	}

	tok, err := StartInteractiveLogin(context.Background(), []string{"https://www.googleapis.com/auth/cloud-platform"})
	if err != nil {
		t.Fatalf("StartInteractiveLogin: %v", err)
	}
	if tok.AccessToken != "acc2" {
		t.Fatalf("unexpected token: %+v", tok)
	}
}

func TestStartInteractiveLogin_Error(t *testing.T) {
	keyring.MockInit()
	orig := getTokenFromWebFn
	defer func() { getTokenFromWebFn = orig }()

	getTokenFromWebFn = func(_ context.Context, _ *oauth2.Config) (*oauth2.Token, error) {
		return nil, fmt.Errorf("browser unavailable")
	}

	_, err := StartInteractiveLogin(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error from StartInteractiveLogin")
	}
}

// ---------------------------------------------------------------------------
// file_store.go – Validate empty path
// ---------------------------------------------------------------------------

func TestFileStore_Validate_EmptyPath(t *testing.T) {
	err := FileStore{}.Validate()
	if err == nil {
		t.Fatal("expected error for empty Filepath")
	}
}

// ---------------------------------------------------------------------------
// file_store.go – DefaultFilepath error branch via userConfigDir seam
// ---------------------------------------------------------------------------

func TestDefaultFilepath_Error(t *testing.T) {
	orig := userConfigDir
	defer func() { userConfigDir = orig }()
	userConfigDir = func() (string, error) {
		return "", fmt.Errorf("no home dir")
	}
	_, err := DefaultFilepath()
	if err == nil {
		t.Fatal("expected error when userConfigDir fails")
	}
}

// ---------------------------------------------------------------------------
// file_store.go – ensureDir Validate-failure (via Save with empty Filepath)
// ---------------------------------------------------------------------------

func TestFileStore_ensureDir_ValidateFailure(t *testing.T) {
	err := FileStore{}.Save(nil)
	if err == nil {
		t.Fatal("expected error for empty Filepath in Save")
	}
}

// ---------------------------------------------------------------------------
// file_store.go – Load Validate-failure
// ---------------------------------------------------------------------------

func TestFileStore_Load_ValidateFailure(t *testing.T) {
	_, err := FileStore{}.Load()
	if err == nil {
		t.Fatal("expected error for empty Filepath in Load")
	}
}

// ---------------------------------------------------------------------------
// file_store.go – Load non-ErrNotExist read error (point at a directory)
// ---------------------------------------------------------------------------

func TestFileStore_Load_ReadError(t *testing.T) {
	dir := t.TempDir()
	// Use the dir itself as the Filepath — os.ReadFile on a directory yields
	// a non-ErrNotExist error on all platforms.
	store := FileStore{Filepath: dir}
	_, err := store.Load()
	if err == nil {
		t.Fatal("expected read error when Filepath is a directory")
	}
}

// ---------------------------------------------------------------------------
// file_store.go – Load empty file
// ---------------------------------------------------------------------------

func TestFileStore_Load_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(p, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	list, err := FileStore{Filepath: p}.Load()
	if err != nil {
		t.Fatalf("Load empty file: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty slice, got %d", len(list))
	}
}

// ---------------------------------------------------------------------------
// file_store.go – Load malformed JSON
// ---------------------------------------------------------------------------

func TestFileStore_Load_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(p, []byte("{bad json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := FileStore{Filepath: p}.Load()
	if err == nil {
		t.Fatal("expected unmarshal error for malformed JSON")
	}
}

// ---------------------------------------------------------------------------
// file_store.go – Save ensureDir failure (parent is a regular file)
// ---------------------------------------------------------------------------

func TestFileStore_Save_EnsureDirFailure(t *testing.T) {
	dir := t.TempDir()
	// Create a regular file where a directory is needed as parent.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Filepath under the blocker file (which is not a directory).
	store := FileStore{Filepath: filepath.Join(blocker, "accounts.json")}
	err := store.Save([]ServiceAccountDbo{})
	if err == nil {
		t.Fatal("expected error when parent path is a regular file")
	}
}

// ---------------------------------------------------------------------------
// file_store.go – Save MarshalIndent failure via jsonMarshalIndent seam
// ---------------------------------------------------------------------------

func TestFileStore_Save_MarshalFailure(t *testing.T) {
	dir := t.TempDir()
	orig := jsonMarshalIndent
	defer func() { jsonMarshalIndent = orig }()
	jsonMarshalIndent = func(_ interface{}, _, _ string) ([]byte, error) {
		return nil, fmt.Errorf("marshal error")
	}
	store := FileStore{Filepath: dir + "/accounts.json"}
	err := store.Save([]ServiceAccountDbo{})
	if err == nil {
		t.Fatal("expected error from injected jsonMarshalIndent")
	}
}

// ---------------------------------------------------------------------------
// file_store.go – Save os.WriteFile failure via osWriteFile seam
// ---------------------------------------------------------------------------

func TestFileStore_Save_WriteFileFailure(t *testing.T) {
	dir := t.TempDir()
	orig := osWriteFile
	defer func() { osWriteFile = orig }()
	osWriteFile = func(_ string, _ []byte, _ os.FileMode) error {
		return fmt.Errorf("disk full")
	}
	store := FileStore{Filepath: dir + "/accounts.json"}
	err := store.Save([]ServiceAccountDbo{})
	if err == nil {
		t.Fatal("expected write error from injected osWriteFile")
	}
}

// ---------------------------------------------------------------------------
// authenticate.go – StartInteractiveLogin saveRefreshToken error (log path)
// ---------------------------------------------------------------------------

func TestStartInteractiveLogin_SaveTokenError(t *testing.T) {
	keyring.MockInitWithError(fmt.Errorf("keyring unavailable"))
	orig := getTokenFromWebFn
	defer func() { getTokenFromWebFn = orig }()

	// Return a token with a RefreshToken so saveRefreshToken is called,
	// but keyring will fail — the error is only logged, not returned.
	getTokenFromWebFn = func(_ context.Context, _ *oauth2.Config) (*oauth2.Token, error) {
		return &oauth2.Token{AccessToken: "acc", RefreshToken: "ref"}, nil
	}

	tok, err := StartInteractiveLogin(context.Background(), nil)
	if err != nil {
		t.Fatalf("StartInteractiveLogin should not return error on save failure: %v", err)
	}
	if tok.AccessToken != "acc" {
		t.Fatalf("unexpected token: %+v", tok)
	}
}

// ---------------------------------------------------------------------------
// service_account_menu.go – missing file
// ---------------------------------------------------------------------------

func TestProjectsFromServiceAccount_MissingFile(t *testing.T) {
	_, err := projectsFromServiceAccount("/nonexistent/path/sa.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// ---------------------------------------------------------------------------
// service_account_menu.go – malformed JSON
// ---------------------------------------------------------------------------

func TestProjectsFromServiceAccount_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(p, []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := projectsFromServiceAccount(p)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

// ---------------------------------------------------------------------------
// service_account_menu.go – no derivable project_id → empty slice
// ---------------------------------------------------------------------------

func TestProjectsFromServiceAccount_NoProjectID(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "noid.json")
	if err := os.WriteFile(p, []byte(`{"type":"service_account"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	projs, err := projectsFromServiceAccount(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(projs) != 0 {
		t.Fatalf("expected empty slice, got %d projects", len(projs))
	}
}
