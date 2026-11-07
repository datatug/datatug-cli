package endpoints

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/callercontext"
	"github.com/datatug/datatug-cli/pkg/labelguard"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/meaninglabels"
	"github.com/datatug/datatug-core/pkg/semantic"
)

// runQueryLabelsHooks stubs the label store, the table description and the
// object listing the exec/run_query label guard reads, and captures its
// warnings.
func runQueryLabelsHooks(t *testing.T, columns map[string][]string, objects []sourceObject, listErr error) (dir string, warnings *bytes.Buffer) {
	t.Helper()
	prevResolve, prevList, prevWarn := resolveSourceTableHook, listSourceObjectsHook, labelWarnings
	t.Cleanup(func() { resolveSourceTableHook, listSourceObjectsHook, labelWarnings = prevResolve, prevList, prevWarn })
	resolveSourceTableHook = func(_ context.Context, _ datatug.ProjectStore, _, _, _, table string) (resolvedSource, error) {
		names, ok := columns[table]
		if !ok {
			return resolvedSource{}, errors.New("not described")
		}
		var out []semantic.Column
		for _, n := range names {
			out = append(out, semantic.Column{Name: n})
		}
		return resolvedSource{Columns: out}, nil
	}
	listSourceObjectsHook = func(context.Context, string) ([]sourceObject, error) { return objects, listErr }
	warnings = &bytes.Buffer{}
	labelWarnings = labelguard.NewWarner(warnings)
	return t.TempDir(), warnings
}

func runQueryLeaf(table string, all bool, openErr error, columns ...string) secureread.FederatedLeaf {
	return secureread.FederatedLeaf{Database: "shop", Collection: table, Columns: columns, AllColumns: all,
		Open: func(context.Context) (dal.DB, error) { return nil, openErr }}
}

func protectedMessage(t *testing.T, err error) string {
	t.Helper()
	var protected *secureread.FieldProtectedError
	if !errors.As(err, &protected) {
		t.Fatalf("err = %v, want FieldProtectedError", err)
	}
	return protected.Message
}

func TestRunQueryLabelColumns(t *testing.T) {
	ctx := context.Background()
	described := map[string][]string{"Customer": {"Id", "Email"}, "Note": {"Id", "Body"}}
	dir, warnings := runQueryLabelsHooks(t, described, []sourceObject{{Name: "Customer"}, {Name: "Note"}, {Name: "v_mail", View: true}}, nil)
	sql := api.ResolvedSource{ID: "shop", Kind: api.SourceKindSQL, URL: "sqlite://x"}
	owner := &runQueryLabels{projectDir: dir, source: sql}
	strict := &runQueryLabels{projectDir: dir, source: sql, strict: true}

	// No stored label: a suggestion refuses a strict principal, and the owner
	// reads on with one warning per column.
	if msg := protectedMessage(t, strict.columns(ctx, runQueryLeaf("Customer", false, nil, "Email"))); !strings.Contains(msg, "shop.Customer.Email is protected (pii)") {
		t.Fatal(msg)
	}
	for range 2 {
		if err := owner.columns(ctx, runQueryLeaf("Customer", true, nil)); err != nil {
			t.Fatal(err)
		}
	}
	if got := warnings.String(); strings.Count(got, "warning:") != 1 || !strings.Contains(got, "Customer.Email of source shop") {
		t.Fatalf("warnings = %q", got)
	}
	// On a source with no stored label, a wildcard whose columns cannot be
	// described, or whose source cannot be opened, passes.
	if err := strict.columns(ctx, runQueryLeaf("Unknown", true, nil)); err != nil {
		t.Fatal(err)
	}
	if err := strict.columns(ctx, runQueryLeaf("Customer", true, errors.New("refused"))); err != nil {
		t.Fatal(err)
	}
	// An HTTP source is described from its declaration, never opened here.
	http := &runQueryLabels{projectDir: dir, source: api.ResolvedSource{ID: "shop", Kind: api.SourceKindHTTP}, strict: true}
	if msg := protectedMessage(t, http.columns(ctx, runQueryLeaf("Customer", true, errors.New("never opened")))); !strings.Contains(msg, "Customer.Email") {
		t.Fatal(msg)
	}

	// A stored protected label refuses the owner too.
	setLabel(t, dir, meaninglabels.Change{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: "shop", Table: "Note", Column: "Body"}, Class: meaninglabels.ClassPII, Reason: "test"})
	if msg := protectedMessage(t, owner.columns(ctx, runQueryLeaf("Note", false, nil, "Body"))); !strings.Contains(msg, "shop.Note.Body is protected (pii)") {
		t.Fatal(msg)
	}
	if msg := protectedMessage(t, owner.columns(ctx, runQueryLeaf("Note", true, nil))); !strings.Contains(msg, "shop.Note.Body") {
		t.Fatal(msg)
	}
	// An unlabelled column of a labelled source is read.
	if err := owner.columns(ctx, runQueryLeaf("Note", false, nil, "Id")); err != nil {
		t.Fatal(err)
	}
	// A labelled source refuses what it cannot open or describe.
	if msg := protectedMessage(t, owner.columns(ctx, runQueryLeaf("Note", false, errors.New("refused"), "Id"))); !strings.Contains(msg, "cannot be opened") {
		t.Fatal(msg)
	}
	if msg := protectedMessage(t, owner.columns(ctx, runQueryLeaf("Unknown", true, nil))); !strings.Contains(msg, "cannot be listed") {
		t.Fatal(msg)
	}
	// An unreviewed view of a labelled source is masked whole.
	if err := owner.columns(ctx, runQueryLeaf("v_mail", false, nil, "contact")); err == nil {
		t.Fatal("unreviewed view read")
	}
	// A table-level label refuses a wildcard, naming the table only.
	setLabel(t, dir, meaninglabels.Change{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: "shop", Table: "Secret"}, Class: meaninglabels.ClassSecret, Reason: "test"})
	if msg := protectedMessage(t, owner.columns(ctx, runQueryLeaf("Secret", true, nil))); !strings.Contains(msg, "shop.Secret is protected (secret)") {
		t.Fatal(msg)
	}

	// Labels that cannot be read refuse.
	prevStore := labelsStoreFor
	t.Cleanup(func() { labelsStoreFor = prevStore })
	labelsStoreFor = func(string) labelStore { return failingLoad{} }
	if err := owner.columns(ctx, runQueryLeaf("Customer", false, nil, "Id")); !errors.Is(err, errRunQueryLabelsUnreadable) {
		t.Fatalf("err = %v", err)
	}
	if err := owner.nativeSQL(ctx, "shop", "sqlite://x"); !errors.Is(err, errRunQueryLabelsUnreadable) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunQueryLabelIsView(t *testing.T) {
	ctx := context.Background()
	runQueryLabelsHooks(t, nil, []sourceObject{{Name: "Plain"}, {Name: "v", View: true}}, nil)
	sql := &runQueryLabels{source: api.ResolvedSource{Kind: api.SourceKindSQL}}
	if sql.isView(ctx, "plain") || !sql.isView(ctx, "V") || sql.isView(ctx, "missing") {
		t.Fatal("isView of a listed source")
	}
	if (&runQueryLabels{source: api.ResolvedSource{Kind: api.SourceKindHTTP}}).isView(ctx, "v") {
		t.Fatal("an HTTP endpoint is no view")
	}
	runQueryLabelsHooks(t, nil, nil, errors.New("down"))
	if !sql.isView(ctx, "Plain") {
		t.Fatal("a source that cannot be listed treats an object as a view")
	}
}

