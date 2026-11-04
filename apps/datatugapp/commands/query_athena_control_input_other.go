//go:build !linux && !darwin

package commands

import (
	"context"
	"os"
)

func athenaNewNativeControlReader(ctx context.Context, file *os.File) (reader athenaOwnedControlReader, err error) {
	if ctx == nil {
		return nil, errAthenaControl
	}
	defer func() {
		if ctxErr := athenaFresh(ctx); ctxErr != nil {
			reader = nil
			err = ctxErr
			return
		}
		if err != nil {
			reader = nil
			err = errAthenaControl
		}
	}()
	if err = athenaFresh(ctx); err != nil {
		return nil, err
	}
	if file == nil {
		return nil, errAthenaControl
	}
	return nil, errAthenaControl
}
