package dtproject

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/cloudproject"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/widgets"
)

func salesProject() cloudproject.Project {
	return cloudproject.Project{
		Ref:          cloudproject.Ref{SpaceID: "acme", ProjectID: "sales"},
		ID:           "sales",
		Title:        "Sales",
		Environments: []cloudproject.Item{{ID: "dev", Title: "Development"}},
		Databases:    []cloudproject.Item{{ID: "warehouse", Title: "Warehouse"}},
		Queries:      []cloudproject.Item{{ID: "reports/top", Title: "Top"}},
	}
}

var salesRef = dtconfig.ProjectRef{
	ID:    "datatug.app/space/acme/sales",
	Url:   "https://datatug.app/space/acme/store/firestore/project/sales",
	Title: "Sales",
}

func TestOpenCloudProjectShowsItAndRemembersIt(t *testing.T) {
	w := newLinkWorld(t)
	push := only[nav.PushMsg](t, runCmd(OpenCloudProject(salesProject(), []string{"query", "reports", "top"})))
	if push.Page.Title != "Sales" || contentTitle(push.Page) != "Queries (1)" {
		t.Fatalf("page %q, content %q", push.Page.Title, contentTitle(push.Page))
	}
	if len(w.added) != 1 || w.added[0] != salesRef {
		t.Fatalf("added = %+v", w.added)
	}
	if len(w.bumped) != 1 || w.bumped[0] != salesRef.ID || len(w.opened) != 0 {
		t.Fatalf("bumped %v, opened %v", w.bumped, w.opened)
	}
}

func TestOpenCloudProjectOfAKnownProjectAtAPageWithoutScreen(t *testing.T) {
	known := salesRef
	w := newLinkWorld(t, nil, &known)
	msgs := runCmd(OpenCloudProject(salesProject(), []string{"chat"}))
	push := only[nav.PushMsg](t, msgs)
	if contentTitle(push.Page) != "Project: Sales" {
		t.Fatalf("content %q", contentTitle(push.Page))
	}
	view := push.Page.Content.(textScreen).text
	if !strings.Contains(view, ReadOnlyHint) || !strings.Contains(view, salesRef.Url) {
		t.Errorf("summary = %q", view)
	}
	if alert := only[nav.AlertMsg](t, msgs); alert.Message != noPageScreenNotice {
		t.Errorf("alert = %q", alert.Message)
	}
	if len(w.added) != 0 || len(w.bumped) != 1 {
		t.Fatalf("added %v, bumped %v", w.added, w.bumped)
	}
}

func TestOpenCloudProjectSettingsFailures(t *testing.T) {
	newLinkWorld(t)
	stub(t, &readSettings, func() (dtconfig.Settings, error) { return dtconfig.Settings{}, errors.New("bad yaml") })
	if msgs := runCmd(OpenCloudProject(salesProject(), nil)); len(msgs) == 0 {
		t.Fatal("no error shown")
	}
	stub(t, &readSettings, func() (dtconfig.Settings, error) { return dtconfig.Settings{}, nil })
	stub(t, &addProjectToSettings, func(dtconfig.ProjectRef) error { return errors.New("read-only disk") })
	for _, m := range runCmd(OpenCloudProject(salesProject(), nil)) {
		if _, ok := m.(nav.PushMsg); ok {
			t.Fatal("project opened although it could not be saved")
		}
	}
}

func TestCloudProjectDataListErrorsUseCloudLines(t *testing.T) {
	stub(t, &CloudErrorLine, func(err error) string { return "line: " + err.Error() })
	p := salesProject()
	p.EnvsErr, p.DBsErr, p.QueriesErr = errors.New("e"), errors.New("d"), errors.New("q")
	data := cloudProjectData(p)
	if data.envsErr.Error() != "line: e" || data.dbsErr.Error() != "line: d" || data.queriesErr.Error() != "line: q" {
		t.Fatalf("errors = %v %v %v", data.envsErr, data.dbsErr, data.queriesErr)
	}
	if !data.readOnly || !data.hasQueries || len(data.environments) != 1 || len(data.databases) != 1 {
		t.Fatalf("data = %+v", data)
	}
	if got := newSectionScreen(data, sectionQueries).(itemList).err; got == nil || got.Error() != "line: q" {
		t.Errorf("queries panel error = %v", got)
	}
}

func TestQueryItems(t *testing.T) {
	items := queryItems([]cloudproject.Item{{ID: "a", Title: "A"}})
	if len(items) != 1 || items[0] != (widgets.MenuItem{ID: "a", Label: "a", Detail: "A", Shortcut: 'a'}) {
		t.Fatalf("items = %+v", items)
	}
}

func TestIsCloudRef(t *testing.T) {
	if !IsCloudRef(salesRef) {
		t.Fatal("cloud ref not recognised")
	}
	for _, ref := range []dtconfig.ProjectRef{
		{ID: "x", Path: "~/p", Url: salesRef.Url},
		{ID: "github.com/x/y", Url: "github.com/x/y"},
	} {
		if IsCloudRef(ref) {
			t.Errorf("%+v is not a cloud ref", ref)
		}
	}
}

func TestOpenCloudRefReadsAgainAndOpens(t *testing.T) {
	w := newLinkWorld(t, &salesRef)
	var asked []cloudproject.Ref
	stub(t, &CloudLoader, func(_ context.Context, ref cloudproject.Ref) (cloudproject.Project, error) {
		asked = append(asked, ref)
		return salesProject(), nil
	})
	push := only[nav.PushMsg](t, runCmd(selectProjectNode(widgets.TreeNode{Ref: &salesRef})))
	if push.Page.Title != "Sales" || contentTitle(push.Page) != "Project: Sales" {
		t.Fatalf("page %q, content %q", push.Page.Title, contentTitle(push.Page))
	}
	if len(asked) != 1 || asked[0] != (cloudproject.Ref{SpaceID: "acme", ProjectID: "sales"}) {
		t.Fatalf("asked = %+v", asked)
	}
	if len(w.opened) != 0 {
		t.Fatalf("a folder store was opened: %v", w.opened)
	}
}

func TestOpenCloudRefFailures(t *testing.T) {
	newLinkWorld(t)
	alertOf := func(ref dtconfig.ProjectRef) string {
		return only[nav.AlertMsg](t, runCmd(openCloudRef(ref))).Message
	}
	if got := alertOf(dtconfig.ProjectRef{ID: "x", Url: "https://datatug.app/store/firestore/project/p"}); !strings.Contains(got, "Not a DataTug Cloud project address") {
		t.Errorf("personal: %q", got)
	}
	if got := alertOf(dtconfig.ProjectRef{ID: "x", Url: "https://datatug.app/space/" + strings.Repeat("s", 129) + "/store/firestore/project/p"}); !strings.Contains(got, "space id") {
		t.Errorf("bad id: %q", got)
	}
	stub(t, &CloudLoader, nil)
	if got := alertOf(salesRef); got != cloudDeepLinkNotice {
		t.Errorf("no loader: %q", got)
	}
	stub(t, &CloudLoader, func(context.Context, cloudproject.Ref) (cloudproject.Project, error) {
		return cloudproject.Project{}, cloudproject.ErrForbidden
	})
	stub(t, &CloudErrorLine, func(err error) string { return "denied line" })
	if got := alertOf(salesRef); got != "denied line" {
		t.Errorf("forbidden: %q", got)
	}
}

func TestDefaultCloudErrorLine(t *testing.T) {
	if got := CloudErrorLine(errors.New("x")); got != "x" {
		t.Fatalf("got %q", got)
	}
}
