package endpoints

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/meaninglabelsfile"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/meaninglabels"
	"github.com/datatug/datatug-core/pkg/semantic"
)

// labelsFixture is the lookup fixture plus a Cards table and a view over
// Customer, read by an admin principal.
func labelsFixture(t *testing.T) (project, dir string) {
	t.Helper()
	project = lookupFixture(t, "alice", []string{"admin"})
	prevWrites := labelWritesAllowed
	labelWritesAllowed = func() bool { return true }
	t.Cleanup(func() { labelWritesAllowed = prevWrites })
	dir, _ = relatedProjectDir(project)
	db, err := sql.Open("sqlite", filepath.Join(dir, "chinook.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE Cards (Id INTEGER NOT NULL PRIMARY KEY, CustomerId INTEGER REFERENCES Customer(CustomerId), card_number TEXT, Note TEXT)`,
		`INSERT INTO Cards VALUES (1, 1, '4111111111111111', 'first'), (2, 2, '5500000000000004', 'second')`,
		`CREATE VIEW v_contacts AS SELECT Email AS contact, Country FROM Customer`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return project, dir
}

func post(t *testing.T, handler http.HandlerFunc, body string, agent bool) (int, string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
	if agent {
		r.Header.Set(agentHeader, "1")
	}
	w := httptest.NewRecorder()
	handler(w, r)
	return w.Code, w.Body.String()
}

func setLabel(t *testing.T, dir string, change meaninglabels.Change) {
	t.Helper()
	s := meaninglabelsfile.Store{Dir: dir}
	doc, err := s.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := meaninglabels.Apply(doc, change, meaninglabels.Actor{ID: "user:test"}, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(context.Background(), out, doc.Revision); err != nil {
		t.Fatal(err)
	}
}

func src(project string) string {
	return `"project":"` + project + `","env":"local","source":"chinook"`
}

func TestMeaningsDescribe(t *testing.T) {
	project, dir := labelsFixture(t)
	setLabel(t, dir, meaninglabels.Change{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: "chinook", Table: "Cards", Column: "card_number"},
		Class: meaninglabels.ClassFinancial, Kind: "card-number", Reason: "PAN"})
	code, raw := post(t, meaningsDescribeHandler, `{`+src(project)+`,"tables":["customer","cards","v_contacts","invoice"]}`, true)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, raw)
	}
	var got describeResponse
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	tables := map[string]describedTable{}
	for _, tb := range got.Tables {
		tables[tb.Name] = tb
	}
	if len(tables) != 4 || got.Notice == "" || got.LabelsRevision == "" {
		t.Fatalf("describe: %s", raw)
	}
	col := func(table, column string) describedColumn {
		for _, c := range tables[table].Columns {
			if c.Name == column {
				return c
			}
		}
		t.Fatalf("no %s.%s", table, column)
		return describedColumn{}
	}
	if l := col("Customer", "Email").Label; l == nil || l.Class != "pii" || l.Origin != "suggested" || l.Rule != "pii-email" {
		t.Fatalf("Customer.Email: %+v", l)
	}
	if l := col("Cards", "card_number").Label; l == nil || l.Class != "financial" || l.Origin != "column" || !l.Revealable {
		t.Fatalf("card_number: %+v", l)
	}
	if l := col("v_contacts", "contact").Label; l == nil || l.Origin != "unreviewed-object" || tables["v_contacts"].Kind != "view" {
		t.Fatalf("view: %+v", l)
	}
	if col("Customer", "CustomerId").Meaning == nil {
		t.Fatalf("Customer.CustomerId has no meaning: %s", raw)
	}
	foundFK := false
	for _, j := range got.Joins {
		if j.From.Table == "Cards" && j.To.Table == "Customer" && j.Provenance == "foreign-key" {
			foundFK = true
		}
	}
	if !foundFK {
		t.Fatalf("joins: %+v", got.Joins)
	}
	if strings.Contains(raw, "4111111111111111") {
		t.Fatal("describe must contain no values")
	}
}

