//go:build !darwin && !linux && !windows

package dbcopy

import "fmt"

func publishExportExclusive(_, _ string) error {
	return fmt.Errorf("exclusive atomic export publication is unavailable on this platform")
}
