package commands

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func TestReadServeFlagsRejectsMissingSecurityAndStorageConfiguration(t *testing.T) {
	// A command assembled without one of these flags is broken wiring, not
	// permission to silently use a default owner, root or execution capability.
	for _, missing := range []string{
		serveProjectsRootFlag, serveAsFlag, serveRoleFlag, serveGroupFlag,
		serveAllowWritesFlag, serveAllowOpaqueSQLFlag, serveAllowLiveConnsFlag,
		serveHTTPOfflineFlag, serveExecTimeoutFlag, serveEvidenceDirFlag,
		serveSnapshotByteCapFlag, serveSnapshotRetentionFlag,
		serveDetectiveAIProfileFlag, serveIdleTimeoutFlag,
	} {
		t.Run(missing, func(t *testing.T) {
			registered := serveCommandArgs()
			command := &cobra.Command{}
			registered.Flags().VisitAll(func(flag *pflag.Flag) {
				if flag.Name != missing {
					command.Flags().AddFlag(flag)
				}
			})
			if _, err := readServeFlags(command); err == nil || !strings.Contains(err.Error(), missing) {
				t.Fatalf("missing %q was accepted: %v", missing, err)
			}
		})
	}
}
