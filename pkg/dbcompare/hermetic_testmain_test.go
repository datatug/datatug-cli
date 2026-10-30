package dbcompare

import (
	"os"
	"testing"

	"github.com/datatug/datatug-cli/internal/hermetictest"
)

// Native inGitDB fixtures use os.UserCacheDir for their lock files. Redirect
// user directories for this package's test binary so comparison tests remain
// hermetic under both the standalone and repository-wide hermetic suites.
func TestMain(m *testing.M) {
	os.Exit(hermetictest.Main(m))
}
