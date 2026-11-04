package endpoints

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/meaninglabels"
	"github.com/datatug/datatug-core/pkg/semantic"
)

func TestScanSQL(t *testing.T) {
	for text, want := range map[string]string{
		"SELECT Id, Note FROM Cards":                                  "",
		"SELECT COUNT(*), Id * 2, Id*Id FROM Cards GROUP BY Id":       "",
		"SELECT count( * ) FROM Cards":                                "",
		"SELECT * FROM Cards":                                         "selects a wildcard",
		"select distinct * from Cards":                                "selects a wildcard",
		"SELECT Id, c.* FROM Cards c":                                 "selects a wildcard",
		"SELECT Id, * FROM Cards":                                     "selects a wildcard",
		"SELECT TOP 10 * FROM Customer":                               "selects a wildcard",
		"SELECT TOP (10) *\nFROM Customer":                            "selects a wildcard",
		"SELECT*FROM Customer":                                        "selects a wildcard",
		"SELECT DISTINCT ON (Country) * FROM Customer":                "selects a wildcard",
		"UPDATE Customer SET Note = 'x' RETURNING *":                  "selects a wildcard",
		"SELECT * EXCEPT (Email) FROM Customer":                       "selects a wildcard",
		"SELECT Id FROM (SELECT *) AS t":                              "selects a wildcard",
		"SELECT Note FROM Cards WHERE Note <> '*' -- SELECT * FROM x": "",
		"EXEC dump_cards":                                             "calls a procedure",
		"call dump_cards()":                                           "calls a procedure",
		`SELECT "call" FROM Cards`:                                    "",
		"SELECT Id /*! , Email */ FROM Customer":                      "uses an executable comment",
	} {
		if got := scanSQL(text).shape; got != want {
			t.Errorf("%q: got %q, want %q", text, got, want)
		}
	}
	for text, want := range map[string]string{
		"SELECT c.Card_Number FROM Cards c":                             "c card_number cards from select",
		`SELECT "a""b", [EMAIL], ` + "`x`" + ` FROM t`:                  "a\"b email from select t x",
		"SELECT Note /* email */ FROM Cards WHERE Note = 'it''s email'": "cards from note select where",
		"SELECT correo_electrónico FROM clientes":                       "clientes correo_electrónico from select",
		// a backslash-escaped quote: the standard reading sees Email after it
		`SELECT 'O\'Brien' UNION SELECT Email FROM Customer`: "brien customer email from select union",
		// a trailing backslash: the escaping reading would swallow Email
		`SELECT 'C:\' AS p, Email FROM Customer`: "as c customer email from p select",
	} {
		if got := strings.Join(scanSQL(text).words, " "); got != want {
			t.Errorf("%q: got %q, want %q", text, got, want)
		}
	}
}

func TestRefuseAgentQueryWrites(t *testing.T) {
	called := false
	h := refuseAgentQueryWrites(func(http.ResponseWriter, *http.Request) { called = true })
	for agent, want := range map[bool]bool{true: false, false: true} {
		called = false
		r := httptest.NewRequest(http.MethodPost, "/datatug/queries/create_query", nil)
		if agent {
			r.Header.Set(agentHeader, "1")
		}
		w := httptest.NewRecorder()
		h(w, r)
		if called != want || agent && !strings.Contains(w.Body.String(), meaninglabels.CodeRawRefused) {
			t.Fatalf("agent=%v: called=%v %d %s", agent, called, w.Code, w.Body.String())
		}
	}
}

