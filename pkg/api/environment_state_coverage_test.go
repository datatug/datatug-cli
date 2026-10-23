package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/gofrs/flock"
)

// keepEnvironmentSeams restores every seam an environment test may replace.
func keepEnvironmentSeams(t *testing.T) {
	t.Helper()
	now, files, readFiles, state, commit := environmentNow, environmentFilesSeam, environmentReadFilesSeam, environmentStateSeam, environmentCommitSeam
	gitState, gitOutput, lock := localGitStateSeam, localGitOutputSeam, flockTryLockContextSeam
	t.Cleanup(func() {
		environmentNow, environmentFilesSeam, environmentReadFilesSeam, environmentStateSeam, environmentCommitSeam = now, files, readFiles, state, commit
		localGitStateSeam, localGitOutputSeam, flockTryLockContextSeam = gitState, gitOutput, lock
	})
}

func wantEnvironmentCode(t *testing.T, err error, code string) {
	t.Helper()
	var failure *EnvironmentError
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("error = %v, want %s", err, code)
	}
}

func environmentOp(state *dto.EnvironmentState, n int) string {
	return state.ScopeBinding + fmt.Sprintf(".%08x-1234-4123-8123-123456789abc", n)
}

func environmentCreateRequest(state *dto.EnvironmentState, n int, name string) dto.CreateEnvironmentRequest {
	return dto.CreateEnvironmentRequest{EnvironmentMutation: dto.EnvironmentMutation{Scope: state.Scope, IfRevision: state.Revision, OperationID: environmentOp(state, n)}, Name: name}
}

func mustEnvironmentState(t *testing.T, scope dto.EnvironmentScope) *dto.EnvironmentState {
	t.Helper()
	state, err := LocalEnvironmentState(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func writeEnvironmentFile(t *testing.T, root, id, body string) {
	t.Helper()
	dir := filepath.Join(root, "environments", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".env.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestEnvironmentErrorMessageIsItsCode(t *testing.T) {
	if got := environmentError("ACCESS_DENIED", 403).Error(); got != "ACCESS_DENIED" {
		t.Fatal(got)
	}
}

func TestEnvironmentPolicyGrants(t *testing.T) {
	ctx := context.Background()
	scope, caps := environmentFixture(t)
	root, _ := servedProjectDir("p")
	paths := map[string]string{"p": root}

	// No serving principal: a refusal, never a 401.
	ConfigureSecureSession(secureread.Session{}, paths, caps)
	_, err := LocalEnvironmentState(ctx, scope)
	wantEnvironmentCode(t, err, "ACCESS_DENIED")

	// An explicit policy that denies reading.
	caps.EnvironmentMetadataPolicies["p"] = EnvironmentMetadataPolicy{PrincipalID: "actor-A"}
	ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "actor-A"}}, paths, caps)
	_, err = LocalEnvironmentState(ctx, scope)
	wantEnvironmentCode(t, err, "ACCESS_DENIED")

	// A project policy that names another principal: this one reads but never writes, even
	// unrestricted.
	ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "actor-B"}, Unrestricted: true}, paths, caps)
	if state := mustEnvironmentState(t, scope); !state.Capabilities.CanReadOrder || state.Capabilities.CanWriteOrder || state.Capabilities.CanCreateEnvironment {
		t.Fatalf("foreign-policy capabilities %+v", state.Capabilities)
	}
	delete(caps.EnvironmentMetadataPolicies, "p")

	// A policy-restricted principal with no metadata policy reads, as the legacy route lets it,
	// but may neither reorder nor create.
	ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "actor-B"}}, paths, caps)
	state := mustEnvironmentState(t, scope)
	if !state.Capabilities.CanReadOrder || state.Capabilities.CanWriteOrder || state.Capabilities.CanCreateEnvironment || state.Capabilities.Reasons["canWriteOrder"] == "" || state.Capabilities.Reasons["canCreateEnvironment"] == "" {
		t.Fatalf("capabilities %+v", state.Capabilities)
	}
	_, err = LocalCreateEnvironment(ctx, environmentCreateRequest(state, 1, "Dev"))
	wantEnvironmentCode(t, err, "ACCESS_DENIED")

	// An unrestricted session may change the order only with --allow-writes.
	caps.AllowWrites = false
	ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "admin"}, Unrestricted: true}, paths, caps)
	if state = mustEnvironmentState(t, scope); state.Capabilities.CanWriteOrder {
		t.Fatal("writes granted without --allow-writes")
	}
	caps.AllowWrites = true
	ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "admin"}, Unrestricted: true}, paths, caps)
	if state = mustEnvironmentState(t, scope); !state.Capabilities.CanWriteOrder || !state.Capabilities.CanCreateEnvironment {
		t.Fatalf("unrestricted capabilities %+v", state.Capabilities)
	}
}