func TestMeaningsFind(t *testing.T) {
	project, _ := labelsFixture(t)
	code, raw := post(t, meaningsFindHandler, `{`+src(project)+`,"text":"email"}`, true)
	if code != http.StatusOK || !strings.Contains(raw, `"table":"Customer","column":"Email"`) || !strings.Contains(raw, `"table":"Employee","column":"Email"`) {
		t.Fatalf("%d %s", code, raw)
	}
	if code, _ := post(t, meaningsFindHandler, `{`+src(project)+`,"text":"  "}`, true); code != http.StatusBadRequest {
		t.Fatal("empty text")
	}
	if code, _ := post(t, meaningsFindHandler, `{"project":"nope","env":"local","source":"chinook","text":"x"}`, true); code != http.StatusNotFound {
		t.Fatal("unknown project")
	}
	code, raw = post(t, meaningsFindHandler, `{`+src(project)+`,"text":"customer"}`, true)
	if code != http.StatusOK || !strings.Contains(raw, `{"table":"Customer"}`) {
		t.Fatalf("table match: %s", raw)
	}
}

type tableReadAnswer struct {
	Recordset struct {
		Columns []apicontract.Column `json:"columns"`
		Rows    [][]json.RawMessage  `json:"rows"`
	} `json:"recordset"`
	Protection map[string]*meaninglabels.Protection `json:"protection"`
	Notice     string                               `json:"notice"`
	Error      struct {
		Code string `json:"code"`
	} `json:"error"`
}

func doRead(t *testing.T, body string, agent bool) (int, tableReadAnswer, string) {
	t.Helper()
	code, raw := post(t, recordsReadHandler, body, agent)
	var a tableReadAnswer
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		t.Fatalf("decode: %v: %s", err, raw)
	}
	return code, a, raw
}

func TestRecordsReadMasks(t *testing.T) {
	project, dir := labelsFixture(t)
	spy := spyQueries(t)
	code, a, raw := doRead(t, `{`+src(project)+`,"table":"Cards","limit":5}`, true)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, raw)
	}
	if strings.Contains(raw, "4111111111111111") || a.Protection["card_number"] == nil || a.Protection["card_number"].Class != "financial" {
		t.Fatalf("card_number not masked: %s", raw)
	}
	if len(a.Recordset.Columns) != 4 || a.Recordset.Columns[2].Name != "card_number" || len(a.Recordset.Rows) != 2 {
		t.Fatalf("shape: %s", raw)
	}
	if spy.calls != 1 {
		t.Fatalf("one query, got %d", spy.calls)
	}
	// filter or sort on a masked column is refused before any query
	for _, body := range []string{
		`{` + src(project) + `,"table":"Cards","where":[{"column":"card_number","op":"=","value":"4111"}]}`,
		`{` + src(project) + `,"table":"Cards","orderBy":[{"column":"CARD_NUMBER","desc":true}]}`,
	} {
		if code, a, _ := doRead(t, body, true); code != http.StatusForbidden || a.Error.Code != meaninglabels.CodeFieldProtected {
			t.Fatalf("%s: %d %s", body, code, a.Error.Code)
		}
	}
	if spy.calls != 1 {
		t.Fatal("a refused read must not query")
	}
	// allowed filter, sort and != on unmasked columns
	code, a, raw = doRead(t, `{`+src(project)+`,"table":"Cards","columns":["Id","Note"],"where":[{"column":"Id","op":"!=","value":1}],"orderBy":[{"column":"Id"},{"column":"Note","desc":true}]}`, true)
	if code != http.StatusOK || len(a.Recordset.Rows) != 1 || len(a.Protection) != 0 {
		t.Fatalf("filtered: %d %s", code, raw)
	}
	// every requested column masked: no row is read
	code, a, raw = doRead(t, `{`+src(project)+`,"table":"Cards","columns":["card_number"]}`, true)
	if code != http.StatusOK || len(a.Recordset.Rows) != 0 || !strings.Contains(a.Notice, "no row was read") {
		t.Fatalf("all masked: %d %s", code, raw)
	}
	// a person reveals a revealable column locally; an agent may not
	code, _, _ = doRead(t, `{`+src(project)+`,"table":"Cards","reveal":["card_number"]}`, true)
	if code != http.StatusForbidden {
		t.Fatal("agent reveal must be refused")
	}
	code, a, raw = doRead(t, `{`+src(project)+`,"table":"Cards","reveal":["card_number"]}`, false)
	if code != http.StatusOK || !strings.Contains(raw, "4111111111111111") {
		t.Fatalf("person reveal: %d %s", code, raw)
	}
	audit, _ := os.ReadFile(filepath.Join(dir, meaninglabelsfile.AuditPath))
	if !strings.Contains(string(audit), "meaning.field.revealed_locally") || strings.Contains(string(audit), "4111") {
		t.Fatalf("audit: %s", audit)
	}
	// the unreviewed view is never read in clear, for people too
	code, a, raw = doRead(t, `{`+src(project)+`,"table":"v_contacts"}`, false)
	if strings.Contains(raw, "@") || (code == http.StatusOK && a.Protection["Country"] == nil) {
		t.Fatalf("view: %d %s", code, raw)
	}
	for body, want := range map[string]int{
		`{` + src(project) + `,"table":"Cards","limit":501}`:                                               400,
		`{` + src(project) + `,"table":"Cards","columns":["nope"]}`:                                        400,
		`{` + src(project) + `,"table":"Cards","where":[{"column":"Id","op":"LIKE","value":1}]}`:           400,
		`{` + src(project) + `,"table":"Cards","orderBy":[{"column":"nope"}]}`:                             400,
		`{"project":"nope","env":"local","source":"chinook","table":"Cards"}`:                              404,
		`{` + src(project) + `,"table":"NoSuch"}`:                                                          404,
		`{` + src(project) + `,"table":"Cards","bogus":1}`:                                                 400,
		`{` + src(project) + `,"table":"Cards","reveal":["Id"],"columns":["Id"]}` + strings.Repeat(" ", 1): 200,
	} {
		if code, _, raw := doRead(t, body, false); code != want {
			t.Fatalf("%s: %d %s", body, code, raw)
		}
	}
}

