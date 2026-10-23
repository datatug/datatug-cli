package strictjson

import (
	"os"
	"testing"

	"github.com/datatug/datatug-cli/internal/hermetictest"
)

func TestMain(m *testing.M) {
	os.Exit(hermetictest.Main(m))
}

func TestDecodeObject(t *testing.T) {
	for _, bad := range []string{``, `[]`, `null`, `{} {}`, `{1:2}`, `{"a":[1,}`, `{"a":{"b":1,"b":2}}`, `{"a":1,"a":2}`, `{"a":1`} {
		if DecodeObject([]byte(bad), nil) == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if err := DecodeObject([]byte(`{"a":1}`), nil); err != nil {
		t.Fatal(err)
	}
	var target struct {
		A []any `json:"a"`
		C struct {
			B int `json:"b"`
		} `json:"c"`
	}
	if err := DecodeObject([]byte(`{"a":[{"b":1},2],"c":{"b":3}}`), &target); err != nil || target.C.B != 3 || len(target.A) != 2 {
		t.Fatalf("target=%+v err=%v", target, err)
	}
	if err := DecodeObject([]byte(`{"c":"not an object"}`), &target); err == nil {
		t.Fatal("type mismatch accepted")
	}
}
