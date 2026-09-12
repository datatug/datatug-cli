package endpoints

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/strongo/validation"
)

// These tests run the capture endpoint against datatug-core's real
// revisioned filestore released in datatug-core v0.28.1.

// realCaptureSetup serves a fresh semantic test project, committed to its
// own Git repository, as principal as/roles with --allow-writes, through
// the real captureStoreFor.
func realCaptureSetup(t *testing.T, as string, roles []string) (apicontract.Scope, string) {
	t.Helper()
	projectDir, projectID := writeSemanticTestProject(t)
	gitInit(t, projectDir)
	session, err := secureread.NewSession(secureread.SessionOptions{As: as, Roles: roles, PoliciesDir: projectDir + "/policies"})
	if err != nil {
		t.Fatalf("secureread.NewSession: %v", err)
	}
	pathsByID := map[string]string{projectID: projectDir}
	api.ConfigureSecureSession(session, pathsByID, api.Capabilities{AllowWrites: true})
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return filestore.NewStore("files", pathsByID)
	}
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	return apicontract.Scope{Project: projectID, Environment: semanticTestEnv, SecurityContextID: api.SecurityContextID()}, projectDir
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "fixture")
}

// treeDigest maps every file under dir (outside .git) to its SHA-256, so a
// refused write can be proven to have changed nothing.
func treeDigest(t *testing.T, dir string) map[string]string {
	t.Helper()
	digest := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.IsDir() {
			digest[path+"/"] = "dir"
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			digest[path] = "symlink -> " + target
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		digest[path] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func assertTreeUnchanged(t *testing.T, dir string, before map[string]string) {
	t.Helper()
	if after := treeDigest(t, dir); !reflect.DeepEqual(before, after) {
		t.Errorf("a refused capture changed the project tree:\nbefore %v\nafter  %v", before, after)
	}
}

func TestCaptureQuery_RealStore_CreateLandsAsAnUncommittedPair(t *testing.T) {
	scope, projectDir := realCaptureSetup(t, "admin", []string{"admin"})
	req := validCaptureRequest(scope)
	w := postCapture(t, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", w.Code, w.Body.String())
	}
	resp := decodeCaptureResponse(t, w)
	if !reflect.DeepEqual(resp.Query, req.Query) {
		t.Errorf("stored query = %+v, want %+v", resp.Query, req.Query)
	}

	folder := filepath.Join(projectDir, "queries", "customers")
	body, err := os.ReadFile(filepath.Join(folder, "customer-invoices-captured.query.dtql"))
	if err != nil {
		t.Fatalf("the DTQL body sidecar was not written: %v", err)
	}
	if string(body) != captureTestDTQL {
		t.Errorf("body = %q, want the captured DTQL", body)
	}
	metadata, err := os.ReadFile(filepath.Join(folder, "customer-invoices-captured.query.json"))
	if err != nil {
		t.Fatalf("the query metadata was not written: %v", err)
	}
	var stored datatug.QueryDef
	if err := json.Unmarshal(metadata, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Type != datatug.QueryTypeDTQL || stored.Purpose != req.Query.Purpose || stored.Text != "" {
		t.Errorf("metadata = %s", metadata)
	}
	if len(stored.Parameters) != 1 || stored.Parameters[0].Meta == nil || *stored.Parameters[0].Meta != (datatug.EntityFieldRef{Entity: "Customer", Field: "ID"}) {
		t.Errorf("the semantic parameter was not persisted with its Meta: %s", metadata)
	}
	wantCapture := datatug.QueryCapture{Author: "admin", Environment: semanticTestEnv, Source: semanticTestSource, Collection: "Invoice",
		Bindings: []datatug.QueryCaptureBinding{{ParameterID: "Customer.ID", Origin: "selection"}}}
	if stored.Capture == nil || !reflect.DeepEqual(*stored.Capture, wantCapture) {
		t.Errorf("capture = %+v, want %+v", stored.Capture, wantCapture)
	}
	if len(stored.Targets) != 1 || stored.Targets[0].Catalog != semanticTestSource {
		t.Errorf("targets = %+v, want the captured source", stored.Targets)
	}
	for _, forbidden := range []string{"defaultValue", "rows", "factId", "password"} {
		if strings.Contains(string(metadata), forbidden) {
			t.Errorf("metadata carries %q: %s", forbidden, metadata)
		}
	}

	projStore, err := api.ProjectStoreFor(scope.Project)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := projStore.(datatug.RevisionedQueriesStore).LoadQueryRevision(context.Background(), resp.QueryID)
	if err != nil {
		t.Fatalf("LoadQueryRevision: %v", err)
	}
	if string(loaded.Revision) != resp.Revision {
		t.Errorf("response revision %q is not the stored revision %q", resp.Revision, loaded.Revision)
	}

	if got := runGit(t, projectDir, "rev-list", "--count", "HEAD"); got != "1" {
		t.Errorf("capture made a Git commit: %s commits", got)
	}
	status := runGit(t, projectDir, "status", "--porcelain", "--untracked-files=all")
	for _, want := range []string{"?? queries/customers/customer-invoices-captured.query.json", "?? queries/customers/customer-invoices-captured.query.dtql"} {
		if !strings.Contains(status, want) {
			t.Errorf("git status does not show %q as an uncommitted change:\n%s", want, status)
		}
	}
}

func TestCaptureQuery_RealStore_UpdateAndStaleRevision(t *testing.T) {
	scope, projectDir := realCaptureSetup(t, "admin", []string{"admin"})
	created := decodeCaptureResponse(t, postCapture(t, validCaptureRequest(scope)))

	update := validCaptureRequest(scope)
	update.IfNoneMatch, update.IfMatch = false, created.Revision
	update.Query.Purpose = "Which invoices does this customer have, reviewed?"
	w := postCapture(t, update)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	updated := decodeCaptureResponse(t, w)
	if updated.Revision == created.Revision {
		t.Errorf("the revision did not change")
	}

	before := treeDigest(t, projectDir)
	stale := validCaptureRequest(scope)
	stale.IfNoneMatch, stale.IfMatch = false, created.Revision
	stale.Query.Purpose = "stale"
	w = postCapture(t, stale)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
	}
	if env := decodeCaptureError(t, w); env.Error.Code != string(codeRevisionConflict) || env.Error.Field != "ifMatch" {
		t.Errorf("error = %+v", env.Error)
	}
	assertTreeUnchanged(t, projectDir, before)
}

func TestCaptureQuery_RealStore_CreateOverAnExistingIDConflicts(t *testing.T) {
	scope, projectDir := realCaptureSetup(t, "admin", []string{"admin"})
	if w := postCapture(t, validCaptureRequest(scope)); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	before := treeDigest(t, projectDir)
	w := postCapture(t, validCaptureRequest(scope))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
	}
	if env := decodeCaptureError(t, w); env.Error.Field != "query.id" {
		t.Errorf("field = %q, want query.id", env.Error.Field)
	}
	assertTreeUnchanged(t, projectDir, before)
}

// TestCaptureQuery_RealStore_RefusalsWriteNothing covers refusals at each
// layer - request validation, authorization and the store's own location
// checks - against the real file system.
func TestCaptureQuery_RealStore_RefusalsWriteNothing(t *testing.T) {
	t.Run("a read-only principal", func(t *testing.T) {
		scope, projectDir := realCaptureSetup(t, "sam", []string{"support"})
		before := treeDigest(t, projectDir)
		assertCaptureError(t, postCapture(t, validCaptureRequest(scope)), nil, http.StatusForbidden, apicontract.ErrCodeAccessDenied, "")
		assertTreeUnchanged(t, projectDir, before)
	})
	t.Run("a traversing folder", func(t *testing.T) {
		scope, projectDir := realCaptureSetup(t, "admin", []string{"admin"})
		before := treeDigest(t, filepath.Dir(projectDir))
		req := validCaptureRequest(scope)
		req.Query.FolderPath = "../../outside"
		assertCaptureError(t, postCapture(t, req), nil, http.StatusBadRequest, apicontract.ErrCodeInvalidRequest, "query.folderPath")
		assertTreeUnchanged(t, filepath.Dir(projectDir), before)
	})
	t.Run("a Windows device name the store refuses", func(t *testing.T) {
		scope, projectDir := realCaptureSetup(t, "admin", []string{"admin"})
		before := treeDigest(t, projectDir)
		req := validCaptureRequest(scope)
		req.Query.ID = "con"
		assertCaptureError(t, postCapture(t, req), nil, http.StatusBadRequest, apicontract.ErrCodeInvalidRequest, "query.id")
		assertTreeUnchanged(t, projectDir, before)
	})
	t.Run("a folder that is a symlink out of the project", func(t *testing.T) {
		scope, projectDir := realCaptureSetup(t, "admin", []string{"admin"})
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(projectDir, "queries", "linked")); err != nil {
			t.Fatal(err)
		}
		before := treeDigest(t, projectDir)
		req := validCaptureRequest(scope)
		req.Query.FolderPath = "linked"
		assertCaptureError(t, postCapture(t, req), nil, http.StatusBadRequest, apicontract.ErrCodeInvalidRequest, "query.folderPath")
		assertTreeUnchanged(t, projectDir, before)
		if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
			t.Errorf("the symlink target holds %d entries (%v); nothing should have been written there", len(entries), err)
		}
	})
}