func TestRecordsReadFailures(t *testing.T) {
	project, dir := labelsFixture(t)
	prevRun, prevEnc, prevStore := runStructuredRelatedHook, toContractRecordsetHook, labelsStoreFor
	t.Cleanup(func() {
		runStructuredRelatedHook, toContractRecordsetHook, labelsStoreFor = prevRun, prevEnc, prevStore
	})
	runStructuredRelatedHook = func(*secureread.Executor, context.Context, string, dal.StructuredQuery, map[string]any) (secureread.Result, error) {
		return secureread.Result{}, secureread.ErrAccessDenied
	}
	if code, a, _ := doRead(t, `{`+src(project)+`,"table":"Cards"}`, true); code != http.StatusForbidden || a.Error.Code != "ACCESS_DENIED" {
		t.Fatalf("denied: %d", code)
	}
	runStructuredRelatedHook = func(*secureread.Executor, context.Context, string, dal.StructuredQuery, map[string]any) (secureread.Result, error) {
		return secureread.Result{}, errors.New("disk")
	}
	if code, _, _ := doRead(t, `{`+src(project)+`,"table":"Cards"}`, true); code != http.StatusInternalServerError {
		t.Fatalf("failure: %d", code)
	}
	runStructuredRelatedHook = prevRun
	toContractRecordsetHook = func(secureread.Result) (apicontract.Recordset, error) {
		return apicontract.Recordset{}, errors.New("x")
	}
	if code, _, _ := doRead(t, `{`+src(project)+`,"table":"Cards"}`, true); code != http.StatusInternalServerError {
		t.Fatalf("encode: %d", code)
	}
	toContractRecordsetHook = prevEnc
	labelsStoreFor = func(string) labelStore { return failingAudit{meaninglabelsfile.Store{Dir: dir}} }
	if code, _, _ := doRead(t, `{`+src(project)+`,"table":"Cards","reveal":["card_number"]}`, false); code != http.StatusInternalServerError {
		t.Fatalf("unaudited reveal must be refused: %d", code)
	}
	labelsStoreFor = func(string) labelStore { return failingLoad{} }
	if code, _, _ := doRead(t, `{`+src(project)+`,"table":"Cards"}`, true); code != http.StatusInternalServerError {
		t.Fatalf("unreadable labels must refuse: %d", code)
	}
}

type failingAudit struct{ meaninglabelsfile.Store }

func (failingAudit) Append(meaninglabelsfile.AuditRecord) error { return errors.New("audit down") }

type failingLoad struct{ failingAudit }

func (failingLoad) Load(context.Context) (meaninglabels.Document, error) {
	return meaninglabels.Document{}, errors.New("unreadable")
}

