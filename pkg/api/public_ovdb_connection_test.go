package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

func publicConnectionAPIFixture(t *testing.T) (dto.EnvironmentScope, Capabilities, string) {
	t.Helper()
	scope, caps := environmentFixture(t)
	root, err := servedProjectDir("p")
	if err != nil {
		t.Fatal(err)
	}
	body := `{"id":"p","databases":[{"id":"music","connectionId":"music-ovdb","ovdb":{"baseUrl":"https://public.example","databaseId":"music-db"}},{"id":"other","connectionId":"other-ovdb","ovdb":{"baseUrl":"https://public.example","databaseId":"other-db"}}],"credentials":{"password":"private-project"}}`
	if err = os.WriteFile(filepath.Join(root, "datatug-project.json"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{"dev", "prod"} {
		writeEnvironmentFile(t, root, env, `{"id":"`+env+`","dbServers":[],"custom":{"password":"private-env"}}`)
	}
	ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "actor-A"}, Unrestricted: true}, map[string]string{"p": root}, caps)
	return scope, caps, root
}

func publicConnectionRequest(t *testing.T, scope dto.EnvironmentScope) dto.CreatePublicOVDBConnectionRequest {
	t.Helper()
	state, err := LocalPublicOVDBConnectionState(context.Background(), scope, "dev", "music")
	if err != nil {
		t.Fatal(err)
	}
	return dto.CreatePublicOVDBConnectionRequest{EnvironmentMutation: dto.EnvironmentMutation{Scope: state.Scope, IfRevision: state.Revision, OperationID: state.ScopeBinding + ".12345678-1234-4123-8123-123456789abc"}, EnvironmentID: "dev", DatabaseID: "music"}
}