func TestTranslateRevisionedStoreError(t *testing.T) {
	other := errors.New("disk full")
	tests := []struct {
		name  string
		err   error
		kind  captureStoreErrorKind
		field string
	}{
		{"an unsafe id", &datatug.InvalidQueryLocationError{ID: "..", Reason: "id: must not be \"..\""}, captureErrLocation, "query.id"},
		{"an unsafe folder", &datatug.InvalidQueryLocationError{FolderPath: "a/..", Reason: "folder path segment"}, captureErrLocation, "query.folderPath"},
		{"a symlinked folder", &datatug.InvalidQueryLocationError{FolderPath: "linked", ID: "q", Reason: "resolves through a symlink"}, captureErrLocation, "query.folderPath"},
		{"an incomplete record", fmt.Errorf("load: %w", &datatug.IncompleteQueryRecordError{ID: "q", Reason: "no body"}), captureErrIncomplete, "query.id"},
		{"a revision conflict", &datatug.QueryRevisionConflictError{ID: "q", Reason: "stale revision"}, captureErrConflict, ""},
		{"refused content", validation.NewErrBadRecordFieldValue("targets[0].catalog", "must not embed a password"), captureErrContent, "query"},
		{"a bad request", validation.NewBadRequestError(errors.New("bad")), captureErrContent, "query"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var refused *captureStoreError
			if !errors.As(translateRevisionedStoreError(tt.err), &refused) {
				t.Fatalf("expected a *captureStoreError for %v", tt.err)
			}
			if refused.Kind != tt.kind || refused.Field != tt.field || refused.Reason == "" {
				t.Errorf("got %+v, want kind %d field %q", refused, tt.kind, tt.field)
			}
		})
	}
	if got := translateRevisionedStoreError(other); got != other {
		t.Errorf("an unclassified error must pass through unchanged, got %v", got)
	}
}

