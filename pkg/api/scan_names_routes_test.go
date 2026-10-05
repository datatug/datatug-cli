package api

import (
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/internal/sourcecases"
	"github.com/stretchr/testify/assert"
)

// There is one definition of a plain name. The routes of serve refuse an environment or a
// catalog that is not one (ValidateIdentifier), and a scan writes the environment and the
// database as folders of the project, adding to that only what a folder needs
// (CheckScanName): so a project that a scan writes can always be browsed. Every name that the
// scan accepts for --env, --db or --dbmodel is one that every route accepts, and a name the
// routes refuse is one the scan refuses.
func TestEveryNameTheScanAcceptsIsOneTheRoutesOfServeAccept(t *testing.T) {
	names := []string{
		"shop", "Shop_2.0-x", "LOCAL", "UAT", "café", "données", "日本語", "a", "1", "a.b", "a-b", "a_b", "1st",
		"con", "CON", "nul.txt", "com1", "lpt9.log", "shop.", "x ", " x", "", ".", "..", ".hidden", "_x", "-x", "a b", "a/b", "a\\b", "a:b",
		"a*b", "a?b", `a"b`, "a<b", "a>b", "a|b", "a\x00b", "a\nb", "a%2Fb", "a%b", "C:", "C:x", "<source id not shown>", "<unparsable source>",
		strings.Repeat("a", 128), strings.Repeat("a", 129), strings.Repeat("é", 128), strings.Repeat("é", 129),
		"https://u:p@h/db1", "postgres://alice:pw@db.example.com/shop", "env:DATATUG_X", "sqlite:///tmp/x.db",
	}
	for _, unsafe := range sourcecases.UnsafeIdentifiers() {
		names = append(names, unsafe.ID)
	}

	accepted := 0
	for _, name := range names {
		for _, flag := range []string{"--env", "--db", "--dbmodel"} {
			scanErr := CheckScanName(flag, name)
			if scanErr == nil {
				accepted++
				assert.NoError(t, ValidateIdentifier("environment", name), "the scan accepts %q for %s, and a route refuses it", name, flag)
				assert.NoError(t, ValidateIdentifier("catalog", name), "the scan accepts %q for %s, and a route refuses it", name, flag)
				assert.NoError(t, ValidateCatalogIdentifiers(name, name), "the scan accepts %q for %s, and a route refuses it", name, flag)
			}
			if ValidateIdentifier("catalog", name) != nil {
				assert.Error(t, scanErr, "a route refuses %q, and the scan accepts it for %s", name, flag)
			}
		}
	}
	if accepted < 10 {
		t.Fatalf("the scan accepted only %d of the names: the test does not reach the accepting side", accepted)
	}
}
