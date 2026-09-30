package datatugui

import (
	"errors"
	"strings"
	"testing"

	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/uitest"
)

func TestScreenOpenedRecordsAndPersists(t *testing.T) {
	r := fakeSeams(t)
	if msg := ScreenOpened("viewers/sql", "SQL")(); msg != nil {
		t.Errorf("message = %#v", msg)
	}
	if len(r.opened) != 1 || r.opened[0] != "viewers/sql|SQL" || r.saved[0] != "viewers/sql" {
		t.Errorf("opened=%v saved=%v", r.opened, r.saved)
	}
}

func TestDrillPushesAPage(t *testing.T) {
	msgs := uitest.Msgs(Drill("Detail", nav.Static("t", "x")))
	push, ok := msgs[0].(nav.PushMsg)
	if !ok || push.Page.Title != "Detail" || push.Page.Content == nil || push.Page.Menu != nil {
		t.Errorf("messages = %#v", msgs)
	}
}

func TestReportError(t *testing.T) {
	r := fakeSeams(t)
	if ReportError("load", nil) != nil {
		t.Error("no error, no command")
	}
	cause := errors.New("disk full")
	msgs := uitest.Msgs(ReportError("save the project", cause))
	shown, ok := msgs[0].(nav.ErrorMsg)
	if !ok || !errors.Is(shown.Err, cause) || !strings.Contains(shown.Err.Error(), "save the project") {
		t.Errorf("messages = %#v", msgs)
	}
	if len(r.logged) != 1 || !strings.Contains(r.logged[0], "disk full") {
		t.Errorf("logged = %v", r.logged)
	}
}