func TestPublicOVDBConnectionAPICreatesExactEnvironmentAndReplays(t *testing.T) {
	scope, _, root := publicConnectionAPIFixture(t)
	previousStore := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previousStore })
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("local", map[string]string{"p": root}) }
	ctx := context.Background()
	request := publicConnectionRequest(t, scope)
	original := request
	result, err := LocalCreatePublicOVDBConnection(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := GetEnvironmentSummary(ctx, dto.ProjectItemRef{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: "p"}, ID: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	summaryBody, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	var summaryWire struct {
		Connections []string `json:"editionConnections"`
	}
	if err = json.Unmarshal(summaryBody, &summaryWire); err != nil || len(summaryWire.Connections) != 1 || summaryWire.Connections[0] != "music-ovdb" {
		t.Fatalf("assignment lost on summary reload: %s %v", summaryBody, err)
	}
	if strings.Contains(string(summaryBody), "private-") || strings.Contains(string(summaryBody), "password") {
		t.Fatal("environment credentials escaped summary")
	}
	if result.ConnectionID != "music-ovdb" || result.DatabaseID != "music" || result.EnvironmentID != "dev" || result.Status != "committed" {
		t.Fatalf("bad receipt %+v", result)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "https://") || strings.Contains(string(encoded), "private-") || strings.Contains(string(encoded), root) {
		t.Fatal("receipt leaked source configuration")
	}
	replay, err := LocalCreatePublicOVDBConnection(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := json.Marshal(replay)
	if string(second) != string(encoded) {
		t.Fatal("replay changed")
	}
	state, err := LocalPublicOVDBConnectionState(ctx, scope, "dev", "music")
	if err != nil || state.CanAdd || state.Reason == "" {
		t.Fatalf("duplicate capability %+v %v", state, err)
	}
	request.OperationID = state.ScopeBinding + ".22345678-1234-4123-8123-123456789abc"
	request.IfRevision = state.Revision
	_, err = LocalCreatePublicOVDBConnection(ctx, request)
	wantEnvironmentCode(t, err, "CONNECTION_ALREADY_EXISTS")
	catalog, err := LocalProjectConnectionCatalog(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: "p"})
	if err != nil || len(catalog.Connections) != 1 || catalog.Connections[0].ID != "music-ovdb" {
		t.Fatalf("created catalog unavailable %+v %v", catalog, err)
	}
	prod, _ := os.ReadFile(filepath.Join(root, "environments", "prod", "prod.env.json"))
	if strings.Contains(string(prod), "music-ovdb") {
		t.Fatal("wrong environment changed")
	}
	request = original
	request.DatabaseID = "other"
	_, err = LocalCreatePublicOVDBConnection(ctx, request)
	wantEnvironmentCode(t, err, "OPERATION_CONFLICT")
}

func TestPublicOVDBConnectionAPIDeniesCatalogLargerThanPublicReaderBeforeWrite(t *testing.T) {
	scope, _, root := publicConnectionAPIFixture(t)
	request := publicConnectionRequest(t, scope)
	if err := os.Mkdir(filepath.Join(root, "connections"), 0700); err != nil {
		t.Fatal(err)
	}
	const retained = `{"format":"datatug-demo-connections/v1","connections":[{"id":"keep","dataset":"keep","storage":"ovdb","readiness":"public-api","environments":["prod"]}],"credentials":{"token":"private-existing"}}`
	if err := os.WriteFile(filepath.Join(root, "connections", "keep.json"), []byte(retained), 0600); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(root, "datatug-project.json")
	projectBody, _ := os.ReadFile(projectPath)
	var project map[string]any
	if err := json.Unmarshal(projectBody, &project); err != nil {
		t.Fatal(err)
	}
	project["databases"].([]any)[0].(map[string]any)["ovdb"].(map[string]any)["baseUrl"] = "https://public.example/" + strings.Repeat("a", 150000)
	largeProject, _ := json.Marshal(project)
	if err := os.WriteFile(projectPath, largeProject, 0600); err != nil {
		t.Fatal(err)
	}
	environmentPath := filepath.Join(root, "environments", "dev", "dev.env.json")
	before, _ := os.ReadFile(environmentPath)
	_, err := LocalPublicOVDBConnectionState(context.Background(), scope, "dev", "music")
	wantEnvironmentCode(t, err, "CONNECTION_UNAVAILABLE")
	_, err = LocalCreatePublicOVDBConnection(context.Background(), request)
	wantEnvironmentCode(t, err, "CONNECTION_UNAVAILABLE")
	after, _ := os.ReadFile(environmentPath)
	if string(before) != string(after) {
		t.Fatal("refused oversized descriptor changed environment")
	}
	afterProject, _ := os.ReadFile(projectPath)
	if string(afterProject) != string(largeProject) {
		t.Fatal("refused descriptor changed project metadata")
	}
	afterCatalog, _ := os.ReadFile(filepath.Join(root, "connections", "keep.json"))
	if string(afterCatalog) != retained {
		t.Fatal("refused descriptor changed existing catalog")
	}
	if _, err = os.Stat(filepath.Join(root, "connections", "music-ovdb.json")); !os.IsNotExist(err) {
		t.Fatal("oversized unreadable catalog was committed")
	}
	catalog, err := LocalProjectConnectionCatalog(context.Background(), dto.ProjectRef{StoreID: LocalStoreID, ProjectID: "p"})
	if err != nil || len(catalog.Connections) != 1 || catalog.Connections[0].ID != "keep" {
		t.Fatalf("existing public catalog unreadable %+v %v", catalog, err)
	}
}

func TestPublicOVDBConnectionLegacySummaryReadRetainsSafeAssignment(t *testing.T) {
	_, _, root := publicConnectionAPIFixture(t)
	path := filepath.Join(root, "environments", "dev", "dev.env.json")
	if err := os.WriteFile(path, []byte(`{"id":"dev","dbServers":[],"editionConnections":["music-ovdb"],"password":"private-env"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(root, "environments", "dev", "environment-summary.json")); err != nil {
		t.Fatal(err)
	}
	reader, err := openQualifiedProjectReader(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.close()
	env, _, err := readQualifiedEnvironment(reader, "dev")
	if err != nil || len(env.EditionConnections) != 1 || env.EditionConnections[0] != "music-ovdb" {
		t.Fatalf("legacy assignment lost %+v %v", env, err)
	}
}

func TestPublicOVDBConnectionAPIRejectsWrongScopeAndStaleSnapshot(t *testing.T) {
	for _, mode := range []string{"wrongenv", "wrongdb", "unknownenv", "unknowndb", "badenv", "baddb", "stale", "wrongoperation", "foreignauthority", "github"} {
		t.Run(mode, func(t *testing.T) {
			scope, _, root := publicConnectionAPIFixture(t)
			request := publicConnectionRequest(t, scope)
			code := "CONNECTION_REVISION_CONFLICT"
			switch mode {
			case "wrongenv":
				request.EnvironmentID = "prod"
			case "wrongdb":
				request.DatabaseID = "other"
			case "unknownenv":
				request.EnvironmentID = "unknown"
				code = "DATABASE_OR_ENVIRONMENT_NOT_FOUND"
			case "unknowndb":
				request.DatabaseID = "unknown"
				code = "DATABASE_OR_ENVIRONMENT_NOT_FOUND"
			case "badenv":
				request.EnvironmentID = "../dev"
				code = "INVALID_REQUEST"
			case "baddb":
				request.DatabaseID = "../music"
				code = "INVALID_REQUEST"
			case "stale":
				body, _ := os.ReadFile(filepath.Join(root, "datatug-project.json"))
				_ = os.WriteFile(filepath.Join(root, "datatug-project.json"), append(body, ' '), 0600)
			case "wrongoperation":
				request.OperationID = "invalid"
				code = "OPERATION_CONFLICT"
			case "foreignauthority":
				request.Scope.AuthorityID = "foreign"
				code = "OPERATION_CONFLICT"
			case "github":
				request.Scope.StoreID = "github.com"
				code = "INVALID_REQUEST"
			}
			_, err := LocalCreatePublicOVDBConnection(context.Background(), request)
			wantEnvironmentCode(t, err, code)
			if _, err = os.Stat(filepath.Join(root, "connections", "music-ovdb.json")); !os.IsNotExist(err) {
				t.Fatal("rejected write created descriptor")
			}
		})
	}
}

func TestPublicOVDBConnectionAPIRequiresWritableOperatorAndCurrentHead(t *testing.T) {
	for _, mode := range []string{"readonly", "limitedmetadata", "revoked", "wrongbranch", "stalehead", "headmoves", "commitfailure", "revoked-at-decision", "headmoves-at-decision"} {
		t.Run(mode, func(t *testing.T) {
			scope, caps, root := publicConnectionAPIFixture(t)
			oldGit, oldCommit := localGitStateSeam, publicConnectionCommit
			t.Cleanup(func() { localGitStateSeam = oldGit; publicConnectionCommit = oldCommit })
			gitDir := t.TempDir()
			head := "head-a"
			calls := 0
			localGitStateSeam = func(context.Context, string) (gitState, error) {
				calls++
				return gitState{root: root, gitDir: gitDir, branch: "main", head: head}, nil
			}
			branch := "main"
			scope.Branch = &branch
			request := publicConnectionRequest(t, scope)
			code := "ACCESS_DENIED"
			switch mode {
			case "readonly":
				caps.AllowWrites = false
				ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "actor-A"}, Unrestricted: true}, map[string]string{"p": root}, caps)
			case "limitedmetadata":
				ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "actor-A"}}, map[string]string{"p": root}, caps)
			case "revoked":
				ConfigureSecureSession(secureread.Session{}, nil, caps)
			case "wrongbranch":
				other := "other"
				request.Scope.Branch = &other
				code = "ENVIRONMENT_REVISION_CONFLICT"
			case "stalehead":
				head = "head-b"
				code = "CONNECTION_REVISION_CONFLICT"
			case "headmoves":
				calls = 0
				localGitStateSeam = func(context.Context, string) (gitState, error) {
					calls++
					if calls >= 3 {
						head = "head-b"
					}
					return gitState{root: root, gitDir: gitDir, branch: "main", head: head}, nil
				}
				code = "CONNECTION_REVISION_CONFLICT"
			case "commitfailure":
				publicConnectionCommit = func(context.Context, *filestore.EnvironmentFiles, filestore.PublicOVDBConnectionSnapshot, string, *dto.EnvironmentOperationResult, func() error) error {
					return errors.New("private storage failure")
				}
				code = "OUTCOME_UNCERTAIN"
			case "revoked-at-decision", "headmoves-at-decision":
				publicConnectionCommit = func(ctx context.Context, f *filestore.EnvironmentFiles, snapshot filestore.PublicOVDBConnectionSnapshot, binding string, result *dto.EnvironmentOperationResult, authorize func() error) error {
					return f.CommitPublicOVDBConnectionGuarded(ctx, snapshot, binding, result, func() error {
						if mode == "revoked-at-decision" {
							ConfigureSecureSession(secureread.Session{}, nil, caps)
						} else {
							head = "head-b"
						}
						return authorize()
					})
				}
				code = "OUTCOME_UNCERTAIN"
			}
			if mode == "readonly" || mode == "limitedmetadata" {
				state, err := LocalPublicOVDBConnectionState(context.Background(), scope, "dev", "music")
				if err != nil || state.CanAdd {
					t.Fatalf("false capability %+v %v", state, err)
				}
			}
			_, err := LocalCreatePublicOVDBConnection(context.Background(), request)
			wantEnvironmentCode(t, err, code)
			if _, err = os.Stat(filepath.Join(root, "connections", "music-ovdb.json")); !os.IsNotExist(err) {
				t.Fatal("rejected write created descriptor")
			}
		})
	}
}

func TestPublicOVDBConnectionAPIConcurrentCreatesShareRevisionFence(t *testing.T) {
	scope, _, root := publicConnectionAPIFixture(t)
	a := publicConnectionRequest(t, scope)
	b := a
	b.OperationID = strings.Replace(a.OperationID, ".12345678-", ".22345678-", 1)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, request := range []dto.CreatePublicOVDBConnectionRequest{a, b} {
		go func(request dto.CreatePublicOVDBConnectionRequest) {
			<-start
			_, err := LocalCreatePublicOVDBConnection(context.Background(), request)
			results <- err
		}(request)
	}
	close(start)
	success, conflict := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			success++
		} else {
			var problem *EnvironmentError
			if !errors.As(err, &problem) || problem.Code != "CONNECTION_REVISION_CONFLICT" {
				t.Fatalf("unexpected concurrent outcome %v", err)
			}
			conflict++
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	body, err := os.ReadFile(filepath.Join(root, "environments", "dev", "dev.env.json"))
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Connections []string `json:"editionConnections"`
	}
	if err = json.Unmarshal(body, &env); err != nil || len(env.Connections) != 1 {
		t.Fatalf("duplicate association %s %v", body, err)
	}
}

func TestPublicOVDBConnectionAPIRealDemoDirectoryCatalogStateCreateReload(t *testing.T) {
	scope, _, root := publicConnectionAPIFixture(t)
	if err := os.Mkdir(filepath.Join(root, "connections"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"demo-db.json", "shipping.json", "log-sources.json"} {
		body, err := os.ReadFile(filepath.Join("testdata", "connection-directory", name))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(root, "connections", name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	ref := dto.ProjectRef{StoreID: LocalStoreID, ProjectID: "p"}
	catalog, err := LocalProjectConnectionCatalog(ctx, ref)
	if err != nil || len(catalog.Connections) != 20 {
		t.Fatalf("actual package catalog count=%d %v", len(catalog.Connections), err)
	}
	before, _ := os.ReadFile(filepath.Join(root, "connections", "log-sources.json"))
	request := publicConnectionRequest(t, scope)
	if _, err = LocalCreatePublicOVDBConnection(ctx, request); err != nil {
		t.Fatal(err)
	}
	catalog, err = LocalProjectConnectionCatalog(ctx, ref)
	if err != nil || len(catalog.Connections) != 21 {
		t.Fatalf("created mixed catalog count=%d %v", len(catalog.Connections), err)
	}
	after, _ := os.ReadFile(filepath.Join(root, "connections", "log-sources.json"))
	if string(before) != string(after) {
		t.Fatal("unrelated log sources changed")
	}
	if err = os.WriteFile(filepath.Join(root, "connections", "foreign.json"), before, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = LocalProjectConnectionCatalog(ctx, ref); err == nil {
		t.Fatal("misplaced log document silently skipped")
	}
	if err = os.Remove(filepath.Join(root, "connections", "foreign.json")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "connections", "log-sources.json"), []byte(`{"format":"datatug-log-sources/v1","sources":[],"futureConnection":"hidden"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = LocalProjectConnectionCatalog(ctx, ref); err == nil {
		t.Fatal("unknown log extension silently skipped")
	}
}