func TestEnvironmentScopeValidation(t *testing.T) {
	ctx := context.Background()
	scope, caps := environmentFixture(t)
	root, _ := servedProjectDir("p")
	text := func(v string) *string { return &v }
	cases := map[string]struct {
		scope dto.EnvironmentScope
		code  string
	}{
		"other store":            {dto.EnvironmentScope{StoreID: "github.com", ProjectID: "p"}, "INVALID_REQUEST"},
		"space":                  {dto.EnvironmentScope{StoreID: LocalStoreID, ProjectID: "p", SpaceID: text("s")}, "INVALID_REQUEST"},
		"branch of a non-git":    {dto.EnvironmentScope{StoreID: LocalStoreID, ProjectID: "p", Branch: text("main")}, "ENVIRONMENT_REVISION_CONFLICT"},
		"another authority":      {dto.EnvironmentScope{StoreID: LocalStoreID, ProjectID: "p", AuthorityID: "authority-B"}, "OPERATION_CONFLICT"},
		"a resolved branch":      {dto.EnvironmentScope{StoreID: LocalStoreID, ProjectID: "p", ResolvedBranch: text("main")}, "ENVIRONMENT_REVISION_CONFLICT"},
		"another resolved scope": {dto.EnvironmentScope{StoreID: LocalStoreID, ProjectID: "p", Owner: text("o")}, "INVALID_REQUEST"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LocalEnvironmentState(ctx, c.scope)
			wantEnvironmentCode(t, err, c.code)
		})
	}
	if _, err := LocalEnvironmentState(ctx, dto.EnvironmentScope{StoreID: LocalStoreID, ProjectID: "unknown"}); err == nil {
		t.Fatal("an unserved project was read")
	}
	ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "actor-A"}}, map[string]string{"p": filepath.Join(root, "missing")}, caps)
	if _, err := LocalEnvironmentState(ctx, scope); err == nil {
		t.Fatal("a missing project directory was read")
	}

	// Without a configured authority the agent derives a stable one.
	caps.EnvironmentAuthorityID = ""
	ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "actor-A"}}, map[string]string{"p": root}, caps)
	first, second := mustEnvironmentState(t, scope), mustEnvironmentState(t, scope)
	if !strings.HasPrefix(first.Scope.AuthorityID, "local-") || first.Scope.AuthorityID != second.Scope.AuthorityID {
		t.Fatalf("derived authority %q %q", first.Scope.AuthorityID, second.Scope.AuthorityID)
	}
}