func TestRunQueryLabelNativeSQL(t *testing.T) {
	ctx := context.Background()
	dir, warnings := runQueryLabelsHooks(t, map[string][]string{"Customer": {"Id", "Email"}}, []sourceObject{{Name: "Customer"}, {Name: "v_people", View: true, Columns: []string{"phone"}}}, nil)
	owner := &runQueryLabels{projectDir: dir, source: api.ResolvedSource{ID: "shop"}}
	strict := &runQueryLabels{projectDir: dir, source: api.ResolvedSource{ID: "shop"}, strict: true}

	// Suggestions: a strict principal is refused, the owner is warned; a
	// view's reported columns count when it is not described.
	if msg := protectedMessage(t, strict.nativeSQL(ctx, "shop", "sqlite://x")); !strings.Contains(msg, "Customer.Email, v_people.phone") {
		t.Fatal(msg)
	}
	if err := owner.nativeSQL(ctx, "shop", "sqlite://x"); err != nil || !strings.Contains(warnings.String(), "native SQL is not masked") {
		t.Fatalf("err = %v, warnings = %q", err, warnings.String())
	}
	// A stored protected label refuses every principal.
	setLabel(t, dir, meaninglabels.Change{Action: meaninglabels.ActionPropose, Target: meaninglabels.Target{Source: "shop", Table: "Customer", Column: "Id"}, Class: meaninglabels.ClassPII, Reason: "test"})
	if msg := protectedMessage(t, owner.nativeSQL(ctx, "shop", "sqlite://x")); !strings.Contains(msg, "protected by sensitivity labels") {
		t.Fatal(msg)
	}
	// A source with nothing suggested runs.
	runQueryLabelsHooks(t, map[string][]string{"Plain": {"Id"}}, []sourceObject{{Name: "Plain"}}, nil)
	if err := strict.nativeSQL(ctx, "other", "sqlite://y"); err != nil {
		t.Fatal(err)
	}
	// A source whose tables cannot be listed refuses a strict principal only.
	runQueryLabelsHooks(t, nil, nil, errors.New("down"))
	if msg := protectedMessage(t, strict.nativeSQL(ctx, "other", "sqlite://y")); !strings.Contains(msg, "cannot be listed") {
		t.Fatal(msg)
	}
	if err := owner.nativeSQL(ctx, "other", "sqlite://y"); err != nil {
		t.Fatal(err)
	}
}

func TestRunQueryStrict(t *testing.T) {
	prevCaller, prevPolicyFree := serveCallerIsAgent, policyFreeReads
	t.Cleanup(func() { serveCallerIsAgent, policyFreeReads = prevCaller, prevPolicyFree })
	serveCallerIsAgent = func() bool { return false }
	policyFreeReads = func() bool { return true }
	if runQueryStrict(context.Background()) {
		t.Fatal("the policy-free owner is not strict")
	}
	// The header, the serve process's harness and a policy-governed session
	// each make a read strict.
	if !runQueryStrict(withAgentContext(context.Background(), true)) {
		t.Fatal("agent header")
	}
	serveCallerIsAgent = func() bool { return true }
	if !runQueryStrict(context.Background()) {
		t.Fatal("agent harness")
	}
	serveCallerIsAgent = func() bool { return false }
	policyFreeReads = func() bool { return false }
	if !runQueryStrict(context.Background()) {
		t.Fatal("policy-governed session")
	}
}

func TestDetectServeCallerIsAgent(t *testing.T) {
	for _, name := range []string{callercontext.HarnessEnv, callercontext.AIAgentEnv} {
		t.Setenv(name, "")
	}
	for _, h := range callercontext.Harnesses {
		t.Setenv(h.Marker, "")
	}
	if detectServeCallerIsAgent() {
		t.Fatal("no harness")
	}
	t.Setenv(callercontext.HarnessEnv, "codex")
	if !detectServeCallerIsAgent() {
		t.Fatal("harness")
	}
}