func TestMeaningsLabelsListAndChange(t *testing.T) {
	project, dir := labelsFixture(t)
	code, raw := post(t, meaningsLabelsHandler, `{`+src(project)+`,"tables":["Cards"]}`, false)
	var list labelsResponse
	if err := json.Unmarshal([]byte(raw), &list); err != nil || code != 200 || len(list.Tables) != 1 || list.Revision != "" {
		t.Fatalf("list: %d %s", code, raw)
	}
	change := func(body string, agent bool) (int, string) { return post(t, meaningsLabelsChangeHandler, body, agent) }
	// agent proposes; may not clear
	code, raw = change(`{`+src(project)+`,"ifRevision":"","change":{"action":"propose","target":{"source":"chinook","table":"Cards","column":"Note"},"class":"pii"}}`, true)
	if code != 200 || !strings.Contains(raw, "meaning.label.proposed") {
		t.Fatalf("propose: %d %s", code, raw)
	}
	var resp labelChangeResponse
	_ = json.Unmarshal([]byte(raw), &resp)
	code, raw = change(`{`+src(project)+`,"ifRevision":"`+resp.Revision+`","change":{"action":"clear","target":{"source":"chinook","table":"Cards","column":"Note"},"reason":"r"}}`, true)
	if code != http.StatusForbidden || !strings.Contains(raw, meaninglabels.CodeAgentNotAllowed) {
		t.Fatalf("agent clear: %d %s", code, raw)
	}
	// owner dismisses the proposal at its stored target
	code, raw = change(`{`+src(project)+`,"ifRevision":"`+resp.Revision+`","change":{"action":"dismiss","target":{"source":"chinook","table":"Cards","column":"Note"},"reason":"not personal"}}`, false)
	if code != 200 {
		t.Fatalf("dismiss: %d %s", code, raw)
	}
	_ = json.Unmarshal([]byte(raw), &resp)
	// a loosening write at an inexact target is refused
	code, raw = change(`{`+src(project)+`,"ifRevision":"`+resp.Revision+`","change":{"action":"set","target":{"source":"chinook","table":"customer","column":"email"},"class":"none","reason":"r"}}`, false)
	if code != http.StatusBadRequest || !strings.Contains(raw, meaninglabels.CodeTargetAmbiguous) {
		t.Fatalf("ambiguous: %d %s", code, raw)
	}
	// an exact one is saved; a stale revision conflicts
	code, raw = change(`{`+src(project)+`,"ifRevision":"`+resp.Revision+`","change":{"action":"set","target":{"source":"chinook","table":"Customer","column":"Email"},"class":"none","reason":"r"}}`, false)
	if code != 200 {
		t.Fatalf("exact: %d %s", code, raw)
	}
	code, raw = change(`{`+src(project)+`,"ifRevision":"`+resp.Revision+`","change":{"action":"set","target":{"source":"chinook","table":"Cards"},"class":"pii","reason":"r"}}`, false)
	if code != http.StatusConflict {
		t.Fatalf("stale: %d %s", code, raw)
	}
	// an ignored proposal saves nothing
	_ = json.Unmarshal([]byte(raw), &resp)
	doc, _ := meaninglabelsfile.Store{Dir: dir}.Load(context.Background())
	code, raw = change(`{`+src(project)+`,"ifRevision":"stale","change":{"action":"propose","target":{"source":"chinook","table":"Customer","column":"Email"},"class":"pii"}}`, true)
	if code != 200 || !strings.Contains(raw, "proposal_ignored") {
		// the owner's none on Customer.Email is weaker than pii, so the proposal is stored instead
		if code != http.StatusConflict {
			t.Fatalf("propose over owner: %d %s", code, raw)
		}
	}
	for body, want := range map[string]int{
		`{` + src(project) + `,"ifRevision":"` + doc.Revision + `","change":{"action":"set","target":{"source":"other","table":"Cards"},"class":"pii","reason":"r"}}`:            400,
		`{"project":"nope","env":"local","source":"chinook","ifRevision":"","change":{"action":"set","target":{"source":"chinook","table":"Cards"},"class":"pii","reason":"r"}}`: 404,
		`{` + src(project) + `,"ifRevision":"` + doc.Revision + `","change":{"action":"set","target":{"source":"chinook","table":"Cards"},"class":"x","reason":"r"}}`:            400,
		`{` + src(project) + `,"ifRevision":"` + doc.Revision + `","change":{"action":"class","target":{"source":"chinook"},"class":"pii","nonRevealable":true,"reason":"r"}}`:   200,
	} {
		if code, raw := change(body, false); code != want {
			t.Fatalf("%s: %d %s", body, code, raw)
		}
	}
	if code, _ := post(t, meaningsLabelsHandler, `{"project":"nope","env":"local","source":"chinook"}`, false); code != 404 {
		t.Fatal("unknown project")
	}
}

