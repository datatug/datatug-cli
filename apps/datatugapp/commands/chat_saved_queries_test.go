package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

func TestChatSavedQuerySaveIsCreateOnlyAndListed(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, "queries"), 0o700); err != nil {
		t.Fatal(err)
	}
	store, id := filestore.NewSingleProjectStore(projectDir, "saved-query-test")
	service := chatSavedQueries{projectDir: projectDir, store: store.GetProjectStore(id), projectID: id, session: secureread.Session{Unrestricted: true}}
	created, err := service.Save(ctx, chat.SavedQuerySaveRequest{Title: "Prague customers", Type: "DTQL", Text: "from: {name: Customer}\nlimit: 50\n", Database: "chinook", Tags: []string{"customers", "Prague"}})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := service.List(ctx)
	if err != nil || len(listed) != 1 || listed[0].ID != created.ID || listed[0].Type != "DTQL" {
		t.Fatalf("saved query not listed: %+v, %v", listed, err)
	}
	loaded, err := service.store.LoadQuery(ctx, created.ID)
	if err != nil || loaded.Text != "from: {name: Customer}\nlimit: 50\n" || len(loaded.Targets) != 1 || loaded.Targets[0].Catalog != "chinook" || len(loaded.Tags) != 2 {
		t.Fatalf("saved definition changed: %+v, %v", loaded, err)
	}
	if _, err := service.Save(ctx, chat.SavedQuerySaveRequest{Title: "Unsafe", Type: "HTTP", Text: "https://api.example.test/?token=private"}); err == nil {
		t.Fatal("HTTP query with URL parameters was saved")
	}
	if _, err := service.Save(ctx, chat.SavedQuerySaveRequest{Title: "People API", Type: "HTTP", Text: "https://api.example.test/people"}); err != nil {
		t.Fatal(err)
	}
	writer := service.store.(datatug.RevisionedQueriesStore)
	_, err = writer.PutQuery(ctx, &datatug.QueryDefWithFolderPath{QueryDef: datatug.QueryDef{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "customer-invoices", Title: "Customer invoices"}},
		Type:        datatug.QueryTypeDTQL, Text: "from: {name: Invoice}\n",
		Parameters: datatug.Parameters{{ID: "CustomerId", Type: "string", IsRequired: true, DefaultValue: "001"}, {ID: "Enabled", Type: "boolean", DefaultValue: true}},
	}}, datatug.QueryWriteCondition{IfNoneMatch: true})
	if err != nil {
		t.Fatal(err)
	}
	listed, err = service.List(ctx)
	if err != nil || len(listed) != 3 || len(listed[0].Parameters) != 2 || listed[0].Parameters[0].ID != "CustomerId" {
		t.Fatalf("query parameters missing from picker: %+v, %v", listed, err)
	}
	parsed, err := accesspolicies.ParseVariables([]string{"CustomerId=" + listed[0].Parameters[0].DefaultValue, "Enabled=" + listed[0].Parameters[1].DefaultValue})
	if err != nil || parsed["CustomerId"] != "001" || parsed["Enabled"] != true {
		t.Fatalf("default values lost their types: %#v, %v", parsed, err)
	}
	var foundHTTP bool
	for _, item := range listed {
		if item.Title == "People API" && item.Type == "HTTP" {
			foundHTTP = true
		}
	}
	if !foundHTTP {
		t.Fatalf("HTTP query missing from picker: %+v", listed)
	}
}

func TestMultiValueDefaultEncodingPreservesList(t *testing.T) {
	defaultValue, err := encodeParameterDefault([]any{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := accesspolicies.ParseVariables([]string{"Ids=" + defaultValue})
	if err != nil || !reflect.DeepEqual(parsed["Ids"], []any{1, 2}) {
		t.Fatalf("multi-value default lost its list type: %#v, %v", parsed, err)
	}
}

func TestChatSavedQueryRejectsUndeclaredCapturedParameters(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, "queries"), 0o700); err != nil {
		t.Fatal(err)
	}
	store, id := filestore.NewSingleProjectStore(projectDir, "parameter-capture-test")
	service := chatSavedQueries{projectDir: projectDir, store: store.GetProjectStore(id), projectID: id, session: secureread.Session{Unrestricted: true}}
	_, err := service.Save(ctx, chat.SavedQuerySaveRequest{Title: "Customer invoices", Type: "DTQL", Text: "from: {name: Invoice}\nwhere: {op: '==', left: {field: CustomerId}, right: {param: CustomerId}}\nlimit: 10\n"})
	if err == nil || !strings.Contains(err.Error(), "parameter declarations") {
		t.Fatalf("parameterized query was silently saved: %v", err)
	}
	listed, err := service.List(ctx)
	if err != nil || len(listed) != 0 {
		t.Fatalf("rejected query appeared after reload: %+v, %v", listed, err)
	}
}

func TestChatSavedQueryRealChinookWhenConfigured(t *testing.T) {
	projectDir := os.Getenv("DATATUG_CHINOOK_PROJECT")
	if projectDir == "" {
		t.Skip("set DATATUG_CHINOOK_PROJECT to run against a local Chinook project")
	}
	projectDir, projectStore, err := resolveQueryProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	service := chatSavedQueries{projectDir: projectDir, store: projectStore, executor: secureread.NewExecutor(secureread.Session{Unrestricted: true}), env: "local"}
	listed, err := service.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var hasCustomerID bool
	for _, item := range listed {
		if item.ID == "customers/customer-invoices" && len(item.Parameters) == 1 && item.Parameters[0].ID == "CustomerId" {
			hasCustomerID = true
		}
	}
	if !hasCustomerID {
		t.Fatal("real saved query parameter missing from picker")
	}
	lookup, err := service.LookupParameter(context.Background(), "customers/customer-invoices", "CustomerId")
	if err != nil {
		t.Fatal(err)
	}
	if lookup == nil || lookup.Key != "CustomerId" || len(lookup.Result.Rows) != 59 {
		t.Fatalf("unexpected Chinook FK lookup: %+v", lookup)
	}
	var foundCustomerFive bool
	for _, row := range lookup.Result.Rows {
		if fmt.Sprint(row.Data["CustomerId"]) == "5" && row.Data["FirstName"] == "František" {
			foundCustomerFive = true
		}
	}
	if !foundCustomerFive {
		t.Fatal("lookup did not return real Chinook customer 5")
	}
	result, err := service.RunWithVariables(context.Background(), "customers/customer-invoices", map[string]string{"CustomerId": "5"})
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceID != "chinook-local" || len(result.Result.Rows) != 7 {
		t.Fatalf("unexpected Chinook result: source=%q rows=%d", result.SourceID, len(result.Result.Rows))
	}
}

func TestChatSavedQueryParameterErrorDoesNotEchoValue(t *testing.T) {
	service := chatSavedQueries{}
	_, err := service.RunWithVariables(context.Background(), "ignored", map[string]string{"CustomerId": "{token: private}"})
	if err == nil || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "token") {
		t.Fatalf("parameter error exposed input: %v", err)
	}
}
