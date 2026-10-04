package chat

import (
	"context"
	"os"
	"strings"
	"testing"
)

// An env:NAME source names a variable that the operator can point at another
// database while the name stays. The scope identity must follow the database,
// or record sets read from the old one would open under the new one.
func TestSessionStoreScopeFollowsWhereAnEnvSourcePoints(t *testing.T) {
	ctx := context.Background()
	path := testStorePath(t)
	scope := ChatScope{
		ProjectID: "shop-project", Environment: "prod", Database: "shop", AccessFingerprint: "admin-policy-v1",
		Sources: map[string]string{"shop": "env:DT03_CHAT_PG_URL"},
	}

	t.Setenv("DT03_CHAT_PG_URL", "postgres://alice:s3cret@db-a.example.com:5432/shop")
	original := openTestStore(t, path, scope)
	session, err := original.Create(ctx, "Orders from database A")
	if err != nil {
		t.Fatal(err)
	}
	originalScope := original.scope
	_ = original.Close()

	// The same destination under a rotated password is the same scope.
	t.Setenv("DT03_CHAT_PG_URL", "postgres://alice:rotated@db-a.example.com:5432/shop")
	sameDatabase := openTestStore(t, path, scope)
	if sameDatabase.scope != originalScope {
		t.Fatalf("a rotated password changed the scope: %s != %s", sameDatabase.scope, originalScope)
	}
	list, err := sameDatabase.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != session.ID {
		t.Fatalf("List after a password rotation = %+v, %v", list, err)
	}
	_ = sameDatabase.Close()

	// The variable now points at another host: another scope, nothing carried over.
	t.Setenv("DT03_CHAT_PG_URL", "postgres://alice:s3cret@db-b.example.com:5432/shop")
	repointed := openTestStore(t, path, scope)
	if repointed.scope == originalScope {
		t.Fatal("repointing the variable at another database kept the same scope")
	}
	list, err = repointed.List(ctx)
	if err != nil || len(list) != 0 {
		t.Fatalf("List after repointing the variable = %+v, %v", list, err)
	}
	if _, err = repointed.Load(ctx, session.ID); err == nil {
		t.Fatal("a session from database A opened under database B")
	}
	_ = repointed.Close()

	// Pointing it back finds the original sessions again.
	t.Setenv("DT03_CHAT_PG_URL", "postgres://alice:s3cret@db-a.example.com:5432/shop")
	restored := openTestStore(t, path, scope)
	if list, err = restored.List(ctx); err != nil || len(list) != 1 {
		t.Fatalf("List after pointing the variable back = %+v, %v", list, err)
	}
	_ = restored.Close()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "s3cret") || strings.Contains(string(raw), "rotated") {
		t.Fatal("the chat store file holds a password")
	}
}

func TestSessionStoreScopeOfAnUnsetEnvSourceIsItsOwn(t *testing.T) {
	path := testStorePath(t)
	scope := ChatScope{
		ProjectID: "shop-project", Environment: "prod", Database: "shop", AccessFingerprint: "admin-policy-v1",
		Sources: map[string]string{"shop": "env:DT03_CHAT_UNSET_URL"},
	}
	unresolved := openTestStore(t, path, scope)
	unresolvedScope := unresolved.scope
	_ = unresolved.Close()

	t.Setenv("DT03_CHAT_UNSET_URL", "postgres://alice:s3cret@db-a.example.com/shop")
	resolved := openTestStore(t, path, scope)
	if resolved.scope == unresolvedScope {
		t.Fatal("a variable that gained a value kept the scope it had while unset")
	}
}