func TestRecordsSchemaCarriesLabels(t *testing.T) {
	project, dir := labelsFixture(t)
	code, raw := doSchema(t, `{`+src(project)+`,"table":"Customer"}`)
	if code != 200 || !strings.Contains(raw, `"name":"Email","type":"string","label":{"class":"pii"`) || !strings.Contains(raw, `"protection":{"class":"pii"`) {
		t.Fatalf("%d %s", code, raw)
	}
	prev := labelsStoreFor
	t.Cleanup(func() { labelsStoreFor = prev })
	labelsStoreFor = func(string) labelStore { return failingLoad{failingAudit{meaninglabelsfile.Store{Dir: dir}}} }
	if code, _ := doSchema(t, `{`+src(project)+`,"table":"Customer"}`); code != http.StatusInternalServerError {
		t.Fatalf("unreadable labels: %d", code)
	}
}

func TestAgentGuards(t *testing.T) {
	project, dir := labelsFixture(t)
	ctx := context.Background()
	if !agentFromContext(withAgentContext(ctx, true)) || agentFromContext(ctx) {
		t.Fatal("agent context")
	}
	if err := agentRawRefusal(ctx, project, "local", "chinook"); err == nil || !strings.Contains(err.Error(), meaninglabels.CodeRawRefused) {
		t.Fatalf("raw: %v", err)
	}
	if err := agentRawRefusal(ctx, "nope", "local", "chinook"); err == nil {
		t.Fatal("unknown project must refuse")
	}
	tugql := func(doc string) error { return agentTugQLGuard(ctx, project, "local", "chinook", []byte(doc)) }
	for doc, want := range map[string]string{
		"from: {name: Cards}\n":                                           meaninglabels.CodeFieldProtected,
		"from: {name: Cards}\ncolumns: [{field: card_number}]\n":          meaninglabels.CodeFieldProtected,
		"from: {name: Cards}\ncolumns: [{wildcard: {exclude: [Note]}}]\n": meaninglabels.CodeFieldProtected,
		"from: {name: Cards}\ncolumns: [{field: Id}, {field: Note}]\n":    "",
		"from: {name: Cards}\ncolumns: [{field: Id, as: card_number}]\n":  meaninglabels.CodeFieldProtected,
		"from: {name: Genre}\n":                                           "",
		"::":                                                              "INVALID_REQUEST",
		"from: {name: NoSuch}\ncolumns: [{field: Id}]\n":                  meaninglabels.CodeRawRefused,
		"from:\n  name: Cards\n  alias: k\n  joins:\n    - from: {name: Customer, alias: c}\n      on: [{left: {field: CustomerId, source: k}, op: '==', right: {field: CustomerId, source: c}}]\ncolumns: [{field: Id, source: k}]\n":   meaninglabels.CodeFieldProtected,
		"from:\n  name: Album\n  alias: al\n  joins:\n    - from: {name: Artist, alias: ar}\n      on: [{left: {field: ArtistId, source: al}, op: '==', right: {field: ArtistId, source: ar}}]\ncolumns: [{field: Title, source: al}]\n": "",
		"from:\n  name: Album\n  alias: al\n  joins:\n    - from: {name: NoSuch, alias: ar}\n      on: [{left: {field: ArtistId, source: al}, op: '==', right: {field: ArtistId, source: ar}}]\ncolumns: [{field: Title, source: al}]\n": meaninglabels.CodeRawRefused,
	} {
		err := tugql(doc)
		if (want == "") != (err == nil) || (want != "" && !strings.Contains(err.Error(), want)) {
			t.Fatalf("%q: want %q, got %v", doc, want, err)
		}
	}
	setLabel(t, dir, meaninglabels.Change{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: "chinook", Table: "Genre"}, Class: meaninglabels.ClassPII, Reason: "r"})
	if err := tugql("from: {name: Genre}\ncolumns: [{field: GenreId}]\nwhere: {op: '==', left: {field: Name}, right: {value: Rock}}\n"); err == nil {
		t.Fatal("expression on masked column must refuse")
	}
	_ = os.Remove(filepath.Join(dir, meaninglabelsfile.FileName))
	if labelHTTPStatus("LABELS_REVISION_CONFLICT") != 409 || labelHTTPStatus("LABEL_X") != 400 || labelHTTPStatus("OTHER") != 0 || httpStatusFor(meaninglabels.CodeFieldProtected) != 403 {
		t.Fatal("status mapping")
	}
	if labelContractError(errors.New("plain")).Error() != "plain" {
		t.Fatal("plain errors pass through")
	}
	if (sourceObject{Name: "a", View: true}).String() != "a(view=true)" {
		t.Fatal("string")
	}
}

