package dbmirror

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/ingitdb/ingitdb-go/ingitdb"
	"google.golang.org/genproto/googleapis/type/latlng"
)

func TestJSONValueKeepsEveryFirestoreType(t *testing.T) {
	at := time.Date(2026, 11, 20, 8, 0, 0, 123456000, time.FixedZone("x", 3600))
	got, err := jsonValue(map[string]any{
		"null": nil, "bool": true, "string": "s", "int": int64(-7), "double": 1.5,
		"nan": math.NaN(), "inf": math.Inf(1), "-inf": math.Inf(-1),
		"time":  at,
		"bytes": []byte("hello"),
		"ref":   &firestore.DocumentRef{Path: "projects/p/databases/(default)/documents/a/b"},
		"geo":   &latlng.LatLng{Latitude: 1.5, Longitude: -2},
		"v64":   firestore.Vector64{1, 2.5},
		"v32":   firestore.Vector32{3},
		"list":  []any{int64(1), map[string]any{"x": "y"}},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"null": nil, "bool": true, "string": "s", "int": int64(-7), "double": 1.5,
		"nan": map[string]any{"$float": "NaN"}, "inf": map[string]any{"$float": "Infinity"}, "-inf": map[string]any{"$float": "-Infinity"},
		"time":  map[string]any{"$time": "2026-11-20T07:00:00.123456Z"},
		"bytes": map[string]any{"$bytes": "aGVsbG8="},
		"ref":   map[string]any{"$ref": "a/b"},
		"geo":   map[string]any{"$geo": map[string]any{"latitude": 1.5, "longitude": -2.0}},
		"v64":   map[string]any{"$vector": []any{1.0, 2.5}},
		"v32":   map[string]any{"$vector": []any{3.0}},
		"list":  []any{int64(1), map[string]any{"x": "y"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("\n got %#v\nwant %#v", got, want)
	}
	if relativePath("a/b") != "a/b" {
		t.Error("a path without a database prefix is kept")
	}
}

func TestJSONValueRefusesAnUnknownType(t *testing.T) {
	for _, value := range []any{
		map[string]any{"a": map[string]any{"b": struct{}{}}},
		map[string]any{"a": []any{int64(1), struct{}{}}},
	} {
		_, err := jsonValue(value, "")
		if err == nil || !strings.Contains(err.Error(), "struct {}") || !strings.Contains(err.Error(), `"a.`) {
			t.Errorf("%v", err)
		}
	}
}

func TestEncodeRecordIsCanonical(t *testing.T) {
	got := string(encodeRecord(map[string]any{"b": []any{}, "a": map[string]any{}, "c": "<&>", "d": 1e21, "e": 2.0, "f": int64(3), "g": false, "h": nil, "i": []any{"x", 1.25}}))
	want := `{
  "a": {},
  "b": [],
  "c": "<&>",
  "d": 1e+21,
  "e": 2.0,
  "f": 3,
  "g": false,
  "h": null,
  "i": [
    "x",
    1.25
  ]
}
`
	if got != want {
		t.Errorf("got\n%s", got)
	}
	if decoded := decodeRecord([]byte(got)); decoded["e"] != 2.0 || decoded["c"] != "<&>" {
		t.Errorf("%v", decoded)
	}
}

func TestApplyMasks(t *testing.T) {
	data := map[string]any{
		"email": "a@example.com",
		"age":   int64(4),
		"members": []any{
			map[string]any{"name": "A", "id": "1"},
			map[string]any{"id": "2"},
			"text",
		},
		"byID": map[string]any{"x": map[string]any{"phone": "1"}, "y": map[string]any{"phone": int64(2)}},
		"list": []any{"a", "b"},
	}
	hit := applyMasks(data, []string{"email", "age", "members.*.name", "byID.*.phone", "list.1", "nothing", "members.9.name"})
	if !reflect.DeepEqual(hit, []string{"email", "age", "members.*.name", "byID.*.phone", "list.1"}) {
		t.Errorf("hit %v", hit)
	}
	want := map[string]any{
		"email":   "***",
		"members": []any{map[string]any{"name": "***", "id": "1"}, map[string]any{"id": "2"}, "text"},
		"byID":    map[string]any{"x": map[string]any{"phone": "***"}, "y": map[string]any{}},
		"list":    []any{"a", "***"},
	}
	if !reflect.DeepEqual(data, want) {
		t.Errorf("%#v", data)
	}
	items := []any{map[string]any{"a": int64(1)}}
	if !maskPath(items, []string{"*"}) || items[0] != nil {
		t.Errorf("%#v", items)
	}
}

func TestConvertRecordWritesTimesByColumnType(t *testing.T) {
	at := time.Date(2026, 11, 20, 8, 0, 0, 0, time.FixedZone("x", 3600))
	def := &ingitdb.CollectionDef{Columns: map[string]*ingitdb.ColumnDef{
		"at": {Type: ingitdb.ColumnTypeDateTime}, "ats": {Type: "[]datetime"}, "loose": {Type: ingitdb.ColumnTypeAny},
	}}
	got, err := convertRecord(map[string]any{"at": at, "ats": []any{at, "text"}, "loose": at, "undeclared": at,
		"tagLike": map[string]any{"$ref": "x"}, "two": map[string]any{"$ref": "x", "b": 1.0}}, def)
	if err != nil {
		t.Fatal(err)
	}
	text := "2026-11-20T07:00:00.000000Z"
	want := map[string]any{"at": text, "ats": []any{text, "text"}, "loose": map[string]any{"$time": text}, "undeclared": map[string]any{"$time": text},
		"tagLike": map[string]any{"$map": map[string]any{"$ref": "x"}}, "two": map[string]any{"$ref": "x", "b": 1.0}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("\n got %#v\nwant %#v", got, want)
	}
	for _, data := range []map[string]any{{"at": struct{}{}}, {"ats": []any{struct{}{}}}} {
		if _, err := convertRecord(data, def); err == nil || !strings.Contains(err.Error(), "struct {}") {
			t.Error(err)
		}
	}
}

func TestTemporalProblemsNameNoValue(t *testing.T) {
	def := &ingitdb.CollectionDef{Columns: map[string]*ingitdb.ColumnDef{
		"at": {Type: ingitdb.ColumnTypeDateTime}, "day": {Type: ingitdb.ColumnTypeDate}, "hour": {Type: ingitdb.ColumnTypeTime}, "s": {Type: ingitdb.ColumnTypeString},
	}}
	if got := temporalProblems(def, map[string]any{"at": "2026-11-20T07:00:00.000000Z", "day": "2026-11-20", "hour": "07:30", "s": "x"}); len(got) != 0 {
		t.Error(got)
	}
	if got := temporalProblems(def, map[string]any{"hour": "07:30:15", "at": map[string]any{"$time": "x"}}); len(got) != 0 {
		t.Error(got)
	}
	got := temporalProblems(def, map[string]any{"at": "SECRET", "day": "2026-13-01", "hour": "25:00"})
	if len(got) != 3 || strings.Contains(strings.Join(got, ";"), "SECRET") || !strings.Contains(got[0], `field "at": not a valid datetime`) {
		t.Error(got)
	}
}
