package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/dto"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func environmentFixture(t *testing.T) (dto.EnvironmentScope, Capabilities) {
	t.Helper()
	root := t.TempDir()
	if e := os.WriteFile(filepath.Join(root, "datatug-project.json"), []byte(`{"id":"p","custom":{"keep":true}}`), 0600); e != nil {
		t.Fatal(e)
	}
	caps := Capabilities{AllowWrites: true, EnvironmentPrivateDir: t.TempDir(), EnvironmentAuthorityID: "authority-A", EnvironmentMetadataPolicies: map[string]EnvironmentMetadataPolicy{"p": {PrincipalID: "actor-A", CanReadOrder: true, CanWriteOrder: true, CanCreateEnvironment: true}}}
	ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "actor-A"}}, map[string]string{"p": root}, caps)
	t.Cleanup(func() { ConfigureSecureSession(secureread.Session{}, nil, Capabilities{}) })
	return dto.EnvironmentScope{StoreID: LocalStoreID, ProjectID: "p"}, caps
}
func TestLocalEnvironmentCreateOrderReplayAndRevocation(t *testing.T) {
	ctx := context.Background()
	scope, caps := environmentFixture(t)
	state, e := LocalEnvironmentState(ctx, scope)
	if e != nil {
		t.Fatal(e)
	}
	op := state.ScopeBinding + ".12345678-1234-4123-8123-123456789abc"
	request := dto.CreateEnvironmentRequest{EnvironmentMutation: dto.EnvironmentMutation{Scope: state.Scope, IfRevision: state.Revision, OperationID: op}, Name: " DEV "}
	result, e := LocalCreateEnvironment(ctx, request)
	if e != nil {
		t.Fatal(e)
	}
	if result.EnvironmentID != "dev" {
		t.Fatal(result)
	}
	replay, e := LocalCreateEnvironment(ctx, request)
	if e != nil {
		t.Fatal(e)
	}
	{
		a, _ := json.Marshal(replay)
		b, _ := json.Marshal(result)
		if string(a) != string(b) {
			t.Fatalf("replay differs %s %s", a, b)
		}
	}
	latest, e := LocalEnvironmentState(ctx, scope)
	if e != nil || len(latest.Order) != 1 || latest.Order[0] != "dev" {
		t.Fatalf("state %v %v", latest, e)
	}
	save := dto.SaveEnvironmentOrderRequest{EnvironmentMutation: dto.EnvironmentMutation{Scope: latest.Scope, IfRevision: latest.Revision, OperationID: latest.ScopeBinding + ".22345678-1234-4123-8123-123456789abc"}, EnvironmentOrder: []string{"dev"}}
	if _, e = LocalSaveEnvironmentOrder(ctx, save); e != nil {
		t.Fatal(e)
	}
	root, _ := servedProjectDir("p")
	b, _ := os.ReadFile(filepath.Join(root, "datatug-project.json"))
	var fields map[string]any
	_ = json.Unmarshal(b, &fields)
	if fields["custom"] == nil {
		t.Fatal("unrelated metadata lost")
	}
	request.Name = "different"
	if _, e = LocalCreateEnvironment(ctx, request); e == nil {
		t.Fatal("operation payload changed")
	}
	paths := map[string]string{"p": root}
	caps.EnvironmentMetadataPolicies["p"] = EnvironmentMetadataPolicy{PrincipalID: "actor-A", CanReadOrder: true}
	ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "actor-A"}}, paths, caps)
	request.Name = " DEV "
	_, e = LocalCreateEnvironment(ctx, request)
	var denied *EnvironmentError
	if !errors.As(e, &denied) || denied.Code != "ACCESS_DENIED" {
		t.Fatalf("revoked replay %v", e)
	}
}

func TestLocalEnvironmentDefaultReorderAndMembershipFence(t *testing.T) {
	ctx := context.Background()
	scope, _ := environmentFixture(t)
	for i, name := range []string{"PROD", "LOCAL", "DEV", "QA"} {
		state, e := LocalEnvironmentState(ctx, scope)
		if e != nil {
			t.Fatal(e)
		}
		_, e = LocalCreateEnvironment(ctx, dto.CreateEnvironmentRequest{EnvironmentMutation: dto.EnvironmentMutation{Scope: state.Scope, IfRevision: state.Revision, OperationID: state.ScopeBinding + fmt.Sprintf(".%08d-1234-4123-8123-123456789abc", i+1)}, Name: name})
		if e != nil {
			t.Fatal(e)
		}
	}
	state, e := LocalEnvironmentState(ctx, scope)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(state.Order, []string{"local", "dev", "qa", "prod"}) {
		t.Fatalf("default %v", state.Order)
	}
	request := dto.SaveEnvironmentOrderRequest{EnvironmentMutation: dto.EnvironmentMutation{Scope: state.Scope, IfRevision: state.Revision, OperationID: state.ScopeBinding + ".87654321-1234-4123-8123-123456789abc"}, EnvironmentOrder: []string{"qa", "local", "prod", "dev"}}
	receipt, e := LocalSaveEnvironmentOrder(ctx, request)
	if e != nil {
		t.Fatal(e)
	}
	second, e := LocalEnvironmentState(ctx, scope)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(second.Order, request.EnvironmentOrder) {
		t.Fatalf("reader B %v", second.Order)
	}
	digest, e := dto.EnvironmentStateDigest(*second)
	if e != nil || digest != receipt.AfterStateDigest {
		t.Fatal("receipt does not prove canonical state")
	}
	stale := request
	stale.OperationID = state.ScopeBinding + ".97654321-1234-4123-8123-123456789abc"
	if _, e = LocalSaveEnvironmentOrder(ctx, stale); e == nil {
		t.Fatal("stale shared revision accepted")
	}
	other := scope
	otherAPI := "other-agent-api"
	other.ProjectAPI = &otherAPI
	different, e := LocalEnvironmentState(ctx, other)
	if e != nil {
		t.Fatal(e)
	}
	if different.ScopeBinding == state.ScopeBinding || different.Revision == state.Revision {
		t.Fatal("full scope alias collided")
	}
}

func TestLocalEnvironmentStateReadOnlyProject(t *testing.T) {
	scope, _ := environmentFixture(t)
	root, err := servedProjectDir("p")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0700) })
	state, err := LocalEnvironmentState(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision == "" {
		t.Fatal("missing revision")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("read created project state: %v", entries)
	}
}
