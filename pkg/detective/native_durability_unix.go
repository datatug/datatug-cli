//go:build !windows

package detective

import (
	"errors"
	"os"
)

// A synced temp file and rename alone do not durably publish the directory
// entry. Native admission fails closed until file and directory sync complete.
func syncPrivateDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
func nativeBridgePlatformAdmission() error { return nil }
