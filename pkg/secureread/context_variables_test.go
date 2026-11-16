package secureread

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-core/pkg/datatug"
)

// Every TugQL entry point refuses a document using a context variable, as a
// "{name}" source-path segment or a param reference, before a source opens.
func TestExecutorRefusesContextVariablesBeforeOpeningASource(t *testing.T) {
	original := openSourceFn
	t.Cleanup(func() { openSourceFn = original })
	opened := 0
	openSourceFn = func(context.Context, string, bool, int) (dal.DB, func(), error) {
		opened++
		return nil, func() {}, errors.New("no source in this test")
	}
	executor := NewExecutor(Session{Unrestricted: true})
	ctx := context.Background()
	const source = "sqlite:///never-opened.sqlite"
	for variable, document := range map[string][]byte{
		"SneatUserPersonalSpaceID": []byte("from: {name: \"spaces/{SneatUserPersonalSpaceID}/ext/datatug/projects\"}\nlimit: 5\n"),
		"SneatUserID":              []byte("from: {name: users}\nwhere: {op: '==', left: {field: id}, right: {param: SneatUserID}}\nlimit: 5\n"),
	} {
		urls := map[string]string{"main": source}
		runs := map[string]func() error{
			"RunTugQL": func() error { _, err := executor.RunTugQL(ctx, source, document, nil); return err },
			"RunFederatedTugQL": func() error {
				_, err := executor.RunFederatedTugQL(ctx, document, urls, nil)
				return err
			},
			"StreamFederatedTugQL": func() error {
				_, err := executor.StreamFederatedTugQL(ctx, document, urls, nil)
				return err
			},
			"RunRecursiveTugQL": func() error { _, err := executor.RunRecursiveTugQL(ctx, source, document, nil); return err },
			"RunExactAggregateTugQL": func() error {
				_, err := executor.RunExactAggregateTugQL(ctx, source, document, document, nil, nil)
				return err
			},
		}
		for name, run := range runs {
			err := run()
			if !errors.Is(err, datatug.ErrContextVariableNotBound) || !strings.Contains(err.Error(), "context variable "+variable+",") {
				t.Errorf("%s with %s: %v, want a context-variable refusal", name, variable, err)
			}
		}
	}
	if opened != 0 {
		t.Fatalf("a source was opened %d times", opened)
	}
}
