//go:build windows

package dbcopy

import "golang.org/x/sys/windows"

func publishExportExclusive(stage, destination string) error {
	from, err := windows.UTF16PtrFromString(stage)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	// A zero flag set moves within the same volume and refuses to replace an
	// existing target. Staging is created beside the destination.
	return windows.MoveFileEx(from, to, 0)
}
