package dbcopy

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/internal/sourcecases"
)

// TestProperty_NothingThisPackageShowsHoldsASecret is the DT-0C acceptance
// property at the layer every command path shares. For every generated source
// string (every scheme the CLI knows, in upper, lower and mixed case, bare and
// wrapped, with generated user names, passwords and tokens in userinfo, in the
// query string, in the fragment and in the position a parser can misread as
// userinfo, including empty user names and passwords with spaces, slashes,
// at signs and digits only) no secret of four or more characters is in
//
//   - SourceDisplay and SourceIDDisplay of the string,
//   - the error Parse returns, or, when Parse accepts it, in the ref's Raw and in
//     every way a ref is printed (%v, %+v, %#v, String, GoString, Display),
//   - the error Open, OpenFailure and CheckFile return for the accepted ref, even
//     when the driver quotes the whole DSN it was given,
//   - the error CheckSourceFile returns for the string and for the ref's Path,
//   - the error Parse returns for an "env:NAME" source whose variable holds it.
//
// It reads what this package returns itself, with no redactor in between: no
// RedactText, RedactSourceURL or RedactError is called here.
func TestProperty_NothingThisPackageShowsHoldsASecret(t *testing.T) {
	cases := sourcecases.All()
	accepted := 0
	for _, c := range cases {
		texts := []string{SourceDisplay(c.Source), SourceIDDisplay(c.Source)}
		add := func(err error) {
			if err != nil {
				texts = append(texts, err.Error())
			}
		}
		add(CheckSourceFile(c.Source))

		ref, err := Parse(c.Source)
		add(err)
		if err == nil {
			accepted++
			texts = append(texts,
				ref.Raw, ref.String(), ref.GoString(), ref.Display(),
				fmt.Sprint(ref), fmt.Sprintf("%v %+v %#v", ref, &ref, ref),
			)
			add(CheckSourceFile(ref.Path))
			add(ref.CheckFile())
			_, openErr := ref.Open(context.Background())
			add(openErr)
			add(ref.OpenFailure(quotingDriverError(ref.Path)))
			add(ref.OpenFailure(fmt.Errorf("dial %q: %w", c.Source, ErrSourceFileMissing)))
		}

		_, err = parseSource("env:SHOP_PG_URL", fakeEnv(map[string]string{"SHOP_PG_URL": c.Source}))
		add(err)

		if leaked := sourcecases.Leaks(c, texts...); len(leaked) > 0 {
			t.Errorf("%s\n  leaked %q in:\n    %s", c.Name, leaked, strings.Join(texts, "\n    "))
		}
	}
	if accepted == 0 || accepted == len(cases) {
		t.Fatalf("Parse accepted %d of %d generated sources: the generator no longer covers both outcomes", accepted, len(cases))
	}
}

// Harmless URLs and Windows drive paths are shown as they are, apart from the
// userinfo and the query string that are dropped.
func TestSourceDisplay_HarmlessSourcesAreShownUnmodified(t *testing.T) {
	t.Parallel()
	for _, source := range []string{
		"sqlite:///tmp/foo.db",
		"sqlite://./rel/foo.db",
		"sqlite://C:\\data\\x.db",
		"ingitdb://./project",
		"ingitdb:///abs/project",
		"http://./demo-project-1",
		"https:///Users/alex/team@work/project",
		"http://C:\\work\\a@b",
		"https://C:/work/a@b/proj",
		"openvaultdb:///tmp/c.json",
		"postgres://db.example.com:5432/shop",
		"postgres://[::1]:5432/shop",
		"postgresql://db.example.com/shop",
		"env:SHOP_PG_URL",
	} {
		if shown := SourceDisplay(source); shown != source {
			t.Errorf("SourceDisplay(%q) = %q, want it unchanged", source, shown)
		}
	}
	for source, want := range map[string]string{
		"postgres://alice:s3cret@db.example.com:5432/shop?sslmode=require": "postgres://db.example.com:5432/shop",
		"sqlite:///x.db?mode=ro":         "sqlite:///x.db",
		"POSTGRES://db.example.com/shop": "postgres://db.example.com/shop",
	} {
		if shown := SourceDisplay(source); shown != want {
			t.Errorf("SourceDisplay(%q) = %q, want %q", source, shown, want)
		}
	}
}