func TestEnvironmentGitFences(t *testing.T) {
	ctx := context.Background()
	keepEnvironmentSeams(t)
	scope, _ := environmentFixture(t)
	root, _ := servedProjectDir("p")
	gitDir := t.TempDir()
	calls := 0
	heads := map[int]string{}
	localGitStateSeam = func(context.Context, string) (gitState, error) {
		calls++
		head := heads[calls]
		if head == "" {
			head = "abc"
		}
		return gitState{root: root, gitDir: gitDir, branch: "main", head: head}, nil
	}
	reset := func(changed ...int) {
		calls = 0
		heads = map[int]string{}
		for _, n := range changed {
			heads[n] = "moved"
		}
	}

	reset()
	state := mustEnvironmentState(t, scope)
	if state.Scope.ResolvedBranch == nil || *state.Scope.ResolvedBranch != "main" {
		t.Fatalf("resolved branch %v", state.Scope.ResolvedBranch)
	}
	reset()
	main := "main"
	branchScope := scope
	branchScope.Branch = &main
	if _, err := LocalEnvironmentState(ctx, branchScope); err != nil {
		t.Fatal(err)
	}
	reset()
	other := "other"
	branchScope.Branch = &other
	_, err := LocalEnvironmentState(ctx, branchScope)
	wantEnvironmentCode(t, err, "ENVIRONMENT_REVISION_CONFLICT")

	// The branch head moves between the first look and the locked read, or during the read.
	for _, moved := range []int{2, 3} {
		reset(moved)
		_, err = LocalEnvironmentState(ctx, scope)
		wantEnvironmentCode(t, err, "ENVIRONMENT_REVISION_CONFLICT")
	}

	// A mutation also holds the project API lock in the Git directory.
	reset()
	result, err := LocalCreateEnvironment(ctx, environmentCreateRequest(state, 1, "Dev"))
	if err != nil || result.EnvironmentID != "dev" {
		t.Fatalf("create in git: %v %v", result, err)
	}
	reset()
	state = mustEnvironmentState(t, scope)
	reset(3) // the head moves just before the commit
	_, err = LocalCreateEnvironment(ctx, environmentCreateRequest(state, 2, "QA"))
	wantEnvironmentCode(t, err, "ENVIRONMENT_REVISION_CONFLICT")

	// Once committed, a later head move does not turn the durable write into a rejection.
	reset(4)
	if _, err = LocalCreateEnvironment(ctx, environmentCreateRequest(state, 4, "QA")); err != nil {
		t.Fatalf("head moved after the commit: %v", err)
	}

	reset()
	state = mustEnvironmentState(t, scope)
	reset()
	flockTryLockContextSeam = func(*flock.Flock, context.Context, time.Duration) (bool, error) { return false, nil }
	_, err = LocalCreateEnvironment(ctx, environmentCreateRequest(state, 3, "UAT"))
	wantEnvironmentCode(t, err, "TEMPORARILY_UNAVAILABLE")
}

func TestEnvironmentGitStateFallbacks(t *testing.T) {
	ctx := context.Background()
	keepEnvironmentSeams(t)
	scope, _ := environmentFixture(t)
	root, _ := servedProjectDir("p")
	failure := errors.New("git failure")
	localGitStateSeam = func(context.Context, string) (gitState, error) { return gitState{}, failure }
	answers := map[string]error{}
	localGitOutputSeam = func(_ context.Context, _ string, args ...string) (string, error) {
		if err := answers[strings.Join(args, " ")]; err != nil {
			return "", err
		}
		return "out-" + args[len(args)-1], nil
	}

	// An unborn branch: Git answers everything but HEAD.
	state := mustEnvironmentState(t, scope)
	if state.Scope.ResolvedBranch == nil || *state.Scope.ResolvedBranch != "out-HEAD" {
		t.Fatalf("unborn branch %v", state.Scope.ResolvedBranch)
	}
	// A detached HEAD has no branch to fence.
	answers["symbolic-ref --quiet --short HEAD"] = failure
	_, err := LocalEnvironmentState(ctx, scope)
	wantEnvironmentCode(t, err, "ENVIRONMENT_REVISION_CONFLICT")
	// No work tree top level: served as a plain folder.
	delete(answers, "symbolic-ref --quiet --short HEAD")
	answers["rev-parse --show-toplevel"] = failure
	if state = mustEnvironmentState(t, scope); state.Scope.ResolvedBranch != nil {
		t.Fatal("a folder without a work tree resolved a branch")
	}
	// Not a repository at all, unless a .git entry says Git is just unavailable.
	answers["rev-parse --absolute-git-dir"] = failure
	if state = mustEnvironmentState(t, scope); state.Scope.ResolvedBranch != nil {
		t.Fatal("a plain folder resolved a branch")
	}
	if err = os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: elsewhere"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = LocalEnvironmentState(ctx, scope)
	wantEnvironmentCode(t, err, "TEMPORARILY_UNAVAILABLE")
}

