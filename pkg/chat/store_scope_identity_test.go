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

// A source is identified by where it points and as whom, not by the display form
// the store keeps for it: two roles of one PostgreSQL database, and two
// directories whose names differ after a "#", are two scopes and do not see each
// other's stored results.
func TestSessionStoreScopeSeparatesWhatTheDisplayFormWouldMerge(t *testing.T) {
	ctx := context.Background()
	scopeOf := func(source string) ChatScope {
		return ChatScope{ProjectID: "shop-project", Environment: "prod", Database: "shop", AccessFingerprint: "admin-policy-v1", Sources: map[string]string{"shop": source}}
	}
	for name, pair := range map[string][2]string{
		"two roles of one database": {"postgres://reader:one@db.example.com:5432/shop", "postgres://admin:two@db.example.com:5432/shop"},
		"two directories":           {"ingitdb:///tmp/a#b/data/ingitdb", "ingitdb:///tmp/a#c/data/ingitdb"},
	} {
		t.Run(name, func(t *testing.T) {
			path := testStorePath(t)
			first := openTestStore(t, path, scopeOf(pair[0]))
			session, err := first.Create(ctx, "Orders")
			if err != nil {
				t.Fatal(err)
			}
			firstScope := first.scope
			_ = first.Close()

			second := openTestStore(t, path, scopeOf(pair[1]))
			if second.scope == firstScope {
				t.Fatal("two sources that are different places share a scope")
			}
			if list, err := second.List(ctx); err != nil || len(list) != 0 {
				t.Fatalf("List under the second source = %+v, %v", list, err)
			}
			if _, err := second.Load(ctx, session.ID); err == nil {
				t.Fatal("a session of the first source opened under the second")
			}
			_ = second.Close()

			// The first source's scope is stable, and a rotated password does not change it.
			again := openTestStore(t, path, scopeOf(strings.Replace(pair[0], ":one@", ":rotated@", 1)))
			if again.scope != firstScope {
				t.Fatal("reopening the first source (with a rotated password) changed its scope")
			}
			if list, err := again.List(ctx); err != nil || len(list) != 1 {
				t.Fatalf("List under the first source = %+v, %v", list, err)
			}
			_ = again.Close()

			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"one@", ":two", "rotated"} {
				if strings.Contains(string(raw), secret) {
					t.Fatalf("the chat store file holds %q", secret)
				}
			}
		})
	}
}
