package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/datatug"
)

// --env takes the ID of an environment, and a source string can be typed there.
// Every message of resolveQueryDatabase names the environment only when it is a
// plain name, and carries the loader's error, which quotes the path the ID
// became, only then.
func TestResolveQueryDatabase_NamesTheEnvironmentOnlyWhenItIsAPlainName(t *testing.T) {
	const typed = "sqlite://carol:pw-Zk39x@host.example/x"
	loaderError := errors.New("open /project/environments/entry: no such file or directory")
	catalog := func(id string) *datatug.DbCatalog {
		c := &datatug.DbCatalog{}
		c.ID = id
		return c
	}
	for _, tc := range []struct {
		name       string
		store      *covCStore
		wantPhrase string
		wantCause  bool
	}{
		{"the catalogs cannot be listed", &covCStore{catalogsErr: loaderError}, "load database catalogs for environment", true},
		{"no catalog is configured", &covCStore{}, "has no database catalogs configured", false},
		{"several catalogs and no choice", &covCStore{catalogs: datatug.DbCatalogs{catalog("one"), catalog("two")}}, "does not declare which one to use", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, plainErr := resolveQueryDatabase(context.Background(), tc.store, "local", &datatug.QueryDef{ID: "saved"})
			if plainErr == nil || !strings.Contains(plainErr.Error(), tc.wantPhrase) || !strings.Contains(plainErr.Error(), `environment "local"`) {
				t.Fatalf("a plain environment is named: %v", plainErr)
			}
			if errors.Is(plainErr, loaderError) != tc.wantCause {
				t.Errorf("the loader's error is carried = %v, want %v: %v", !tc.wantCause, tc.wantCause, plainErr)
			}

			_, err := resolveQueryDatabase(context.Background(), tc.store, typed, &datatug.QueryDef{ID: "saved"})
			if err == nil || !strings.Contains(err.Error(), tc.wantPhrase) {
				t.Fatalf("error = %v, want one that says %q", err, tc.wantPhrase)
			}
			for _, part := range []string{"carol", "pw-Zk39x", "host.example", "/project/environments"} {
				if strings.Contains(err.Error(), part) {
					t.Errorf("the message shows %q of the environment or of the loader's error: %v", part, err)
				}
			}
			if !strings.Contains(err.Error(), `environment "<source id not shown>"`) {
				t.Errorf("a source string given as the environment is not shown as one: %v", err)
			}
			if errors.Is(err, loaderError) {
				t.Errorf("the loader's error, which quotes the path, is carried for an environment that is not a plain name: %v", err)
			}
		})
	}
}

// recordingT is the part of testing.T that requirePostgresScanUnavailable uses,
// recording instead of stopping.
type recordingT struct{ failures []string }

func (*recordingT) Helper() {}
func (r *recordingT) Fatalf(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

// The command property stops at once when a datatug-core upgrade lets the scan
// reach a server; while the scan is not available it says nothing.
func TestRequirePostgresScanUnavailable(t *testing.T) {
	refused := &recordingT{}
	requirePostgresScanUnavailable(refused, func() error { return errors.New("not available") })
	if len(refused.failures) != 0 {
		t.Fatalf("a scan that is not available failed the guard: %v", refused.failures)
	}
	open := &recordingT{}
	requirePostgresScanUnavailable(open, func() error { return nil })
	if len(open.failures) != 1 || !strings.Contains(open.failures[0], "would open the generated hosts") {
		t.Fatalf("a scan that is available must fail the guard loudly: %v", open.failures)
	}
	// The release this branch is built on does not scan PostgreSQL.
	actual := &recordingT{}
	requirePostgresScanUnavailable(actual, api.CheckPostgresScanAvailable)
	if len(actual.failures) != 0 {
		t.Fatalf("api.CheckPostgresScanAvailable() is nil: %v", actual.failures)
	}
}
