package daemon

import (
	"regexp"
	"strings"
	"testing"
)

var testID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`).MatchString

func TestBuildResultLink(t *testing.T) {
	st := Status{UIBase: "https://datatug.app/store/http-127.0.0.1:8989", Running: true}
	link, err := BuildResultLink(st, ResultLinkRequest{ID: "t4", View: "sources", Row: 3, Exists: true, Code: "c d"}, testID)
	if err != nil || link != (ResultLink{URL: st.UIBase + "/result/t4?view=sources&row=3#code=c%20d", Kind: KindQueryResult, Running: true, UISupport: UISupportPending, ResultExists: true}) {
		t.Fatalf("%+v %v", link, err)
	}
	link, err = BuildResultLink(st, ResultLinkRequest{ID: "t4", Exists: true}, testID)
	if err != nil || link.URL != st.UIBase+"/result/t4?view=table" {
		t.Fatalf("%+v %v", link, err)
	}
	link, err = BuildResultLink(st, ResultLinkRequest{ID: "gone"}, testID)
	if err != nil || link.URL != "" || link.ResultExists {
		t.Fatalf("a result that is not there gets no link: %+v %v", link, err)
	}
	for name, req := range map[string]ResultLinkRequest{
		"bad id":   {ID: "../x", Exists: true},
		"bad view": {ID: "t4", View: "chart", Exists: true},
		"bad row":  {ID: "t4", Row: -1, Exists: true},
		"big row":  {ID: "t4", Row: MaxResultRow + 1, Exists: true},
	} {
		if _, err := BuildResultLink(st, req, testID); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	long := Status{UIBase: "https://" + strings.Repeat("a", MaxLinkLength)}
	if _, err := BuildResultLink(long, ResultLinkRequest{ID: "t4", Exists: true}, testID); err == nil {
		t.Fatal("an over-long link was built")
	}
	if uiSupport[KindQueryResult] != UISupportPending {
		t.Fatal("query-result is pending until the web route ships")
	}
}