// plainProjectStore is a ProjectStore with no revisioned query store.
type plainProjectStore struct{ datatug.ProjectStore }

func TestAdaptRevisionedStore_RequiresTheRevisionedInterface(t *testing.T) {
	if _, err := adaptRevisionedStore(plainProjectStore{}); !errors.Is(err, errCaptureStoreUnavailable) {
		t.Fatalf("expected errCaptureStoreUnavailable, got %v", err)
	}
}

func TestCapturedQueryDef_RoundTrips(t *testing.T) {
	record := capturedRecord{
		Query:  validCaptureRequest(apicontract.Scope{}).Query,
		Author: "admin", Environment: "local", Collection: "Invoice",
	}
	def := capturedQueryDef(record)
	if err := def.QueryDef.Validate(); err != nil {
		t.Fatalf("the mapped QueryDef is invalid: %v", err)
	}
	if got := capturedRecordOf(def); !reflect.DeepEqual(got, record) {
		t.Errorf("round trip = %+v, want %+v", got, record)
	}
	record.Query.Parameters, record.Query.BindingOrigins = []capturedParameter{}, []captureBindingOrigin{}
	record.Query.DTQL = "from:\n  name: Invoice\n"
	if got := capturedRecordOf(capturedQueryDef(record)); !reflect.DeepEqual(got, record) {
		t.Errorf("parameterless round trip = %+v, want %+v", got, record)
	}
}