func TestEnvironmentLockedReadRechecksTheSession(t *testing.T) {
	ctx := context.Background()
	keepEnvironmentSeams(t)
	scope, caps := environmentFixture(t)
	root, _ := servedProjectDir("p")
	paths := map[string]string{"p": root}
	sessions := []secureread.Session{{}, {Principal: &access.Principal{ID: "actor-A"}}}
	for _, session := range sessions {
		ConfigureSecureSession(sessions[1], paths, caps)
		environmentReadFilesSeam = func(ctx context.Context, root string, fn func(*filestore.EnvironmentFiles) error) error {
			ConfigureSecureSession(session, paths, caps) // revoked, or replaced by a new generation
			return filestore.ReadEnvironmentFiles(ctx, root, fn)
		}
		_, err := LocalEnvironmentState(ctx, scope)
		wantEnvironmentCode(t, err, "ACCESS_DENIED")
	}
	environmentReadFilesSeam = filestore.ReadEnvironmentFiles
	ConfigureSecureSession(sessions[1], paths, caps)
	environmentStateSeam = func(ctx context.Context, f *filestore.EnvironmentFiles, s dto.EnvironmentScope, binding string, c dto.EnvironmentCapabilities, fence string) (*dto.EnvironmentState, []byte, error) {
		ConfigureSecureSession(sessions[1], paths, caps) // a new generation while reading
		return localEnvironmentState(ctx, f, s, binding, c, fence)
	}
	_, err := LocalEnvironmentState(ctx, scope)
	wantEnvironmentCode(t, err, "ACCESS_DENIED")
}