func TestListSourceObjectsAndSeams(t *testing.T) {
	project, _ := labelsFixture(t)
	dir, _ := relatedProjectDir(project)
	objects, err := listSourceObjects(context.Background(), "sqlite://"+filepath.Join(dir, "chinook.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	views := 0
	for _, o := range objects {
		if o.View {
			views++
		}
	}
	if views != 1 || len(objects) < 10 {
		t.Fatalf("objects: %+v", objects)
	}
	if _, err := listSourceObjects(context.Background(), "nope://x"); err == nil {
		t.Fatal("bad url")
	}
	if _, err := listSourceObjects(context.Background(), "sqlite:///no/such/dir/x.sqlite"); err == nil {
		t.Fatal("missing file")
	}
	prev := listSourceObjectsHook
	t.Cleanup(func() { listSourceObjectsHook = prev })
	listSourceObjectsHook = func(context.Context, string) ([]sourceObject, error) { return nil, errors.New("down") }
	if code, _ := post(t, meaningsDescribeHandler, `{`+src(project)+`}`, true); code == 200 {
		t.Fatal("describe must fail when the source cannot be listed")
	}
	m := &meaningSource{registered: api.ResolvedSource{Kind: api.SourceKindSQL, URL: "x"}}
	if !m.isView(context.Background(), "Cards") {
		t.Fatal("unknown kind counts as a view")
	}
	m.registered.Kind = api.SourceKindHTTP
	if !m.isView(context.Background(), "Cards") {
		t.Fatal("non-SQL counts as a view")
	}
	if _, err := m.objects(context.Background(), nil); err == nil {
		t.Fatal("non-SQL describe")
	}
	if (meaningSourceRequest{Project: "p", Env: "e", Source: "s", Tables: make([]string, maxMeaningObjects+1)}).validate() == nil {
		t.Fatal("too many tables")
	}
	if joins := joinsOf([]describedTable{
		{Name: "A", Columns: []describedColumn{{Name: "cid", KeyField: true, Meaning: &meaningRef{Entity: "Customer", Field: "ID"}}}},
		{Name: "B", Columns: []describedColumn{{Name: "customer", KeyField: true, Meaning: &meaningRef{Entity: "Customer", Field: "ID"}}}},
	}); len(joins) != 1 || joins[0].Provenance != "meaning" {
		t.Fatalf("meaning joins: %+v", joins)
	}
	if entityField(datatug.Entities{nil, {ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "E"}}, Fields: datatug.EntityFields{nil}}}, "E", "x") != nil {
		t.Fatal("no field")
	}
	if toLabelView(meaninglabels.ColumnLabel{}) != nil || len(labelColumns([]semantic.Column{{Name: "a"}})) != 1 {
		t.Fatal("helpers")
	}
}

func TestOwnerLabelChangesNeedAllowWrites(t *testing.T) {
	project, _ := labelsFixture(t)
	labelWritesAllowed = func() bool { return false }
	body := `{` + src(project) + `,"ifRevision":"","change":{"action":"set","target":{"source":"chinook","table":"Cards"},"class":"pii","reason":"r"}}`
	if code, raw := post(t, meaningsLabelsChangeHandler, body, false); code != http.StatusForbidden || !strings.Contains(raw, "--allow-writes") {
		t.Fatalf("%d %s", code, raw)
	}
	propose := `{` + src(project) + `,"ifRevision":"","change":{"action":"propose","target":{"source":"chinook","table":"Cards","column":"Note"},"class":"pii"}}`
	if code, raw := post(t, meaningsLabelsChangeHandler, propose, true); code != http.StatusOK {
		t.Fatalf("a proposal needs no write capability: %d %s", code, raw)
	}
}