func TestAgentSavedSQL(t *testing.T) {
	project, dir := labelsFixture(t)
	write := func(id, sql string) {
		t.Helper()
		q := filepath.Join(dir, "queries")
		if err := os.MkdirAll(q, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(q, id+".query.json"), []byte(`{"createdAt":"2026-01-01T00:00:00Z","id":"`+id+`","type":"SQL"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(q, id+".query.sql"), []byte(sql), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("safe", "SELECT Id, Note FROM Cards ORDER BY Id")
	write("card", "SELECT Id, card_number FROM Cards")
	write("contacts", "SELECT Country FROM v_contacts")
	write("wild", "SELECT * FROM Cards")
	agentCtx := withAgentContext(context.Background(), true)
	prevRun := runNativeSQLHook
	t.Cleanup(func() { runNativeSQLHook = prevRun })
	var ran []string
	runNativeSQLHook = func(_ *secureread.Executor, _ context.Context, _, text string, _ ...dal.QueryArg) (secureread.Result, error) {
		ran = append(ran, text)
		return secureread.Result{}, nil
	}

	if _, err := computeRunQuery(agentCtx, agentRunRequest(project, "safe", "")); err != nil || len(ran) != 1 {
		t.Fatalf("safe saved SQL: %v %v", err, ran)
	}
	for _, id := range []string{"card", "contacts", "wild"} {
		if _, err := computeRunQuery(agentCtx, agentRunRequest(project, id, "")); errCode(err) != meaninglabels.CodeRawRefused {
			t.Fatalf("%s: %v", id, err)
		}
	}
	// a person runs the same query unguarded
	if _, err := computeRunQuery(context.Background(), agentRunRequest(project, "card", "")); err != nil || len(ran) != 2 {
		t.Fatalf("person: %v %v", err, ran)
	}
	// a missing saved query fails before the guard
	if _, err := computeRunQuery(agentCtx, agentRunRequest(project, "missing", "")); err == nil {
		t.Fatal("missing query")
	}
}

func TestSourceProtectedNames(t *testing.T) {
	project, dir := labelsFixture(t)
	setHooks(t)
	ctx := context.Background()
	names := func() map[string]bool {
		t.Helper()
		got, err := sourceProtectedNames(ctx, project, "local", "chinook")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	refuses := func(what string) {
		t.Helper()
		if _, err := sourceProtectedNames(ctx, project, "local", "chinook"); errCode(err) != meaninglabels.CodeRawRefused {
			t.Fatalf("%s: %v", what, err)
		}
	}
	// the fixture: suggested card_number and email, an unreviewed view masked whole
	got := names()
	for _, n := range []string{"card_number", "email", "v_contacts"} {
		if !got[n] {
			t.Fatalf("missing %s in %v", n, got)
		}
	}
	// the view's own columns are not protected elsewhere: naming the view is
	if got["note"] || got["cards"] || got["country"] || got["contact"] {
		t.Fatalf("unprotected names: %v", got)
	}

	// stored labels count even for objects the listing does not show
	setLabel(t, dir, meaninglabels.Change{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: "chinook", Table: "Hidden", Column: "main.secret_note"}, Class: meaninglabels.ClassSecret, Reason: "r"})
	setLabel(t, dir, meaninglabels.Change{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: "chinook", Table: "Payroll"}, Class: meaninglabels.ClassFinancial, Reason: "r"})
	setLabel(t, dir, meaninglabels.Change{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: "other", Table: "Elsewhere"}, Class: meaninglabels.ClassPII, Reason: "r"})
	listSourceObjectsHook = func(context.Context, string) ([]sourceObject, error) {
		return []sourceObject{{Name: "main.Opaque"}, {Name: "v", View: true, Columns: []string{"x"}}}, nil
	}
	resolveSourceTableHook = func(_ context.Context, _ datatug.ProjectStore, _, _, _, table string) (resolvedSource, error) {
		if table == "main.Opaque" {
			return resolvedSource{}, errors.New("cannot describe")
		}
		return resolvedSource{Columns: []semantic.Column{{Name: "id", Type: "int"}}}, nil
	}
	got = names()
	for _, n := range []string{"secret_note", "payroll", "opaque", "v"} {
		if !got[n] {
			t.Fatalf("missing %s in %v", n, got)
		}
	}
	if got["elsewhere"] || got["id"] || got["x"] {
		t.Fatalf("over-protected: %v", got)
	}

	listSourceObjectsHook = func(context.Context, string) ([]sourceObject, error) { return nil, errors.New("down") }
	refuses("unlistable source")
	if _, err := sourceProtectedNames(ctx, "nope", "local", "chinook"); errCode(err) != meaninglabels.CodeRawRefused {
		t.Fatalf("unknown project: %v", err)
	}
	apiResolveSource = func(context.Context, datatug.ProjectStore, string, string, string) (api.ResolvedSource, error) {
		return api.ResolvedSource{ID: "chinook", Kind: api.SourceKindHTTP}, nil
	}
	refuses("non-SQL source")
	if err := agentSavedSQLRefusal(ctx, project, "local", "chinook", "SELECT 1"); errCode(err) != meaninglabels.CodeRawRefused || !strings.Contains(err.Error(), "protected columns") {
		t.Fatalf("guard passes the refusal on: %v", err)
	}
}