func TestEnvironmentStateReadFailures(t *testing.T) {
	ctx := context.Background()
	scope, _ := environmentFixture(t)
	root, _ := servedProjectDir("p")
	project := filepath.Join(root, "datatug-project.json")
	writeEnvironmentFile(t, root, "dev", `{"id":"dev","presentation":"loud"}`)
	if _, err := LocalEnvironmentState(ctx, scope); err == nil {
		t.Fatal("presentation metadata of the wrong shape was accepted")
	}
	writeEnvironmentFile(t, root, "dev", `{`)
	if _, err := LocalEnvironmentState(ctx, scope); err == nil {
		t.Fatal("a malformed environment was accepted")
	}
	if err := os.WriteFile(project, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LocalEnvironmentState(ctx, scope); err == nil {
		t.Fatal("a malformed project file was accepted")
	}
	if err := os.Remove(project); err != nil {
		t.Fatal(err)
	}
	if _, err := LocalEnvironmentState(ctx, scope); err == nil {
		t.Fatal("a missing project file was accepted")
	}
}

func TestEnvironmentCreateValidationAndIDs(t *testing.T) {
	ctx := context.Background()
	scope, _ := environmentFixture(t)
	root, _ := servedProjectDir("p")
	writeEnvironmentFile(t, root, "dev", `{"id":"dev","title":"Development"}`)
	writeEnvironmentFile(t, root, "qa", `{"id":"qa"}`)
	state := mustEnvironmentState(t, scope)
	for _, name := range []string{"  ", strings.Repeat("x", 201), "bad\tname"} {
		_, err := LocalCreateEnvironment(ctx, environmentCreateRequest(state, 1, name))
		wantEnvironmentCode(t, err, "INVALID_REQUEST")
	}
	for _, name := range []string{"Development", "qa"} { // a title, or the ID of an untitled one
		_, err := LocalCreateEnvironment(ctx, environmentCreateRequest(state, 1, name))
		wantEnvironmentCode(t, err, "DUPLICATE_NAME")
	}
	request := environmentCreateRequest(state, 1, "x")
	request.OperationID = strings.Repeat("0", 64) + ".12345678-1234-4123-8123-123456789abc"
	_, err := LocalCreateEnvironment(ctx, request)
	wantEnvironmentCode(t, err, "OPERATION_CONFLICT")
	request = environmentCreateRequest(state, 1, "x")
	request.IfRevision = "environment-revision/1.stale"
	_, err = LocalCreateEnvironment(ctx, request)
	wantEnvironmentCode(t, err, "ENVIRONMENT_REVISION_CONFLICT")

	for i, c := range []struct{ name, prefix string }{
		{"dev!", "dev-"},                         // the slug collides with a live ID
		{"!!!", ""},                              // no usable slug
		{strings.Repeat("Ab_", 40), "ab-ab-ab-"}, // a long slug is cut to 80 characters
	} {
		state = mustEnvironmentState(t, scope)
		result, err := LocalCreateEnvironment(ctx, environmentCreateRequest(state, 10+i, c.name))
		if err != nil {
			t.Fatal(c.name, err)
		}
		if !strings.HasPrefix(result.EnvironmentID, c.prefix) || len(result.EnvironmentID) > 80+37 || result.EnvironmentID == "dev" {
			t.Fatalf("%q -> %q", c.name, result.EnvironmentID)
		}
	}
}

func TestEnvironmentSaveOrderValidation(t *testing.T) {
	ctx := context.Background()
	scope, _ := environmentFixture(t)
	root, _ := servedProjectDir("p")
	if err := os.WriteFile(filepath.Join(root, "datatug-project.json"), []byte(`null`), 0o600); err != nil {
		t.Fatal(err)
	}
	writeEnvironmentFile(t, root, "dev", `{"id":"dev"}`)
	writeEnvironmentFile(t, root, "prod", `{"id":"prod"}`)
	state := mustEnvironmentState(t, scope)
	save := func(order ...string) error {
		_, err := LocalSaveEnvironmentOrder(ctx, dto.SaveEnvironmentOrderRequest{EnvironmentMutation: dto.EnvironmentMutation{Scope: state.Scope, IfRevision: state.Revision, OperationID: environmentOp(state, 1)}, EnvironmentOrder: order})
		return err
	}
	wantEnvironmentCode(t, save("dev"), "INVALID_REQUEST")
	wantEnvironmentCode(t, save("dev", "prod", "qa"), "INVALID_REQUEST")
	if err := save("prod", "dev"); err != nil { // a null project file still takes the order
		t.Fatal(err)
	}
	if state = mustEnvironmentState(t, scope); strings.Join(state.Order, ",") != "prod,dev" || state.NeedsRepair {
		t.Fatalf("saved %v", state.Order)
	}
}

func TestEnvironmentReplayExpiryAndOperationStatus(t *testing.T) {
	ctx := context.Background()
	keepEnvironmentSeams(t)
	scope, caps := environmentFixture(t)
	state := mustEnvironmentState(t, scope)
	status := func(id string) (*dto.EnvironmentOperationResult, error) {
		return LocalEnvironmentOperationStatus(ctx, dto.EnvironmentOperationStatusRequest{Scope: state.Scope, OperationID: id})
	}
	// Nothing recorded yet: the outcome is not knowable.
	_, err := status(environmentOp(state, 1))
	wantEnvironmentCode(t, err, "TEMPORARILY_UNAVAILABLE")
	_, err = status("not-an-operation")
	wantEnvironmentCode(t, err, "OPERATION_CONFLICT")

	environmentNow = func() time.Time { return time.Now().Add(-8 * 24 * time.Hour) }
	request := environmentCreateRequest(state, 1, "Dev")
	if _, err = LocalCreateEnvironment(ctx, request); err != nil {
		t.Fatal(err)
	}
	environmentNow = time.Now
	_, err = LocalCreateEnvironment(ctx, request)
	wantEnvironmentCode(t, err, "OPERATION_EXPIRED")
	receipt, err := status(request.OperationID)
	if err != nil || receipt.Status != "expired" || receipt.EnvironmentID != "dev" {
		t.Fatalf("status %+v %v", receipt, err)
	}
	_, err = status(environmentOp(state, 2))
	wantEnvironmentCode(t, err, "TEMPORARILY_UNAVAILABLE")

	// A receipt the agent cannot trust fails the replay instead of re-running the operation.
	receipts, _ := filepath.Glob(filepath.Join(caps.EnvironmentPrivateDir, "*", "*.receipt.json"))
	if len(receipts) == 0 {
		t.Fatal("no receipt was recorded")
	}
	for _, path := range receipts {
		if err = os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = LocalCreateEnvironment(ctx, request); err == nil {
		t.Fatal("an untrusted receipt was replayed")
	}
}

func TestEnvironmentMutationFences(t *testing.T) {
	ctx := context.Background()
	keepEnvironmentSeams(t)
	scope, caps := environmentFixture(t)
	root, _ := servedProjectDir("p")
	paths := map[string]string{"p": root}
	failure := errors.New("read failure")
	// failAt makes the n-th authoritative read of the next mutation return what change says.
	failAt := func(n int, change func(*dto.EnvironmentState) (*dto.EnvironmentState, error)) {
		calls := 0
		environmentStateSeam = func(ctx context.Context, f *filestore.EnvironmentFiles, s dto.EnvironmentScope, binding string, c dto.EnvironmentCapabilities, fence string) (*dto.EnvironmentState, []byte, error) {
			state, raw, err := localEnvironmentState(ctx, f, s, binding, c, fence)
			if calls++; calls == n && err == nil {
				state, err = change(state)
			}
			return state, raw, err
		}
	}
	cases := []struct {
		name  string
		n     int
		code  string
		state func(*dto.EnvironmentState) (*dto.EnvironmentState, error)
	}{
		{"first read fails", 1, "", func(*dto.EnvironmentState) (*dto.EnvironmentState, error) { return nil, failure }},
		{"reread fails", 2, "", func(*dto.EnvironmentState) (*dto.EnvironmentState, error) { return nil, failure }},
		{"revision moves", 2, "ENVIRONMENT_REVISION_CONFLICT", func(s *dto.EnvironmentState) (*dto.EnvironmentState, error) {
			s.Revision += "x"
			return s, nil
		}},
		{"grant revoked", 2, "ACCESS_DENIED", func(s *dto.EnvironmentState) (*dto.EnvironmentState, error) {
			revoked := caps
			revoked.AllowWrites = false
			ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "actor-A"}}, paths, revoked)
			return s, nil
		}},
		{"confirmation fails", 3, "OUTCOME_UNCERTAIN", func(*dto.EnvironmentState) (*dto.EnvironmentState, error) { return nil, failure }},
		{"confirmation differs", 3, "OUTCOME_UNCERTAIN", func(s *dto.EnvironmentState) (*dto.EnvironmentState, error) {
			s.Order = append(s.Order, "ghost")
			return s, nil
		}},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "actor-A"}}, paths, caps)
			environmentStateSeam = localEnvironmentState
			state := mustEnvironmentState(t, scope)
			failAt(c.n, c.state)
			_, err := LocalCreateEnvironment(ctx, environmentCreateRequest(state, 100+i, fmt.Sprintf("Env %d", i)))
			if c.code == "" {
				if !errors.Is(err, failure) {
					t.Fatalf("error = %v", err)
				}
				return
			}
			wantEnvironmentCode(t, err, c.code)
		})
	}
	environmentStateSeam = localEnvironmentState
	ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "actor-A"}}, paths, caps)
	environmentCommitSeam = func(*filestore.EnvironmentFiles, map[string][]byte, map[string]string, string, dto.EnvironmentOperationResult) error {
		return failure
	}
	state := mustEnvironmentState(t, scope)
	_, err := LocalCreateEnvironment(ctx, environmentCreateRequest(state, 200, "Lost"))
	wantEnvironmentCode(t, err, "OUTCOME_UNCERTAIN")

	// A session replaced right after the commit does not turn the durable write into a rejection.
	environmentCommitSeam = func(f *filestore.EnvironmentFiles, changes map[string][]byte, expected map[string]string, binding string, result dto.EnvironmentOperationResult) error {
		err := f.CommitChecked(changes, expected, binding, result)
		ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "actor-A"}}, paths, caps)
		return err
	}
	if _, err = LocalCreateEnvironment(ctx, environmentCreateRequest(state, 201, "Kept")); err != nil {
		t.Fatalf("session replaced after the commit: %v", err)
	}
}