func TestLabelWritesAllowedDefaultReadsCapabilities(t *testing.T) {
	if labelWritesAllowed() != api.GetCapabilities().AllowWrites {
		t.Fatal("default must follow the serve capability")
	}
}

func TestAgentTugQLGuardKeepsPolicyDenial(t *testing.T) {
	project := lookupFixture(t, "bob", []string{"support"})
	// support may read Customer only: Invoice is denied by policy, and the
	// guard answers as the policy does, never with a masking code.
	err := agentTugQLGuard(context.Background(), project, "local", "chinook", []byte("from: {name: Invoice}\ncolumns: [{field: InvoiceId}]\n"))
	if err == nil || !strings.Contains(err.Error(), "ACCESS_DENIED") {
		t.Fatalf("want ACCESS_DENIED, got %v", err)
	}
}

func TestAgentRawRefusalSeesEveryTable(t *testing.T) {
	project, dir := labelsFixture(t)
	ctx := context.Background()
	prevList, prevResolve := listSourceObjectsHook, resolveSourceTableHook
	t.Cleanup(func() { listSourceObjectsHook, resolveSourceTableHook = prevList, prevResolve })
	plain := func(context.Context, string) ([]sourceObject, error) {
		return []sourceObject{{Name: "a"}, {Name: "b"}, {Name: "v", View: true, Columns: []string{"x"}}}, nil
	}
	describe := func(failing string) func(context.Context, datatug.ProjectStore, string, string, string, string) (resolvedSource, error) {
		return func(_ context.Context, _ datatug.ProjectStore, _, _, _, table string) (resolvedSource, error) {
			if table == failing {
				return resolvedSource{}, errors.New("cannot describe")
			}
			return resolvedSource{Columns: []semantic.Column{{Name: "id", Type: "int"}}}, nil
		}
	}
	setLabel(t, dir, meaninglabels.Change{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: "chinook", Table: "v"}, Class: meaninglabels.ClassNone, Reason: "reviewed"})
	listSourceObjectsHook, resolveSourceTableHook = plain, describe("")
	if err := agentRawRefusal(ctx, project, "local", "chinook"); err != nil {
		t.Fatalf("unlabelled source: %v", err)
	}
	// a table that cannot be described refuses (fail closed)
	resolveSourceTableHook = describe("b")
	if err := agentRawRefusal(ctx, project, "local", "chinook"); err == nil {
		t.Fatal("an undescribable table must refuse")
	}
	// a view that cannot be described falls back to its column names
	resolveSourceTableHook = describe("v")
	if err := agentRawRefusal(ctx, project, "local", "chinook"); err != nil {
		t.Fatalf("view fallback: %v", err)
	}
	// a suggestion on any table refuses, however many tables come first
	listSourceObjectsHook = func(context.Context, string) ([]sourceObject, error) {
		out := make([]sourceObject, 0, 302)
		for i := 0; i < 301; i++ {
			out = append(out, sourceObject{Name: fmt.Sprintf("t%03d", i)})
		}
		return append(out, sourceObject{Name: "zz_users"}), nil
	}
	resolveSourceTableHook = func(_ context.Context, _ datatug.ProjectStore, _, _, _, table string) (resolvedSource, error) {
		if table == "zz_users" {
			return resolvedSource{Columns: []semantic.Column{{Name: "password_hash", Type: "text"}}}, nil
		}
		return resolvedSource{Columns: []semantic.Column{{Name: "id", Type: "int"}}}, nil
	}
	if err := agentRawRefusal(ctx, project, "local", "chinook"); err == nil {
		t.Fatal("password_hash after 301 tables must refuse")
	}
	listSourceObjectsHook = func(context.Context, string) ([]sourceObject, error) { return nil, errors.New("down") }
	if err := agentRawRefusal(ctx, project, "local", "chinook"); err == nil {
		t.Fatal("an unlistable source must refuse")
	}
	// a stored protected label refuses before anything is listed
	setLabel(t, dir, meaninglabels.Change{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: "chinook", Table: "a"}, Class: meaninglabels.ClassPII, Reason: "r"})
	if err := agentRawRefusal(ctx, project, "local", "chinook"); err == nil {
		t.Fatal("stored label must refuse")
	}
}

func TestRefuseAgentRows(t *testing.T) {
	called := false
	h := refuseAgentRows(func(http.ResponseWriter, *http.Request) { called = true })
	if code, raw := post(t, h, `{}`, true); code != http.StatusForbidden || called || !strings.Contains(raw, meaninglabels.CodeRawRefused) {
		t.Fatalf("agent: %d %s", code, raw)
	}
	post(t, h, `{}`, false)
	if !called {
		t.Fatal("a person passes through")
	}
}

func TestLabelChangeRefusesStaleRevisionBeforeApplying(t *testing.T) {
	project, dir := labelsFixture(t)
	setLabel(t, dir, meaninglabels.Change{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: "chinook", Table: "Cards"}, Class: meaninglabels.ClassPII, Reason: "r"})
	body := `{` + src(project) + `,"ifRevision":"not-current","change":{"action":"set","target":{"source":"chinook","table":"Invoice"},"class":"pii","reason":"r"}}`
	if code, raw := post(t, meaningsLabelsChangeHandler, body, false); code != http.StatusConflict {
		t.Fatalf("%d %s", code, raw)
	}
}

func TestFindMultiWord(t *testing.T) {
	project, _ := labelsFixture(t)
	code, raw := post(t, meaningsFindHandler, `{`+src(project)+`,"text":"customer email"}`, true)
	if code != http.StatusOK || !strings.Contains(raw, `"table":"Customer","column":"Email"`) || strings.Contains(raw, `"column":"Country"`) {
		t.Fatalf("%d %s", code, raw)
	}
}

type failingSave struct{ meaninglabelsfile.Store }

func (failingSave) Save(context.Context, meaninglabels.Document, string) error {
	return errors.New("disk full")
}

func TestLabelChangeSaveFailureAndNonSQLRaw(t *testing.T) {
	project, dir := labelsFixture(t)
	prevStore, prevResolve := labelsStoreFor, apiResolveSource
	t.Cleanup(func() { labelsStoreFor, apiResolveSource = prevStore, prevResolve })
	labelsStoreFor = func(string) labelStore { return failingSave{meaninglabelsfile.Store{Dir: dir}} }
	body := `{` + src(project) + `,"ifRevision":"","change":{"action":"set","target":{"source":"chinook","table":"Cards"},"class":"pii","reason":"r"}}`
	if code, _ := post(t, meaningsLabelsChangeHandler, body, false); code != http.StatusInternalServerError {
		t.Fatalf("save failure: %d", code)
	}
	labelsStoreFor = prevStore
	apiResolveSource = func(context.Context, datatug.ProjectStore, string, string, string) (api.ResolvedSource, error) {
		return api.ResolvedSource{ID: "chinook", Kind: api.SourceKindHTTP}, nil
	}
	if err := agentRawRefusal(context.Background(), project, "local", "chinook"); err == nil {
		t.Fatal("a non-SQL source must refuse")
	}
}

func TestCollectionOf(t *testing.T) {
	ref := dal.NewRootCollectionRef("a", "")
	if c, ok := collectionOf(&ref); !ok || c.Name() != "a" {
		t.Fatal("pointer ref")
	}
	if _, ok := collectionOf(nil); ok {
		t.Fatal("nil source")
	}
}

func TestAgentQueryGuardShapes(t *testing.T) {
	project, _ := labelsFixture(t)
	ctx := context.Background()
	q := dal.NewQueryBuilder(dal.From(notACollection{})).SelectColumns(dal.Column{Expression: dal.Field("Id")})
	prevExec := secureExecutorHook
	secureExecutorHook = func() (*secureread.Executor, bool) { return nil, false }
	err := agentQueryGuard(ctx, project, "local", "chinook", q)
	secureExecutorHook = prevExec
	if err == nil {
		t.Fatal("a non-collection base must be refused on a labelled source")
	}
	base := dal.NewRootCollectionRef("Album", "")
	if err := agentJoinGuard(ctx, "nope", "local", "chinook", base, nil); err == nil {
		t.Fatal("unknown project")
	}
	if err := agentJoinGuard(ctx, project, "local", "chinook", base, []dal.JoinedSource{{RecordsetSource: notACollection{}}}); err == nil {
		t.Fatal("a joined non-collection must refuse")
	}
}