func TestEnvironmentIDsCollideCaseInsensitively(t *testing.T) {
	scope, _ := environmentFixture(t)
	root, _ := servedProjectDir("p")
	writeEnvironmentFile(t, root, "Dev", `{"id":"Dev","title":"Mixed"}`)
	state := mustEnvironmentState(t, scope)
	result, err := LocalCreateEnvironment(context.Background(), environmentCreateRequest(state, 1, "dev"))
	if err != nil || !strings.HasPrefix(result.EnvironmentID, "dev-") {
		t.Fatalf("create dev next to Dev: %v %v", result, err)
	}
}

func TestQualifiedEnvironmentsFollowTheSharedOrder(t *testing.T) {
	dir := t.TempDir()
	writeEnvironmentFile(t, dir, "dev", `{"id":"dev"}`)
	writeEnvironmentFile(t, dir, "prod", `{"id":"prod"}`)
	writeEnvironmentFile(t, dir, "local", `{"id":"local"}`)
	project := filepath.Join(dir, "datatug-project.json")
	if err := os.WriteFile(project, []byte(`{"id":"p","environmentOrder":["prod","gone"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := openQualifiedProjectReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.close()
	environments, err := readQualifiedEnvironments(reader)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, v := range environments {
		ids = append(ids, v.env.ID)
	}
	if strings.Join(ids, ",") != "prod,local,dev" {
		t.Fatalf("order %v", ids)
	}
	if err = os.WriteFile(project, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = readQualifiedEnvironments(reader); err == nil {
		t.Fatal("a malformed project file was accepted")
	}
}
